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
import { stripPrivateKeys } from '../lib/custody';
// useControllerStore is read LAZILY (getState() inside actions, never at module
// init) so the controller↔topology store cycle stays runtime-only — symmetric to how
// controllerStore reads useTopologyStore.getState(). Needed for mode-aware import
// custody (plan-5, D5).
import { useControllerStore } from './controllerStore';

interface TopologyState {
  // 数据
  project: Project;
  domains: Domain[];
  nodes: Node[];
  edges: Edge[];
  // 分配方案版本号（Spec E 规则 R0）：由编译器写入并原样回传/持久化。
  // 缺省 0 表示尚未编译；getTopology 仅在 >0 时回送，避免污染从未编译的拓扑。
  allocSchemaVersion: number;

  // 历史快照
  history: CompileHistoryEntry[];

  // 编译/校验结果
  validateResult: ValidateResponse | null;
  compileResult: CompileResponse | null;
  isCompiling: boolean;
  isValidating: boolean;
  error: string | null;

  // 画布 UI 偏好：是否在节点卡片上展开已编译接口详情（纯展示）。
  // 接口是编译产物而非绘图原语 —— 连线手势始终是节点对节点，端口由后端分配；
  // 因此接口详情默认收起，按需展开，避免误导用户「连线 = 选择某个接口/端口」。
  showInterfaces: boolean;
  setShowInterfaces: (show: boolean) => void;

  // 选中状态
  selectedNodeId: string | null;
  selectedEdgeId: string | null;
  selectedDomainId: string | null;
  language: UILanguage;
  setLanguage: (lang: UILanguage) => void;

  // Project 操作
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
  // 为指定主链路 edge 创建一条备份链路（role: 'backup'）：复制 from/to/type/transport/
  // endpoint_host，但不复制端口与任何 pin（备份链路自有独立分配，由后端按 edge 重新 pin）。
  // 追加后选中新边并返回其 id；primaryEdgeId 不存在时返回 null。
  // 参见 docs/spec/data-model/edge.md（§Parallel links）。
  addBackupEdge: (primaryEdgeId: string) => string | null;
  updateEdge: (id: string, updates: Partial<Edge>) => void;
  removeEdge: (id: string) => void;
  // 当某节点的 public_endpoints 主机变更/移除时，同步指向它的 edge 上快照的 endpoint_host
  reconcileEdgeEndpoints: (
    nodeId: string,
    oldHost: string,
    newHost: string | null
  ) => void;

  // 选中
  selectNode: (id: string | null) => void;
  selectEdge: (id: string | null) => void;
  selectDomain: (id: string | null) => void;

  // API 操作
  validate: () => Promise<void>;
  compile: () => Promise<void>;
  exportArtifacts: () => Promise<void>;
  downloadDeployScript: (format: 'sh' | 'ps1') => Promise<void>;

  // 工具
  getTopology: () => Topology;
  // fromServer 标记本次加载是否来自控制器服务端 hydration（区别于本地导入/快照）：服务端
  // 拉来的设计含机密 fleet 数据（公网 IP/SSH 目标），controller 模式下绝不能落盘或在登出后
  // 残留。见 canvasFromServer 的安全不变量。
  loadTopology: (topo: Topology, fromServer?: boolean) => void;
  // 画布来源（安全不变量）：true 表示当前画布内容是「服务端权威的机密数据」——它由一次服务端
  // hydration 写入（loadTopology(topo,true)），或由一次 deploy() 写到服务端后标记（部署后本地画布
  // 即等同服务端权威副本）；其后的本地编辑仍视为派生机密数据。controller 模式下为 true 时：
  // ①不持久化到 localStorage（partialize 置空），②登出/会话失效时清空（controllerStore），
  // ③未登录时从登录门「切回本地」是整画布重置而非保图。本地原创工作（导入/新建/reset）置 false，
  // 正常持久化——那是用户自有数据。
  // 已知边界（均为 minor，刻意取舍）：(a) 与服务端字节完全相同的「空对空」no-op hydration 提前
  // 返回，不改来源标记——但此时本地与服务端无差异，无机密可泄漏；(b) 尚未部署、也非服务端来的
  // 「controller 模式草稿」标记为 false 以便登出后保留未保存草稿（代价：本机登出态可见，敏感度低于
  // 已部署的 fleet 数据）；一旦部署即转为 true 受保护。
  canvasFromServer: boolean;
  setCanvasFromServer: (v: boolean) => void;
  reset: () => void;
  // 模式边界清洗（plan-5，D6）：controller→local 切换时调用。图（project/domains/节点身份/
  // edges）保留，但清空一切密钥材料与编译产物——私钥/公钥、overlay_ip、edge 的 compiled_port
  // 与 pinned_* 分配、alloc_schema_version、编译历史/结果。下次本地编译会重新生成一套干净的
  // 密钥与分配。切换是有损操作，调用方须先经用户确认。
  purgeModeBoundaryState: () => void;
  // 控制器模式导入占位计数（plan-5，D5）：importProject 在 controller 模式下剥离导入文件里的
  // 私钥后，置为被占位的数量（0=无提示）。Shell 据此显示一条可关闭提示（用 txt() 实时本地化）。
  importPlaceholdered: number;
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

// UX-3：新工作区预置一个默认网络域，使“连接两台公网服务器”的首位用户无需先理解
// CIDR/分配模式就能立即添加节点（去掉了“先建域”的不透明前置门槛）。
// CIDR 选用 10.20.0.0/24 —— 刻意避开 10.10.0.0/24（transit 地址池），否则会与
// 每条链路的 transit IP 冲突。transit_cidr 留空，让后端沿用其 10.10.0.0/24 默认值。
// 每次调用返回全新对象/数组，避免共享引用被后续状态变更原地改写。
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

export const useTopologyStore = create<TopologyState>()(
  persist(
    (set, get) => ({
      // 初始数据
      // UX-3：种入默认网络域（见 makeDefaultDomains 注释）。已持久化的工作区在 rehydrate
      // 时会用 localStorage 中的 domains 覆盖此初始值（persist 默认浅合并 + partialize 持久化
      // domains），因此既有项目不受影响，只有全新工作区才会看到这个默认域。
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
      importPlaceholdered: 0,
      // 默认 false：全新/本地工作区不是服务端机密数据。仅 hydrateFromServer 会置 true。
      canvasFromServer: false,

      setCanvasFromServer: (v) => set({ canvasFromServer: v }),

      setLanguage: (lang) => set({ language: lang }),

      dismissImportNotice: () => set({ importPlaceholdered: 0 }),

  // UI
  setShowInterfaces: (show) => set({ showInterfaces: show }),

  // Project
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
        // 同时移除归属此 domain 的节点
        nodes: state.nodes.filter((n) => n.domain_id !== id),
        // 同时移除与被删除节点关联的边，避免孤儿 edge
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
      // 同时移除关联的边
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

  // 复制主链路 edge → 备份链路（role: 'backup'）。只复制逻辑链路意图字段
  // （from/to/type/transport/endpoint_host），不复制 compiled_port/endpoint_port 与
  // 任何 pin：备份链路必须拥有独立的端口 / transit IP / 链路本地地址，由后端按 edge 重新分配。
  // 追加后通过 selectEdge 语义选中新边（清空 node/domain 选中），返回新边 id；找不到则返回 null。
  // 参见 docs/spec/data-model/edge.md（§Parallel links）。
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

  removeEdge: (id) =>
    set((state) => ({
      edges: state.edges.filter((e) => e.id !== id),
      selectedEdgeId: state.selectedEdgeId === id ? null : state.selectedEdgeId,
    })),

  // 节点 public_endpoints 发生变更时，把指向该节点、且快照了旧主机的 edge 同步过来。
  // newHost 为字符串：主机被改名 → 改写 endpoint_host 并清空陈旧的 compiled_port。
  // newHost 为 null：该主机被移除 → 清空 endpoint_host / endpoint_port / compiled_port，
  // 让连接退回“后端自动解析”状态，避免拨向已不存在的目标。
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

  // 选中
  selectNode: (id) => set({ selectedNodeId: id, selectedEdgeId: null, selectedDomainId: null }),
  selectEdge: (id) => set({ selectedEdgeId: id, selectedNodeId: null, selectedDomainId: null }),
  selectDomain: (id) => set({ selectedDomainId: id, selectedNodeId: null, selectedEdgeId: null }),

  // 获取完整拓扑
  getTopology: () => {
    const { project, domains, nodes, edges, allocSchemaVersion } = get();
    const topo: Topology = { project, domains, nodes, edges };
    // Spec E 规则 R0：仅在已编译（>0）时回送版本号，让编译器写入的值原样往返。
    if (allocSchemaVersion > 0) {
      topo.alloc_schema_version = allocSchemaVersion;
    }
    return topo;
  },

  // 导出当前设计为 JSON 下载。filename 可选（默认 <project.id>.json）——hydration 的
  // 一次性备份（plan-4，D9）用它命名 pre-hydration-backup-<date>.json。
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
        // D45/D55：route_policies 为保留特性，校验会拒绝非空数组。导入时若文件携带了
        // 非空 route_policies，则剥离它并通过 error 状态给出可见提示，避免静默丢弃。
        const hasReservedRoutePolicies =
          Array.isArray(topo.route_policies) && topo.route_policies.length > 0;
        if (hasReservedRoutePolicies) {
          delete topo.route_policies;
        }
        // 控制器模式导入占位（plan-5，D5）：controller 模式是零知识的，导入文件携带的私钥
        // 必须被剥离（节点改用 agent 持有的密钥），并提醒用户。本地模式不受影响（私钥在
        // 本地/气隙模式下是合法的设计数据，往返保留）。
        let placeholdered = 0;
        if (useControllerStore.getState().mode === 'controller') {
          const result = stripPrivateKeys(topo);
          topo = result.topo;
          placeholdered = result.stripped;
        }
        // loadTopology 只接收四个切片 + 版本号，这里先加载再补提示，
        // 因为 loadTopology 会清空 error。
        get().loadTopology(topo);
        // 总是写入（含 0）：一次干净导入（0 个被占位）必须清掉上一次导入残留的「N 个被
        // 占位」横幅，否则提示会粘滞（plan-5 review）。
        set({ importPlaceholdered: placeholdered });
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

  // 加载拓扑（导入项目 / 恢复快照）。保持四切片语义：只接收 project/domains/nodes/edges，
  // 外加 Spec E 规则 R0 的 alloc_schema_version（文件中存在时读取，否则归零）。
  // D75：清空历史与选中状态，避免导入的新项目与上一份项目的快照做无意义的 diff。
  loadTopology: (topo, fromServer = false) =>
    set({
      project: topo.project,
      domains: topo.domains,
      nodes: topo.nodes,
      edges: topo.edges,
      allocSchemaVersion: topo.alloc_schema_version ?? 0,
      history: [],
      validateResult: null,
      compileResult: null,
      error: null,
      selectedNodeId: null,
      selectedEdgeId: null,
      selectedDomainId: null,
      // 本地导入/快照恢复默认 false；只有服务端 hydration 传 true。原子设置（与切片同一次 set），
      // 避免「先 false 落盘服务端数据、再翻 true」的瞬态持久化窗口。
      canvasFromServer: fromServer,
    }),

  // 模式边界清洗（plan-5，D6）：controller→local 切换的有损动作。图保留（project/domains/
  // 节点身份/edges/能力/endpoint/ssh 等），但每个节点的密钥材料（私钥/公钥/fixed 标志）与
  // overlay_ip、每条 edge 的编译产物（compiled_port + 全部 pinned_* 分配）、拓扑级
  // alloc_schema_version、以及编译历史/结果一并清空。这样下次本地编译会重新生成一套干净、
  // 自洽的密钥与分配，绝不把舰队（fleet）用过的密钥残留在浏览器里。字段逐一显式枚举（而非
  // 模式匹配），新增 secret/pin 字段时须同步更新这里（plan-5.5 插入点的清洗清单完整性风险）。
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
      // 切到本地模式后，控制器模式导入残留的「已占位」横幅也一并清掉（plan-5 review）。
      importPlaceholdered: 0,
      error: null,
      selectedNodeId: null,
      selectedEdgeId: null,
      selectedDomainId: null,
      // 切回本地后图归操作员本地所有，不再是服务端机密镜像：清除来源标记，恢复正常持久化。
      canvasFromServer: false,
    })),

  // 重置
  // D75：与 loadTopology 一致，连同历史与版本号一并清空，避免残留快照在下一份项目里继续 diff。
  reset: () =>
    set({
      project: { ...defaultProject },
      // UX-3：与初始状态保持一致 —— 重置后仍预置默认网络域，避免把用户重新丢回
      // “没有域、添加节点按钮被禁用”的死胡同。
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
      // UX-3：与 reset / 初始状态保持一致，预置默认网络域。
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

  // API: 校验
  validate: async () => {
    set({ isValidating: true, error: null });
    try {
      const topo = get().getTopology();
      // 控制器模式下 /api/validate 已置于 operator-auth 之后（plan-12 / T6）。该路由就在面板自身
      // （operator）源上，httpOnly session cookie 会随同源请求自动带上；附上 operator 凭据让 POST 通过：
      // 优先 Bearer（内存里的 session/break-glass token，免 CSRF），刷新后无内存 token 时回退到
      // cookie + 双提交 CSRF 头（与 controllerClient 的 configOf 一致）。本地模式不加任何头（路由公开）。
      const headers: Record<string, string> = { 'Content-Type': 'application/json' };
      const cs = useControllerStore.getState();
      if (cs.mode === 'controller') {
        const bearer = cs.sessionToken || cs.operatorToken;
        if (bearer) headers['Authorization'] = `Bearer ${bearer}`;
        if (cs.csrfToken) headers['X-CSRF-Token'] = cs.csrfToken;
      }
      const res = await fetch('/api/validate', {
        method: 'POST',
        headers,
        body: JSON.stringify(topo),
      });
      if (!res.ok) {
        throw new Error(await readApiErrorMessage(res, 'error.validateFailed', get().language));
      }
      const data: ValidateResponse = await res.json();
      set({ validateResult: data, isValidating: false });
    } catch (err) {
      set({
        error: err instanceof Error ? err.message : '校验请求失败',
        isValidating: false,
      });
    }
  },

  // API: 编译
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
      const res = await fetch('/api/compile', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(topo),
      });
      if (!res.ok) {
        throw new Error(await readApiErrorMessage(res, 'error.compileFailed', get().language));
      }
      const data: CompileResponse = await res.json();

      // In-flight mode-flip guard: the front-door check above only rejects FRESH
      // invocations. If the operator switched to controller mode while this air-gap
      // compile was in flight, the response carries reconstructed private keys
      // (data.topology.nodes). Persisting them now would write fleet private keys into
      // the controller-mode store and its localStorage mirror — exactly the boundary
      // the zero-knowledge custody model forbids. Drop the result instead.
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
        // Spec E 规则 R0：把编译器写入的分配方案版本号回吸到 store，保证下次编译与持久化都带上它。
        allocSchemaVersion: data.topology.alloc_schema_version ?? state.allocSchemaVersion,
        history: [newHistoryEntry, ...state.history].slice(0, 5), // keep last 5
      }));
    } catch (err) {
      set({
        error: err instanceof Error ? err.message : 'Compile request failed',
        isCompiling: false,
      });
    }
  },

  // API: 导出
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
      const res = await fetch('/api/export', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(topo),
      });
      if (!res.ok) {
        throw new Error(await readApiErrorMessage(res, 'error.exportFailed', get().language));
      }

      const blob = await res.blob();
      const disposition = res.headers.get('Content-Disposition') || '';
      const filenameMatch = disposition.match(/filename\*=UTF-8''([^;]+)|filename="?([^";]+)"?/i);
      const inferredName = decodeURIComponent(filenameMatch?.[1] || filenameMatch?.[2] || 'artifacts.zip');

      const url = URL.createObjectURL(blob);
      const a = document.createElement('a');
      a.href = url;
      a.download = inferredName;
      document.body.appendChild(a);
      a.click();
      document.body.removeChild(a);
      URL.revokeObjectURL(url);
    } catch (err) {
      set({
        error: err instanceof Error ? err.message : '导出请求失败',
      });
    }
  },

  clearHistory: () => set({ history: [] }),

  // API: 下载部署脚本
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
      const res = await fetch(`/api/deploy-script?format=${format}`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(topo),
      });
      if (!res.ok) {
        throw new Error(await readApiErrorMessage(res, 'error.deployScriptFailed', get().language));
      }

      const blob = await res.blob();
      const filename = format === 'ps1' ? 'deploy-all.ps1' : 'deploy-all.sh';
      const url = URL.createObjectURL(blob);
      const a = document.createElement('a');
      a.href = url;
      a.download = filename;
      document.body.appendChild(a);
      a.click();
      document.body.removeChild(a);
      URL.revokeObjectURL(url);
    } catch (err) {
      set({
        error: err instanceof Error ? err.message : 'Deploy script download failed',
      });
    }
  },
    }),
    {
      name: 'topology-storage',
      // We only persist these properties to avoid saving volatile UI state like isCompiling or errors
      partialize: (state) => {
        // 安全不变量（controller server-authoritative）：服务端 hydrate 来的设计含机密 fleet
        // 数据（公网 IP/SSH 目标）。controller 模式下绝不把它落盘——否则登出后任何拿到浏览器的
        // 人都能从 localStorage 读出（或一键「切回本地」渲染出来）。D1 说画布是「可丢弃的镜像」：
        // 登录会从服务端重新 hydrate，故无需持久化。本地模式、或本地原创工作（canvasFromServer
        // =false）照常持久化——那是用户自己机器上的自有数据，不是机密镜像。mode 跨 store 惰性读取
        // （与 importProject 同一手法，运行时取值规避模块级循环依赖）。init-safety：本 store 未配置
        // persist version/migrate，故 partialize 不会在模块初始化期（hydrate 阶段）被调用——首次调用
        // 发生在两个 store 都已就绪后的用户态 set()，此时 useControllerStore.getState() 必定可用。
        const serverHeld =
          state.canvasFromServer && useControllerStore.getState().mode === 'controller';
        return {
          project: serverHeld ? { ...defaultProject } : state.project,
          domains: serverHeld ? makeDefaultDomains() : state.domains,
          nodes: serverHeld ? [] : state.nodes,
          edges: serverHeld ? [] : state.edges,
          // Spec E 规则 R0：版本号也要持久化，刷新页面后仍能往返编译器写入的分配方案。
          allocSchemaVersion: serverHeld ? 0 : state.allocSchemaVersion,
          // 来源标记本身要持久化：刷新后若仍未登录，登录门据此知道画布是机密镜像而整体重置。
          canvasFromServer: state.canvasFromServer,
          language: state.language,
          // 画布偏好与语言同级持久化：刷新后保持用户选择的接口详情展开状态。
          showInterfaces: state.showInterfaces,
        };
      },
    }
  )
);
