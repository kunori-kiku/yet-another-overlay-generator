# Plan 4.1 — Phase 2a: controller persistence foundation

Parent: [plan-4-2026_06_08.md](plan-4-2026_06_08.md) (decomposition + adopted decisions) · Prereq:
Plans 1–3 merged. First of four Phase 2 sub-plans (4.1 persistence → 4.2 enrollment+mTLS → 4.3 HTTP
surface+deploy+agent integration → 4.4 frontend).

## Goal

The durable **state layer** the rest of Phase 2 builds on: a single tenant-scoped `Store` interface
in the new `internal/controller` package, with stdlib-only implementations. No HTTP, no enrollment,
no mTLS yet — just registry + topology + signed-bundle/generation + hash-chained audit, behind one
chokepoint, fully CI-green via the in-memory impl.

## Key constraint that shaped this (adopted)

Go is **not installed locally**, so a new module dependency (Postgres driver) cannot be added — `go
mod tidy` / `go.sum` can't be generated, and CI (`go vet`/`go test`) would fail on the unresolved
module. Therefore Phase 2 v1 persistence is **stdlib-only**:

- `MemStore` — in-memory, the CI-exercised impl + long-poll primitive.
- `FileStore` — JSON files under a state dir (durable for a real single-tenant v1 deployment), stdlib
  `encoding/json` + `os` only.
- **Postgres adapter is a documented future `Store` impl** (`docs/spec/controller/persistence.md`
  §Postgres), added in a later PR when the driver can be vendored (a dev with Go runs `go mod tidy`).
  The interface makes that swap drop-in. This honors *minimal-deps* and *no-local-Go* without an ugly
  workaround, and a file-backed store is appropriate for a single-tenant v1 (Postgres is a
  scale/multi-tenant concern, Phase 3).

## Read first

- `internal/render/render.go` (KeyCustody/AgentHeld — the controller renders public-keys-only),
  `internal/bundlesig` (Canonicalize/Sign — bundles stored are the Phase-0 signed form).
- `internal/agent/state.go` (the agent's own JSON state shape — mirror its temp+rename 0600 write
  discipline in FileStore), `internal/artifacts/export.go` (manifest fields).
- `cmd/server/main.go`, `internal/api/handler.go` (where 4.3 will wire the Store; not touched here).

## Implementation steps

1. **`internal/controller/store.go`** — the `Store` interface + types + sentinel errors + `TenantID`.
   Every method takes `TenantID` as a **mandatory first predicate** (the structural tenant-isolation
   chokepoint; Phase 2 passes a constant from `YAOG_TENANT_ID`, Phase 3 derives it from the
   principal). Surface (registry / topology / bundles+generation / audit; enrollment-token methods are
   added by 4.2):
   - Registry: `UpsertNode`, `GetNode`, `ListNodes`, `SetAppliedGeneration`, `TouchLastSeen`.
   - Topology (public-keys-only JSON): `PutTopology`, `GetTopology`.
   - Bundles+generation: `StageBundle`, `PromoteStaged` (atomic staged→current, ++generation),
     `GetCurrentBundle(nodeID)`, `CurrentGeneration`, `WaitForGeneration(afterGen)` (long-poll
     primitive — blocks until generation advances or ctx done).
   - Audit (append-only, SHA-256 hash-chained): `AppendAudit`, `ListAudit`.
   - Types: `Node` (WG **public** key only, never private), `TopologyRecord`, `SignedBundle`
     (`Files map[string][]byte`, IsStaged/IsCurrent), `AuditEntry` (Seq/PrevHash/Hash).
2. **`internal/controller/audit.go`** — the hash-chain helper: canonical-bytes of an entry (stable
   field order), `Hash = hex(SHA256(canonicalBytes_including_PrevHash))`, chained from the prior
   entry. Documented as tamper-EVIDENT for operational visibility only (an attacker with write access
   to the backing store can recompute the whole chain — real cryptographic anti-tamper is Phase 3,
   matching the honest framing already used for the agent's anti-rollback stub).
3. **`internal/controller/memstore.go`** — `MemStore` (maps keyed by `TenantID`), mutex-guarded,
   per-(tenant) generation broadcast for `WaitForGeneration` (a `sync.Cond` or a closed-channel
   broadcast). The CI-exercised impl.
4. **`internal/controller/filestore.go`** — `FileStore`: JSON under `<dir>/<tenant>/…`, 0700 dirs,
   0600 files, temp+rename writes (mirror `internal/agent/state.go`). `WaitForGeneration` polls the
   on-disk generation with a short interval + ctx cancel. Durable single-tenant v1 persistence.
5. **Tests** (`internal/controller/*_test.go`):
   - `store_compat_test.go` — a table-driven compatibility suite run against **both** `MemStore` and
     `FileStore` (t.TempDir): round-trip every type; stage→promote advances generation +
     current-bundle; `WaitForGeneration` returns on promote and respects ctx-cancel.
   - `tenant_isolation_test.go` — the perpetual **cross-tenant gate**: data written under tenant A is
     invisible to tenant B for every read (GetNode/ListNodes/GetTopology/GetCurrentBundle/ListAudit).
   - `audit_test.go` — hash chain links correctly; flipping any entry breaks verification.
6. **Spec** `docs/spec/controller/persistence.md` — the Store contract, the tenant chokepoint
   invariant, the stdlib MemStore/FileStore impls, the **honest** audit-tamper scope, and the
   documented **Postgres adapter** contract (schema sketch) for the later dep-adding PR. README index.

## Definition of done

- [ ] CI green (`go vet`/`go test`); compat suite passes on MemStore **and** FileStore; cross-tenant
      gate + audit-chain tests pass.
- [ ] No new go.mod dependency (stdlib only). Compiler/renderer/air-gap untouched.
- [ ] `WaitForGeneration` works (long-poll primitive ready for 4.3).
- [ ] `persistence.md` documents the Postgres-adapter path so 4.x/5 can add it drop-in.

## Out of scope (later sub-plans)

Enrollment tokens + mTLS cert issuance (4.2); the HTTP surface, TLS/mTLS, deploy endpoints, and agent
integration (4.3); the frontend (4.4); the Postgres adapter impl + its dep (a later PR); multi-tenant
enforcement, KMS, hardware-signed membership (Plan 5).
