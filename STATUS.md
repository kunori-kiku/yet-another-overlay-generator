# STATUS
<!-- regenerated: 2026-06-14 (login-gate + namespace-split hardening merged) -->
<!-- by: follow-up hardening (post controller-server-authority-redesign) -->

## Active work

- **Subject:** none. `controller-server-authority-redesign-2026_06_12` closed **delivered**
  (archived to `implementation_plans/_completed/`). Since then, four owner-raised concerns
  were fixed as standalone, independently-reviewed PRs (no new subject folder):
  - **#66** (merge `768211d`) — login-gate hardening: the controller login gate was only a
    client-side render check, not a data boundary (server-hydrated design persisted at rest +
    survived logout, readable via "switch to local" or devtools); now a provenance flag
    (`canvasFromServer`) keeps server-held design out of localStorage and flushes it on
    logout/gate, while preserving the operator's own local work. Plus: passkey button no longer
    dead-disabled before username entry; browser tab title fixed.
  - **#67** (merge `5025de3`) — **BREAKING** API namespace split: `/api/v1/controller/` →
    `/api/v1/operator/` (panel, :8080) + `/api/v1/agent/` (nodes, :9090), so the two surfaces
    split by path (expose only the agent endpoint publicly, keep the panel behind a VPN).
- **Branch:** `main` (no active feature branch; all merged + deleted).
- **Last shipped (to `main`):** PR #67 namespace split (`5025de3`, 2026-06-14). NOT yet released
  (the last tag, `v2.0.0-preview.6`, predates #66 and #67).

## Open questions / blockers

- **Release pending (user-gated).** `main` is ahead of `v2.0.0-preview.6` by #66 + #67, and #67
  is a **breaking** path change (enrolled agents must re-bootstrap). Cutting a `v*` tag triggers
  the Release + Docker workflows — an outward-facing call for the user. A `preview.7` would carry
  the login-gate hardening + the namespace split.
- **Live deployment migration is user-owed.** `overlay.kunorikiku.com` needs: the env rename
  (`YAOG_CONTROLLER_PATH_PREFIX` → operator/agent pair; server fails loud on the old one) AND,
  after #67, re-bootstrapping nodes onto the new `/api/v1/agent/` path. Steps:
  `docs/MIGRATION-controller-server-authority.md` (§1 env, §1b namespace).

## Next actions

- **Cut `v2.0.0-preview.7`?** (user decision) — bundles #66 (login-gate hardening) + #67
  (breaking namespace split); release notes must point at the migration guide.
- **Migrate the live controller (owed):** env rename + re-bootstrap nodes onto `/api/v1/agent/`;
  confirm the startup log names both base paths; re-login; update proxy/tunnel path rules to the
  two new namespaces (`/<op-prefix>/api/v1/operator/*` → :8080, `/<agent-prefix>/api/v1/agent/*` → :9090).
- **Manual browser two-node smoke (owed, carried):** browser + authenticator + two real nodes on
  `http://localhost` (not `127.0.0.1` — WebAuthn). Verify SC1 (cache-clear → login → hydrated
  canvas), SC5 (persisted controller mode → full-page login), SC6 (controller→local warns +
  preserves graph + purges secrets), SC8 (fleet "not in design" markers + one-click revoke),
  login-survives-refresh, dark/light, no token in localStorage.
- **Candidate follow-up subject (carried):** full light-mode theming of the legacy
  topology/deploy editing forms (mechanical recolor).

## Recently closed subjects (last 3)

- [controller-server-authority-redesign-2026_06_12](implementation_plans/_completed/controller-server-authority-redesign-2026_06_12/CLOSURE.md)
  — 7 plans (+1.5), PRs #59–#65 (delivered): controller mode made server-authoritative —
  server-authoritative cache + login gate + hydrate, operator/agent prefix split, enforced key
  custody (400 + canonical storage), version history (N=10), safety bugs (orphan-agent idle,
  promote scoping), identity reconciliation. Followed by hardening PRs #66/#67 (above).
- [panel-appshell-redesign-2026_06_09](implementation_plans/_completed/panel-appshell-redesign-2026_06_09/CLOSURE.md)
  — 6 PRs: operator panel → dashboard app-shell (routes, selection aside, Apple-minimal
  auto dark/light + vibrancy, persisted mode/caches, httpOnly-cookie refresh-surviving login).
- [mimic-tcp-transport-2026_06_07](implementation_plans/_completed/mimic-tcp-transport-2026_06_07/outline.md)
  — 3 stacked PRs: transport:"tcp" wraps links with mimic (eBPF UDP→fake-TCP) for UDP-hostile networks.
