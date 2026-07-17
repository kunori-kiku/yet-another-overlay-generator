# STATUS
<!-- regenerated: 2026-07-17 -->
<!-- by: close-phase -->

## Active work

- **Subject:** the rc.12 implementation subject is archived as Delivered. The only remaining
  unarchived subject is `mixed-controller-local-mode-2026_06_25`, whose historical plan-5 and plan-7
  remain pending and were not resumed here.
- **Branch:** `fix/rc12-telemetry-drafts` @ closure head `34f8240`, retained for the cumulative PR.
- **Current plan:** the archived rc.12 [Plan 9](implementation_plans/_completed/deployment-stability-and-charted-telemetry-2026_07_17/plan-9-2026_07_17.md)
  terminal checklist: merge the reviewed branch, prove exact-main CI, then tag and verify publication.
- **Latest completed work:** plans 1-8, integrated review/fix/re-review, full local gates, full specs
  refresh, and subject bookkeeping are complete (closure commit `34f8240`, 2026-07-17).
- **Release candidate:** `v2.0.0-rc.12` is release-ready and **uncut**. No tag, GitHub release/draft,
  or official GHCR rc.12 reference existed at the early preflight; Plan 9 must repeat that check.

## Open questions / blockers

- No implementation blocker remains. Publication is blocked until the cumulative PR's required checks
  pass, the reviewed branch is squash-merged, and push CI succeeds on that exact `origin/main` commit.
- No physical NVIDIA or AMD GPU was available for an on-hardware smoke. Fixture, provider-boundary,
  process-bound, portability, and cross-build coverage passed; the caveat remains explicit rather than
  being represented as a completed hardware test.
- The annotated `v2.0.0-rc.12` tag must be the final repository mutation. Any red exact-main gate or
  pre-existing immutable reference stops publication.

## Next actions

1. Create or reuse the cumulative PR from `fix/rc12-telemetry-drafts` and wait for every required check.
2. Squash-merge the reviewed branch, update local `main`, and prove it exactly equals `origin/main`.
3. Wait for successful push CI on that exact main commit, including the PowerShell gate unavailable locally.
4. Repeat the tag/release/GHCR preflight, then create and push the annotated tag as the final source mutation.
5. Monitor the release transaction and verify all 22 assets plus native amd64/arm64 official image pointers.

## Recently closed subjects (last 3)

- `deployment-stability-and-charted-telemetry-2026_07_17` (2026-07-17, Delivered): rc.12 fixes,
  URL/device telemetry, chart/history framework, reviews, specs, and release-ready evidence completed.
- `post-refactor-debt-paydown-2026_07_14` (2026-07-15, Complete): residual security, correctness,
  documentation, and structural debt closed through fourteen reviewed plans.
- `framework-refactor-2026_07_13` (2026-07-14, Complete): unified compile architecture and repository
  framework restructuring completed.
