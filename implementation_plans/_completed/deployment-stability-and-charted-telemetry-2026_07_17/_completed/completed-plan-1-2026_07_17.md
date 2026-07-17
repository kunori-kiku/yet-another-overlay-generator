# plan-1 — Restore deploy validation and compatibility boundaries

**Outline:** [outline.md](./outline.md)
**Subject:** deployment-stability-and-charted-telemetry-2026_07_17
**Track:** Go + frontend
**Depends on:** none

## Goal

Turn an incomplete telemetry draft into a structured, localizable deployment rejection without
changing served or staged deployment state. Keep draft Save permissive, and expose “Deploy anyway”
only when an older controller genuinely lacks the POST deploy-preview route.

## Prerequisites

- Preserve the existing dirty worktree; do not reset, discard, or blanket-stage changes belonging to
  plans 2–9.
- Read [outline.md](./outline.md) completely, then read root `PRINCIPLES.md`.
- Confirm the active branch is `fix/rc12-telemetry-drafts` and `origin/main` is still the recorded
  subject base before editing.
- Treat `update-topology` as a draft/custody boundary. It must not invoke full executable-topology
  validation merely to reject an unfinished blank-host row.

## Reads from specs

Reads from specs: model-validation, controller-stage-promote, panel-deploy-fleet

## Read first

Read these files completely before editing; the line anchors identify the current target seams.

1. `internal/compiler/compiler.go:83-108,169-191` — `TopologyValidationError`, cloning, and
   `(*Compiler).CompileAt` validation returns.
2. `internal/api/errmap.go:63-82` — `mapTopologyValidationErr`.
3. `internal/api/handler_deploy.go:20-57,60-95,98-164` — stage, deploy-preview, and compile-preview
   error mapping.
4. `internal/api/handler_topology.go:23-78` — draft Save/custody behavior that must remain permissive.
5. `internal/apierr/apierr.go:51-62,180-190` — public code registration and 422 status.
6. `internal/validator/schema.go:349-354` and `internal/validator/telemetry_probe_test.go:11-39` —
   executable probe validation and current manual/managed cases.
7. `internal/compiler/compiler_test.go:12-61`, `internal/api/errmap_test.go:89-126`, and
   `internal/api/deploy_force_preview_test.go:49-255` — focused backend regressions.
8. `frontend/src/stores/controller/deploy.ts:101-137` — `openDeployPreview` compatibility latch.
9. `frontend/src/api/controller/transport.ts:13-44` — `ControllerError` status/body contract.
10. `frontend/src/stores/controllerStore.contextGeneration.test.ts:1-133` — store seeding, response,
    and fetch-stub pattern for the new focused test.
11. `frontend/src/i18n/index.ts`, `frontend/src/i18n/index.test.ts:1-30`, and
    `frontend/src/i18n/messages/{en,zh}.ts` — nested validator localization.
12. `frontend/e2e/adversarial/deploy-faults.spec.ts:133-181` — existing end-to-end positive and
    blocking cases; retain them, but do not run Playwright in this plan unless the focused store test
    cannot express the boundary.

## Implementation steps

### Step 1 — Preserve typed compiler validation findings

In `internal/compiler/compiler.go`, retain this package-level error shape:

```go
type TopologyValidationError struct {
    Phase    string
    Findings []validator.ValidationError
}

func (e *TopologyValidationError) Error() string
```

- `newTopologyValidationError` must deep-copy the findings slice and every non-nil `Params` map.
- `CompileAt` must return that wrapper for schema and semantic failures instead of flattening the
  validator output into an opaque formatted error.
- Do not merge validator codes into the HTTP `apierr` namespace.

In `internal/api/errmap.go`, keep one mapper with this signature:

```go
func mapTopologyValidationErr(err error) *apierr.Error
```

It must use `errors.As`, return nil for every non-validation error, and map the first bounded finding
to `CodeTopologyValidationFailed` with `field`, `validation_code`, `validation_message`, and
`validation_param_<name>` parameters. Register that public code as HTTP 422 in `internal/apierr`.

### Step 2 — Apply the mapping only at executable deploy boundaries

In `HandleStage`, `HandleDeployPreview`, and `HandleCompilePreview`:

1. Preserve existing source-coded/controller sentinel handling.
2. Call `mapTopologyValidationErr` for the typed compiler wrapper.
3. Leave any unknown storage, keystone, renderer, exporter, or runtime failure on its existing
   operational/internal path; do not translate it into an operator-fixable validation error.

Do not add schema/semantic validation to `HandleUpdateTopology`. Its existing private-key custody
check, collision normalization, canonical marshal, version write, and audit remain the whole Save
boundary.

### Step 3 — Make the draft regression compact and state-safe

In `internal/api/deploy_force_preview_test.go`, keep one integration test named:

```go
func TestBlankTelemetryDestinationDraftReturnsStructuredValidationWithoutMutatingServedDeploy(t *testing.T)
```

Its exact sequence is:

1. Establish and promote one valid served generation.
2. Save one managed-node ICMP probe whose `host` is empty; Save must return 200.
3. Assert deploy-preview and stage return `422/topology_validation_failed` with the nested validator
   field/code/detail.
4. Assert the served generation and served bundles are byte/struct unchanged, and that no promotable
   staged set was created.
5. Remove the incomplete row and assert one preview and one stage succeed. Do not retain a second,
   redundant “fill the destination” recovery sequence.

This plan deliberately does not claim that Save leaves topology allocation pins unchanged: Save owns
normalization, while client-allocation semantics are plan 2. The required invariant here is that the
failed executable operation cannot change served or staged deployment state.

Keep `TestDeployHandlers_OperationalFaultsRemainInternal` as one preview fault plus one stage fault;
both must remain `internal/500`.

### Step 4 — Restrict the frontend compatibility latch

In `openDeployPreview` at `frontend/src/stores/controller/deploy.ts:101-137`, use this classification:

```ts
if (err instanceof ControllerError && (err.status === 404 || err.status === 405)) {
  set({ deployPreviewError: localized, deployPreviewing: false });
  return;
}
set({ error: localized, deployPreviewError: null, deployPreviewing: false });
```

- Clear `deployPreviewError` at the start of every new preview attempt.
- A success must leave the compatibility error cleared.
- 401, 403, 409, 422, 429, 5xx, malformed/error responses, timeouts, and network failures are blocking.
- Keep structured validation localization through the ordinary `localizeError` / `tError` path.

Create `frontend/src/stores/controllerStore.deployPreview.test.ts`, following the store/fetch setup in
`controllerStore.contextGeneration.test.ts`. Use one small status table plus one retry assertion:

- 404 and 405 produce `deployPreviewError` and no global `error`.
- 422 and one representative 500/network failure produce global `error` and a null
  `deployPreviewError`.
- Seed an old compatibility error, then prove a later success and a later blocking failure each clear
  that stale latch.

Do not create a full HTTP-status matrix and do not duplicate the Playwright flow.

### Step 5 — Format and run the exact focused gate

Run:

```bash
gofmt -w \
  internal/compiler/compiler.go \
  internal/compiler/compiler_test.go \
  internal/api/errmap.go \
  internal/api/errmap_test.go \
  internal/api/handler_deploy.go \
  internal/api/deploy_force_preview_test.go \
  internal/apierr/apierr.go \
  internal/apierr/apierr_test.go

GOSUMDB=sum.golang.org GOPROXY=https://proxy.golang.org,direct \
  go test ./internal/compiler ./internal/api ./internal/apierr \
  -run '^(TestCompilePreservesStructuredValidationFindings|TestMapTopologyValidationErr|TestBlankTelemetryDestinationDraftReturnsStructuredValidationWithoutMutatingServedDeploy|TestDeployHandlers_OperationalFaultsRemainInternal|TestRegistryBijection|TestNewUsesRegistryStatusAndMessage)$' \
  -count=1

cd frontend
npm run vitest -- \
  src/stores/controllerStore.deployPreview.test.ts \
  src/i18n/index.test.ts
cd ..
```

If the store test cannot drive `openDeployPreview` without broad new test infrastructure, stop and
draft an insertion plan instead of silently substituting a large Playwright run.

### Step 6 — Independent review, fixes, and clean re-review

- Spawn an independent reviewer after the focused gate is green. Require review of correctness,
  security boundary, old-controller compatibility, localization, test economy, and accidental
  plan-2/plan-3 scope leakage.
- Fix every actionable finding and rerun only the affected commands above.
- Spawn a fresh independent re-reviewer. Do not commit while any actionable finding remains.

### Step 7 — Commit and push the implementation

Because the starting worktree mixes future-plan hunks into shared test/i18n files, inspect the staged
patch and hunk-stage shared files. Stage only plan-1 changes:

```bash
git add \
  internal/compiler/compiler.go \
  internal/api/errmap.go \
  internal/api/errmap_test.go \
  internal/api/handler_deploy.go \
  internal/api/deploy_force_preview_test.go \
  internal/apierr/apierr.go \
  internal/apierr/apierr_test.go \
  frontend/src/stores/controller/deploy.ts \
  frontend/src/stores/controllerStore.deployPreview.test.ts \
  frontend/src/i18n/index.ts \
  frontend/src/i18n/index.test.ts
git add -p internal/compiler/compiler_test.go
git add -p frontend/src/i18n/messages/en.ts frontend/src/i18n/messages/zh.ts
git diff --cached --check
git diff --cached --stat
```

The cached diff must contain no client-allocation, audit, probe-name, URL, or device-telemetry work.
Then commit exactly:

```bash
GIT_AUTHOR_NAME='KunoriKiku' GIT_AUTHOR_EMAIL='rokuyanlin@gmail.com' \
GIT_COMMITTER_NAME='KunoriKiku' GIT_COMMITTER_EMAIL='rokuyanlin@gmail.com' \
git commit -m "$(cat <<'EOF'
fix(deploy): preserve validation boundaries
EOF
)"
```

Push exactly:

```bash
git push origin fix/rc12-telemetry-drafts
```

After the implementation push, let `execute-implementation-plan` create its separate outline-status
commit and invoke `close-phase`. Do not fold archive, decisions-log, specs, or `STATUS.md` bookkeeping
into the implementation commit; honor the close-phase status/archival prompts.

## Tests produced by this plan

- `internal/compiler/compiler_test.go` — `TestCompilePreservesStructuredValidationFindings`
  - **Lifetime:** perpetual (existing compiler-domain test file)
  - **Guards:** structured schema/semantic findings survive the public compiler facade.
  - **Retirement trigger:** never while controller/API callers depend on typed validation.
  - **Retirement destination:** none; remains in the compiler package.
- `internal/api/errmap_test.go` — `TestMapTopologyValidationErr`
  - **Lifetime:** perpetual (existing API-domain test file)
  - **Guards:** only typed validation maps to the stable 422 envelope.
  - **Retirement trigger:** never while this public HTTP contract exists.
  - **Retirement destination:** none; remains in the API package.
- `internal/api/deploy_force_preview_test.go` — blank-draft and operational-fault regressions
  - **Lifetime:** perpetual (existing API-domain test file)
  - **Guards:** draft Save versus deploy validation, no served/staged mutation, and 500 preservation.
  - **Retirement trigger:** never while draft probe editing and preview/stage exist.
  - **Retirement destination:** none; remains in the API package.
- `frontend/src/stores/controllerStore.deployPreview.test.ts`
  - **Lifetime:** perpetual (frontend store-domain test)
  - **Guards:** the compatibility bypass is available only for preview-route 404/405.
  - **Retirement trigger:** never while old-controller preview compatibility is supported.
  - **Retirement destination:** none; remains under `frontend/src/stores/`.
- `frontend/src/i18n/index.test.ts` — nested topology-validation localization
  - **Lifetime:** perpetual (existing i18n-domain test file)
  - **Guards:** validator codes/details are localized without parsing ad-hoc backend strings.
  - **Retirement trigger:** never while the structured validation envelope exists.
  - **Retirement destination:** none; remains in the i18n package.

## Definition of done

- [ ] An incomplete probe row saves as a draft but preview/stage return localized structured 422.
- [ ] Rejected preview/stage leave served generation, served bundles, and staged state unchanged.
- [ ] Operational faults remain blocking internal errors.
- [ ] “Deploy anyway” is reachable only after preview-route 404/405, with stale latches cleared.
- [ ] The exact focused gates pass.
- [ ] Independent review findings are fixed and the fresh re-review is clean.
- [ ] The scoped implementation commit is pushed; executor and close-phase bookkeeping complete
      separately.

## Out of scope for this plan

- Client allocation validation, reservation, normalization, or schema changes.
- Audit visibility and probe display names.
- URL probes, device discovery, device metrics, or chart framework extensions.
- Full Playwright/CI/release gates; those run once on the final candidate.
