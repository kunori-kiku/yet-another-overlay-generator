# Controller Persistence (Phase 2 — the stateful, quarantined layer)

This document defines the **controller's server-side state layer**: the `Store` interface
(`internal/controller/store.go`), its two stdlib-only implementations (`MemStore`, `FileStore`), the
`TenantID` tenant-isolation chokepoint, the generation / stage→promote / long-poll primitives, and the
append-only hash-chained audit log (`internal/controller/audit.go`). It is the persistence half of the
zero-knowledge custody guarantee that [key-custody.md](key-custody.md) renders against and the durable
home for the signed bundles that [signing.md](signing.md) produces and [agent.md](agent.md) pulls.

**Scope of Phase 2 (this milestone, plan-4.1).** This is the **foundation** sub-plan: the interface,
the two implementations, the audit chain, and the perpetual tenant-isolation gate — CI-green, with **no
HTTP surface**. Enrollment-token methods extend the `Store` in plan-4.2; the per-node API-token methods
(`IssueNodeAPIToken` / `LookupNodeByAPIToken` / `RevokeNodeAPIToken`) extend it in plan-4.5; the plain-HTTP
controller wiring that consumes `WaitForGeneration` for the `/poll` long-poll endpoint and
`LookupNodeByAPIToken` for the auth chokepoint is plan-4.5; the frontend panel is plan-4.4. This document
describes the data-access contract those sub-plans build on. See
[../../design/controller-panel-design-spike-2026_06_07.md](../../design/controller-panel-design-spike-2026_06_07.md)
and [../../../implementation_plans/controller-panel-2026_06_08/plan-4-2026_06_08.md](../../../implementation_plans/controller-panel-2026_06_08/plan-4-2026_06_08.md).

## The quarantine boundary

The `internal/controller` package is **deliberately quarantined** from the pure, stateless
compiler/renderer. Those packages stay **frozen and dependency-minimal**: the compiler maps a topology
to peer configs as a pure function, the renderer maps compiled data to bundle bytes as a pure function,
and neither holds state, talks to a database, nor knows a tenant exists. **All** server-side state —
the node registry, stored topology, staged/current bundles, the generation counter, the audit log —
lives behind the `Store` interface in this package and nowhere else.

The payoff is twofold. First, the air-gap path (`cmd/compiler`, the existing HTTP API) keeps working
byte-for-byte: it never touches `internal/controller`, so the stateful layer cannot regress it. Second,
any future dependency the stateful layer needs (a database driver, a KMS client) is confined to this
one package — it can never leak into the frozen core. The controller **calls** the unchanged
`Compile`/`render.All` against a tenant's stored, public-keys-only topology; it does not reimplement
them.

## The Store interface and the `TenantID` chokepoint

`Store` (`internal/controller/store.go`) is the **single tenant-scoped data-access chokepoint** for the
controller. Every method takes `(ctx context.Context, t TenantID, …)`, and `TenantID` is the
**mandatory first predicate** on every operation — the structural mechanism by which one tenant's data
is never visible to another.

This isolation is **structural, not advisory**: there is no Store method that omits the `TenantID`, so
there is no way to read or mutate state without naming a tenant. A perpetual cross-tenant CI gate
(`tenant_isolation_test.go`) asserts the property end-to-end — it writes data under tenant A and
confirms tenant B sees none of it across every method — and never retires.

The `TenantID` derivation evolves across plans **without changing the data-access shape**:

- **Single-tenant v1 (Phase 2):** `TenantID` is a **constant**, sourced from a `YAOG_TENANT_ID`
  environment value. The predicate is wired and exercised even though it always carries the same value
   — the point is to build the chokepoint correctly *now*, so multi-tenant is a derivation change, not
  a data-layer rewrite.
- **Multi-tenant (Plan 5):** `TenantID` becomes **principal-derived** — read from the authenticated
  caller (the OIDC operator principal / a per-tenant routing key) at the HTTP middleware and threaded
  down. Only *how a `TenantID` is produced* changes; the Store contract, the method set, and the
  isolation gate are identical.

### Stored shapes (public-keys-only)

The Store holds the records defined alongside the interface:

- **`Node`** — one fleet node's registry record: `NodeID`, `WGPublicKey` (base64 WireGuard **public**
  key, bound at enrollment; empty while pending), `APITokenHash` (**hex SHA-256 of the node's bearer
  API token**, stamped at enrollment; empty while pending and after revocation; **never plaintext**),
  `Status` (`NodePending` / `NodeApproved` / `NodeRevoked`), `DesiredGeneration` / `AppliedGeneration`,
  `LastChecksum`, `LastSeen`, `EnrolledAt`. `UpsertNode` matches by `NodeID`; `GetNode` returns
  `ErrNotFound` when absent; `ListNodes` returns a stable order by `NodeID`.
- **`TopologyRecord`** — the operator's stored topology JSON for the tenant, **public-keys-only** (it
  must not carry WireGuard private keys). `PutTopology` assigns an incrementing `Version` (1, 2, 3, …)
  on each call; `GetTopology` returns the current record or `ErrNotFound`.
- **`SignedBundle`** — one node's rendered, Phase-0-signed bundle at a generation: `NodeID`,
  `Generation`, `Files` (bundle-relative path → content: `install.sh`, `wireguard/<iface>.conf`,
  `checksums.sha256`, `bundle.sig`, `signing-pubkey.pem`, `manifest.json`, …), and the `IsStaged` /
  `IsCurrent` flags.
- **`AuditEntry`** — one append-only, hash-chained audit record (see [below](#audit-hash-chain)).

## Zero-knowledge custody — public keys only

The Store is a **public-keys-only** registry. It **never** stores or returns a WireGuard **private**
key — not in a `Node`, not in a stored `TopologyRecord`, not in any `SignedBundle`. This is the
persistence half of the zero-knowledge guarantee: the agent generates and holds its own private key
([agent.md](agent.md)), the renderer produces bundles with the `PRIVATEKEY_PLACEHOLDER` in place of
each node's own private half ([key-custody.md](key-custody.md)), and the controller persists the
matching **public** key and renders against it every time. A `SignedBundle`'s `Files` therefore carry
the placeholder on each `[Interface] PrivateKey =` line; the agent splices its locally-held key in
`install.sh` after verifying the pristine, signed bundle.

Two layers keep private keys out of persistence. At the **type** level (enforced now, by this
package), `Node` exposes only `WGPublicKey` — the registry structurally cannot hold a private key. At
the **bundle** level, a `SignedBundle`'s `Files` are the renderer's output, which the standing
custody guard (`internal/render/custody_guard_test.go`) already proves carry only the placeholder, not
a real key. The controller wiring that stages bundles (plan-4.3) is responsible for staging only
renderer-produced, custody-clean bundles; the Store does not re-scan bundle bytes, so that pre-stage
discipline is the caller's contract, not a guarantee the Store enforces at write time.

At the **topology** level the contract IS now enforced at the API boundary
(controller-server-authority-redesign plan-1): `POST /update-topology` unmarshals the payload and
refuses any topology carrying a non-empty `wireguard_private_key` with a 400 before `PutTopology`
runs, pinned by the perpetual guard `internal/api/topology_custody_test.go`. A stored
`TopologyRecord` therefore cannot carry a private key via the operator API path.

## The per-node API-token index

Authenticating an agent request means resolving a presented bearer token back to its `Node`
([controller-api.md](controller-api.md) §The single auth chokepoint). The Store owns that resolution
through three tenant-scoped methods and a reverse index keyed by the token's **hash** — the plaintext
token is **never** stored (the same hash-at-rest discipline as the enrollment token):

- **`IssueNodeAPIToken(ctx, t TenantID, nodeID, tokenHash string) error`** — stamps `tokenHash` onto the
  node's `APITokenHash` **and** writes the reverse index entry `tokenHash → nodeID`, both under one lock.
  Returns `ErrNotFound` if no node with `nodeID` exists for the tenant. Called by `Enroll`
  ([enrollment.md](enrollment.md)) once the node record has been upserted.
- **`LookupNodeByAPIToken(ctx, t TenantID, tokenHash string) (Node, error)`** — resolves a presented
  token's hash to its `Node` via the reverse index. Returns `ErrTokenInvalid` if the hash is **unmapped**
  **or** the resolved node is `NodeRevoked` (a revoked node's token is dead even if the index entry has
  not yet been cleared — the status check is fail-closed). This is the lookup the HTTP auth chokepoint
  calls on every agent request.
- **`RevokeNodeAPIToken(ctx, t TenantID, nodeID string) error`** — clears the node's `APITokenHash`
  **and** deletes the reverse-index entry, so the token stops authenticating **immediately** (no TTL,
  no CRL, no propagation delay). It is **idempotent**: revoking a node that has no token (already revoked,
  or never enrolled) is a benign no-op, not an error.

No **new error type** is introduced — the methods reuse the existing `ErrNotFound` / `ErrTokenInvalid`.
The index reuses the **same tenant predicate** as every other Store method, so a token issued under
tenant A is structurally invisible to a lookup under tenant B (the perpetual `tenant_isolation_test.go`
gate covers the API-token path the same way it covers every other method: issue under A → lookup under
B returns `ErrTokenInvalid`, lookup under A returns the node).

Because the index keys on the **hash**, a read of the backing store can never recover a usable token —
only its SHA-256. The plaintext is emitted **exactly once**, in the `/enroll` response, and lives only
on the enrolling node's disk thereafter ([agent.md](agent.md) §The enroll subcommand).

## The two stdlib implementations

Phase 2 ships **two** `Store` implementations, both **stdlib only** (no new `go.mod` dependency). Each
carries a compile-time assertion that it satisfies the interface
(`var _ Store = (*MemStore)(nil)` / `(*FileStore)(nil)`).

### MemStore — in-memory (CI + the long-poll primitive)

`func NewMemStore() *MemStore` returns a fully in-memory store. It is the **CI-exercised** implementation
(deterministic, no filesystem) and the home of the **long-poll primitive**: it holds the per-tenant
generation counter and the synchronization that wakes `WaitForGeneration` waiters on promote (a
condition variable / channel broadcast guarded by the store mutex). All Phase 2 unit and integration
tests — the tenant-isolation gate, the stage/promote semantics, the audit-chain verification — run
against `MemStore`.

The per-node API-token index is a per-tenant `apiTokens map[string]string` (`tokenHash → nodeID`) held
on the same `tenantState` as the node registry and initialized in `newTenantState`. It is **deep-copied**
on read exactly like the other per-tenant maps, and every `IssueNodeAPIToken` / `LookupNodeByAPIToken` /
`RevokeNodeAPIToken` operation runs under the store mutex `s.mu`, so an issue/lookup/revoke race resolves
to a single consistent outcome.

### FileStore — durable JSON on disk (single-tenant v1)

`func NewFileStore(root string) (*FileStore, error)` returns a store that persists state as JSON under
`root`, which it creates with mode **0700**. It is the **durable** backing for a single-tenant v1
deployment: state survives a controller restart.

The per-node API-token reverse index is persisted as one small JSON file per token under an
**`apitokens/`** sub-dir of the tenant directory: `apitokens/<hash>.json` holding `{"NodeID": "<id>"}`,
where `<hash>` is the token's hex SHA-256 run through `sanitizeComponent` (the same path-safety guard the
store uses for every on-disk key) and written via `writeJSONAtomic` (temp-file-then-`rename`, **0600**).
`ensureTenantDir` creates `apitokens` alongside the other per-tenant sub-dirs at startup. `IssueNodeAPIToken`
writes the file (and stamps the node record); `RevokeNodeAPIToken` removes it (and clears the node's
`APITokenHash`); `LookupNodeByAPIToken` reads it, then loads the named node and applies the
`NodeRevoked`-is-`ErrTokenInvalid` check. Only the **hash** is ever on disk — never the plaintext token.

Durability discipline:

- **Permissions:** the root directory is **0700**; written files are **0600**. The store can hold
  signed bundles, tenant topology, and the API-token index, so it is treated as sensitive even though it
  carries no private keys (and no plaintext tokens — only their hashes).
- **Atomic per-file writes (torn-write safe, not crash-durable):** every single record is written to a
  **temporary file then `rename`d** into place, so a concurrent reader sees either the old complete
  file or the new complete file — never a half-written one. This is *torn-write* protection, not full
  *crash durability*: the temp file is not `fsync`ed before rename, so a power loss can still lose a
  just-written record that the OS had only buffered. For v1 this is acceptable (the agent re-pulls and
  re-applies; the operator can re-deploy); real crash-durability + multi-record transactionality is a
  property of the future Postgres adapter.
- **`PromoteStaged` is atomic for concurrent in-process callers, best-effort across a crash.** The flip
  writes several files in sequence (each node's current bundle, removing its staged marker, bumping its
  node record) and commits `generation.json` **last**, so an in-process caller (guarded by the store
  mutex) and any reader only ever observe a consistent generation. A crash *mid-flip* can leave a
  partially-flipped on-disk state (some nodes flipped, `generation.json` not yet bumped); because the
  generation is committed last, a retry re-promotes the still-staged remainder and converges. True
  cross-record crash-atomicity (a single transaction) is a Postgres-adapter property, deferred.
- **Long-poll:** `FileStore` satisfies the same `WaitForGeneration` **contract** as `MemStore`, but by
  a different mechanism: it **polls the persisted `generation.json` on a short interval** (no in-process
  condition variable), returning as soon as the counter advances past `afterGen` or `ctx` is done. The
  plan-4.3 `/poll` endpoint therefore behaves equivalently regardless of the backing store; the polling
  approach also generalizes naturally to cross-process once a shared store (Postgres) is used.

Both implementations satisfy the **same Store contract** — the only differences are where the bytes
live and the (documented) crash-durability and ctx-cancellation properties noted above.

## Generation, stage → promote, and the long-poll primitive

The controller's deploy workflow is built on a monotonic, per-tenant **generation** counter and a
two-step **stage → promote** that makes a fleet roll-out atomic and observable.

- **`StageBundle`** stores a node's rendered bundle as its **staged** (not-yet-current) version.
  Staging **replaces** any prior staged bundle for that node — staging is idempotent per node, so a
  re-render before promote simply overwrites.
- **`PromoteStaged`** is the atomic flip. If **no** bundles are staged it returns `ErrNoStagedBundle`.
  Otherwise it, in one atomic step: flips **all** staged bundles to current (`IsStaged=false`,
  `IsCurrent=true`, clearing the prior current for each promoted node), **increments the tenant
  generation by 1**, sets each promoted node's `DesiredGeneration` to the new generation, **wakes any
  `WaitForGeneration` waiters**, and returns the new generation. The flip is all-or-nothing: a deploy
  is never observed half-promoted.
- **`GetCurrentBundle`** returns a node's current (promoted) bundle, or `ErrNotFound`. This is what the
  plan-4.3 `/config` endpoint serves to the **caller's own** node.
- **`CurrentGeneration`** returns the tenant's generation, **0** before any promote.
- **`WaitForGeneration(ctx, t, afterGen)`** is the **long-poll primitive** for plan-4.3's `/poll`
  endpoint: it **blocks** until the tenant's generation is strictly greater than `afterGen`, then
  returns it; or returns `(0, ctx.Err())` if `ctx` is done first (the endpoint's ~55 s deadline). An
  agent polls with the generation it last applied; the call returns the moment an operator promotes,
  turning the deploy into a near-instant push without holding a persistent connection open server-side
  beyond the poll window.

Stage → promote also gives Plan 5 its hook for **out-of-band approval + instant rollback**: approval
gates the promote step, and rollback is re-promoting a prior bundle set — neither changes this Store
contract. (Partial-fleet deploy — *render what's ready* — is handled **above** the Store: the
controller filters the topology to the enrolled subgraph before calling the unchanged
`Compile`/`render.All`, then stages the resulting per-node bundles; the Store stores whatever bundles it
is handed.)

## Audit hash chain

`AppendAudit` records an append-only `AuditEntry` per state-changing action (topology updated, bundle
staged, promoted, node enrolled/revoked, …). Each implementation **must** chain entries via
`chainAudit` (`internal/controller/audit.go`):

- assign **`Seq`** monotonically **per tenant**;
- set the entry's **`Timestamp`** from the caller-provided value;
- set **`PrevHash`** to the tenant's **prior** entry `Hash` (empty for the first entry);
- compute **`Hash`** = `hex(SHA256(canonical(entry incl. PrevHash)))` over the fixed canonical encoding
  (`Seq`, RFC3339Nano/UTC `Timestamp`, `Actor`, `Action`, `NodeID`, `PrevHash` — every field except the
  `Hash` itself), so the digest is deterministic across processes and both Store implementations.

`ListAudit` returns a tenant's entries in `Seq` order. `VerifyAuditChain([]AuditEntry) int` reports the
index of the first entry that breaks the chain (a `PrevHash` mismatch or a `Hash` that does not
recompute), or `-1` if the chain is intact.

### Honest limitation — tamper-EVIDENT only

The chain is **tamper-evident for operational visibility, not a cryptographic anti-tamper guarantee**:

> An actor with **write access to the backing store** can recompute every `Hash` from the entry it
> rewrote forward, producing a fresh, internally-consistent chain. So `VerifyAuditChain` catches an
> *accidental* corruption or a *partial/naive* edit, and gives operators a visible integrity signal —
> but it does **not** stop a determined attacker who already owns the store.

This matches the agent's anti-rollback honesty framing ([agent.md](agent.md)): Phase 2 establishes the
chain *mechanism* and its on-disk shape so that **real** anti-tamper — an external, append-only or
externally-witnessed audit sink whose integrity does not depend on the writer's honesty — is a **Plan
5** hardening that builds on this contract rather than reinventing it.

## Postgres adapter (future)

A **Postgres-backed `Store`** is a **documented future implementation**, not part of Phase 2. It would
live in `internal/controller` alongside `MemStore`/`FileStore` (the quarantine boundary keeps any
driver dependency confined to this package) and satisfy the same `Store` interface — a **drop-in swap**,
since the interface is the only seam the rest of the controller knows.

It is **not added now** for a concrete environment reason: Go is not installed in this development
environment, so a new module dependency's `go.sum` entries cannot be generated, and adding a Postgres
driver to `go.mod` would break CI. The Postgres adapter is therefore deferred to a later PR — when the
driver can be properly vendored — and the stdlib `FileStore` is the appropriate durable backing for a
single-tenant v1 (Postgres is a scale / multi-tenant concern, aligned with Plan 5).

The intended adapter would use Go's stdlib **`database/sql`** with a **`pgx`** driver (`pgx/stdlib`),
keeping the rest of the controller driver-agnostic. A brief schema sketch — every table keyed by
`tenant_id`, mirroring the stored shapes above:

| Table            | Key columns                                | Notes                                                        |
| ---------------- | ------------------------------------------ | ----------------------------------------------------------- |
| `nodes`          | `(tenant_id, node_id)`                      | WG **public** key, API-token **hash**, status, desired/applied gen, last_seen, enrolled_at — **never** a private key or a plaintext token |
| `api_tokens`     | `(tenant_id, token_hash)`                   | reverse index `token_hash → node_id`; hash only, never plaintext |
| `topologies`     | `(tenant_id, version)`                      | public-keys-only JSON; `version` increments per `PutTopology` |
| `signed_bundles` | `(tenant_id, node_id, generation)`          | bundle files, `is_staged` / `is_current` flags               |
| `audit_log`      | `(tenant_id, seq)`                          | append-only; `prev_hash` / `hash` chain, timestamp, actor, action, node_id |

`tenant_id` as the leading column of every primary key makes the tenant predicate a **structural**
property of the schema itself (and the place a database-level row-security policy would attach in Plan
5), exactly mirroring the in-code `TenantID` chokepoint. The generation counter and the long-poll wake
become a transaction + `LISTEN/NOTIFY` (or a polled `CurrentGeneration`) in the Postgres adapter,
preserving the `WaitForGeneration` contract across processes.
