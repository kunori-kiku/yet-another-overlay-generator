# STATUS
<!-- regenerated: 2026-07-17 -->
<!-- by: close-phase -->

## Active work

- **Subject:** focused rc.13 agent self-update health-gate hotfix. The archived rc.12 telemetry and
  deployment subject remains Delivered; the historical mixed-controller/local-mode subject was not
  resumed.
- **Branch:** `fix/rc13-selfupdate-health-retry` at reviewed hotfix commit `5667b46`; PR #311 targets
  `main`.
- **Current plan:** pass only the repository-required PR checks, squash-merge, prove required CI on
  the exact resulting `origin/main`, then cut and verify `v2.0.0-rc.13` through the ordinary release
  workflow.
- **Latest completed work:** rc.12 was published from `122b217`; the rc.13 fix now retains the
  self-update breadcrumb and rollback backup across health failures and uses the existing persisted
  three-attempt ceiling instead of abandoning on the first request error.
- **Release candidate:** `v2.0.0-rc.13` is release-ready and **uncut**. No rc.13 source tag, GitHub
  release, or official versioned image has been created.

## Open questions / blockers

- No implementation blocker remains. Publication waits only for PR #311's required checks, the
  squash merge, and the required push CI on that exact main commit.
- The behavior change is confined to the already-bounded post-swap health reconciliation path. Its
  focused regression proves three retries retain the new binary and `.bak`, while the next supervised
  boot crosses the existing ceiling and performs the crash-safe rollback.
- Any red required gate or pre-existing immutable rc.13 tag/release/image reference stops fresh
  publication and enters the documented recovery decision instead.

## Next actions

1. Wait for every required PR #311 check; do not add optional local gates.
2. Squash-merge, update local `main`, and prove it exactly equals `origin/main`.
3. Wait for the required push CI on that exact main commit.
4. Run the rc.13 tag/release/GHCR absence preflight and create one annotated tag at that main tip.
5. Monitor the release transaction and verify the 22 assets, GitHub Latest, and native amd64/arm64
   official image pointers.

## Recently closed subjects (last 3)

- `deployment-stability-and-charted-telemetry-2026_07_17` (2026-07-17, Delivered): rc.12 fixes,
  URL/device telemetry, chart/history framework, reviews, specs, and publication completed.
- `post-refactor-debt-paydown-2026_07_14` (2026-07-15, Complete): residual security, correctness,
  documentation, and structural debt closed through fourteen reviewed plans.
- `framework-refactor-2026_07_13` (2026-07-14, Complete): unified compile architecture and repository
  framework restructuring completed.
