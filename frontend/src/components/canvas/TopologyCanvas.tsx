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
  MarkerType,
} from '@xyflow/react';
import '@xyflow/react/dist/style.css';
import { CustomNode } from './CustomNode';
import { useTopologyStore } from '../../stores/topologyStore';

const nodeTypes = { custom: CustomNode };

export function TopologyCanvas() {
  const {
    nodes: topoNodes,
    edges: topoEdges,
    domains,
    addEdge: addTopoEdge,
    selectNode,
    selectEdge,
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
        position: { x: 100 + (i % 4) * 200, y: 100 + Math.floor(i / 4) * 180 },
        data: {
          label: n.name,
          role: n.role,
          overlayIp: n.overlay_ip || '',
          domainName: domainMap[n.domain_id] || '',
        },
      })),
    [topoNodes, domainMap]
  );

  // 将拓扑边转为 React Flow 边
  const flowEdges: FlowEdge[] = useMemo(
    () =>
      topoEdges
        .filter((e) => e.is_enabled)
        .map((e) => ({
          id: e.id,
          source: e.from_node_id,
          target: e.to_node_id,
          animated: e.type === 'relay-path',
          label: e.endpoint_host
            ? `${e.endpoint_host}:${e.endpoint_port || ''}`
            : e.type,
          style: { stroke: e.type === 'public-endpoint' ? '#f59e0b' : '#6b7280' },
          markerEnd: { type: MarkerType.ArrowClosed, color: '#9ca3af' },
          labelStyle: { fill: '#9ca3af', fontSize: 10 },
        })),
    [topoEdges]
  );

  const [nodes, setNodes, onNodesChange] = useNodesState(flowNodes);
  const [edges, setEdges, onEdgesChange] = useEdgesState(flowEdges);

  // 同步 React Flow 节点变化
  // 当拓扑数据变化时重新设置
  useMemo(() => {
    setNodes(flowNodes);
  }, [flowNodes, setNodes]);

  useMemo(() => {
    setEdges(flowEdges);
  }, [flowEdges, setEdges]);

  const onConnect = useCallback(
    (params: Connection) => {
      // 在 React Flow 中添加边
      setEdges((eds) => addEdge({ ...params, markerEnd: { type: MarkerType.ArrowClosed, color: '#9ca3af' } }, eds));

      // 在拓扑 store 中添加边
      if (params.source && params.target) {
        const id = `edge-${Date.now()}`;
        addTopoEdge({
          id,
          from_node_id: params.source,
          to_node_id: params.target,
          type: 'direct',
          transport: 'udp',
          is_enabled: true,
        });
      }
    },
    [setEdges, addTopoEdge]
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
  }, [selectNode, selectEdge]);

  return (
    <ReactFlow
      nodes={nodes}
      edges={edges}
      onNodesChange={onNodesChange}
      onEdgesChange={onEdgesChange}
      onConnect={onConnect}
      onNodeClick={onNodeClick}
      onEdgeClick={onEdgeClick}
      onPaneClick={onPaneClick}
      nodeTypes={nodeTypes}
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
