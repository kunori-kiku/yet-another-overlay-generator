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

## API Integration

All API calls are made from the Zustand store actions:
- `validate()` → `POST /api/validate`
- `compile()` → `POST /api/compile` (updates topology state from response, saves to history)
- `exportArtifacts()` → `POST /api/export` (downloads ZIP via blob URL)
- `downloadDeployScript(format)` → `POST /api/deploy-script?format=sh|ps1`
