# STATUS
<!-- regenerated: 2026-07-17 -->
<!-- by: close-phase / execute-implementation-plan -->

## Active work

- **Subject:** `deployment-stability-and-charted-telemetry-2026_07_17`
- **Branch:** `fix/rc12-telemetry-drafts` @ `d4eb699`
- **Current plan:** plan-2 — Repair client allocation compatibility and browser baselines
- **Last shipped:** Plan 1 restored structured deploy validation, kept rejected drafts from changing
  served or staged deployment state, and limited “Deploy anyway” to older-controller 404/405 responses
  (commit `3035a2e`, 2026-07-17).
- **Other open subject:** `mixed-controller-local-mode-2026_06_25` remains open with its historical
  plan-5 and plan-7 pending; it is not part of the rc.12 execution sequence.

## Open questions / blockers

- None. Plan 1’s focused Go and frontend gates passed from the exact cached tree, and three independent
  review passes found no remaining correctness, security-boundary, localization, or scope defects.

## Next actions

1. Execute and close plan-2’s historical client-allocation and browser-baseline repair.
2. Execute and close plan-3’s audit-noise and display-only probe-name fixes.
3. Execute plans 4–7 for the successor signed telemetry policy, constrained URL probes, automatic
   disk/GPU discovery, controller history, and Fleet charts.
4. Run plan-8’s whole-subject review/fix/re-review loop, CI-equivalent gates, documentation updates,
   and mandatory full `refresh-specs` flow.
5. Run plan-9 only after exact-main verification; the annotated `v2.0.0-rc.12` tag remains the final
   repository mutation before read-only GitHub/container verification.

## Recently closed subjects (last 3)

- `post-refactor-debt-paydown-2026_07_14` (2026-07-15, delivered): fourteen-plan correctness,
  structural, CI, and custody/publication paydown shipped through rc.6 and rc.8.
- `framework-refactor-2026_07_13` (2026-07-14, delivered): modular controller/frontend structure and
  the chart-first telemetry framework were integrated and independently reviewed.
- `telemetry-history-and-delta-deploy-2026_07_13` (2026-07-14, delivered): bounded controller history,
  server-side rollups, visible live refresh, and delta deployment behavior were shipped.
