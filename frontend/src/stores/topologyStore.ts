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
import { detectSystemLanguage, type UILanguage } from '../i18n';

interface TopologyState {
  // 数据
  project: Project;
  domains: Domain[];
  nodes: Node[];
  edges: Edge[];

  // 历史快照
  history: CompileHistoryEntry[];

  // 编译/校验结果
  validateResult: ValidateResponse | null;
  compileResult: CompileResponse | null;
  isCompiling: boolean;
  isValidating: boolean;
  error: string | null;

  // 界面状态
  viewMode: 'topology' | 'audit';
  setViewMode: (mode: 'topology' | 'audit') => void;

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
  updateEdge: (id: string, updates: Partial<Edge>) => void;
  removeEdge: (id: string) => void;

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
  exportProject: () => void;
  importProject: (file: File) => Promise<void>;
  clearHistory: () => void;
  flushWorkspace: () => void;
}

const defaultProject: Project = {
  id: 'project-1',
  name: 'New Project',
  version: '0.1.0',
};

const defaultLanguage: UILanguage = detectSystemLanguage();

export const useTopologyStore = create<TopologyState>()(
  persist(
    (set, get) => ({
      // 初始数据
      project: { ...defaultProject },
      domains: [],
      nodes: [],
      edges: [],
      history: [],
      validateResult: null,
      compileResult: null,
      isCompiling: false,
      isValidating: false,
      error: null,
      viewMode: 'topology',
      selectedNodeId: null,
      selectedEdgeId: null,
      selectedDomainId: null,
        language: defaultLanguage,

      setLanguage: (lang) => set({ language: lang }),

  // UI
  setViewMode: (mode) => set({ viewMode: mode }),

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

  // 选中
  selectNode: (id) => set({ selectedNodeId: id, selectedEdgeId: null, selectedDomainId: null }),
  selectEdge: (id) => set({ selectedEdgeId: id, selectedNodeId: null, selectedDomainId: null }),
  selectDomain: (id) => set({ selectedDomainId: id, selectedNodeId: null, selectedEdgeId: null }),

  // 获取完整拓扑
  getTopology: () => {
    const { project, domains, nodes, edges } = get();
    return { project, domains, nodes, edges };
  },

  exportProject: () => {
    const topo = get().getTopology();
    const dataStr = "data:text/json;charset=utf-8," + encodeURIComponent(JSON.stringify(topo, null, 2));
    const downloadAnchorNode = document.createElement('a');
    downloadAnchorNode.setAttribute("href",     dataStr);
    downloadAnchorNode.setAttribute("download", `${topo.project.id || 'project'}.json`);
    document.body.appendChild(downloadAnchorNode);
    downloadAnchorNode.click();
    downloadAnchorNode.remove();
  },

  importProject: async (file: File) => {
    try {
      const text = await file.text();
      const topo = JSON.parse(text) as Topology;
      if (topo.project && topo.domains && topo.nodes && topo.edges) {
        get().loadTopology(topo);
      } else {
        throw new Error('Invalid project file format');
      }
    } catch (err) {
      set({ error: err instanceof Error ? err.message : 'Import failed' });
    }
  },

  // 加载拓扑
  loadTopology: (topo) =>
    set({
      project: topo.project,
      domains: topo.domains,
      nodes: topo.nodes,
      edges: topo.edges,
      validateResult: null,
      compileResult: null,
      error: null,
    }),

  // 重置
  reset: () =>
    set({
      project: { ...defaultProject },
      domains: [],
      nodes: [],
      edges: [],
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
      domains: [],
      nodes: [],
      edges: [],
      history: [],
      validateResult: null,
      compileResult: null,
      isCompiling: false,
      isValidating: false,
      error: null,
      viewMode: 'topology',
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
        id: crypto.randomUUID(),
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
        history: [newHistoryEntry, ...state.history].slice(0, 50), // keep last 50
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
        history: state.history,
        language: state.language,
      }),
    }
  )
);
