import { useCallback, useEffect, useMemo, useRef } from 'react';
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
import { txt, STRINGS } from '../../i18n';

const nodeTypes = { custom: CustomNode };
const edgeTypes = { custom: CustomEdge };

// 自动布局的节点尺寸缺省值（React Flow 尚未量测时使用）
const DEFAULT_NODE_WIDTH = 180;
const DEFAULT_NODE_HEIGHT = 110;

// 边的渲染等价判定：keyed 同步用。只有渲染相关字段变化时才替换边对象，
// 保持对象身份稳定 → React Flow 跳过未变边的重渲染（消除整层边闪烁）。
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
  } = useTopologyStore();

  // Persist node positions across re-renders so dragging is not lost
  const positionMap = useRef<Record<string, { x: number; y: number }>>({});
  // onInit 捕获实例：自动布局完成后做带动画的 fitView（无需 ReactFlowProvider 包裹）。
  const rfInstance = useRef<ReactFlowInstance | null>(null);
  // 自动布局动画帧句柄：重复点击/卸载时取消未完成的动画。
  const layoutAnimation = useRef<number | null>(null);

  // 构建 domain 名称索引
  const domainMap = useMemo(() => {
    const m: Record<string, string> = {};
    domains.forEach((d) => (m[d.id] = d.name));
    return m;
  }, [domains]);

  // 构建每个节点的已编译接口详情（节点卡片上的展示徽标，受「显示接口详情」开关控制）。
  // 注意：接口不再充当连接手柄 —— 连线手势是节点对节点，端口由后端编译时分配。
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

  // 将拓扑节点转为 React Flow 节点。
  // 纯计算：渲染期间不读写 positionMap ref（react-hooks/refs 约束）。
  // 这里只给出默认网格位置；已拖拽/持久化的位置在下方同步 effect 中合并。
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

  // 将拓扑边转为 React Flow 边（使用自定义 edge）。
  // 边始终在节点级锚点之间渲染（不再路由到接口手柄）：边是节点对节点的逻辑链路，
  // 接口/端口是它的编译产物。端口语义拆成结构化字段交给 CustomEdge 渲染徽标：
  //   port    —— compiled_port（后端分配真值）或显式 endpoint_port 覆盖；
  //   pending —— compiled_port 为空（未编译，或被拨号相关编辑失效）→ 虚线。
  const flowEdges: FlowEdge[] = useMemo(
    () =>
      topoEdges
        .filter((e) => e.is_enabled)
        .map((e) => {
          const pInfo = parallelEdgeInfo[e.id] || { index: 0, count: 1 };
          const port = e.compiled_port || e.endpoint_port || undefined;
          const pending = !e.compiled_port;
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
            },
            markerEnd: { type: MarkerType.ArrowClosed },
          };
        }),
    [topoEdges, parallelEdgeInfo, topoNodes]
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

  // 数据变更（名称、角色、接口等）同步进 React Flow 状态，但不覆盖拖拽位置。
  // setState 与 positionMap ref 的读写属于副作用，必须在 effect 中执行，
  // 而不是渲染期间的 useMemo（修复审计发现 D18：渲染期副作用导致节点
  // 在无关编辑后跳回旧坐标）。
  useEffect(() => {
    setNodes((currentNodes) =>
      flowNodes.map((fn) => {
        // 首次见到该节点时，把默认网格位置登记为持久化位置
        if (!positionMap.current[fn.id]) {
          positionMap.current[fn.id] = fn.position;
        }
        const existing = currentNodes.find((n) => n.id === fn.id);
        return {
          ...fn,
          // 保留 React Flow 标记的选中态，避免同步时选中描边闪断
          selected: existing?.selected,
          position: positionMap.current[fn.id] || existing?.position || fn.position,
        };
      })
    );
  }, [flowNodes, setNodes]);

  // keyed 边同步：渲染字段未变的边保留原对象身份（含 selected 标记），
  // 避免旧版整批 setEdges(flowEdges) 在任何 store 变更时重建全部边对象、
  // 触发整层边重渲染的卡顿/闪烁。
  useEffect(() => {
    setEdges((current) => {
      const prevById = new Map(current.map((e) => [e.id, e]));
      let changed = current.length !== flowEdges.length;
      const next = flowEdges.map((fe) => {
        const prev = prevById.get(fe.id);
        if (prev && edgeRenderEqual(prev, fe)) {
          return prev;
        }
        changed = true;
        return { ...fe, selected: prev?.selected };
      });
      return changed ? next : current;
    });
  }, [flowEdges, setEdges]);

  // 卸载时取消未完成的布局动画帧
  useEffect(
    () => () => {
      if (layoutAnimation.current !== null) {
        cancelAnimationFrame(layoutAnimation.current);
      }
    },
    []
  );

  // 自动布局：dagre 分层布局算出目标坐标，再用 easeOutCubic 插值平滑过渡。
  // 不用 CSS transform 过渡 —— React Flow 拖拽也走 transform，二者会互相打架；
  // 逐帧更新 position 状态是官方推荐的动画方式。动画过程同步写 positionMap，
  // 让布局结果像手动拖拽一样被持久化。
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

    // dagre 返回中心点坐标；React Flow position 是左上角。
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
        // 用 crypto.randomUUID() 而非毫秒时间戳生成边 ID：两次快速连线会落在同一毫秒，
        // 导致 ID 冲突，之后任何按 ID 进行的编辑/删除都会同时命中两条边（修复 D17）。
        const id = `edge-${crypto.randomUUID()}`;
        const targetNode = topoNodes.find((n) => n.id === params.target);
        const preferredEndpoint = targetNode?.public_endpoints?.[0];

        // 只填充 endpoint_host（目标节点的可达性提示），endpoint_port 保持为空 →
        // 由后端作为唯一端口权威自动分配监听端口。仅当运营商显式输入端口时才视为 NAT 覆盖。
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

  // 连线合法性校验（拖拽期间被高频调用，必须保持纯函数 + O(边数) 轻量）：
  // 1) 拒绝自环（source === target）—— 一个节点连自己没有意义；
  // 2) 拒绝与现有「已启用」边重复的节点对（任一方向）—— 平行边由 parallelEdgeInfo
  //    可视化，但同一对节点的重复直连边只会产生混淆且无新增语义。
  // 读取 topoEdges（拓扑真源），而非 React Flow 的 edges 派生状态。
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
      {/* 画布工具栏：自动布局 + 接口详情开关 */}
      <Panel position="top-left" className="flex items-center gap-2">
        <button
          onClick={runAutoLayout}
          className="px-2.5 py-1 bg-gray-700 hover:bg-gray-600 border border-gray-600 rounded text-xs text-gray-200 transition-colors duration-150"
        >
          ✨ {txt(language, ...STRINGS.autoLayoutLabel)}
        </button>
        <label className="flex items-center gap-1.5 px-2.5 py-1 bg-gray-700 border border-gray-600 rounded text-xs text-gray-200 cursor-pointer transition-colors duration-150 hover:bg-gray-600">
          <input
            type="checkbox"
            checked={showInterfaces}
            onChange={(e) => setShowInterfaces(e.target.checked)}
            className="rounded"
          />
          {txt(language, ...STRINGS.showInterfacesLabel)}
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
