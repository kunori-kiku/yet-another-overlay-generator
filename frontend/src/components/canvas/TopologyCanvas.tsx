import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import {
  ReactFlow,
  Background,
  Controls,
  MiniMap,
  Panel,
  addEdge,
  useNodesState,
  useEdgesState,
  type Connection,
  type Edge as FlowEdge,
  type Node as FlowNode,
  type NodeChange,
  type EdgeChange,
  type ReactFlowInstance,
  MarkerType,
} from '@xyflow/react';
import '@xyflow/react/dist/style.css';
import dagre from '@dagrejs/dagre';
import { CustomNode } from './CustomNode';
import { CustomEdge } from './CustomEdge';
import { useTopologyStore } from '../../stores/topologyStore';
import { t } from '../../i18n';
import { resolveNodeInterfaces } from '../../lib/compiledInterfaces';
import { uuid } from '../../lib/uuid';

const nodeTypes = { custom: CustomNode };
const edgeTypes = { custom: CustomEdge };

// Default node dimensions for auto-layout (used before React Flow has measured them).
const DEFAULT_NODE_WIDTH = 180;
const DEFAULT_NODE_HEIGHT = 110;

// Edge render-equality check for keyed syncing. Only replace the edge object when a
// render-relevant field changes, keeping object identity stable -> React Flow skips
// re-rendering unchanged edges (eliminates whole-layer edge flicker).
function edgeRenderEqual(a: FlowEdge, b: FlowEdge): boolean {
  if (a.source !== b.source || a.target !== b.target) return false;
  const da = (a.data ?? {}) as Record<string, unknown>;
  const db = (b.data ?? {}) as Record<string, unknown>;
  const keys = [
    'edgeType',
    'label',
    'pending',
    'port',
    'parallelIndex',
    'parallelCount',
    'sourceNodeName',
    'targetNodeName',
    'roleChip',
    'deemphasized',
  ];
  return keys.every((k) => da[k] === db[k]);
}

export function TopologyCanvas() {
  const {
    nodes: topoNodes,
    edges: topoEdges,
    domains,
    compileResult,
    language,
    showInterfaces,
    setShowInterfaces,
    addEdge: addTopoEdge,
    removeNode: removeTopoNode,
    removeEdge: removeTopoEdge,
    selectNode,
    selectEdge,
    selectDomain,
    selectedNodeId,
    selectedEdgeId,
  } = useTopologyStore();

  // Focus opacity (Decisions #11): connection-drag-in-progress flag. onConnectStart sets it,
  // onConnectEnd clears it (including drags cancelled mid-way); during a drag all edges are
  // deemphasized and all nodes stay fully opaque.
  const [connecting, setConnecting] = useState(false);

  // Persist node positions across re-renders so dragging is not lost
  const positionMap = useRef<Record<string, { x: number; y: number }>>({});
  // onInit captures the instance: run an animated fitView once auto-layout finishes (no
  // ReactFlowProvider wrapper required).
  const rfInstance = useRef<ReactFlowInstance | null>(null);
  // Auto-layout animation-frame handle: cancel an unfinished animation on repeat click / unmount.
  const layoutAnimation = useRef<number | null>(null);

  // Build a domain-name index
  const domainMap = useMemo(() => {
    const m: Record<string, string> = {};
    domains.forEach((d) => (m[d.id] = d.name));
    return m;
  }, [domains]);

  // Link-role chips (contract item 5 / Decisions #5): group all enabled edges by "undirected
  // node pair". Single-edge pairs get no chip (keeps the look clean); within a multi-edge pair:
  //   - backup edges (role === 'backup') -> 'b1','b2',..., numbered by appearance order in
  //     topoEdges among this pair's backups;
  //   - redundant same-direction roleless/primary edges (same from->to, first wins, mirrors
  //     backend D71) -> 'duplicate' warning;
  //   - all other primary-class edges (the representative edge + the reverse roleless edge,
  //     merged into the same primary link) -> 'primary' (★).
  // Also produces an edgeId -> 'b1'/'★' role-marker map, reused by node interface chips so they
  // stay consistent with the edge fan ordinals.
  // Note: declared before nodeInterfaceMap -- the latter depends on edgeRoleMarker to compute
  // the node chip's ★/bN marker.
  const { edgeRoleChip, edgeRoleMarker } = useMemo(() => {
    const enabledEdges = topoEdges.filter((e) => e.is_enabled);

    // Undirected-pair grouping (same pairKey convention as parallelEdgeInfo)
    const pairMap: Record<string, typeof enabledEdges> = {};
    for (const e of enabledEdges) {
      const pairKey = [e.from_node_id, e.to_node_id].sort().join('::');
      if (!pairMap[pairKey]) pairMap[pairKey] = [];
      pairMap[pairKey].push(e);
    }

    const chip: Record<string, string> = {};
    const marker: Record<string, string> = {};
    for (const edges of Object.values(pairMap)) {
      if (edges.length <= 1) continue; // single-edge pair: no chip
      const firstPrimaryByDirection: Record<string, boolean> = {};
      let backupOrdinal = 0;
      for (const e of edges) {
        if (e.role === 'backup') {
          backupOrdinal += 1;
          const tag = `b${backupOrdinal}`;
          chip[e.id] = tag;
          marker[e.id] = tag;
          continue;
        }
        // primary class (role empty or 'primary'). Same-direction dedup mirrors D71: first wins.
        const direction = `${e.from_node_id}->${e.to_node_id}`;
        if (firstPrimaryByDirection[direction]) {
          chip[e.id] = 'duplicate'; // redundant same-direction -> warning chip (no node interface role marker)
          continue;
        }
        firstPrimaryByDirection[direction] = true;
        chip[e.id] = 'primary';
        marker[e.id] = '★';
      }
    }
    return { edgeRoleChip: chip, edgeRoleMarker: marker };
  }, [topoEdges]);

  // Build each node's compiled interface details (the display chips on the node card, gated by
  // the "show interface details" toggle).
  // Note: interfaces no longer act as connection handles -- the connection gesture is
  // node-to-node, and ports are allocated by the backend at compile time.
  //
  // Decisions #12: switched to the shared edge-aware resolver resolveNodeInterfaces -- it
  // matches each compiled interface back to its edge via the pinned port (ports are unique
  // within a node), avoiding the old approach of "strip wg- from the interface name to
  // back-derive the peer name", which rendered garbage chips for backup interfaces
  // (wg-<clean8><hash4>). RightPanel reuses the same resolver.
  // Here the resolver output (peerName / listenPort / role / edgeId / real interface name) is
  // mapped into node-card chips, and a role marker consistent with the edge fan (★ / bN) is
  // computed from role + edgeId; an 'unknown' peerName is left by the resolver as the verbatim
  // interface name (never stripping wg-) and carries no marker.
  interface IfaceChip {
    name: string;        // real interface name (for the tooltip; never strips wg-)
    listenPort: number;  // backend-allocated listen port
    peerName: string;    // peer node name (falls back to the interface name when 'unknown')
    roleMarker?: string; // '★' / 'b1' / ... or undefined
  }
  const nodeInterfaceMap = useMemo(() => {
    const m: Record<string, IfaceChip[]> = {};
    if (!compileResult) return m;
    for (const node of topoNodes) {
      const infos = resolveNodeInterfaces(
        node.id,
        compileResult.wireguard_configs,
        topoNodes,
        topoEdges
      );
      if (infos.length === 0) continue;
      m[node.id] = infos.map((info) => {
        let roleMarker: string | undefined;
        if (info.role === 'primary') {
          roleMarker = '★';
        } else if (info.role === 'backup') {
          // Stay consistent with the edge fan ordinal: take this backup edge's bN from edgeRoleMarker.
          roleMarker = info.edgeId ? edgeRoleMarker[info.edgeId] : undefined;
        }
        // role === 'unknown': peerName is already the verbatim interface name; no marker.
        return {
          name: info.interfaceName,
          listenPort: info.listenPort,
          peerName: info.peerName,
          roleMarker,
        };
      });
    }
    return m;
  }, [compileResult, topoNodes, topoEdges, edgeRoleMarker]);

  // Convert topology nodes into React Flow nodes.
  // Pure computation: do not read/write the positionMap ref during render (react-hooks/refs
  // constraint). This only assigns default grid positions; dragged/persisted positions are
  // merged in the sync effect below.
  const flowNodes: FlowNode[] = useMemo(
    () =>
      topoNodes.map((n, i) => ({
        id: n.id,
        type: 'custom',
        position: {
          x: 100 + (i % 4) * 280,
          y: 100 + Math.floor(i / 4) * 250,
        },
        data: {
          label: n.name,
          role: n.role,
          overlayIp: n.overlay_ip || '',
          domainName: domainMap[n.domain_id] || '',
          interfaces: nodeInterfaceMap[n.id] || [],
        },
      })),
    [topoNodes, domainMap, nodeInterfaceMap]
  );

  // Compute parallel-edge indices (multiple edges between the same node pair)
  const parallelEdgeInfo = useMemo(() => {
    const pairMap: Record<string, string[]> = {};
    const enabledEdges = topoEdges.filter((e) => e.is_enabled);

    for (const e of enabledEdges) {
      const pairKey = [e.from_node_id, e.to_node_id].sort().join('::');
      if (!pairMap[pairKey]) pairMap[pairKey] = [];
      pairMap[pairKey].push(e.id);
    }

    const info: Record<string, { index: number; count: number }> = {};
    for (const ids of Object.values(pairMap)) {
      for (let i = 0; i < ids.length; i++) {
        info[ids[i]] = { index: i, count: ids.length };
      }
    }
    return info;
  }, [topoEdges]);

  // Convert topology edges into React Flow edges (using the custom edge).
  // Edges always render between node-level anchors (no longer routed to interface handles): an
  // edge is a node-to-node logical link, and interfaces/ports are its compile products. The
  // port semantics are split into structured fields handed to CustomEdge for chip rendering:
  //   port    -- compiled_port (the backend-allocated truth) or an explicit endpoint_port override;
  //   pending -- the "awaiting compile" signal -> dashed line. Note compiled_port is only written
  //   back for edges with an endpoint_host (compiler.go's CompiledPort write-back rule); a passive
  //   edge without endpoint_host must use the pin fields (present on every enabled edge after
  //   compile) to decide whether it is compiled, otherwise it would stay dashed forever.
  //   Edges with an endpoint_host still go by compiled_port: dial-related edits clear it (D19),
  //   and the dashed fallback is exactly the "needs recompile" visual feedback.
  const flowEdges: FlowEdge[] = useMemo(
    () =>
      topoEdges
        .filter((e) => e.is_enabled)
        .map((e) => {
          const pInfo = parallelEdgeInfo[e.id] || { index: 0, count: 1 };
          const port = e.compiled_port || e.endpoint_port || undefined;
          const hasPins =
            e.pinned_from_port !== undefined || e.pinned_to_port !== undefined;
          const pending = e.endpoint_host ? !e.compiled_port : !hasPins;
          const label = e.endpoint_host || e.type;
          const sourceNode = topoNodes.find((n) => n.id === e.from_node_id);
          const targetNode = topoNodes.find((n) => n.id === e.to_node_id);

          return {
            id: e.id,
            source: e.from_node_id,
            target: e.to_node_id,
            type: 'custom',
            data: {
              edgeType: e.type,
              label,
              pending,
              port,
              parallelIndex: pInfo.index,
              parallelCount: pInfo.count,
              sourceNodeName: sourceNode?.name || '',
              targetNodeName: targetNode?.name || '',
              roleChip: edgeRoleChip[e.id], // ★ / bN / duplicate / undefined (single-edge pair)
            },
            markerEnd: { type: MarkerType.ArrowClosed },
          };
        }),
    [topoEdges, parallelEdgeInfo, topoNodes, edgeRoleChip]
  );

  // Focus-opacity computation (Decisions #11, verbatim): from the current selection + the
  // connection-drag state, compute the "deemphasize" predicates injected into node/edge
  // data.deemphasized (applied in the sync effect below).
  // Priority: connection drag > selected edge > selected node > none (all bright).
  //   - during a connection drag: all edges deemphasized, all nodes bright (nodes are the drop target);
  //   - selected node: deemphasize everything except the node itself + its incident edges (the
  //     far node stays deemphasized too, per the literal text of #11);
  //   - selected edge: deemphasize everything except the edge itself + its two endpoint nodes;
  //   - a background click clears the selection -> the predicate returns to "nothing
  //     deemphasized" (onPaneClick already cleared it, so the predicate recovers naturally).
  const deemphasis = useMemo<{
    isNodeDeemphasized: (id: string) => boolean;
    isEdgeDeemphasized: (id: string) => boolean;
  }>(() => {
    // During a connection drag: all edges deemphasized, all nodes bright (nodes are the drop target). Highest priority.
    if (connecting) {
      return {
        isNodeDeemphasized: () => false,
        isEdgeDeemphasized: () => true,
      };
    }

    // Selected edge: deemphasize everything except the edge itself + its two endpoint nodes.
    if (selectedEdgeId) {
      const sel = topoEdges.find((e) => e.id === selectedEdgeId);
      const endpoints = sel
        ? new Set([sel.from_node_id, sel.to_node_id])
        : new Set<string>();
      return {
        isNodeDeemphasized: (id: string) => !endpoints.has(id),
        isEdgeDeemphasized: (id: string) => id !== selectedEdgeId,
      };
    }

    // Selected node: deemphasize everything except the node itself + its incident edges (the far node stays deemphasized too, per the literal text of #11).
    if (selectedNodeId) {
      const incidentEdgeIds = new Set(
        topoEdges
          .filter(
            (e) =>
              e.from_node_id === selectedNodeId || e.to_node_id === selectedNodeId
          )
          .map((e) => e.id)
      );
      return {
        isNodeDeemphasized: (id: string) => id !== selectedNodeId,
        isEdgeDeemphasized: (id: string) => !incidentEdgeIds.has(id),
      };
    }

    // Nothing selected, not dragging: all bright (a background click clears the selection and naturally lands here).
    return {
      isNodeDeemphasized: () => false,
      isEdgeDeemphasized: () => false,
    };
  }, [connecting, selectedNodeId, selectedEdgeId, topoEdges]);

  const [nodes, setNodes, onNodesChange] = useNodesState(flowNodes);
  const [edges, setEdges, onEdgesChange] = useEdgesState(flowEdges);

  const handleNodesChange = useCallback(
    (changes: NodeChange[]) => {
      onNodesChange(changes);
      for (const change of changes) {
        if (change.type === 'remove') {
          delete positionMap.current[change.id];
          removeTopoNode(change.id);
        }
        // Persist drag position
        if (change.type === 'position' && change.position) {
          positionMap.current[change.id] = change.position;
        }
      }
    },
    [onNodesChange, removeTopoNode]
  );

  const handleEdgesChange = useCallback(
    (changes: EdgeChange[]) => {
      onEdgesChange(changes);
      for (const change of changes) {
        if (change.type === 'remove') {
          removeTopoEdge(change.id);
        }
      }
    },
    [onEdgesChange, removeTopoEdge]
  );

  // Sync data changes (name, role, interfaces, etc.) into React Flow state without overwriting
  // dragged positions. setState and reading/writing the positionMap ref are side effects, so
  // they must run in an effect, not in a render-time useMemo (fixes audit finding D18:
  // render-time side effects caused nodes to jump back to old coordinates after an unrelated edit).
  useEffect(() => {
    setNodes((currentNodes) =>
      flowNodes.map((fn) => {
        // First time we see this node, register its default grid position as the persisted position
        if (!positionMap.current[fn.id]) {
          positionMap.current[fn.id] = fn.position;
        }
        const existing = currentNodes.find((n) => n.id === fn.id);
        return {
          ...fn,
          // Focus opacity (Decisions #11): inject the deemphasize predicate into data; CustomNode fades the root container accordingly.
          data: { ...fn.data, deemphasized: deemphasis.isNodeDeemphasized(fn.id) },
          // Preserve React Flow's selected flag to avoid the selection outline flickering during sync
          selected: existing?.selected,
          position: positionMap.current[fn.id] || existing?.position || fn.position,
        };
      })
    );
  }, [flowNodes, setNodes, deemphasis]);

  // Keyed edge sync: edges whose render fields are unchanged keep their original object identity
  // (including the selected flag), avoiding the old wholesale setEdges(flowEdges) that rebuilt
  // every edge object on any store change and triggered the whole-layer re-render jank/flicker.
  useEffect(() => {
    setEdges((current) => {
      const prevById = new Map(current.map((e) => [e.id, e]));
      let changed = current.length !== flowEdges.length;
      const next = flowEdges.map((feBase) => {
        // Focus opacity (Decisions #11): inject the deemphasize predicate into data before the
        // keyed equality check, so when the deemphasize state changes (deemphasized is in
        // edgeRenderEqual's keys) the edge object is replaced and re-rendered.
        const fe = {
          ...feBase,
          data: {
            ...feBase.data,
            deemphasized: deemphasis.isEdgeDeemphasized(feBase.id),
          },
        };
        const prev = prevById.get(fe.id);
        if (prev && edgeRenderEqual(prev, fe)) {
          return prev;
        }
        changed = true;
        return { ...fe, selected: prev?.selected };
      });
      return changed ? next : current;
    });
  }, [flowEdges, setEdges, deemphasis]);

  // Cancel any unfinished layout animation frame on unmount
  useEffect(
    () => () => {
      if (layoutAnimation.current !== null) {
        cancelAnimationFrame(layoutAnimation.current);
      }
    },
    []
  );

  // Auto-layout: dagre's layered layout computes target coordinates, then easeOutCubic
  // interpolation smooths the transition. We do not use CSS transform transitions -- React Flow
  // dragging also uses transform, so the two would fight; updating position state frame-by-frame
  // is the officially recommended animation approach. The animation writes positionMap as it
  // goes, so the layout result is persisted just like a manual drag.
  const runAutoLayout = useCallback(() => {
    if (nodes.length === 0) return;

    const g = new dagre.graphlib.Graph();
    g.setGraph({ rankdir: 'TB', nodesep: 70, ranksep: 110, marginx: 40, marginy: 40 });
    g.setDefaultEdgeLabel(() => ({}));
    for (const n of nodes) {
      g.setNode(n.id, {
        width: n.measured?.width ?? DEFAULT_NODE_WIDTH,
        height: n.measured?.height ?? DEFAULT_NODE_HEIGHT,
      });
    }
    for (const e of edges) {
      g.setEdge(e.source, e.target);
    }
    dagre.layout(g);

    // dagre returns center-point coordinates; React Flow's position is the top-left corner.
    const targets: Record<string, { x: number; y: number }> = {};
    const from: Record<string, { x: number; y: number }> = {};
    for (const n of nodes) {
      const gn = g.node(n.id);
      if (!gn) continue;
      const w = n.measured?.width ?? DEFAULT_NODE_WIDTH;
      const h = n.measured?.height ?? DEFAULT_NODE_HEIGHT;
      targets[n.id] = { x: gn.x - w / 2, y: gn.y - h / 2 };
      from[n.id] = { ...n.position };
    }

    if (layoutAnimation.current !== null) {
      cancelAnimationFrame(layoutAnimation.current);
    }
    const duration = 450;
    const start = performance.now();
    const step = (now: number) => {
      const t = Math.min(1, (now - start) / duration);
      const ease = 1 - Math.pow(1 - t, 3); // easeOutCubic
      setNodes((curr) =>
        curr.map((n) => {
          const f = from[n.id];
          const to = targets[n.id];
          if (!f || !to) return n;
          const pos = {
            x: f.x + (to.x - f.x) * ease,
            y: f.y + (to.y - f.y) * ease,
          };
          positionMap.current[n.id] = pos;
          return { ...n, position: pos };
        })
      );
      if (t < 1) {
        layoutAnimation.current = requestAnimationFrame(step);
      } else {
        layoutAnimation.current = null;
        rfInstance.current?.fitView({ padding: 0.2, duration: 300 });
      }
    };
    layoutAnimation.current = requestAnimationFrame(step);
  }, [nodes, edges, setNodes]);

  const onConnect = useCallback(
    (params: Connection) => {
      setEdges((eds) => addEdge({ ...params, type: 'custom', data: { edgeType: 'direct', label: 'direct', pending: true, parallelIndex: 0, parallelCount: 1 }, markerEnd: { type: MarkerType.ArrowClosed } }, eds));

      if (params.source && params.target) {
        // Generate the edge ID with uuid() rather than a millisecond timestamp: two quick
        // connections can land in the same millisecond, causing an ID collision so any later
        // edit/delete by ID would hit both edges (fixes D17).
        const id = `edge-${uuid()}`;
        const targetNode = topoNodes.find((n) => n.id === params.target);
        const preferredEndpoint = targetNode?.public_endpoints?.[0];

        // Fill only endpoint_host (the target node's reachability hint); leave endpoint_port empty
        // -> the backend, as the sole port authority, auto-allocates the listen port. A port counts
        // as a NAT override only when the operator types one explicitly.
        addTopoEdge({
          id,
          from_node_id: params.source,
          to_node_id: params.target,
          type: 'direct',
          endpoint_host: preferredEndpoint?.host,
          transport: 'udp',
          is_enabled: true,
        });
        selectEdge(id);
      }
    },
    [setEdges, addTopoEdge, topoNodes, selectEdge]
  );

  // Focus opacity (Decisions #11): a connection drag starting -> set connecting (all edges deemphasized, nodes bright).
  const onConnectStart = useCallback(() => {
    setConnecting(true);
  }, []);

  // onConnectEnd always clears connecting -- including drags cancelled mid-way (dropped on empty
  // space), so focus opacity returns to normal after the drag ends (on a successful connection,
  // onConnect has already handled selecting the new edge before this runs).
  const onConnectEnd = useCallback(() => {
    setConnecting(false);
  }, []);

  const onNodeClick = useCallback(
    (_: React.MouseEvent, node: FlowNode) => {
      selectNode(node.id);
    },
    [selectNode]
  );

  const onEdgeClick = useCallback(
    (_: React.MouseEvent, edge: FlowEdge) => {
      selectEdge(edge.id);
    },
    [selectEdge]
  );

  const onPaneClick = useCallback(() => {
    selectNode(null);
    selectEdge(null);
    selectDomain(null);
  }, [selectNode, selectEdge, selectDomain]);

  // Connection-validity check (called at high frequency during a drag, so it must stay a pure
  // function and O(edge count) lightweight):
  // 1) reject self-loops (source === target) -- a node connecting to itself is meaningless;
  // 2) reject node pairs that duplicate an existing "enabled" edge (either direction) -- parallel
  //    edges are visualized by parallelEdgeInfo, but a duplicate direct edge between the same node
  //    pair only adds confusion with no new semantics.
  // Reads topoEdges (the topology source of truth), not React Flow's derived edges state.
  const isValidConnection = useCallback(
    (connection: Connection | FlowEdge) => {
      const { source, target } = connection;
      if (!source || !target) return false;
      if (source === target) return false;
      return !topoEdges.some(
        (e) =>
          e.is_enabled &&
          ((e.from_node_id === source && e.to_node_id === target) ||
            (e.from_node_id === target && e.to_node_id === source))
      );
    },
    [topoEdges]
  );

  return (
    <ReactFlow
      nodes={nodes}
      edges={edges}
      onNodesChange={handleNodesChange}
      onEdgesChange={handleEdgesChange}
      onConnect={onConnect}
      onConnectStart={onConnectStart}
      onConnectEnd={onConnectEnd}
      onInit={(instance) => {
        rfInstance.current = instance;
      }}
      isValidConnection={isValidConnection}
      onNodeClick={onNodeClick}
      onEdgeClick={onEdgeClick}
      onPaneClick={onPaneClick}
      nodeTypes={nodeTypes}
      edgeTypes={edgeTypes}
      fitView
      fitViewOptions={{ padding: 0.2, duration: 400 }}
      className="bg-gray-900"
    >
      <Background color="#374151" gap={20} />
      <Controls className="!bg-gray-700 !border-gray-600 !text-gray-300" />
      {/* Canvas toolbar: auto-layout + interface-detail toggle */}
      <Panel position="top-left" className="flex items-center gap-2">
        <button
          onClick={runAutoLayout}
          className="px-2.5 py-1 bg-gray-700 hover:bg-gray-600 border border-gray-600 rounded text-xs text-gray-200 transition-colors duration-150"
        >
          ✨ {t(language, 'autoLayoutLabel')}
        </button>
        <label className="flex items-center gap-1.5 px-2.5 py-1 bg-gray-700 border border-gray-600 rounded text-xs text-gray-200 cursor-pointer transition-colors duration-150 hover:bg-gray-600">
          <input
            type="checkbox"
            checked={showInterfaces}
            onChange={(e) => setShowInterfaces(e.target.checked)}
            className="rounded"
          />
          {t(language, 'showInterfacesLabel')}
        </label>
      </Panel>
      <MiniMap
        nodeColor={(n) => {
          const role = (n.data as Record<string, unknown>)?.role as string;
          switch (role) {
            case 'router': return '#3b82f6';
            case 'relay': return '#eab308';
            case 'gateway': return '#a855f7';
            case 'client': return '#06b6d4';
            default: return '#22c55e';
          }
        }}
        className="!bg-gray-800"
      />
    </ReactFlow>
  );
}
