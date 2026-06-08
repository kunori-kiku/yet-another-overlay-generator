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

1. `POST /api/v1/controller/login` with `{ "username", "password" }` (UNAUTHENTICATED;
   reachable before the operator has a session).
2. The controller verifies the password against the stored argon2id hash and, on
   success, mints a **session**: a 256-bit random bearer token returned **once** in
   `{ "session_token", "operator", "expires_at" }`. Only the token's hex SHA-256 is
   stored (never the plaintext), with an expiry (`DefaultSessionTTL`, 12h).
3. The panel presents the session token as `Authorization: Bearer <session_token>` on
   operator routes. `operatorAuth` accepts **either** a valid (unexpired) session **or**,
   when configured, the break-glass token (constant-time compared).
4. `POST /api/v1/controller/logout` (authenticated) deletes the presented session.

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
- **2FA (TOTP / passkey-login) is a follow-up slice.** A passkey login factor will be its
  own random-challenge WebAuthn assertion — NOT the content-bound keystone verifier.
