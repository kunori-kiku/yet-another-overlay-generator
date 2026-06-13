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

  // 焦点透明度（Decisions #11）：连线拖拽进行中标志。onConnectStart 置位、
  // onConnectEnd 复位（含中途取消的拖拽）；拖拽期间所有边弱化、所有节点保持全不透明。
  const [connecting, setConnecting] = useState(false);

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

  // 链路角色徽标（contract item 5 / Decisions #5）：按「无向节点对」分组所有 enabled 边，
  // 单边对不设徽标（保持简洁观感）；多边对内：
  //   - backup 边（role === 'backup'）→ 'b1','b2',...，按 topoEdges 出现顺序在本对备份中取序号；
  //   - 同向多余的 roleless/primary 边（同 from->to，首条胜，镜像后端 D71）→ 'duplicate' 告警；
  //   - 其余 primary class 边（代表边 + 反向 roleless 边，归并为同一主链路）→ 'primary'（★）。
  // 同时产出 edgeId → 'b1'/'★' 的角色标记映射，供节点接口徽标复用以与边扇形序号一致。
  // 注：声明在 nodeInterfaceMap 之前 —— 后者依赖 edgeRoleMarker 计算节点 chip 的 ★/bN 标记。
  const { edgeRoleChip, edgeRoleMarker } = useMemo(() => {
    const enabledEdges = topoEdges.filter((e) => e.is_enabled);

    // 无向对分组（与 parallelEdgeInfo 的 pairKey 口径一致）
    const pairMap: Record<string, typeof enabledEdges> = {};
    for (const e of enabledEdges) {
      const pairKey = [e.from_node_id, e.to_node_id].sort().join('::');
      if (!pairMap[pairKey]) pairMap[pairKey] = [];
      pairMap[pairKey].push(e);
    }

    const chip: Record<string, string> = {};
    const marker: Record<string, string> = {};
    for (const edges of Object.values(pairMap)) {
      if (edges.length <= 1) continue; // 单边对：无徽标
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
        // primary class（role 为空或 'primary'）。同向去重镜像 D71：首条胜。
        const direction = `${e.from_node_id}->${e.to_node_id}`;
        if (firstPrimaryByDirection[direction]) {
          chip[e.id] = 'duplicate'; // 同向多余 → 告警徽标（无节点接口角色标记）
          continue;
        }
        firstPrimaryByDirection[direction] = true;
        chip[e.id] = 'primary';
        marker[e.id] = '★';
      }
    }
    return { edgeRoleChip: chip, edgeRoleMarker: marker };
  }, [topoEdges]);

  // 构建每个节点的已编译接口详情（节点卡片上的展示徽标，受「显示接口详情」开关控制）。
  // 注意：接口不再充当连接手柄 —— 连线手势是节点对节点，端口由后端编译时分配。
  //
  // Decisions #12：改用共享的 edge-aware 解析器 resolveNodeInterfaces —— 通过 pinned 端口
  // 把每个已编译接口匹配回它的边（端口在节点内唯一），避免旧版「从接口名剥离 wg- 反推
  // peer 名」对 backup 接口（wg-<clean8><hash4>）渲染出垃圾 chip。RightPanel 复用同一解析器。
  // 这里把解析结果（peerName / listenPort / role / edgeId / 真实接口名）映射成节点卡片 chip，
  // 并据 role + edgeId 计算与边扇形一致的角色标记（★ / bN）；'unknown' 的 peerName 由解析器
  // 回退为接口名原文（永不剥离 wg-），不带标记。
  interface IfaceChip {
    name: string;        // 真实接口名（tooltip 用，永不剥离 wg-）
    listenPort: number;  // 后端分配的监听端口
    peerName: string;    // 对端节点名（'unknown' 时回退为接口名）
    roleMarker?: string; // '★' / 'b1' / ... 或 undefined
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
          // 与边扇形序号一致：从 edgeRoleMarker 取该 backup 边的 bN。
          roleMarker = info.edgeId ? edgeRoleMarker[info.edgeId] : undefined;
        }
        // role === 'unknown'：peerName 已是接口名原文，不带标记。
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
  //   pending —— 「待编译」信号 → 虚线。注意 compiled_port 只对带 endpoint_host 的
  //   边写回（compiler.go 的 CompiledPort 写回规则），无 endpoint_host 的被动边要用
  //   pin 字段（每条 enabled 边编译后都有）判断是否已编译，否则会永远显示虚线。
  //   带 endpoint_host 的边仍以 compiled_port 为准：拨号相关编辑会清掉它（D19），
  //   虚线回退正是「需要重新编译」的可视反馈。
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
              roleChip: edgeRoleChip[e.id], // ★ / bN / duplicate / undefined（单边对）
            },
            markerEnd: { type: MarkerType.ArrowClosed },
          };
        }),
    [topoEdges, parallelEdgeInfo, topoNodes, edgeRoleChip]
  );

  // 焦点透明度判定（Decisions #11，逐字）：根据当前选中态 + 连线拖拽态算出
  // 「弱化」谓词，注入节点/边的 data.deemphasized（在下方同步 effect 中应用）。
  // 优先级：连线拖拽 > 选中边 > 选中节点 > 无（全亮）。
  //   - 连线拖拽中：所有边弱化，所有节点全亮（节点是落点目标）；
  //   - 选中节点：除该节点本身 + 与其相连的边外全部弱化（远端节点照样弱化，#11 字面）；
  //   - 选中边：除该边本身 + 其两端节点外全部弱化；
  //   - 背景点击清空选中 → 谓词回到「全不弱化」（onPaneClick 已清空，谓词自然恢复）。
  const deemphasis = useMemo<{
    isNodeDeemphasized: (id: string) => boolean;
    isEdgeDeemphasized: (id: string) => boolean;
  }>(() => {
    // 连线拖拽中：所有边弱化、所有节点全亮（节点是落点目标）。优先级最高。
    if (connecting) {
      return {
        isNodeDeemphasized: () => false,
        isEdgeDeemphasized: () => true,
      };
    }

    // 选中边：除该边本身 + 其两端节点外全部弱化。
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

    // 选中节点：除该节点本身 + 与其相连的边外全部弱化（远端节点照样弱化，#11 字面）。
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

    // 无选中、未拖拽：全亮（背景点击清空选中后自然回到这里）。
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
          // 焦点透明度（Decisions #11）：弱化谓词注入 data，CustomNode 据此淡出根容器。
          data: { ...fn.data, deemphasized: deemphasis.isNodeDeemphasized(fn.id) },
          // 保留 React Flow 标记的选中态，避免同步时选中描边闪断
          selected: existing?.selected,
          position: positionMap.current[fn.id] || existing?.position || fn.position,
        };
      })
    );
  }, [flowNodes, setNodes, deemphasis]);

  // keyed 边同步：渲染字段未变的边保留原对象身份（含 selected 标记），
  // 避免旧版整批 setEdges(flowEdges) 在任何 store 变更时重建全部边对象、
  // 触发整层边重渲染的卡顿/闪烁。
  useEffect(() => {
    setEdges((current) => {
      const prevById = new Map(current.map((e) => [e.id, e]));
      let changed = current.length !== flowEdges.length;
      const next = flowEdges.map((feBase) => {
        // 焦点透明度（Decisions #11）：把弱化谓词注入 data 后再做 keyed 等价比较，
        // 弱化态变化时（deemphasized ∈ edgeRenderEqual keys）边对象会被替换并重渲染。
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
        // 用 uuid() 而非毫秒时间戳生成边 ID：两次快速连线会落在同一毫秒，
        // 导致 ID 冲突，之后任何按 ID 进行的编辑/删除都会同时命中两条边（修复 D17）。
        const id = `edge-${uuid()}`;
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

  // 焦点透明度（Decisions #11）：连线拖拽开始 → 置位 connecting（所有边弱化、节点全亮）。
  const onConnectStart = useCallback(() => {
    setConnecting(true);
  }, []);

  // onConnectEnd 始终复位 connecting —— 包括中途取消（落在空白处）的拖拽，
  // 保证拖拽结束后焦点透明度恢复正常（成功连线时 onConnect 已先于此处理新边选中）。
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
      {/* 画布工具栏：自动布局 + 接口详情开关 */}
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
