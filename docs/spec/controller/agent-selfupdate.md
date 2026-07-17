# Signed agent self-update (canary-then-fleet)

This document defines how a controller-managed node agent (`internal/agent`, `cmd/agent`) replaces
its OWN binary with a newer version, verified against a controller-signed pin, rolled out a canary
subset first, with a swap that cannot brick a node. It is the beta.2 half of the
`signed-self-update-and-rc-hardening` subject (the version *reporting* half shipped in beta.1).

It builds on [agent.md](agent.md) (the verify → apply control loop), [signing.md](signing.md) /
[../artifacts/mimic.md](../artifacts/mimic.md) (the `artifacts.json` bundle member + its trust
chain), and the off-host [keystone](persistence.md). Self-update touches ONLY the agent binary —
never WireGuard private keys — so the zero-knowledge key-custody guarantee is unaffected.

## Trust chain (custody)

The new binary is fetched over UNTRUSTED transport (github.com / a `GH_PROXY` mirror). Its
integrity rests entirely on a SHA-256 pin that rides the **same Ed25519 signature + keystone
binding as the rest of the bundle**:

```
sha256 pin  ∈  artifacts.json (.agent.bins["linux-<arch>"])  ∈  bundleFiles  ∈  checksums.sha256
            ∈  bundle.sig (Ed25519)  ∈  keystone trust-list
```

`VerifyBundle` already confirms a present `artifacts.json` is **covered by `checksums.sha256`**
(an uncovered one is refused — closing a smuggled-pin vector), so by the time `maybeSelfUpdate`
reads `.agent`, the pin is the controller-signed, keystone-bound pin. The agent verifies the
downloaded bytes against THAT pin — **never** the upstream `.sha256` sidecar (same untrusted
transport as the binary). This is the **HIGH self-update custody principle** ([../../PRINCIPLES.md](../../../PRINCIPLES.md)).

## The decision (`decideSelfUpdate`, pure)

Given the verified `.agent` block, the running `BuildVersion`, and the persisted floor:

| Outcome | Condition |
|---|---|
| **skip** | no agent block, or `desired == running` (and not forced) |
| **refuse** | `desired < running` (downgrade), or `desired < AgentVersionFloor`, or forced but `desired < min_version` (misconfigured rollout) |
| **forced** | `running < agent.min_version` — must update BEFORE applying the bundle |
| **after-apply** | `desired > running`, `≥ floor` — update AFTER a successful apply |

Version precedence is SemVer-aware (`compareVersions`): numeric pre-release fields compare
numerically (`beta.2 < beta.10`); an **empty/absent** running version is the MINIMAL sentinel, so a
legacy agent that reports no version self-updates rather than being frozen out.

## The swap (`performSelfUpdate`)

Reached only for a `forced`/`after-apply` decision. On success it re-execs and does NOT return;
any failure BEFORE the swap keeps the running binary (keep-last-good):

1. **arch → asset.** `key := "linux-" + runtime.GOARCH`; scoped to **amd64/arm64** (D9),
   fail-closed otherwise. The pin is `.agent.bins[key]`.
2. **download** to `.yaog-agent.<ver>.partial` beside the current binary (same FS ⇒ the install
   rename is atomic; a cross-FS layout falls back to copy+fsync+rename). Two sources are tried in
   order — the operator-configured **proxy first** (`${GH_PROXY}${.agent.release_url}/<asset>`, for
   nodes that cannot reach GitHub directly), then a **direct GitHub fetch**
   (`${.agent.release_url}/<asset>`) as a fallback when the proxy is slow/down (the live failure mode
   — a gh-proxy body-read timeout). The SHA-256-vs-pin check (step 3) gates the swap regardless of
   which source served the bytes, so multi-source is custody-safe. Each attempt is bounded by a
   **response-header timeout** + a per-attempt **stall watchdog** (abort if no bytes flow for
   `selfUpdateStallTimeout`); a single **absolute ceiling** (`selfUpdateAbsoluteCap`) bounds the WHOLE
   download — both source attempts share it, so the fallback cannot multiply the worst-case stall on
   the (main-thread) caller. This replaces the old single total deadline, which tripped on the body
   read of a large binary over a slow link.
3. **CUSTODY: verify** the bytes' SHA-256 against the signed pin — mismatch ⇒ refuse.
4. **self-test** `<partial> version` must print EXACTLY `desired` — else refuse (catches a corrupt
   or wrong-arch binary that somehow matched a hash).
5. **breadcrumb** `State.PendingUpdate{from, to, attempts:0, confirmed:false}` written crash-durably
   (temp-rename) BEFORE the swap. State is read once before download and re-read immediately before
   this write; either read failing is a hard refusal. The update never falls back to a fresh
   zero-value state that could erase an unresolved `PendingApply` or an anti-rollback floor.
6. **swap (install-then-flip)** `copy(target → target.bak)` (the live binary stays in place), then
   atomically `rename(partial → target)`. A same-directory rename **replaces** the target, so the
   ExecStart path always names a valid executable across ANY crash point — there is never a window
   with no binary (a crash during a move-then-install ordering would otherwise brick). `.bak` is the
   rollback artifact. A cross-FS layout falls back to copy+fsync+rename.
7. **re-exec** `syscall.Exec(target, os.Args, env)` — the new binary resumes as the daemon and the
   startup reconcile resolves the breadcrumb.

Finalization and abandonment are ordered around the same durability boundary: the agent removes a
`.bak` rollback artifact and reports a terminal state only after the corresponding state replacement
has synced successfully. A read or final-save failure leaves the breadcrumb and available backup in
place for a later recovery pass.

`min_version` ordering (D3 — bumped ONLY on a bundle/wire-format break): a `forced` update runs
AFTER verify+membership+anti-rollback but BEFORE apply, so an agent below the bundle's required
floor never applies an incompatible bundle (if it cannot update — no pin / downgrade / abandoned /
target-below-min — it refuses to apply and reports unhealthy). A non-forced update runs after a
clean apply (best-effort). The anti-rollback (`compiled_at`) check precedes the self-update so a
stale bundle never triggers an agent swap.

**Deferred-update retry (no restart needed).** A non-forced (`after-apply`) update is attempted in
the apply path, but the daemon applies only on a NEW generation — so a download that fails on a
*stable* generation used to wedge the rollout until a manual `systemctl restart yaog-agent`. The
daemon therefore re-attempts a deferred self-update on its **idle cycles**, on a backoff
(`--selfupdate-retry-interval`, default 10m; `≤0` disables), WITHOUT waiting for a new generation.
The trigger is the persisted `State.SelfUpdateBlocked` latch (set when a post-apply attempt is
refused/deferred; cleared once the target is no longer armed). The retry reuses the SAME
`Fetch → VerifyBundle → VerifyMembership → decideSelfUpdate → performSelfUpdate` path as the apply
loop (every retry re-verifies — no stale/unverified pins) and runs on the **main loop thread** so a
swap never interrupts a mid-flight apply. The forced (pre-apply) path already retries — a failure
errors the whole cycle and the daemon re-runs it on its error backoff.

The retry's fetch is wrapped by `agent.WithMembershipGate` (`selfupdate_retry.go`), which runs
`VerifyMembership` after `VerifyBundle` — the **same keystone binding the apply path enforces before a
swap**. This is load-bearing: the retry *decides a binary swap* from the fetched `artifacts.json` pin,
so on a keystone-ON node that pin MUST be bound to the off-host operator credential, not merely the
tier-1 `bundle.sig`. Verifying only `VerifyBundle` (which, with no `--pubkey` pinned in the standard
controller posture, trusts the bundle's own `signing-pubkey.pem`) would let any party that can serve a
`VerifyBundle`-passing bundle drive an agent-binary swap once a deferral is armed — a keystone bypass
to root code-exec. When keystone is OFF (no operator credential) `VerifyMembership` is a no-op, so the
gate is identical to the bundle-only fetch there.

## Startup reconcile + crash-loop bound (two phases + finalize)

This is what bounds the systemd `Restart=always` loop WITHOUT a unit-file change. No breadcrumb ⇒
no-op. The reconcile is split so a freshly-swapped binary that crashes during early init is STILL
bounded:

- **Phase A — `ReconcileSelfUpdateEarly`** runs as the VERY FIRST thing in controller mode, BEFORE
  any crash-prone setup (token/client/pubkey reads). It increments `Attempts` **crash-durably
  FIRST** (so every boot counts, even an early-init panic), and at `Attempts > maxSelfUpdateAttempts`
  (3) it **abandons** (rolls back to `.bak`, records the abandoned target). Needs only the state dir
  + build version — no controller client.
- **Phase B — `ReconcileSelfUpdatePromote`** runs after the client + pinned key exist. When the
  running build IS the target and not yet `Confirmed`, it runs the **health gate** (one clean
  `Fetch + VerifyBundle`): a pass marks the update **PROBATIONARY** (`Confirmed`, keep `.bak`, floor
  NOT yet advanced); a failure rolls back + abandons. A breadcrumb that is ALREADY `Confirmed` on
  boot **resumes probation** (it does NOT immediately roll back — a benign host reboot during the
  short probation window must not falsely abandon a healthy binary); a genuinely-crashing binary
  never completes a cycle to finalize and is caught by Phase A's Attempts cap. This closes the
  daemon-only-crash-after-promote brick class (the health gate alone proves only `version` +
  fetch/verify, not that the daemon loop runs) without false-abandoning healthy nodes.
- **Finalize — `FinalizeSelfUpdate`** runs after the new binary completes its FIRST full daemon
  cycle (proving it actually runs). It **advances `AgentVersionFloor`** (the ONLY place the floor
  advances), clears the breadcrumb + the abandoned-target memory, and drops `.bak`.

Accepted residual (fails safe): a crash-reboot during probation and a *benign* host reboot during
probation are indistinguishable to the agent, so both count toward Phase A's Attempts cap. If a
HEALTHY new binary's host happens to reboot the full cap (3) of times before its first cycle
finalizes (e.g. repeated power loss during a long-poll-bound first cycle), the target is abandoned
(rolled back to `.bak` + `AbandonedAgentVersion` recorded) even though the binary was fine. This errs
toward the SAFE direction — rollback to the known-good binary, never a brick — and is irreducible
without an external liveness signal (resetting Attempts on a Confirmed resume would reintroduce the
unbounded crash loop). The operator re-triggers by changing `TargetAgentVersion`. The window is the
first-cycle duration (seconds to one long-poll), so the trigger is rare.

In-flight guard: `performSelfUpdate` refuses to start a second swap while a breadcrumb is already
pending (e.g. a prior re-exec failed and the daemon retried the cycle) — re-swapping would overwrite
the `.bak` rollback target with the already-installed new binary and reset `Attempts`, both of which
would defeat the bound. A post-swap re-exec failure reports `swapped=true` so the caller does NOT
record a routine failure (that would erase the on-disk breadcrumb the next-boot reconcile needs).

Crash-safety: rollback renames `.bak → target` BEFORE clearing the breadcrumb, so a crash
mid-rollback re-tries on the next boot rather than stranding a broken binary unbreadcrumbed. A
failed update **never** advances the floor; `AbandonedAgentVersion` records a doomed target so
`decideSelfUpdate` will not re-arm the SAME version (no perpetual flap) until the operator moves to
a different target.

Stalled-rollout visibility (`selfupdate: Blocked`): a post-apply self-update that keeps being
**deferred** before any swap — most commonly when the rollout target was bumped but its pins still
resolve to the *old* binary, so the downloaded binary's self-test version (or its signed hash) does
not match the target and `performSelfUpdate` refuses (no brick, no breadcrumb) — now records a
curated reason in `State.SelfUpdateBlocked`. `selfUpdateCondition` surfaces it (lowest precedence,
below an in-flight/abandoned signal) as a `selfupdate` **Blocked** warn condition, so the panel +
the `/telemetry` heartbeat show WHY a node is not advancing instead of it silently staying behind.
It is observability only (touches no custody field) and self-clearing: `recordSuccess` rebuilds the
apply state without it each cycle, and the deferred path re-sets it only while the block persists.
The remedy is operator-side — re-arm the rollout so its pins point at the target build (the one-click
"update all → controller version" re-fetches matching pins), then redeploy. And the routine apply-state writers (`recordSuccess`/`recordFailure`) **preserve**
`AgentVersionFloor` + the in-flight breadcrumb (the same discipline as the membership floor), so a
normal cycle can neither wipe the anti-downgrade floor nor erase a swap in flight.

## Canary-then-fleet rollout (D2)

The `artifacts.json` agent block is **per-node**: a node receives it only when a target version is
configured AND the node is in the rollout set. The operator stages the rollout via
`ControllerSettings`:

- `TargetAgentVersion` — the version to drive toward. **Empty ⇒ no agent block for any node ⇒ no
  self-update** (the safety contract).
- `MinAgentVersion` — the forced-before-apply floor (a bundle/wire break); empty ⇒ no forced update.
- `AgentBins` — `"linux-<arch>"` → `{asset, sha256}` (the agent release base reuses the existing
  `AgentReleaseBaseURL`).
- `AgentCanaryNodeIDs` — during the canary phase only these nodes receive the agent block.
- `AgentRolloutFleetWide` — the operator's "promote canary → fleet" action: when true, EVERY
  enrolled node receives the agent block.

`AgentRolloutNodeIDs(cs, nodes)` computes the per-compile set; the per-node `artifacts.json` is then
built by `render.All`. All pins are validated strictly at POST (`validateAgentRollout`, D8): semver
versions, `linux-<arch>` keys, safe asset names, 64-hex SHA-256, and a target with no bins is
rejected (it could only no-op).

## Manual recovery

A bad swap is self-healing (rollback + abandon at the cap), but a human can intervene:

- The previous binary is at `<binary>.bak` until promotion: `mv <binary>.bak <binary>` restores it.
- To stop a stuck rollout, clear `PendingUpdate` in the node's `state.json`
  (`/var/lib/yaog-agent/state.json`) and restart the unit; on the controller, set
  `TargetAgentVersion` back (or remove the node from the canary set) so the bundle stops carrying
  the agent block.
- `yaog-agent version` reports the running build; the agent reports it on every check-in, so the
  panel's per-node version badge (beta.1, `NodeRegistry` / `FleetNodeDetailPage`) **flips to the
  target on a successful self-update** — a canary node whose badge has not advanced (while peers'
  have) is the fleet-visible signal of a pending-or-failed update, distinguished by the node's
  reported health line and the agent journal.

## Panel scope (observability + configuration)

Per-node self-update is observed through the beta.1 surfaces (the reported-version badge flips to
the target on success; the health line carries the apply outcome) AND, since
`controller-panel-rollout-ui` (beta.3), a dedicated panel surface:

- **Configuration.** The rollout *pins* (`AgentBins`, `TargetAgentVersion`, `MinAgentVersion`, the
  canary set, `AgentRolloutFleetWide`) are edited in the `AgentUpdateSettings` card on the Settings
  page (controller-mode only) — and the mimic GitHub-`.deb` catalog in the symmetric
  `MimicCatalogSettings` card. Both still POST the same strictly-validated operator
  `POST /settings` (full-replace), so the API remains the authority; the cards add an **"Assist from
  GitHub release"** affordance that fetches the `.sha256` sidecars (via the new operator
  `POST /release-pins`, gh-proxy-applied + SSRF-guarded) to PRE-FILL the per-asset pins for the
  operator to REVIEW. The assist is convenience only — it never auto-saves a pin, and trust stays
  the keystone-signed `artifacts.json` the agent verifies against (custody unchanged).
- **Observability.** A per-node update-status chip (`off / not-targeted / pending / applying /
  applied / failed / stale`) on `NodeRegistry` + `FleetNodeDetailPage`, derived (pure
  `deriveUpdateState`) from the server-computed `in_rollout` membership, the reported version vs the
  configured target (a real SemVer comparator), and the `lastHealth` markers — with an opt-in live
  poll. `failed` is best-effort (the agent's `abandoned:` marker is transient); a reliable
  persistent failed state would need a positive agent-reported field (a deliberate future wire
  change, not built).

Promotion fleet-wide is gated behind an explicit confirm (the empty-target safety contract: an
empty `TargetAgentVersion` ⇒ no self-update ⇒ no chip). This closes the descoped plan-9 "Canary UI".

## Out of scope

Auto (non-canary) fleet rollout; closing the bootstrap-TOFU hole (the agent's first binary is
fetched by the bootstrap over TLS-to-the-mirror without a pre-shared pin) remains future work.
