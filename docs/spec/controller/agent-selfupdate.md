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
2. **download** `${GH_PROXY}${.agent.release_url}/<asset>` to `.yaog-agent.<ver>.partial` beside the
   current binary (same FS ⇒ the install rename is atomic; a cross-FS layout falls back to
   copy+fsync+rename).
3. **CUSTODY: verify** the bytes' SHA-256 against the signed pin — mismatch ⇒ refuse.
4. **self-test** `<partial> version` must print EXACTLY `desired` — else refuse (catches a corrupt
   or wrong-arch binary that somehow matched a hash).
5. **breadcrumb** `State.PendingUpdate{from, to, attempts:0, confirmed:false}` written crash-durably
   (temp-rename) BEFORE the swap.
6. **swap (install-then-flip)** `copy(target → target.bak)` (the live binary stays in place), then
   atomically `rename(partial → target)`. A same-directory rename **replaces** the target, so the
   ExecStart path always names a valid executable across ANY crash point — there is never a window
   with no binary (a crash during a move-then-install ordering would otherwise brick). `.bak` is the
   rollback artifact. A cross-FS layout falls back to copy+fsync+rename.
7. **re-exec** `syscall.Exec(target, os.Args, env)` — the new binary resumes as the daemon and the
   startup reconcile resolves the breadcrumb.

`min_version` ordering (D3 — bumped ONLY on a bundle/wire-format break): a `forced` update runs
AFTER verify+membership+anti-rollback but BEFORE apply, so an agent below the bundle's required
floor never applies an incompatible bundle (if it cannot update — no pin / downgrade / abandoned /
target-below-min — it refuses to apply and reports unhealthy). A non-forced update runs after a
clean apply (best-effort; the next cycle retries). The anti-rollback (`compiled_at`) check precedes
the self-update so a stale bundle never triggers an agent swap.

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

In-flight guard: `performSelfUpdate` refuses to start a second swap while a breadcrumb is already
pending (e.g. a prior re-exec failed and the daemon retried the cycle) — re-swapping would overwrite
the `.bak` rollback target with the already-installed new binary and reset `Attempts`, both of which
would defeat the bound. A post-swap re-exec failure reports `swapped=true` so the caller does NOT
record a routine failure (that would erase the on-disk breadcrumb the next-boot reconcile needs).

Crash-safety: rollback renames `.bak → target` BEFORE clearing the breadcrumb, so a crash
mid-rollback re-tries on the next boot rather than stranding a broken binary unbreadcrumbed. A
failed update **never** advances the floor; `AbandonedAgentVersion` records a doomed target so
`decideSelfUpdate` will not re-arm the SAME version (no perpetual flap) until the operator moves to
a different target. And the routine apply-state writers (`recordSuccess`/`recordFailure`) **preserve**
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

## Panel scope (observability)

Per-node self-update is observed through the **existing** beta.1 surfaces: the reported-version
badge flips to the target on success, and the health line carries the apply outcome. The rollout
*pins* (`AgentBins`, `TargetAgentVersion`, `MinAgentVersion`, the canary set) are configured via the
operator `POST /settings` API and validated strictly there — **the same model as the mimic
GitHub-`.deb` catalog**, which deliberately has no bespoke panel form for its signed-pin map. A
dedicated canary-progress widget (a per-node pending/applied/failed chip + an in-panel pin editor)
is a documented follow-up, not a beta.2 blocker.

## Out of scope

Auto (non-canary) fleet rollout; closing the bootstrap-TOFU hole (the agent's first binary is
fetched by the bootstrap over TLS-to-the-mirror without a pre-shared pin — deferred to rc.2).
