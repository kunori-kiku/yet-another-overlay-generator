# Closure — panel-appshell-redesign

**Closed 2026-06-09. All six phases merged to `main`, each independently reviewed.**

Restructured the YAOG operator panel from a fixed 5-zone PoC into a dashboard app-shell
with Apple-minimal theming, broader non-secret persistence, and refresh-surviving login —
with zero loss of existing functionality.

## Shipped (PRs)

| Plan | PR | Commit | What |
|---|---|---|---|
| P1 | #53 | d1dadd6 | App-shell scaffold + theme system (react-router v7; `Shell` = collapsible sidebar + top app bar + `<Outlet/>`; Tailwind v4 class dark-mode + `@theme` tokens + theme-scoped accent; `ThemeProvider` + anti-FOUC; `uiStore`). Today's UI rendered unchanged inside the chrome. |
| P2 | #54 | 86ec31e | Sections → deep-linkable routes (`/overview /design /fleet /deploy /security /settings`); `viewMode` retired; `DeployPanel` decomposed (ConnectionSettings + LocalDeploy extracted); `TopBar` absorbed into the shell; `AuditView`→"Compile History"; memory-only `mode` lifted to the store. |
| P3 | #55 | 286a3fb | Node manipulation → selection-driven right **aside** (DomainEditor/NodeEditor/EdgeEditor extracted **verbatim** from the 1156-line RightPanel); canvas toolbar (create forms + list drawer + Compile); compile-preview → `/deploy`; `/fleet/nodes/:id` detail; RightPanel/LeftPanel/AppLayout deleted. |
| P4 | #56 | ca2f3da | Persisted `mode` + non-secret fleet caches (advisory, fail-closed); Appearance (theme + translucency); mode-aware nav + landing; Refresh → bottom submit. |
| P5 | #57 | ee2b353 | httpOnly session cookie + double-submit CSRF + credentialed CORS (`YAOG_PANEL_ORIGIN`/`YAOG_SECURE_COOKIE`) + `GET /session` probe + server-side `ControllerSettings.Translucency`. The one backend phase; agent routes untouched; zero-knowledge custody preserved. |
| P6 | #58 | a63c177 | Vibrancy (`.app-chrome` + solid fallbacks) + reduced-motion guard + `@layer base` focus baseline + skip-link + AuditView EN/ZH i18n sweep. |

## Process

Per-PR: structure-aware implement → `cd frontend && npm run lint && npm run build` (Go
`vet`/`test` locally + CI) → independent adversarial review **workflow** (4–5 disjoint
dimensions, FINDINGS severity, blocker/major skeptic-verified) → fix-ups → squash-merge
`--delete-branch`. Every PR merged with **0 confirmed blockers/majors**. Notable caught-and-fixed:
P5 break-glass-login-state regression (gate `loggedIn` on a non-empty CSRF token) and the
legacy `Translucency` `*bool` migration; P6 `@layer base` double-focus fix.

## Deliberate scope / follow-ups (not defects)

- **Dark workspace:** chrome follows auto dark/light; the topology/deploy editing canvas
  stays dark (pro-tool look). Full light theming of the legacy forms is a candidate
  follow-up subject (mechanical recolor).
- **Cross-origin panel** needs `YAOG_PANEL_ORIGIN` (+ HTTPS); same-origin Docker needs none.
- **No release tag** was cut (outward-facing; left to the user). Changes are on `main`.

## Owed manual smoke (carried from the keystone program)
- Browser + authenticator + two-node deploy of the controller stack (use `http://localhost`,
  not `127.0.0.1`, for WebAuthn). Verify: login survives refresh (cookie), CSRF, dark/light/
  translucency, no token in `localStorage`.
