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
import { detectSystemLanguage, txt, type UILanguage } from '../i18n';
import { uuid } from '../lib/uuid';

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
  loadTopology: (topo: Topology) => void;
  reset: () => void;
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

      setLanguage: (lang) => set({ language: lang }),

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
      const topo = JSON.parse(text) as Topology;
      if (topo.project && topo.domains && topo.nodes && topo.edges) {
        // D45/D55：route_policies 为保留特性，校验会拒绝非空数组。导入时若文件携带了
        // 非空 route_policies，则剥离它并通过 error 状态给出可见提示，避免静默丢弃。
        const hasReservedRoutePolicies =
          Array.isArray(topo.route_policies) && topo.route_policies.length > 0;
        if (hasReservedRoutePolicies) {
          delete topo.route_policies;
        }
        // loadTopology 只接收四个切片 + 版本号，这里先加载再补提示，
        // 因为 loadTopology 会清空 error。
        get().loadTopology(topo);
        if (hasReservedRoutePolicies) {
          const { language } = get();
          set({
            error: txt(
              language,
              'route_policies 为保留特性（尚未实现），已从导入的项目中移除。',
              'route_policies is a reserved feature (not yet implemented) and was removed from the imported project.'
            ),
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
  loadTopology: (topo) =>
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
    }),

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
    })),

  // API: 校验
  validate: async () => {
    set({ isValidating: true, error: null });
    try {
      const topo = get().getTopology();
      const res = await fetch('/api/validate', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(topo),
      });
      if (!res.ok) {
        const errData = await res.json();
        throw new Error(errData.error || '校验失败');
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
    set({ isCompiling: true, error: null });
    try {
      const topo = get().getTopology();
      const res = await fetch('/api/compile', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(topo),
      });
      if (!res.ok) {
        const errData = await res.json();
        throw new Error(errData.error || '编译失败');
      }
      const data: CompileResponse = await res.json();
      
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
        error: err instanceof Error ? err.message : '编译请求失败',
        isCompiling: false,
      });
    }
  },

  // API: 导出
  exportArtifacts: async () => {
    try {
      const topo = get().getTopology();
      const res = await fetch('/api/export', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(topo),
      });
      if (!res.ok) {
        const errData = await res.json();
        throw new Error(errData.error || '导出失败');
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
    try {
      const topo = get().getTopology();
      const res = await fetch(`/api/deploy-script?format=${format}`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(topo),
      });
      if (!res.ok) {
        const errData = await res.json();
        throw new Error(errData.error || 'Failed to generate deploy script');
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
      partialize: (state) => ({
        project: state.project,
        domains: state.domains,
        nodes: state.nodes,
        edges: state.edges,
        // Spec E 规则 R0：版本号也要持久化，刷新页面后仍能往返编译器写入的分配方案。
        allocSchemaVersion: state.allocSchemaVersion,
        language: state.language,
        // 画布偏好与语言同级持久化：刷新后保持用户选择的接口详情展开状态。
        showInterfaces: state.showInterfaces,
      }),
    }
  )
);
