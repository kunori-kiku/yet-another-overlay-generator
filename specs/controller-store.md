# Controller store

<!-- last-verified: 2026-07-17 -->

## Responsibility

Provide the controller's tenant-scoped persistence contract and one shared behavioral core over an
in-memory adapter and a durable filesystem adapter. `MemStore` and `FileStore` exercise the same
controller behavior and atomic read-modify-write rules rather than maintaining parallel
implementations; `FileStore` additionally enforces the durable filesystem custody boundary
(`internal/controller/store.go:551-565`, `internal/controller/storecore.go:3-48`,
`internal/controller/filestore_custody.go:10-50`).

## Files

- `internal/controller/store.go:29-125` and `internal/controller/store.go:551-698` — public tenant,
  sentinel-error, registry, topology, bundle, generation, and telemetry-history storage contracts.
- `internal/controller/kv.go:1-108` and `internal/controller/storecore.go:26-48` — the small raw-record
  port, locking contract, and backend-independent behavior core.
- `internal/controller/memstore.go:16-44` and `internal/controller/memstore.go:223-237` — tenant-partitioned
  byte maps, in-memory generation wake, and the `MemStore` wrapper.
- `internal/controller/filestore.go:15-71`, `internal/controller/filestore.go:85-164`,
  `internal/controller/filestore.go:247-318`, and `internal/controller/filestore.go:321-370` — frozen
  on-disk collection mapping, generation wake, `FileStore` construction, and persistence lifecycle.
- `internal/controller/filestore_custody.go:10-115`, `internal/controller/filestore_record.go:18-171`, and
  `internal/controller/filestore_io.go:98-172` — custody-directory validation, descriptor-confined
  record access, and durable atomic replacement.
- `internal/controller/store_compat_test.go:3-10` and `internal/controller/store_compat_test.go:37-68` —
  the cross-backend compatibility harness that runs the contract over both adapters.

## Inputs

The server supplies the configured state root and starts/stops FileStore's background persistence
lifecycle (`cmd/server/main.go:171-176`). Operator topology writes arrive only after the API has parsed,
private-key-checked, normalized, and re-marshaled the public model
(`internal/api/handler_topology.go:23-65`). Agent heartbeats and the deployment pipeline call the same
tenant-scoped Store with admitted telemetry or an exact staged candidate
(`internal/api/handler_agent.go:353-382`, `internal/controller/compile_stage.go:302-320`).

## Outputs

The Store returns typed records and public sentinel errors through one interface, while FileStore maps
them to stable tenant-relative JSON/JSONL paths and MemStore round-trips copied JSON bytes in memory
(`internal/controller/store.go:551-565`, `internal/controller/filestore.go:29-52`,
`internal/controller/memstore.go:75-116`). Conditional and multi-record operations expose atomic
outcomes such as topology compare-and-set, one-use token consumption, and exact candidate publication
(`internal/controller/storecore.go:225-251`, `internal/controller/storecore.go:532-556`,
`internal/controller/storecore_stage.go:234-280`).

FileStore also provides the generic bounded telemetry-history backing and flusher lifecycle; metric
projection, exact-series semantics, and API rollup belong to `controller-telemetry`
(`internal/controller/filestore.go:323-370`, `internal/controller/telemetry_history.go:25-35`).

## Decision points (if any)

- Business rules belong in `storeCore`; a backend implements record CRUD, generation wake, and audit
  storage only (`internal/controller/kv.go:65-108`, `internal/controller/storecore.go:3-9`).
- Custody read-modify-write operations compose raw records under `withLock`; the metadata-only node
  existence probe and audit hooks are deliberately self-synchronizing outside that scope
  (`internal/controller/kv.go:10-24`, `internal/controller/kv.go:68-108`).
- An absent FileStore root is created securely; on Unix, only a real, process-owned, non-special root
  may be tightened to `0700`, while unsafe descendants are rejected rather than repaired
  (`internal/controller/filestore_custody.go:10-50`,
  `internal/controller/filestore_custody_unix.go:27-71`).

## Invariants

- Every Store operation is tenant-scoped, and the perpetual isolation suite runs against both
  backends (`internal/controller/store.go:551-565`,
  `internal/controller/tenant_isolation_test.go:10-24`).
- Controller persistence holds WireGuard public keys and token hashes, never WireGuard private keys or
  recoverable bearer tokens (`internal/controller/store.go:139-150`,
  `internal/controller/store.go:230-236`).
- FileStore rejects unsafe custody paths and writes `0600` records by syncing temporary bytes,
  atomically replacing the target, then syncing the parent directory
  (`internal/controller/filestore_custody.go:108-132`, `internal/controller/filestore_record.go:18-30`,
  `internal/controller/filestore_io.go:115-172`).

## Gotchas (optional)

- FileStore is a single-process, single-writer design: one mutex serializes store operations and its
  generation wait polls `generation.json` every 200 ms
  (`internal/controller/filestore.go:21-27`, `internal/controller/filestore.go:294-318`).
- Fleet reads use `GetNode`, which merges volatile observations; custody code that will write the node
  back must use `GetNodeRecord` so transient health is not persisted
  (`internal/controller/store.go:570-580`, `internal/controller/storecore.go:107-143`).
- Telemetry-history appends are memory-only on the heartbeat path; FileStore flushes later and
  requeues failed writes inside its bounded volatile buffer rather than failing the heartbeat
  (`internal/controller/telemetry_history.go:25-30`,
  `internal/controller/telemetry_history.go:1071-1138`).
