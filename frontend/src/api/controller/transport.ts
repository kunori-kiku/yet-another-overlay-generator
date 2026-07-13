// Shared transport for the controller-panel HTTP client. This is the fetch/credentials/CSRF/
// error-decode plumbing every per-domain module (auth/fleet/deploy/keystone/settings/release/
// telemetry) builds on. Each operator-facing route is
//   <baseURL><pathPrefix>/api/v1/operator/<route>
// Auth is uniformly Authorization: Bearer <operatorToken>. The backend responds with snake_case
// JSON, which the domain modules map at the boundary into the camelCase controller types.
//
// Error convention: any non-2xx throws a ControllerError, which preserves the backend's coded
// error envelope on .body ({ error: { code, message, params } }, or a non-JSON body wrapped as
// { error: "<text>" }). The store localizes it via tError at the catch site per the current
// language (it never surfaces the raw "<status> <JSON>" to the operator).

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
export function controllerErrorFromText(status: number, text: string): ControllerError {
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
export async function errorFromResponse(res: Response): Promise<ControllerError> {
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
export async function request(
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
export function postJSON(
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
