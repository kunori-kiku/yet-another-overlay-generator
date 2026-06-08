import { create } from 'zustand';
import { persist } from 'zustand/middleware';
import type {
  ControllerNode,
  ControllerAuditEntry,
  StageResult,
} from '../types/controller';
import type { ControllerConfig } from '../api/controllerClient';
import {
  getNodes,
  getAudit,
  mintEnrollmentToken,
  updateTopology,
  stage,
  promote,
  revoke,
} from '../api/controllerClient';
import { useTopologyStore } from './topologyStore';

// 控制器面板（Mode B）状态。它是 controller 连接 + fleet 视图的单一来源，独立于
// topologyStore（后者仍是拓扑数据的唯一来源）。deploy() 时从 topologyStore 读取当前
// 拓扑并复用 compile() 发送的同一 model.Topology JSON 形状。
interface ControllerState {
  // 连接配置（baseURL/pathPrefix/operatorToken 组成 ControllerConfig；agentBaseURL 是
  // EnrollmentFlow 给节点用的 agent 端口地址，仅作展示，不参与 operator 请求构造）。
  baseURL: string;
  pathPrefix: string;
  agentBaseURL: string;
  operatorToken: string;

  // fleet 视图
  nodes: ControllerNode[];
  audit: ControllerAuditEntry[];
  auditVerified: boolean;
  lastDeploy: StageResult | null;

  // 易失 UI 状态
  loading: boolean;
  error: string | null;
  lastSyncedAt: number | null;

  // actions
  setConfig: (partial: Partial<ControllerConfig & { agentBaseURL: string }>) => void;
  refresh: () => Promise<void>;
  mintToken: (nodeId: string, ttl: number) => Promise<string>;
  deploy: () => Promise<void>;
  revoke: (nodeId: string) => Promise<void>;
}

// 从连接字段切出 controllerClient 需要的 ControllerConfig（不含 agentBaseURL）。
function configOf(state: ControllerState): ControllerConfig {
  return {
    baseURL: state.baseURL,
    pathPrefix: state.pathPrefix,
    operatorToken: state.operatorToken,
  };
}

// step-up SEAM（Plan-5）：在 stage/promote 这类敏感的 promote-to-fleet 操作之前要求一次
// 用户密钥确认。v1 立即 resolve（无操作），未来在此挂接硬件 / Bitwarden 签名钩子。
function requireUserKey(): Promise<void> {
  // Plan-5 hardware/Bitwarden signing hooks here.
  return Promise.resolve();
}

export const useControllerStore = create<ControllerState>()(
  persist(
    (set, get) => ({
      // 默认连接配置（见 DESIGN：operator 默认 :8080，agent 默认 :9090）。
      baseURL: 'http://localhost:8080',
      pathPrefix: '',
      agentBaseURL: 'http://localhost:9090',
      operatorToken: '',

      nodes: [],
      audit: [],
      auditVerified: false,
      lastDeploy: null,

      loading: false,
      error: null,
      lastSyncedAt: null,

      setConfig: (partial) => set(partial),

      // 刷新 fleet 视图：并行拉取 nodes + audit。任一失败则记录 error，并保持已有视图不变。
      refresh: async () => {
        set({ loading: true, error: null });
        try {
          const cfg = configOf(get());
          const [nodes, audit] = await Promise.all([getNodes(cfg), getAudit(cfg)]);
          set({
            nodes,
            audit: audit.entries,
            auditVerified: audit.verified,
            loading: false,
            lastSyncedAt: Date.now(),
          });
        } catch (err) {
          set({
            error: err instanceof Error ? err.message : 'Failed to refresh controller state',
            loading: false,
          });
        }
      },

      // 为某节点铸造一次性 enrollment token，返回明文 token（仅此一次可见）。
      mintToken: async (nodeId, ttl) => {
        return mintEnrollmentToken(configOf(get()), nodeId, ttl);
      },

      // 部署当前拓扑到 fleet：复用 topologyStore.compile() 发往 /api/compile 的同一
      // model.Topology JSON 形状（getTopology() → {project,domains,nodes,edges,...}），
      // 经 update-topology → stage → promote → refresh。stage/promote 之前过一次
      // requireUserKey() step-up seam（Plan-5 在此挂接签名钩子）。
      deploy: async () => {
        set({ loading: true, error: null });
        try {
          const cfg = configOf(get());
          const topo = useTopologyStore.getState().getTopology();
          const topoJSON = JSON.stringify(topo);
          await updateTopology(cfg, topoJSON);
          // step-up：把任何用户密钥确认放在改动 fleet 状态（stage/promote）之前。
          await requireUserKey();
          const result = await stage(cfg);
          // 当没有已注册节点时 stage 不产生任何 bundle（staged 为空），此时 promote 会
          // 返回 409 ErrNoStagedBundle —— 那不是错误，而是「还没有节点入网」。直接展示
          // skippedUnenrolled，跳过 promote，避免把正常情况渲染成报错。
          if (result.staged.length > 0) {
            await promote(cfg);
          }
          set({ lastDeploy: result, loading: false });
          await get().refresh();
        } catch (err) {
          set({
            error: err instanceof Error ? err.message : 'Deploy failed',
            loading: false,
          });
        }
      },

      // 驱逐一个节点后刷新视图。
      revoke: async (nodeId) => {
        set({ loading: true, error: null });
        try {
          await revoke(configOf(get()), nodeId);
          set({ loading: false });
          await get().refresh();
        } catch (err) {
          set({
            error: err instanceof Error ? err.message : 'Revoke failed',
            loading: false,
          });
        }
      },
    }),
    {
      name: 'controller-storage',
      // 仅持久化连接端点，绝不持久化 operatorToken（密钥不落 localStorage），
      // 也不持久化易失的 fleet 视图 / loading / error。
      partialize: (state) => ({
        baseURL: state.baseURL,
        pathPrefix: state.pathPrefix,
        agentBaseURL: state.agentBaseURL,
      }),
    }
  )
);
