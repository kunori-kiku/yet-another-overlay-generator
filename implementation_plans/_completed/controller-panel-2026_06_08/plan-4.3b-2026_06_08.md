# Plan 4.3b — Phase 2c-b: controller HTTP surface + TLS 1.3 + mTLS auth chokepoint

Parent: [plan-4.3-2026_06_08.md](plan-4.3-2026_06_08.md) · Prereq: 4.1/4.2/4.3a merged. Second of the
three 4.3 PRs. Adds the networked controller service that fronts the Store + enrollment + compile.

## Goal

Expose the controller over HTTP with TLS 1.3 + per-node mTLS, on an **env-gated controller mode** of
`cmd/server` — beside the untouched air-gap endpoints. One **auth chokepoint** derives `tenant:node`
from the verified client-cert CN; the agent-facing routes are mutually authenticated, `/enroll` is the
only route reachable without a client cert (gated by the single-use token + PoP from 4.2).

## Adopted design (from the refine-investigation)

- **Binary:** extend `cmd/server`; controller mode is on when controller config is present (a
  `YAOG_CONTROLLER_*` env, e.g. a state dir + tenant id). Air-gap `/api/compile|export|deploy-script`
  untouched. Controller routes under `/api/v1/controller/`.
- **TLS:** `http.Server.TLSConfig{MinVersion: tls.VersionTLS13, ClientCAs: devCA pool, ClientAuth:
  tls.VerifyClientCertIfGiven}`. `VerifyClientCertIfGiven` lets `/enroll` be reached certless while
  every other route requires a cert that the middleware verifies chains to the dev CA.
- **Auth chokepoint** (`auth_controller.go`): one middleware. For agent routes it requires a verified
  client cert, parses CN `"<tenant>:<node>"`, and puts `tenant`+`node` in the request context;
  `tenant` is also pinned to the configured `YAOG_TENANT_ID` (single-tenant v1) — a cert for a
  different tenant is rejected. A node may only act as ITSELF: `/config`, `/report`, `/poll` use the
  cert's node, never a URL/body field. `/enroll` skips the cert requirement. Operator routes require a
  cert whose CN is the configured operator identity (e.g. `"<tenant>:operator"`); OIDC is Plan 5.
- **Routes** (`handler_controller.go`, all JSON):
  - `POST /api/v1/controller/enroll` (no mTLS) → `enrollment.Enroll(store, ca, tenant, req, now)`;
    returns `{client_cert_pem, ca_cert_pem, fingerprint}`.
  - `GET  /api/v1/controller/config` (mTLS) → the **caller's** current `SignedBundle` (node from the
    cert); 404 before first promote; `TouchLastSeen`.
  - `GET  /api/v1/controller/poll?after=<gen>` (mTLS) → long-poll `Store.WaitForGeneration` with a
    ~55s server deadline (ctx from the request + a timer); returns `{generation}` or 204 on timeout.
  - `POST /api/v1/controller/report` (mTLS) → `{applied_generation, checksum, health}` →
    `SetAppliedGeneration` + `TouchLastSeen` + audit.
  - `POST /api/v1/controller/update-topology` (operator) → validate (reuse validator via a dry
    `compiler` schema check or just store) + `PutTopology`.
  - `POST /api/v1/controller/stage` (operator) → `CompileAndStage`; returns the `StageResult`.
  - `POST /api/v1/controller/promote` (operator) → `PromoteStaged`; returns `{generation}`.
- The `DevCA` is created at controller startup (ephemeral, 4.2) and shared by the enroll handler +
  the TLS `ClientCAs` pool. The Phase-0 bundle signing key (`YAOG_BUNDLE_SIGNING_KEY`) is read by
  `CompileAndStage`'s `Export` as before.

## Implementation

1. `internal/api/auth_controller.go` — the mTLS middleware + CN parsing + context helpers
   (`tenantFromCtx`, `nodeFromCtx`), an `isOperator` check, fail-closed 401/403.
2. `internal/api/handler_controller.go` — a `ControllerHandler` struct holding `{store controller.Store,
   ca *controller.DevCA, tenant controller.TenantID}` + the seven handlers; JSON request/response
   types; long-poll deadline handling.
3. `cmd/server/main.go` + `internal/api/server.go` — env-gated wiring: when controller mode is on,
   build the Store (FileStore from a state dir), the DevCA, register the routes, and serve with the
   TLS 1.3 + mTLS config; otherwise the server is exactly as today.
4. Tests `internal/api/controller_http_test.go` — `httptest.NewUnstartedServer` + `StartTLS` with the
   dev CA, MemStore: `/enroll` over a CA-less client → get a client cert → mTLS client (cert in the
   transport) calls `/config` (404 then, after operator stage+promote, the bundle), `/poll` (returns
   on a concurrent promote; 204 on timeout with a short deadline), `/report`; auth rejects a missing
   cert (401 on `/config`) and a wrong-CN/operator-only route from a node cert (403). Operator routes
   exercised with an operator cert.
5. Spec `docs/spec/controller/controller-api.md` (routes, auth model, the certless-`/enroll`
   exception, long-poll semantics, the single-tenant constant) + README index.

## Definition of done

- [ ] CI green; in-process httptest+TLS+dev-CA covers enroll→mTLS→config/poll/report + auth rejection;
      air-gap endpoints byte-identical; no new go.mod dep (stdlib net/http + crypto/tls).
- [ ] One auth chokepoint; a node can only fetch its own config; tenant pinned from the cert+config.

## Out of scope (4.3c / 4.4 / Plan 5)

The agent-side mTLS client + the full enroll→apply e2e (4.3c); the frontend (4.4); OIDC operator
login, RBAC, multi-tenant principal-derived tenant, KMS, step-up promote (Plan 5). Real-host mTLS
handshake verification is the manual smoke (CI uses in-process TLS).
