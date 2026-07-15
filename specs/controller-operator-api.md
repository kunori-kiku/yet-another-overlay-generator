# Controller operator API

<!-- last-verified: 2026-07-15 -->
<!-- 2026-06-15 (extensible-i18n closeout): error responses now coded via the internal/apierr envelope {error:{code,message,params}} — English-default message + panel-localized by error.<code>; no endpoint/flow change. -->
<!-- 2026-06-16 (controller-panel-rollout-ui): added the operator POST /release-pins endpoint (assisted .sha256 sidecar fetch through the gh-proxy, SSRF-guarded, agent + mimic) and an in_rollout bool on the nodes view (server-computed AgentRolloutNodeIDs membership). The agent self-update + mimic pins are now ALSO edited via the panel (specs/panel-deploy-fleet.md), but still through the same strictly-validated POST /settings. New apierr codes agent_release_*. -->

## Responsibility
Authenticate operators through password sessions with optional TOTP or login-passkey factors, plus an optional break-glass bearer token, and serve the operator-port HTTP routes that drive controller state: topology/version history, compile/stage/promote, fleet state, enrollment/revocation/rekey, audit, settings and release assistance, login-factor management, and keystone enrollment/signing.

The operator surface is distinct from both the agent port and the anonymous surface. `EnableController` mounts operator and agent routes on separate muxes; without it the panel mux exposes only `GET /api/health` (and an optional SPA). The former anonymous compute routes were removed, so no anonymous HTTP path reaches the compile pipeline (`internal/api/server.go:44-79,90-96`).

## Files
- `internal/api/routes_controller.go:15-180,263-422` — `ControllerHandler` configuration, complete operator route table, independent operator/agent path prefixes, and credentialed-CORS behavior.
- `internal/api/adapter.go:32-85` + `internal/api/helpers_controller.go:14-36` — typed `op`/`opRaw` adapters, structural identity check, method gate, and size-capped/unknown-field-rejecting JSON decoding.
- `internal/api/handler_login.go:61-251`, `cookie_session.go:37-177`, `auth_controller.go:152-212` — password/TOTP/passkey login tail, cookie/CSRF session handling, bearer-or-cookie operator middleware, and break-glass resolution.
- `internal/api/handler_passkey.go:100-400` — login-passkey status/register/disable and passwordless begin/finish. Registration and disable use field-scoped login-credential compare-and-set (`handler_passkey.go:193-301`); ordinary assertions use the shared UP/signature verifier (`handler_passkey.go:145-178`).
- `internal/api/handler_webauthn_enrollment.go:22-122` — authenticated `POST /webauthn/enrollment/begin` and the shared enrollment-proof verifier: ten-minute, purpose+actor-scoped, replace-bounded challenge; exact-candidate UP+UV assertion; verify before atomic consume.
- `internal/api/handler_keystone.go:63-270,290-359` — server-authoritative, recovery-aware keystone
  status; first pin/idempotent pin/acknowledged rotation; staged-manifest read; and atomic signature
  installation. `internal/controller/keystone_transition.go` supplies the durable credential-CAS/audit
  protocol used by the status and mutation paths.
- `internal/api/handler_topology.go:30-137`, `handler_deploy.go:22-177`, `handler_enrollment.go:44-205`, `handler_rekey.go:26-100`, `handler_audit.go:17-36` — domain-split topology, deploy, fleet-enrollment, rekey, and audit actions.
- `internal/api/handler_settings.go:102-199`, `release_pins.go:272-432`, `release_assets.go:142-193` — persisted settings plus the two convenience-only release-assistance endpoints. Release pins are not a trust anchor.
- `internal/api/wire_controller.go:80-330` — explicit snake_case controller DTOs, including node views, enrollment-token warnings, keystone status/pin bodies, and trust-list bodies.
- `internal/controller/store.go:355-376,613-633,666-710` + `storecore.go:655-718,823-856,982-1005` — assertion-challenge lifecycle, keystone CAS, and field-scoped login-credential CAS shared by both store backends.
- `cmd/server/main.go:26-87,109-144,149-256` — environment wiring for the tenant, split path prefixes, trusted proxies, panel origins, secure-cookie posture, build version, store, and both controller listeners.

## Inputs
- Browser requests under `OperatorBasePath()` (`/api/v1/operator/` plus the optional prefix), with JSON bodies capped and decoded strictly (`internal/api/routes_controller.go:373-380`; `internal/api/helpers_controller.go:26-36`).
- Operator credentials: `Authorization: Bearer` takes precedence; otherwise the httpOnly `yaog_session` cookie is accepted and state-changing cookie requests must echo the readable `yaog_csrf` cookie in `X-CSRF-Token` (`internal/api/auth_controller.go:160-188`; `internal/api/cookie_session.go:86-118`).
- Tenant and actor identities stamped into the request context by `operatorAuth`, then checked structurally by `op`/`opRaw` before a typed handler runs (`internal/api/auth_controller.go:181-188`; `internal/api/adapter.go:43-84`).
- `controller.Store`, including topology/fleet/session/audit operations, assertion challenges, keystone state, and the two compare-and-set credential primitives (`internal/controller/store.go:613-710`).
- Controller operations `CompileAndStage`, `CompileSubgraph`, `PromoteStaged`, and `InstallTrustListSignature`, invoked from the deploy/keystone handlers (`internal/api/handler_deploy.go:22-165`; `internal/api/handler_keystone.go:309-346`).
- `trustlist.VerifyAssertion` for existing login assertions and `trustlist.VerifyUserVerifiedAssertion` only at the new-browser-credential enrollment boundary (`internal/api/handler_passkey.go:145-178`; `internal/api/handler_webauthn_enrollment.go:77-120`).

## Outputs
- Explicit JSON DTOs. The fleet list exposes `has_wg_public_key`, not the key bytes or API-token hash, and exposes server-computed `in_rollout` rather than asking the panel to re-derive rollout membership (`internal/api/wire_controller.go:141-179`; `internal/api/handler_enrollment.go:44-84`).
- A plaintext session token and CSRF token returned once at successful login, alongside `Set-Cookie` headers written before the response; only the session-token hash is stored (`internal/api/handler_login.go:175-208`; `internal/api/cookie_session.go:47-70`).
- A plaintext node-enrollment token returned once, optionally with a non-blocking design-membership warning; only its hash is persisted (`internal/api/wire_controller.go:222-239`; `internal/api/handler_enrollment.go:140-201`).
- WebAuthn enrollment nonces returned once while only their SHA-256 hashes are stored. Repeated begins replace the prior live nonce for the same actor+purpose, and creation also purges expired challenge records (`internal/controller/login_challenge.go:24-52`; `internal/controller/storecore.go:655-689`).
- Scoped store mutations and audit records. Browser credential writes use CAS: keystone pin/rotation
  compares the exact prior credential and durably couples a committed transition to one identified
  audit event, while login-passkey register/disable changes only `LoginCredential` plus `UpdatedAt`,
  preserving concurrent password/TOTP changes (`internal/api/handler_keystone.go`;
  `internal/controller/keystone_transition.go`; `internal/api/handler_passkey.go:240-301`).
- Promote/rekey generation changes that wake agent pollers (`internal/api/handler_deploy.go:148-177`; `internal/api/handler_rekey.go:63-100`).

## Decision points
- **Credential path:** Bearer authentication is tried first and is CSRF-exempt; cookie authentication is the fallback and requires double-submit CSRF on state-changing methods. Missing credentials return 401; present but unresolved credentials return 403 (`internal/api/auth_controller.go:152-189`).
- **Operator resolution:** a stored, unexpired session is tried before constant-time comparison with the optional break-glass token hash; an empty configured hash disables break-glass (`internal/api/auth_controller.go:192-212`).
- **CORS/cookies:** an allowlisted browser origin is reflected with `Allow-Credentials: true`; other origins get the non-credentialed `*` fallback. `SameSite=None` is used only with a configured cross-origin allowlist and secure cookies; otherwise cookies are `Lax` (`internal/api/routes_controller.go:391-421`; `internal/api/cookie_session.go:37-45`).
- **Second-factor precedence:** after password verification, a registered login passkey takes precedence over TOTP. Accepted TOTP steps advance atomically so concurrent reuse loses (`internal/api/handler_login.go:109-167`; `internal/controller/store.go:720-727`).
- **Rate limiting:** username and resolved-client-IP slots are reserved atomically before slow password work; a successful login refunds them and only the lockout transition is audited. Forwarding headers are honored only when the direct peer is in `YAOG_TRUSTED_PROXIES` (`internal/api/loginratelimit.go:83-157,159-206`; `internal/api/handler_login.go:211-225`).
- **Promote:** an empty staged set maps to 409. With keystone on, a missing, unsigned, corrupt, or non-verifying staged manifest is a 422 precondition failure; successful promote is audited best-effort after the generation flips (`internal/api/handler_deploy.go:148-177`; `internal/controller/compile_promote.go:12-70`).
- **Browser-credential enrollment:** `login` and `keystone` challenges are not interchangeable. New login credentials and first/replacement/rotated WebAuthn keystones must prove an assertion from the exact candidate over the authenticated actor's server nonce with UP+UV. A raw Ed25519 keystone has no browser ceremony. An exact idempotent WebAuthn re-pin preserves the compatibility path: it performs a compare-only CAS and needs neither a new proof nor migration of grandfathered optional fields (`internal/api/handler_webauthn_enrollment.go:39-120`; `internal/api/handler_keystone.go:192-253`).

## Invariants
- Plaintext session and enrollment tokens are returned at most once and stored only as SHA-256 hashes (`internal/api/handler_login.go:175-208`; `internal/api/handler_enrollment.go:184-201`).
- Tenant and operator identity come from authenticated context, never from an operator request body; typed handlers cannot run until the adapter's identity check succeeds (`internal/api/auth_controller.go:186-188`; `internal/api/adapter.go:43-84`).
- `Access-Control-Allow-Origin: *` is never paired with `Allow-Credentials`, and cookies/CSRF apply only to operator routes; agent routes remain bearer-only (`internal/api/routes_controller.go:391-421`; `internal/api/cookie_session.go:3-13`).
- Credential persistence is race-detecting, not last-writer-wins. A stale keystone transition returns `ErrOperatorCredentialChanged`; a stale login-passkey mutation returns `ErrLoginCredentialChanged` without changing current state (`internal/controller/store.go:57-65`; `internal/controller/storecore.go:823-856,982-1005`).
- A keystone status read reconciles any pending credential-CAS/audit marker before returning. It can
  therefore heal a POST whose credential write committed before an audit or cleanup error, while a
  marker whose target never committed is discarded without producing an audit. An unrelated current
  credential is a storage conflict and the marker is retained for diagnosis (`internal/controller/keystone_transition.go`).

## Gotchas
- `YAOG_OPERATOR_PATH_PREFIX` is scanner obscurity, not an authentication boundary. The actual separation is operator middleware plus the distinct operator and agent muxes/ports (`internal/api/routes_controller.go:49-68,263-279`; `internal/api/server.go:16-28,59-79`).
- A break-glass token resolves an operator identity but has no operator account. Login-passkey enrollment begin/status/manage therefore reject it; logout still clears cookies and returns 204 while deleting no stored break-glass credential (`internal/api/handler_webauthn_enrollment.go:46-63`; `internal/api/handler_passkey.go:183-203`; `internal/api/handler_login.go:227-251`).
- Enrollment is the only server-side UV requirement. UV describes one ceremony, not an immutable capability of a credential. Ordinary login, keystone signing, controller promote verification, and node membership verification continue accepting a valid UP+signature assertion, preserving existing users and deployed fleets (`internal/trustlist/webauthn.go:23-25,43-95,118-203`).
- The first-party UI prefers UV for later assertions, while the server remains compatibility-tolerant
  of a valid UP-only result. The warning explains that a non-UV credential may be duplicable and that
  any usable duplicate is sufficient for later possession-based use. Backup Eligibility/Backup State
  are separate from UV, and because YAOG requests `attestation: "none"`, enrollment does not prove
  hardware provenance, non-exportability, or that a custom authenticated client supplied a
  hardware-backed key (`frontend/src/lib/webauthn.ts:241-268,303-334,391-465`;
  `frontend/src/i18n/messages/en.ts:687`).

Deep docs: `docs/spec/controller/operator-auth.md` (login/session/2FA), `docs/spec/controller/controller-api.md` (two-port controller model), and `docs/spec/controller/signing.md` (keystone workflow).
