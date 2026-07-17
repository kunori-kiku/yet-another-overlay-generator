# STATUS
<!-- regenerated: 2026-07-17 -->
<!-- by: Codex -->

## Active work

- **Subject:** focused rc.14 Design-canvas scalability and readability release.
- **Branch:** `codex/canvas-readability-rc14`, based on published rc.13 commit `723d731`.
- **Current plan:** complete review-fix-re-review, merge the bounded frontend-only change, prove the
  required CI on the exact resulting `main`, then cut and verify `v2.0.0-rc.14` through the ordinary
  release workflow.
- **Latest completed work:** the canvas now permits a 0.1-percent overview zoom and has independent
  display-only controls for link endpoint addresses and node overlay IPs. Frontend lint, production
  build, the focused preference test, and the full 430-test Vitest suite pass locally.
- **Release candidate:** `v2.0.0-rc.14` is implemented and locally green, but remains **uncut** until
  review and the repository-required exact-commit checks complete.

## Open questions / blockers

- No implementation blocker remains. Publication waits for final review, merge, required PR/main
  checks, and the normal tag-time release transaction.
- The change is presentation-only: it does not alter topology DTOs, controller persistence,
  compilation, generated artifacts, deployment policy, or the default detailed canvas view.
- Any red required gate or pre-existing immutable rc.14 tag/release/image reference stops fresh
  publication and enters the documented recovery decision.

## Next actions

1. Finish the independent post-implementation reviews and address any actionable findings.
2. Commit and open the rc.14 PR; wait for every required check and merge it.
3. Prove local `main` exactly equals the successful `origin/main` revision with a clean worktree.
4. Run rc.14 tag/release/GHCR absence preflight and create one annotated tag at that main tip.
5. Monitor the release transaction and verify the 22 assets, GitHub Latest, and native amd64/arm64
   official image pointers.

## Recently closed subjects (last 3)

- `rc13-agent-selfupdate-health-retry` (2026-07-17, Delivered): retained the candidate and rollback
  breadcrumb across transient health failures and published rc.13.
- `deployment-stability-and-charted-telemetry-2026_07_17` (2026-07-17, Delivered): rc.12 fixes,
  URL/device telemetry, chart/history framework, reviews, specs, and publication completed.
- `post-refactor-debt-paydown-2026_07_14` (2026-07-15, Complete): residual security, correctness,
  documentation, and structural debt closed through fourteen reviewed plans.
