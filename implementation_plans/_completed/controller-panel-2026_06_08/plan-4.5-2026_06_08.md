# Plan 4.5 — Phase 2 rework: token auth + plain HTTP + two ports (replaces mTLS)

Parent: [plan-4-2026_06_08.md](plan-4-2026_06_08.md). Owner-directed revision (2026-06-08) of the
merged 4.2/4.3 transport+auth model. The frozen design spike's "per-node mTLS + forced TLS 1.3" is
**withdrawn**: mTLS is operationally heavy (cert tuning, ephemeral-CA re-enroll-on-restart) and not a
must. New model: **per-node bearer tokens + plain HTTP by default + two ports**; TLS is delegated to a
reverse proxy (nginx/caddy), never forced in-app.

This is ONE atomic PR (PR-A) across controller + api + agent + specs — splitting it would require
throwaway shims (forbidden). The frontend panel is PR-B ([plan-4.4](plan-4.4-2026_06_08.md), revised).

## What is PRESERVED (do not touch the behaviour)

Zero-knowledge custody (node WG private keys stay on machines; controller holds public keys only); the
single-use enrollment token (`NewEnrollmentToken`/`CreateEnrollmentToken`/`ConsumeEnrollmentToken`,
atomic burn); render-what's-ready compile/stage (`CompileAndStage`); tenant-scoped persistence + the
cross-tenant CI gate; bundle Ed25519 signing; the agent's verify (`VerifyBundle`) + `install.sh` apply;
the air-gap path (`/api/compile|export|deploy-script|validate|health`) stays **open & unauthenticated**
(Mode A "download install script" must keep working).

## The contract (exact — all five partitions implement to THIS)

### Store (`internal/controller/store.go` + memstore + filestore)
- `Node`: replace `MTLSCertFP string` → `APITokenHash string` (hex SHA-256 of the node's bearer token;
  empty while pending; never plaintext).
- New tenant-scoped methods (reuse `ErrNotFound`/`ErrTokenInvalid`; no new error type):
  - `IssueNodeAPIToken(ctx, t TenantID, nodeID, tokenHash string) error` — stamps `APITokenHash` on the
    node **and** writes the reverse index `hash→nodeID`; `ErrNotFound` if the node is absent.
  - `LookupNodeByAPIToken(ctx, t TenantID, tokenHash string) (Node, error)` — resolves a presented
    token's hash to its `Node`; `ErrTokenInvalid` if unmapped **or** the node is `NodeRevoked`.
  - `RevokeNodeAPIToken(ctx, t TenantID, nodeID string) error` — clears `APITokenHash` + deletes the
    index entry (immediate revocation).
- MemStore: `apiTokens map[string]string` (hash→nodeID) in `tenantState`, deep-copied, under `s.mu`.
- FileStore: `apitokens/<hash>.json` reverse index (sanitizeComponent + writeJSONAtomic, 0600); add
  `apitokens` to `ensureTenantDir`.
- Tests: `store_compat_test.go` Node literal swap + `TestStoreAPITokens`; `tenant_isolation_test.go`
  API-token cross-tenant block (issue under A → lookup under B = `ErrTokenInvalid`, under A = ok).

### Enrollment (`internal/controller/enrollment.go` + test)
- **Remove** `DevCA`, `NewDevCA`, `IssueClientCert`, `IssueServerCert`, `ServerTLSConfig`,
  `CACertPool`, `CACertPEM`, `randomSerial`, `serialBits`, the cert/TLS imports, and the whole
  `enrollment_test.go` CA/CSR test surface (`newCSR`, `verifiesToCA`, `TestDevCAIssueAndVerify`).
- **Keep** `HashToken`, `enrollTokenBytes`, `NewEnrollmentToken`, the `Enroll` ceremony skeleton
  (burn → register WG pubkey → audit), `EnrollmentToken`.
- **Add** `NewNodeAPIToken(now) (plaintext, hash string)` (mirror `NewEnrollmentToken`: 32-byte
  crypto/rand, base64url, `HashToken`).
- `EnrollRequest`: `{Token, NodeID, WGPublicKey}` (drop `CSRDER`).
- `EnrollResult`: `{NodeID, APIToken string}` (drop `ClientCertPEM/CACertPEM/Fingerprint`).
- `Enroll(ctx, store Store, t TenantID, req EnrollRequest, now time.Time) (EnrollResult, error)` (drop
  the `*DevCA` param): `ConsumeEnrollmentToken` (burn-first) → `NewNodeAPIToken` → `UpsertNode`
  (WGPublicKey, `NodeApproved`) → `IssueNodeAPIToken(hash)` → `AppendAudit("enroll")` → return plaintext
  once. Keep the reserved-operator-name guard at the HTTP layer.

### HTTP / auth / transport / two ports (`internal/api/*` + `cmd/server`)
- **auth_controller.go** (token chokepoint, no mTLS): `bearerToken(r)`; `authenticateNode(r)` →
  `LookupNodeByAPIToken(HashToken(token))` → node identity (404/`ErrTokenInvalid` → 401; `NodeRevoked`
  → 403); `requireNode` wraps agent routes; `operatorAuth` (replaces `requireOperator`) → constant-time
  (`crypto/subtle`) compare of `HashToken(presented)` vs `ControllerHandler.operatorTokenHash` → 401/403.
  Node acts only as itself (identity from the token, never a URL/body field). Rewrite the file header.
- **handler_controller.go**: `ControllerHandler{store, tenant, operatorTokenHash, operatorName}` (drop
  `ca`). `NewControllerHandler(store, tenant, operatorTokenHash, operatorName)`. Split `Routes` into
  `RegisterAgentRoutes(mux)` (`/enroll` no-auth, `config|poll|report` `requireNode`) and
  `RegisterOperatorRoutes(mux)` (`update-topology|stage|promote|nodes|audit|topology|enrollment-token`
  `operatorAuth`). `HandleEnroll` returns `{api_token}` (was cert). Wire JSON:
  `enrollRequestJSON{enrollment_token,node_id,wg_public_key}`, `enrollResponseJSON{api_token,node_id}`.
- **server.go**: add `agentMux`; `EnableController(ch)` → `ch.RegisterOperatorRoutes(s.mux)` +
  `ch.RegisterAgentRoutes(s.agentMux)`; drop `controllerTLS`/`ListenAndServeTLS`; add
  `ListenAndServeAgent(addr)` (agent mux, ~90s WriteTimeout for `/poll`); `Handler()`/`AgentHandler()`
  for tests. Both plain HTTP. Air-gap routes stay open on `s.mux`.
- **cmd/server/main.go**: controller mode gate stays (`YAOG_CONTROLLER_STATE_DIR` + `YAOG_TENANT_ID`) +
  **require** `YAOG_CONTROLLER_OPERATOR_TOKEN` (fail to start if controller mode on and it's unset);
  ports `-addr` (operator/panel, default `:8080`) + `-agent-addr` / `YAOG_CONTROLLER_AGENT_ADDR`
  (default `:9090`); serve both concurrently via an error channel; both plain HTTP.
- **Optional secret path prefix** (`YAOG_CONTROLLER_PATH_PREFIX`, default empty): when set, ALL controller
  routes (both ports — operator AND agent `/api/v1/controller/...`) mount under `/<prefix>/api/v1/...`
  instead of `/api/v1/...`, so the panel/agent entry is unguessable to drive-by scanners. Normalize to a
  single leading `/` and no trailing `/`; empty = today's behaviour. The prefix lives in the **base URL**
  the operator distributes (agent `--controller` and the panel's controller address include it), so the
  agent/panel client code needs NO change — only the server mounts under it. The air-gap endpoints are
  NOT prefixed (Mode A stays at its known open paths). Honest framing: this is **obscurity, not a
  security boundary** (a long path can leak via logs/referrers) — defense-in-depth atop the tokens +
  proxy-TLS + the off-host signature, never a substitute for them.
- Tests `controller_http_test.go`: rebuild on `httptest.NewServer` (plain) for the two muxes + bearer
  tokens — enroll (no auth) → api_token; node bearer config/poll/report; operator-token operator routes;
  wrong/absent token → 401/403; node token never works on operator routes; cross-node still isolated.

### Agent (`internal/agent/controller_client.go` + `cmd/agent` + test)
- `ControllerClient{baseURL, nodeToken, httpClient, pollClient, lastFetchedGen, priorGen}`;
  `NewControllerClient(baseURL, nodeToken string)` (plain net/http; no tls/CA/cert).
- `Enroll(enrollmentToken, nodeID, wgPub) (*EnrollResult, error)` → POST `/enroll` (no auth), returns
  `{APIToken}`. `Fetch`/`Poll`/`Report` set `Authorization: Bearer <nodeToken>` (guard `nodeToken!=""`);
  keep all status/body/base64/`lastFetchedGen`/report-on-success logic.
- `cmd/agent`: `enroll --controller <agent-url> --node-id --token [--token-out
  /etc/wireguard/agent-controller.token]` → EnsureKey → Enroll → write token 0600. `run --controller
  <agent-url> --token <path> [--pubkey] [--after]` → load token → `NewControllerClient` → poll → on
  change `agent.Run` (reuses verify + install.sh + report). Drop `--controller-ca/--mtls-*/--tenant`.

### Specs (`docs/spec/controller/*`)
Rewrite for the new model, retracting mTLS/forced-TLS/CSR/DevCA/cert-CN-identity: `controller-api.md`
(token chokepoint, two ports, plain HTTP + proxy-TLS, open air-gap), `enrollment.md` (token, not cert),
`agent.md` (bearer token, not mTLS cert), `persistence.md` (`APITokenHash`), `deploy.md` (revocation =
clear token). No change: `key-custody.md`, `signing.md`. State the honest trade-off: bearer tokens are
replayable if leaked → confidentiality relies on proxy TLS; this is the conscious v1 model.

## Transport decisions (2026-06-08, owner dialogue)

- **gRPC: declined.** It needs `google.golang.org/grpc` + protobuf (heavy new go.mod deps) — infeasible
  in this env (no Go → no `go.sum` → CI fails) and against the stdlib-minimal principle; it is also
  *less* CDN-friendly than plain HTTP/JSON (HTTP/2 trailers, browser needs grpc-web + a proxy), which
  undercuts the CDN/caddy posture. Revisit only with a Go-capable env AND a concrete need long-poll
  can't meet.
- **Timeliness via daemon long-poll, not a new transport.** Long-poll already returns within one
  round-trip of a `promote` (`WaitForGeneration` broadcasts on the generation bump). For *continuous*
  near-real-time, the agent `run` becomes a **daemon loop** (poll → on change apply+report → re-poll at
  the new watermark; back-off on transport error; keep-last-good). Add this to PR-A's agent partition
  (currently single-shot). SSE is the noted stdlib upgrade path if true server-push is ever needed.
- **CDN-friendly** is a property of the plain-HTTP + secret-path design; preserve it.

## Definition of done

- [ ] CI green; no `go.mod` change; no mTLS/TLS/x509 left in the controller path; air-gap endpoints open
      & unchanged; two ports serve plain HTTP; token auth (operator + per-node) constant-time/hash-keyed;
      revocation immediate; tenant-isolation + zero-knowledge gates still pass; specs match code.

## Out of scope (Plan 5)

OIDC operator login + RBAC + per-operator audit identity (replaces the env operator token); multi-tenant
principal-derived tenant; KMS; hardware-signed membership; optional in-app TLS toggle.
