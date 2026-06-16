// 控制器面板（plan-4.5 networked controller）的前端数据模型。
// 这些类型镜像 internal/api/handler_controller.go 的 operator-facing JSON 形状，
// 但统一用 camelCase（从后端的 snake_case 映射而来，映射发生在 controllerClient.ts）。

// 节点在控制器注册表中的生命周期状态。镜像 controller.NodeStatus：
// 'pending'（已 enroll、待 operator approve）/ 'approved'（已纳入编译子图）/
// 'revoked'（已驱逐，bearer 凭据立即失效）。
export type ControllerNodeStatus = 'pending' | 'approved' | 'revoked';

// 一台注册节点的 operator 视图。刻意不含任何密钥材料（既无 WG 公钥字节，也无 API
// token 哈希）：hasWGPublicKey 仅表明公钥已在档。镜像 handler_controller.go 的 nodeJSON。
export interface ControllerNode {
  nodeId: string;
  status: ControllerNodeStatus;
  hasWGPublicKey: boolean;
  desiredGeneration: number;
  appliedGeneration: number;
  lastChecksum: string;
  lastHealth: string;
  // plan-4：agent 上报的构建版本（observability）；版本感知前的旧 agent 上报为空串，UI 显示「—」。
  agentVersion: string;
  lastSeen: string;
  enrolledAt: string;
  // plan-4.6 fleet-wide key rotation：operator 已为该节点请求轮换 WG 密钥，等待 agent
  // 重新生成本地私钥并经 POST /rekey 注册新公钥（注册成功后由后端清零此标志）。
  rekeyRequested: boolean;
  // controller-panel-rollout-ui plan-1: server-computed agent self-update rollout membership
  // (AgentRolloutNodeIDs — the canary subset, or the whole fleet once promoted). The per-node
  // update-status chip reads it; the panel never re-derives canary membership client-side.
  inRollout: boolean;
}

// 审计链中的一条记录。镜像 controller.AuditEntry 的 operator-facing 字段。
export interface ControllerAuditEntry {
  timestamp: string;
  actor: string;
  action: string;
  nodeId: string;
}

// /stage 的结果：被编译进本代的节点、因未 enroll 被跳过的节点、以及暂存代号。
// 镜像 handler_controller.go 的 stageResponseJSON。
export interface StageResult {
  staged: string[];
  skippedUnenrolled: string[];
  generation: number;
}
