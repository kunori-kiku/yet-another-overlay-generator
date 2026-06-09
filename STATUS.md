# STATUS
<!-- regenerated: 2026-06-09 (panel-appshell-redesign closed) -->
<!-- by: execute-implementation-plan / close-phase -->

## Active work

- **None active.** The `panel-appshell-redesign-2026_06_09` subject is **COMPLETE** —
  all six phases merged to `main` (PRs #53–#58), each independently reviewed. Archived to
  `implementation_plans/_completed/panel-appshell-redesign-2026_06_09/` (see `CLOSURE.md`).
- **Branch:** main (no active feature branch).
- **Last shipped (on main, untagged):** the operator-panel app-shell redesign —
  - P1 #53 `d1dadd6` shell scaffold + theme · P2 #54 `86ec31e` sections→routes ·
    P3 #55 `286a3fb` right aside + toolbar · P4 #56 `ca2f3da` persisted mode + caches +
    appearance · P5 #57 `ee2b353` httpOnly-cookie auth + CSRF + CORS · P6 #58 `a63c177`
    polish + i18n + a11y.
- **Last release tag:** v2.0.0-preview.3 (2026-06-09, pre-redesign). The redesign is on
  `main` only — no tag cut (outward-facing; left to the user).

## Open questions / blockers

- **Release of the redesign is user-gated.** `main` is ahead of v2.0.0-preview.3 by the six
  app-shell PRs; cutting a `v*` tag (→ Release + Docker workflows) is an outward-facing call
  for the user.
- **Remaining Plan 5 (task #20)** stays GATED on user provider forks (multi-tenant / KMS /
  OIDC). See [[security-model-keystone]].

## Next actions

- **Owed manual smoke (carried):** browser + authenticator + two-node controller deploy
  (use `http://localhost`, not `127.0.0.1`, for WebAuthn). Verify the redesign end-to-end:
  login survives refresh (cookie), CSRF, dark/light/system + translucency, no token in
  `localStorage`; and the keystone passkey deploy.
- **Optional (user-gated):** cut a release tag for the redesign once the smoke passes.
- **Candidate follow-up subject:** full light-mode theming of the legacy topology/deploy
  forms (the editing canvas is intentionally dark today — a mechanical recolor across the
  deploy/* + aside/* components).
- **Cross-origin panel deployments** must set `YAOG_PANEL_ORIGIN` (+ HTTPS); same-origin
  Docker needs no config (`docs/spec/controller/operator-auth.md`).

## Recently closed subjects (last 3)

- [panel-appshell-redesign-2026_06_09](implementation_plans/_completed/panel-appshell-redesign-2026_06_09/CLOSURE.md)
  — 6 PRs: operator panel → dashboard app-shell (routes, selection aside, Apple-minimal
  auto dark/light + vibrancy, persisted mode/caches, httpOnly-cookie refresh-surviving login).
- [mimic-tcp-transport-2026_06_07](implementation_plans/_completed/mimic-tcp-transport-2026_06_07/outline.md)
  — 3 stacked PRs: transport:"tcp" wraps links with mimic (eBPF UDP→fake-TCP) for UDP-hostile networks.
- [parallel-links-and-babel-failover-2026_06_07](implementation_plans/_completed/parallel-links-and-babel-failover-2026_06_07/outline.md)
  — 3 stacked PRs: per-edge link identity, babel cost failover, focus-transparency UX.
