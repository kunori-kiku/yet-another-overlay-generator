# STATUS
<!-- regenerated: 2026-06-12 (controller-server-authority-redesign drafted) -->
<!-- by: draft-implementation-plan -->

## Active work

- **Subject:** `controller-server-authority-redesign-2026_06_12` — drafted, awaiting execution.
  Seven plans: backend prefix split + custody enforcement → topology version history → safety
  bugs (agent busy-loop, staged-bundle purge) → panel login gate + hydrate-on-login →
  mode-boundary custody → identity reconciliation → docs/migration/closure smoke. All ten
  user decisions locked in the outline's Decisions log; audit findings + fresh `specs/`
  (bootstrapped `1abd662`) are the ground truth.
- **Branch:** main (no active feature branch yet; plan-1 will branch).
- **Current plan:** `plan-1-2026_06_12.md` (pending).
- **Last shipped:** v2.0.0-preview.5 (UUID insecure-context fix `97f504a`), README config
  reference (`ab88799`), specs/ bootstrap (`1abd662`). The appshell redesign closed 2026-06-09
  (PRs #53–#58, see `_completed/panel-appshell-redesign-2026_06_09/CLOSURE.md`).

## Open questions / blockers

- **Release of the redesign is user-gated.** `main` is ahead of v2.0.0-preview.3 by the six
  app-shell PRs; cutting a `v*` tag (→ Release + Docker workflows) is an outward-facing call
  for the user.
- **Remaining Plan 5 (task #20)** stays GATED on user provider forks (multi-tenant / KMS /
  OIDC). See [[security-model-keystone]].

## Next actions

- **Execute plan-1:** backend prefix split (`YAOG_OPERATOR_PATH_PREFIX` /
  `YAOG_AGENT_PATH_PREFIX`, clean break) + 400-reject private-key-bearing topologies +
  missing audit entries + startup base-path log. Breaking for the live deployment — the plan
  carries the migration snippet.
- **Owed manual smoke (carried, now scheduled):** folded into plan-7's closure smoke —
  browser + authenticator + two-node controller deploy (use `http://localhost`, not
  `127.0.0.1`, for WebAuthn), extended with this subject's success criteria.
- **Candidate follow-up subject (carried):** full light-mode theming of the legacy
  topology/deploy forms.
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
