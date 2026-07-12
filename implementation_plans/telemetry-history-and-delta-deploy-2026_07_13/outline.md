# telemetry-history-and-delta-deploy — CPU/RAM history charts + skip-unchanged deploy (ships as v2.0.0-rc.5)

> Subject opened 2026-07-13 (owner request: "telemetry's CPU/RAM persisted as diagrams (specify
> granularity)" + "deploy has no split-device deploy — nodes with no difference are all refreshed").
> **DRAFTED, plans-only at draft time (owner: budget) — execution starts on the owner's go.**
> Execution per-PR: independent workflow review → fix → re-review → CI green → merge.

## Context — assessment (2026-07-13, verified in code)

### Track A: telemetry history
- The agent's `resourceSampler` (`internal/agent/telemetry_resource.go`) emits
  `metrics["resource"]` = `hostResource{Load1,Load5,Load15, MemTotalKB, MemAvailKB}` per telemetry
  heartbeat, from pure `/proc/loadavg` + `/proc/meminfo` reads. **There is NO true CPU%** — loadavg
  only. The panel renders the CURRENT value (`frontend/src/components/deploy/ResourcePanel.tsx`,
  `frontend/src/lib/resource.ts`, wired in `FleetNodeDetailPage.tsx`).
- `RecordTelemetry` is **memory-only BY DESIGN** (`internal/controller/filestore.go:751-758`,
  `store.go:494-501`): heartbeats write ONLY the in-memory overlay under `telemetryMu` — never a
  durable rewrite (DoS reasoning). **History must therefore be a NEW bounded path** that preserves
  this invariant: in-memory ring per node, appended at RecordTelemetry, flushed to disk periodically
  (interval/threshold), NEVER per-heartbeat.
- No history endpoint or chart exists anywhere.

### Track B: delta deploy
- **Root cause of "all nodes refresh":** the agent ALREADY skips equal-content applies —
  `internal/agent/cycle.go:159-178` skips when `prev.LastChecksum == man.Checksum` **AND the fetched
  generation ≤ the live cursor** (the dual clause deliberately preserves the operator's single-shot
  force-reapply from a cold cursor). Deploy (`CompileAndStage`, `internal/controller/compile.go:211`)
  stages EVERY node at a NEW generation → every agent sees gen > cursor → the skip never engages →
  every node re-applies (install.sh re-runs → Phase-0 teardown → WG bounce) even when its bundle is
  byte-identical.
- **The clean seam exists:** bundles are per-node with their own `Generation`
  (`SignedBundle`, `store.go:188-196`); `NodeStatus` tracks `DesiredGeneration` vs
  `AppliedGeneration` (`store.go:97-100`); a node's content identity is
  `hex(sha256(checksums.sha256))` — already the off-host-bound identity (`compile.go:24-32`), and
  `manifest.json`'s `compiled_at` is already OUT of the checksummed set (`compile.go:131`) so
  unchanged nodes recompile to the same digest. Fix = controller-side: **don't re-stage a node whose
  digest is unchanged** → its generation stays put → the agent sees nothing new → zero refresh.
  Agent semantics (`cycle.go`) stay UNTOUCHED.
- Danger zone: the keystone promote/serve path with MIXED generations (`PromoteStaged`
  `compile.go:434`; `ErrNoStagedBundle` `store.go:42-45`; the beta.5 served-vs-staged trust-list
  split). The trust-list is regenerated each stage and binds ALL nodes' digests OUT of the
  checksummed set — an unchanged node's entry is unchanged, so serving the new trust-list beside an
  old-generation bundle stays verifiable. Plan-5 proves this seam with regression tests before any
  UX lands.

## Decisions log (locked with the owner, 2026-07-13)

1. **Charts: Recharts — but built REUSABLE.** A series-generic themed `TimeSeriesChart` wrapper
   ("one day it could carry ping data" — owner). Accepts the dependency (npm SCA gate covers it).
2. **Retention: raw-only log + query-time aggregation, hard cap CONFIGURABLE** (a fleet setting;
   one knob, not tier configs). Granularity is purely a query/UI parameter.
3. **CPU: add true `cpu_pct`** (stateful /proc/stat jiffies delta agent-side) beside loadavg.
4. **Delta deploy: Skip + Force + pre-deploy preview** (the full option). Skip basis = the
   checksums.sha256 digest. Per-node AND fleet-wide Force. Preview = dry-run compile + digest
   compare in the Deploy dialog, bound to the topology version.
5. Shape accepted as-is (8 plans, two tracks, ONE subject, ships as `v2.0.0-rc.5`).
6. **Plans-only at draft** (owner budget); execution on go. Plans PR merges on CI without the
   multi-agent review (docs-only); the full review regime applies to every execution PR.
7. **plan-1.5 INSERTED during execution (2026-07-13, owner-prompted).** The owner flagged that
   "new telemetry only fires at deployment" has RECURRED and been patched one signal at a time, and
   asked for the deeper framework defect. A 3-trace Opus investigation
   (`framework-finding-telemetry-dual-path.md`) found the root cause: TWO observability emitters
   (apply `/report` conditions-only vs the heartbeat `/telemetry` sampler set) with nothing forcing
   parity/freshness. Fix = a post-apply KICK unifying to one path (apply triggers a live sampler
   beat). Scoped AGENT-ONLY (the custody-wire cleanup + durable-metrics + merge-semantics deferred as
   custody-sensitive / plan-2-owned). Sequenced BEFORE plan-2 (history gains a deploy-instant sample).

## Plan status

| # | Plan | Status | PR |
|---|------|--------|-----|
| 1 | Agent `cpu_pct` (stateful /proc/stat delta) + current-value panel row | ✅ merged | #249 |
| 1.5 | Unify observability on the sampler heartbeat (post-apply kick) + freshness guards | ✅ merged | #250 |
| 2 | Controller history store (ring + periodic flush + configurable cap) | pending | — |
| 3 | History query API (server-side aggregation, operator-gated) | pending | — |
| 4 | Recharts reusable `TimeSeriesChart` + node-detail charts + granularity picker | pending | — |
| 5 | Staging skip-unchanged (per-node digest compare; keystone-seam regression proofs) | pending | — |
| 6 | Force redeploy (per-node + fleet) + pre-deploy preview dialog | pending | — |
| 7 | Docs (spec + bilingual wiki) | pending | — |
| 8 | Release v2.0.0-rc.5 | pending | — |

## Cross-cutting invariants (review lenses check these)

- **Heartbeats never force disk writes** (the RecordTelemetry DoS invariant): history appends go to
  an in-memory ring; flush is timer/threshold-driven and failure-tolerant; a slow disk can never
  stall a heartbeat. History locking never holds the store-wide `mu` (mirror `telemetryMu`'s
  isolation).
- **Deploy correctness over cleverness:** the skip decision uses the digest that is ALREADY the
  off-host trust identity; when in doubt (missing/corrupt prior bundle, digest unreadable) → stage
  normally (fail open to a redeploy, never to a stale config). **Keystone rotation/first-pin change
  ZERO bundle bytes yet require a full re-stage (the promote flips the served trust-list only when
  something is staged) → the skip is keystone-aware and disables itself for that stage (plan-5, the
  review blocker).** Rekey-all safety is PEER-driven (a rekeyed node's own digest never changes —
  its private key is a placeholder spliced at run time; its pubkey lives in PEERS' bundles), so
  rekey re-stages flow through peers naturally; a future per-node rekey must Force the rekeyed node.
- **Agent untouched in Track B:** `cycle.go` apply semantics do not change; the single-shot
  force-reapply workflow keeps working byte-for-byte.
- **Observability stays live-only client-side:** charts fetch history on view; `stripLiveTelemetry`
  / localStorage custody rules unchanged.
- **Panel:** theme tokens only (no hardcoded colors — the beta.13 lesson), `data-testid` locators
  (never color classes), i18n en+zh for every new string.
- Go↔TS: Track B touches NO rendered artifact bytes (staging logic only) → no golden churn expected;
  assert zero golden diff in plan-5. New validator codes: none planned.
- `gofmt -l ./cmd ./internal` clean; FE verified with `npm run build` (tsc -b); new FE test files
  must match the vitest include globs.

## Out of scope (deferred / not asked)

- Per-peer ping/RTT history (the chart component is built generic FOR it; wiring is a future subject).
- Alerting/thresholds on resource history; Prometheus/export endpoints.
- Downsampling tiers (raw+cap chosen); per-metric retention knobs beyond the single cap.
- Any change to agent apply semantics or the single-shot rerun workflow.

## Risks (stop-losses)

- **Keystone mixed-generation serving + zero-byte re-sign flows** (plan-5): if the served/staged
  split can't safely serve old-generation bundles beside a new trust-list, OR the keystone-aware
  skip-disable can't cleanly detect rotation/first-pin at stage time, STOP and re-design with the
  owner (plan-5.5) before any skip ships.
- **Recharts bundle/SCA**: if the security scan flags it or the bundle grows unacceptably, fall back
  to uPlot (owner said reusable matters, not the specific lib) — a plan-4.5 decision.
- **Raw-log growth**: hard cap enforced at flush time; a fleet that never tunes it stays bounded by
  the default.
