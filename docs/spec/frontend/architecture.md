# Frontend Architecture

## State Management

The Zustand store (`topologyStore.ts`) is the single source of truth:
- **Persisted** (localStorage): `project`, `domains`, `nodes`, `edges`, `language`
- **Volatile**: `compileResult`, `validateResult`, `isCompiling`, `isValidating`, `error`,
  `viewMode`, selection state, `history`

## Component Hierarchy

```
App
└── AppLayout
    ├── TopBar            (project name, compile/validate/export buttons, language toggle)
    ├── LeftPanel          (domain list, node list, CRUD forms)
    ├── TopologyCanvas     (React Flow graph editor)
    │   ├── CustomNode     (per-node visual representation with per-peer interface handles)
    │   └── CustomEdge     (connection line with endpoint/port labels)
    ├── RightPanel         (selected item properties editor)
    └── BottomBar          (validation/compile results, error display)
```

## TypeScript Types

Frontend types in `types/topology.ts` mirror the Go backend model exactly. Key response types:
- `ValidateResponse`: `{ valid, errors?, warnings? }`
- `CompileResponse`: `{ topology, wireguard_configs, babel_configs, sysctl_configs, install_scripts, deploy_scripts, manifest }`
- `CompileHistoryEntry`: Stores up to 5 recent compilation snapshots

## Compute integration

Design compute is invoked from the Zustand store actions. In **local mode** these run entirely in the
browser on the Go/WASM engine (`frontend/src/lib/localEngine.ts` → `frontend/src/wasm/`) — there is no
backend round-trip and no anonymous compute route (the four `/api/{validate,compile,export,deploy-script}`
routes were removed; see [../operations/deployment-topology.md](../operations/deployment-topology.md)):
- `validate()` — schema + semantic validation
- `compile()` — full compile (updates topology state from the result, saves to history)
- `exportArtifacts()` — export ZIP (downloads via blob URL)
- `downloadDeployScript(format)` — deploy script (`sh` | `ps1`)

In **controller mode**, design compute is the operator-gated controller path (`HandleCompilePreview` /
`HandleStage`); LOCAL design still runs in-browser on the WASM engine.

### Consume compiled names — never reconstruct them

The backend is the sole authority for WireGuard interface names (and every other compiled,
name-derived value). The frontend MUST consume these names from the compile response and MUST NOT
reconstruct them. Specifically:

- Per-peer WireGuard config keys in `CompileResponse.wireguard_configs` have the form
  `"<nodeID>:<interfaceName>"`. To display or look up a peer's compiled interface name, the frontend
  MUST parse the `interfaceName` out of that key, not recompute it from the node name.
- The frontend MUST NOT reimplement the `wgInterfaceName` algorithm (see
  [../artifacts/naming.md](../artifacts/naming.md)). That algorithm has a hashed branch for names
  longer than 12 characters that plain client-side truncation cannot reproduce; any reconstruction
  diverges from the backend for such names.

> **Compliance:** the RightPanel "Compiled values" lookup rebuilds the interface name client-side as
> `` `wg-${toNode.name.toLowerCase().replace(/[^a-z0-9-]/g, '-')}`.slice(0, 15) `` at
> `frontend/src/components/layout/RightPanel.tsx:622`, so the lookup silently misses for any node name
> longer than 12 characters. Closed by Plan 4 (PR #6).
