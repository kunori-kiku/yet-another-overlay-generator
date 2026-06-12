# Controller stage & promote orchestration

<!-- last-verified: 2026-06-12 -->

## Responsibility
Compiles the enrolled subgraph of the stored topology into per-node bundles staged at the next generation, and gates the operator's promote that flips them live.

## Files
- `internal/controller/compile.go:140-273` — `CompileAndStage`: load topology → enrolled subgraph → frozen pipeline → stage bundles → optional keystone manifest → audit.
- `internal/controller/compile.go:288-342` — `stageManifest`: builds and stores the unsigned off-host-signable membership manifest with the monotonic epoch.
- `internal/controller/compile.go:364-407` — `PromoteStaged` (package func): keystone signature gate, then delegates to `Store.PromoteStaged`.
- `internal/controller/compile.go:413-437` — `pinFromOperatorCredential`: builds the `trustlist.PinnedCredential` the promote gate verifies against.
- `internal/controller/compile.go:452-507` — `enrolledSubgraph`: registry-driven admission, key stamping/clearing, edge dropping.
- `internal/controller/compile.go:528-566` — `persistAllocations`: writes compiled allocation pins back into the full stored topology.
- `internal/controller/compile.go:573-597` — `readBundleDir`: reads an exported node dir into a slash-keyed file map.

## Inputs
- Operator HTTP layer (see specs/controller-operator-api.md): `HandleStage` calls `CompileAndStage(ctx, store, tenant, time.Now())` (`internal/api/handler_controller.go:688`); `HandlePromote` calls `controller.PromoteStaged(ctx, store, tenant)` (`internal/api/handler_controller.go:718`).
- From the Store (see specs/controller-store.md): stored topology JSON (`GetTopology`, compile.go:143), node registry (`ListNodes`, compile.go:156), pinned operator credential — keystone ON/OFF probe (`GetOperatorCredential`, compile.go:173), prior stored trust list for epoch arithmetic (`GetCurrentSignedTrustList`, compile.go:305), and `CurrentGeneration` (compile.go:214).
- The frozen pipeline, driven exactly as the air-gap path: `render.GenerateKeys(&subgraph, render.AgentHeld)` (compile.go:180, see specs/render-keys.md) → `compiler.NewCompiler().Compile` (compile.go:184, see specs/compiler-allocation.md) → `render.All` (compile.go:188) → `artifacts.Export` into a temp dir removed on return (compile.go:202-209, see specs/artifacts-signing.md).

## Outputs
- `StageResult{Staged, SkippedUnenrolled []string (node IDs), Generation int64}` (compile.go:109-118) returned to the operator API; staged at `CurrentGeneration+1` (compile.go:218).
- Per-node `SignedBundle{NodeID, Generation, Files, IsStaged: true}` via `Store.StageBundle` (compile.go:237-244); restaging replaces a node's prior staged bundle (`internal/controller/store.go:319-321`).
- Keystone ON only: a staged `StoredTrustList` with canonical `TrustListJSON`, **empty** `SignatureJSON`, and the monotonic epoch, via `PutSignedTrustList` (compile.go:334-340; manifest format: see specs/keystone-trustlist.md).
- The full topology re-stored with per-node `OverlayIP` and per-edge port/transit/link-local pins merged in (compile.go:558-563), so the next compile sticky-pins them.
- One `"stage"` audit entry per run (compile.go:259-266).
- `PromoteStaged` returns the new generation `int64`; the underlying `Store.PromoteStaged` flips ALL staged bundles to current, increments the tenant generation by one, stamps `DesiredGeneration` on each promoted node that has a registry record, and wakes `/poll` long-poll waiters (`internal/controller/store.go:322-328`; impls `internal/controller/memstore.go:262-287`, `internal/controller/filestore.go:491-584`) — consumed downstream by specs/controller-agent-api.md and specs/agent.md.

## Decision points (if any)
- **No stored topology** → `ErrNotFound` is benign: empty `StageResult`, nil error (compile.go:143-149).
- **Enrollment admission**: a topology node enters the subgraph iff its registry record is `NodeApproved` with a non-empty `WGPublicKey` (compile.go:455-460); excluded nodes are reported in `SkippedUnenrolled` (compile.go:488-492).
- **Zero enrolled nodes** → early return `StageResult{SkippedUnenrolled: skipped}`, nil error — nothing rendered, no generation consumed (compile.go:164-166).
- **Client readiness**: an enrolled `client` whose only enabled outbound edge targets an unenrolled peer is itself excluded (it would fail the compiler's exactly-one-edge rule) (compile.go:482-486, 512-519).
- **Edge dropping**: any edge with an unenrolled far end is omitted; it activates on a later deploy (compile.go:499-504).
- **Keystone ON/OFF**: a pinned operator credential turns it on (compile.go:172-177). ON → each staged bundle's `checksums.sha256` digest is captured (`bundleSHA256 = hex(sha256(checksums.sha256))`, compile.go:62-65, 229-236) and the manifest is staged unsigned (compile.go:252-256). Staging never requires a signature.
- **Monotonic epoch**: reuse the prior manifest's epoch iff membership (`node_id → {wg key, bundle digest}`) is identical, else prior+1, else 0 on first manifest (compile.go:303-317).
- **Promote gate** (compile.go:364-407): keystone OFF → straight `Store.PromoteStaged` (compile.go:367-369). Keystone ON → refuse when no manifest is staged (compile.go:377-379), when `SignatureJSON` is empty (compile.go:382-384), or when `trustlist.Verify` fails against the pinned credential (compile.go:398-404). HTTP maps `ErrNoStagedBundle` to 409 and gate refusals to 422 (`internal/api/handler_controller.go:718-728`).

## Invariants
- Staging never advances the generation counter; only `Store.PromoteStaged` increments it and stamps `DesiredGeneration` — operator promote is the sole go-live decision (`internal/controller/store.go:322-328`; deep doc: `docs/spec/controller/deploy.md`).
- Zero-knowledge custody: keys are generated `AgentHeld` (compile.go:180) and any stray `WireGuardPrivateKey` on an imported topology node is cleared before rendering (compile.go:495) — PRINCIPLES.md "Key custody" (PRINCIPLES.md:43-47).
- Allocation stability (I10 / superset rule): `persistAllocations` writes compiled pins back into the FULL stored topology — including `AllocSchemaVersion` (compile.go:556) — so re-compiles after incremental enrollment reproduce identical allocations — PRINCIPLES.md:15-18.

## Gotchas (optional)
- A bundle's staged `Generation` is provisional: `Store.PromoteStaged` recomputes `newGen = current+1` at flip time and overwrites `b.Generation` (`internal/controller/memstore.go:269-273`, `internal/controller/filestore.go:533-551`), so repeated stage-without-promote never burns generation numbers.
- The promote gate verifies only the manifest **signature**; it does NOT re-derive staged bundles' digests against the manifest's `BundleSHA256` values — the agent is the authoritative offline chokepoint (compile.go:358-363, see specs/agent.md).
- `docs/spec/controller/persistence.md`'s public-keys-only claim for stored topologies is NOT enforced at `PutTopology` (raw JSON is stored as-is); the render-time clear in `enrolledSubgraph` (compile.go:495) is the actual defense, and `persistAllocations` copies pins only, never key material (compile.go:528-555).
- The export deliberately contains no trust-list files — the manifest binds each bundle's `checksums.sha256` digest, so it cannot live inside that checksum set; the signed manifest is appended to the served file map at `/config` time instead (compile.go:199-201, see specs/controller-agent-api.md).
