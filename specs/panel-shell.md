# Panel app shell

<!-- last-verified: 2026-07-15 -->

## Responsibility

Keep one persistent, mode-aware frame around the routed panel: authentication gating, responsive
navigation, active-route chrome, appearance and language controls, import/export affordances, and
custody notices. The shell coordinates domain stores but does not own topology, controller, or
credential state itself.

## Files

- `frontend/src/App.tsx:18-73` defines the browser router, mode-aware landing, controller-only deep
  link guard, route-scoped `ReactFlowProvider`, error boundary, and theme provider.
- `frontend/src/components/shell/Shell.tsx:14-140` performs session restoration and renders the
  login/splash gate or the persistent sidebar/drawer/topbar/notices/outlet layout.
- `frontend/src/components/shell/Sidebar.tsx:10-114` shares one navigation body between the
  collapsible desktop sidebar and always-expanded mobile drawer.
- `frontend/src/components/shell/Topbar.tsx:14-125` derives the breadcrumb and owns mode-aware design
  import/export/flush controls plus language, theme, and user menus.
- `frontend/src/components/shell/nav.ts:12-57` is the single navigation taxonomy and landing helper.
- `frontend/src/components/shell/UserMenu.tsx:8-113` shows local mode, signed-in identity and expiry,
  break-glass state, controller version, and sign-out.
- `frontend/src/theme/ThemeProvider.tsx:7-42` owns the `.dark` and `.no-translucency` document classes;
  `frontend/index.html:9-31` seeds them before first paint.
- `frontend/src/stores/uiStore.ts:4-92` owns non-secret shell preferences and ephemeral mobile-drawer
  state.
- `frontend/src/i18n/index.ts:1-87` and `frontend/src/i18n/messages/{en,zh}.ts` provide the keyed,
  typed catalog, interpolation, English fallback, and coded-error localization.

## Router and mode boundaries

`App` renders `ErrorBoundary -> ThemeProvider -> RouterProvider`. The router mounts `Shell` once and
places route content in its `<Outlet>`. `/design` alone receives a `ReactFlowProvider`; the other
pages do not initialize the canvas runtime (`frontend/src/App.tsx:36-61`).

The index route and wildcard redirect to `/overview` in controller mode and `/design` in local
mode. Overview, Fleet, and fleet-node detail are controller-only not just in the sidebar: their
elements are wrapped in `RequireControllerMode`, so a local-mode deep link redirects instead of
rendering stale cached controller UI (`frontend/src/App.tsx:18-34,52-58`). Design, Deploy, Security,
and Settings remain reachable in both modes and gate their own mode-specific content.

`NAV_ITEMS` supplies paths, icons, labels, and local visibility. Sidebar links, Topbar active-route
labels, and landing behavior derive from that table rather than maintaining parallel route lists
(`frontend/src/components/shell/nav.ts:33-57`).

## Authentication gate

Local mode enters the shell directly. Controller mode first probes the httpOnly session cookie via
`checkSession`. Until that probe settles, the shell renders a quiet full-viewport status splash;
this prevents a protected canvas or login-page flash. An unauthenticated operator sees `LoginPage`
before any chrome renders. A configured in-memory break-glass bearer passes the gate without being
misrepresented as a login session (`frontend/src/components/shell/Shell.tsx:18-82`).

The requested route remains in the router while the gate is closed, so a valid session resumes the
original deep link. Switching away from and back to controller mode resets and reruns the probe
instead of trusting stale login state.

The shell never reads or persists session credentials. It consumes derived auth state from
`controllerStore`; cookie handling, CSRF, bearer headers, and logout remain in the auth/client
layer. `UserMenu` distinguishes a named session from break-glass recovery and exposes session
expiry, server build version, and sign-out only where meaningful.

## Responsive chrome and design surface

At the `lg` breakpoint and above, the docked Sidebar can persist an expanded or icon-only width.
Below it, the docked sidebar is hidden and Topbar opens an off-canvas Drawer containing the same
navigation body. `mobileNavOpen` is ephemeral and omitted from persistence, so reload cannot
restore a blocking overlay (`frontend/src/components/shell/Shell.tsx:84-107` and
`frontend/src/stores/uiStore.ts:19-24,80-89`).

The Design route is deliberately not a compressed desktop editor. Below `lg` it mounts a
read-only pan/zoom canvas with a gate; at desktop width it mounts the toolbar, optional elements
list, editable canvas, selection aside, and validation footer
(`frontend/src/components/pages/DesignPage.tsx:12-60`).

The shell includes a keyboard skip link, semantic navigation and main regions, accessible drawer
labels, route-aware `NavLink` current-state behavior, and shared focus-ring styling.

## Design import/export controls

Topbar shows the project I/O cluster only on Design. Export downloads the current design in either
mode. Import is custody-aware:

- Local mode loads a local draft through `topologyStore.importProject`.
- Controller mode confirms replacement, strips non-authoritative key material, writes a new server
  topology version, and rehydrates from server through `controllerStore.importDesignToServer`.

Flush is local-only. In controller mode clearing the disposable browser mirror would neither
delete nor undo the authoritative server design, so presenting it as a destructive action would be
misleading (`frontend/src/components/shell/Topbar.tsx:30-43`).

Shell-level notice banners surface when a server hydration replaces a local design, when a
controller import drops design key material, or when a local import clears unusable stranded
public-key-only state (`frontend/src/components/shell/Shell.tsx:109-133`).

## Appearance, language, and persistence

`uiStore` persists an explicit non-secret allowlist: `theme`, `sidebarCollapsed`, effective
`translucency`, and `localTranslucency`. `mobileNavOpen` is excluded. ThemeProvider applies the
resolved system/light/dark preference and vibrancy class; the inline document script reads the
same store shape before React mounts to avoid a theme flash.

Controller mode treats server settings as the effective translucency authority, while
`localTranslucency` retains the user's independent local preference. Returning to local mode
restores that preference instead of inheriting the server's fleet setting
(`frontend/src/stores/uiStore.ts:25-41,60-64`).

UI language remains part of the topology workspace and is consumed explicitly by components. The
catalog is keyed by the complete English key set; Chinese is additive and falls back per key.
`tError` and `tValidationError` localize coded backend and validation errors without ad-hoc string
parsing (`frontend/src/i18n/index.ts:11-87`).

## Invariants and gotchas

- The auth gate encloses all shell chrome in controller mode; logged-out narrow viewports do not
  leak a sidebar or drawer.
- Controller-only visibility is enforced by router guards as well as navigation filtering.
- The shell persists preferences only. Credentials and topology/controller domain state stay in
  their dedicated stores and custody allowlists.
- The anti-FOUC script depends on the `ui-storage` key and its persisted field names; a store-shape
  migration must update both the store and inline bootstrap.
- Import/export semantics are mode-aware even though their controls occupy the same Topbar slots.

Deep documentation: [frontend architecture](../docs/spec/frontend/architecture.md).
