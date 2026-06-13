# Controller operator API

<!-- last-verified: 2026-06-13 -->

## Responsibility
Authenticates operators (password + TOTP/passkey sessions in httpOnly cookies, plus an optional break-glass bearer token) and serves the operator-port HTTP routes that drive the fleet lifecycle: topology, stage/promote, nodes, revoke, audit, settings, enrollment tokens, rekey-all, and keystone signing.

> **controller-server-authority-redesign (plans 1/2/6):** `POST /update-topology`
> enforces key custody — it rejects (400) any payload carrying a non-empty
> `wireguard_private_key` and stores the canonical re-marshaled bytes — and appends an
> `update-topology` audit entry; `promote` appends a `promote` entry (both gaps closed).
> Topology is retained as bounded history (`GET /topology?version=N`,
> `GET /topology/versions`, last 10). `POST /enrollment-token` returns a non-blocking
> `warning` when the node-id is absent from the stored design (warn-not-block). The
> operator/agent secret path prefixes are split (`YAOG_OPERATOR_PATH_PREFIX` /
> `YAOG_AGENT_PATH_PREFIX`).

## Files
- `internal/api/handler_controller.go:37-268` — `ControllerHandler` config (path prefix, panel-origin allowlist, secure cookie), `RegisterOperatorRoutes` route table (175-218), `SetPathPrefix`/`basePath` (223-235), `cors()` middleware (248-268)
- `internal/api/handler_controller.go:646-1018` — operator action handlers: update-topology, stage, promote, nodes, revoke, audit, topology, enrollment-token, rekey-all
- `internal/api/handler_controller.go:1084-1318` — keystone routes: operator-credential pin, GET trustlist, POST trustlist-signature (verification core: see specs/keystone-trustlist.md)
- `internal/api/handler_login.go:1-243` — POST /login (password leg + TOTP/passkey second-factor legs), shared session-mint tail `mintSessionResponse` (171-200), POST /logout (222-242)
- `internal/api/handler_passkey.go:1-407` — passkey register/disable/status, passwordless login begin/finish, shared `verifyLoginAssertion` (156-177)
- `internal/api/handler_totp.go:1-161` — TOTP enroll/confirm/disable/status for the logged-in operator; `currentOperator` resolver (41-53)
- `internal/api/cookie_session.go:1-176` — httpOnly `yaog_session` + readable `yaog_csrf` cookies, double-submit CSRF check (96-106), GET /session probe (142-167)
- `internal/api/auth_controller.go:141-201` — `operatorAuth` middleware (bearer-or-cookie, CSRF gate) and `resolveOperator` (session lookup, then constant-time break-glass compare)
- `internal/api/loginratelimit.go:1-145` — per-username + per-IP failed-login limiter with atomic check-and-reserve gate (73-116)
- `internal/api/handler_bootstrap.go:49-123` — GET/POST /settings (operator half; GET /bootstrap is agent-port, see specs/controller-agent-api.md)
- `internal/api/server.go:48-51` — `EnableController` mounts operator routes on the operator/panel mux (shared with the air-gap API)
- `cmd/server/main.go:29-63,119-175` — env wiring: `YAOG_OPERATOR_PATH_PREFIX` / `YAOG_AGENT_PATH_PREFIX` (split per audience), `YAOG_PANEL_ORIGIN`, `YAOG_SECURE_COOKIE`, break-glass token hashed before handler construction; startup log names both mounted base paths

## Inputs
- Browser panel requests (see specs/panel-auth.md for the login UX, specs/panel-deploy-fleet.md for the deploy/fleet consumers): JSON bodies (size-capped, unknown-field-rejecting `decodeJSON`, `internal/api/handler_controller.go:1336-1345`), credential via `Authorization: Bearer` header or the `yaog_session` cookie + `X-CSRF-Token` header.
- `controller.Store` (see specs/controller-store.md): `GetOperator`, `LookupSession`/`CreateSession`/`DeleteSession`, `PutTopology`, `ListNodes`, `AppendAudit`, `CreateEnrollmentToken`, `GetCurrentSignedTrustList`, etc.
- Controller core operations `controller.CompileAndStage` / `controller.PromoteStaged` (see specs/controller-stage-promote.md), invoked at `internal/api/handler_controller.go:688,718`.
- `trustlist.Verify` / `trustlist.VerifyAssertion` for keystone signatures and WebAuthn login assertions (see specs/keystone-trustlist.md), at `internal/api/handler_controller.go:1289` and `internal/api/handler_passkey.go:176`.

## Outputs
- JSON DTOs to the panel (snake_case wire structs, `internal/api/handler_controller.go:270-437`); the node view exposes no key material — only `has_wg_public_key` (`internal/api/handler_controller.go:330-344`).
- `Set-Cookie` headers: httpOnly session + readable CSRF cookie, written before the body on every successful login path (`internal/api/cookie_session.go:48-69`, `internal/api/handler_login.go:188`).
- Store mutations: sessions, operator records (TOTP/passkey fields), topology versions, settings, enrollment-token hashes, signed trust lists, and audit entries on every operator action.
- Generation bumps (`HandleRekeyAll` → `BumpGeneration`, `internal/api/handler_controller.go:1005`; promote, 718) that wake agent `/poll` waiters — consumed by specs/controller-agent-api.md and specs/agent.md.

## Decision points
- **Credential path** (`internal/api/auth_controller.go:154-169`): Bearer header first (CSRF-exempt — a cross-site form cannot set it); else the session cookie, where state-changing methods (`internal/api/cookie_session.go:110-117`) must carry a valid double-submit CSRF token or get 403. Missing credential = 401; present-but-unrecognized = 403.
- **Operator resolution** (`internal/api/auth_controller.go:191-201`): session lookup by token hash first, then constant-time compare against the break-glass token hash (empty hash disables break-glass entirely).
- **CORS mode** (`internal/api/handler_controller.go:248-268`): allowlisted Origin → reflect exact origin + `Allow-Credentials: true`; otherwise wildcard `*` without credentials (Bearer-only fallback). `Vary: Origin` always.
- **Second-factor precedence** (`internal/api/handler_login.go:113-158`): a registered passkey overrides TOTP; each is a two-leg 401-challenge flow. TOTP steps are atomically burned via `AdvanceTOTPStep` so a concurrent replay loses (139-157).
- **SameSite** (`internal/api/cookie_session.go:38-43`): `None` only when a cross-origin allowlist is set AND cookies are Secure; otherwise `Lax`.
- **Promote error mapping** (`internal/api/handler_controller.go:718-728`): empty staged set → 409; keystone gate (missing/invalid manifest signature) → 422; trustlist-signature substitution mismatch → 409 (1270-1273); no pinned credential → 412 (1243-1251).
- **Rate-limit gate** (`internal/api/loginratelimit.go:73-116`): attempts are counted at the gate (before argon2 work) closing the check-then-record TOCTOU; only the lockout *transition* is audited (`internal/api/handler_login.go:207-216`).

## Invariants
- Plaintext credentials are returned at most once and stored only as SHA-256 hashes — session tokens (`internal/api/handler_login.go:179-183`), enrollment tokens (`internal/api/handler_controller.go:929-934`); operator views never expose WG keys or token hashes (PRINCIPLES.md "Key custody").
- Handlers read tenant + operator identity only from the request context stamped by `operatorAuth` (`internal/api/handler_controller.go:1325-1332`); the tenant is pinned from `YAOG_TENANT_ID`, never request-supplied.
- `Access-Control-Allow-Origin: *` is never emitted together with `Allow-Credentials` (`internal/api/handler_controller.go:256-261`), and cookies/CSRF/credentialed CORS apply to operator routes only — agent routes stay Bearer-only (`internal/api/cookie_session.go:10-13`).

## Gotchas
- `operatorPrefix` (`YAOG_OPERATOR_PATH_PREFIX`) is drive-by-scanner obscurity, NOT a security boundary (`internal/api/handler_controller.go:76-87`); operator routes share the panel port's mux with the air-gap API and optional SPA (`internal/api/server.go:48-51`), while agent routes live on a separate port under their own independent `YAOG_AGENT_PATH_PREFIX`.
- The limiter's per-IP key is `r.RemoteAddr`, which collapses to one bucket behind a reverse proxy (`internal/api/loginratelimit.go:14-17,139-145`); a failed second-factor leg keeps the reserved slot counted — only a fully successful login refunds it (`internal/api/handler_login.go:110-115`).
- Logout with the break-glass token deletes no session yet still returns 204 and clears both cookies (`internal/api/handler_login.go:222-242`); TOTP/passkey management 403s under break-glass because that identity has no operator account (`internal/api/handler_totp.go:41-53`).

Deep docs: `docs/spec/controller/operator-auth.md` (login/session/2FA design), `docs/spec/controller/controller-api.md` (two-port bearer model), `docs/spec/controller/signing.md` (keystone) — all claims above re-verified against live code.
