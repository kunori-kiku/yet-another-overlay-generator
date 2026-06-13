# Controller store (persistence)

<!-- last-verified: 2026-06-12 -->

## Responsibility
Persist all tenant-scoped controller state — node registry, topology versions, staged/current signed bundles, generation counter, enrollment tokens, node API tokens, operator accounts/sessions/login-challenges, keystone credential + signed trust-list, settings, and the hash-chained audit log — behind one swappable `Store` interface with two stdlib-only implementations.

## Files
- `internal/controller/store.go:1-467` — `Store` interface (`internal/controller/store.go:294-467`), `TenantID` (`store.go:30`), sentinel errors (`store.go:34-49`), and every persisted record type: `Node` (`store.go:65-93`), `TopologyRecord` (`store.go:98-102`), `SignedBundle` (`store.go:107-114`), `AuditEntry` (`store.go:121-129`), `OperatorCredential` (`store.go:139-145`), `StoredTrustList` (`store.go:153-157`), `EnrollmentToken` (`store.go:162-168`), `Operator`/`LoginCredential` (`store.go:175-217`), `LoginChallenge` (`store.go:234-238`), `Session` (`store.go:246-251`), `ControllerSettings` (`store.go:257-278`).
- `internal/controller/filestore.go:1-1371` — `FileStore`, JSON-on-disk impl; on-disk layout doc (`filestore.go:24-42`), path sanitization (`filestore.go:69-81`), atomic temp-file+rename writes (`filestore.go:183-197`), per-tenant 0700 dirs (`filestore.go:94-105`), 200 ms-poll `WaitForGeneration` (`filestore.go:662-690`).
- `internal/controller/memstore.go:1-741` — `MemStore`, in-memory reference impl; per-tenant state map (`memstore.go:15-76`), deep-copy-on-store-and-return discipline (`memstore.go:115-142`, `376-395`, `578-587`), `sync.Cond`-based `WaitForGeneration` with a ctx-watcher goroutine (`memstore.go:328-370`).
- `internal/controller/audit.go:1-54` — shared hash-chain helpers: `canonicalAuditBytes` (`audit.go:14-23`), `chainAudit` (`audit.go:29-34`), `VerifyAuditChain` (`audit.go:42-54`).

## Inputs
- `cmd/server/main.go:124` constructs `NewFileStore(stateDir)` (`filestore.go:53-61`) and injects it into the HTTP layer; `cmd/server/operator.go:52` opens the same root for the `create-operator` CLI.
- **controller-operator-api** (see specs/controller-operator-api.md) — `NewControllerHandler(store controller.Store, …)` at `internal/api/handler_controller.go:101`; operator routes call `PutTopology`, `UpsertNode`, `CreateEnrollmentToken`, operator/session/challenge methods, `SetOperatorCredential`, `Get/PutSettings`.
- **controller-stage-promote** (see specs/controller-stage-promote.md) — `CompileAndStage`/`PromoteStaged` in `internal/controller/compile.go:140,364` drive `GetTopology`, `StageBundle`, `Store.PromoteStaged`, `PutSignedTrustList`, `AppendAudit`.
- **controller-agent-api** (see specs/controller-agent-api.md) — agent routes call `ConsumeEnrollmentToken`, `IssueNodeAPIToken`, `LookupNodeByAPIToken`, `SetAppliedGeneration`, `TouchLastSeen`, `WaitForGeneration`.

## Outputs
- `Node`, `SignedBundle`, `TopologyRecord`, `Session`, `Operator` values returned to the API layers above; `GetCurrentBundle` (`store.go:330`) is what the agent ultimately downloads (see specs/agent.md).
- `OperatorCredential` / `StoredTrustList` reads feed keystone verification and bundle embedding (see specs/keystone-trustlist.md): `GetOperatorCredential`/`GetCurrentSignedTrustList` (`store.go:412-421`).
- `WaitForGeneration(ctx, t, afterGen) (int64, error)` (`store.go:345`) is the long-poll primitive behind the agent `/poll` endpoint.
- On disk (FileStore): `<root>/<tenant>/{nodes,bundles,tokens,login-challenges,apitokens,operators,sessions}/*.json` plus `topology.json`, `generation.json`, `audit.json`, `operator_credential.json`, `signed_trustlist.json`, `settings.json` (`filestore.go:24-42,99`).

## Decision points
- **Promote vs. bump.** `PromoteStaged` flips staged→current, bumps generation, stamps `DesiredGeneration` on registered nodes, wakes waiters (`memstore.go:262-287`, `filestore.go:491-584`); `BumpGeneration` advances the counter only — a wake signal (e.g. fleet rekey), not a deploy (`store.go:333-341`, `memstore.go:314-322`, `filestore.go:634-654`).
- **Single-use enforcement differs by record.** Enrollment tokens are burned by setting `ConsumedAt` (replay → `ErrTokenConsumed`, `filestore.go:735-766`); login challenges are burned by DELETION (replay finds nothing → `ErrChallengeInvalid`; expired records are lazily GC'd, wrong-operator records left intact) (`store.go:366-374`, `memstore.go:447-464`, `filestore.go:795-825`).
- **API-token lookups are self-consistent.** `LookupNodeByAPIToken` rejects unless the reverse index resolves to a node whose own `APITokenHash` still matches AND `Status == NodeApproved` (`memstore.go:498-511`, `filestore.go:888-925`); rotation deletes the old index entry first (`filestore.go:865-873`).
- **Missing record → `ErrNotFound`** (`store.go:36`); expired/unknown bearer → `ErrTokenInvalid`; `LookupSession` lazily deletes an expired session (`filestore.go:1241-1246`, `memstore.go:675-678`).
- **TOTP replay watermark** advances only if `step >` stored value, as one atomic check-and-set (`store.go:439-445`, `filestore.go:1163-1193`).

## Invariants
- **Tenant isolation chokepoint:** every `Store` method takes `TenantID` as a mandatory predicate (`store.go:25-30`); FileStore additionally sanitizes tenant/node/hash path components against traversal (`filestore.go:69-81`). Enforced by `internal/controller/tenant_isolation_test.go`.
- **Hash-not-plaintext for every bearer secret:** enrollment tokens, node API tokens, sessions, and login challenges store only hex SHA-256 hashes; passwords only argon2id PHC strings (`store.go:71-74,160-167,172-174,219-227,246-251`). Cross-ref PRINCIPLES.md "Key custody": the Store never stores or returns a WireGuard private key (`store.go:285`) — but see Gotchas on enforcement.
- **Crash-safe writes, behavior parity:** FileStore writes every file via temp-file+rename (`filestore.go:183-197`), commits `generation.json` last in promote so waiters never observe the new generation before its bundles (`filestore.go:578-583`); a shared compat test (`internal/controller/store_compat_test.go`) holds both impls to identical semantics.

## Gotchas
- **`PutTopology` stores bytes verbatim — the custody gate is at the API boundary, not in the store.** Both impls defensively copy the caller's JSON and persist it untouched. The "public-keys-only" guarantee is now ENFORCED (no longer just a caller contract): `POST /update-topology` unmarshals the payload, rejects any non-empty `wireguard_private_key` with 400, and stores the canonical re-marshaled bytes (perpetual gate `internal/api/topology_custody_test.go`), and the panel strips/​placeholders client-side — so a `TopologyRecord` cannot carry a private key via the operator API. The store method itself stays gate-free (a second writer, e.g. a future CLI importer, would need its own gate); the earlier "doc drift" is closed at the boundary every real writer goes through. **Bounded version history (plan-2):** each put is retained (last `TopologyHistoryLimit`=10) via `ListTopologyVersions`/`GetTopologyVersion`; only committed versions are visible (a crash orphan is skipped, the current record always lists, a corrupt entry never bricks the list).
- **The audit chain is tamper-EVIDENT only.** Anyone with write access to the backing store can recompute every hash; `VerifyAuditChain` returns the first broken index or -1 (`store.go:117-120`, `audit.go:36-54`).
- **Concurrency model is single-process.** FileStore serializes via one in-process mutex; atomic renames give crash durability, not cross-process arbitration (`filestore.go:19-22`). MemStore's `cond.Broadcast` wakes ALL tenants' waiters, relying on predicate recheck (`memstore.go:284-285,319-320`); FileStore's `WaitForGeneration` returns already-available data even when ctx is already done (`filestore.go:659-670`). `DeleteOperator` does NOT cascade sessions — immediate lockout requires deleting them too (`store.go:434-437`).

## Deep docs
docs/spec/controller/persistence.md (layout + future Postgres mapping; verify the public-keys-only claim against code, per above), docs/spec/controller/key-custody.md, docs/spec/controller/operator-auth.md.
