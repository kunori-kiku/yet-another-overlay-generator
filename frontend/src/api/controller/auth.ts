// Operator authentication client routes (plan-5.2): password login (+ optional TOTP / passkey
// second factor), session probe/logout, TOTP 2FA self-service, and login-passkey management.
// The login passkey is the phishing-resistant factor (and passwordless credential), distinct from
// the keystone signing credential; its assertion wire shape is SignedTrustList (same as keystone
// signing), which is why WebAuthnAlg / SignedTrustList are imported from ./keystone.

import {
  ctlURL,
  request,
  postJSON,
  errorFromResponse,
  controllerErrorFromText,
  type ControllerConfig,
} from './transport';
import type { WebAuthnAlg, SignedTrustList } from './keystone';

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
