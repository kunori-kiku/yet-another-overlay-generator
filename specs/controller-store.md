# Controller store (persistence)

<!-- last-verified: 2026-07-16 -->

## Responsibility

Persist every tenant-scoped controller record behind one `Store` contract while keeping storage
mechanics separate from business rules. The production `FileStore` and in-memory `MemStore` both
embed the same `storeCore`; only the small record-KV backend, generation wake mechanism, and audit-log
storage differ. This prevents the test implementation and the shipping implementation from acquiring
different custody, concurrency, or migration behavior.

The store owns controller state for the node registry, topology history, staged/current bundles,
generation counter, enrollment and node API tokens, WebAuthn assertion challenges, operator accounts,
login-passkey fields, sessions, keystone public credential, its pending audited-transition marker,
staged/served trust lists, controller settings, bundle-signing anchor, bounded audit log, and resource
telemetry history. It never accepts or persists a WireGuard private key.

## Structure

```text
Store interface + record types (store.go)
              |
        shared storeCore
      /                   \
  memkv                    filekv
  maps + sync.Cond         JSON files + 200 ms generation poll
      \                   /
       MemStore / FileStore

volatile telemetry overlay + bounded resource-history buffer live beside storeCore,
not as duplicated backend business logic
```

## Files

- `internal/controller/store.go:1-754` defines `TenantID`, public record types, public sentinel
  errors, and the complete `Store` interface (`store.go:492-754`). `AssertionChallenge` is the shared
  login/enrollment-proof nonce record (`store.go:355-376`); credential compare-and-set methods are
  part of the public contract (`store.go:672-676,706-710`).
- `internal/controller/kv.go:1-106` is the deliberately small storage port. Its frozen collection
  names preserve the existing FileStore layout (`kv.go:43-63`), and its locking contract distinguishes
  in-lock record primitives from the self-synchronizing heartbeat existence probe and audit hooks
  (`kv.go:10-24,65-105`).
- `internal/controller/storecore.go:1-1215` is the single behavioral implementation. Every
  multi-record custody transition runs under `kvBackend.withLock`; examples include assertion-challenge replacement/consumption (`storecore.go:653-718`),
  keystone compare-and-set (`storecore.go:823-858`), the atomic served-config read
  (`storecore.go:916-960`), login-credential compare-and-set (`storecore.go:982-1007`), and the TOTP
  replay watermark (`storecore.go:1061-1087`).
- `internal/controller/storecore_stage.go` owns incremental staging compatibility,
  `ReplaceStagedSet`, exact-seal validation, promotion, and staged trust-list history. Keeping the
  invalidate/components/seal-last protocol in one file makes its crash ordering reviewable.
- `internal/controller/keystone_transition.go` owns the tenant-serialized, write-ahead recovery
  protocol that couples a keystone credential CAS to exactly one audit event. The Store core only
  persists the marker and the conditional credential write; transition policy remains above the raw
  record layer.
- `internal/controller/storecore_telemetry.go:1-199` keeps high-frequency live telemetry in a
  separately locked overlay. Fleet reads merge it onto durable node records, while custody
  read-modify-write callers use `GetNodeRecord` and cannot accidentally bake transient health into
  persistence (`store.go:501-514`).
- `internal/controller/memstore.go:1-237` provides the byte-copying in-memory KV, `sync.Cond`
  generation wake, and in-memory audit backing; `MemStore` itself is only a thin `storeCore` wrapper
  (`memstore.go:223-237`).
- `internal/controller/filestore.go:1-354` provides the JSON-on-disk KV, durable
  temp-file+fsync+rename writes, frozen path mapping, 200 ms generation polling, and the thin
  `FileStore` wrapper (`filestore.go:314-354`).
- `internal/controller/telemetry_history.go:1-464` buffers resource samples away from the heartbeat
  custody lock and, for FileStore, flushes append-only per-node JSONL on a background interval. The
  operator-configured cap is cached so a heartbeat never reads from disk (`telemetry_history.go:79-178`).
- `internal/controller/audit.go:1-59` supplies the shared hash-chain and rotation bounds; backend code
  only chooses slice versus JSONL storage.

## Inputs and consumers

- `cmd/server/main.go` constructs `NewFileStore(stateDir)`, starts its telemetry-history flusher, and
  injects it into the controller HTTP layer. `yaog-server create-operator` opens the same state root.
- Operator handlers use topology, registry, keystone, enrollment-token, account, session, assertion
  challenge, and settings methods. See `specs/controller-operator-api.md`.
- Agent handlers consume enrollment tokens, maintain node API tokens, record reports/heartbeats, and
  read or long-poll served configuration. See `specs/controller-agent-api.md` and `specs/agent.md`.
- Compile/stage/promote logic uses the topology, bundle, signed-trust-list, signing-anchor, generation,
  and audit methods. See `specs/controller-stage-promote.md` and `specs/keystone-trustlist.md`.
- The panel receives DTOs derived from `Node`, `TopologyRecord`, `Session`, settings, and the keystone
  public descriptors; secrets are represented only by one-way hashes where the store needs a lookup.

## Persisted FileStore shape

The layout in `filestore.go:29-49` is backward-compatible state and must not be renamed casually:

```text
<root>/<tenant>/
  nodes/<node>.json
  topology.json
  topology-history/<version>.json
  bundles/<node>.{staged,current}.json
  tokens/<hash>.json
  login-challenges/<hash>.json       # AssertionChallenge; directory name is historical
  apitokens/<hash>.json
  operators/<username>.json
  sessions/<hash>.json
  generation.json
  audit.jsonl
  operator_credential.json
  keystone-transition.json           # pending credential-CAS/audit recovery marker
  signed_trustlist.json              # staged slot; filename is historical
  trustlist-history.json             # epoch base only; never served/promoted
  staged-set.json                    # exact candidate seal, or non-promotable history marker
  served_trustlist.json
  settings.json
  signing-anchor.json

<root>/telemetry-history/<tenant>/<node>.jsonl
```

Tenant directories are mode `0700`; state files are mode `0600`. Path components are sanitized before
they become filenames (`filestore.go:103-117`; `internal/controller/filestore_io.go:21-59`).

## Decision points

- **One behavior core, two storage adapters.** A backend implements raw byte CRUD and a few
  backend-specific wake/log hooks; it does not decide token validity, credential transitions,
  promotion semantics, or public errors. `var _ Store = (*storeCore)(nil)` keeps the shared core
  compile-time complete (`storecore.go:40-46`).
- **Atomic multi-record transitions use the backend lock and a durable stage commit marker.**
  `ReplaceStagedSet` invalidates `staged-set.json`, replaces/prunes all candidate records, and writes
  the exact generation/node-set/manifest seal last. A partial FileStore candidate is therefore inert
  after reopen. `PromoteStaged` revalidates that seal, flips eligible bundles,
  updates desired generations, copies a signed staged trust list into the served slot, and writes the
  generation counter last. `GetServedConfig` reads its bundle/keystone/served-manifest tuple under the
  same lock. The counter is therefore the commit/wake point, not merely another file.
- **Credential writes are field- or record-scoped CAS operations.** Keystone pin/rotation compares the
  whole prior `OperatorCredential`; login-passkey registration/disable compares only the prior
  `LoginCredential` and changes that field plus `UpdatedAt`. Stale browser ceremonies receive
  `ErrOperatorCredentialChanged` or `ErrLoginCredentialChanged` rather than overwriting a newer choice
  or unrelated password/TOTP changes.
- **Audited keystone CAS is recoverable across errors and restart.** The controller writes the exact
  expected/next/audit identity before CAS. Reconciliation appends only when `next` is current, finds a
  committed append by its random `EventID`, and retains the marker across append/delete ambiguity.
  A still-expected credential proves the CAS did not commit and permits safe restart; any unrelated
  current credential is a fail-closed conflict. `GET /operator-credential` uses the recovery-aware
  read so a failed POST followed by status hydration cannot permanently lose its audit.
- **Single-use mechanisms have intentionally different burn shapes.** Enrollment tokens retain a
  `ConsumedAt` marker so replay can return `ErrTokenConsumed`. Assertion challenges are deleted on
  successful consume, so replay becomes `ErrChallengeInvalid`. Challenge creation purges expiry;
  enrollment uses `ReplaceAssertionChallengeForSubject` to retain at most one live actor+purpose
  challenge, while normal login permits concurrent challenges for the same account.
- **Telemetry is observability, not deploy custody.** Heartbeats update a volatile overlay and an
  in-memory history buffer without the store-wide lock or synchronous disk writes. `/report` can still
  update durable applied-generation state and reflect fresher observations into the overlay, but
  routine reports no longer append to the durable audit chain. Historical `action:"report"` rows from
  older controllers remain untouched in `ListAudit`, preserving raw-chain compatibility.
- **Promote and bump are different operations.** `PromoteStaged` publishes eligible staged data;
  `BumpGeneration` only advances the wake counter, for signals such as fleet rekey. It never changes a
  current bundle.

## Invariants

- **Tenant isolation is structural:** every public `Store` method takes `TenantID`; both backends
  partition on it. `internal/controller/tenant_isolation_test.go` is the perpetual behavior gate.
- **Bearer plaintext is not persisted:** enrollment tokens, node API tokens, assertion challenges, and
  sessions are keyed by SHA-256 hashes. Passwords are argon2id PHC strings. WebAuthn and keystone
  records contain public credential material only.
- **WireGuard private-key custody stops before persistence:** the operator API rejects topology private
  keys before `PutTopology`; node records hold only WireGuard public keys. The generic store method
  still accepts opaque topology JSON, so every future writer needs the same boundary validation.
- **File layout and JSON compatibility are release contracts:** the login-challenge collection and
  `operator` JSON field names remain historical even though the current type is
  `AssertionChallenge{Subject: ...}`. Existing state loads without migration.
- **Backend behavior parity is tested, not assumed:** `internal/controller/store_compat_test.go` runs
  the same contract suite against MemStore and FileStore; focused credential CAS tests cover both.
- **Audit is bounded and hash-chained, but only tamper-evident:** an actor who can rewrite the state root
  can recompute the chain. It is operational evidence, not an external trust anchor.
- **Keystone transition events are exactly-once within recovery:** a fixed timestamp and random
  `EventID` are stored before CAS and included in the versioned audit hash input. General/legacy audit
  entries retain their original canonical bytes and omit the field.

## Gotchas

- FileStore is a single-controller-process design. One in-process mutex serializes custody operations;
  atomic rename protects individual-file durability but does not coordinate two controller processes.
  Its long poll checks `generation.json` every 200 ms; MemStore wakes with `sync.Cond`.
- A process crash partway through FileStore promotion can leave individual files ahead of the still-old
  generation counter. The counter never advertises that partial generation; any transient bundle /
  manifest mismatch fails closed at the agent digest binding and a repeated promote repairs it. This is
  not a transactional database snapshot.
- `DeleteOperator` deliberately does not cascade existing sessions. Callers requiring immediate
  lockout must delete those sessions as a separate policy action.
- `GetNode` merges live telemetry and is for fleet views. Custody read-modify-write code must use
  `GetNodeRecord`; writing the merged view would turn transient observability into durable state.
- Topology-history pruning and corrupt-entry skipping favor a serviceable recovery surface. A crash
  orphan newer than the committed `topology.json` is invisible until a later write self-heals it.

## Verification

- `internal/controller/store_compat_test.go` — cross-backend contract and crash-shape coverage.
- `internal/controller/operator_credential_cas_test.go` and
  `internal/controller/login_credential_cas_test.go` — stale/concurrent credential transitions.
- `internal/controller/keystone_transition_recovery_test.go` — CAS, audit-append, and marker-delete
  failures; committed-before-error ambiguity; exact-once status healing; and FileStore reopen.
- `internal/controller/login_challenge_test.go` — concurrent login challenges, actor/purpose
  replacement, expiry, wrong-subject preservation, and one-use deletion.
- `internal/controller/tenant_isolation_test.go` — mandatory tenant partitioning.
- `internal/controller/filestore_durability_test.go` and audit/resource-history tests — durable-write,
  torn-tail, rotation, cap, and background-flush behavior.

## Deep docs

`docs/spec/controller/persistence.md`, `docs/spec/controller/key-custody.md`,
`docs/spec/controller/operator-auth.md`, and `specs/keystone-trustlist.md`.
