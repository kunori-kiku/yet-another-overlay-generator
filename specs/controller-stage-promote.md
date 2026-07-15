# Controller stage and promote orchestration

<!-- last-verified: 2026-07-16 -->

## Responsibility

Project the stored design onto the nodes whose public identities are ready, run the shared compile
pipeline in agent-held custody, stage only bundles that need delivery, construct the optional
off-host-signable membership manifest, and keep promotion as the sole operation that makes a new
generation live.

## Files

- `internal/controller/compile.go:48-181` defines `StageResult`, bundle-content identity, delta-skip
  eligibility, fetch settings, and exported-directory loading.
- `internal/controller/compile_subgraph.go:21-240` builds the ready subgraph, invokes
  `internal/localcompile` in `AgentHeld` custody, reserves pins belonging to excluded edges, and
  writes compiled allocation pins back into the full topology.
- `internal/controller/compile_manualnode.go:19-110` validates operator-asserted manual-node public
  identities and enforces cross-source key uniqueness.
- `internal/controller/compile_stage.go:21-330` owns stage options and the mutating stage sequence.
- `internal/controller/compile_preview.go:18-110` performs the read-only deploy preview using the
  same digest and delta-skip decisions.
- `internal/controller/keystone.go:18-225` reconciles the bundle-signing anchor and assembles the
  staged membership manifest.
- `internal/controller/compile_promote.go:12-70` applies the keystone signature gate and promotes.
- `internal/controller/storecore_stage.go` implements the durable staged-set seal and the exact-set
  store transition shared by MemStore and FileStore.
- `internal/controller/tenantlock.go:3-28` serializes multi-call stage, promote, enrollment, and
  rekey operations per tenant in this single-process controller.

The operator endpoints are implemented in `internal/api/handler_deploy.go:20-176`: `stage` accepts
optional force controls, `deploy-preview` describes the unforced blast radius, `compile-preview`
returns the rendered ready subgraph without writes, and `promote` flips the staged generation.

## Admission and compilation

`CompileAndStage` loads and parses the stored topology, heals pre-existing allocation-pin
collisions, loads the node registry and controller settings, then calls `CompileSubgraph`
(`internal/controller/compile_stage.go:81-123`).

A managed node is ready only when its registry record is approved and has a non-empty WireGuard
public key. A manual node is ready from the public key asserted in the topology; it never enrolls.
Before either preview or stage, every manual public key must be present, valid, and unique across
manual and approved managed nodes (`internal/controller/compile_manualnode.go:19-79`). Edges with
an unready endpoint are dropped. A ready client whose required dial target is not ready is also
excluded until a later deploy. Only excluded managed nodes appear in `SkippedUnenrolled`.

The projection overwrites managed topology keys from the registry and clears every
`WireGuardPrivateKey`, including stray values on manual imports. `CompileSubgraph` then calls the
canonical `internal/localcompile` facade with `render.AgentHeld`; rendered private-key locations
contain `PRIVATEKEY_PLACEHOLDER`, never real key material
(`internal/controller/compile_subgraph.go:21-90,93-182`). Allocation pins held by edges outside the
ready subgraph are reserved so incremental enrollment cannot allocate over them.

The `update-topology` API also enforces this custody boundary before storage: it rejects any
non-empty `wireguard_private_key`, heals colliding pins, and stores the canonical checked model
instead of unchecked raw bytes (`internal/api/handler_topology.go:23-65`).

## Stage sequence

The complete mutating stage runs under the per-tenant operation lock:

1. Project readiness before resolving any signing key. If no node is ready, purge every stale
   staged bundle and audit `stage-empty`; no render/export bytes exist, so a broken signing-key file
   is irrelevant to this cleanup path.
2. For a non-empty subgraph, resolve `YAOG_BUNDLE_SIGNING_KEY` exactly once. Pass that same in-memory
   `ConfigSigner` snapshot into `localcompile`/render, signing-anchor reconciliation, and
   `artifacts.ExportWithSigner`. The public key embedded in `install.sh`, the persisted public
   anchor, `signing-pubkey.pem`, and `bundle.sig` therefore cannot come from different filesystem
   reads if the configured key file changes during a stage.
3. Compile the ready subgraph and persist only allocation write-backs into the full stored
   topology. A byte-identical write-back is skipped to preserve bounded topology history.
4. Reconcile the signer snapshot against the tenant's persisted signing anchor before export. A
   first configured key is pinned by trust on first use; dropping a pinned key or swapping it
   without the explicit rotation opt-in fails closed (`internal/controller/keystone.go:78-123`).
5. Export with the same signer snapshot to a controller-owned temporary directory and read each
   node directory into the store's slash-keyed file map.
6. Compare `hex(sha256(checksums.sha256))` with the node's currently served digest. A match is
   `Unchanged` and is not staged, unless the operator forced that node/all nodes or keystone first
   pin/credential rotation requires a full restage (`internal/controller/compile.go:115-150` and
   `internal/controller/compile_stage.go:200-255`). Uncertainty fails toward staging.
7. With keystone enabled, build an unsigned canonical manifest for off-host signing. Its members
   include every ready node—both changed and unchanged—with that node's public key and bundle
   digest. This prevents delta-skipped nodes from disappearing from the trust set
   (`internal/controller/compile_stage.go`).
8. Publish the changed bundles and optional manifest through `ReplaceStagedSet`. The store deletes
   the prior `staged-set.json` seal first, writes every candidate bundle, prunes every record outside
   the exact changed set, writes the manifest, and writes a seal containing the provisional
   generation, sorted node IDs, and manifest hash/epoch last. A process crash before the final seal
   can leave loose files, but they cannot promote after restart.

`StageResult` reports node IDs in `Staged`, `UnchangedNodeIDs`, and `SkippedUnenrolled` plus the
provisional generation. Staging itself never advances the tenant generation.

## Empty and unchanged stages

No stored topology is a benign no-op. If the topology exists but has no ready nodes, stage still
purges all previously staged bundles and audits `stage-empty`; otherwise a removed design could
leave an old root-executed bundle promotable. Readiness is evaluated before loading the optional
signer so an unreadable key cannot strand that stale bundle (`internal/controller/compile_stage.go:121-149`).

If every ready bundle is byte-identical to the served bundle, nothing is promotable. The controller
clears every promotable staged bundle and candidate seal, returns the current generation with all
ready nodes in `UnchangedNodeIDs`, does not regenerate the manifest, and audits `stage-unchanged`
(`internal/controller/compile_stage.go:257-280`).

For API/status compatibility, the most recent manifest may remain readable behind a seal marked
`historical`. That marker has no node IDs, cannot promote, and is never inherited by direct
incremental staging; it exists only so the panel can continue showing the last epoch. The separate
history record remains the compiler's monotonic-epoch base.

These purges are custody controls, not cleanup conveniences: a prior stage awaiting a signature
must not survive a later reverted or empty design and become live on an unrelated promote.

## Keystone and promotion

Keystone is enabled by a pinned operator credential. `buildStagedManifest` returns canonical
trust-list bytes with an empty signature for the seal-last batch. Its monotonic epoch is reused only
when the complete mapping
`node_id -> (wg_public_key,bundle_sha256)` is identical to the prior staged manifest; otherwise it
increments, starting at zero (`internal/controller/keystone.go:125-196`). A separate history record
preserves that epoch base when an abandoned stage is cleared; history is never served and never
authorizes promotion. The operator obtains the active sealed bytes, signs them off-host, and submits
the detached signature. Signature installation may replace only `SignatureJSON` over the same sealed
canonical bytes and epoch.

`PromoteStaged` holds the same tenant lock. With keystone off it delegates directly to the store.
With keystone on it refuses a missing manifest, empty signature, malformed credential binding, or
signature that does not verify against the currently pinned credential
(`internal/controller/compile_promote.go:32-70`). Successful promotion flips only bundles staged
for the expected next generation, advances the tenant generation, stamps desired generations on
affected registry nodes, publishes the signed manifest, and wakes long-poll waiters.

The store independently requires the exact sealed generation and sorted bundle set, plus a
byte/epoch match for the optional manifest. Missing seals, loose same-generation records, and
manifest substitutions return `ErrIncompleteStagedSet` (also matching `ErrNoStagedBundle`) without
changing live state. Promotion retains staged inputs until the generation counter commits last, so a
pre-commit crash can re-drive the whole flip rather than losing the already-copied subset.

The controller's promote gate verifies the signature over the stored manifest but deliberately
does not re-derive each staged bundle digest. The agent is the authoritative offline check: it
hashes its received `checksums.sha256`, binds that digest to its signed member entry, verifies the
bundle, and only then applies it.

## Preview and force controls

`POST /deploy-preview` compiles and exports the posted current canvas without storing topology,
pins, bundles, manifests, or audits. It shares the served digest and keystone full-restage decision
with the real stage and reports each ready node as changed or unchanged
(`internal/controller/compile_preview.go:37-110`).

`POST /stage` accepts an empty body for normal delta behavior, `{force_all:true}` for a fleet-wide
redeploy, or `force_nodes` for drift recovery on selected nodes. Force changes delivery, not the
compiled content or trust model.

## Invariants and gotchas

- Promotion is the only go-live transition; repeated stage operations do not consume generation
  numbers.
- A generation bump between stage and promote invalidates the provisional set; re-stage rather
  than promoting bundles compiled before the bump.
- Loose staged JSON is not authority. Only the durable `staged-set.json` seal written after every
  component authorizes promotion; a clean restage is the recovery operation after a partial write.
- Staged bundle membership is the changed delivery set, while keystone manifest membership is the
  full ready trust set. They intentionally differ under delta-skip.
- Trust-list files are appended at config-serving time and never included inside the bundle whose
  checksum digest they bind.
- Controller compile, preview, and stage all use the shared localcompile/render path; no entrypoint
  has an alternate compiler.
