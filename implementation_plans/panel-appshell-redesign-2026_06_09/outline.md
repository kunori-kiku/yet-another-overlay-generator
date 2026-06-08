# Panel App-Shell Redesign — Outline

<!-- subject: panel-appshell-redesign · drafted 2026-06-09 · branch: per-phase feat branches off main -->

The durable spine for restructuring the YAOG operator panel (a PoC) into a dashboard **app-shell** with Apple-minimal styling, broader persistence, and refresh-surviving login. One milestone = one shippable, independently-reviewed PR. Approved plan mirror: `.claude/plans/valiant-wondering-crab.md`.

## 1. Mission

Turn the working-but-PoC operator panel into a proper dashboard **app-shell** — collapsible sidebar + top app bar + top-right user/theme menu + a selection-driven right aside — in refined **Apple-minimal** style (auto dark/light, theme-scoped accent, optional translucency), with broader client persistence and login that survives a page refresh.

Success criteria:
- Persistent app-shell chrome; sections are deep-linkable routes; node editing moves to a right aside (canvas is no longer the always-on default scene).
- Auto dark/light (system-following, top-right toggle override); refined accent (graphite dark / teal light); optional translucency (server-side setting, local fallback).
- Local/Controller **Mode** is persisted; fleet/settings paint instantly from non-secret caches; **Refresh** is a bottom submit-style action.
- Login survives refresh via an **httpOnly cookie** (cross-origin-capable) without ever persisting a token to web storage.
- **Zero** loss of existing functionality; EN/ZH parity; compiler/renderer/air-gap untouched.

## 2. Principles (executor ground truth)

Inherits `PRINCIPLES.md`. Subject-specific:

- **[STATED] Compiler/renderer/air-gap frozen — HIGH.** This is a frontend reorg + one scoped controller-auth change. Never alter compile/render/export byte-output. Violation: editing anything under `internal/compiler`, `internal/renderer`, `internal/artifacts`, or the air-gap install/deploy scripts.
- **[STATED] "Open redesign of flows" is UX-only — MEDIUM.** The user granted latitude to change frontend interactions, NOT to remove features or touch the freeze. Violation: dropping a topology-editing capability "to simplify"; reading "open redesign" as license to change backend behavior.
- **[INFERRED] Zero-knowledge token custody — HIGH.** `sessionToken`/`operatorToken`/CSRF token never reach `localStorage`/`sessionStorage`. Cookie auth keeps the session in an httpOnly cookie; login state derives from a server probe. Violation: adding any token to a persist `partialize`.
- **[INFERRED] Persisted-topology back-compat — MEDIUM.** Existing `topology-storage` / `controller-storage` localStorage must keep loading; new persisted fields additive/optional. Violation: renaming a persisted key; non-optional new field.
- **[INFERRED] Agent routes are machine-to-machine — HIGH.** `RegisterAgentRoutes` stays Bearer-only, no `cors()`, no cookies, no CSRF. Violation: wrapping agent routes in cors()/cookie auth.
- **[INFERRED] i18n parity — MEDIUM.** Every new string via `txt(language, zh, en)`; no hardcoded UI text.

## 3. Current state of the world (2026-06-09)

- Branch `main`; latest release `v2.0.0-preview.3` (controller-panel auth stack #38–#48 + #49 loopback bind + #50 README docker tutorial + #51 webauthn IP guard + #52 path-prefix hint). The controller-panel 2.0 program is checkpointed.
- Frontend: React 19 + Vite 8 + **Tailwind v4 CSS-first** (`@import "tailwindcss";`, no config file, no light mode) + Zustand 5 + `@xyflow/react` 12. No router (`viewMode` conditional render). ~312 `txt()` i18n call sites.
- Panel layout: `AppLayout.tsx` 5-zone; `RightPanel.tsx` is **1157 lines**; `DeployPanel.tsx` holds the Local/Controller `useState` + 7 deploy sub-components.

## 4. Must-read references

**Memory:** `controller-panel-2-trimmed-plan5-shipped.md`, `security-model-keystone.md`, `MEMORY.md`.
**Docs:** `PRINCIPLES.md`, `STATUS.md`, `docs/spec/controller/*.md`, `docs/design/controller-panel-design-spike-2026_06_07.md`.
**Frontend (read-first per phase):** `src/App.tsx`, `src/main.tsx`, `src/index.css`, `vite.config.ts`, `src/components/layout/AppLayout.tsx`, `LeftPanel.tsx`, `RightPanel.tsx` (1157 ln), `TopBar.tsx`, `BottomBar.tsx`, `src/components/canvas/TopologyCanvas.tsx`, `src/components/deploy/*.tsx`, `src/components/audit/AuditView.tsx`, `src/stores/topologyStore.ts` (persist partialize ~570–586), `src/stores/controllerStore.ts` (partialize ~691–704, memory-only tokens ~58–62), `src/i18n.ts`.
**Backend (P5):** `internal/api/handler_login.go` (`mintSessionResponse` ~167, `HandleLogout` ~206), `handler_passkey.go` (~405), `auth_controller.go` (`operatorAuth`/`bearerToken`/`resolveOperator` ~149–187), `handler_controller.go` (`cors()` Allow-Origin `*` ~194), `internal/controller/store.go` (`ControllerSettings` ~253–271) + `settings.go`, `handler_bootstrap.go` (`HandleSettings`), `cmd/server/main.go` (env wiring). **RE-VERIFY all line numbers — they drift.**
**Web:** Tailwind v4 dark-mode + `@theme` (tailwindcss.com/docs/dark-mode, /functions-and-directives#theme); React Router v7 (reactrouter.com/en/main); OWASP CSRF + Session cheatsheets; MDN SameSite + Access-Control-Allow-Credentials.

## 5. Standing rules

See `PRINCIPLES.md` (git author/no-force-push/local-gates) and memory. Per-PR cadence: implement → `cd frontend && npm run lint && npm run build` → CI → independent review workflow (≤5 agents, disjoint dimensions, skeptic-verify blocker/major) → squash-merge `--delete-branch`.

## 6. Decisions log (this session)

- **Preserve scope:** *Open redesign of flows* (frontend UX latitude; freeze + invariants still hold).
- **Routing:** *react-router-dom v7*.
- **Cookie auth:** *Cross-origin-capable* (credentialed CORS + `YAOG_PANEL_ORIGIN` allowlist + `SameSite=None;Secure` + double-submit CSRF; Bearer fallback).
- **Appearance:** theme = top-right toggle (client, per-device, system-following); **translucency = server-side `ControllerSettings` field** (local fallback in Local mode). Accent theme-scoped (graphite dark / teal light).
- **Subject:** `panel-appshell-redesign`.
- **Correction:** translucency is a panel-only setting — do NOT inject it into the agent bootstrap script (research conflated it).

## 7. Milestones

Each links its plan file. Goal / Proposed solution / Hazards / Verification / Stop-loss.

### M1 — Shell scaffold + theme foundation → `plan-1-2026_06_09.md`
- **Goal:** app-shell chrome + theme system, today's UI rendered unchanged inside it.
- **Solution:** add `react-router-dom`; `ThemeProvider` + Tailwind v4 `@theme`/`@custom-variant dark`/accent CSS vars; `Shell` (collapsible sidebar + top app bar + top-right theme/user menu + `<Outlet/>`); index route renders the existing `AppLayout` content. Persist `sidebarCollapsed` + `theme`.
- **Hazards:** Tailwind v4 dark-variant wiring; bundle already >500 KB; not breaking the existing canvas mount.
- **Verification:** `npm run lint && npm run build`; today's UI shows inside chrome; theme toggle flips light/dark/system; hard-refresh on a sub-URL resolves.
- **Stop-loss:** if router add is disruptive, land theme-only first, shell second.

### M2 — Sections → routes → `plan-2-2026_06_09.md`
- **Goal:** each scene is a deep-linkable route under the shell.
- **Solution:** routes `/overview /design /fleet /deploy /security /settings`; retire `viewMode`; sidebar/topbar `NavLink`s; scope `ReactFlowProvider` above `/design`.
- **Hazards:** canvas must mount only on `/design`; selection state across nav.
- **Verification:** deep-link each; canvas absent off `/design`.
- **Stop-loss:** keep a redirect from legacy state to routes.

### M3 — Right aside + canvas toolbar → `plan-3-2026_06_09.md`
- **Goal:** node manipulation → selection-driven right aside; canvas full-width when idle.
- **Solution:** extract `LeftPanel`/`RightPanel` editors into an aside; `[+Domain][+Node]` toolbar; Fleet node detail `/fleet/nodes/:id`; compile-preview → Deploy.
- **Hazards:** `RightPanel` is 1157 ln — extract without losing any editor/feature; `reconcileEdgeEndpoints` coupling.
- **Verification:** every editor reachable in the aside; nothing-selected → full canvas; manual parity pass vs old RightPanel.
- **Stop-loss:** `plan-3.5` splits node/edge/compile extraction.

### M4 — Persisted mode + caches + appearance → `plan-4-2026_06_09.md`
- **Goal:** mode persists; instant paint; appearance wired; Refresh is submit-style.
- **Solution:** add `mode`, `settings`, `nodes`, `lastSyncedAt`, `sidebarCollapsed`, `theme` to partializes (non-secret only); Appearance (theme client; translucency server `GET /settings` + local fallback); Refresh → bottom of Connection.
- **Hazards:** caches must be paint-only (never gate Deploy); no token persisted.
- **Verification:** reload restores mode/theme/fleet; DevTools shows no token in storage.
- **Stop-loss:** ship persistence without the appearance settings if server-side wiring slips (it depends on P5-adjacent settings route, but the GET path already exists).

### M5 — httpOnly-cookie auth (cross-origin) + CSRF + CORS → `plan-5-2026_06_09.md` ⚠️ RISKIEST
- **Goal:** login survives refresh; cross-origin-capable; XSS can't read the session.
- **Solution:** cookie set/read/clear on `mintSessionResponse` (covers password + passkey); double-submit CSRF; credentialed CORS via `YAOG_PANEL_ORIGIN` (no `*`+credentials; `Vary: Origin`); `SameSite=None;Secure` + `YAOG_SECURE_COOKIE`; frontend `credentials:'include'` + `X-CSRF-Token`; `selectLoggedIn` ← `GET /session` probe. Bearer/break-glass kept.
- **Hazards:** wildcard+credentials illegal; agent routes must stay cookie-free; same-origin Docker vs cross-origin.
- **Verification:** backend tests (cookie on every login path, CSRF reject, logout clears, origin reject, break-glass still works); manual refresh-survives.
- **Stop-loss:** `plan-5.5` → same-origin only (`SameSite=Strict`, no origin reflection).

### M6 — Apple-minimal polish + i18n + a11y → `plan-6-2026_06_09.md`
- **Goal:** the refined look + full localization + accessibility.
- **Solution:** hairlines/air/soft-elevation/translucency, quiet motion, EN/ZH for all new surfaces, empty/loading/error states, sidebar keyboard a11y.
- **Verification:** lint/build; no hardcoded strings; keyboard-navigable sidebar.
- **Stop-loss:** polish is incremental; ship per-section.

## 8. Insertion-point markers

- **plan-3.5** — `RightPanel` (1157 ln) extraction too risky in one pass → split node/edge/compile-preview.
- **plan-5.5** — cross-origin credentialed CORS raises a review concern → same-origin only.

## 9. Closure criteria

- [ ] M1–M6 merged to main, each independently reviewed.
- [ ] No existing feature lost; EN/ZH parity; compiler/renderer/air-gap untouched.
- [ ] No token in web storage (DevTools verified).
- [ ] Login survives refresh; dark/light/system + translucency work.
- [ ] STATUS.md updated; subject `git mv` to `_completed/` at closure.

## 10. Plan status

| Plan | Milestone | Status |
|---|---|---|
| plan-1 | Shell scaffold + theme | done — d1dadd6 (PR #53); review: 0 confirmed blocker/major, a11y+persistence fixups applied |
| plan-2 | Sections → routes | done — 86ec31e (PR #54); viewMode retired, DeployPanel decomposed losslessly, review 0 confirmed blocker/major |
| plan-3 | Right aside + toolbar | done — 286a3fb (PR #55); RightPanel/LeftPanel decomposed verbatim, no feature lost, /fleet/nodes/:id added; review 0 confirmed blocker/major; plan-3.5 not needed |
| plan-4 | Persisted mode + caches + appearance | pending |
| plan-5 | Cookie auth + CSRF + CORS | pending |
| plan-6 | Polish + i18n + a11y | pending |
