# Controller HTTP API (Phase 2c-b — TLS 1.3 + mTLS networked controller)

This document defines the controller's **networked surface**: the `/api/v1/controller/` HTTP routes,
the **TLS 1.3 + per-node mutual-TLS** model that authenticates them, the **single auth chokepoint**
that derives `tenant:node` from a verified client-cert CN, and the **env-gated controller mode** of
`cmd/server` that turns the whole thing on **beside** the untouched air-gap endpoints. It is the
wire-facing layer in front of the controller core: it serves the registry/topology/bundle state of
[persistence.md](persistence.md), runs the `Enroll` ceremony of [enrollment.md](enrollment.md), and
drives the `CompileAndStage` step of [deploy.md](deploy.md). Everything here is **stdlib only**
(`net/http`, `crypto/tls`, `crypto/x509`) — no new `go.mod` dependency.

**Scope of this milestone (plan-4.3b).** This document and the HTTP/TLS/auth layer
(`internal/api/handler_controller.go`, `internal/api/auth_controller.go`, the env-gated wiring in
`cmd/server/main.go` + `internal/api/server.go`, plus `IssueServerCert` / `ServerTLSConfig` added to
`internal/controller/enrollment.go`) are the **networked controller service**. The compile/stage core
it calls is [4.3a](deploy.md) (merged); the registry/Store it serves is [4.1](persistence.md); the
enrollment crypto is [4.2](enrollment.md). The **agent-side mTLS client** and the full
enroll→pull→verify→apply→report **end-to-end** test are **plan-4.3c**; the **frontend** controller
panel is **plan-4.4**; **OIDC** operator login, RBAC, multi-tenant principal-derived `TenantID`, KMS,
and step-up promote are **Plan 5**. See
[../../../implementation_plans/controller-panel-2026_06_08/plan-4.3b-2026_06_08.md](../../../implementation_plans/controller-panel-2026_06_08/plan-4.3b-2026_06_08.md)
and the parent [plan-4-2026_06_08.md](../../../implementation_plans/controller-panel-2026_06_08/plan-4-2026_06_08.md).

## The env-gated controller mode

`cmd/server` runs in **one of two modes**, selected by environment at startup:

- **Air-gap mode (default — unchanged).** When the controller env is **not** set, `cmd/server` is
  **exactly** the server it is today: it serves the air-gap `/api/health`, `/api/validate`,
  `/api/compile`, `/api/export`, `/api/deploy-script` endpoints over plain HTTP via `NewServer()` and
  `ListenAndServe`, with the existing panic-recovery + CORS + timeout middleware
  ([../api/http-api.md](../api/http-api.md)). Nothing about the air-gap path changes — same routes,
  same bytes, same behavior.
- **Controller mode (env-gated).** When the controller env **is** set, `cmd/server` additionally
  builds the controller dependencies (a durable `FileStore` over the state dir, an ephemeral `DevCA`,
  the `ControllerHandler`), registers the `/api/v1/controller/` routes, and serves over **TLS 1.3 with
  per-node mTLS** instead of plain HTTP. The air-gap routes remain registered on the **same** mux and
  remain reachable; the controller routes are layered on, not a replacement.

**The gate.** Controller mode is on iff the controller state-dir env is present:

| Env var                     | Meaning                                                                       |
| --------------------------- | ----------------------------------------------------------------------------- |
| `YAOG_CONTROLLER_STATE_DIR` | Directory for the durable `FileStore` (created `0700`). **Presence = enable.** |
| `YAOG_TENANT_ID`            | The single-tenant `TenantID` constant pinned for v1 (see below).               |
| `YAOG_BUNDLE_SIGNING_KEY`   | Optional Phase-0 bundle-signing key, read by `CompileAndStage`'s `Export`.     |

The constant env name is defined once in the server package (e.g. `envControllerStateDir =
"YAOG_CONTROLLER_STATE_DIR"`). Leaving `YAOG_CONTROLLER_STATE_DIR` unset is the explicit way to keep a
deployment air-gap-only — the controller code is compiled in but dormant, so an operator who never
opts in is never exposed to the networked surface.

## The TLS 1.3 + mTLS model

Controller mode terminates **TLS 1.3** and authenticates clients by **certificate**. The `DevCA`
([enrollment.md](enrollment.md)) is the single ephemeral root: it issues both the controller's **server**
cert and the per-node/operator **client** certs, so one in-memory CA anchors the entire mTLS mesh.

`(*DevCA).ServerTLSConfig(serverCert tls.Certificate)` builds the config:

```go
&tls.Config{
    MinVersion:   tls.VersionTLS13,            // TLS 1.3 floor — no downgrade
    Certificates: []tls.Certificate{serverCert}, // the controller's own server cert
    ClientCAs:    <pool containing the dev CA cert>, // trust anchor for client certs
    ClientAuth:   tls.VerifyClientCertIfGiven, // see below
}
```

**`VerifyClientCertIfGiven` is the deliberate choice** (not `RequireAndVerifyClientCert`): it lets a
client **without** a cert complete the handshake — which is exactly what a node enrolling for the
**first time** needs, since it has no client cert yet. But if a client **does** present a cert, the TLS
stack **verifies it chains to the dev CA** during the handshake. So the verification is done by TLS for
us; the application middleware then decides **per route** whether a verified cert is *required*:
`/enroll` tolerates its absence, every other controller route rejects it. (If we used
`RequireAndVerifyClientCert`, the certless `/enroll` call could not even handshake — the whole bootstrap
would be impossible.)

### The DevCA additions (the only edit outside `internal/api` / `cmd`)

Two methods are added to `internal/controller/enrollment.go` so the DevCA can stand up the TLS server,
keeping the mTLS material on one ephemeral root:

- **`(*DevCA).IssueServerCert(host string, now time.Time) (tls.Certificate, error)`** — issues the
  controller's **server** certificate (`ExtKeyUsageServerAuth`), signed by the same CA, with
  `DNSNames` / `IPAddresses` that include the configured `host` **plus** `"localhost"` and `127.0.0.1`
  so the in-process `httptest` TLS server and a local smoke both verify. It returns a
  `tls.Certificate` (cert + private key) ready to drop into `tls.Config.Certificates`.
- **`(*DevCA).ServerTLSConfig(serverCert tls.Certificate) *tls.Config`** — builds the mTLS config
  above: TLS 1.3 floor, the server cert, the CA pool as `ClientCAs`, `VerifyClientCertIfGiven`.

Both are **stdlib-only** and reuse the DevCA's existing ephemeral signing key. Because the CA is
ephemeral, a controller restart invalidates **both** the server cert and every issued client cert —
nodes re-enroll, and the agent re-pins the new CA cert from the fresh enroll response
([enrollment.md](enrollment.md) §The dev controller-CA).

## The single auth chokepoint

All authentication and authorization for the controller routes funnels through **one** middleware in
`internal/api/auth_controller.go`. There is no second place identity is derived — that is the point: a
single chokepoint is auditable and impossible to bypass per-route by accident.

**Identity from the client-cert CN.** A verified client cert carries a Common Name of the form
`"<tenant>:<node>"` (the exact identity `enrollment.go` binds at issuance). The middleware parses it
with `strings.Cut(cn, ":")` and puts both halves into the request context, retrievable via the
unexported helpers `tenantFromCtx(ctx)` and `nodeFromCtx(ctx)`. Downstream handlers read identity
**only** from the context — never from a URL query parameter or a request-body field.

**The decision ladder** (fail-closed at every rung):

1. **Cert required but absent → 401.** For every route except `/enroll`, if `r.TLS.PeerCertificates`
   is empty (no client cert was presented and verified), the middleware rejects with **401
   Unauthorized**. `/enroll` skips this rung — it is the bootstrap route a certless node must reach.
2. **Cert present but no parseable CN → 401.** A verified cert whose CN is empty or does not split into
   a non-empty `tenant` and `node` is rejected **401** — it carries no usable identity.
3. **Tenant mismatch → 403.** If the cert's `tenant` component is not equal to the configured
   `YAOG_TENANT_ID`, the request is rejected **403 Forbidden**. In single-tenant v1 the tenant is a
   **pinned constant**; this rung is where Plan 5's multi-tenant `TenantID` derivation will attach
   without changing the chokepoint shape ([persistence.md](persistence.md) §the `TenantID` chokepoint).
4. **Node acts only as itself.** Agent routes (`/config`, `/poll`, `/report`) operate on the **cert's
   node** (`nodeFromCtx`) — never a node named in the URL or body. There is no way for node A's cert to
   fetch node B's bundle or report on B's behalf: the identity is the cert, full stop. This is the
   structural guarantee that one compromised node cert cannot read or mutate another node's state.
5. **Operator gate → 403 for a normal node.** Operator routes (`/update-topology`, `/stage`,
   `/promote`) require a cert whose **node** component equals the configured operator identity
   `"operator"` (CN `"<tenant>:operator"`). An `isOperator` check on the context node enforces it; a
   **normal node cert on an operator route is rejected 403**. The operator is just another mTLS
   identity issued by the same DevCA (CN `<tenant>:operator`) — there is no password, no session.
   **OIDC operator login + RBAC is Plan 5**; this cert gate is the v1 stand-in.

The same context helpers are used by both the agent handlers (to scope to the caller's own node) and
the operator handlers (to assert the operator identity), so identity flows from one place to all seven
routes.

## Routes

All controller routes live under `/api/v1/controller/`, accept and return **JSON** (`Content-Type:
application/json`), and carry the error-body convention of the air-gap API
([../api/http-api.md](../api/http-api.md)): a failure returns `{"error": "<message>"}` with the
appropriate status. Byte-valued bundle files are transported **base64-encoded** in JSON.

| Method | Path                              | Auth          | Purpose                                                  |
| ------ | --------------------------------- | ------------- | ------------------------------------------------------- |
| `POST` | `/api/v1/controller/enroll`         | **none** (certless) | Run the enrollment ceremony; issue a client cert     |
| `GET`  | `/api/v1/controller/config`         | mTLS (node)   | Fetch the caller's current promoted bundle              |
| `GET`  | `/api/v1/controller/poll?after=N`   | mTLS (node)   | Long-poll for a generation strictly greater than `N`    |
| `POST` | `/api/v1/controller/report`         | mTLS (node)   | Report the generation/checksum/health the node applied  |
| `POST` | `/api/v1/controller/update-topology`| mTLS (operator) | Store the tenant's topology (public-keys-only)        |
| `POST` | `/api/v1/controller/stage`          | mTLS (operator) | Compile + stage the enrolled subgraph                  |
| `POST` | `/api/v1/controller/promote`        | mTLS (operator) | Promote the staged generation to current               |

### `POST /enroll` — the certless bootstrap

The **one** route reachable **without** a client cert (the node has none yet). It is gated instead by
the **single-use enrollment token + CSR proof-of-possession** of [enrollment.md](enrollment.md): the
TLS handshake succeeds certless (`VerifyClientCertIfGiven`), and the middleware skips the
cert-required rung for this path.

- **Request** — `controller.EnrollRequest` over JSON: the plaintext enrollment `token`, the claimed
  `node_id`, the DER-encoded self-signed mTLS CSR (the PoP), and the node's WireGuard **public** key.
- **Handler** — calls `controller.Enroll(ctx, store, ca, tenant, req, now)`. The token is atomically
  burned, the CSR's self-signature and CN (`"<tenant>:<node_id>"`) are verified, a per-node client
  cert is issued, and the node is registered `NodeApproved` with its WG public key + cert fingerprint.
- **Response** — `200` with `{"client_cert_pem": "...", "ca_cert_pem": "...", "fingerprint": "..."}`.
  The agent installs the client cert and **pins** the CA cert as its trust anchor for every subsequent
  mTLS call. A bad/expired/consumed token, a failed PoP, or a CN mismatch is a hard refusal (no cert
  issued); see [enrollment.md](enrollment.md) for the fail-safe burn-before-issue ordering.

### `GET /config` — the caller's current bundle

Serves the **caller's own** current (promoted) bundle. The node is taken from the cert
(`nodeFromCtx`), never a parameter.

- **Auth** — mTLS; node identity from the cert.
- **Handler** — `store.GetCurrentBundle(ctx, tenant, callerNode)` + `store.TouchLastSeen(ctx, tenant,
  callerNode, now)` (the check-in side effect that drives the panel's "last seen").
- **Response** — `200` with `{"generation": N, "files": {"<path>": "<base64>", …}}` — the bundle's
  generation and its files (each value base64-encoded). **404** if the node has no current bundle yet
  (i.e. before the operator's **first promote** that targets this node); the agent treats 404 as
  "nothing to apply yet" and keeps polling.

### `GET /poll?after=N` — the long-poll change-notify

The near-instant push primitive: an agent calls `/poll?after=<the generation it last applied>` and the
call **blocks** until a newer generation is promoted, turning a fleet deploy into a near-immediate pull
without a persistent server-side connection.

- **Auth** — mTLS; node identity from the cert.
- **Semantics** — the handler builds a `context.WithTimeout(r.Context(), ~55s)` and calls
  `store.WaitForGeneration(ctx, tenant, after)` ([persistence.md](persistence.md) §long-poll
  primitive). The `~55s` server deadline sits **just under** a typical 60s client/proxy idle timeout,
  so the call returns cleanly before any intermediary cuts it.
  - **Generation advanced** → `200` with `{"generation": M}` where `M > after` (the value
    `WaitForGeneration` returned the instant an operator promoted).
  - **Deadline reached with no advance** → **204 No Content** (empty body). The agent simply
    re-polls with the **same** `after` watermark. 204-then-repoll is the steady-state idle loop.
- **The `after` watermark** — `after=<generation>` is the agent's high-water mark: the last generation
  it successfully applied (or `0` on first boot). `WaitForGeneration` returns only a generation
  **strictly greater** than `after`, so an agent never re-applies a generation it already has, and a
  promote that happened *between* polls is caught on the next poll (the counter is already ahead of
  `after`, so the call returns immediately rather than blocking).

### `POST /report` — applied-state report

The node reports what it actually applied, closing the desired/applied loop the panel surfaces.

- **Auth** — mTLS; node identity from the cert (the node reports only on **itself**).
- **Request** — JSON `{"applied_generation": N, "checksum": "...", "health": "..."}`: the generation
  the agent applied, the manifest checksum it verified, and a free-form health string.
- **Handler** — `store.SetAppliedGeneration(ctx, tenant, callerNode, applied_generation, checksum)` +
  `store.TouchLastSeen(ctx, tenant, callerNode, now)` + an audit append. The node's registry record
  then carries `AppliedGeneration` / `LastChecksum` / `LastSeen`, which the panel diffs against
  `DesiredGeneration` to show convergence.
- **Response** — `200` (e.g. `{"ok": true}`).

### `POST /update-topology` — operator stores the topology

- **Auth** — mTLS **operator** cert (CN `"<tenant>:operator"`); a normal node cert → **403**.
- **Request** — the tenant's topology JSON (public-keys-only; it must not carry WireGuard private
  keys — [persistence.md](persistence.md) §zero-knowledge custody).
- **Handler** — `store.PutTopology(ctx, tenant, body)`, which assigns the next `Version`.
- **Response** — `200` (e.g. `{"version": V}`).

### `POST /stage` — operator compiles + stages

- **Auth** — mTLS **operator** cert; a normal node cert → **403**.
- **Handler** — `controller.CompileAndStage(ctx, store, tenant, now)` ([deploy.md](deploy.md)): renders
  the **enrolled subgraph** into signed per-node bundles staged at `CurrentGeneration + 1`. Staging is
  reversible and invisible to agents (a staged bundle is not yet current, so `/config` and `/poll` do
  not surface it).
- **Response** — `200` with the `StageResult`: `{"staged": ["<nodeID>", …], "skipped_unenrolled":
  ["<nodeID>", …], "generation": G}` — who was staged, who is still waiting on enrollment, and the
  prospective generation (the one a subsequent `/promote` would make current).

### `POST /promote` — operator promotes the staged generation

- **Auth** — mTLS **operator** cert; a normal node cert → **403**.
- **Handler** — `store.PromoteStaged(ctx, tenant)` ([persistence.md](persistence.md)): the **atomic
  flip** that turns the staged bundles current, increments the generation, stamps each promoted node's
  `DesiredGeneration`, and **wakes** every `WaitForGeneration` waiter — so agents blocked on `/poll`
  return the instant promote commits.
- **Response** — `200` with `{"generation": G}` (the new current generation). **`ErrNoStagedBundle`**
  (nothing staged) surfaces as an error body.

## The `ControllerHandler` and route registration

A `ControllerHandler` struct holds the controller dependencies:

```go
type ControllerHandler struct {
    store        controller.Store
    ca           *controller.DevCA
    tenant       controller.TenantID
    operatorName string // "operator" — the node component an operator cert must carry
}
```

It exposes a way to register its seven routes on an `*http.ServeMux` (e.g. `func (h *ControllerHandler)
Routes(mux *http.ServeMux)`, or an option threaded through `NewServer`), so a test can build a plain
`http.Handler` and drive it under `httptest` + `StartTLS` with the dev CA and a `MemStore`. The same
registration is used by `cmd/server` under the env gate; `cmd/server` then serves the mux with the
`DevCA.ServerTLSConfig(...)` TLS 1.3 + mTLS config. The auth chokepoint wraps the agent and operator
routes; `/enroll` is registered with the cert-optional path so a certless client can reach it.

## Summary

- `cmd/server` is **env-gated**: with `YAOG_CONTROLLER_STATE_DIR` unset it is the air-gap server
  exactly as today; with it set it additionally serves `/api/v1/controller/` over **TLS 1.3 + mTLS**.
  The air-gap endpoints are **unchanged** in both modes.
- TLS uses `MinVersion: tls.VersionTLS13`, the dev CA as `ClientCAs`, and
  **`VerifyClientCertIfGiven`** so the certless **`/enroll`** bootstrap can handshake while every other
  route's middleware **requires** a verified cert.
- One **DevCA** (ephemeral root) issues **both** the controller **server** cert
  (`IssueServerCert` / `ServerTLSConfig`, the only edit outside `internal/api` + `cmd`) and the
  per-node/operator **client** certs.
- A **single auth chokepoint** derives `tenant:node` from the client-cert CN: a node **acts only as
  itself** (`/config`, `/poll`, `/report` use the cert's node), the tenant is **pinned** to
  `YAOG_TENANT_ID`, and operator routes (`/update-topology`, `/stage`, `/promote`) require the
  **operator** cert (CN `<tenant>:operator`) — a node cert on them is **403**.
- **Long-poll**: `/poll?after=<generation>` blocks on `WaitForGeneration` with a **~55s** deadline,
  returns `{generation}` on advance or **204** on timeout; `after` is the agent's applied-generation
  watermark.
- **OIDC / RBAC / multi-tenant / KMS / step-up promote** are **Plan 5**; the **agent mTLS client +
  e2e** is **plan-4.3c**; the **frontend panel** is **plan-4.4**.

See also [enrollment.md](enrollment.md) (the certless `/enroll` ceremony + the DevCA),
[deploy.md](deploy.md) (the `CompileAndStage` behind `/stage` and the stage→promote model),
[persistence.md](persistence.md) (the `Store` behind every route — `GetCurrentBundle`,
`WaitForGeneration`, `SetAppliedGeneration`, `PromoteStaged`, the `TenantID` chokepoint), and
[agent.md](agent.md) (the node-side counterpart that will consume these routes in plan-4.3c).
