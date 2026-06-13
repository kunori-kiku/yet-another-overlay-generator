# Controller agent API

<!-- last-verified: 2026-06-13 -->

> **controller-server-authority-redesign (plan-6):** enrollment and rekey now enforce
> the identity invariant — one APPROVED WireGuard public key binds to one node-id.
> `Enroll` and `controller.Rekey` both call the shared `CheckWGKeyUnique` under the
> per-tenant op lock (`tenantlock.go`, also taken by stage/promote), so a pubkey already
> approved under a DIFFERENT node-id is refused: `ErrDuplicateWGKey` → 409 at `/enroll`
> and `/rekey`. Same-id re-enroll (reinstalled host, fresh token) is allowed; a revoked
> node frees its key. The refusal is audited (`enroll-rejected-duplicate-key`).

## Responsibility
Serve the agent-facing HTTP surface of the networked controller — enroll, config fetch, generation long-poll, apply report, rekey re-registration, and the unauthenticated one-line bootstrap installer — on a dedicated agent mux/port.

## Files
- `internal/api/handler_controller.go:151-167` — `RegisterAgentRoutes`: mounts `/enroll`, `/config`, `/poll`, `/report`, `/rekey` (+ `/bootstrap`) under `basePath()` (lines 231-235); agent handlers at 446-487 (`HandleEnroll`), 492-556 (`HandleConfig`), 563-600 (`HandlePoll`), 605-641 (`HandleReport`), 1026-1073 (`HandleRekey`); helpers `identity` 1325-1332, `decodeJSON` 1336-1345, `parseAfter` 1383-1392. Operator handlers in the same file belong to "see specs/controller-operator-api.md".
- `internal/api/handler_bootstrap.go:130-161` — `HandleBootstrap`: serves the bash install+enroll+apply script (no auth); `renderBootstrapScript` 197-217 injects shell-quoted (`shQuote`, 191-193) server defaults ahead of the static body `bootstrapScriptBody` 223-322; settings read via `loadSettings` 38-47 (operator-facing `HandleSettings` 52-123 writes them; see specs/controller-operator-api.md).
- `internal/controller/enrollment.go:46-49,65-78,97-105,144-192` — `HashToken`, `NewEnrollmentToken(nodeID, ttl, now) (plaintext, tok)`, `NewNodeAPIToken(now) (plaintext, hash)`, and `Enroll(ctx, store, t, EnrollRequest, now) (EnrollResult, error)` — the burn-token → mint-bearer → register-node ceremony.
- `internal/api/auth_controller.go:79-139` — shared chokepoint used here: `bearerToken` (79-94), `authenticateNode` (110-122, hash-vs-hash lookup via `Store.LookupNodeByAPIToken`), `requireNode` (128-139, pins tenant+node onto the request context).
- `internal/api/server.go:48-51,169-189` — `EnableController` registers agent routes on the separate `agentMux`; `ListenAndServeAgent` serves it as plain HTTP with a 90s WriteTimeout sized for the long-poll.

## Inputs
- The agent binary (see specs/agent.md) sends JSON over plain HTTP to the agent port: `POST /enroll` `{enrollment_token, node_id, wg_public_key}` (`enrollRequestJSON`, handler_controller.go:275-279), `GET /config`, `GET /poll?after=N`, `POST /report` `{applied_generation, checksum, health}` (308-312), `POST /rekey` `{wg_public_key}` (402-404); plus `GET /bootstrap` from `curl` on a fresh node.
- A single-use enrollment-token plaintext, minted out-of-band by the operator route `POST /enrollment-token` (see specs/controller-operator-api.md) via `controller.NewEnrollmentToken` (enrollment.go:65-78).
- `controller.Store` (see specs/controller-store.md): `ConsumeEnrollmentToken`, `LookupNodeByAPIToken`, `GetCurrentBundle`, `WaitForGeneration`, `GetNode`/`UpsertNode`, `SetAppliedGeneration`, `TouchLastSeen`, `AppendAudit`, `GetSettings`, `GetOperatorCredential`, `GetCurrentSignedTrustList`.
- Operator-saved settings (`PublicAgentURL`, `GithubProxy`, `AgentReleaseBaseURL`) feed the bootstrap script (handler_bootstrap.go:135-157).

## Outputs
- `enrollResponseJSON {api_token, node_id}` (handler_controller.go:283-286) — the per-node bearer plaintext, returned exactly once; thereafter only its SHA-256 exists server-side.
- `configResponseJSON {generation, files (base64 by bundle-relative path), rekey_requested}` (290-298) — bundles produced by stage/promote (see specs/controller-stage-promote.md); when keystone is on, `trustlist.json` + `trustlist.sig` are appended to the served file map (538-549; see specs/keystone-trustlist.md).
- `pollResponseJSON {generation}` on advance, or bare 204 on the ~55s deadline (563-600; `defaultPollDeadline` 49).
- Store mutations: enroll registers the node `NodeApproved` + issues the token reverse-index (enrollment.go:159-175); report stamps `SetAppliedGeneration` + `TouchLastSeen` + audit (handler_controller.go:621-639); rekey swaps `WGPublicKey` and clears `RekeyRequested` (1048-1061). Audit actors are `"agent:<node>"`.
- The bootstrap bash script (`text/x-shellscript`, handler_bootstrap.go:157-160): downloads the per-arch `yaog-agent` (GitHub proxy applied, 270-278), runs `yaog-agent enroll`, then installs a systemd daemon (default) or applies once with `--once` (296-321).

## Decision points (if any)
- **Enroll error mapping** (handler_controller.go:470-480): `ErrTokenInvalid`/`ErrTokenConsumed` → 401 (authorization failure); anything else → 400. A `node_id` equal to the operator name is 403 — the operator identity is reserved (460-462).
- **Config keystone branch** (538-549): operator credential pinned ⇒ a signed manifest MUST exist to serve (else fail-closed 500); genuine `ErrNotFound` on the credential ⇒ keystone off, plain bundle. 404 before the node's first promote (505-508).
- **Poll** (573-599): malformed/negative/overflowing `?after=` → 400 (`parseAfter`, 1383-1392); generation advance → 200; deadline/cancel → 204 so the agent re-polls on a fresh connection.
- **Rekey** (1041-1044): empty `wg_public_key` → 400; the target node is always the bearer token's node, never a body field.
- **Bootstrap keystone bake** (handler_bootstrap.go:149-155): only a genuine `ErrNotFound` emits a keystone-OFF script; any other store error is a loud 500 (a silently keystone-OFF script would ship a non-verifying node).
- **Controller base composition** (handler_bootstrap.go:140-144): `controllerBase = TrimRight(PublicAgentURL,"/") + agentPrefix` — the AGENT prefix (`YAOG_AGENT_PATH_PREFIX`), never the operator one; the agent appends `/api/v1/controller/` itself. The prefix is normalized by `SetAgentPathPrefix` (handler_controller.go).

## Invariants
- **Burn-before-mint, never un-burned**: `Enroll` atomically consumes the enrollment token as step 1; a failure in any later step does NOT restore it — the operator mints a fresh token to retry (enrollment.go:137-148). Single-use is the protected property.
- **Hash-only token storage**: enrollment and per-node API tokens are 32 bytes of `crypto/rand`, base64url on the wire, stored only as hex SHA-256; every auth comparison is hash-vs-hash (enrollment.go:38-49; auth_controller.go:115). Plaintext crosses the wire exactly once.
- **A node acts only as itself, and only with public keys**: agent handlers read tenant+node from the context pinned by `requireNode` — never from URL/body (auth_controller.go:124-139) — and the wire carries only `wg_public_key` fields, upholding the zero-knowledge key-custody principle (PRINCIPLES.md:43-47; docs/spec/controller/key-custody.md).

## Gotchas (optional)
- `/enroll` and `/bootstrap` are deliberately unauthenticated on the agent port (handler_controller.go:159,166); `/bootstrap` discloses settings URLs and the pinned operator credential (public material) to anyone reaching that port. The secret path prefix is obscurity, explicitly NOT a security boundary (handler_controller.go:76-82).
- `wg_public_key` is stored verbatim — no format validation that it is a real Curve25519 key, and `/enroll` accepts an empty one ("registered as-is", enrollment.go:14-17,159-165); only `/rekey` rejects empty (handler_controller.go:1041-1044). docs/spec/controller/persistence.md's public-keys-only claim is likewise a convention, not code-enforced.
- `TouchLastSeen` failures on `/config` and `/poll` are intentionally swallowed (handler_controller.go:514,580) — a check-in stamp must never deny a node its config. Revoked nodes get an indistinguishable opaque 401 (auth_controller.go:104-109), so an evicted agent cannot tell revocation from a bad token.

Deep docs: docs/spec/controller/enrollment.md, docs/spec/controller/bootstrap.md, docs/spec/controller/controller-api.md (verified against code above).
