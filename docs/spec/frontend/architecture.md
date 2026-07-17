# Frontend architecture

<!-- last-verified: 2026-07-17 -->

The React/Vite frontend supports two workflows in one application:

- **Local design** runs validation, compilation, export, and deploy-script generation entirely in
  the browser through the Go/WASM build.
- **Controller mode** treats the server as the topology, fleet, deployment, settings, and
  authentication authority. Validation remains browser-local, while compile preview and
  stage/promote use authenticated operator endpoints.

There is no hand-maintained TypeScript compiler and no anonymous server compute fallback.

## Application composition

`frontend/src/App.tsx` composes the top level as:

```text
App
└── ErrorBoundary
    └── ThemeProvider
        └── RouterProvider
            └── Shell
                ├── controller session/login gate
                ├── Sidebar (desktop) or Drawer + Sidebar (mobile)
                ├── Topbar
                ├── custody/hydration notices
                └── Outlet
                    ├── DesignPage (route-scoped ReactFlowProvider)
                    ├── OverviewPage
                    ├── FleetPage / FleetNodeDetailPage
                    ├── DeployPage
                    ├── SecurityPage
                    └── SettingsPage
```

The Shell remains mounted across navigation, while route pages own their domain-specific layout.
Controller-only Overview and Fleet routes have render-time guards as well as mode-filtered
navigation. The Design route uses a full desktop editor; smaller viewports receive a read-only
canvas preview rather than partially mounting editing controls that do not fit.

See [the panel shell component spec](../../../specs/panel-shell.md) for the route, auth-gate, and
responsive-chrome contract.

## State ownership

The frontend deliberately uses three Zustand domain stores. They are not interchangeable and
should not be collapsed into one global store.

### `topologyStore`

`frontend/src/stores/topologyStore.ts` owns the editable graph and local compute workflow:

- project, domains, nodes, edges, and allocation schema version;
- language and canvas/selection preferences;
- validation and compile results plus loading/error state;
- local import/export/reset and graph CRUD;
- the latest five local compile snapshots in `history`.

The persistence allowlist includes the local workspace, language, allocation schema, provenance,
and interface-display preference. Compile/validation results, selection, loading flags, errors,
and compile history are volatile. History is therefore bounded in memory and not restored after a
reload (`frontend/src/stores/topologyStore.ts:788-805,881-929`).

`canvasFromServer` is a custody marker. While controller mode displays a server-authoritative
canvas, the persisted topology fields are replaced with empty defaults so public IPs, SSH targets,
and other fleet-confidential design data do not remain in localStorage. Logout/session loss and
mode transitions use the same provenance to scrub or reset the mirror. Local designs remain
persistable because they are user-owned local data.

### `controllerStore`

`frontend/src/stores/controllerStore.ts` composes one stable public store from focused slices under
`frontend/src/stores/controller/`:

- `auth.ts` — mode, login/session, logout, authenticated controller version/capabilities, and
  auth-derived state;
- `fleet.ts` — node/audit views, enrollment, revocation, rekey, and live reads;
- `deploy.ts` — server compile preview, deploy preview, stage, and promote;
- `keystone.ts` — public credential metadata and signing workflow;
- `settings.ts` — controller settings and rollout configuration;
- `sync.ts` — server topology hydration, save/import, diffing, and mode-boundary reconciliation.

All slices share one `create(...persist(...))` boundary. The single auditable persistence allowlist
lives in `frontend/src/stores/controller/persist.ts`; slice files must not invent their own
persistence. It permits endpoints, mode, a scrubbed non-secret fleet cache, settings, sync time,
and public keystone identifiers. It excludes operator/session/CSRF tokens, signing operations,
preview results, transient errors/loading, and raw live telemetry. Auth secrets stay in memory or
httpOnly cookies.

Fleet owns active telemetry. The registry and node detail join configured `telemetry_probes` and
`telemetry_devices` with live `probeResults`, `deviceInventory`, and `deviceSamples`. The detail page
co-locates the hand-edited policy, whole-design Save/conflict flow, latest status, and
component-local charts; Design's properties aside does not own this operational editor. The typed
probe editor supports ICMP/TCP host destinations and an HTTP(S) URL plus expected success status.
The device control is one automatic-discovery opt-in (`all-eligible-v1`), not a browser-authored
hardware list.

Each probe may have a presentation-only name, with immutable ID as fallback. Result matching and
history requests use ID plus the exact typed destination and, for URL probes, expected status. Names
are absent from both executable policy members, so a name-only Save does not restage a bundle. Save
is a topology-draft mutation and may preserve an unfinished destination row; Deploy is the separate
keystone-sign-and-activate boundary, and preview/stage blocks only when that invalid draft belongs to
a ready node. Before every successor-bearing Save, the store refreshes the authenticated session and
requires controller capability `telemetry-policy-v2-topology`; an older controller is refused rather
than allowed to canonicalize URL/device fields away.

Normal Deploy also requires each affected managed node's latest authenticated capabilities. The
structured `telemetry_policy_upgrade_required` dialog offers the explicit `upgrade-agents-first`
path: preserve the complete saved draft and deploy compatible legacy policy while listing
`telemetry_policy_omitted_node_ids`. A configured signed rollout upgrades only its covered nodes;
absent or partial coverage produces a non-blocking warning, and uncovered agents must be updated out
of band. After every affected node has advertised the required capabilities through authenticated
telemetry, a normal Deploy activates the retained URL/device policy. The compatibility mode is never
an implicit fallback.

Probe latency and availability use the shared exact-series history chart. Device charts reuse that
framework for filesystem-used percentage, disk read/write rate, disk-I/O-busy percentage, GPU
utilization, and GPU-VRAM-used percentage. Actual URL status and categorical device inventory/provider
status remain live context rather than artificial numeric series. Missing values remain chart gaps;
zero is plotted when it was genuinely measured. Fetched history, probe results, device inventory,
and device samples never enter the Zustand persistence boundary. Both Fleet routes share a
non-overlapping ten-second Live scheduler and one feedback control that exposes in-flight refresh,
last-success age, next refresh, hidden-tab pause, and stale/error state. The Live/manual path fetches
only the node observation endpoint; full audit, Settings, and keystone hydration remain foreground or
security/bootstrap work. Fleet freshness has its own transient clock, so an unrelated topology save
cannot make telemetry appear current.

The audit view consumes the complete raw controller chain and uses its server verification result for
the integrity badge. For compatibility, raw legacy `action:"report"` rows remain in that chain; only
after full-chain verification does the component filter those noisy historical rows from the visible
operator table. New routine agent reports update Fleet state and do not create audit rows.

### `uiStore`

`frontend/src/stores/uiStore.ts` owns shell-only presentation state: theme, sidebar collapse,
effective and local translucency, the ephemeral mobile drawer, and the shared in-memory Fleet Live
toggle. Its `partialize` allowlist persists only non-secret preferences; the open drawer and Live
observation state are intentionally transient.

## Compute integration

`topologyStore.validate()` calls the Go/WASM validator in both modes because schema and semantic
validation require no private key. The browser does not call a controller validation endpoint.

In local mode, the remaining compute actions call adapters in `frontend/src/lib/localEngine.ts`:

- `compile()` runs the complete air-gap compile path, writes compiler allocations and generated
  keys back into the local workspace, and records a volatile history snapshot;
- `exportArtifacts()` downloads the per-node bundle ZIP;
- `downloadDeployScript()` downloads the selected project-level deploy script.

Those actions have controller-mode refusal guards because air-gap compilation reconstructs or
generates private keys. They also re-check the mode after an in-flight compile before accepting the
result, preventing a local private-key result from landing after a switch into controller mode
(`frontend/src/stores/topologyStore.ts:749-813`).

In controller mode, `controllerStore` sends a public-keys-only canvas to authenticated operator
routes. Compile preview invokes the server's `AgentHeld` subgraph compile and places the returned
placeholder-only `CompileResponse` into `topologyStore` for display. Deploy saves the authoritative
topology, previews/stages bundles, and promotes separately. Local `validate()` still runs against
the same model before those operations.

Both modes ultimately execute the Go `internal/localcompile` facade. The permanent WASM/golden and
wire-drift tests guard parity; adding a second frontend compiler or a silent server fallback would
break that architecture.

## Type and wire boundaries

The TypeScript model and controller DTOs live under `frontend/src/types/`, principally
`topology.ts` and `controller.ts`. Go remains authoritative for compile behavior and JSON
semantics. `internal/wiredrift/drift_test.go` structurally compares the hand-mirrored wire fields,
including `omitempty` behavior and nested response types, so a backend DTO change must update its
TypeScript mirror deliberately.

Backend failures use the coded `{error:{code,message,params}}` envelope. Validation findings carry
their own code and params in the successful validation response. Components resolve both through
the keyed catalog in `frontend/src/i18n/index.ts`, with complete English messages and per-key
Chinese fallback; they should not parse English error text.

## Compiled values are outputs, never frontend derivations

The backend is the sole authority for WireGuard interface names, allocated ports, transit
addresses, and link-local addresses. UI code must consume them from `CompileResponse` and compiler
write-backs rather than reconstructing naming or allocation algorithms.

`frontend/src/lib/compiledInterfaces.ts` is the shared interface-display resolver used by node and
edge views. It parses the authoritative interface name from a `wireguard_configs` key of the form
`<nodeID>:<interfaceName>`, parses the config's `ListenPort`, and matches that port to the edge's
pinned endpoint. Backup-link names can contain hashed suffixes, so stripping `wg-`, truncating a
node name, or reproducing the Go naming function is not valid (`compiledInterfaces.ts:1-127`).

This consumption rule also applies to newly exposed compiled fields: extend the Go response, its
wire-drift mirror, and a shared frontend resolver instead of growing view-local heuristics.

## Custody and persistence invariants

- The controller canvas is a disposable, non-persisted mirror once it is server-authoritative.
- Private WireGuard keys exist only in local air-gap state or downloaded local artifacts; they are
  stripped/refused at controller boundaries.
- Controller auth secrets are never in a Zustand persistence allowlist. Browser sessions use
  httpOnly cookies; break-glass bearer material is memory-only.
- Live peer/probe telemetry, device inventory/samples, and fetched history are display-only and
  removed from the persisted fleet cache.
- Every persisted store has an explicit allowlist. Adding a field to a store does not implicitly
  authorize writing it to localStorage.
- Mode transitions are security boundaries: they scrub keys, allocations, compile results/history,
  and server-derived data before a workspace becomes persistable local state.

## Extension guidance

When adding frontend behavior:

1. Put state in the store that owns its domain; keep page-local state local when it need not survive
   navigation.
2. Review the relevant `partialize` function explicitly. Persistence is a custody decision, not a
   convenience default.
3. Use controller client helpers and coded errors for network operations; do not issue ad-hoc fetches
   or parse server prose in components.
4. Use the Go/WASM local engine for local compute and authenticated controller actions for fleet
   compute. Do not duplicate compile/validation rules in TypeScript.
5. Consume backend-derived identifiers and allocations through shared helpers.
6. Add both English and Chinese catalog entries for new user-facing strings and keep Go/TypeScript
   DTO mirrors covered by wire-drift tests.
