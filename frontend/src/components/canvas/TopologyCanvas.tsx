import { useCallback, useMemo, useRef } from 'react';
import {
  ReactFlow,
  Background,
  Controls,
  MiniMap,
  addEdge,
  useNodesState,
  useEdgesState,
  type Connection,
  type Edge as FlowEdge,
  type Node as FlowNode,
  type NodeChange,
  type EdgeChange,
  MarkerType,
} from '@xyflow/react';
import '@xyflow/react/dist/style.css';
import { CustomNode } from './CustomNode';
import { CustomEdge } from './CustomEdge';
import { useTopologyStore } from '../../stores/topologyStore';

const nodeTypes = { custom: CustomNode };
const edgeTypes = { custom: CustomEdge };

export function TopologyCanvas() {
  const {
    nodes: topoNodes,
    edges: topoEdges,
    domains,
    compileResult,
    addEdge: addTopoEdge,
    removeNode: removeTopoNode,
    removeEdge: removeTopoEdge,
    selectNode,
    selectEdge,
    selectDomain,
  } = useTopologyStore();

  // Persist node positions across re-renders so dragging is not lost
  const positionMap = useRef<Record<string, { x: number; y: number }>>({});

  // 构建 domain 名称索引
  const domainMap = useMemo(() => {
    const m: Record<string, string> = {};
    domains.forEach((d) => (m[d.id] = d.name));
    return m;
  }, [domains]);

  // 构建每个节点的已编译接口详情（用于多 handle 显示）
  interface IfaceInfo {
    name: string;       // e.g. "wg-beta"
    listenPort: number; // allocated listen port
    peerName: string;   // remote node name (e.g. "beta")
  }
  const nodeInterfaceMap = useMemo(() => {
    const m: Record<string, IfaceInfo[]> = {};
    if (!compileResult) return m;
    for (const [key, config] of Object.entries(compileResult.wireguard_configs)) {
      const colonIdx = key.indexOf(':');
      if (colonIdx < 0) continue;
      const nodeId = key.slice(0, colonIdx);
      const ifaceName = key.slice(colonIdx + 1);

      const portMatch = config?.match(/ListenPort\s*=\s*(\d+)/);
      const listenPort = portMatch ? parseInt(portMatch[1], 10) : 0;

      // Derive peer name from interface name: "wg-beta" → "beta"
      const peerName = ifaceName.startsWith('wg-') ? ifaceName.slice(3) : ifaceName;

      if (!m[nodeId]) m[nodeId] = [];
      m[nodeId].push({ name: ifaceName, listenPort, peerName });
    }
    return m;
  }, [compileResult]);

  // 将拓扑节点转为 React Flow 节点
  const flowNodes: FlowNode[] = useMemo(
    () =>
      topoNodes.map((n, i) => {
        // Use persisted position if available, otherwise assign grid position
        if (!positionMap.current[n.id]) {
          positionMap.current[n.id] = {
            x: 100 + (i % 4) * 280,
            y: 100 + Math.floor(i / 4) * 250,
          };
        }
        return {
          id: n.id,
          type: 'custom',
          position: positionMap.current[n.id],
          data: {
            label: n.name,
            role: n.role,
            overlayIp: n.overlay_ip || '',
            domainName: domainMap[n.domain_id] || '',
            interfaces: nodeInterfaceMap[n.id] || [],
          },
        };
      }),
    [topoNodes, domainMap, nodeInterfaceMap]
  );

  // 计算平行边索引（同一对节点之间的多条边）
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

  // 将拓扑边转为 React Flow 边（使用自定义 edge）
  const flowEdges: FlowEdge[] = useMemo(
    () =>
      topoEdges
        .filter((e) => e.is_enabled)
        .map((e) => {
          const pInfo = parallelEdgeInfo[e.id] || { index: 0, count: 1 };
          const displayPort = e.compiled_port || e.endpoint_port || '';
          const label = e.endpoint_host
            ? `${e.endpoint_host}:${displayPort}`
            : e.type;

          let targetHandle: string | undefined;
          let sourceHandle: string | undefined;
          let sourceNodeName = '';
          let targetNodeName = '';
          if (compileResult) {
            const sourceNode = topoNodes.find((n) => n.id === e.from_node_id);
            const targetNode = topoNodes.find((n) => n.id === e.to_node_id);
            sourceNodeName = sourceNode?.name || '';
            targetNodeName = targetNode?.name || '';

            if (sourceNode && targetNode) {
              const targetIfaces = nodeInterfaceMap[e.to_node_id] || [];
              const sourceIfaces = nodeInterfaceMap[e.from_node_id] || [];

              const srcIfaceName = `wg-${sourceNode.name.toLowerCase().replace(/[^a-z0-9-]/g, '-')}`.slice(0, 15);
              if (targetIfaces.some((iface) => iface.name === srcIfaceName)) {
                targetHandle = srcIfaceName;
              }

              const tgtIfaceName = `wg-${targetNode.name.toLowerCase().replace(/[^a-z0-9-]/g, '-')}`.slice(0, 15);
              if (sourceIfaces.some((iface) => iface.name === tgtIfaceName)) {
                sourceHandle = tgtIfaceName;
              }
            }
          } else {
            const sourceNode = topoNodes.find((n) => n.id === e.from_node_id);
            const targetNode = topoNodes.find((n) => n.id === e.to_node_id);
            sourceNodeName = sourceNode?.name || '';
            targetNodeName = targetNode?.name || '';
          }

          return {
            id: e.id,
            source: e.from_node_id,
            target: e.to_node_id,
            type: 'custom',
            ...(targetHandle ? { targetHandle } : {}),
            ...(sourceHandle ? { sourceHandle } : {}),
            data: {
              edgeType: e.type,
              label,
              parallelIndex: pInfo.index,
              parallelCount: pInfo.count,
              sourceNodeName,
              targetNodeName,
            },
            markerEnd: { type: MarkerType.ArrowClosed },
          };
        }),
    [topoEdges, parallelEdgeInfo, compileResult, topoNodes, nodeInterfaceMap]
  );

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

  // Sync data changes (name, role, interfaces, etc.) without overwriting positions
  useMemo(() => {
    setNodes((currentNodes) =>
      flowNodes.map((fn) => {
        const existing = currentNodes.find((n) => n.id === fn.id);
        return {
          ...fn,
          position: positionMap.current[fn.id] || existing?.position || fn.position,
        };
      })
    );
  }, [flowNodes, setNodes]);

  useMemo(() => {
    setEdges(flowEdges);
  }, [flowEdges, setEdges]);

  const onConnect = useCallback(
    (params: Connection) => {
      setEdges((eds) => addEdge({ ...params, type: 'custom', data: { edgeType: 'direct', label: 'direct', parallelIndex: 0, parallelCount: 1 }, markerEnd: { type: MarkerType.ArrowClosed } }, eds));

      if (params.source && params.target) {
        const id = `edge-${Date.now()}`;
        const targetNode = topoNodes.find((n) => n.id === params.target);
        const preferredEndpoint = targetNode?.public_endpoints?.[0];

        addTopoEdge({
          id,
          from_node_id: params.source,
          to_node_id: params.target,
          type: 'direct',
          endpoint_host: preferredEndpoint?.host,
          endpoint_port: preferredEndpoint?.port,
          transport: 'udp',
          is_enabled: true,
        });
        selectEdge(id);
      }
    },
    [setEdges, addTopoEdge, topoNodes, selectEdge]
  );

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

  return (
    <ReactFlow
      nodes={nodes}
      edges={edges}
      onNodesChange={handleNodesChange}
      onEdgesChange={handleEdgesChange}
      onConnect={onConnect}
      onNodeClick={onNodeClick}
      onEdgeClick={onEdgeClick}
      onPaneClick={onPaneClick}
      nodeTypes={nodeTypes}
      edgeTypes={edgeTypes}
      fitView
      className="bg-gray-900"
    >
      <Background color="#374151" gap={20} />
      <Controls className="!bg-gray-700 !border-gray-600 !text-gray-300" />
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
