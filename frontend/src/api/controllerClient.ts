// 控制器面板的 HTTP 客户端。每个函数针对 internal/api/handler_controller.go 暴露的
// operator-facing 路由：
//   <baseURL><pathPrefix>/api/v1/controller/<route>
// 鉴权统一是 Authorization: Bearer <operatorToken>。后端响应是 snake_case JSON，本层
// 在边界处把它映射成 camelCase 的 controller 类型（见 ../types/controller）。
//
// 错误约定：任何非 2xx 都抛出 Error(`${status} ${body}`)，让 store 把原始状态码与正文
// 直接呈现给 operator（控制器是机器对机器的纯 JSON 接口，不做花哨的错误包装）。

import type {
  ControllerNode,
  ControllerAuditEntry,
  StageResult,
} from '../types/controller';

// 控制器连接配置：operator base URL、可选的 secret path 前缀、operator bearer token。
// 注意这是连接层配置，agentBaseURL 等面板偏好留在 store，不参与请求构造。
export interface ControllerConfig {
  baseURL: string;
  pathPrefix: string;
  operatorToken: string;
}

// 把用户输入的 secret path 前缀规范化为 "" 或 "/<seg>"（单个前导斜杠、无尾随斜杠），
// 与后端 SetPathPrefix 的归一化规则保持一致。
function normalizePrefix(prefix: string): string {
  const p = prefix.trim().replace(/^\/+/, '').replace(/\/+$/, '');
  return p === '' ? '' : '/' + p;
}

// 构造一条控制器路由的完整 URL。baseURL 去掉尾随斜杠，避免与 path 前缀拼出双斜杠。
export function ctlURL(cfg: ControllerConfig, route: string): string {
  const base = cfg.baseURL.replace(/\/+$/, '');
  return `${base}${normalizePrefix(cfg.pathPrefix)}/api/v1/controller/${route}`;
}

// --- 后端 snake_case 响应形状（仅本模块内部使用，映射后即丢弃）---

interface NodeJSON {
  node_id: string;
  status: string;
  has_wg_public_key: boolean;
  desired_generation: number;
  applied_generation: number;
  last_checksum: string;
  last_health: string;
  last_seen: string;
  enrolled_at: string;
}

interface AuditEntryJSON {
  timestamp: string;
  actor: string;
  action: string;
  node_id: string;
}

interface AuditResponseJSON {
  entries: AuditEntryJSON[] | null;
  verified: boolean;
}

interface StageResponseJSON {
  staged: string[] | null;
  skipped_unenrolled: string[] | null;
  generation: number;
}

interface GenerationResponseJSON {
  generation: number;
}

interface EnrollmentTokenResponseJSON {
  token: string;
}

interface RevokeResponseJSON {
  node_id: string;
  revoked: boolean;
}

// --- 共享 request 辅助 ---

// 发起一个带 Bearer 鉴权的请求；非 2xx 抛 Error(`${status} ${body}`)。
async function request(
  cfg: ControllerConfig,
  route: string,
  init?: RequestInit
): Promise<Response> {
  const headers = new Headers(init?.headers);
  headers.set('Authorization', `Bearer ${cfg.operatorToken}`);
  const res = await fetch(ctlURL(cfg, route), { ...init, headers });
  if (!res.ok) {
    const body = await res.text();
    throw new Error(`${res.status} ${body}`);
  }
  return res;
}

// 发起一个 JSON-body 的 POST（自动带上 Content-Type 与 Bearer）。
function postJSON(
  cfg: ControllerConfig,
  route: string,
  body: string
): Promise<Response> {
  return request(cfg, route, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body,
  });
}

// --- snake_case → camelCase 映射 ---

function mapNode(n: NodeJSON): ControllerNode {
  return {
    nodeId: n.node_id,
    status: n.status as ControllerNode['status'],
    hasWGPublicKey: n.has_wg_public_key,
    desiredGeneration: n.desired_generation,
    appliedGeneration: n.applied_generation,
    lastChecksum: n.last_checksum,
    lastHealth: n.last_health,
    lastSeen: n.last_seen,
    enrolledAt: n.enrolled_at,
  };
}

function mapAuditEntry(e: AuditEntryJSON): ControllerAuditEntry {
  return {
    timestamp: e.timestamp,
    actor: e.actor,
    action: e.action,
    nodeId: e.node_id,
  };
}

// --- 公开 API（每个都接收 (cfg, ...)）---

// 列出整个 fleet 注册表（operator-only）。
export async function getNodes(cfg: ControllerConfig): Promise<ControllerNode[]> {
  const res = await request(cfg, 'nodes', { method: 'GET' });
  const data = (await res.json()) as NodeJSON[] | null;
  return (data ?? []).map(mapNode);
}

// 拉取审计链以及它是否完整可校验（operator-only）。
export async function getAudit(
  cfg: ControllerConfig
): Promise<{ entries: ControllerAuditEntry[]; verified: boolean }> {
  const res = await request(cfg, 'audit', { method: 'GET' });
  const data = (await res.json()) as AuditResponseJSON;
  return {
    entries: (data.entries ?? []).map(mapAuditEntry),
    verified: data.verified,
  };
}

// 取回当前存储的拓扑 JSON（operator-only）。返回 unknown：存储的字节是 public-keys-only
// 的拓扑，本层不强加结构（由调用方按需解释）。
export async function getTopology(cfg: ControllerConfig): Promise<unknown> {
  const res = await request(cfg, 'topology', { method: 'GET' });
  return (await res.json()) as unknown;
}

// 为某节点铸造一次性 enrollment token，返回明文 token（仅此一次可见）。
export async function mintEnrollmentToken(
  cfg: ControllerConfig,
  nodeId: string,
  ttlSeconds: number
): Promise<string> {
  const res = await postJSON(
    cfg,
    'enrollment-token',
    JSON.stringify({ node_id: nodeId, ttl_seconds: ttlSeconds })
  );
  const data = (await res.json()) as EnrollmentTokenResponseJSON;
  return data.token;
}

// 上传一份新拓扑版本（operator-only）。topoJSON 是已序列化的 model.Topology JSON
// 字符串，原样作为请求 body 提交。
export async function updateTopology(
  cfg: ControllerConfig,
  topoJSON: string
): Promise<void> {
  await postJSON(cfg, 'update-topology', topoJSON);
}

// 把已 enroll 的子图编译并暂存到下一代（operator-only）。
export async function stage(cfg: ControllerConfig): Promise<StageResult> {
  const res = await postJSON(cfg, 'stage', '');
  const data = (await res.json()) as StageResponseJSON;
  return {
    staged: data.staged ?? [],
    skippedUnenrolled: data.skipped_unenrolled ?? [],
    generation: data.generation,
  };
}

// 把暂存的 bundle 翻转为 current 并 bump 代号（operator-only），唤醒 /poll 等待者。
export async function promote(
  cfg: ControllerConfig
): Promise<{ generation: number }> {
  const res = await postJSON(cfg, 'promote', '');
  const data = (await res.json()) as GenerationResponseJSON;
  return { generation: data.generation };
}

// 驱逐一个节点（operator-only），其 bearer 凭据立即失效。
export async function revoke(cfg: ControllerConfig, nodeId: string): Promise<void> {
  const res = await postJSON(cfg, 'revoke', JSON.stringify({ node_id: nodeId }));
  // 消费响应体以释放连接；revoked 标志在成功时恒为 true，调用方无需分支。
  await (res.json() as Promise<RevokeResponseJSON>);
}
