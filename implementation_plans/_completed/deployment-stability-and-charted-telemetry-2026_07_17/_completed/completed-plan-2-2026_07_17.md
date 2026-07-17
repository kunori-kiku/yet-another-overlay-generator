# plan-2 — Repair client allocation compatibility and browser baselines

**Outline:** [outline.md](./outline.md)
**Subject:** deployment-stability-and-charted-telemetry-2026_07_17
**Track:** Go + frontend
**Depends on:** plan-1 completed and closed

## Goal

Make validation, allocation, compiler write-back, excluded-subgraph reservation, Go/TypeScript
normalization, the allocation editor, and browser synchronization agree on the historical
client-to-router contract. An unrelated telemetry edit or topology expansion must not invalidate or
renumber an established client edge.

## Prerequisites

- Verify plan 1 is `done` in the outline and its close-phase archive completed.
- Preserve all remaining dirty work; do not reset the partial allocation fix or stage probe-name and
  future telemetry hunks into this plan.
- Read [outline.md](./outline.md), root `PRINCIPLES.md`, and the cited specs completely.
- Confirm this is a semantic correction using existing fields. Do not bump `alloc_schema_version`.
- Before editing, search the active tree for stale phrases equivalent to “client edge pins are
  ignored,” “clear all client pins,” or “client-touched edge contributes no reservation.”

## Reads from specs

Reads from specs: compiler-allocation, model-validation, controller-stage-promote, panel-design

## Read first

Read every file completely; these anchors identify the exact current seams and stale assertions.

1. `internal/model/topology.go:275-296` and `frontend/src/types/topology.ts:128-136` — mirrored edge
   allocation contract.
2. `internal/validator/semantic_pins.go:45-135` and `internal/validator/code.go:104-116,230-238` —
   endpoint-specific validation and the compatibility-only deprecated code.
3. `internal/compiler/compiler.go:230-305` — allocation write-back and `compiled_port`.
4. `internal/compiler/peers_build.go:280-429,461-499` — pin reservation, gap fill, and router-side
   client rendering.
5. `internal/compiler/peers_prealloc.go:7-108` — `BuildReservedFromExcludedEdges`.
6. `internal/normalize/pins.go:85-220` — collision healing and client-endpoint migration.
7. `internal/compiler/compiler_test.go:63-108`, `internal/compiler/reserved_test.go:9-118`,
   `internal/validator/allocation_pins_test.go:297-343`, `internal/normalize/pins_test.go:24-103`, and
   `internal/controller/compile_test.go:134-344` — stale backend expectations.
8. `internal/localcompile/testdata/fail/06-validator-client-edge-constraints.json` and
   `internal/localcompile/testdata/golden/fail/validator-client-edge-constraints.json` — affected
   native fail-corpus fixture and golden.
9. `internal/localcompile/manifest_golden_test.go:163-197` — `TestGoldenFail` update/verification path.
10. `frontend/src/lib/allocationFields.ts:1-19`, `frontend/src/lib/normalizeEdges.ts:41-99,101-200`, and
    `frontend/src/lib/normalizeEdges.test.ts:1-164` — shared allocation field catalog and stale browser
    assertions/imports.
11. `frontend/src/components/design/aside/EdgeEditor.tsx:113-176,550-596` — NAT readout, partial-pair
    warning, and endpoint-local port inputs.
12. `frontend/src/stores/topologyStore.ts:368-377,428-442,444-475,636-658` — role/edit healing,
    server allocation merge, and custody scrub.
13. `frontend/src/stores/controller/sync.ts:75-142,146-255` and
    `frontend/src/stores/controller/helpers.ts:156-183` — hydration/save baselines and dirty checks.
14. `frontend/src/stores/controllerStore.contextGeneration.test.ts:1-133,181-199` — controller-store
    hydration test setup for the new baseline regression.
15. `internal/wiredrift/drift_test.go:294-319` — stale frontend allocation-list ownership.
16. `docs/spec/compiler/allocation-stability.md:145-160,275-290`,
    `specs/compiler-allocation.md:30-45`, and `specs/model-validation.md:105-160` — active descriptions,
    including the malformed sentence fragment.

## Implementation steps

### Step 1 — Encode the endpoint-specific contract

Apply this contract consistently:

- A client endpoint uses shared `wg0` and therefore has no dedicated per-link listen-port pin.
- The non-client endpoint of the same link owns a real per-link interface/listen port. Its one-sided
  pin is valid, range-checked, deduplicated, reserved, reused, rendered, and persisted.
- Transit IPv4 and link-local IPv6 allocations remain complete two-sided pairs on client links.
- `compiled_port` remains the effective client dial port: explicit `endpoint_port` when configured,
  otherwise the non-client endpoint’s pinned listen port.
- `CodePinClientAllocationIgnored` remains registered for old wire/catalog compatibility, is marked
  deprecated, and is no longer emitted for valid historical data.

Update the Go and TypeScript model comments to state the exception precisely. Do not introduce a new
field, reinterpret `compiled_port` as the sticky allocation, or change the schema version.

### Step 2 — Reconcile validator, allocator, write-back, and excluded reservations

In `validateAllocationPins`:

```go
fromClient := fromNode.Role == "client"
toClient := toNode.Role == "client"
if (fromClient && edge.PinnedFromPort != 0) ||
   (toClient && edge.PinnedToPort != 0) {
    result.AddError(prefix, CodePinClientPortPin, P{"id", edge.ID})
}
validatePinPairCompleteness(prefix, edge, !(fromClient || toClient), result)
```

Continue ordinary range/dedup checks on both port fields and ordinary completeness/pool/dedup checks
on both address pairs.

In compiler allocation/write-back:

- Reserve a valid one-sided non-client port before gap filling.
- Gap-fill only the missing non-client side of a client link; the client side stays zero.
- Preserve complete pinned transit/link-local pairs.
- Write all live sticky values back to the edge, then force only the client endpoint’s port to zero.
- Render the router-side peer with that router listen port and the complete address allocations.

In `BuildReservedFromExcludedEdges`, reserve the non-client port and complete address pairs of every
enabled excluded client link. Included and disabled edges remain skipped.

### Step 3 — Heal only invalid endpoint-local state

In Go and TypeScript normalizers:

1. Clear `pinned_from_port` only when `from_node_id` is a client.
2. Clear `pinned_to_port` only when `to_node_id` is a client.
3. Preserve the opposite endpoint port, complete transit/link-local pairs, and `compiled_port`.
4. Include those surviving resources in collision ownership.
5. Strip an edge’s allocation fields only for a genuine collision with a different link.
6. Preserve no-op references and idempotence.

For the existing broad collision tests, give the client edge non-colliding address values, seed both
an invalid client-side port and a valid router-side port, and assert only the invalid endpoint value
is healed. Do not let an accidental `.1/.2` collision make the test pass by stripping everything.

In `EdgeEditor`, disable only the input belonging to the client endpoint and suppress the ordinary
two-port completeness warning for a client link. Keep range warnings on the live router-side port.

### Step 4 — Single-source frontend allocation fields and repair drift checks

Keep these exact leaf constants in `frontend/src/lib/allocationFields.ts`:

```ts
export const PERSISTED_ALLOCATION_PIN_FIELDS = [
  'pinned_from_port',
  'pinned_to_port',
  'pinned_from_transit_ip',
  'pinned_to_transit_ip',
  'pinned_from_link_local',
  'pinned_to_link_local',
] as const;

export const SERVER_ALLOCATION_FIELDS = [
  'compiled_port',
  ...PERSISTED_ALLOCATION_PIN_FIELDS,
] as const;
```

Use the six-field list for persisted-pin assertions and the seven-field list for canvas reconciliation
and custody clearing. Do not restore ambiguous `PIN_FIELDS` / `ALLOCATION_PIN_FIELDS` aliases.

Update `TestFEPinListsAreEdgeFields` to read `PERSISTED_ALLOCATION_PIN_FIELDS` from
`allocationFields.ts`. The current Go helper cannot parse TypeScript array spreads, so do not point it
at `SERVER_ALLOCATION_FIELDS`; instead, make `normalizeEdges.test.ts` independently assert the exact
six- and seven-field sets and the `compiled_port` composition.

### Step 5 — Normalize browser hydration and save baselines

Keep this helper shape in `frontend/src/lib/normalizeEdges.ts`:

```ts
export function normalizeTopologyForCanvas(topo: Topology): Topology {
  const healed = healCollidingPins(topo.edges, topo.nodes);
  const edges = sanitizeLinkDirection(healed);
  return edges === topo.edges ? topo : { ...topo, edges };
}
```

In `hydrateFromServer`, normalize before both `loadTopology` and:

```ts
set({
  lastSyncedSnapshot: canonicalDesign(normalized),
  lastSyncedTopology: normalized,
});
```

In `saveDesign`, normalize the visible canvas before the no-op/dirty comparison and normalize the
final clean payload after any server-pin merge. Record exactly the payload sent as both post-save
baselines.

Create `frontend/src/stores/controllerStore.syncNormalization.test.ts`. Stub one GET `/topology`
returning a client→router edge with:

- invalid `pinned_from_port` on the client;
- valid `pinned_to_port` on the router;
- complete transit/link-local pairs; and
- `compiled_port` equal to the router port.

After `hydrateFromServer`, assert the client-side port is absent, every other allocation survives in
both stores/baselines, `isDesignDirty` is false, and an immediate `saveDesign()` issues no additional
request.

### Step 6 — Rewrite the focused regressions and fail golden

- Rename the stale compiler test to:

```go
func TestCompileClientEdgeAllocationsRemainStableAcrossSuperset(t *testing.T)
```

  Compile a client→router edge, assert the endpoint-specific shape, then add a `node-0` router/link
  whose link identity allocates earlier and recompile the first result. Assert the original router
  port, transit pair, link-local pair, and effective dial port are unchanged.
- Replace the two stale client-pin validator tests with one table-driven
  `TestValidateAllocationPins_ClientEndpointContract`: valid router-side port plus complete addresses
  passes without the deprecated warning; client-side port emits `CodePinClientPortPin`.
- Update `TestBuildReservedFromExcludedEdges_SkipRules` so the excluded client edge reserves its
  router port and complete address pairs while included/disabled edges remain skipped.
- Update the controller subtest `TestCompileAndStage_RenderWhatsReady/enrolling client fills in the
  subgraph` so the unchanged third stage succeeds and stored client allocations retain the valid
  endpoint-specific shape.
- Update the fail fixture description to describe compatible client-pin migration plus the independent
  multiple-outbound/backup validation errors. The golden must drop the deprecated ignored/client-port
  findings after normalization, preserve the router-side/address allocations in `healed_edges`, and
  remain a failing fixture because of the independent client-edge constraints.

Regenerate only that golden after inspecting the intended semantic delta:

```bash
GOSUMDB=sum.golang.org GOPROXY=https://proxy.golang.org,direct \
  go test ./internal/localcompile \
  -run '^TestGoldenFail$/^validator-client-edge-constraints$' \
  -update
git diff -- internal/localcompile/testdata/golden/fail/validator-client-edge-constraints.json
```

### Step 7 — Update durable descriptions

Correct the endpoint-specific client exception in the Go/TypeScript model comments,
`docs/spec/compiler/allocation-stability.md`, `specs/compiler-allocation.md`, and
`specs/model-validation.md`. Repair the malformed manual-node/telemetry-draft paragraph in
`specs/model-validation.md`. Do not edit archived rc investigation material.

### Step 8 — Format and run the exact focused gate

Run:

```bash
gofmt -w \
  internal/model/topology.go \
  internal/validator/semantic_pins.go \
  internal/validator/code.go \
  internal/validator/allocation_pins_test.go \
  internal/compiler/compiler.go \
  internal/compiler/compiler_test.go \
  internal/compiler/peers_build.go \
  internal/compiler/peers_prealloc.go \
  internal/compiler/reserved_test.go \
  internal/normalize/pins.go \
  internal/normalize/pins_test.go \
  internal/controller/compile_test.go \
  internal/wiredrift/drift_test.go

GOSUMDB=sum.golang.org GOPROXY=https://proxy.golang.org,direct \
  go test ./internal/compiler ./internal/validator ./internal/normalize ./internal/wiredrift \
  -run '^(TestCompileClientEdgeAllocationsRemainStableAcrossSuperset|TestValidateAllocationPins_ClientEndpointContract|TestHealCollidingPins|TestBuildReservedFromExcludedEdges_SkipRules|TestFEPinListsAreEdgeFields)$' \
  -count=1

GOSUMDB=sum.golang.org GOPROXY=https://proxy.golang.org,direct \
  go test ./internal/controller \
  -run '^TestCompileAndStage_RenderWhatsReady$/^enrolling_client_fills_in_the_subgraph$' \
  -count=1

GOSUMDB=sum.golang.org GOPROXY=https://proxy.golang.org,direct \
  go test ./internal/localcompile \
  -run '^TestGoldenFail$/^validator-client-edge-constraints$' \
  -count=1

cd frontend
npm run vitest -- \
  src/lib/normalizeEdges.test.ts \
  src/stores/controllerStore.syncNormalization.test.ts
cd ..
```

### Step 9 — Independent review, fixes, and clean re-review

- Spawn an independent reviewer after the exact gate passes.
- Require an explicit search for remnants of the all-client-pins-cleared model, plus review of
  collisions, excluded reservations, browser baseline equality, Go/TypeScript parity, compatibility,
  and test economy.
- Fix every actionable finding, rerun affected commands, and obtain a clean fresh re-review before
  committing.

### Step 10 — Commit and push the implementation

Hunk-stage shared model/spec/test files so probe-name or future telemetry changes remain uncommitted:

```bash
git add \
  internal/validator/semantic_pins.go \
  internal/validator/code.go \
  internal/validator/allocation_pins_test.go \
  internal/compiler/peers_build.go \
  internal/compiler/peers_prealloc.go \
  internal/compiler/reserved_test.go \
  internal/normalize/pins.go \
  internal/normalize/pins_test.go \
  internal/controller/compile_test.go \
  internal/localcompile/testdata/fail/06-validator-client-edge-constraints.json \
  internal/localcompile/testdata/golden/fail/validator-client-edge-constraints.json \
  internal/wiredrift/drift_test.go \
  frontend/src/lib/allocationFields.ts \
  frontend/src/lib/normalizeEdges.ts \
  frontend/src/lib/normalizeEdges.test.ts \
  frontend/src/components/design/aside/EdgeEditor.tsx \
  frontend/src/stores/topologyStore.ts \
  frontend/src/stores/controller/sync.ts \
  frontend/src/stores/controller/helpers.ts \
  frontend/src/stores/controllerStore.syncNormalization.test.ts \
  docs/spec/compiler/allocation-stability.md
git add -p internal/model/topology.go internal/compiler/compiler.go internal/compiler/compiler_test.go
git add -p frontend/src/types/topology.ts
git add -p specs/compiler-allocation.md specs/model-validation.md
git diff --cached --check
git diff --cached --stat
```

The cached diff must contain no audit, probe-name, URL, device, or release work. Commit exactly:

```bash
GIT_AUTHOR_NAME='KunoriKiku' GIT_AUTHOR_EMAIL='rokuyanlin@gmail.com' \
GIT_COMMITTER_NAME='KunoriKiku' GIT_COMMITTER_EMAIL='rokuyanlin@gmail.com' \
git commit -m "$(cat <<'EOF'
fix(allocation): preserve client router pins
EOF
)"
```

Push exactly:

```bash
git push origin fix/rc12-telemetry-drafts
```

Then let the executor create its separate outline-status commit and invoke `close-phase`; do not mix
closure bookkeeping into the implementation commit.

## Tests produced by this plan

- `internal/compiler/compiler_test.go` — `TestCompileClientEdgeAllocationsRemainStableAcrossSuperset`
  - **Lifetime:** perpetual (compiler-domain regression)
  - **Guards:** endpoint-specific client allocation shape and superset stability.
  - **Retirement trigger:** never while sticky allocation compatibility is supported.
  - **Retirement destination:** none; remains in the compiler package.
- `internal/validator/allocation_pins_test.go` — `TestValidateAllocationPins_ClientEndpointContract`
  - **Lifetime:** perpetual (validator-domain contract)
  - **Guards:** valid router-side versus invalid client-side port semantics.
  - **Retirement trigger:** never while the client role uses shared `wg0`.
  - **Retirement destination:** none; remains in the validator package.
- `internal/normalize/pins_test.go` and `frontend/src/lib/normalizeEdges.test.ts`
  - **Lifetime:** perpetual (cross-runtime normalization contract)
  - **Guards:** only the client endpoint port is healed; other allocations and idempotence survive.
  - **Retirement trigger:** never while Go and browser normalize persisted topologies.
  - **Retirement destination:** none; remain in their domain packages.
- `internal/compiler/reserved_test.go` — excluded-client case in
  `TestBuildReservedFromExcludedEdges_SkipRules`
  - **Lifetime:** perpetual (controller-subgraph allocation invariant)
  - **Guards:** excluded client links retain ownership of router/address resources.
  - **Retirement trigger:** never while controller compilation uses enrolled subgraphs.
  - **Retirement destination:** none; remains in the compiler package.
- `internal/controller/compile_test.go` — repeated-stage client subtest
  - **Lifetime:** perpetual (controller production-path regression)
  - **Guards:** the persisted output of one stage remains valid on the next stage.
  - **Retirement trigger:** never while staged controller deployment is supported.
  - **Retirement destination:** none; remains in the controller package.
- `internal/localcompile/testdata/golden/fail/validator-client-edge-constraints.json`
  - **Lifetime:** perpetual (native localcompile fail-corpus golden)
  - **Guards:** fail-channel and healed-edge semantics remain deterministic.
  - **Retirement trigger:** only if the fixture itself is deliberately retired with replacement
    coverage.
  - **Retirement destination:** the repository’s ordinary legacy-golden location if retired.
- `frontend/src/stores/controllerStore.syncNormalization.test.ts`
  - **Lifetime:** perpetual (frontend synchronization invariant)
  - **Guards:** normalized canvas and server baselines cannot diverge or manufacture dirty state.
  - **Retirement trigger:** never while controller hydration/save baselines exist.
  - **Retirement destination:** none; remains under `frontend/src/stores/`.
- `internal/wiredrift/drift_test.go` — `TestFEPinListsAreEdgeFields`
  - **Lifetime:** perpetual (wire-drift gate)
  - **Guards:** the six frontend persisted pin names remain real `model.Edge` JSON fields.
  - **Retirement trigger:** never while Go/TypeScript edge DTOs are hand mirrored.
  - **Retirement destination:** none; remains in `internal/wiredrift`.

## Definition of done

- [ ] Existing router-side one-sided client ports and complete address pairs validate and persist.
- [ ] A port on the client endpoint is the only endpoint port rejected/healed.
- [ ] `compiled_port` remains the effective dial target and is stable across a superset compile.
- [ ] Excluded client links reserve every still-live resource.
- [ ] Go and TypeScript normalization/editor behavior agree.
- [ ] Hydration/save baselines match the normalized canvas and an immediate Save is a no-op.
- [ ] The focused fail golden and all exact gates pass.
- [ ] Independent findings are fixed and the fresh re-review is clean.
- [ ] The scoped implementation commit is pushed and close-phase completes separately.

## Out of scope for this plan

- Allocation schema migration, bulk reassignment, or destructive pin clearing.
- Audit filtering and probe display names.
- URL probes, device discovery/metrics, or chart framework work.
- Full CI, Playwright, release, or container publication gates.
