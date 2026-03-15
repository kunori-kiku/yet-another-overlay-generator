import { useCallback, useMemo } from 'react';
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
    addEdge: addTopoEdge,
    removeNode: removeTopoNode,
    removeEdge: removeTopoEdge,
    selectNode,
    selectEdge,
    selectDomain,
  } = useTopologyStore();

  // 构建 domain 名称索引
  const domainMap = useMemo(() => {
    const m: Record<string, string> = {};
    domains.forEach((d) => (m[d.id] = d.name));
    return m;
  }, [domains]);

  // 将拓扑节点转为 React Flow 节点
  const flowNodes: FlowNode[] = useMemo(
    () =>
      topoNodes.map((n, i) => ({
        id: n.id,
        type: 'custom',
        position: { x: 100 + (i % 4) * 250, y: 100 + Math.floor(i / 4) * 200 },
        data: {
          label: n.name,
          role: n.role,
          overlayIp: n.overlay_ip || '',
          domainName: domainMap[n.domain_id] || '',
        },
      })),
    [topoNodes, domainMap]
  );

  // 计算平行边索引（同一对节点之间的多条边）
  const parallelEdgeInfo = useMemo(() => {
    const pairMap: Record<string, string[]> = {};
    const enabledEdges = topoEdges.filter((e) => e.is_enabled);

    for (const e of enabledEdges) {
      // 双向合并: 将 A->B 和 B->A 归为同一对
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
          const label = e.endpoint_host
            ? `${e.endpoint_host}:${e.endpoint_port || ''}`
            : e.type;

          return {
            id: e.id,
            source: e.from_node_id,
            target: e.to_node_id,
            type: 'custom',
            data: {
              edgeType: e.type,
              label,
              parallelIndex: pInfo.index,
              parallelCount: pInfo.count,
            },
            markerEnd: { type: MarkerType.ArrowClosed },
          };
        }),
    [topoEdges, parallelEdgeInfo]
  );

  const [nodes, setNodes, onNodesChange] = useNodesState(flowNodes);
  const [edges, setEdges, onEdgesChange] = useEdgesState(flowEdges);

  const handleNodesChange = useCallback(
    (changes: NodeChange[]) => {
      onNodesChange(changes);
      for (const change of changes) {
        if (change.type === 'remove') {
          removeTopoNode(change.id);
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

  // 同步 React Flow 节点变化
  useMemo(() => {
    setNodes(flowNodes);
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
            default: return '#22c55e';
          }
        }}
        className="!bg-gray-800"
      />
    </ReactFlow>
  );
}
