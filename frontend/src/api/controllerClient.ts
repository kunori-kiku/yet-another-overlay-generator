// HTTP client for the controller panel. Each function targets an operator-facing route
// exposed by internal/api/handler_controller.go (the operator namespace, kept separate
// from the agent namespace /api/v1/agent/):
//   <baseURL><pathPrefix>/api/v1/operator/<route>
// Auth is uniformly Authorization: Bearer <operatorToken>. The backend responds with
// snake_case JSON, which this layer maps at the boundary into the camelCase controller
// types (see ../types/controller).
//
// Error convention: any non-2xx throws a ControllerError, which preserves the backend's
// coded error envelope on .body ({ error: { code, message, params } }, or a non-JSON body
// wrapped as { error: "<text>" }). The store localizes it via tError at the catch site
// per the current language (it never surfaces the raw "<status> <JSON>" to the operator).

import type {
  ControllerNode,
  ControllerAuditEntry,
  StageResult,
} from '../types/controller';
import type { CompileResponse } from '../types/topology';
import { mapNodeConditions, type ConditionWire } from '../lib/nodeConditions';

// ControllerError is thrown for any non-2xx controller response. It preserves the parsed coded
// error envelope on .body so the store can localize it via tError; .status is the HTTP status and
// .message is an English "<status> <body>" fallback for logs / non-localized contexts.
export class ControllerError extends Error {
  readonly status: number;
  readonly body: unknown;
  constructor(status: number, body: unknown, message: string) {
    super(message);
    this.name = 'ControllerError';
    this.status = status;
    this.body = body;
  }
}

// controllerErrorFromText builds a ControllerError from an already-read response body: a JSON body
// becomes the coded envelope tError localizes; a non-JSON body is wrapped as { error: <text> } so
// tError still surfaces it. Used directly where the body was consumed for status branching (login).
function controllerErrorFromText(status: number, text: string): ControllerError {
  let body: unknown;
  try {
    body = text ? JSON.parse(text) : { error: '' };
  } catch {
    body = { error: text };
  }
  return new ControllerError(status, body, `${status} ${text}`);
}

// errorFromResponse drains a non-2xx Response and builds a ControllerError carrying the parsed
// body. The Response body is consumed exactly once.
async function errorFromResponse(res: Response): Promise<ControllerError> {
  return controllerErrorFromText(res.status, await res.text());
}

// Controller connection config: operator base URL, optional secret path prefix, operator
// bearer token. Note this is connection-layer config; panel preferences such as agentBaseURL
// stay in the store and take no part in request construction.
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
  // controllerVersion mirrors GET /session: the controller's own build version, echoed on every
  // login so the panel surfaces it + uses it as the one-click agent rollout target without waiting
  // for the next /session probe. A stamped release reports a real semver; an unstamped build reports
  // the literal "dev" (the controller normalizes an empty BuildVersion to "dev"); "" only when an
  // older controller predates the field. The panel treats "dev"/non-semver as "no version to match".
  controllerVersion: string;
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
  controller_version?: string;
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
  // rotate ACKNOWLEDGES that this pin REPLACES a different already-pinned credential (which
  // strands every node until re-provisioned + redeployed). The controller refuses a changed
  // credential without it (409 keystone_rotation_requires_ack). Ignored on a first/idempotent pin.
  rotate?: boolean;
}

// OperatorCredentialPinResult is the POST /operator-credential result: rotated true only when the
// pin REPLACED a different credential; unchanged true on an idempotent re-pin; redeployRequired
// true when (after a rotation) the served fleet is still signed under the old key.
export interface OperatorCredentialPinResult {
  ok: boolean;
  rotated: boolean;
  unchanged: boolean;
  redeployRequired: boolean;
}

// OperatorCredentialStatus is the SERVER-authoritative keystone status (GET /operator-credential):
// the panel reflects THIS, never a browser-local cache, so a cleared browser can never falsely
// read "Not enrolled". It carries only non-secret public identifiers. publicKeyPEM is the
// credential's PUBLIC PEM (audit-only, already baked into every node bundle): a cleared/fresh
// browser recovers it — with credentialId/alg/rpId — to re-prompt the authenticator WITHOUT
// re-pinning (plan-3 signing-handle auto-recovery). The private key never leaves the authenticator.
export interface OperatorCredentialStatus {
  pinned: boolean;
  alg: string;
  credentialId: string;
  rpId: string;
  origin: string;
  fingerprint: string;
  publicKeyPEM: string;
  redeployRequired: boolean;
}

// controllerErrorCode extracts the backend error CODE from a caught ControllerError's coded
// envelope ({ error: { code } }), or null for any other error / shape. Lets the store branch on a
// specific failure (e.g. keystone_rotation_requires_ack) without string-matching messages.
export function controllerErrorCode(err: unknown): string | null {
  if (!(err instanceof ControllerError)) return null;
  const inner = (err.body as { error?: unknown } | null | undefined)?.error;
  if (inner && typeof inner === 'object') {
    const code = (inner as { code?: unknown }).code;
    if (typeof code === 'string') return code;
  }
  return null;
}

// normalizePrefix normalizes the user-entered secret path prefix to "" or "/<seg>"
// (single leading slash, no trailing slash), matching the backend SetPathPrefix
// normalization rules.
function normalizePrefix(prefix: string): string {
  const p = prefix.trim().replace(/^\/+/, '').replace(/\/+$/, '');
  return p === '' ? '' : '/' + p;
}

// ctlURL builds the full URL for a controller route. The baseURL trailing slash is
// stripped to avoid a double slash where it joins the path prefix. baseURL MUST be an
// absolute http(s) URL: otherwise fetch would resolve it relative to the panel's own
// origin and send the operator bearer token to the wrong origin (credential leak). On an
// invalid URL it throws so the caller can record it in store.error.
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
  return `${base}${normalizePrefix(cfg.pathPrefix)}/api/v1/operator/${route}`;
}

// --- backend snake_case response shapes (used only inside this module, discarded after mapping) ---

interface NodeJSON {
  node_id: string;
  status: string;
  has_wg_public_key: boolean;
  desired_generation: number;
  applied_generation: number;
  last_checksum: string;
  last_health: string;
  agent_version?: string;
  last_seen: string;
  enrolled_at: string;
  rekey_requested: boolean;
  in_rollout?: boolean;
  conditions?: ConditionWire[];
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
  warning?: string;
}

// MintTokenResult is the operator-facing result of minting an enrollment token: the
// plaintext token (shown once) plus an optional non-blocking design-membership
// warning (plan-6: set when the node-id is absent from the stored design).
export interface MintTokenResult {
  token: string;
  warning: string;
}

interface RevokeResponseJSON {
  node_id: string;
  revoked: boolean;
}

interface RekeyAllResponseJSON {
  requested: number;
}

interface ClearRekeyResponseJSON {
  node_id: string;
  cleared: boolean;
}

// --- shared request helpers ---

// isStateChanging reports whether an HTTP method mutates state (used to decide whether the
// cookie path must carry the CSRF header).
function isStateChanging(method: string): boolean {
  const m = method.toUpperCase();
  return m !== 'GET' && m !== 'HEAD' && m !== 'OPTIONS';
}

// request issues a request with credentials (credentials:'include' so the httpOnly session
// cookie travels, keeping the operator logged in across a refresh); it attaches a Bearer when
// an operatorToken (session/break-glass) is held, otherwise relies on the cookie alone;
// state-changing requests on the cookie path also carry the X-CSRF-Token double-submit token.
// A non-2xx throws Error(`${status} ${body}`).
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
    throw await errorFromResponse(res);
  }
  return res;
}

// postJSON issues a JSON-body POST (automatically setting Content-Type and the Bearer).
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
    throw controllerErrorFromText(res.status, text);
  }
  const data = (await res.json()) as LoginResponseJSON;
  return {
    kind: 'success',
    result: {
      sessionToken: data.session_token,
      operator: data.operator,
      expiresAt: data.expires_at,
      csrfToken: data.csrf_token,
      controllerVersion: data.controller_version ?? '',
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
    throw await errorFromResponse(res);
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
    throw await errorFromResponse(res);
  }
  const d = (await res.json()) as LoginResponseJSON;
  return {
    sessionToken: d.session_token,
    operator: d.operator,
    expiresAt: d.expires_at,
    csrfToken: d.csrf_token,
    controllerVersion: d.controller_version ?? '',
  };
}

// SessionInfo is the GET /session probe result: the operator identity, the session
// expiry (RFC3339), and the in-memory CSRF token recovered from the cookie. Used by the
// panel on mount to re-derive login state after a refresh without reading a token in JS.
export interface SessionInfo {
  operator: string;
  expiresAt: string;
  csrfToken: string;
  // controllerVersion is the controller's own build version (plan-7/8): a real semver on a stamped
  // release, the literal "dev" on an unstamped build, or "" only when an older controller predates
  // the field. The panel surfaces it in the user menu and uses a real-semver value as the one-click
  // "update all agents" target + the refuse-newer hint ("dev"/non-semver = no version to match).
  controllerVersion: string;
}

interface SessionResponseJSON {
  operator: string;
  expires_at: string;
  csrf_token: string;
  controller_version?: string;
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
    throw await errorFromResponse(res);
  }
  const d = (await res.json()) as SessionResponseJSON;
  return {
    operator: d.operator,
    expiresAt: d.expires_at,
    csrfToken: d.csrf_token,
    controllerVersion: d.controller_version ?? '',
  };
}

// logout revokes the current session (POST /logout, authed by the session bearer in
// cfg.operatorToken). Best-effort: the caller clears local session state regardless.
export async function logout(cfg: ControllerConfig): Promise<void> {
  const res = await request(cfg, 'logout', { method: 'POST' });
  await res.text();
}

// --- snake_case → camelCase mapping ---

function mapNode(n: NodeJSON): ControllerNode {
  return {
    nodeId: n.node_id,
    status: n.status as ControllerNode['status'],
    hasWGPublicKey: n.has_wg_public_key,
    desiredGeneration: n.desired_generation,
    appliedGeneration: n.applied_generation,
    lastChecksum: n.last_checksum,
    lastHealth: n.last_health,
    agentVersion: n.agent_version ?? '',
    lastSeen: n.last_seen,
    enrolledAt: n.enrolled_at,
    rekeyRequested: n.rekey_requested,
    inRollout: n.in_rollout ?? false,
    conditions: mapNodeConditions(n.conditions),
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

// AgentPin is one integrity-pinned release asset: the asset filename and the SHA-256 the agent
// verifies the downloaded bytes against before exec. Mirrors renderer.Artifact (Go) and the map
// values of agent_bins / mimic_debs on the wire.
export interface AgentPin {
  asset: string;
  sha256: string;
}

// ControllerSettings is the operator-editable, server-persisted bootstrap config: the public
// agent URL (where nodes curl the bootstrap / enroll), an optional GitHub proxy prefix (default
// off), the agent-binary release base URL, the signed agent self-update rollout, and the mimic
// GitHub-.deb catalog. POST /settings is FULL-REPLACE — see postSettings.
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
  // Signed agent self-update rollout (controller-panel-rollout-ui). All NON-SECRET pins.
  // EMPTY targetAgentVersion ⇒ no self-update (the safety contract). agentBins maps
  // "linux-<arch>" to the pinned asset; canary/fleet-wide stage the canary-then-fleet rollout.
  targetAgentVersion: string;
  minAgentVersion: string;
  agentBins: Record<string, AgentPin>;
  agentCanaryNodeIds: string[];
  agentRolloutFleetWide: boolean;
  // Mimic GitHub-.deb catalog. mimicDebs maps "<codename>-<arch>" to the pinned .deb; empty
  // mimicReleaseBase ⇒ distro-only mimic (no GitHub fallback).
  mimicVersion: string;
  mimicReleaseBase: string;
  mimicDebs: Record<string, AgentPin>;
  // Fleet-wide mimic→UDP fallback policy a tcp link inherits ('' / 'udp' / 'none'). plan-4; UI in plan-6.
  mimicFallbackDefault: string;
}

// SettingsJSON mirrors settingsJSON in internal/api/handler_bootstrap.go. The rollout + mimic
// fields are omitempty on the wire (Go), hence optional here; mapSettings supplies safe defaults.
interface SettingsJSON {
  public_agent_url: string;
  github_proxy: string;
  agent_release_base_url: string;
  translucency: boolean;
  agent_path_prefix?: string;
  target_agent_version?: string;
  min_agent_version?: string;
  agent_bins?: Record<string, AgentPin>;
  agent_canary_node_ids?: string[];
  agent_rollout_fleet_wide?: boolean;
  mimic_version?: string;
  mimic_release_base?: string;
  mimic_debs?: Record<string, AgentPin>;
  mimic_fallback_default?: string;
}

export function mapSettings(d: SettingsJSON): ControllerSettings {
  return {
    publicAgentURL: d.public_agent_url,
    githubProxy: d.github_proxy,
    agentReleaseBaseURL: d.agent_release_base_url,
    translucency: d.translucency,
    agentPathPrefix: d.agent_path_prefix ?? '',
    targetAgentVersion: d.target_agent_version ?? '',
    minAgentVersion: d.min_agent_version ?? '',
    agentBins: d.agent_bins ?? {},
    agentCanaryNodeIds: d.agent_canary_node_ids ?? [],
    agentRolloutFleetWide: d.agent_rollout_fleet_wide ?? false,
    mimicVersion: d.mimic_version ?? '',
    mimicReleaseBase: d.mimic_release_base ?? '',
    mimicDebs: d.mimic_debs ?? {},
    mimicFallbackDefault: d.mimic_fallback_default ?? '',
  };
}

// emptyControllerSettings is the all-unset initial value for a controlled settings form before the
// server record loads: the rollout + mimic fields mirror mapSettings's omitempty defaults, while
// translucency intentionally seeds the server's default-on appearance (the real GET always carries a
// concrete translucency + a defaulted release base, so this is never produced from a live response).
// Shared so each settings form does not re-spell the full field set (and they stay in sync as fields grow).
export function emptyControllerSettings(): ControllerSettings {
  return {
    publicAgentURL: '',
    githubProxy: '',
    agentReleaseBaseURL: '',
    translucency: true,
    agentPathPrefix: '',
    targetAgentVersion: '',
    minAgentVersion: '',
    agentBins: {},
    agentCanaryNodeIds: [],
    agentRolloutFleetWide: false,
    mimicVersion: '',
    mimicReleaseBase: '',
    mimicDebs: {},
    mimicFallbackDefault: '',
  };
}

// toSettingsJSON maps the FULL ControllerSettings to its wire form. Every persisted field is
// included because POST /settings is FULL-REPLACE: the server rebuilds ControllerSettings purely
// from the body (handler_bootstrap.go), so any omitted field is persisted as its zero value — an
// omit-list literal here would silently WIPE the rollout/mimic config on an unrelated edit. The
// read-only agent_path_prefix is deliberately NOT sent (server-derived; POST ignores it).
export function toSettingsJSON(s: ControllerSettings): SettingsJSON {
  return {
    public_agent_url: s.publicAgentURL,
    github_proxy: s.githubProxy,
    agent_release_base_url: s.agentReleaseBaseURL,
    translucency: s.translucency,
    target_agent_version: s.targetAgentVersion,
    min_agent_version: s.minAgentVersion,
    agent_bins: s.agentBins,
    agent_canary_node_ids: s.agentCanaryNodeIds,
    agent_rollout_fleet_wide: s.agentRolloutFleetWide,
    mimic_version: s.mimicVersion,
    mimic_release_base: s.mimicReleaseBase,
    mimic_debs: s.mimicDebs,
    mimic_fallback_default: s.mimicFallbackDefault,
  };
}

// getSettings reads the current bootstrap settings (defaults applied server-side).
export async function getSettings(cfg: ControllerConfig): Promise<ControllerSettings> {
  const res = await request(cfg, 'settings', { method: 'GET' });
  return mapSettings((await res.json()) as SettingsJSON);
}

// postSettings saves the bootstrap settings and returns the stored values. It sends the FULL
// settings (toSettingsJSON) — POST is full-replace, so a caller editing one field must still
// round-trip every other field or it is wiped (see toSettingsJSON).
export async function postSettings(cfg: ControllerConfig, s: ControllerSettings): Promise<ControllerSettings> {
  const res = await postJSON(cfg, 'settings', JSON.stringify(toSettingsJSON(s)));
  return mapSettings((await res.json()) as SettingsJSON);
}

// AgentPinFetchRequest is the body of the assisted release-pin fetch (POST release-pins, plan-1):
// kind selects the asset grammar + default base; version optionally pins a "latest" base to a tag;
// base optionally overrides the saved base; assets may be empty for kind='agent' (certified arches
// derived server-side).
export interface AgentPinFetchRequest {
  kind: 'agent' | 'mimic';
  version?: string;
  base?: string;
  assets: { key: string; asset: string }[];
}

// AgentPinFetchResult is the resolved pins + resolution metadata. versionApplied is REQUIRED by
// the rollout-UI contract: when true, base is the TAGGED url the pins were computed against and
// the UI must persist it as the agent release base — the agent fetches the verbatim saved base
// with no latest→tag rewrite, so a tagged pin + a moving "latest" base is a fail-closed hash
// mismatch (see the release_pins.go Base doc + the outline decisions log).
export interface AgentPinFetchResult {
  pins: Record<string, AgentPin>;
  base: string;
  version: string;
  versionApplied: boolean;
  proxyApplied: boolean;
  resolved: Record<string, string>;
}

// fetchPins calls the operator release-pins endpoint to pre-fill artifact pins for REVIEW. The
// fetched sidecar is convenience-only transport; trust stays the signed artifacts.json the agent
// verifies against. A coded error surfaces as ControllerError for tError to localize.
export async function fetchPins(cfg: ControllerConfig, body: AgentPinFetchRequest): Promise<AgentPinFetchResult> {
  const res = await postJSON(cfg, 'release-pins', JSON.stringify(body));
  const d = (await res.json()) as {
    pins?: Record<string, AgentPin>;
    base: string;
    version: string;
    version_applied: boolean;
    proxy_applied: boolean;
    resolved?: Record<string, string>;
  };
  return {
    pins: d.pins ?? {},
    base: d.base,
    version: d.version,
    versionApplied: d.version_applied,
    proxyApplied: d.proxy_applied,
    resolved: d.resolved ?? {},
  };
}

// ReleaseAssetsRequest is the body of the assisted release-asset DISCOVERY fetch (POST
// release-assets, plan-4): base optionally overrides the saved mimic release base; version
// optionally pins a "latest" base to a tag. The kind is implicitly mimic (the only discover caller).
export interface ReleaseAssetsRequest {
  base?: string;
  version?: string;
}

// ReleaseAssetsResult is the discovered .deb asset names + resolution metadata. assets is the list
// of *.deb names the release publishes (debug sidecars excluded server-side); the operator picks
// from it. base/version/versionApplied/proxyApplied mirror the release-pins resolution metadata.
export interface ReleaseAssetsResult {
  assets: string[];
  base: string;
  version: string;
  versionApplied: boolean;
  proxyApplied: boolean;
}

// fetchReleaseAssets calls the operator release-assets endpoint to LIST a GitHub release's .deb
// asset names, so the mimic catalog can offer a pick-from checklist instead of hand-typed
// filenames. Discovery is a convenience only — the SHA-256 pin is still fetched (per-row Assist) and
// saved separately; nothing is trusted or persisted here. A coded error surfaces as ControllerError.
export async function fetchReleaseAssets(
  cfg: ControllerConfig,
  body: ReleaseAssetsRequest,
): Promise<ReleaseAssetsResult> {
  const res = await postJSON(cfg, 'release-assets', JSON.stringify(body));
  const d = (await res.json()) as {
    assets?: string[];
    base: string;
    version: string;
    version_applied: boolean;
    proxy_applied: boolean;
  };
  return {
    assets: d.assets ?? [],
    base: d.base,
    version: d.version,
    versionApplied: d.version_applied,
    proxyApplied: d.proxy_applied,
  };
}

// --- public API (each takes (cfg, ...)) ---

// getNodes lists the entire fleet registry (operator-only).
export async function getNodes(cfg: ControllerConfig): Promise<ControllerNode[]> {
  const res = await request(cfg, 'nodes', { method: 'GET' });
  const data = (await res.json()) as NodeJSON[] | null;
  return (data ?? []).map(mapNode);
}

// getAudit fetches the audit chain together with whether it is complete and verifiable
// (operator-only).
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

// getTopology retrieves the currently stored topology JSON (operator-only). It returns
// unknown: the stored bytes are a public-keys-only topology, and this layer imposes no
// structure (the caller interprets it as needed). A 404 (no topology stored yet on the
// server — before the first deploy) returns null so the caller keeps the local canvas; any
// other error throws as usual.
export async function getTopology(cfg: ControllerConfig): Promise<unknown | null> {
  try {
    const res = await request(cfg, 'topology', { method: 'GET' });
    return (await res.json()) as unknown;
  } catch (err) {
    // request() throws ControllerError on non-2xx; 404 = "no topology stored yet" (the normal
    // first-run shape), surfaced as null. Match on the typed status, not the message string.
    if (err instanceof ControllerError && err.status === 404) {
      return null;
    }
    throw err;
  }
}

// mintEnrollmentToken mints a one-time enrollment token for a node, returning the plaintext
// token (shown only this once).
export async function mintEnrollmentToken(
  cfg: ControllerConfig,
  nodeId: string,
  ttlSeconds: number
): Promise<MintTokenResult> {
  const res = await postJSON(
    cfg,
    'enrollment-token',
    JSON.stringify({ node_id: nodeId, ttl_seconds: ttlSeconds })
  );
  const data = (await res.json()) as EnrollmentTokenResponseJSON;
  return { token: data.token, warning: data.warning ?? '' };
}

// updateTopology uploads a new topology version (operator-only). topoJSON is the serialized
// model.Topology JSON string, submitted verbatim as the request body.
export async function updateTopology(
  cfg: ControllerConfig,
  topoJSON: string
): Promise<void> {
  await postJSON(cfg, 'update-topology', topoJSON);
}

// compilePreview is a read-only, server-authoritative compile preview (operator-only): it
// POSTs the current design, the server renders the enrolled subgraph (no staging, no
// persistence, no side effects), and returns the configs plus the IDs of skipped (not yet
// enrolled) nodes. Zero-knowledge — the rendered wg configs contain only placeholder private
// keys. The response is the air-gap CompileResponse shape plus skipped_unenrolled.
export async function compilePreview(
  cfg: ControllerConfig,
  topoJSON: string
): Promise<CompileResponse> {
  const res = await postJSON(cfg, 'compile-preview', topoJSON);
  return (await res.json()) as CompileResponse;
}

// stage compiles the enrolled subgraph and stages it into the next generation (operator-only).
export async function stage(cfg: ControllerConfig): Promise<StageResult> {
  const res = await postJSON(cfg, 'stage', '');
  const data = (await res.json()) as StageResponseJSON;
  return {
    staged: data.staged ?? [],
    skippedUnenrolled: data.skipped_unenrolled ?? [],
    generation: data.generation,
  };
}

// promote flips the staged bundle to current and bumps the generation (operator-only),
// waking the /poll waiters.
export async function promote(
  cfg: ControllerConfig
): Promise<{ generation: number }> {
  const res = await postJSON(cfg, 'promote', '');
  const data = (await res.json()) as GenerationResponseJSON;
  return { generation: data.generation };
}

// revoke evicts a node (operator-only); its bearer credential is invalidated immediately.
export async function revoke(cfg: ControllerConfig, nodeId: string): Promise<void> {
  const res = await postJSON(cfg, 'revoke', JSON.stringify({ node_id: nodeId }));
  // Consume the response body to free the connection; the revoked flag is always true on
  // success, so the caller needs no branch.
  await (res.json() as Promise<RevokeResponseJSON>);
}

// rekeyAll requests a WG key rotation for the whole fleet (operator-only, plan-4.6 ROUTINE
// tier): it marks every approved node RekeyRequested. This is the start of the zero-knowledge
// flow — the controller never touches private keys; each agent regenerates its own local key
// and registers the new public key via /rekey. It returns the number of nodes marked. Note:
// after marking, one more Deploy is required — only once the nodes re-register their new public
// keys does the next generation carry everyone's new public keys and let the fleet converge.
export async function rekeyAll(cfg: ControllerConfig): Promise<{ requested: number }> {
  const res = await postJSON(cfg, 'rekey-all', '');
  const data = (await res.json()) as RekeyAllResponseJSON;
  return { requested: data.requested };
}

// clearRekey clears a single node's pending rekey mark (operator-only) without evicting it —
// the node keeps its approval status and bearer credential (unlike revoke). It is used to
// release a stuck "Roll keys" straggler (an offline/dead node, or a mistakenly triggered
// fleet-wide rotation); otherwise the panel's rekeying gate keeps Deploy disabled. Idempotent:
// returns cleared:false when there is no pending rekey mark.
export async function clearRekey(cfg: ControllerConfig, nodeId: string): Promise<{ cleared: boolean }> {
  const res = await postJSON(cfg, 'clear-rekey', JSON.stringify({ node_id: nodeId }));
  const data = (await res.json()) as ClearRekeyResponseJSON;
  return { cleared: data.cleared };
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
  try {
    const res = await request(cfg, 'trustlist', { method: 'GET' });
    const data = (await res.json()) as TrustListResponseJSON;
    return { trustlistJson: data.trustlist_json, epoch: data.epoch };
  } catch (err) {
    // request() throws ControllerError on non-2xx; 404 = keystone OFF (no operator
    // credential pinned, or nothing staged) → null so deploy() promotes directly.
    // Match on the typed status, not the message. (Previously this used a raw fetch
    // WITHOUT credentials:'include', so a refreshed cookie-only operator session 401'd
    // and could not keystone-sign on Deploy on a keystone-ON tenant — F1.)
    if (err instanceof ControllerError && err.status === 404) {
      return null;
    }
    throw err;
  }
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
): Promise<OperatorCredentialPinResult> {
  const res = await postJSON(
    cfg,
    'operator-credential',
    JSON.stringify({
      alg: body.alg,
      credential_id: body.credentialId,
      public_key_pem: body.publicKeyPEM,
      rpid: body.rpId,
      origin: body.origin,
      rotate: body.rotate ?? false,
    }),
  );
  const d = (await res.json()) as {
    ok?: boolean;
    rotated?: boolean;
    unchanged?: boolean;
    redeploy_required?: boolean;
  };
  return {
    ok: d.ok ?? false,
    rotated: d.rotated ?? false,
    unchanged: d.unchanged ?? false,
    redeployRequired: d.redeploy_required ?? false,
  };
}

// getOperatorCredentialStatus reads the SERVER-authoritative keystone status (GET
// /operator-credential, operator-only). It is the source of truth for the panel's "enrolled"
// display — a browser-local cache must never decide it (a cleared browser would falsely read
// "Not enrolled" and invite a fleet-stranding re-pin).
export async function getOperatorCredentialStatus(
  cfg: ControllerConfig,
): Promise<OperatorCredentialStatus> {
  const res = await request(cfg, 'operator-credential', { method: 'GET' });
  const d = (await res.json()) as {
    pinned?: boolean;
    alg?: string;
    credential_id?: string;
    rpid?: string;
    origin?: string;
    fingerprint?: string;
    public_key_pem?: string;
    redeploy_required?: boolean;
  };
  return {
    pinned: d.pinned ?? false,
    alg: d.alg ?? '',
    credentialId: d.credential_id ?? '',
    rpId: d.rpid ?? '',
    origin: d.origin ?? '',
    fingerprint: d.fingerprint ?? '',
    publicKeyPEM: d.public_key_pem ?? '',
    redeployRequired: d.redeploy_required ?? false,
  };
}
