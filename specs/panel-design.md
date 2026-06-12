# Panel — Design (topology state + canvas)

<!-- last-verified: 2026-06-12 -->

## Responsibility
Holds the editable topology (project/domains/nodes/edges) as a zustand store persisted to localStorage, and renders it as an interactive React Flow canvas with selection-driven editors, calling the air-gap compile/validate API.

## Files
- `frontend/src/stores/topologyStore.ts:1-580` — zustand store: topology slices, CRUD, selection, compile/validate/export fetches, import/export, persist middleware (key `topology-storage`).
- `frontend/src/lib/uuid.ts:1-16` — `uuid()`: `crypto.randomUUID` with RFC-4122 v4 fallback for non-secure contexts (HTTP over LAN).
- `frontend/src/components/pages/DesignPage.tsx:1-42` — `/design` route layout: toolbar, optional lists drawer, canvas, aside, BottomBar footer.
- `frontend/src/components/canvas/TopologyCanvas.tsx:1-621` — store↔React Flow sync, drag positions, dagre auto-layout, connect gesture, parallel-edge fan-out, role chips, focus de-emphasis.
- `frontend/src/components/canvas/CustomNode.tsx:1-149` — role-colored node card with node-level handles and optional compiled-interface chips.
- `frontend/src/components/canvas/CustomEdge.tsx:1-237` — bezier edge with curvature offset for parallel edges, port badge, pending-dashed style, ★/bN/duplicate role chip.
- `frontend/src/components/design/CanvasToolbar.tsx:1-52` — create entry points (DomainForm/NodeForm), lists-drawer toggle, Compile button.
- `frontend/src/components/design/ElementsLists.tsx:1-32` — Domains & Nodes lists for the drawer.
- `frontend/src/components/design/DesignAside.tsx:1-26` — selection-driven right aside; null when nothing selected.
- `frontend/src/components/design/aside/DomainEditor.tsx:1-99` — domain name/CIDR/transit-CIDR/routing/allocation editor.
- `frontend/src/components/design/aside/NodeEditor.tsx:1-497` — node editor: role/capabilities, public endpoints (+edge reconciliation), extra prefixes, pinned key, SSH config.
- `frontend/src/components/design/aside/EdgeEditor.tsx:1-385` — edge editor: type/endpoint/transport/role/priority/weight, backup-link derivation, pinned-allocation display + unpin.
- `frontend/src/components/domains/DomainForm.tsx:1-115` — collapsible domain create form with CIDR regex validation.
- `frontend/src/components/domains/DomainList.tsx:1-58` — drag-to-reorder, click-to-select domain list.
- `frontend/src/components/nodes/NodeForm.tsx:1-281` — collapsible node create form; parses `host:port` public address (UX-5).
- `frontend/src/components/nodes/NodeList.tsx:1-58` — drag-to-reorder, click-to-select node list.

## Inputs
- Types `Topology`/`ValidateResponse`/`CompileResponse`/`CompileHistoryEntry` from `frontend/src/types/topology.ts:3-156` (wire contract: `docs/spec/api/wire-contract.md`).
- HTTP responses from `POST /api/validate|/api/compile|/api/export|/api/deploy-script` (routes `internal/api/server.go:62-65`; see specs/airgap-api.md, specs/model-validation.md, specs/compiler-allocation.md).
- Rehydrated state from localStorage key `topology-storage` (`frontend/src/stores/topologyStore.ts:565`); imported project JSON files (`:339-370`).
- Shared resolvers: `resolveNodeInterfaces` / `resolveEdgeInterface` (`frontend/src/lib/compiledInterfaces.ts:48,103`) map compiled `wireguard_configs` back to edges via pinned ports; `deriveCapabilitiesFromRole` (`frontend/src/lib/roleCapabilities.ts:11`) mirrors backend role inference.
- Mounted under a route-scoped `ReactFlowProvider` in `frontend/src/App.tsx:32-34` (see specs/panel-shell.md).

## Outputs
- `getTopology(): Topology` (`frontend/src/stores/topologyStore.ts:318-326`) — the JSON body POSTed to all four air-gap endpoints and serialized by `exportProject` (`:328-337`).
- Persisted slices (partialize `:567-577`): project, domains, nodes, edges, allocSchemaVersion, language, showInterfaces.
- Store actions consumed outside Design: `exportProject`/`importProject`/`flushWorkspace` by Topbar (`frontend/src/components/shell/Topbar.tsx:18-20`, see specs/panel-shell.md); `validate`/`validateResult` by BottomBar (`frontend/src/components/layout/BottomBar.tsx:5`); `exportArtifacts`/`downloadDeployScript` by LocalDeploy and `history`/`clearHistory` by AuditView (see specs/panel-deploy-fleet.md).

## Decision points
- **alloc_schema_version round-trip (Spec E R0):** sent only when >0 (never compiled ⇒ omitted) (`topologyStore.ts:318-326`); compile re-absorbs the compiler-written value and replaces all four slices with the returned topology, pushing a history snapshot capped at 5 (`:478-488`).
- **Import sanitization (D45/D55):** non-empty `route_policies` are stripped from imported files with a visible error message, not silently dropped (`topologyStore.ts:344-363`).
- **Connect gesture:** new edge gets `endpoint_host` from the target's first public endpoint, `endpoint_port` left empty so the backend allocates; id is `edge-${uuid()}` (D17: timestamp ids collided) (`TopologyCanvas.tsx:485-511`). `isValidConnection` rejects self-loops and duplicate enabled node-pairs in either direction (`:549-562`).
- **Pending (dashed) edge:** edges with `endpoint_host` test `compiled_port`; passive edges test pin fields instead, else they would stay dashed forever (`TopologyCanvas.tsx:228-246`).
- **Dial-affecting edits clear `compiled_port` (D19):** edge type, endpoint host/port, and role changes all reset it so the dashed "recompile needed" state reappears (`EdgeEditor.tsx:76-91,100-115,132,145-155,230-239`).
- **Backup links:** `addBackupEdge` copies intent fields only (from/to/type/transport/endpoint_host), never ports or pins (`topologyStore.ts:250-271`; `docs/spec/data-model/edge.md`). Button hidden when the edge touches a client node or is itself backup (`EdgeEditor.tsx:42-50`); nudge shown when a backup shares its primary's endpoint host (`:53-63`).
- **Cascade deletes:** `removeDomain` removes its nodes and their edges (`topologyStore.ts:172-192`); `removeNode` removes incident edges (`:218-226`).
- **Endpoint reconciliation:** renaming a node's public host rewrites snapshotted `endpoint_host` on inbound edges and clears `compiled_port`; removing the host clears the endpoint entirely (back to backend auto-resolve) (`topologyStore.ts:290-310`, triggered `NodeEditor.tsx:33-35,59-62`).
- **Role change re-derives capabilities (D54):** client forces `has_public_ip=false` (`NodeEditor.tsx:130-142`); NodeForm derives `has_public_ip` from a non-empty top-level public address (`NodeForm.tsx:69-75`).
- **Default-domain seeding (UX-3):** fresh workspaces get `domain-default` at 10.20.0.0/24 (avoids the 10.10.0.0/24 transit pool); rehydration overrides it for existing projects (`topologyStore.ts:107-136,393-409`).
- **Edge role chips:** per undirected node-pair — single edge: no chip; backups: b1,b2…; extra same-direction roleless/primary edges: `duplicate` (mirrors backend D71); representative: ★ (`TopologyCanvas.tsx:93-137`).

## Invariants
- Selection is mutually exclusive: each of `selectNode`/`selectEdge`/`selectDomain` clears the other two (`topologyStore.ts:313-315`); DesignAside renders exactly one editor (`DesignAside.tsx:15-23`).
- Backend is the sole port/interface authority (PRINCIPLES.md "Backend is the sole port authority"): the frontend never stamps `endpoint_port` at draw time (`TopologyCanvas.tsx:496-506`) and never reconstructs interface names — it resolves them from compiled output via pinned ports (`EdgeEditor.tsx:171-184`, Decisions #12).
- Volatile state (isCompiling, errors, results, history, selection) is excluded from persistence by `partialize` (`topologyStore.ts:566-577`).

## Gotchas
- localStorage persists the whole `nodes` slice, so pinned WireGuard private keys (`fixed_private_key`) live in browser storage and exported JSON — acknowledged in `docs/spec/frontend/architecture.md` ("Secret material — explicit"); any docs claim of public-keys-only persistence is drift and not enforced in code.
- Compile `history` snapshots and canvas drag positions are session-only: history is not in `partialize`, and positions live in a `positionMap` ref (`TopologyCanvas.tsx:80`), so a refresh re-grids nodes (`:188-206`).
- Interfaces are compile artifacts, not drawing primitives: handles are node-level and `onConnect` ignores handle ids (`CustomNode.tsx:14-17`); the `showInterfaces` toggle is display-only (`topologyStore.ts:36-40`).
