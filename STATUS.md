# STATUS
<!-- regenerated: 2026-07-17 -->
<!-- by: close-phase / execute-implementation-plan -->

## Active work

- **Subject:** `deployment-stability-and-charted-telemetry-2026_07_17`
- **Branch:** `fix/rc12-telemetry-drafts` @ `a1d3871`
- **Current plan:** plan-3 — Quiet routine audits and finish display-only probe names
- **Last shipped:** Plan 2 preserved the router-side port and complete address allocations on client
  links, aligned Go/TypeScript normalization and collision ownership, and made browser hydration/save
  baselines converge on the normalized topology (commit `f136d72`, 2026-07-17).
- **Other open subject:** `mixed-controller-local-mode-2026_06_25` remains open with its historical
  plan-5 and plan-7 pending; it is not part of the rc.12 execution sequence.

## Open questions / blockers

- None. Plan 2's exact Go gate, 70 focused frontend tests, and production frontend build passed from
  the isolated cached tree; three fresh re-review passes found no remaining backend, frontend,
  documentation, compatibility, or commit-scope defects.

## Next actions

1. Execute and close plan-3's audit-noise and display-only probe-name fixes.
2. Execute plans 4–7 for the successor signed telemetry policy, constrained URL probes, automatic
   disk/GPU discovery, controller history, and Fleet charts.
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
