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
5. **breadcrumb** `State.PendingUpdate{from, to, attempts:0}` written crash-durably (temp-rename)
   BEFORE the swap.
6. **swap** `rename(target → target.bak)` then install the new binary; on install failure the
   backup is restored so the node is never left binary-less.
7. **re-exec** `syscall.Exec(target, os.Args, env)` — the new binary resumes as the daemon and the
   startup reconcile resolves the breadcrumb.

`min_version` ordering (D3 — bumped ONLY on a bundle/wire-format break): a `forced` update runs
AFTER verify+membership but BEFORE apply, so an agent below the bundle's required floor never
applies an incompatible bundle (if it cannot update — no pin / downgrade — it refuses to apply and
reports unhealthy). A non-forced update runs after a clean apply (best-effort; the next cycle
retries).

## Startup reconcile + crash-loop bound (`ReconcileSelfUpdate`)

Runs on EVERY boot, BEFORE the daemon loop — this is what bounds the systemd `Restart=always` loop
WITHOUT a unit-file change. With no breadcrumb it is a no-op. With one:

- It increments `Attempts` **crash-durably FIRST**, so even a new binary that crashes during early
  init is bounded (each boot counts).
- `Attempts > maxSelfUpdateAttempts` (3) ⇒ **abandon**: roll back to `.bak` (if running the failed
  target) and clear the breadcrumb — the loop ends.
- Running build **is** the target ⇒ run the **health gate** (one clean `Fetch + VerifyBundle`): a
  pass **promotes** (advance `AgentVersionFloor` to the new version — the ONLY place the floor
  advances — drop `.bak`, clear the breadcrumb); a failure **rolls back** to `.bak` and re-execs the
  prior binary.
- Running build is NOT the target (swap/exec never took effect) ⇒ leave the breadcrumb so attempts
  keep climbing toward the cap.

A failed update **never** advances the floor, so an attacker cannot use a rolled-back update to
lower the anti-downgrade bar.

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
