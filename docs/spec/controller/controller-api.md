# Controller HTTP API (Phase 2 — bearer-token auth, plain HTTP, two ports)

This document defines the controller's **networked surface**: the audience-split HTTP routes
(operator/panel under `/api/v1/operator/`, agent/node under `/api/v1/agent/`),
the **per-node bearer-token + operator-token** model that authenticates them, the **single auth
chokepoint** that derives `tenant:node` from a presented token, the **two-port split** (an
operator/panel port and an agent control-channel port), and the **env-gated controller mode** of
`cmd/server` that turns the whole networked surface on. It is the
wire-facing layer in front of the controller core: it serves the registry/topology/bundle state of
[persistence.md](persistence.md), runs the `Enroll` ceremony of [enrollment.md](enrollment.md), and
drives the `CompileAndStage` step of [deploy.md](deploy.md). Everything here is **stdlib only**
(`net/http`, `crypto/sha256`, `crypto/subtle`) — **no `crypto/tls`, no `crypto/x509`**, and no new
`go.mod` dependency. **TLS is delegated to a reverse proxy** (nginx/caddy); the app never terminates it.

**Scope of this milestone (plan-4.5).** This document and the HTTP/auth layer
(`internal/api/handler_controller.go`, `internal/api/auth_controller.go`, the env-gated wiring in
`cmd/server/main.go` + `internal/api/server.go`) are the **networked controller service**. The
compile/stage core it calls is [deploy.md](deploy.md); the registry/Store it serves is
[persistence.md](persistence.md); the enrollment crypto is [enrollment.md](enrollment.md); the
agent-side client is [agent.md](agent.md). The **frontend** controller panel is **plan-4.4**; **OIDC**
operator login, RBAC, per-operator audit identity, multi-tenant principal-derived `TenantID`, KMS, and
an optional in-app TLS toggle are **Plan 5**. See
[../../../implementation_plans/controller-panel-2026_06_08/plan-4.5-2026_06_08.md](../../../implementation_plans/controller-panel-2026_06_08/plan-4.5-2026_06_08.md)
and the parent [plan-4-2026_06_08.md](../../../implementation_plans/controller-panel-2026_06_08/plan-4-2026_06_08.md).

> **Retraction (2026-06-08).** An earlier revision of this spec specified **per-node mutual TLS** with a
> forced **TLS 1.3** server, an ephemeral **`DevCA`** that issued client/server certs, a **CSR
> proof-of-possession** at enroll, **client-cert-CN identity**, an out-of-band **CA pin**, and an
> `MTLSCertFP` on the node record. **All of that is withdrawn.** mTLS was operationally heavy (cert
> tuning, ephemeral-CA re-enroll-on-restart) and not a must-have for the single-tenant v1 trust model.
> The model below — **per-node bearer tokens over plain HTTP, with TLS pushed to a reverse proxy** — is
> the conscious replacement. Wherever this document previously said "client cert", read "bearer token";
> wherever it said "the dev CA", read "no in-app CA exists".

## The env-gated controller mode

`cmd/server` is **controller-only** — one build, no `-tags airgap` variant, and **no anonymous compute
surface**. The four former anonymous routes (`/api/validate`, `/api/compile`, `/api/export`,
`/api/deploy-script`) were **removed** in framework-refactor plan-9; only `GET /api/health` is public
(see [../operations/deployment-topology.md](../operations/deployment-topology.md)). Its startup
behavior is selected by environment:

- **Controller env set.** When the controller env **is** set, `cmd/server` builds the controller
  dependencies (a durable `FileStore` over the state dir, the `ControllerHandler`), registers the
  operator routes on the **operator/panel port** and the agent routes on a **separate agent
  control-channel port**, and serves **both over plain HTTP** with the existing panic-recovery + CORS +
  timeout middleware ([../api/http-api.md](../api/http-api.md)).
- **Controller env unset.** When it is **not** set, `cmd/server` **fails loud** (`cmd/server/main.go`)
  and exits — it links no anonymous compute surface to fall back to, so it names the fix (set the
  controller env, or use the standalone static-local-design site / `cmd/compiler` for offline
  compilation) instead of standing up a do-nothing listener.

**The gate.** Controller mode is on iff **both** `YAOG_CONTROLLER_STATE_DIR` **and** `YAOG_TENANT_ID`
are set (`cmd/server`: `if stateDir == "" || tenant == "" { fail loud }`). When it is on, the operator
token is **mandatory**: `cmd/server` **fails to start** (`log.Fatal`) if `YAOG_CONTROLLER_OPERATOR_TOKEN`
is empty — there is no anonymous-operator fallback.

| Env var                          | Meaning                                                                            |
| -------------------------------- | --------------------------------------------------------------------------------- |
| `YAOG_CONTROLLER_STATE_DIR`      | Directory for the durable `FileStore` (created `0700`). Required to enable.        |
| `YAOG_TENANT_ID`                 | The single-tenant `TenantID` constant pinned for v1. Also required to enable.      |
| `YAOG_CONTROLLER_OPERATOR_TOKEN` | The operator's bearer token (plaintext). **Required** when controller mode is on — the server refuses to start without it. Compared constant-time, hash-at-rest in process. |
| `YAOG_CONTROLLER_AGENT_ADDR`     | Listen address for the **agent control-channel** port (default `:9090`); `-agent-addr` flag overrides. |
| `YAOG_BUNDLE_SIGNING_KEY`        | Optional Phase-0 bundle-signing key, read by `CompileAndStage`'s `Export`.         |

The constant env names are defined once in the server package. Leaving the gate env unset makes
`cmd/server` **fail loud** and exit rather than stand up a listener — there is no anonymous compute
surface, so offline compilation is the standalone static-local-design site or `cmd/compiler`.

## Plain HTTP + proxy TLS (the transport decision)

The controller serves **plain HTTP** on both ports. It does **not** terminate TLS, does **not** load a
server certificate, and does **not** import `crypto/tls`. Transport confidentiality is the **reverse
proxy's** job:

```
                    ┌──────────────────────── operator / panel (TLS) ──────────────────────────┐
  operator ──TLS──▶ │  nginx / caddy  ──plain HTTP──▶  cmd/server  -addr  (default :8080)        │
                    │                                   • GET /api/health (open liveness probe)   │
                    │                                   • operator routes (operatorAuth)          │
                    └──────────────────────────────────────────────────────────────────────────┘
                    ┌──────────────────────── agent control channel (TLS) ─────────────────────┐
  node agent ─TLS─▶ │  nginx / caddy  ──plain HTTP──▶  cmd/server  -agent-addr (default :9090)   │
                    │                                   • /enroll (no auth)                       │
                    │                                   • /config /poll /report (requireNode)     │
                    └──────────────────────────────────────────────────────────────────────────┘
```

**Why delegate TLS.** Cert provisioning, renewal (ACME), cipher policy, HSTS, and SNI are exactly the
problems nginx/caddy already solve well; reimplementing them in-app (the withdrawn mTLS model) added
operational weight without buying anything the proxy cannot. The operator stands up one proxy in front
of both ports and gets modern TLS for free; the app stays small and stdlib-only.

**The honest trade-off — bearer tokens are replayable if leaked.** A bearer token authenticates
**whoever presents it**: there is no per-request proof-of-possession (no signature, no client cert), so a
token captured on the wire or lifted off a node's disk can be **replayed** by an attacker until it is
revoked. Confidentiality of the token in transit therefore rests **entirely on the proxy's TLS** — this
is why running the controller behind a TLS-terminating proxy is **not optional in production** even
though the app speaks plain HTTP. This is a deliberate v1 simplification over the withdrawn mTLS model
(where the private key never left the node and could not be replayed from a captured request); it trades
that property for far lower operational cost, and bounds the blast radius with **immediate revocation**
(clear the token → the next request fails, [deploy.md](deploy.md) §Revocation, [persistence.md](persistence.md)
§The per-node API-token index). An in-app TLS toggle and a stronger PoP scheme are documented Plan 5
directions.

## The two ports

Controller mode serves **two** muxes on **two** addresses, both plain HTTP:

- **Operator / panel port** (`-addr`, default `:8080`) — `s.mux`. Carries the **open, unauthenticated
  liveness probe** (`GET /api/health`) **and** the operator routes (`/update-topology`, `/stage`,
  `/promote`, `/nodes`, `/audit`, `/topology`, `/enrollment-token`, all behind `operatorAuth`). This is
  the port a human operator and the frontend panel talk to. There is **no** anonymous compute route —
  the four `/api/{validate,compile,export,deploy-script}` endpoints were removed (plan-9); LOCAL design
  compiles in-browser on the WASM engine.
- **Agent control-channel port** (`-agent-addr` / `YAOG_CONTROLLER_AGENT_ADDR`, default `:9090`) —
  `s.agentMux`. Carries only the agent routes: `/enroll` (no auth — the bootstrap), and `/config`,
  `/poll`, `/report` (each behind `requireNode`). Nothing else lives here.

The split keeps the two audiences on separate listeners so a proxy can apply different policy to each
(e.g. the agent port may face a wider network than the panel port), and so the long-poll `/poll`
handler's long write timeout is confined to the agent listener. `EnableController(ch)` wires it:
`ch.RegisterOperatorRoutes(s.mux)` + `ch.RegisterAgentRoutes(s.agentMux)`. `cmd/server` serves both
concurrently — two goroutines feeding one error channel; the first listener error brings the process
down.

**Two independent secret path prefixes** (controller-server-authority-redesign): each port's routes
mount under their own optional prefix — `YAOG_OPERATOR_PATH_PREFIX` for the operator mux and
`YAOG_AGENT_PATH_PREFIX` for the agent mux (the old single `YAOG_CONTROLLER_PATH_PREFIX` is removed;
the server refuses to start if it is still set). So a path-based proxy on one hostname can route
`/<operator-prefix>/*` → `:8080` and `/<agent-prefix>/*` → `:9090` unambiguously. The prefixes are
drive-by-scanner obscurity, NOT a security boundary. The server logs both mounted base paths at
startup (`OperatorBasePath()` / `AgentBasePath()`), so a proxy misroute is diagnosable from the
container log; the bootstrap installer bakes the **agent** prefix into the node's controller URL.

## The single auth chokepoint

All authentication and authorization for the controller routes funnels through **one** file,
`internal/api/auth_controller.go`. There is no second place identity is derived — that is the point: a
single chokepoint is auditable and impossible to bypass per-route by accident.

**Token parsing.** Both auth paths read the credential from the **`Authorization: Bearer <token>`**
header via the unexported `bearerToken(r) (string, bool)` helper (it returns `("", false)` when the
header is missing or not a `Bearer` scheme). There is **no** token in a URL query parameter or a request
body — the presented bearer token is the **sole** source of identity.

**Hash-then-compare.** The chokepoint never compares plaintext. A presented token is hashed with
`controller.HashToken` (hex SHA-256) and the **hash** is what is matched — against the per-node reverse
index for agents, and against the stored operator-token hash for the operator. The plaintext is never
stored and never logged.

### Node authentication — `authenticateNode` + `requireNode`

`authenticateNode(r) (authResult, int, string)` is the agent-route gate:

1. `bearerToken(r)` — **missing → 401** (`Unauthorized`, the request carried no credential).
2. `store.LookupNodeByAPIToken(ctx, h.tenant, controller.HashToken(token))`
   ([persistence.md](persistence.md) §The per-node API-token index):
   - an **unmapped** hash → the store returns `ErrTokenInvalid` → **401** (the token is not a live
     node token).
   - a node whose `Status == NodeRevoked` → **403** (`Forbidden`; the identity is known but evicted —
     see the note below on why revoked-via-status surfaces as 403 here).
   - otherwise → `authResult{tenant: h.tenant, node: node.NodeID}` and the request proceeds.

`requireNode(next)` wraps the agent handlers: it runs `authenticateNode`, writes the error status on
failure, and on success injects the tenant + the **node's own id** into the request context (the
unexported `ctxKeyTenant` / `ctxKeyNode` helpers). Downstream agent handlers read identity **only** from
that context.

**Node acts only as itself.** `/config`, `/poll`, and `/report` operate on the **token's node** —
never a node named in the URL or body. There is no way for node A's token to fetch node B's bundle or
report on B's behalf: the identity is the token, full stop. This is the structural guarantee that one
compromised node token cannot read or mutate another node's state — the same property the withdrawn mTLS
model gave via the cert CN, now carried by the token.

### Operator authentication — `operatorAuth`

`operatorAuth(next)` replaces the withdrawn `requireOperator`. It gates every operator route:

1. `bearerToken(r)` — **missing → 401**.
2. `subtle.ConstantTimeCompare([]byte(controller.HashToken(token)), []byte(h.operatorTokenHash)) != 1`
   → **403**. The compare is **constant-time** (`crypto/subtle`) so the operator token cannot be
   recovered by timing the comparison; both sides are the fixed-length hex SHA-256, so the lengths always
   match.
3. On success it injects `ctxKeyTenant = h.tenant` and `ctxKeyNode = h.operatorName` (the reserved
   operator identity, default `"operator"`), so an operator action audits with a stable actor name.

The operator token is a **single shared secret** (`YAOG_CONTROLLER_OPERATOR_TOKEN`) — there is no
password, no session, no per-operator identity in v1. **OIDC operator login + RBAC + per-operator audit
identity is Plan 5**; this env token is the v1 stand-in. A **node** token presented on an operator route
fails the constant-time compare (a node-token hash is in the per-node index, not the operator slot) and
is rejected **403** — node tokens never work on operator routes, and the operator token is never a valid
node token (it is not in the per-node index, so `authenticateNode` returns 401).

> **401 vs 403.** Missing credential → **401** (authenticate first). A credential that is **present but
> not accepted** → **403** (a known-but-revoked node, or a token that fails the operator compare). An
> unmapped/garbage token on an agent route surfaces as **401** because `ErrTokenInvalid` means "this is
> not a credential I recognize", which is an authentication failure, not an authorization one.

## Routes

Agent/node routes live under `/api/v1/agent/` (agent port) and operator/panel routes under
`/api/v1/operator/` (operator port) — distinct namespaces so the two surfaces never collide by
path. All accept and return **JSON** (`Content-Type:
application/json`), and carry the JSON error-body convention of the HTTP API
([../api/http-api.md](../api/http-api.md)): a failure returns `{"error": "<message>"}` with the
appropriate status. Byte-valued bundle files are transported **base64-encoded** in JSON.

| Method | Port  | Path                                | Auth           | Purpose                                                  |
| ------ | ----- | ----------------------------------- | -------------- | ------------------------------------------------------- |
| `POST` | agent | `/api/v1/agent/enroll`              | **none**       | Run the enrollment ceremony; issue the node's API token  |
| `GET`  | agent | `/api/v1/agent/config`              | node bearer    | Fetch the caller's current promoted bundle              |
| `GET`  | agent | `/api/v1/agent/poll?after=N`        | node bearer    | Long-poll for a generation strictly greater than `N`    |
| `POST` | agent | `/api/v1/agent/report`              | node bearer    | Report the generation/checksum/health the node applied  |
| `POST` | agent | `/api/v1/agent/telemetry`           | node bearer    | LIVE health heartbeat (conditions + last-seen; never deploy state) |
| `POST` | panel | `/api/v1/operator/update-topology`  | operator token | Store the tenant's topology (public-keys-only)         |
| `POST` | panel | `/api/v1/operator/stage`            | operator token | Compile + stage the enrolled subgraph                  |
| `POST` | panel | `/api/v1/operator/promote`          | operator token | Promote the staged generation to current               |
| `GET`  | panel | `/api/v1/operator/nodes`            | operator token | List the registry (no key material, no tokens)         |
| `GET`  | panel | `/api/v1/operator/audit`            | operator token | Read the hash-chained audit log + its verification     |
| `GET`  | panel | `/api/v1/operator/topology`         | operator token | Read the current stored topology                       |
| `POST` | panel | `/api/v1/operator/enrollment-token` | operator token | Mint a single-use enrollment token (plaintext once)    |

### `POST /enroll` — the unauthenticated bootstrap

The **one** route reachable **without** a credential (the node has none yet). It is gated instead by the
**single-use enrollment token** of [enrollment.md](enrollment.md): the request carries a live token that
the controller **burns** before issuing anything. It is registered on the **agent** mux with **no auth
wrapper**.

- **Request** — `enrollRequestJSON`: the plaintext enrollment token, the claimed node id, and the node's
  WireGuard **public** key:

  ```go
  type enrollRequestJSON struct {
      Token       string `json:"enrollment_token"`
      NodeID      string `json:"node_id"`
      WGPublicKey string `json:"wg_public_key"`
  }
  ```

  There is **no CSR** and **no cert material** — the CSR proof-of-possession of the withdrawn mTLS model
  is gone.
- **Handler** — `HandleEnroll` first applies the **reserved-operator-name guard**: a `node_id` equal to
  the configured `operatorName` is rejected **403** (a node may not enroll as the operator identity). It
  then calls `controller.Enroll(ctx, store, tenant, req, now)` ([enrollment.md](enrollment.md) §The
  Enroll ceremony — note the **new signature has no `*DevCA` parameter**). The enrollment token is
  atomically burned, a fresh per-node bearer token is minted, the node is registered `NodeApproved` with
  its WG public key + the **token's hash**, and the token's hash is indexed.
- **Response** — `200` with `enrollResponseJSON`:

  ```go
  type enrollResponseJSON struct {
      ApiToken string `json:"api_token"`
      NodeID   string `json:"node_id"`
  }
  ```

  The plaintext `api_token` is returned **exactly once** — the agent persists it 0600 and presents it as
  `Authorization: Bearer <token>` on every subsequent call. A bad/expired/consumed enrollment token is a
  hard refusal (no API token issued); see [enrollment.md](enrollment.md) for the fail-safe burn-before-issue
  ordering.

### `GET /config` — the caller's current bundle

Serves the **caller's own** current (promoted) bundle. The node is taken from the token
(`ctxKeyNode`), never a parameter.

- **Auth** — node bearer; node identity from the token.
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

- **Auth** — node bearer; node identity from the token.
- **Semantics** — the handler builds a `context.WithTimeout(r.Context(), ~55s)` and calls
  `store.WaitForGeneration(ctx, tenant, after)` ([persistence.md](persistence.md) §long-poll
  primitive). The `~55s` server deadline sits **just under** a typical 60s client/proxy idle timeout, so
  the call returns cleanly before any intermediary cuts it. (The agent listener's `WriteTimeout` is set
  generously — ~90s — so the long-poll write is never severed by the server's own write deadline.)
  - **Generation advanced** → `200` with `{"generation": M}` where `M > after` (the value
    `WaitForGeneration` returned the instant an operator promoted).
  - **Deadline reached with no advance** → **204 No Content** (empty body). The agent simply re-polls
    with the **same** `after` watermark. 204-then-repoll is the steady-state idle loop.
- **The `after` watermark** — `after=<generation>` is the agent's high-water mark: the last generation
  it successfully applied (or `0` on first boot). `WaitForGeneration` returns only a generation
  **strictly greater** than `after`, so an agent never re-applies a generation it already has, and a
  promote that happened *between* polls is caught on the next poll (the counter is already ahead of
  `after`, so the call returns immediately rather than blocking).

### `POST /report` — applied-state report

The node reports what it actually applied, closing the desired/applied loop the panel surfaces.

- **Auth** — node bearer; node identity from the token (the node reports only on **itself**).
- **Request** — JSON `{"applied_generation": N, "checksum": "...", "health": "..."}`: the generation
  the agent applied, the manifest checksum it verified, and a free-form health string.
- **Handler** — `store.SetAppliedGeneration(ctx, tenant, callerNode, applied_generation, checksum)` +
  `store.TouchLastSeen(ctx, tenant, callerNode, now)` + an audit append. The node's registry record then
  carries `AppliedGeneration` / `LastChecksum` / `LastSeen`, which the panel diffs against
  `DesiredGeneration` to show convergence.
- **Response** — `200` (`{"status": "ok"}`).

### `POST /telemetry` — live health heartbeat

A daemon-mode agent sends a periodic heartbeat so the panel reflects **current** node health, not the
frozen apply-time snapshot. (Before this channel, Node Conditions were sampled only at apply time —
when WireGuard was still mid-handshake (`wireguard: LinkDown`) and a self-update was mid-probation
(`selfupdate: HealthConfirmedProbationary`) — and never refreshed while the node idled, so the panel
showed a stale worst case.)

- **Auth** — node bearer; node identity from the token (the node reports only on **itself**).
- **Request** — JSON `{"conditions": [...], "metrics": {...}, "agent_version": "..."}`. It carries the
  same structured conditions as `/report` PLUS an extensible `metrics` map, and **deliberately no**
  `applied_generation` / `checksum`: telemetry is **observability, kept strictly separate from deploy
  custody**.
- **Handler** — `store.RecordTelemetry(ctx, tenant, callerNode, conditions, metrics, agent_version, now)`,
  which updates ONLY the node's `Conditions` (server-stamped with the controller clock), the extensible
  `Telemetry` metrics map (replaced wholesale; served verbatim in the node JSON for the panel), and
  `LastSeen` (+ `LastAgentVersion`). It **never** touches `AppliedGeneration` / `LastChecksum` /
  `LastHealth` / `DesiredGeneration`, so a heartbeat can never advance or regress a node's applied
  generation. It is **intentionally not audited** — a 30s heartbeat would flood the hash-chained log.
- **Conditions: dual-write.** Conditions now flow from BOTH paths: `/telemetry` is the LIVE source
  (refreshes every `--telemetry-interval`, default 30s) and `/report` still stamps them at apply-time;
  both wholesale-replace `node.Conditions`, last-writer-wins, so the heartbeat supersedes the stale
  apply-time snapshot within one interval. **Back-compat:** a legacy agent (no heartbeat, or
  `--telemetry-interval 0`) gets conditions only at apply-time on `/report` exactly as before; a new
  agent against an old controller (no `/telemetry` route) gets a swallowed `404` heartbeat and its
  `/report` conditions still land.
- **Extension point.** The agent side is a pluggable `Sampler` framework (`internal/agent/telemetry.go`):
  a `Sampler` is registered in `BuildTelemetry`, runs each heartbeat under a panic guard, and writes
  conditions and/or named `metrics`. The first probe is the condition sampler; the first real **metric**
  is `wireguardPeersSampler`, which emits `metrics["wireguard_peers"]` — the per-peer link health
  (`{peer, interface, endpoint, last_handshake, status}`, no key material) the controller persists +
  serves under `node.telemetry` and the panel renders as a **collapsible per-link panel** (the detail
  behind the aggregate `wireguard` condition). A future probe (e.g. per-peer RTT) adds another `Sampler`
  with no transport change. `Condition.Type`/`Status` are plain strings, so a new condition type needs
  no model change. Relatedly, the aggregate `wireguard` condition now distinguishes **all** peers down
  (`LinkDown`) from **some** down (`SomePeersDown`), so one offline mesh peer no longer flags the whole
  node as down.
- **The `resource` sampler + `cpu_pct`.** `resourceSampler` (`internal/agent/telemetry_resource.go`)
  emits `metrics["resource"]` = `{cpu_pct?, load1, load5, load15, mem_total_kb, mem_available_kb}` (no key
  material). It is **stateful**: `cpu_pct` is the delta of `/proc/stat` busy-vs-total jiffies between
  consecutive heartbeats, so the **first** beat after daemon start carries **no** `cpu_pct` (a gap, never
  a fabricated 0). The controller retains a bounded per-node history of this metric and serves it as the
  node-detail CPU/RAM/load charts — see [../operations/telemetry-history.md](../operations/telemetry-history.md).
- **Freshness — metrics ride the heartbeat + a post-apply kick.** The Sampler heartbeat is the **sole**
  producer of `metrics`: the apply-time `/report` carries **conditions only, never metrics**, so a
  metric like `resource` exists *only* on the heartbeat path and can no longer be a frozen apply-time
  snapshot. **Conditions**, by contrast, remain **dual-write** (above) — `/report` still stamps them at
  apply-time and the heartbeat refreshes them live, last-writer-wins (dropping the `/report` conditions
  emission is deferred as custody-sensitive). To keep a just-deployed node from waiting up to a full
  interval for that live refresh, the agent's apply loop sends a non-blocking, coalescing **kick** to the
  heartbeat loop after each applied cycle, so a fresh heartbeat (carrying the just-applied state, metrics
  included) posts immediately and promptly supersedes the apply-time conditions snapshot. This is the
  structural fix for the recurring "a new metric only ever fires at deploy time, then freezes" class: a
  new metric is emitted by a `Sampler` on the heartbeat, never bolted onto `/report`.
- **Response** — `200` (`{"status": "ok"}`).

### `POST /update-topology` — operator stores the topology

- **Auth** — operator token; a node token → **403**.
- **Request** — the tenant's topology JSON (public-keys-only; it must not carry WireGuard private keys —
  [persistence.md](persistence.md) §zero-knowledge custody).
- **Handler** — `store.PutTopology(ctx, tenant, body)`, which assigns the next `Version`.
- **Response** — `200` (e.g. `{"version": V}`).

### `POST /stage` — operator compiles + stages

- **Auth** — operator token; a node token → **403**.
- **Handler** — `controller.CompileAndStage(ctx, store, tenant, now)` ([deploy.md](deploy.md)): renders
  the **enrolled subgraph** into signed per-node bundles staged at `CurrentGeneration + 1`. Staging is
  reversible and invisible to agents (a staged bundle is not yet current, so `/config` and `/poll` do not
  surface it).
- **Response** — `200` with the `StageResult`: `{"staged": ["<nodeID>", …], "skipped_unenrolled":
  ["<nodeID>", …], "generation": G}` — who was staged, who is still waiting on enrollment, and the
  prospective generation (the one a subsequent `/promote` would make current).

### `POST /promote` — operator promotes the staged generation

- **Auth** — operator token; a node token → **403**.
- **Handler** — `store.PromoteStaged(ctx, tenant)` ([persistence.md](persistence.md)): the **atomic
  flip** that turns the staged bundles current, increments the generation, stamps each promoted node's
  `DesiredGeneration`, and **wakes** every `WaitForGeneration` waiter — so agents blocked on `/poll`
  return the instant promote commits.
- **Response** — `200` with `{"generation": G}` (the new current generation). **`ErrNoStagedBundle`**
  (nothing staged) surfaces as an error body.

### `GET /nodes` — operator reads the registry

The panel's fleet view. Returns the node registry with **no key material and no tokens** — only the
operational columns the panel needs to show convergence and last-seen.

- **Auth** — operator token; a node token → **403**.
- **Handler** — `store.ListNodes(ctx, tenant)` ([persistence.md](persistence.md)).
- **Response** — `200` with `[]nodeJSON`: `node_id`, `status`, **`has_wg_public_key`** (a boolean, never
  the key itself), `desired_generation`, `applied_generation`, `last_checksum`, `last_seen`,
  `enrolled_at`. The `WGPublicKey` and `APITokenHash` fields are **never** serialized — the panel needs
  to know a key is *present*, not what it is, and the token hash never leaves the store.

### `GET /audit` — operator reads the audit log

- **Auth** — operator token; a node token → **403**.
- **Handler** — `store.ListAudit(ctx, tenant)` + `controller.VerifyAuditChain(entries)`
  ([persistence.md](persistence.md) §audit hash chain).
- **Response** — `200` with `{"entries": [...], "verified": <bool-or-index>}`: the hash-chained entries
  in `Seq` order plus the chain-verification result (`-1` / `true` = intact, otherwise the index of the
  first broken entry), so the panel can surface an integrity signal.

### `GET /topology` — operator reads the current topology

- **Auth** — operator token; a node token → **403**.
- **Handler** — `store.GetTopology(ctx, tenant)` ([persistence.md](persistence.md)); `ErrNotFound`
  before the first `update-topology` surfaces as a 404.
- **Response** — `200` with the current stored topology JSON (public-keys-only), so the panel can render
  the design the operator last pushed.

### `POST /enrollment-token` — operator mints an enrollment token

The operator's authorize step, surfaced over HTTP so the panel can mint a token for a node about to come
online.

- **Auth** — operator token; a node token → **403**.
- **Request** — JSON `{"node_id": "<id>", "ttl_seconds": N}`.
- **Handler** — `controller.NewEnrollmentToken(node_id, ttl, now)` then
  `store.CreateEnrollmentToken(ctx, tenant, tok)` ([enrollment.md](enrollment.md) §The enrollment
  token). Only the **hash** is persisted.
- **Response** — `200` with `{"token": "<plaintext>"}` — the plaintext enrollment token returned
  **once**, for the operator to hand the node out-of-band. (It is distinct from the per-node **API**
  token, which the node receives from `/enroll`.)

## The `ControllerHandler` and route registration

A `ControllerHandler` struct holds the controller dependencies — note there is **no `ca` field** (the
DevCA is gone):

```go
type ControllerHandler struct {
    store             controller.Store
    tenant            controller.TenantID
    operatorTokenHash string        // hex SHA-256 of the operator bearer token (never plaintext)
    operatorName      string        // "operator" — the reserved actor name + the node_id /enroll rejects
    pollDeadline      time.Duration // the ~55s /poll long-poll deadline
}
```

`NewControllerHandler(store, tenant, operatorTokenHash, operatorName string) *ControllerHandler` builds
it — the caller (`cmd/server`) hashes the env operator token with `controller.HashToken` **before**
constructing the handler, so the handler never holds the plaintext. It exposes **two** registration
methods rather than one combined `Routes`:

- `RegisterAgentRoutes(mux *http.ServeMux)` — `/enroll` (no auth), `/config`, `/poll`, `/report` (each
  wrapped in `requireNode`). Registered on the **agent** mux.
- `RegisterOperatorRoutes(mux *http.ServeMux)` — `/update-topology`, `/stage`, `/promote`, `/nodes`,
  `/audit`, `/topology`, `/enrollment-token` (each wrapped in `operatorAuth`). Registered on the
  **operator/panel** mux.

A test builds two plain `http.Handler`s via `Server.Handler()` / `Server.AgentHandler()` (which return
the two muxes) and drives them under `httptest.NewServer` (**plain**, not `StartTLS`) with a `MemStore`
and bearer tokens. `cmd/server` uses the same registration under the env gate, then serves each mux on
its own plain-HTTP listener.

## Summary

- `cmd/server` is **controller-only** and **env-gated**: with `YAOG_CONTROLLER_STATE_DIR` /
  `YAOG_TENANT_ID` unset it **fails loud** and exits; with them set it serves the controller routes — and
  **requires** `YAOG_CONTROLLER_OPERATOR_TOKEN` (it `log.Fatal`s without it). There is **no** anonymous
  compute surface — only `GET /api/health` is open on the operator/panel port; the four
  `/api/{validate,compile,export,deploy-script}` routes were removed (plan-9).
- **No TLS in-app.** Both ports speak **plain HTTP**; TLS is delegated to a reverse proxy (nginx/caddy).
  The controller imports neither `crypto/tls` nor `crypto/x509`.
- **Two ports**: an **operator/panel** port (`-addr`, default `:8080`) carrying the open `/api/health`
  probe + the `operatorAuth` operator routes, and an **agent** control-channel port (`-agent-addr` /
  `YAOG_CONTROLLER_AGENT_ADDR`, default `:9090`) carrying `/enroll` (no auth) + the `requireNode` agent
  routes. Served concurrently over one error channel.
- A **single auth chokepoint** in `auth_controller.go`: `authenticateNode` resolves a node bearer token
  via `LookupNodeByAPIToken(HashToken(token))` (unmapped → 401, revoked → 403) and a node **acts only as
  itself**; `operatorAuth` constant-time-compares `HashToken(token)` against the stored operator-token
  hash (missing → 401, mismatch → 403). Node tokens never work on operator routes; the operator token is
  never a valid node token.
- **Long-poll**: `/poll?after=<generation>` blocks on `WaitForGeneration` with a **~55s** deadline,
  returns `{generation}` on advance or **204** on timeout; `after` is the agent's applied-generation
  watermark; the agent listener's ~90s write timeout keeps the long write alive.
- **Honest trade-off**: a leaked bearer token is **replayable** until revoked, so confidentiality rests
  on the proxy's TLS; revocation is **immediate** (clear the token). This is the conscious v1 model.
- **OIDC / RBAC / per-operator audit / multi-tenant / KMS / in-app TLS** are **Plan 5**; the **frontend
  panel** is **plan-4.4**.

See also [enrollment.md](enrollment.md) (the `/enroll` token ceremony — no cert, no DevCA),
[deploy.md](deploy.md) (the `CompileAndStage` behind `/stage`, the stage→promote model, and revocation),
[persistence.md](persistence.md) (the `Store` behind every route — `GetCurrentBundle`,
`WaitForGeneration`, `SetAppliedGeneration`, `PromoteStaged`, the per-node API-token index, the `TenantID`
chokepoint), and [agent.md](agent.md) (the node-side counterpart that consumes these routes with a bearer
token).
