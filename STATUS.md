# STATUS
<!-- regenerated: 2026-07-17 -->
<!-- by: close-phase / execute-implementation-plan -->

## Active work

- **Subject:** `deployment-stability-and-charted-telemetry-2026_07_17`
- **Branch:** `fix/rc12-telemetry-drafts` @ `1b8422b`
- **Current plan:** plan-7 — Activate device telemetry, history, and Fleet charts
- **Last shipped:** Plan 6 added dormant bounded disk/filesystem/GPU contracts and Linux collectors
  with private hashed identities, explicit zero-versus-gap behavior, hard discovery/process limits,
  and portable non-Linux stubs (commit `efabd64`, 2026-07-17).
- **Other open subject:** `mixed-controller-local-mode-2026_06_25` remains open with its historical
  plan-5 and plan-7 pending; it is not part of the rc.12 execution sequence.

## Open questions / blockers

- None. Focused native and race tests, `go vet`, Windows/amd64 and Linux/arm64 package builds, and
  formatting checks passed. Three independent final reviews found no remaining disk/filesystem,
  GPU/security, boundedness, contract, portability, structural, or hygiene defect.

## Next actions

1. Execute and close plan-7's signed opt-in activation, controller history/API projection, and reusable
   Fleet charts for every dynamic device metric.
2. Run plan-8's whole-subject review/fix/re-review loop, CI/release-equivalent gates, documentation
   updates, and mandatory full `refresh-specs` flow.
3. Run plan-9 only after exact-main verification; the annotated `v2.0.0-rc.12` tag remains the final
   repository mutation before read-only GitHub/container verification.

## Recently closed subjects (last 3)

- `post-refactor-debt-paydown-2026_07_14` (2026-07-15, delivered): fourteen-plan correctness,
  structural, CI, and custody/publication paydown shipped through rc.6 and rc.8.
- `framework-refactor-2026_07_13` (2026-07-14, delivered): modular controller/frontend structure and
  the chart-first telemetry framework were integrated and independently reviewed.
- `telemetry-history-and-delta-deploy-2026_07_13` (2026-07-14, delivered): bounded controller history,
  server-side rollups, visible live refresh, and delta deployment behavior were shipped.
