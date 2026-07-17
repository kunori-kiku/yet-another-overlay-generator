# Controller operator API

<!-- last-verified: 2026-07-17 -->

## Responsibility

Own the controller's browser/operator HTTP boundary: mount the dedicated operator namespace, divide
pre-authentication login from authenticated control-plane routes, enforce the shared browser security
middleware, and translate handlers into stable HTTP responses
(`internal/api/routes_controller.go:277-365`, `internal/api/server.go:59-74`). Browser auth state stays
with [Panel auth](panel-auth.md), while the handlers delegate durable state to
[Controller store](controller-store.md), deployment transactions to
[Controller stage and promote](controller-stage-promote.md), history semantics to
[Controller telemetry](controller-telemetry.md), and signing semantics to
[Keystone and trust lists](keystone-trustlist.md)
(`frontend/src/stores/controller/auth.ts:85-212`, `internal/api/handler_deploy.go:43-58`,
`internal/api/telemetry_history.go:909-941`, `internal/api/handler_keystone.go:277-346`).

## Files

- `internal/api/routes_controller.go:277-365` — registers every pre-authenticated and protected
  operator endpoint; `internal/api/routes_controller.go:388-436` owns the namespace and CORS wrapper.
- `internal/api/auth_controller.go:163-226` — resolves session or break-glass credentials and injects
  the authenticated tenant/operator identity.
- `internal/api/cookie_session.go:28-120` — defines session/CSRF cookies, SameSite selection, and the
  double-submit check for state-changing cookie requests.
- `internal/api/adapter.go:32-84` and `internal/api/helpers_controller.go:14-56` — centralize method,
  identity, body-size, and strict-JSON request framing.
- `internal/api/handler_login.go:64-212`, `internal/api/handler_passkey.go:201-420`, and
  `internal/api/handler_totp.go:42-143` — implement password, passwordless/passkey, session, and TOTP
  account-authentication routes.
- `internal/api/handler_topology.go:23-131`, `internal/api/handler_deploy.go:22-208`,
  `internal/api/handler_enrollment.go:44-205`, and `internal/api/handler_settings.go:102-199` — adapt
  design, deployment, Fleet lifecycle, and settings operations to HTTP.
- `internal/api/handler.go:123-149` and `internal/apierr/apierr.go:1-16` — serialize registered coded
  failures through the shared `{error:{code,message,params}}` envelope.

## Inputs

Browser requests arrive under the optional operator prefix plus `/api/v1/operator/`; password and
passwordless-passkey login routes are pre-authenticated, while all remaining routes pass through the
session-or-break-glass middleware (`internal/api/routes_controller.go:293-321`,
`internal/api/routes_controller.go:388-395`). [Panel auth](panel-auth.md) supplies credentials and the
CSRF echo; authenticated handlers receive tenant and actor only from middleware-injected context
(`internal/api/auth_controller.go:171-200`, `internal/api/adapter.go:43-59`).

Design and deployment calls enter through topology Save, preview, stage, and promote; Fleet calls use
nodes, exact node history, manual bundle, enrollment/revocation, rekey, and audit routes
(`internal/api/routes_controller.go:322-345`). Settings and release-assistance calls use their secure
multi-method adapters, while keystone status/pin, manifest fetch, and signature submission use the
same protected namespace (`internal/api/routes_controller.go:346-364`).

## Outputs

The surface returns JSON DTOs for ordinary operations and permits raw stored topology or ZIP bundle
responses only through the authenticated raw adapter (`internal/api/adapter.go:32-84`,
`internal/api/routes_controller.go:331-342`). Successful password, passkey-factor, or passwordless
login converges on one session-minting tail that sets the httpOnly session cookie and readable CSRF
cookie before returning controller identity/version/capabilities
(`internal/api/handler_login.go:175-212`, `internal/api/cookie_session.go:48-71`).

Ordinary failures use the status, stable code, default message, and parameters registered by the leaf
`apierr` package; uncoded internal causes are bucketed before serialization. The two successful
first-factor continuations deliberately return specialized `401` ceremony bodies for TOTP or passkey
completion rather than the ordinary coded envelope (`internal/api/handler.go:129-149`,
`internal/apierr/apierr.go:25-28`, `internal/apierr/apierr.go:169-179`,
`internal/api/handler_login.go:33-39`, `internal/api/handler_passkey.go:44-60`).

## Decision points (if any)

- `/login` and `/login/passkey/{begin,finish}` are the only operator pre-authentication credential
  routes. Password login prefers a registered passkey factor over TOTP; either successful path and
  passwordless finish mint the same session (`internal/api/routes_controller.go:295-321`,
  `internal/api/handler_login.go:119-179`, `internal/api/handler_passkey.go:384-420`).
- Bearer authentication is checked first and is CSRF-exempt; absent Bearer, a session cookie is
  accepted only with a matching CSRF cookie/header on mutating methods. Resolution then prefers a
  live named session before constant-time comparison with the optional break-glass hash
  (`internal/api/auth_controller.go:171-226`).
- An allowlisted browser origin is reflected with credentials; every other origin receives the
  non-credentialed wildcard. Every operator response, including preflight and authentication
  failures, receives `Cache-Control: private, no-store`
  (`internal/api/routes_controller.go:231-240`, `internal/api/routes_controller.go:293-294`,
  `internal/api/routes_controller.go:406-436`).
- Domain work remains delegated: stage/promote behavior belongs to
  [Controller stage and promote](controller-stage-promote.md), history selection/rollup to
  [Controller telemetry](controller-telemetry.md), and keystone pin/signature verification to
  [Keystone and trust lists](keystone-trustlist.md) (`internal/api/handler_deploy.go:22-208`,
  `internal/api/telemetry_history.go:849-941`, `internal/api/handler_keystone.go:63-346`).

## Invariants

- Operator and agent routes occupy different muxes/listeners and namespaces; without controller mode,
  the operator mux exposes only health plus an optional SPA, never anonymous compile/export routes
  (`internal/api/server.go:44-74`, `internal/api/server.go:91-97`).
- Protected typed handlers cannot run without middleware-provided tenant/operator identity; method
  and identity checks occur in the shared adapter before dispatch
  (`internal/api/auth_controller.go:192-200`, `internal/api/adapter.go:43-59`).
- Credentialed CORS never uses a wildcard, cookie authentication applies CSRF to every mutating
  method, and dynamic operator API responses prohibit shared-cache storage
  (`internal/api/routes_controller.go:406-436`, `internal/api/cookie_session.go:97-120`,
  `internal/api/routes_controller.go:231-240`).

## Gotchas (optional)

- `YAOG_OPERATOR_PATH_PREFIX` hides the route from casual scanning but is not an authorization
  boundary; the middleware and separate listener are the boundary
  (`internal/api/routes_controller.go:49-68`, `internal/api/routes_controller.go:376-395`).
- Break-glass authentication has an operator identity but no account, so account-bound TOTP and login
  passkey management require a named session; keystone recovery operations remain available through
  their separately protected routes (`internal/api/handler_totp.go:42-55`,
  `internal/api/routes_controller.go:309-321`, `internal/api/routes_controller.go:359-364`).
- Both listeners are plain HTTP. Production must terminate TLS before password, cookie, or replayable
  Bearer credentials cross the network (`internal/api/server.go:61-71`,
  `internal/api/handler_login.go:64-74`).
