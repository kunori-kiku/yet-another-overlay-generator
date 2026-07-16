# STATUS
<!-- regenerated: 2026-07-17 -->
<!-- by: close-phase / execute-implementation-plan -->

## Active work

- **Subject:** `deployment-stability-and-charted-telemetry-2026_07_17`
- **Branch:** `fix/rc12-telemetry-drafts` @ `5e77878`
- **Current plan:** plan-5 — Add constrained URL probes end to end
- **Last shipped:** Plan 4 preserved exact version-1 telemetry policy behavior, added an exclusive
  signed successor policy member, and introduced authenticated latest-heartbeat readiness plus a
  draft-preserving agent-upgrade projection (commit `5e751ca`, 2026-07-17).
- **Other open subject:** `mixed-controller-local-mode-2026_06_25` remains open with its historical
  plan-5 and plan-7 pending; it is not part of the rc.12 execution sequence.

## Open questions / blockers

- None. The isolated Plan 4 index passed all relevant Go packages, frontend lint, 45 focused tests,
  the production build, and a Windows agent cross-compile; three independent final reviews found no
  remaining rollout, compatibility, custody, structural, or hygiene defect.

## Next actions

1. Execute and close plan-5's constrained URL probes, exact expected-status success rule, live status
   metadata, and shared latency/availability charts.
2. Execute plans 6–7 for automatic disk/GPU discovery, bounded collection, controller history, and
   reusable Fleet charts.
3. Run plan-8's whole-subject review/fix/re-review loop, CI-equivalent gates, documentation updates,
   and mandatory full `refresh-specs` flow.
4. Run plan-9 only after exact-main verification; the annotated `v2.0.0-rc.12` tag remains the final
   repository mutation before read-only GitHub/container verification.

## Recently closed subjects (last 3)

- `post-refactor-debt-paydown-2026_07_14` (2026-07-15, delivered): fourteen-plan correctness,
  structural, CI, and custody/publication paydown shipped through rc.6 and rc.8.
- `framework-refactor-2026_07_13` (2026-07-14, delivered): modular controller/frontend structure and
  the chart-first telemetry framework were integrated and independently reviewed.
- `telemetry-history-and-delta-deploy-2026_07_13` (2026-07-14, delivered): bounded controller history,
  server-side rollups, visible live refresh, and delta deployment behavior were shipped.
