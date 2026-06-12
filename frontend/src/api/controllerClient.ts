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
  // operatorToken is the EFFECTIVE operator bearer: a login session token when logged
  // in, else the optional break-glass operator token. The store's configOf() picks it
  // (session preferred); this layer attaches `Authorization: Bearer <it>` when non-empty.
  // After a refresh it is empty and the httpOnly session cookie authenticates instead.
  operatorToken: string;
  // csrfToken is the in-memory double-submit CSRF token (from the login or /session
  // response). It is echoed as X-CSRF-Token on cookie-authed state-changing requests.
  // Never persisted (memory only); empty for the Bearer/break-glass path.
  csrfToken: string;
}

// LoginResult is the result of a successful POST /login: the session bearer token
// (held in MEMORY only — never persisted), the operator identity, and the session
// expiry (RFC3339).
export interface LoginResult {
  sessionToken: string;
  operator: string;
  expiresAt: string;
  // csrfToken is the double-submit token (also set as the readable yaog_csrf cookie). Held
  // in memory and echoed as X-CSRF-Token on state-changing cookie-authed requests.
  csrfToken: string;
}

// LoginOutcome is what login() returns. Either the password (and any required second
// factor) verified and a session was minted ('success'), or the password was correct
// but the operator has TOTP 2FA enrolled and must resubmit with a code
// ('totp_required'). The latter is the backend's 401 {error, totp_required:true} — it
// is NOT a hard failure, so the panel branches to "collect a code" instead of showing
// an error. A wrong password / lockout / any other non-2xx still throws.
export type LoginOutcome =
  | { kind: 'success'; result: LoginResult }
  | { kind: 'totp_required' }
  | { kind: 'passkey_required'; challenge: PasskeyChallenge };

// PasskeyChallenge is a server-issued login challenge the panel feeds to assertLogin:
// the base64url random nonce, the registered credential to assert with (credentialId =
// allow_credentials[0].id, or null when the server returned none — a passwordless decoy
// for an unknown / passkey-less username), the rpid binding, and the credential's alg
// (needed to build the SignedTrustList; an assertion response cannot reveal it). It backs
// the /login passkey_required 401, passwordless begin, and the disable re-auth leg.
export interface PasskeyChallenge {
  challenge: string;
  credentialId: string | null;
  rpid: string;
  alg: WebAuthnAlg | '';
}

// passkeyChallengeJSON mirrors the backend's passkey challenge payloads (the
// passkey_required 401 fields, passkeyChallengeResponseJSON). allow_credentials is the
// WebAuthn list (0 or 1 entry here).
interface passkeyChallengeJSON {
  challenge: string;
  allow_credentials: { type: string; id: string }[] | null;
  rpid: string;
  alg: string;
}

function mapPasskeyChallenge(d: passkeyChallengeJSON): PasskeyChallenge {
  const creds = d.allow_credentials ?? [];
  return {
    challenge: d.challenge,
    credentialId: creds.length > 0 ? creds[0].id : null,
    rpid: d.rpid,
    alg: (d.alg as WebAuthnAlg) || '',
  };
}

// LoginResponseJSON mirrors loginResponseJSON in internal/api/handler_login.go.
interface LoginResponseJSON {
  session_token: string;
  operator: string;
  expires_at: string;
  csrf_token: string;
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

// 判断 HTTP 方法是否会改写状态（用于决定 cookie 路径是否要带 CSRF 头）。
function isStateChanging(method: string): boolean {
  const m = method.toUpperCase();
  return m !== 'GET' && m !== 'HEAD' && m !== 'OPTIONS';
}

// 发起一个请求：携带凭据（credentials:'include' 让 httpOnly session cookie 随行，刷新后
// 仍登录）；持有 operatorToken（session/break-glass）时附 Bearer，否则仅靠 cookie；状态改写
// 类请求在 cookie 路径上附带 X-CSRF-Token 双提交令牌。非 2xx 抛 Error(`${status} ${body}`)。
async function request(
  cfg: ControllerConfig,
  route: string,
  init?: RequestInit
): Promise<Response> {
  const headers = new Headers(init?.headers);
  if (cfg.operatorToken) {
    headers.set('Authorization', `Bearer ${cfg.operatorToken}`);
  }
  const method = init?.method ?? 'GET';
  if (cfg.csrfToken && isStateChanging(method)) {
    headers.set('X-CSRF-Token', cfg.csrfToken);
  }
  const res = await fetch(ctlURL(cfg, route), { ...init, headers, credentials: 'include' });
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

// --- operator login (plan-5.2) ---

// login authenticates an operator (username + password, plus an optional TOTP code
// when 2FA is enrolled) and returns a LoginOutcome. UNAUTHENTICATED: it sends NO bearer
// (you log in to OBTAIN one). A 401 carrying {"totp_required":true} is returned as the
// 'totp_required' outcome (password accepted, second factor needed) rather than thrown;
// any other non-2xx throws Error(`${status} ${body}`) so the store can surface the
// controller's message verbatim (401 invalid username or password / 429 too many
// attempts).
export async function login(
  cfg: ControllerConfig,
  username: string,
  password: string,
  totp?: string,
  passkey?: SignedTrustList
): Promise<LoginOutcome> {
  const body: Record<string, unknown> = { username, password, totp: totp ?? '' };
  if (passkey) {
    body.passkey = passkey;
  }
  const res = await fetch(ctlURL(cfg, 'login'), {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(body),
    // credentials:'include' so the browser stores the httpOnly session + CSRF cookies.
    credentials: 'include',
  });
  if (!res.ok) {
    const text = await res.text();
    // A 401 carrying a second-factor flag means the password verified but a passkey
    // assertion (passkey_required) or TOTP code (totp_required) is needed — branch to the
    // ceremony, do not treat as a hard error. A wrong-password 401 (no flag) and every
    // other status still throw. Passkey takes precedence when both are checked server-side.
    if (res.status === 401) {
      try {
        const j = JSON.parse(text) as passkeyChallengeJSON & {
          passkey_required?: boolean;
          totp_required?: boolean;
        };
        if (j.passkey_required === true) {
          return { kind: 'passkey_required', challenge: mapPasskeyChallenge(j) };
        }
        if (j.totp_required === true) {
          return { kind: 'totp_required' };
        }
      } catch {
        /* not JSON — fall through to the generic error */
      }
    }
    throw new Error(`${res.status} ${text}`);
  }
  const data = (await res.json()) as LoginResponseJSON;
  return {
    kind: 'success',
    result: {
      sessionToken: data.session_token,
      operator: data.operator,
      expiresAt: data.expires_at,
      csrfToken: data.csrf_token,
    },
  };
}

// --- operator TOTP 2FA (plan-5.2) ---
//
// These four routes manage the CURRENTLY LOGGED-IN operator's optional second factor
// (internal/api/handler_totp.go). All require a real operator session — the break-glass
// token has no account, so the controller answers 403 (surfaced as a thrown Error).

// TOTPEnrollment is the just-minted, NOT-yet-active second factor from POST /totp/enroll:
// the base32 shared secret (shown so the operator can type it into an authenticator) and
// an otpauth:// URI for QR/import. It is persisted only after confirmTOTP verifies a code
// derived from it — an abandoned enroll leaves 2FA untouched.
export interface TOTPEnrollment {
  secret: string;
  otpauthURI: string;
}

interface TOTPStatusJSON {
  enabled: boolean;
}

interface TOTPEnrollJSON {
  secret: string;
  otpauth_uri: string;
}

// getTOTPStatus reports whether the current operator account has 2FA enrolled.
export async function getTOTPStatus(cfg: ControllerConfig): Promise<boolean> {
  const res = await request(cfg, 'totp/status', { method: 'GET' });
  return ((await res.json()) as TOTPStatusJSON).enabled;
}

// enrollTOTP mints a fresh secret + otpauth URI. NOTHING is persisted yet — the operator
// proves possession via confirmTOTP before 2FA turns on.
export async function enrollTOTP(cfg: ControllerConfig): Promise<TOTPEnrollment> {
  const res = await postJSON(cfg, 'totp/enroll', '');
  const d = (await res.json()) as TOTPEnrollJSON;
  return { secret: d.secret, otpauthURI: d.otpauth_uri };
}

// confirmTOTP activates 2FA: it echoes the secret from enrollTOTP plus a current code;
// the controller persists the secret only when the code verifies (else a 400 throws).
export async function confirmTOTP(
  cfg: ControllerConfig,
  secret: string,
  code: string
): Promise<void> {
  const res = await postJSON(cfg, 'totp/confirm', JSON.stringify({ secret, code }));
  await res.text();
}

// disableTOTP turns 2FA off; a current code is required so a hijacked session cannot
// trivially strip the second factor (else a 400 throws).
export async function disableTOTP(cfg: ControllerConfig, code: string): Promise<void> {
  const res = await postJSON(cfg, 'totp/disable', JSON.stringify({ code }));
  await res.text();
}

// --- operator passkey login (plan-5.2) ---
//
// A login passkey is the phishing-resistant second factor (and passwordless credential),
// distinct from the keystone signing credential. status/register/disable are operator-
// authed; the passwordless begin/finish are UNAUTHENTICATED (you log in to OBTAIN a
// session). The assertion wire shape is SignedTrustList (same as keystone signing).

// RegisterPasskeyBody is the POST /passkey/register payload: the PUBLIC half of a freshly
// created WebAuthn credential (from enrollOperatorCredential) plus the rp binding.
export interface RegisterPasskeyBody {
  alg: WebAuthnAlg;
  credentialId: string;
  publicKeyPEM: string;
  rpId: string;
  origin: string;
}

// DisablePasskeyOutcome: the two-phase disable returns either a challenge to assert
// (re-auth) or 'done' (idempotent — there was no passkey to remove).
export type DisablePasskeyOutcome =
  | { kind: 'challenge'; challenge: PasskeyChallenge }
  | { kind: 'done' };

// getPasskeyStatus reports whether the current operator has a login passkey registered.
export async function getPasskeyStatus(cfg: ControllerConfig): Promise<boolean> {
  const res = await request(cfg, 'passkey/status', { method: 'GET' });
  return ((await res.json()) as { registered: boolean }).registered;
}

// registerPasskey stores the operator's login passkey (operator-authed).
export async function registerPasskey(cfg: ControllerConfig, body: RegisterPasskeyBody): Promise<void> {
  const res = await postJSON(
    cfg,
    'passkey/register',
    JSON.stringify({
      alg: body.alg,
      credential_id: body.credentialId,
      public_key_pem: body.publicKeyPEM,
      rpid: body.rpId,
      origin: body.origin,
    }),
  );
  await res.text();
}

// disablePasskeyBegin requests the disable re-auth challenge (operator-authed). An empty
// body asks the server to either issue a challenge (a passkey is registered) or report
// 'done' (none registered — idempotent).
export async function disablePasskeyBegin(cfg: ControllerConfig): Promise<DisablePasskeyOutcome> {
  const res = await postJSON(cfg, 'passkey/disable', '{}');
  const j = (await res.json()) as passkeyChallengeJSON & { registered?: boolean };
  if (j.challenge) {
    return { kind: 'challenge', challenge: mapPasskeyChallenge(j) };
  }
  return { kind: 'done' };
}

// disablePasskeyFinish submits the re-auth assertion to remove the passkey (operator-authed).
export async function disablePasskeyFinish(cfg: ControllerConfig, assertion: SignedTrustList): Promise<void> {
  const res = await postJSON(cfg, 'passkey/disable', JSON.stringify({ passkey: assertion }));
  await res.text();
}

// passkeyLoginBegin issues a passwordless login challenge for a username (UNAUTHENTICATED).
// A returned challenge with credentialId === null means the username has no passkey (a
// decoy); the caller should surface "no passkey for this account".
export async function passkeyLoginBegin(cfg: ControllerConfig, username: string): Promise<PasskeyChallenge> {
  const res = await fetch(ctlURL(cfg, 'login/passkey/begin'), {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ username }),
    credentials: 'include',
  });
  if (!res.ok) {
    const b = await res.text();
    throw new Error(`${res.status} ${b}`);
  }
  return mapPasskeyChallenge((await res.json()) as passkeyChallengeJSON);
}

// passkeyLoginFinish completes a passwordless login: it submits the assertion and returns
// the minted session (UNAUTHENTICATED). A non-2xx (uniform 401 on any failure) throws.
export async function passkeyLoginFinish(
  cfg: ControllerConfig,
  username: string,
  assertion: SignedTrustList,
): Promise<LoginResult> {
  const res = await fetch(ctlURL(cfg, 'login/passkey/finish'), {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ username, passkey: assertion }),
    credentials: 'include',
  });
  if (!res.ok) {
    const b = await res.text();
    throw new Error(`${res.status} ${b}`);
  }
  const d = (await res.json()) as LoginResponseJSON;
  return {
    sessionToken: d.session_token,
    operator: d.operator,
    expiresAt: d.expires_at,
    csrfToken: d.csrf_token,
  };
}

// SessionInfo is the GET /session probe result: the operator identity, the session
// expiry (RFC3339), and the in-memory CSRF token recovered from the cookie. Used by the
// panel on mount to re-derive login state after a refresh without reading a token in JS.
export interface SessionInfo {
  operator: string;
  expiresAt: string;
  csrfToken: string;
}

interface SessionResponseJSON {
  operator: string;
  expires_at: string;
  csrf_token: string;
}

// getSession probes the current operator session via the httpOnly cookie (or Bearer).
// Returns null when not logged in (401/403); any other non-2xx throws. credentials:
// 'include' so the session cookie travels.
export async function getSession(cfg: ControllerConfig): Promise<SessionInfo | null> {
  const headers = new Headers();
  if (cfg.operatorToken) {
    headers.set('Authorization', `Bearer ${cfg.operatorToken}`);
  }
  const res = await fetch(ctlURL(cfg, 'session'), { method: 'GET', headers, credentials: 'include' });
  if (res.status === 401 || res.status === 403) {
    await res.text();
    return null;
  }
  if (!res.ok) {
    const b = await res.text();
    throw new Error(`${res.status} ${b}`);
  }
  const d = (await res.json()) as SessionResponseJSON;
  return { operator: d.operator, expiresAt: d.expires_at, csrfToken: d.csrf_token };
}

// logout revokes the current session (POST /logout, authed by the session bearer in
// cfg.operatorToken). Best-effort: the caller clears local session state regardless.
export async function logout(cfg: ControllerConfig): Promise<void> {
  const res = await request(cfg, 'logout', { method: 'POST' });
  await res.text();
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

// --- bootstrap settings (plan-5.2) ---

// ControllerSettings is the operator-editable, server-persisted bootstrap config:
// the public agent URL (where nodes curl the bootstrap / enroll), an optional GitHub
// proxy prefix (default off), and the agent-binary release base URL.
export interface ControllerSettings {
  publicAgentURL: string;
  githubProxy: string;
  agentReleaseBaseURL: string;
  // translucency is the panel appearance preference (P5), served server-side via
  // GET/POST /settings. It is NOT part of the agent bootstrap script.
  translucency: boolean;
  // agentPathPrefix is READ-ONLY, server-reported (YAOG_AGENT_PATH_PREFIX,
  // normalized '' or '/<seg>'): the prefix agent-facing URLs mount under. The panel
  // composes the bootstrap one-liner / enroll command from it — never from the
  // operator-prefix mirror, which belongs to the panel's own API base.
  agentPathPrefix: string;
}

// SettingsJSON mirrors settingsJSON in internal/api/handler_bootstrap.go.
interface SettingsJSON {
  public_agent_url: string;
  github_proxy: string;
  agent_release_base_url: string;
  translucency: boolean;
  agent_path_prefix?: string;
}

function mapSettings(d: SettingsJSON): ControllerSettings {
  return {
    publicAgentURL: d.public_agent_url,
    githubProxy: d.github_proxy,
    agentReleaseBaseURL: d.agent_release_base_url,
    translucency: d.translucency,
    agentPathPrefix: d.agent_path_prefix ?? '',
  };
}

// getSettings reads the current bootstrap settings (defaults applied server-side).
export async function getSettings(cfg: ControllerConfig): Promise<ControllerSettings> {
  const res = await request(cfg, 'settings', { method: 'GET' });
  return mapSettings((await res.json()) as SettingsJSON);
}

// postSettings saves the bootstrap settings and returns the stored values.
export async function postSettings(cfg: ControllerConfig, s: ControllerSettings): Promise<ControllerSettings> {
  const body = JSON.stringify({
    public_agent_url: s.publicAgentURL,
    github_proxy: s.githubProxy,
    agent_release_base_url: s.agentReleaseBaseURL,
    translucency: s.translucency,
  });
  const res = await postJSON(cfg, 'settings', body);
  return mapSettings((await res.json()) as SettingsJSON);
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
