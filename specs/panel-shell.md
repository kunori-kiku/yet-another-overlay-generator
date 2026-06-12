# Panel app shell

<!-- last-verified: 2026-06-12 -->

## Responsibility
Mounts the persistent panel chrome (collapsible sidebar + top app bar) around deep-linkable routes, owning theme/translucency/sidebar preferences, the bilingual string layer, mode-aware navigation and landing, and login-state restore on mount.

## Files
- `frontend/src/App.tsx:24-46` — `createBrowserRouter` config: `Shell` layout route wrapping all pages; `IndexRedirect` (lines 16-19) for index and `*` fallback.
- `frontend/src/main.tsx:6-10` — entry point; renders `<App/>` under `StrictMode`.
- `frontend/index.html:10-31` — anti-FOUC inline script: applies `.dark`/`.no-translucency` to `<html>` pre-paint from persisted `ui-storage`.
- `frontend/src/components/shell/Shell.tsx:13-42` — layout grid, a11y skip-link, `<Outlet/>` main region, session-restore effect (lines 20-22).
- `frontend/src/components/shell/Sidebar.tsx:13-83` — collapsible left nav of `<NavLink>`s; fold button persists collapsed state via uiStore.
- `frontend/src/components/shell/Topbar.tsx:14-109` — active-section breadcrumb; Design-only import/export/flush cluster (lines 54-78); zh/en language toggle (lines 80-103); ThemeToggle + UserMenu.
- `frontend/src/components/shell/nav.ts:32-56` — `NAV_ITEMS` taxonomy (single source of truth) + `navItemsForMode`, `landingPathForMode`, `activeNavItem`.
- `frontend/src/components/shell/ThemeToggle.tsx:9-33` — top-right button cycling system → light → dark.
- `frontend/src/components/shell/UserMenu.tsx:10-58` — click-outside/Escape-dismiss account popover; contents still the P1 placeholder (lines 45-55).
- `frontend/src/components/shell/icons.tsx:9-129` — dependency-free inline SVG icon set (stroke, `currentColor`).
- `frontend/src/components/shell/styles.ts:5-6` — shared `FOCUS_RING` class fragment for consistent keyboard focus styling.
- `frontend/src/theme/ThemeProvider.tsx:22-42` — owns `.dark` and `.no-translucency` classes on `<html>`; live-tracks OS scheme while pref is `system`.
- `frontend/src/stores/uiStore.ts:27-55` — persisted zustand store (`ui-storage` key): `theme`, `sidebarCollapsed`, `translucency` + setters/togglers.
- `frontend/src/i18n.ts:1-148` — `UILanguage` (`'zh' | 'en'`), `detectSystemLanguage()`, `txt()`, and the shared `STRINGS` tuple table.

## Inputs
- **controllerStore** (see specs/panel-auth.md, specs/panel-deploy-fleet.md): `mode: 'local' | 'controller'` (`frontend/src/stores/controllerStore.ts:62`) drives nav filtering and landing; `checkSession(): Promise<void>` (`frontend/src/stores/controllerStore.ts:463-485`) is called by the shell's mount effect.
- **topologyStore** (see specs/panel-design.md): `language: UILanguage` + `setLanguage` (`frontend/src/stores/topologyStore.ts:46-47`), `exportProject(): void`, `importProject(file: File): Promise<void>`, `flushWorkspace(): void` (`frontend/src/stores/topologyStore.ts:95-98`) wired to the Topbar I/O cluster.
- **Browser**: `localStorage['ui-storage']` (read pre-paint by `frontend/index.html:14` and rehydrated by zustand `persist`, `frontend/src/stores/uiStore.ts:45`); `navigator.language` for the language default (`frontend/src/i18n.ts:3-8`, applied at `frontend/src/stores/topologyStore.ts:126`); `prefers-color-scheme` media query (`frontend/src/theme/ThemeProvider.tsx:5`).

Deep doc: `docs/spec/frontend/architecture.md` covers frontend state-management conventions, but its component-hierarchy section predates this shell (see Gotchas).

## Outputs
- **Route mount points** under the persistent shell (`frontend/src/App.tsx:28-43`): `/design` (panel-design, with `ReactFlowProvider` scoped to that route only), `/overview`, `/fleet`, `/fleet/nodes/:id`, `/deploy`, `/settings` (panel-deploy-fleet), `/security` (panel-auth).
- **`<html>` classes** `.dark` and `.no-translucency` (`frontend/src/theme/ThemeProvider.tsx:12,38`) consumed by the token CSS in `frontend/src/index.css`.
- **Shared utilities consumed by every panel sibling**: `txt(lang: UILanguage, zh: string, en: string): string` and `STRINGS` (`frontend/src/i18n.ts:10-12,16`); `FOCUS_RING` (`frontend/src/components/shell/styles.ts:5-6`); shell icons.
- **Nav helpers**: `navItemsForMode(mode: PanelMode): readonly NavItem[]` (`frontend/src/components/shell/nav.ts:42-44`), `landingPathForMode(mode: PanelMode): string` (`nav.ts:47-49`), `activeNavItem(pathname: string): NavItem | undefined` (`nav.ts:52-56`).
- **`useUiStore`** preference state (theme/translucency/sidebar) read by the Settings appearance section (see specs/panel-deploy-fleet.md).

## Decision points (if any)
- **Mode-aware landing**: index route and unknown paths redirect to `/overview` in controller mode, `/design` in local mode (`frontend/src/App.tsx:16-19,43`; `frontend/src/components/shell/nav.ts:47-49`).
- **Per-mode sidebar visibility**: local mode hides Overview and Fleet (`localVisible: false`); Security stays visible because it hosts local Compile History; hidden routes remain deep-linkable (`frontend/src/components/shell/nav.ts:25-29,42-44`).
- **Session restore gating**: `checkSession()` fires only when `mode === 'controller'`, on mount and on mode flips (`frontend/src/components/shell/Shell.tsx:20-22`); the genuine-cookie-vs-break-glass distinction is owned by panel-auth.
- **Design-only project I/O**: the import/export/flush cluster renders only when the active nav item is `design`; flush requires a `window.confirm` (`frontend/src/components/shell/Topbar.tsx:24,32-41,54-78`).
- **Theme resolution**: dark iff pref is `dark`, or pref is `system` and the OS prefers dark; the OS-change listener attaches only while pref is `system` (`frontend/src/theme/ThemeProvider.tsx:9-13,28-32`).

## Invariants
- uiStore persists only an explicit non-secret allowlist (`theme`, `sidebarCollapsed`, `translucency`) via `partialize` — deliberate guard so future fields can't silently leak to localStorage, aligned with the key-custody principle (PRINCIPLES.md "Key custody"; `frontend/src/stores/uiStore.ts:46-52`).
- The shell never touches tokens or credentials: login-state restore is delegated entirely to controllerStore's httpOnly-cookie probe (`frontend/src/components/shell/Shell.tsx:18-22`; see specs/panel-auth.md).
- `NAV_ITEMS` is the single source of truth for the section taxonomy — Sidebar links, Topbar breadcrumb, and landing logic all derive from it (`frontend/src/components/shell/nav.ts:12-14,32-39`).

## Gotchas (optional)
- In controller mode the **server** is the translucency authority: controllerStore's `refresh`/`loadSettings` overwrite `uiStore.translucency` from `ControllerSettings` (`frontend/src/stores/controllerStore.ts:292-294,313-315`); the local toggle stands only in local mode.
- The anti-FOUC script in `frontend/index.html:14-20` parses the persisted zustand JSON (`parsed.state.theme`, `parsed.state.translucency`) directly — renaming the `ui-storage` key or reshaping the store breaks pre-paint theming silently (ThemeProvider reconciles after mount, so it shows as a flash, not an error).
- UI language lives in **topologyStore** (persisted with the topology workspace), not uiStore — the Topbar toggle writes cross-store (`frontend/src/components/shell/Topbar.tsx:16-17`; `frontend/src/stores/topologyStore.ts:46-47,428`). Also note `docs/spec/frontend/architecture.md:12-22` still documents the retired `AppLayout`/`TopBar` hierarchy; trust this spec + code for shell structure.
