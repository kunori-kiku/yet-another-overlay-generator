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

// --- keystone (off-host operator signing) wire types ---
//
// These mirror internal/trustlist/types.go (SignedTrustList) and the keystone
// routes in internal/api/handler_controller.go. The byte-level contract — every
// base64url field is RFC4648 url-alphabet WITHOUT padding (Go base64.RawURLEncoding),
// the challenge binding, and the rpid binding — lives in ../lib/webauthn.ts, which
// is the single place that builds these structs.

// WebAuthnAlg is the keystone signing algorithm. Only ES256 and EdDSA WebAuthn
// assertions are accepted by the node verifier; RS256 etc. are rejected.
export type WebAuthnAlg = 'webauthn-es256' | 'webauthn-eddsa';

// Wire constants for the two accepted algorithms (kept here so both the client
// and the webauthn helper import the literal from one place).
export const AlgWebAuthnES256 = 'webauthn-es256' as const;
export const AlgWebAuthnEdDSA = 'webauthn-eddsa' as const;

// SignedTrustList is the detached-signature artifact the operator's authenticator
// produces over a deploy's canonical membership manifest. Field names + base64url
// encodings match trustlist.SignedTrustList exactly (snake_case JSON, RawURLEncoding
// on every base64url field). It is carried as the `signed` field of POST
// /trustlist-signature.
export interface SignedTrustList {
  alg: WebAuthnAlg;
  credential_id: string; // base64url(rawId)
  public_key: string; // pinned PKIX PEM (audit only; node verifies the PINNED key)
  signature: string; // base64url(response.signature) — ES256 is ASN.1 DER
  authenticator_data: string; // base64url(response.authenticatorData)
  client_data_json: string; // base64url(response.clientDataJSON)
}

// trustListResponseJSON shape from GET /trustlist: the canonical manifest bytes
// (STANDARD base64) to be signed, plus the membership epoch they carry.
interface TrustListResponseJSON {
  trustlist_json: string; // standard base64 of the canonical manifest bytes
  epoch: number;
}

// The panel-facing GET /trustlist result: trustlistJson is STANDARD base64 (the
// caller base64-decodes it to recover the canonical bytes whose SHA-256 is the
// WebAuthn challenge). A null return means the keystone is OFF for the tenant
// (404 = no operator credential pinned / nothing staged to sign).
export interface TrustListToSign {
  trustlistJson: string;
  epoch: number;
}

// operatorCredentialRequestJSON shape for POST /operator-credential: the pinned
// off-host signing credential. public_key_pem is the PKIX "PUBLIC KEY" PEM; rpid
// MUST equal the rp.id used at create() time (the node binds SHA256(rpid) to the
// assertion rpIdHash); origin is advisory on the node.
export interface OperatorCredentialBody {
  alg: WebAuthnAlg;
  credentialId: string; // base64url(rawId)
  publicKeyPEM: string; // PKIX "PUBLIC KEY" PEM
  rpId: string; // location.hostname
  origin: string; // location.origin
}

// 把用户输入的 secret path 前缀规范化为 "" 或 "/<seg>"（单个前导斜杠、无尾随斜杠），
// 与后端 SetPathPrefix 的归一化规则保持一致。
function normalizePrefix(prefix: string): string {
  const p = prefix.trim().replace(/^\/+/, '').replace(/\/+$/, '');
  return p === '' ? '' : '/' + p;
}

// 构造一条控制器路由的完整 URL。baseURL 去掉尾随斜杠，避免与 path 前缀拼出双斜杠。
// baseURL 必须是绝对 http(s) URL：否则 fetch 会相对于面板自身的 origin 解析，把 operator
// bearer token 发到错误的源（凭据泄露）。非法时直接抛错，由调用方写入 store.error。
export function ctlURL(cfg: ControllerConfig, route: string): string {
  const base = cfg.baseURL.trim().replace(/\/+$/, '');
  let parsed: URL;
  try {
    parsed = new URL(base);
  } catch {
    throw new Error('controller URL must be an absolute http(s) URL, e.g. http://localhost:8080');
  }
  if (parsed.protocol !== 'http:' && parsed.protocol !== 'https:') {
    throw new Error('controller URL must use http or https');
  }
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
  rekey_requested: boolean;
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

interface RekeyAllResponseJSON {
  requested: number;
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
    rekeyRequested: n.rekey_requested,
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

// 为整个 fleet 请求一次 WG 密钥轮换（operator-only，plan-4.6 ROUTINE tier）：把每个已审批
// 节点标记为 RekeyRequested。这是 zero-knowledge 流程的起点——控制器从不接触私钥，各 agent
// 自行重生本地密钥并经 /rekey 注册新公钥。返回被标记的节点数。注意：标记后还需再 Deploy 一次，
// 待节点重新注册新公钥，新一代配置才会携带全员新公钥使 fleet 收敛。
export async function rekeyAll(cfg: ControllerConfig): Promise<{ requested: number }> {
  const res = await postJSON(cfg, 'rekey-all', '');
  const data = (await res.json()) as RekeyAllResponseJSON;
  return { requested: data.requested };
}

// --- keystone (off-host operator signing) ---

// getTrustlist fetches the STAGED membership manifest the operator must sign
// (operator-only). It returns the canonical bytes as STANDARD base64 plus the
// epoch — the panel base64-decodes trustlistJson and signs SHA-256 of those bytes.
//
// A 404 means the keystone is OFF (no operator credential pinned, or nothing
// staged): the handler returns 404 from GET /trustlist when there is no staged
// manifest, and the keystone is only ON once a credential is pinned. We map 404
// to null so deploy() can promote directly (today's behavior) when the keystone
// is off, and only run the signing ceremony when a manifest comes back.
export async function getTrustlist(cfg: ControllerConfig): Promise<TrustListToSign | null> {
  const headers = new Headers();
  headers.set('Authorization', `Bearer ${cfg.operatorToken}`);
  const res = await fetch(ctlURL(cfg, 'trustlist'), { method: 'GET', headers });
  if (res.status === 404) {
    // Drain the body to release the connection; 404 = keystone OFF.
    await res.text();
    return null;
  }
  if (!res.ok) {
    const body = await res.text();
    throw new Error(`${res.status} ${body}`);
  }
  const data = (await res.json()) as TrustListResponseJSON;
  return { trustlistJson: data.trustlist_json, epoch: data.epoch };
}

// postTrustlistSignature submits the operator's off-host signature over the
// staged manifest (operator-only). trustlistJson is the STANDARD base64 of the
// exact bytes signed (server-side substitution guard) and signed is the
// SignedTrustList assembled by ../lib/webauthn.ts. A non-2xx (e.g. 400 verify
// failure, 409 manifest changed, 412 no credential pinned) throws as usual.
export async function postTrustlistSignature(
  cfg: ControllerConfig,
  body: { trustlistJson: string; signed: SignedTrustList },
): Promise<void> {
  await postJSON(
    cfg,
    'trustlist-signature',
    JSON.stringify({ trustlist_json: body.trustlistJson, signed: body.signed }),
  );
}

// postOperatorCredential pins the off-host operator signing credential, turning
// the keystone ON for the tenant (operator-only). The body's PEM must parse for
// the declared alg server-side (a malformed pin is a 400). rpid/origin carry the
// WebAuthn relying-party binding the node enforces.
export async function postOperatorCredential(
  cfg: ControllerConfig,
  body: OperatorCredentialBody,
): Promise<void> {
  await postJSON(
    cfg,
    'operator-credential',
    JSON.stringify({
      alg: body.alg,
      credential_id: body.credentialId,
      public_key_pem: body.publicKeyPEM,
      rpid: body.rpId,
      origin: body.origin,
    }),
  );
}
