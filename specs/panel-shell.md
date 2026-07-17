# Panel shell

<!-- last-verified: 2026-07-17 -->

## Responsibility

Boot the React panel, select the reachable local/controller workflow, gate controller sessions and
routes, and keep shared navigation, appearance, language, and routed content in one persistent frame
(`frontend/src/main.tsx:1-10`, `frontend/src/App.tsx:18-73`,
`frontend/src/components/shell/Shell.tsx:14-140`).

## Files

- `frontend/src/main.tsx:1-10` — mounts the shared React application entry into the browser root.
- `frontend/src/App.tsx:18-73` — defines mode-aware landing, controller-only route guards, route
  ownership, the design-only React Flow provider, and top-level error/theme boundaries.
- `frontend/src/components/shell/Shell.tsx:14-140` — gates controller sessions and renders the
  responsive chrome, notices, and route outlet.
- `frontend/src/components/shell/nav.ts:15-57` — provides the shared route taxonomy, per-mode
  visibility, landing path, and active-route lookup.
- `frontend/src/components/shell/Sidebar.tsx:26-114` — renders one mode-filtered navigation body in
  desktop and drawer hosts.
- `frontend/src/components/shell/Topbar.tsx:18-125` — renders route context, responsive navigation,
  mode-aware design import/export controls, and shared language/theme/account controls.
- `frontend/src/components/pages/SettingsPage.tsx:13-107,172-214` — exposes runtime mode and
  appearance selection while hiding the mode switch from local-only builds.
- `frontend/src/stores/uiStore.ts:4-99` — owns non-secret shell preferences and volatile drawer and
  Fleet-live state behind an explicit persistence allowlist.
- `frontend/src/lib/deployMode.ts:31-54` — converts `VITE_LOCAL_ONLY` into the typed build-mode
  descriptor and fixes the local compute engine to Go/WASM.
- `frontend/src/theme/ThemeProvider.tsx:7-42` and `frontend/index.html:9-31` — synchronize theme and
  translucency classes after mount and before first paint.
- `frontend/src/i18n/index.ts:11-46,59-105` — defines typed language keys, English fallback,
  interpolation, and coded-error localization.
- `frontend/package.json:6-17` — exposes the default build and the `VITE_LOCAL_ONLY` static build
  from the same source package.

## Inputs

The browser bootstrap supplies `<App />`, while the build supplies `VITE_LOCAL_ONLY` through
`deployMode(): DeployMode`; both default and local-only artifacts therefore enter through the same
React tree (`frontend/src/main.tsx:1-10`, `frontend/src/lib/deployMode.ts:40-54`,
`frontend/package.json:7-12`).

The composed controller store supplies workflow mode; [panel authentication](panel-auth.md) supplies
`checkSession()`, derived login state, and the in-memory break-glass token; [panel design](panel-design.md)
supplies language and import notices; `uiStore` supplies responsive and appearance state
(`frontend/src/components/shell/Shell.tsx:25-37`).

## Outputs

The shell routes local design work to [panel design](panel-design.md) and controller Fleet/deploy
work to [panel deploy and Fleet](panel-deploy-fleet.md) (`frontend/src/App.tsx:39-60`). The Fleet
node route embeds [panel telemetry](panel-telemetry.md) without moving telemetry state into the shell
(`frontend/src/components/pages/FleetNodeDetailPage.tsx:192-244,306-319`).

It produces the persistent sidebar/topbar/notice frame around `<Outlet />` and applies `.dark` and
`.no-translucency` document classes for every route (`frontend/src/components/shell/Shell.tsx:84-138`,
`frontend/src/theme/ThemeProvider.tsx:22-42`).

## Decision points (if any)

- A truthy `VITE_LOCAL_ONLY` makes controller mode unreachable; otherwise the all-in-one build lets
  the operator select local or controller mode (`frontend/src/lib/deployMode.ts:40-54`,
  `frontend/src/components/pages/SettingsPage.tsx:82-107`).
- Runtime mode chooses the landing path, visible navigation, and whether Overview/Fleet deep links
  render or redirect (`frontend/src/components/shell/nav.ts:33-50`, `frontend/src/App.tsx:18-34,52-58`).
- Local mode enters the frame directly; controller mode first shows a session-check splash, then
  either [panel authentication](panel-auth.md) or the routed frame (`frontend/src/components/shell/Shell.tsx:39-84`).

## Invariants

- Controller-only pages are protected by route guards as well as hidden navigation, so a local-mode
  deep link cannot expose cached controller UI (`frontend/src/App.tsx:24-34,52-54`,
  `frontend/src/components/shell/nav.ts:26-30,42-45`).
- The local-only build hides its controller affordance and the store-side transition refuses the
  same mode even when called programmatically (`frontend/src/components/pages/SettingsPage.tsx:82-107`,
  `frontend/src/stores/controller/sync.ts:383-413`).
- Browser persistence remains allowlisted by domain: shell storage keeps only non-secret
  preferences, controller storage omits credentials and strips live telemetry, and a server-held
  topology mirror is blanked before topology persistence (`frontend/src/stores/uiStore.ts:73-96`,
  `frontend/src/stores/controller/persist.ts:14-45`,
  `frontend/src/stores/topologyStore.ts:876-908`).

## Gotchas (optional)

- Mode switching is intentionally asymmetric: controller-to-local flushes or purges according to
  canvas provenance, while local-to-controller preserves valid local keypairs, clears stranded keys,
  and drops the local compile result (`frontend/src/stores/controller/sync.ts:339-413`).
- The pre-paint script and `uiStore` must keep the `ui-storage`, `theme`, and `translucency` names
  aligned or the initial frame can disagree with `ThemeProvider` (`frontend/index.html:9-31`,
  `frontend/src/stores/uiStore.ts:73-96`).
- Language is a topology-workspace preference while theme/chrome preferences live in `uiStore`, so
  shell controls deliberately read two stores (`frontend/src/components/shell/LanguageToggle.tsx:1-35`,
  `frontend/src/components/shell/ThemeToggle.tsx:1-32`,
  `frontend/src/stores/topologyStore.ts:901-907`).
