# Panel design

<!-- last-verified: 2026-07-17 -->

## Responsibility

Own the editable topology workspace, React Flow canvas, selection-driven domain/node/edge editors,
local Go/WASM validation/compile/export actions, and reconciliation of controller-authored allocation
results (`frontend/src/stores/topologyStore.ts:20-204`,
`frontend/src/components/pages/DesignPage.tsx:16-61`).

## Files

- `frontend/src/stores/topologyStore.ts:258-923` — owns topology CRUD, import/export, mode custody,
  compute actions, and workspace persistence.
- `frontend/src/components/canvas/TopologyCanvas.tsx:67-671` — maps topology state to interactive or
  read-only React Flow nodes and edges.
- `frontend/src/components/design/DesignAside.tsx:8-26` and
  `frontend/src/components/design/aside/DomainEditor.tsx:6-85` — route selection to domain editing.
- `frontend/src/components/design/aside/NodeEditor.tsx:22-556` and
  `frontend/src/components/design/aside/EdgeEditor.tsx:47-654` — edit node and link intent plus
  controller-derived allocation display.
- `frontend/src/lib/localEngine.ts:31-85` and `frontend/src/wasm/wasmEngine.ts:1-180` — lazily bridge
  store actions to the canonical Go/WASM engine.
- `frontend/src/types/topology.ts:3-231` — mirrors topology and compile/validation response DTOs.

## Inputs

The workspace receives user-created or imported topology intent, server-hydrated controller designs,
and server compile/deploy allocation results (`frontend/src/stores/topologyStore.ts:519-690`). Local
compute receives that topology through four fixed WASM adapters, while controller mode supplies its
compiled preview through `panel-deploy-fleet` (`frontend/src/lib/localEngine.ts:55-85`,
`frontend/src/stores/topologyStore.ts:289-291`).

## Outputs

`getTopology` emits project, domains, nodes, edges, and a positive allocation-schema version for
validation, Save, preview, or deploy (`frontend/src/stores/topologyStore.ts:519-532`). Local compile
adopts the normalized/allocated returned topology and a five-entry session history; local export and
deploy helpers become browser downloads (`frontend/src/stores/topologyStore.ts:743-873`). Server
reconciliation merges only the shared allocation-field catalog by edge identity
(`frontend/src/stores/topologyStore.ts:425-478`).

## Decision points (if any)

- Validation is key-free and always runs in-browser. Compile/export/deploy-script actions refuse in
  controller mode because they require private-key custody; controller compilation happens during
  authenticated preview/deploy (`frontend/src/stores/topologyStore.ts:720-873`).
- Below the desktop breakpoint, Design mounts a read-only pan/zoom canvas and no editor controls;
  desktop mounts toolbar, list drawer, mutable canvas, aside, and validation footer
  (`frontend/src/components/pages/DesignPage.tsx:21-61`).
- A canvas connection creates only logical edge intent and no allocated port; self-loops and duplicate
  enabled node pairs are rejected, while parallel links are added explicitly from the edge editor
  (`frontend/src/components/canvas/TopologyCanvas.tsx:522-552,593-636`,
  `frontend/src/stores/topologyStore.ts:408-424`).

## Invariants

- The browser does not implement a parallel TypeScript compiler or anonymous server fallback; all
  local computation crosses the lazily loaded Go/WASM seam
  (`frontend/src/lib/localEngine.ts:5-25,31-85`).
- Server-held controller canvases are disposable confidential mirrors: persistence substitutes an
  empty workspace, logout/mode transitions scrub custody state, and local work remains persistable
  (`frontend/src/stores/topologyStore.ts:611-708,878-915`).
- Selection is mutually exclusive and allocation pins remain compiler/controller authority rather
  than being synthesized from canvas geometry (`frontend/src/stores/topologyStore.ts:510-517`,
  `frontend/src/components/design/DesignAside.tsx:14-23`).

## Gotchas (optional)

- Local/AirGap work may persist its private keys because reproducible offline recompilation requires
  them; that data must be scrubbed when crossing into controller custody
  (`frontend/src/stores/topologyStore.ts:649-676,878-915`).
- Canvas positions and compile history are intentionally session-only, so refresh re-lays out nodes
  and retains only the topology/preferences allowlist (`frontend/src/components/canvas/TopologyCanvas.tsx:92-95,395-414`,
  `frontend/src/stores/topologyStore.ts:878-915`).
- Active telemetry policy is edited under Fleet node detail, not the Design aside; see
  `panel-telemetry` (`frontend/src/components/pages/FleetNodeDetailPage.tsx:192-244`).
