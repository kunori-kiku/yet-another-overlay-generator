# beta16-smoke-hardening — outline

> **Subject:** harden the observability/status path after the beta.15 fleet smoke surfaced a stale
> node-status report. Owner-folded 2026-06-27 ("fix all 3 + ship beta.16 now"). Ships as
> `v2.0.0-beta.16` (prerelease; owner promotes after re-smoke), like beta.10 hardened beta.9.

## Context

While smoking beta.15 the owner saw node `node-1773596103303` report stale status on the panel:
`Agent version v2.0.0-beta.14` + `selfupdate: Blocked` + a frozen `Last Seen`, even though the node's
journal showed it had **successfully self-updated to beta.15** (`self-update to v2.0.0-beta.15 finalized`)
and applied gen 36. A 4-probe investigation (read-only over agent + controller + panel) found the
symptom is multi-causal — three independent layers each freeze a different field. The node + the
beta.15 self-update fix worked; the **reporting/observability path** has the gaps.

## Findings → fixes (two PRs)

### fix-A (frontend) — the node-detail page never refreshes
**Root cause.** `FleetNodeDetailPage` (`/fleet/nodes/:id`) reads `nodes.find(...)` from the persisted
cache with **no refresh-on-mount and no poll** (an in-file comment even admits it). The only
client-side refresh paths are the `/fleet` **list** route (refresh-on-mount) and an opt-in default-OFF
"Live" poll on the list page — neither covers the detail page. So a deep-linked node page is an
indefinitely frozen snapshot regardless of server state. This is the top cause of "Last Seen won't
advance when I look later."
**Fix.** Add refresh-on-mount (mirror `FleetPage`'s `useEffect(refresh)`), a manual Refresh control +
a "last synced / stale" indicator, and an opt-in Live poll on the detail page. Frontend only.
**Files.** `frontend/src/components/pages/FleetNodeDetailPage.tsx` (+ whatever shared Live/refresh hook
`FleetPage` uses in `controllerStore`).

### fix-B (agent) — sticky `selfupdate: Blocked` + heartbeat fragility
1. **`selfupdate: Blocked` outlives the update.** `State.SelfUpdateBlocked` is re-derived every
   heartbeat and its only clearers are `recordSuccess` (new-generation apply) and the beta.15 retry
   path's `updateSkip`-clear (which needs a reachable controller). `FinalizeSelfUpdate` — the success
   path — does **not** clear it. So a node that *succeeded* in self-updating keeps reporting Blocked
   until a config redeploy or a reachable idle-retry.
   **Fix.** Clear `State.SelfUpdateBlocked` in `FinalizeSelfUpdate` (a confirmed self-update means the
   node is no longer blocked). Complements the existing recordSuccess + retry clears.
   **Files.** `internal/agent/selfupdate.go` (`FinalizeSelfUpdate`).
2. **A hung `wg show` can wedge the heartbeat.** `wgShowFn` runs `exec.Command("wg","show","all","dump")`
   with **no timeout**, twice per beat; a wedged `wg`/netlink would block `Telemetry.Collect` forever
   and freeze `Last Seen` while the separate poll loop keeps the daemon "alive" — an "alive but frozen"
   path.
   **Fix.** `exec.CommandContext` with a bounded timeout; on timeout treat WG as unknown, never block.
   **Files.** `internal/agent/conditions_wireguard.go`.
3. **The heartbeat goroutine has no top-level `recover()`.** A panic outside the per-sampler guards
   would silently and permanently kill the only post-apply `Last Seen` refresher.
   **Fix.** Wrap `beat()` in a `recover()` (log + continue), mirroring the per-sampler guard discipline.
   **Files.** `cmd/agent/main.go` (`runHeartbeat`).

## Not in scope (infra / by-design, not code)
- The controller's Cloudflare **502s** that freeze server-side check-ins during an origin outage — the
  agent is correctly resilient (keep-last-good + retry); stabilizing the controller origin is an ops
  task, documented for the owner, not a code change here.
- The agent sends no client-side timestamp, so the panel can't distinguish unreachable-but-alive from
  dead — acceptable; the refresh + stale indicator (fix-A) is the right mitigation.

## Closure
- fix-A + fix-B each merged via an independent workflow review → fix → re-review, CI green.
- `go build/vet/test` + FE build/lint/vitest green; tests for the agent changes.
- Roll `v2.0.0-beta.16` CHANGELOG, tag prerelease on green main. Owner re-smokes + promotes to Latest.
