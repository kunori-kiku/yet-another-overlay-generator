# STATUS
<!-- regenerated: 2026-06-14 (controller-server-authority-redesign closed) -->
<!-- by: close-phase -->

## Active work

- **Subject:** none. `controller-server-authority-redesign-2026_06_12` closed
  **delivered** on 2026-06-14 and was archived to `implementation_plans/_completed/`.
  No subject is in flight — the next session should draft a new subject (or the user
  may pick up one of the carried follow-ups below).
- **Branch:** `main` (no active feature branch; all plan branches merged + deleted).
- **Current plan:** all done — the closed subject's seven plans (+1.5) shipped as PRs
  #59–#65.
- **Last shipped:** controller server-authority redesign — controller mode is now
  server-authoritative end-to-end (server-authoritative cache + hydrate-on-login,
  operator/agent prefix split, enforced zero-knowledge key custody at the API boundary,
  bounded topology version history, orphan-agent idle + promote scoping, identity
  reconciliation). Final PR #65 (merge `24f044e`, 2026-06-14): docs accuracy + breaking
  migration note + a real rekey-refusal audit gap fixed.

## Open questions / blockers

- **Live deployment migration is user-owed.** `overlay.kunorikiku.com` still runs the old
  single `YAOG_CONTROLLER_PATH_PREFIX`; it must be renamed to the operator/agent pair and
  operators re-logged-in. The server now **refuses to start** if the old env is still set.
  Migration steps: `docs/MIGRATION-controller-server-authority.md`.
- **Release of the redesign is user-gated.** `main` is well ahead of the last `v*` tag;
  cutting a tag (→ Release + Docker workflows) is an outward-facing call for the user.

## Next actions

- **Migrate the live controller (owed):** apply the env rename + `docker compose up -d`,
  confirm the startup log names both base paths, re-login, and update proxy/tunnel rules to
  route the operator prefix → :8080 and the agent prefix → :9090.
- **Manual browser two-node smoke (owed, carried since the keystone program):** browser +
  authenticator + two real nodes on `http://localhost` (not `127.0.0.1` — WebAuthn). Verify
  SC1 (cache-clear → login → hydrated canvas), SC5 (persisted controller mode → full-page
  login), SC6 (controller→local warns + preserves graph + purges secrets), SC8 (fleet
  "not in design" markers + one-click revoke), login-survives-refresh, dark/light, no token
  in localStorage.
- **Candidate follow-up subject (carried):** full light-mode theming of the legacy
  topology/deploy editing forms (mechanical recolor).

## Recently closed subjects (last 3)

- [controller-server-authority-redesign-2026_06_12](implementation_plans/_completed/controller-server-authority-redesign-2026_06_12/CLOSURE.md)
  — 7 plans (+1.5), PRs #59–#65 (delivered): controller mode made server-authoritative —
  server-authoritative cache + login gate + hydrate, operator/agent prefix split (clean
  break), enforced key custody (400 + canonical storage), version history (N=10), safety
  bugs (orphan-agent idle, promote scoping), identity reconciliation. Each PR independently
  reviewed (find → adversarial verify → fix).
- [panel-appshell-redesign-2026_06_09](implementation_plans/_completed/panel-appshell-redesign-2026_06_09/CLOSURE.md)
  — 6 PRs: operator panel → dashboard app-shell (routes, selection aside, Apple-minimal
  auto dark/light + vibrancy, persisted mode/caches, httpOnly-cookie refresh-surviving login).
- [mimic-tcp-transport-2026_06_07](implementation_plans/_completed/mimic-tcp-transport-2026_06_07/outline.md)
  — 3 stacked PRs: transport:"tcp" wraps links with mimic (eBPF UDP→fake-TCP) for UDP-hostile networks.
