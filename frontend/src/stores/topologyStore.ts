import { create } from 'zustand';
import { persist } from 'zustand/middleware';
import type {
  Topology,
  Project,
  Domain,
  Node,
  Edge,
  ValidateResponse,
  CompileResponse,
  CompileHistoryEntry,
} from '../types/topology';
import { detectSystemLanguage, t, tError, type MessageKey, type UILanguage } from '../i18n';
import { uuid } from '../lib/uuid';
import { healCollidingPins, sanitizeLinkDirection } from '../lib/normalizeEdges';
import { dropAllKeys } from '../lib/custody';
import { parseContentDispositionFilename, triggerBrowserDownload } from '../lib/download';
// The local-engine seam (plan-6, milestone 1.6). localEngineEnabled() is the SINGLE decision
// point this store consults; the four adapters bridge the air-gap action shapes onto the
// plan-4 TS compiler (drift-pinned by the plan-5 conformance harness). See the seam docstring
// below ALLOCATION_PIN_FIELDS.
import {
  localEngineEnabled,
  localValidate,
  localCompile,
  localExport,
  localDeployScripts,
} from '../compiler/localEngine';
// useControllerStore is read LAZILY (getState() inside actions, never at module
// init) so the controller↔topology store cycle stays runtime-only — symmetric to how
// controllerStore reads useTopologyStore.getState(). Needed for mode-aware import
// custody (plan-5, D5).
import { useControllerStore } from './controllerStore';

// The server-derived per-edge allocation fields: the compiled_port echo plus the six
// pinned_* ports / transit IPs / link-locals. mergeServerAllocations overlays exactly
// these, and controllerStore imports this same constant for its save-time conflict-check
// pin set (canonicalDesignIgnoringPins) — single source of truth, no hand-synced copy.
// KEEP IN SYNC only with EDGE_OMITEMPTY's pin entries in controllerStore.ts (a superset
// that also carries non-pin fields, so it cannot share this array directly).
export const ALLOCATION_PIN_FIELDS = [
  'compiled_port',
  'pinned_from_port',
  'pinned_to_port',
  'pinned_from_transit_ip',
  'pinned_to_transit_ip',
  'pinned_from_link_local',
  'pinned_to_link_local',
] as const;

// ── The local-engine seam (plan-6, milestone 1.6) ──
//
// The four compute actions below — validate(), compile(), exportArtifacts(),
// downloadDeployScript() — share ONE decision shape, deliberately NOT four scattered
// branches (which is how F3-class drift creeps in). They split into two kinds:
//
//   - validate() is KEY-FREE (schema + semantic only), so it runs the in-browser TS validator
//     (localValidate) ALWAYS in controller mode — browser-local verify, the controller never calls
//     /api/validate — and in local mode whenever `localEngineEnabled()`. It has no controller-mode
//     refusal guard, because a public-keys-only canvas validates identically.
//   - compile() / exportArtifacts() / downloadDeployScript() need PRIVATE keys (key generation /
//     bundling), so they keep a controller-mode REFUSAL guard FIRST (controller mode is
//     zero-knowledge — the controller compiles server-side on Deploy), then the local-engine arm.
//
// `localEngineEnabled()` (default-ON, plan-7 Phase 0.5 — true unless the typed VITE_YAOG_LOCAL_ENGINE
// flag is explicitly 'backend') ⇒ call the localEngine adapter (localValidate / localCompile /
// localExport / localDeployScripts), running the plan-4 TS compiler in the browser, byte-pinned to
// the Go pipeline by the green plan-5 conformance harness. The air-gap fetch branches survive ONLY as
// the explicit 'backend' escape hatch and ONLY in LOCAL mode — functional solely against a
// `-tags airgap` oracle, since plan-7 gates those routes off the default controller build (a shipped
// controller 404s /api/validate). Controller mode keeps its verify off the wire entirely: no
// anonymous server-side validation endpoint is reachable, minimizing the controller's attack surface.

interface TopologyState {
  // Data
  project: Project;
  domains: Domain[];
  nodes: Node[];
  edges: Edge[];
  // Allocation-scheme version number (Spec E rule R0): written by the compiler and
  // round-tripped/persisted verbatim. Default 0 means not yet compiled; getTopology only
  // echoes it when >0, to avoid polluting a never-compiled topology.
  allocSchemaVersion: number;

  // History snapshots
  history: CompileHistoryEntry[];

  // Compile/validate results
  validateResult: ValidateResponse | null;
  compileResult: CompileResponse | null;
  isCompiling: boolean;
  isValidating: boolean;
  error: string | null;

  // Canvas UI preference: whether to expand the compiled interface details on node cards
  // (display only). Interfaces are a compile artifact, not a drawing primitive — the
  // connect gesture is always node-to-node, and ports are allocated by the backend.
  // So interface details are collapsed by default and expanded on demand, to avoid
  // misleading the user into thinking "connecting a line = selecting an interface/port".
  showInterfaces: boolean;
  setShowInterfaces: (show: boolean) => void;

  // Selection state
  selectedNodeId: string | null;
  selectedEdgeId: string | null;
  selectedDomainId: string | null;
  language: UILanguage;
  setLanguage: (lang: UILanguage) => void;

  // Project operations
  setProject: (project: Partial<Project>) => void;

  // Domain CRUD
  addDomain: (domain: Domain) => void;
  updateDomain: (id: string, updates: Partial<Domain>) => void;
  removeDomain: (id: string) => void;
  reorderDomains: (sourceId: string, targetId: string) => void;

  // Node CRUD
  addNode: (node: Node) => void;
  updateNode: (id: string, updates: Partial<Node>) => void;
  removeNode: (id: string) => void;
  reorderNodes: (sourceId: string, targetId: string) => void;

  // Edge CRUD
  addEdge: (edge: Edge) => void;
  // Creates a backup link (role: 'backup') for the given primary-link edge: copies
  // from/to/type/transport/endpoint_host, but not the ports or any pin (a backup link has
  // its own independent allocation, re-pinned per edge by the backend). After appending,
  // selects the new edge and returns its id; returns null when primaryEdgeId does not exist.
  // See docs/spec/data-model/edge.md (§Parallel links).
  addBackupEdge: (primaryEdgeId: string) => string | null;
  updateEdge: (id: string, updates: Partial<Edge>) => void;
  // mergeServerAllocations overlays the server's per-edge compiled allocation (the
  // server-derived compiled_port echo + the six pinned_* ports / transit IPs / link-locals)
  // onto the matching canvas edges BY EDGE ID, leaving every other edge field and the current
  // selection / compile history untouched (no full reload). Used after a deploy/compile so the
  // operator immediately SEES the allocated internal port + IP — the value a NAT port-forward
  // must target. Two modes:
  //   - baseEdges OMITTED: unconditional — the server is authoritative, mirror all allocation
  //     fields verbatim (including a cleared pin). For deploy/compile reconciliation.
  //   - baseEdges GIVEN: conditional non-clobber adoption — adopt a server pin ONLY when
  //     neither the canvas NOR the last-synced base had one (i.e. the server ADDED it). An
  //     operator-set or operator-unpinned value (canvas or base defined) is preserved. For
  //     save-time, so even a force-Save never drops a NAT pin the operator never saw.
  mergeServerAllocations: (serverEdges: Edge[], baseEdges?: Edge[]) => void;
  removeEdge: (id: string) => void;
  // When a node's public_endpoints host changes/is removed, reconcile the endpoint_host
  // snapshotted on the edges that point at it.
  reconcileEdgeEndpoints: (
    nodeId: string,
    oldHost: string,
    newHost: string | null
  ) => void;

  // Selection
  selectNode: (id: string | null) => void;
  selectEdge: (id: string | null) => void;
  selectDomain: (id: string | null) => void;

  // API operations
  validate: () => Promise<void>;
  compile: () => Promise<void>;
  // setCompileResult overwrites the compile result directly (without running the local
  // air-gap compile()). Controller mode uses it to surface the SERVER's compile-preview
  // result (PR6) — which carries placeholder private keys only — so CompilePreview and the
  // EdgeEditor "compiled values" block render the controller's authoritative compiled output.
  setCompileResult: (result: CompileResponse | null) => void;
  exportArtifacts: () => Promise<void>;
  downloadDeployScript: (format: 'sh' | 'ps1') => Promise<void>;

  // Utilities
  getTopology: () => Topology;
  // fromServer marks whether this load came from a controller server-side hydration (as
  // opposed to a local import/snapshot): a design pulled from the server contains
  // confidential fleet data (public IPs / SSH targets) that must never be persisted to
  // disk or left behind after logout in controller mode. See the canvasFromServer security
  // invariant.
  loadTopology: (topo: Topology, fromServer?: boolean) => void;
  // Canvas provenance (security invariant): true means the current canvas content is
  // "server-authoritative confidential data" — written either by a server hydration
  // (loadTopology(topo,true)) or marked after a deploy() pushes it to the server (post-deploy,
  // the local canvas equals the server-authoritative copy); subsequent local edits are still
  // treated as derived confidential data. When true in controller mode: (1) it is not
  // persisted to localStorage (partialize blanks it out), (2) it is cleared on logout/session
  // invalidation (controllerStore), (3) "switch back to local" from the login gate while
  // logged out is a full canvas reset rather than a save. Original local work
  // (import/new/reset) sets it false and persists normally — that is the user's own data.
  // Known edge cases (all minor, deliberate trade-offs): (a) a byte-identical "empty to empty"
  // no-op hydration returns early and does not change the provenance flag — but at that point
  // local and server are identical, so no secret can leak; (b) a "controller-mode draft" that
  // is neither deployed nor server-derived is marked false so an unsaved draft survives logout
  // (cost: visible in the logged-out state on this machine, lower sensitivity than deployed
  // fleet data); once deployed it flips to true and becomes protected.
  canvasFromServer: boolean;
  setCanvasFromServer: (v: boolean) => void;
  reset: () => void;
  // Mode-boundary scrub (plan-5, D6): called on a controller→local switch. The graph
  // (project/domains/node identity/edges) is kept, but all key material and compile artifacts
  // are cleared — private/public keys, overlay_ip, the edge's compiled_port and pinned_*
  // allocations, alloc_schema_version, and compile history/results. The next local compile
  // regenerates a clean set of keys and allocations. The switch is lossy, so the caller must
  // get user confirmation first.
  purgeModeBoundaryState: () => void;
  // clearStrandedKeys clears the key material of every node that has a WireGuard PUBLIC key but no
  // PRIVATE key — the "pinned pubkey, no privkey" state that the stateless air-gap compiler cannot
  // render (GenerateKeys case (b)) and that would otherwise force the operator to un-pin node by node.
  // It drops wireguard_public_key + the fixed_private_key flag (the private key is already absent), so
  // the node regenerates a fresh pair on the next local compile. Valid full keypairs (private key
  // present) are LEFT INTACT, preserving the local/air-gap round-trip-keys feature. Returns the count.
  clearStrandedKeys: () => number;
  // Controller-mode import: count of nodes whose design key material (private + public + pin) was
  // dropped, because the controller is server-authoritative for keys (agents register their public
  // keys; the design's keys are non-authoritative + confusing). 0 = no notice. Shell shows a
  // dismissible banner. (Local-mode import uses importClearedKeys below instead.)
  importKeysDropped: number;
  // Local-mode import: count of nodes whose stranded pubkey-only keys were cleared (0 = no notice).
  importClearedKeys: number;
  dismissImportNotice: () => void;
  exportProject: (filename?: string) => void;
  importProject: (file: File) => Promise<void>;
  clearHistory: () => void;
  flushWorkspace: () => void;
}

const defaultProject: Project = {
  id: 'project-1',
  name: 'New Project',
  version: '0.1.0',
};

// UX-3: a new workspace is seeded with one default network domain so the first-time user
// trying to "connect two public servers" can add nodes immediately without first having to
// understand CIDR / allocation mode (removing the opaque "create a domain first" prerequisite).
// The CIDR is 10.20.0.0/24 — deliberately avoiding 10.10.0.0/24 (the transit address pool),
// which would otherwise collide with each link's transit IP. transit_cidr is left empty so the
// backend keeps using its 10.10.0.0/24 default.
// Each call returns a fresh object/array so a shared reference cannot be mutated in place by
// later state changes.
const defaultDomainId = 'domain-default';

function makeDefaultDomains(): Domain[] {
  return [
    {
      id: defaultDomainId,
      name: 'overlay',
      cidr: '10.20.0.0/24',
      allocation_mode: 'auto',
      routing_mode: 'babel',
    },
  ];
}

const defaultLanguage: UILanguage = detectSystemLanguage();

// readApiErrorMessage extracts a LOCALIZED human message from a non-OK API response,
// tolerating a body that is NOT the JSON error envelope — e.g. an HTML 502/504 from a
// reverse proxy, a CSRF/auth redirect, or an empty body. A raw `await res.json()` threw
// a SyntaxError on such bodies, which the outer catch then masked behind a generic
// fallback, hiding the real HTTP status. Read the body once as text; if it is JSON with
// an `error` field, localize it through tError (shape-tolerant: today's {error:string}
// AND the coded {error:{code,message,params}} envelope plan-2 introduces); otherwise
// fall back to a status-qualified message.
async function readApiErrorMessage(res: Response, fallbackKey: MessageKey, lang: UILanguage): Promise<string> {
  const text = await res.text().catch(() => '');
  if (text) {
    try {
      const data = JSON.parse(text);
      if (data && (data as { error?: unknown }).error !== undefined) {
        return tError(data, lang);
      }
    } catch {
      // Body is not JSON (proxy HTML, plain text, truncated) — fall through.
    }
  }
  // Non-JSON body: a localized per-action fallback (keyed, so it respects the UI
  // language) qualified by the HTTP status.
  const status = res.status ? `${res.status}${res.statusText ? ' ' + res.statusText : ''}` : '';
  const base = t(lang, fallbackKey);
  return status ? `${base} (${status})` : base;
}

// localEngineErrorMessage produces a LOCALIZED message for an error surfaced by a compute
// action (plan-6, R6), so the LOCAL-engine message is identical to the server path for the
// same failure. The TS compiler throws a CompileError whose `code`/`params` mirror the Go
// apierr codes byte-for-byte; wrapping it in the coded `{ error: { code, message, params } }`
// envelope routes it through the SAME tError → 'error.<code>' catalog the server response
// would have hit (so e.g. a transit-pool-exhausted local compile reads identically to the
// server's). An error WITHOUT a string `code` (notably the Error thrown on the backend-fetch
// path, whose message is already the localized readApiErrorMessage output) is passed through
// verbatim, leaving the proven server path byte-unchanged; a code-less, message-less error
// falls back to the keyed per-action message.
function localEngineErrorMessage(err: unknown, fallbackKey: MessageKey, lang: UILanguage): string {
  const code = (err as { code?: unknown } | null | undefined)?.code;
  if (typeof code === 'string' && code) {
    const params = (err as { params?: Record<string, string> }).params;
    const message = err instanceof Error ? err.message : undefined;
    return tError({ error: { code, message, params } }, lang);
  }
  return err instanceof Error && err.message ? err.message : t(lang, fallbackKey);
}

export const useTopologyStore = create<TopologyState>()(
  persist(
    (set, get) => ({
      // Initial data
      // UX-3: seed the default network domain (see the makeDefaultDomains comment). An already
      // persisted workspace overrides this initial value with the domains from localStorage on
      // rehydrate (persist's default shallow merge + partialize persisting domains), so existing
      // projects are unaffected; only a brand-new workspace sees this default domain.
      project: { ...defaultProject },
      domains: makeDefaultDomains(),
      nodes: [],
      edges: [],
      allocSchemaVersion: 0,
      history: [],
      validateResult: null,
      compileResult: null,
      isCompiling: false,
      isValidating: false,
      error: null,
      showInterfaces: false,
      selectedNodeId: null,
      selectedEdgeId: null,
      selectedDomainId: null,
        language: defaultLanguage,
      importKeysDropped: 0,
      importClearedKeys: 0,
      // Default false: a brand-new/local workspace is not server-side confidential data. Only
      // hydrateFromServer sets it true.
      canvasFromServer: false,

      setCanvasFromServer: (v) => set({ canvasFromServer: v }),

      // Controller compile-preview (PR6): adopt the server's compiled output directly. No
      // local compile, no history push — just surface the result for CompilePreview/EdgeEditor.
      setCompileResult: (result) => set({ compileResult: result }),

      setLanguage: (lang) => set({ language: lang }),

      dismissImportNotice: () => set({ importKeysDropped: 0, importClearedKeys: 0 }),

      clearStrandedKeys: () => {
        const { nodes } = get();
        let cleared = 0;
        const next = nodes.map((n) => {
          if (n.wireguard_public_key && !n.wireguard_private_key) {
            cleared++;
            return { ...n, wireguard_public_key: undefined, fixed_private_key: false };
          }
          return n;
        });
        if (cleared > 0) set({ nodes: next });
        return cleared;
      },

  // UI preferences
  setShowInterfaces: (show) => set({ showInterfaces: show }),

  // Project operations
  setProject: (updates) =>
    set((state) => ({ project: { ...state.project, ...updates } })),

  // Domain CRUD
  addDomain: (domain) =>
    set((state) => ({ domains: [...state.domains, domain] })),

  updateDomain: (id, updates) =>
    set((state) => ({
      domains: state.domains.map((d) =>
        d.id === id ? { ...d, ...updates } : d
      ),
    })),

  removeDomain: (id) =>
    set((state) => {
      const removedNodeIDs = new Set(
        state.nodes.filter((n) => n.domain_id === id).map((n) => n.id)
      );

      return {
        domains: state.domains.filter((d) => d.id !== id),
        // Also remove the nodes belonging to this domain
        nodes: state.nodes.filter((n) => n.domain_id !== id),
        // Also remove the edges associated with the removed nodes, to avoid orphan edges
        edges: state.edges.filter(
          (e) => !removedNodeIDs.has(e.from_node_id) && !removedNodeIDs.has(e.to_node_id)
        ),
        selectedDomainId: state.selectedDomainId === id ? null : state.selectedDomainId,
        selectedNodeId:
          state.selectedNodeId && removedNodeIDs.has(state.selectedNodeId)
            ? null
            : state.selectedNodeId,
      };
    }),

  reorderDomains: (sourceId, targetId) =>
    set((state) => {
      const next = [...state.domains];
      const sourceIndex = next.findIndex((d) => d.id === sourceId);
      const targetIndex = next.findIndex((d) => d.id === targetId);
      if (sourceIndex < 0 || targetIndex < 0 || sourceIndex === targetIndex) {
        return { domains: state.domains };
      }
      const [moved] = next.splice(sourceIndex, 1);
      next.splice(targetIndex, 0, moved);
      return { domains: next };
    }),

  // Node CRUD
  addNode: (node) =>
    set((state) => ({ nodes: [...state.nodes, node] })),

  updateNode: (id, updates) =>
    set((state) => ({
      nodes: state.nodes.map((n) =>
        n.id === id ? { ...n, ...updates } : n
      ),
    })),

  removeNode: (id) =>
    set((state) => ({
      nodes: state.nodes.filter((n) => n.id !== id),
      // Also remove the associated edges
      edges: state.edges.filter(
        (e) => e.from_node_id !== id && e.to_node_id !== id
      ),
      selectedNodeId: state.selectedNodeId === id ? null : state.selectedNodeId,
    })),

  reorderNodes: (sourceId, targetId) =>
    set((state) => {
      const next = [...state.nodes];
      const sourceIndex = next.findIndex((n) => n.id === sourceId);
      const targetIndex = next.findIndex((n) => n.id === targetId);
      if (sourceIndex < 0 || targetIndex < 0 || sourceIndex === targetIndex) {
        return { nodes: state.nodes };
      }
      const [moved] = next.splice(sourceIndex, 1);
      next.splice(targetIndex, 0, moved);
      return { nodes: next };
    }),

  // Edge CRUD
  addEdge: (edge) =>
    set((state) => ({ edges: [...state.edges, edge] })),

  // Copies a primary-link edge → backup link (role: 'backup'). Only the logical-link intent
  // fields are copied (from/to/type/transport/endpoint_host); compiled_port/endpoint_port and
  // any pin are NOT — a backup link must own an independent port / transit IP / link-local
  // address, re-allocated per edge by the backend. After appending, selects the new edge with
  // selectEdge semantics (clears node/domain selection) and returns the new edge id; returns
  // null if not found. See docs/spec/data-model/edge.md (§Parallel links).
  addBackupEdge: (primaryEdgeId) => {
    const primary = get().edges.find((e) => e.id === primaryEdgeId);
    if (!primary) return null;
    const newId = `edge-${uuid()}`;
    const backup: Edge = {
      id: newId,
      from_node_id: primary.from_node_id,
      to_node_id: primary.to_node_id,
      type: primary.type,
      role: 'backup',
      transport: primary.transport,
      is_enabled: true,
      endpoint_host: primary.endpoint_host,
    };
    set((state) => ({
      edges: [...state.edges, backup],
      selectedEdgeId: newId,
      selectedNodeId: null,
      selectedDomainId: null,
    }));
    return newId;
  },

  updateEdge: (id, updates) =>
    set((state) => ({
      edges: state.edges.map((e) =>
        e.id === id ? { ...e, ...updates } : e
      ),
    })),

  mergeServerAllocations: (serverEdges, baseEdges) =>
    set((state) => {
      const srv = new Map(serverEdges.map((e) => [e.id, e]));
      const base = baseEdges ? new Map(baseEdges.map((e) => [e.id, e])) : null;
      let changed = false;
      const edges = state.edges.map((e) => {
        const s = srv.get(e.id);
        if (!s) return e; // edge not in the server set — leave it (set divergence handled upstream)
        const cur = e as unknown as Record<string, unknown>;
        const srvRec = s as unknown as Record<string, unknown>;
        const baseRec = base?.get(e.id) as unknown as Record<string, unknown> | undefined;
        const next: Record<string, unknown> = { ...cur };
        let edgeChanged = false;
        for (const f of ALLOCATION_PIN_FIELDS) {
          if (base) {
            // Conditional non-clobber adoption: take a server pin only when the server ADDED
            // it (neither canvas nor base carried one). Operator-set / operator-unpinned wins.
            if (cur[f] === undefined && srvRec[f] !== undefined && (!baseRec || baseRec[f] === undefined)) {
              next[f] = srvRec[f];
              edgeChanged = true;
            }
          } else if (cur[f] !== srvRec[f]) {
            // Unconditional reconciliation: the server is authoritative for these fields.
            next[f] = srvRec[f];
            edgeChanged = true;
          }
        }
        if (!edgeChanged) return e;
        changed = true;
        return next as unknown as Edge;
      });
      return changed ? { edges } : { edges: state.edges };
    }),

  removeEdge: (id) =>
    set((state) => ({
      edges: state.edges.filter((e) => e.id !== id),
      selectedEdgeId: state.selectedEdgeId === id ? null : state.selectedEdgeId,
    })),

  // When a node's public_endpoints change, reconcile the edges that point at that node and
  // had snapshotted the old host.
  // newHost is a string: the host was renamed → rewrite endpoint_host and clear the stale
  // compiled_port.
  // newHost is null: the host was removed → clear endpoint_host / endpoint_port / compiled_port,
  // letting the connection fall back to "backend auto-resolves", to avoid dialing a target that
  // no longer exists.
  reconcileEdgeEndpoints: (nodeId, oldHost, newHost) =>
    set((state) => {
      if (!oldHost) return { edges: state.edges };
      let changed = false;
      const edges = state.edges.map((e) => {
        if (e.to_node_id !== nodeId || e.endpoint_host !== oldHost) {
          return e;
        }
        changed = true;
        if (newHost === null) {
          return {
            ...e,
            endpoint_host: undefined,
            endpoint_port: undefined,
            compiled_port: undefined,
          };
        }
        return { ...e, endpoint_host: newHost, compiled_port: undefined };
      });
      return changed ? { edges } : { edges: state.edges };
    }),

  // Selection
  selectNode: (id) => set({ selectedNodeId: id, selectedEdgeId: null, selectedDomainId: null }),
  selectEdge: (id) => set({ selectedEdgeId: id, selectedNodeId: null, selectedDomainId: null }),
  selectDomain: (id) => set({ selectedDomainId: id, selectedNodeId: null, selectedEdgeId: null }),

  // Get the full topology
  getTopology: () => {
    const { project, domains, nodes, edges, allocSchemaVersion } = get();
    const topo: Topology = { project, domains, nodes, edges };
    // Spec E rule R0: echo the version number only when compiled (>0), so the value written
    // by the compiler round-trips verbatim.
    if (allocSchemaVersion > 0) {
      topo.alloc_schema_version = allocSchemaVersion;
    }
    return topo;
  },

  // Exports the current design as a JSON download. filename is optional (defaults to
  // <project.id>.json) — the one-time pre-hydration backup (plan-4, D9) uses it to name
  // pre-hydration-backup-<date>.json.
  exportProject: (filename?: string) => {
    const topo = get().getTopology();
    const dataStr = "data:text/json;charset=utf-8," + encodeURIComponent(JSON.stringify(topo, null, 2));
    const downloadAnchorNode = document.createElement('a');
    downloadAnchorNode.setAttribute("href",     dataStr);
    downloadAnchorNode.setAttribute("download", filename ?? `${topo.project.id || 'project'}.json`);
    document.body.appendChild(downloadAnchorNode);
    downloadAnchorNode.click();
    downloadAnchorNode.remove();
  },

  importProject: async (file: File) => {
    try {
      const text = await file.text();
      let topo = JSON.parse(text) as Topology;
      if (topo.project && topo.domains && topo.nodes && topo.edges) {
        // D45/D55: route_policies is a reserved feature, and validation rejects a non-empty
        // array. On import, if the file carries a non-empty route_policies, strip it and surface
        // a visible notice via the error state, to avoid silently dropping it.
        const hasReservedRoutePolicies =
          Array.isArray(topo.route_policies) && topo.route_policies.length > 0;
        if (hasReservedRoutePolicies) {
          delete topo.route_policies;
        }
        // Make the typed `is_enabled: boolean` honest for imports. Go's Edge.IsEnabled is the one
        // intentionally NON-omitempty Edge bool (internal/model/topology.go:168), so a missing value
        // is the Go bool zero = false (disabled). A hand-edited import lacking is_enabled would
        // otherwise carry `undefined` past the type system; normalize to a concrete boolean here so
        // every consumer agrees, mirroring normalizeEdges.ts's `!== true`. Inside this guard so a
        // malformed import lacking `edges` falls through untouched.
        topo.edges = topo.edges.map((e) => ({ ...e, is_enabled: e.is_enabled === true }));
        // Controller-mode import: the controller is server-authoritative for keys — each node's public
        // key comes from its agent's enrollment and is stamped at compile (enrolledSubgraph), and a
        // private key must never reach the server. So the imported design's key material is BOTH
        // non-authoritative AND confusing; drop ALL of it (private + public + pin flag) before load
        // (pre-load, so a private key never even enters store/localStorage). Local mode is untouched
        // here (its valid keypairs are legitimate round-trip data) — see clearStrandedKeys below.
        let keysDropped = 0;
        if (useControllerStore.getState().mode === 'controller') {
          const result = dropAllKeys(topo);
          topo = result.topo;
          keysDropped = result.dropped;
        }
        // loadTopology only accepts the four slices + the version number, so load first and add
        // the notice afterward, because loadTopology clears error.
        get().loadTopology(topo);
        // Local-mode import of a pubkey-only file (e.g. a controller-exported design, which carries
        // no private keys) would otherwise strand every node in GenerateKeys case (b) — a per-node
        // un-pin chore. Clear the stranded pubkey-only keys so they regenerate fresh on compile;
        // full keypairs (private key present) are kept (round-trip). Controller mode already dropped
        // ALL keys above, so this is a local-only step.
        let clearedKeys = 0;
        if (useControllerStore.getState().mode === 'local') {
          clearedKeys = get().clearStrandedKeys();
        }
        // Always write (including 0): a clean import (0 placeholdered) must clear the "N
        // placeholdered" banner left over from the previous import, otherwise the notice sticks
        // (plan-5 review).
        set({ importKeysDropped: keysDropped, importClearedKeys: clearedKeys });
        if (hasReservedRoutePolicies) {
          const { language } = get();
          set({
            error: t(language, 'topologyStore.routePoliciesIsA'),
          });
        }
      } else {
        throw new Error('Invalid project file format');
      }
    } catch (err) {
      set({ error: err instanceof Error ? err.message : 'Import failed' });
    }
  },

  // Loads a topology (import a project / restore a snapshot). Preserves the four-slice
  // semantics: only accepts project/domains/nodes/edges, plus Spec E rule R0's
  // alloc_schema_version (read when present in the file, otherwise reset to zero).
  // D75: clear history and selection state, to avoid a meaningless diff between the newly
  // imported project and the previous project's snapshot.
  loadTopology: (topo, fromServer = false) =>
    set({
      project: topo.project,
      domains: topo.domains,
      nodes: topo.nodes,
      // Self-heal the "pin occupied by two different links" corruption on every load (server hydrate
      // or local import) so a stale topology validates/compiles cleanly — strips a colliding edge's
      // pins so it re-allocates fresh. Needs node roles to skip client edges. Then coerce any
      // out-of-enum link_direction to undefined (≡ both) so foreign/garbled stored data falls back
      // to doubly-linked instead of tripping the validator. See lib/normalizeEdges.
      edges: sanitizeLinkDirection(healCollidingPins(topo.edges, topo.nodes)),
      allocSchemaVersion: topo.alloc_schema_version ?? 0,
      history: [],
      validateResult: null,
      compileResult: null,
      error: null,
      selectedNodeId: null,
      selectedEdgeId: null,
      selectedDomainId: null,
      // Local import/snapshot restore defaults to false; only a server hydration passes true.
      // Atomic set (in the same set() as the slices), to avoid a transient persistence window
      // of "persist server data as false first, then flip to true".
      canvasFromServer: fromServer,
    }),

  // Mode-boundary scrub (plan-5, D6): the lossy action on a controller→local switch. The graph
  // is kept (project/domains/node identity/edges/capabilities/endpoint/ssh, etc.), but each
  // node's key material (private/public keys/fixed flag) and overlay_ip, every edge's compile
  // artifacts (compiled_port + all pinned_* allocations), the topology-level
  // alloc_schema_version, and compile history/results are all cleared. This way the next local
  // compile regenerates a clean, self-consistent set of keys and allocations, never leaving
  // fleet-used keys behind in the browser. Fields are enumerated explicitly one by one (rather
  // than by pattern match), so when a secret/pin field is added it must be updated here too
  // (the scrub-list-completeness risk of the plan-5.5 insertion point).
  purgeModeBoundaryState: () =>
    set((state) => ({
      nodes: state.nodes.map((n) => ({
        ...n,
        wireguard_private_key: undefined,
        wireguard_public_key: undefined,
        fixed_private_key: undefined,
        overlay_ip: undefined,
      })),
      edges: state.edges.map((e) => ({
        ...e,
        compiled_port: undefined,
        pinned_from_port: undefined,
        pinned_to_port: undefined,
        pinned_from_transit_ip: undefined,
        pinned_to_transit_ip: undefined,
        pinned_from_link_local: undefined,
        pinned_to_link_local: undefined,
      })),
      allocSchemaVersion: 0,
      history: [],
      validateResult: null,
      compileResult: null,
      // After switching to local mode, also clear the "placeholdered" banner left over from a
      // controller-mode import (plan-5 review).
      importKeysDropped: 0,
      importClearedKeys: 0,
      error: null,
      selectedNodeId: null,
      selectedEdgeId: null,
      selectedDomainId: null,
      // Once switched back to local, the graph belongs to the operator locally and is no longer
      // a server confidential mirror: clear the provenance flag and restore normal persistence.
      canvasFromServer: false,
    })),

  // Reset
  // D75: consistent with loadTopology, clear history and the version number too, to avoid a
  // leftover snapshot continuing to diff against the next project.
  reset: () =>
    set({
      project: { ...defaultProject },
      // UX-3: consistent with the initial state — after a reset the default network domain is
      // still seeded, to avoid dropping the user back into the "no domain, add-node button
      // disabled" dead end.
      domains: makeDefaultDomains(),
      nodes: [],
      edges: [],
      allocSchemaVersion: 0,
      history: [],
      validateResult: null,
      compileResult: null,
      error: null,
      selectedNodeId: null,
      selectedEdgeId: null,
      selectedDomainId: null,
      canvasFromServer: false,
    }),

  flushWorkspace: () =>
    set((state) => ({
      project: { ...defaultProject },
      // UX-3: consistent with reset / the initial state, seed the default network domain.
      domains: makeDefaultDomains(),
      nodes: [],
      edges: [],
      allocSchemaVersion: 0,
      history: [],
      validateResult: null,
      compileResult: null,
      isCompiling: false,
      isValidating: false,
      error: null,
      selectedNodeId: null,
      selectedEdgeId: null,
      selectedDomainId: null,
      language: state.language,
      canvasFromServer: false,
    })),

  // API: validate
  validate: async () => {
    set({ isValidating: true, error: null });
    try {
      const topo = get().getTopology();
      const cs = useControllerStore.getState();
      // Validation is purely STRUCTURAL — schema + semantic checks over the topology, no private keys,
      // no server state — and the in-browser TS validator is byte-pinned to the Go pipeline by the
      // conformance harness. So in CONTROLLER mode it ALWAYS runs in-browser (browser-local verify):
      // the controller neither serves nor calls /api/validate. That is a security choice as much as a
      // correctness one — the shipped controller gates the air-gap compute routes off (plan-7,
      // //go:build airgap → 404), and keeping the controller's verify path off the wire keeps its
      // attack surface minimal (no anonymous server-side validation endpoint to reach). A controller
      // (public-keys-only) canvas validates identically — the validator never touches a private key —
      // so validate has no controller-mode refusal guard (unlike compile/export/deploy). In LOCAL
      // mode it runs in-browser too whenever localEngineEnabled() (default-ON); only the explicit
      // VITE_YAOG_LOCAL_ENGINE='backend' opt-out falls through to the air-gap /api/validate fetch.
      if (cs.mode === 'controller' || localEngineEnabled()) {
        const data = await localValidate(topo);
        set({ validateResult: data, isValidating: false });
        return;
      }
      // LOCAL-mode 'backend' opt-out only: the retained /api/validate fetch, hitting the anonymous
      // air-gap oracle (a -tags airgap server). No auth headers — controller mode never reaches here
      // (it returned above), and the air-gap oracle is anonymous by design.
      const res = await fetch('/api/validate', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(topo),
      });
      if (!res.ok) {
        throw new Error(await readApiErrorMessage(res, 'error.validateFailed', get().language));
      }
      const data: ValidateResponse = await res.json();
      set({ validateResult: data, isValidating: false });
    } catch (err) {
      set({
        error: localEngineErrorMessage(err, 'error.validateFailed', get().language),
        isValidating: false,
      });
    }
  },

  // API: compile
  compile: async () => {
    // Defense-in-depth: /api/compile is the air-gap path — it generates/reconstructs WireGuard
    // keys client-side and needs private keys in the design. Controller mode is zero-knowledge
    // (public-keys-only; the controller compiles server-side during Deploy), so a local compile
    // there fails on every node. The Compile button is already hidden in controller mode
    // (CanvasToolbar); this guard makes the store action itself refuse rather than emit a
    // confusing key-generation error if ever invoked.
    if (useControllerStore.getState().mode === 'controller') {
      set({
        error:
          'Local compile is unavailable in controller mode (the design is public-keys-only). Use Deploy — the controller compiles server-side.',
        isCompiling: false,
      });
      return;
    }
    set({ isCompiling: true, error: null });
    try {
      const topo = get().getTopology();
      let data: CompileResponse;
      // Local-engine seam: in LOCAL mode compile in the browser via the plan-4 TS compiler
      // (default-ON). localCompile runs the full AirGap pipeline, so data.topology.nodes
      // carries reconstructed private keys (local export/deploy bundles need them) and
      // data.skipped_unenrolled is UNDEFINED (air-gap shape) — exactly the /api/compile shape
      // the post-compile reconciliation below consumes. Only the explicit 'backend' opt-out
      // makes local mode fall through to the /api/compile fetch (the retained escape-hatch path).
      if (localEngineEnabled()) {
        data = await localCompile(topo);
      } else {
        const res = await fetch('/api/compile', {
          method: 'POST',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify(topo),
        });
        if (!res.ok) {
          throw new Error(await readApiErrorMessage(res, 'error.compileFailed', get().language));
        }
        data = await res.json();
      }

      // In-flight mode-flip guard: the front-door check above only rejects FRESH
      // invocations. If the operator switched to controller mode while this compile was in
      // flight (whether the air-gap fetch OR the local-engine compile), `data` carries
      // reconstructed private keys (data.topology.nodes). Persisting them now would write
      // fleet private keys into the controller-mode store and its localStorage mirror —
      // exactly the boundary the zero-knowledge custody model forbids. Drop the result
      // instead. The local-engine path makes the in-flight window near-zero (the compile is
      // effectively synchronous), but the guard is kept verbatim as defense-in-depth and to
      // preserve the documented custody contract (plan-6, R2 / Phase 3.3).
      if (useControllerStore.getState().mode === 'controller') {
        set({ isCompiling: false });
        return;
      }

      const newHistoryEntry: CompileHistoryEntry = {
        id: uuid(),
        timestamp: new Date().toISOString(),
        topology: topo,
        compileResult: data,
      };

      set((state) => ({
        compileResult: data,
        isCompiling: false,
        project: data.topology.project,
        domains: data.topology.domains,
        nodes: data.topology.nodes,
        edges: data.topology.edges,
        // Spec E rule R0: pull the allocation-scheme version number written by the compiler back
        // into the store, so the next compile and persistence both carry it.
        allocSchemaVersion: data.topology.alloc_schema_version ?? state.allocSchemaVersion,
        history: [newHistoryEntry, ...state.history].slice(0, 5), // keep last 5
      }));
    } catch (err) {
      set({
        error: localEngineErrorMessage(err, 'error.compileFailed', get().language),
        isCompiling: false,
      });
    }
  },

  // API: export
  exportArtifacts: async () => {
    // Defense-in-depth, parity with compile(): /api/export is an air-gap path that
    // generates WireGuard keys server-side from the design and bundles them into the
    // downloaded ZIP. Controller mode is zero-knowledge (public-keys-only), so an
    // export there fails on every node — and shipping keys for a controller design is
    // a category error. The button is local-mode-only in the UI; this guard makes the
    // action refuse rather than emit a confusing key-generation error if ever invoked.
    if (useControllerStore.getState().mode === 'controller') {
      set({
        error:
          'Artifact export is unavailable in controller mode (the design is public-keys-only). The controller compiles and distributes per-node bundles server-side on Deploy.',
      });
      return;
    }
    try {
      const topo = get().getTopology();
      let blob: Blob;
      let filename: string;
      // Local-engine seam: in LOCAL mode build the per-node bundle ZIP in the browser via the
      // plan-4 TS compiler (default-ON) — no fetch — and name the download locally, VERBATIM
      // mirroring handler.go:240's `fmt.Sprintf("%s-artifacts.zip", topo.Project.ID)`
      // with NO `|| 'project'` fallback: an empty project.id yields `-artifacts.zip` on BOTH
      // paths (a fallback here would be a silent F3-class divergence; defaultProject.id is
      // 'project-1' so the empty case is unreachable today, but parity holds unconditionally).
      // Only the explicit 'backend' opt-out makes local mode fall through to the /api/export
      // fetch (the retained escape-hatch path), which carries the server's Content-Disposition
      // filename.
      if (localEngineEnabled()) {
        blob = await localExport(topo);
        filename = `${topo.project.id}-artifacts.zip`;
      } else {
        const res = await fetch('/api/export', {
          method: 'POST',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify(topo),
        });
        if (!res.ok) {
          throw new Error(await readApiErrorMessage(res, 'error.exportFailed', get().language));
        }

        blob = await res.blob();
        filename = parseContentDispositionFilename(res, 'artifacts.zip');
      }

      triggerBrowserDownload(blob, filename);
    } catch (err) {
      set({
        error: localEngineErrorMessage(err, 'error.exportFailed', get().language),
      });
    }
  },

  clearHistory: () => set({ history: [] }),

  // API: download deploy script
  downloadDeployScript: async (format: 'sh' | 'ps1') => {
    // Defense-in-depth, parity with compile()/exportArtifacts(): /api/deploy-script is
    // an air-gap path that compiles the design (key generation included) server-side.
    // Controller mode is public-keys-only, so it fails there; deployment in controller
    // mode goes through the server (stage/promote), not a downloaded script.
    if (useControllerStore.getState().mode === 'controller') {
      set({
        error:
          'Deploy-script download is unavailable in controller mode (the design is public-keys-only). Use Deploy — the controller stages and promotes per-node bundles server-side.',
      });
      return;
    }
    try {
      const topo = get().getTopology();
      let blob: Blob;
      // Local-engine seam: in LOCAL mode render the project-level deploy script in the browser
      // via the plan-4 TS compiler (default-ON) — no fetch — and wrap it in a Blob to reuse the
      // same object-URL download below. localDeployScripts returns both formats; pick by
      // `format`. Only the explicit 'backend' opt-out makes local mode fall through to the
      // /api/deploy-script fetch (the retained escape-hatch path).
      if (localEngineEnabled()) {
        const { sh, ps1 } = await localDeployScripts(topo);
        const script = format === 'ps1' ? ps1 : sh;
        blob = new Blob([script], { type: 'text/plain' });
      } else {
        const res = await fetch(`/api/deploy-script?format=${format}`, {
          method: 'POST',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify(topo),
        });
        if (!res.ok) {
          throw new Error(await readApiErrorMessage(res, 'error.deployScriptFailed', get().language));
        }
        blob = await res.blob();
      }

      // Filenames match handler.go:298/:302 (deploy-all.ps1 / deploy-all.sh) on both paths.
      const filename = format === 'ps1' ? 'deploy-all.ps1' : 'deploy-all.sh';
      triggerBrowserDownload(blob, filename);
    } catch (err) {
      set({
        error: localEngineErrorMessage(err, 'error.deployScriptFailed', get().language),
      });
    }
  },
    }),
    {
      name: 'topology-storage',
      // We only persist these properties to avoid saving volatile UI state like isCompiling or errors
      partialize: (state) => {
        // Security invariant (controller server-authoritative): a design hydrated from the
        // server contains confidential fleet data (public IPs / SSH targets). In controller mode
        // it must never be persisted to disk — otherwise anyone who gets the browser after logout
        // could read it from localStorage (or render it by one-click "switch back to local"). D1
        // says the canvas is a "disposable mirror": logging in re-hydrates from the server, so
        // there is no need to persist. Local mode, or original local work (canvasFromServer=false),
        // persists as usual — that is the user's own data on their own machine, not a confidential
        // mirror. mode is read lazily across stores (same technique as importProject, a runtime
        // lookup to avoid a module-level circular dependency). init-safety: this store configures
        // no persist version/migrate, so partialize is not called during module init (the hydrate
        // phase) — the first call happens on a user-driven set() after both stores are ready, at
        // which point useControllerStore.getState() is guaranteed available.
        const serverHeld =
          state.canvasFromServer && useControllerStore.getState().mode === 'controller';
        return {
          project: serverHeld ? { ...defaultProject } : state.project,
          domains: serverHeld ? makeDefaultDomains() : state.domains,
          nodes: serverHeld ? [] : state.nodes,
          edges: serverHeld ? [] : state.edges,
          // Spec E rule R0: persist the version number too, so the allocation scheme written by
          // the compiler still round-trips after a page refresh.
          allocSchemaVersion: serverHeld ? 0 : state.allocSchemaVersion,
          // The provenance flag itself must be persisted: after a refresh, if still logged out,
          // the login gate uses it to know the canvas is a confidential mirror and reset it wholesale.
          canvasFromServer: state.canvasFromServer,
          language: state.language,
          // The canvas preference is persisted at the same level as language: keep the user's
          // chosen interface-detail expansion state after a refresh.
          showInterfaces: state.showInterfaces,
        };
      },
      // Self-heal a stale localStorage topology on rehydrate: strip a colliding edge's pins (any two
      // different links sharing a transit IP / port / link-local — see lib/normalizeEdges), so a
      // persisted corrupted design validates cleanly without the operator hand-fixing each edge;
      // then coerce any out-of-enum link_direction to undefined (≡ both — same rationale, same
      // choke-point). Runs AFTER hydration and adds NO version/migrate, so the partialize
      // init-safety note holds.
      onRehydrateStorage: () => (state) => {
        if (state) {
          state.edges = sanitizeLinkDirection(healCollidingPins(state.edges, state.nodes));
        }
      },
    }
  )
);
