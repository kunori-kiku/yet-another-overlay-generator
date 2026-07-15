# Operator authentication (password login + sessions)

Status: implemented (plan-5.2). Operator routes on the panel port are authenticated by
**per-operator password login** (the primary path) and, optionally, a **break-glass
token** (a recovery credential). This replaces the single shared operator token as the
day-to-day mechanism while keeping it as an opt-in fallback. OIDC/SSO remains a possible
future pluggable backend (not required for a self-hosted, low-operator-count deployment).

## Why password + optional 2FA, not OIDC

OIDC buys SSO and central account lifecycle across many users/services federated to an
IdP — value a self-hoster with one controller and a handful of operators does not need,
at the cost of a hard runtime dependency (a running IdP, client registration, discovery/
JWKS, clock skew). Per-operator password login is a strict upgrade on the prior single
shared token: per-operator identity, revocation, and (future) phishing-resistant 2FA,
with no federation dependency. See the keystone ([signing.md](signing.md)) for the actual
network trust anchor — operator auth gates the **panel**, not the membership signature.

## Bootstrap: `yaog-server create-operator`

Operator accounts are created out-of-band by the server binary:

```
yaog-server create-operator --state-dir <dir> --tenant <id> --username <name> [--force]
```

`--state-dir`/`--tenant` default to `$YAOG_CONTROLLER_STATE_DIR`/`$YAOG_TENANT_ID`. The
password is read without echo from an interactive terminal (confirmed twice), or from
`--password-file`, or from stdin when piped. It is hashed with **argon2id** and written
to the controller store; the plaintext is never echoed, stored, or logged. `--force`
resets an existing account's password. The command writes one file
(`operators/<username>.json`) and is safe to run while the server is running (the server
reads it on the next login).

## Login flow

1. `POST /api/v1/operator/login` with `{ "username", "password" }` (UNAUTHENTICATED;
   reachable before the operator has a session).
2. The controller verifies the password against the stored argon2id hash and, on
   success, mints a **session**: a 256-bit random bearer token returned **once** in
   `{ "session_token", "operator", "expires_at" }`. Only the token's hex SHA-256 is
   stored (never the plaintext), with an expiry (`DefaultSessionTTL`, 12h).
3. The panel presents the session token as `Authorization: Bearer <session_token>` on
   operator routes. `operatorAuth` accepts **either** a valid (unexpired) session **or**,
   when configured, the break-glass token (constant-time compared).
4. `POST /api/v1/operator/logout` (authenticated) deletes the presented session.

**Panel UX (controller-server-authority-redesign):** the login form is a **full-page
gate** (`components/auth/LoginPage.tsx`), not a section inside Settings — entering the
panel with controller mode persisted shows it before any chrome (the shell renders a
brief "checking session…" splash until the mount session-probe resolves, then either the
gate or the canvas — no flash). A valid break-glass token entered from the gate's
Recovery disclosure opens the panel without minting a session (it is not a login). On a
successful login or cookie-session restore the panel hydrates its canvas from the server
(`GET /topology` → load), overwriting the local cache — controller mode is
server-authoritative; the browser cache is a disposable mirror, with a one-time export
stash before the first overwriting hydration of a differing non-empty local design. See
specs/panel-auth.md and specs/panel-shell.md.

## Defenses

- **Password storage:** argon2id, `m=64 MiB, t=3, p=1`, 16-byte random salt, stored as a
  self-describing PHC string so parameters can be raised without invalidating old hashes.
  (At/above the OWASP floor of `m=19 MiB, t=2, p=1`.)
- **Rate limiting:** failed logins are throttled per **username** and per **source IP**
  (`maxLoginFailures=10` within a `15m` window → lockout for the rest of the window,
  `429` + `Retry-After`). The gate runs before any password work.
- **No username oracle:** unknown-user and wrong-password both return a uniform
  `401 invalid username or password`, and the unknown-user branch runs a dummy argon2
  verify so response timing does not reveal which.
- **Audit:** `login-success` and `login-lockout` are appended to the hash-chained audit
  log (individual non-lockout failures are not, to keep the log bounded under attack).
- **Sessions:** short-lived, revocable (logout), hash-stored, lazily deleted on expiry.

## Two-factor: TOTP (optional, login only)

An operator may enrol **TOTP** (RFC 6238, the standard authenticator-app code) as a second
factor on password login — implemented in stdlib (HMAC-SHA1 + dynamic truncation, no new
dependency; see `internal/controller/totp.go`).

- **Enrol:** `POST /totp/enroll` mints a secret + `otpauth://` URI (not yet active);
  `POST /totp/confirm` activates it only after the operator proves they can generate a code.
  `POST /totp/disable` requires a current code. `GET /totp/status` reports enrolment.
- **Panel UX:** the Deploy → Controller panel drives all of the above (a "Two-Factor
  (TOTP)" section: enable → show the base32 setup key + `otpauth://` link → confirm a code;
  disable with a code), and the sign-in form reveals a 6-digit code field when the backend
  answers `totp_required`. Enrolment is **dependency-free** — the panel shows the setup key
  for manual entry rather than rendering a QR, so the secret never leaves the panel for a
  third-party QR service.
- **At login:** when TOTP is enrolled, a correct password returns `401 {totp_required:true}`
  until a valid code is supplied; a wrong/replayed code is a counted failure (so a code
  brute-force via `/login` is rate-limited), a correct code mints the session. Codes accept
  ±1 step (clock drift) and are **replay-protected** (the last accepted step is recorded;
  a code at or before it is refused).
- **Honest limit:** TOTP's secret is **symmetric**, so it is stored at rest (unlike a
  passkey, where only a public key is stored). A store breach reveals TOTP secrets. TOTP is
  a convenience second factor; a passkey is strictly stronger.

### TOTP is NOT a signing mechanism

TOTP gates the **panel login** only. It can **never** be a keystone signing factor: it is
symmetric (the controller holds the secret, so a breached controller could forge codes —
the exact forgery the keystone prevents) and it produces a time-based code, not an
asymmetric content-bound signature a node can verify offline. For **off-host signing
without a hardware key**, use a **synced/software passkey** — a Bitwarden, iCloud Keychain,
or 1Password passkey needs no YubiKey and produces the WebAuthn signature the keystone
already accepts — or an off-host Ed25519 key (a keypair you hold on your own machine).

## Passkey login (optional, password+passkey 2FA AND passwordless)

An operator may register a **WebAuthn login passkey** as a second factor — the
phishing-resistant, asymmetric sibling of TOTP. It supports **both** login models (a
deployment/operator can use either):

- **Password + passkey (2FA):** at `POST /login`, a correct password for an operator with
  a passkey returns `401 {passkey_required:true, challenge, allow_credentials, rpid}`; the
  panel runs `navigator.credentials.get` and resubmits `POST /login` with the assertion in
  a `passkey` field. Passkey takes **precedence over TOTP** when both are registered (it is
  the stronger factor); TOTP remains the fallback for operators without a passkey.
- **Passwordless:** `POST /login/passkey/begin {username}` issues a challenge +
  `allow_credentials`; `POST /login/passkey/finish {username, passkey}` verifies the
  assertion and mints a session with **no password**. Rate-limited per username+IP by the
  same limiter as password login (a locked account is locked across both paths).

The login challenge is a **server-issued, single-use, short-TTL (5 min) random nonce**,
scoped to the operator and stored hash-only (`internal/controller/login_challenge.go`);
`ConsumeAssertionChallenge` burns it atomically **by deleting it**, so a captured assertion
cannot be replayed (the record is gone) and two concurrent logins cannot both consume one;
completed and expired challenges leave no residue. The unauthenticated `begin` is
**rate-limited** per username+IP by the same limiter as password login (so an attacker
cannot spam it to grow the store, and a locked account is locked across every login path). Registration (`POST /passkey/register`,
operator-authed) stores only the **public** half (`Operator.LoginCredential`, distinct from
the keystone `OperatorCredential`); disable (`POST /passkey/disable`) is two-phase and
requires a **fresh assertion** so a hijacked session cannot strip the factor without the
authenticator. The assertion is verified by the **same** `internal/trustlist` core as the
keystone (`VerifyAssertion`); ONLY the challenge differs — a random nonce here vs the
content-bound manifest hash there. A login passkey must be a WebAuthn credential
(`webauthn-es256` / `webauthn-eddsa`); a raw Ed25519 (no authenticator assertion) is
rejected at registration.

This is the **same primitive** as the keystone — proving control of a hardware/synced
passkey — pointed at a *random* challenge to authenticate the panel session rather than the
*content* challenge that authorizes a membership change. A synced passkey (Bitwarden,
iCloud Keychain, 1Password) needs no hardware key. **Honest limit:** passwordless `begin`
reveals whether a username has a passkey (the `allow_credentials` is empty for none) — a
low-value signal, and both `begin` and `finish` are rate-limited.

### Enrollment-scoped User Verification (UV)

Status: included in the withdrawn `v2.0.0-rc.7` tag at `c3c5c25`, but never published as a GitHub
Release because that identity's arm64 controller image was malformed. The same compatibility-preserving
implementation is published in `v2.0.0-rc.8`. This supersedes the blanket
assertion gate from post-refactor-debt-paydown plan-6 / PR #282 before that gate reached a release.

**Compatibility is the reason for the enrollment boundary.** Existing operators enrolled their
credentials under an acceptance contract that did not require the server-observed UV bit on every
assertion, and existing fleets may currently serve a valid trust-list signature whose ceremony carried
User Presence but not UV. Enabling the blanket gate in an upgrade could therefore lock an operator out
and, because the same verifier runs on nodes, stop upgraded agents from accepting the current config.
New enrollment can require proof prospectively without changing the rules underneath those users.

Both browser-credential enrollment paths — the per-operator **login passkey** and the tenant-level
**keystone passkey** — now prove UV to the controller before the public credential is persisted:

1. The authenticated panel calls `POST /webauthn/enrollment/begin` with purpose `login` or
   `keystone`. The controller creates a 32-byte, ten-minute, single-use challenge stored only as a
   hash and scoped to both the authenticated operator and that purpose.
2. The panel uses the server nonce for `navigator.credentials.create()` with
   `userVerification:"required"`, extracts the candidate credential ID and public key, and immediately
   asks that exact candidate to answer the same nonce with `navigator.credentials.get()`. The panel
   does not transmit registration attestation to the controller, so this signed assertion is the server-verifiable result
   of the first-party enrollment ceremony rather than client-reported capability metadata.
3. The relevant persistence endpoint verifies the assertion against the candidate public key,
   requires the exact candidate credential ID and RP/origin binding, then atomically consumes the nonce.
   It rejects the enrollment unless the authenticator data for **that ceremony** has both User Presence
   and the User-Verified bit (`0x04`). A proof for one purpose or operator cannot be replayed into the
   other path. Raw Ed25519 CLI keystones are not WebAuthn credentials and retain their existing path.

`VerifyUserVerifiedAssertion` is intentionally separate from the shared `VerifyAssertion` core.
Ordinary login, 2FA, disable re-authentication, keystone manifest signing, and node-side
`VerifyMembership` require the cryptographic assertion and User Presence but do not impose the
enrollment-only UV check. The first-party browser uses `userVerification:"preferred"` for those later
ceremonies so UV-capable authenticators can still perform it without excluding an existing non-UV
credential. UV is a result bit for one ceremony, not an immutable credential property, so a one-time
enrollment proof cannot honestly guarantee that every future assertion from a custom client performed UV.

The panel therefore warns on both enrollment surfaces: if a later assertion occurs without UV, it is
possession-only, and whoever holds the authenticator — or a usable synced/duplicated copy — can use the
credential. Backup eligibility/state (`BE`/`BS`, which describes whether a credential can be backed up
or is currently backed up) is independent of UV: a synced passkey can perform UV, while a non-UV
assertion does not by itself prove that the credential is copyable.

Upgrading to rc.8 requires no fleet migration or mandatory trust-list re-sign. Existing credentials
and manifest signatures remain valid under the generic verifier, so deploying this change cannot lock
out an existing operator or brick node config fetch merely because a historical assertion lacked UV.

## Transport (hard requirement)

`/login` carries a plaintext password. The controller speaks plain HTTP (TLS is delegated
to a reverse proxy, plan-4.5), so a deployment **MUST** front the controller with a
TLS-terminating proxy (nginx/caddy). A sniffed password is worse than a sniffed scoped
token. This is a requirement, not advisory.

## Break-glass token (`YAOG_CONTROLLER_OPERATOR_TOKEN`)

Now **optional**. When set, its hash authenticates operator routes alongside sessions —
a recovery path if all operator passwords are lost (and back-compat for the released
preview's panel, which authenticates with this token). When unset, only operator-account
sessions authenticate; the server logs a startup warning if neither a break-glass token
nor any operator account exists. If set, it is a standing admin secret — store it like
one, or leave it unset and rely on `create-operator` on the host for recovery.

## Session cookies & cross-origin (`YAOG_PANEL_ORIGIN`, `YAOG_SECURE_COOKIE`)

A successful login also sets an **httpOnly `yaog_session` cookie** (alongside the JSON
`session_token`, kept for the Bearer fallback) so an operator stays logged in across a
page refresh **without the panel persisting any token in web storage**. The panel
re-derives login state from `GET …/session` (the cookie travels automatically). JS never
reads the session — it lives in an httpOnly cookie.

- **CSRF (double-submit).** Login also sets a readable `yaog_csrf` cookie and returns
  `csrf_token`. On the cookie path, every **state-changing** request must echo that token
  in the `X-CSRF-Token` header; the server constant-time compares it to the cookie. The
  **Bearer** path is exempt (it is not CSRF-vulnerable). Safe methods (GET) are exempt.
- **`YAOG_PANEL_ORIGIN`** — comma-separated allowlist of browser origins permitted to make
  **credentialed** (cookie) cross-origin requests. For a matching `Origin`, CORS reflects
  that exact origin + `Access-Control-Allow-Credentials: true` + `Vary: Origin` (a wildcard
  `*` is **never** sent with credentials). Empty (default) ⇒ **same-origin only** for the
  cookie path; the Bearer path still works via the permissive non-credentialed `*`. A
  same-origin Docker deployment (panel + API on one origin) needs **no** allowlist. A
  cross-origin panel **must** set this and be served over **HTTPS** (`SameSite=None`
  requires `Secure`).
- **`YAOG_SECURE_COOKIE`** — `Secure` attribute on the cookies. Default **true**; set
  `false`/`0`/`no` ONLY for local non-TLS development.
- **Agent routes are untouched** — machine-to-machine routes stay Bearer-only with no
  cookies, no CSRF, and no credentialed CORS.

## Honest limits

- **Rate-limit state is in-process** and resets on restart (ephemeral by design); a
  restart loop could reset the counter — pair with the reverse proxy's own limiting for
  defense in depth.
- **Per-IP limiting collapses behind a proxy** (the source IP is the proxy's); forward
  the real client IP and/or rate-limit at the proxy. Per-username limiting is unaffected.
- **No online password reset / self-service account management** in v1 — use
  `create-operator --force` on the host. (A panel-driven account UI is a later slice.)
- **No password-complexity rules** beyond an 8-character floor — length/passphrases are
  encouraged; argon2id makes each guess expensive.
- **Usernames are case-sensitive**, and the FileStore assumes a case-sensitive
  filesystem (Linux — the controller's only supported host). On a case-folding
  filesystem (macOS/Windows) `Admin` and `admin` would map to the same file; that is not
  a supported deployment.
- **TOTP 2FA is shipped** (backend + panel UI). **Passkey login backend is shipped**
  (password+passkey 2FA AND passwordless, both over a random-challenge WebAuthn assertion —
  NOT the content-bound keystone verifier); its panel UI is the remaining slice.
