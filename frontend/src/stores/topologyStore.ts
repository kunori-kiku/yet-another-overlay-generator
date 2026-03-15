import { create } from 'zustand';
import type {
  Topology,
  Project,
  Domain,
  Node,
  Edge,
  ValidateResponse,
  CompileResponse,
} from '../types/topology';

interface TopologyState {
  // 数据
  project: Project;
  domains: Domain[];
  nodes: Node[];
  edges: Edge[];

  // 编译/校验结果
  validateResult: ValidateResponse | null;
  compileResult: CompileResponse | null;
  isCompiling: boolean;
  isValidating: boolean;
  error: string | null;

  // 选中状态
  selectedNodeId: string | null;
  selectedEdgeId: string | null;

  // Project 操作
  setProject: (project: Partial<Project>) => void;

  // Domain CRUD
  addDomain: (domain: Domain) => void;
  updateDomain: (id: string, updates: Partial<Domain>) => void;
  removeDomain: (id: string) => void;

  // Node CRUD
  addNode: (node: Node) => void;
  updateNode: (id: string, updates: Partial<Node>) => void;
  removeNode: (id: string) => void;

  // Edge CRUD
  addEdge: (edge: Edge) => void;
  updateEdge: (id: string, updates: Partial<Edge>) => void;
  removeEdge: (id: string) => void;

  // 选中
  selectNode: (id: string | null) => void;
  selectEdge: (id: string | null) => void;

  // API 操作
  validate: () => Promise<void>;
  compile: () => Promise<void>;
  exportArtifacts: () => Promise<void>;

  // 工具
  getTopology: () => Topology;
  loadTopology: (topo: Topology) => void;
  reset: () => void;
}

const defaultProject: Project = {
  id: 'project-1',
  name: 'New Project',
  version: '0.1.0',
};

export const useTopologyStore = create<TopologyState>((set, get) => ({
  // 初始数据
  project: { ...defaultProject },
  domains: [],
  nodes: [],
  edges: [],
  validateResult: null,
  compileResult: null,
  isCompiling: false,
  isValidating: false,
  error: null,
  selectedNodeId: null,
  selectedEdgeId: null,

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
    set((state) => ({
      domains: state.domains.filter((d) => d.id !== id),
      // 同时移除归属此 domain 的节点
      nodes: state.nodes.filter((n) => n.domain_id !== id),
    })),

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
  selectNode: (id) => set({ selectedNodeId: id, selectedEdgeId: null }),
  selectEdge: (id) => set({ selectedEdgeId: id, selectedNodeId: null }),

  // 获取完整拓扑
  getTopology: () => {
    const { project, domains, nodes, edges } = get();
    return { project, domains, nodes, edges };
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
    }),

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
      set({ compileResult: data, isCompiling: false });
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
      // 下载 zip
      const blob = await res.blob();
      const url = URL.createObjectURL(blob);
      const a = document.createElement('a');
      a.href = url;
      a.download = `${get().project.id}-artifacts.zip`;
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
}));
