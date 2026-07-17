# plan-3 — Quiet routine audits and finish display-only probe names

**Outline:** [outline.md](./outline.md)
**Subject:** deployment-stability-and-charted-telemetry-2026_07_17
**Track:** Go + frontend
**Depends on:** plans 1-2 completed and closed

## Goal

Keep routine agent reports out of new durable operator/security audit entries while preserving their
Fleet-state updates. Keep legacy `report` rows in the complete raw audit chain and API response, then
hide only those rows in the frontend after chain verification. Finish optional probe display names as
controller/browser metadata that cannot alter executable policy bytes, result/history identity,
bundle staging, or deployment generation.

## Prerequisites

- Verify plans 1 and 2 are `done` in [outline.md](./outline.md), their implementation commits are
  pushed, and their close-phase archives are complete.
- Read [outline.md](./outline.md), root `PRINCIPLES.md`, and the cited specs completely before editing.
- Preserve the existing dirty worktree. Hunk-stage shared model, i18n, wiredrift, and spec files so
  URL/device work from plans 4 onward remains uncommitted.
- Treat the raw audit chain as a compatibility and integrity boundary: do not delete, rewrite, or
  backend-filter legacy `report` rows.
- Treat probe `id` plus exact executable type/host/port as identity. `name` is display metadata only.

## Reads from specs

Reads from specs: controller-agent-api, controller-store, panel-deploy-fleet, model-validation

## Read first

Read these files completely; the line anchors identify the current implementation and test seams.

1. `internal/api/handler_agent.go:240-268,336-379` — routine report/telemetry state updates and stale
   audit comments.
2. `internal/api/handler_audit.go:15-35` — raw-chain listing and backend verification.
3. `internal/api/controller_http_test.go:394-435,1001-1039` — report-state and audit-wire tests.
4. `internal/controller/store.go:730-760` and `internal/controller/storecore.go:1275-1315` — audit
   append/list contract used to seed a legacy row in the API test.
5. `frontend/src/components/deploy/AuditLog.tsx:5-70` and
   `frontend/src/components/deploy/AuditLog.test.tsx:1-72` — presentation-only filtering.
6. `internal/model/topology.go:128-151` and `frontend/src/types/topology.ts:75-84` — mirrored optional
   probe-name DTO field.
7. `internal/probepolicy/policy.go:36-137,139-209` and
   `internal/probepolicy/policy_test.go:48-175` — runtime policy, private v1 wire, validation, marshal,
   and strict parse tests.
8. `internal/controller/compile_stage.go:235-290` and
   `internal/controller/telemetry_probe_name_delta_test.go:17-180` — executable-policy delta skip for
   name-only saves.
9. `frontend/src/components/deploy/TelemetryProbeEditor.tsx:5-121` and
   `frontend/src/components/deploy/TelemetryProbeEditor.test.tsx:1-62` — name editing and accessible
   validation.
10. `frontend/src/lib/probeResults.ts:97-150` and
    `frontend/src/lib/probeResults.test.ts:130-151` — display formatter, result matching, and policy
    equality.
11. `frontend/src/components/deploy/TelemetryProbeResults.tsx:43-187`,
    `frontend/src/components/deploy/TelemetryProbeResults.test.tsx:1-170`, and
    `frontend/src/components/deploy/NodeResourceHistory.tsx:459-485` — live/history labels.
12. `frontend/src/stores/controller/helpers.ts:61-143` — canonical design and `omitempty` behavior.
13. `internal/wiredrift/drift_test.go:257-293` — `TestFEOmitemptyListsMatchModel` mirror guard.
14. `frontend/src/i18n/messages/en.ts:638-685` and `frontend/src/i18n/messages/zh.ts:626-673` — probe
    editor strings.
15. `specs/controller-agent-api.md`, `specs/controller-store.md`, `specs/panel-deploy-fleet.md`, and
    `specs/model-validation.md:148-165` — active architectural descriptions to keep aligned.

## Implementation steps

### Step 1 — Separate routine Fleet state from durable audit

In `HandleReport`, retain request decoding, condition bounds, `SetAppliedGeneration`,
`TouchLastSeen`, not-found mapping, and the `{status:"ok"}` response. Do not call `AppendAudit`.

In `HandleTelemetry`, retain legacy and protocol-v2 recording exactly as-is and do not append audit.
Replace the stale comment that contrasts telemetry with “HandleReport's append” with one explicit
statement: both routine endpoints are intentionally outside the durable audit chain because their
useful high-frequency state is represented in Fleet.

Keep this backend regression in `internal/api/controller_http_test.go`:

```go
func TestHandleReport_UpdatesFleetStateWithoutFloodingAudit(t *testing.T)
```

It must send at least three reports, assert the latest applied generation/checksum/health/version and
`last_seen`-backed Fleet state, assert the audit length is unchanged, assert no new `report` action is
present, and verify the remaining chain.

Extend `TestControllerHTTP_AuditWireShape` to prove the backend half independently:

1. Enroll a node to create one meaningful audit entry.
2. Directly append a valid legacy `controller.AuditEntry{Action: "report", ...}` through the store.
3. GET `/api/v1/operator/audit`.
4. Assert both the meaningful row and legacy `report` row remain in `entries`, snake_case mapping is
   intact, and `verified` is true for the complete chain.

Do not make the backend handler omit `report`, and do not claim a component render test verifies the
cryptographic chain.

### Step 2 — Filter only the already-verified frontend presentation

In `AuditLog`, retain the complete fetched array and its controller-produced `auditVerified` state,
then derive only the table rows:

```ts
const visibleAudit = audit.filter((entry) => entry.action !== 'report');
```

- Use the full `audit.length` to decide whether the verified/unverified badge is shown.
- Use `visibleAudit` for the table and empty-state choice.
- Keep meaningful operator/security/lifecycle rows visible.
- Do not mutate controller-store state, remove rows during fetch, or add an API query/filter.

Keep `AuditLog.test.tsx` presentation-scoped: one render proves a legacy report is hidden while
meaningful rows and the complete-chain badge remain visible; one report-only render proves the
operator-facing empty state plus verification badge. Backend chain retention/verification belongs to
`TestControllerHTTP_AuditWireShape`.

### Step 3 — Complete optional, display-only probe names

Keep this mirrored optional field:

```go
Name string `json:"name,omitempty"`
```

```ts
name?: string;
```

`probepolicy.ValidateName` is authoritative and must accept empty names, require valid UTF-8, require
the value to equal `strings.TrimSpace(value)`, cap it at `MaxNameRunes == 128`, and reject every rune
for which `unicode.IsPrint` is false. Names are not unique.

Keep the frontend editor behavior equivalent in Unicode code points and printable single-line input:

- trim on blur and omit an empty result;
- mark an invalid live value with `aria-invalid`;
- connect the input to a stable error id with `aria-describedby`;
- render the localized error with `role="alert"`;
- add/retain `telemetryProbes.name`, `namePlaceholder`, and `nameInvalid` in English and Chinese.

Use one shared helper:

```ts
export function probeDisplayName(probe: Pick<TelemetryProbe, 'id' | 'name'>): string
```

It returns a non-empty name or falls back to immutable `id`. `TelemetryProbeResults` and
`NodeResourceHistory` must call it. `probeResultMatchesPolicy` must ignore `name`; `sameTelemetryPolicy`
must include `name` so a display edit can be saved. Keep exact executable/history identity as stable
id plus type/host/port.

Test only the boundary:

- extend `TestValidate_TypedKindsBoundsAndDefaults` with valid non-ASCII and invalid whitespace,
  control/format, and over-128-code-point names;
- add/retain one editor test named `marks an invalid display name with accessible feedback`;
- keep helper identity/display assertions in `probeResults.test.ts`;
- keep one representative latest-results render proving the display name appears with the immutable
  id/target fallback behavior. Do not snapshot every label consumer.

### Step 4 — Make generic JSON marshaling of runtime Policy fail closed

`telemetry.json` version 1 must continue to be produced only by `probepolicy.Marshal` through private
`policyWire` / `executableProbeWire`. `Parse` continues strict decoding through the private wire DTO
with `DisallowUnknownFields`, then constructs the public runtime view.

Make `Policy` explicitly runtime-only by removing its JSON tags and adding this value-receiver guard:

```go
var errPolicyRuntimeOnly = errors.New("probepolicy: Policy is a parsed runtime view; use Marshal for telemetry.json")

func (Policy) MarshalJSON() ([]byte, error) {
    return nil, errPolicyRuntimeOnly
}
```

Import `errors`. The value receiver intentionally blocks both `json.Marshal(policy)` and
`json.Marshal(&policy)`. Do not expose the private wire DTO or add `name` to it.

Add:

```go
func TestPolicyRejectsGenericJSONMarshal(t *testing.T)
```

It must call generic JSON marshal on a value and pointer, require an error and no usable bytes, and
confirm the error wraps or contains the runtime-only sentinel. Keep
`TestMarshal_DisplayNameDoesNotChangeExecutablePolicy` as an exact v1 byte/projection test: named and
unnamed probes produce identical compact bytes, `"name"` is absent, and strict Parse rejects a
handcrafted name field. Keep `TestParse_StrictVersionedCanonicalPolicy` rejecting all unknown fields,
unsupported versions, empty probe sets, and trailing JSON.

### Step 5 — Preserve name-only save without executable deployment churn

In the canonical controller design comparison, include `name` so Save persists the metadata. In the
executable policy comparison inside `internal/controller/compile_stage.go`, compare only fields that
enter `telemetry.json`; a name-only change must not produce a bundle delta.

Keep `TestTelemetryProbeNameOnlyChangeDoesNotRestageBundles` as the integration guard. It must prove:

1. the renamed topology is saved and returned;
2. `telemetry.json` bytes remain exact;
3. no new staged generation or bundles appear; and
4. the served generation does not advance.

Update `TestFEOmitemptyListsMatchModel` so the mirrored frontend optional-name field remains covered by
the ordinary Go/TypeScript wire-drift check.

### Step 6 — Update active specifications and comments

Align the active specs with these boundaries:

- routine report/telemetry calls update Fleet state without durable audit append;
- the raw legacy chain is still returned and verified before frontend presentation filtering;
- name is display-only metadata excluded from executable policy and report identity;
- generic JSON marshal of the runtime `Policy` is deliberately rejected.

Do not edit archived investigation documents or add URL/device semantics to these descriptions.

### Step 7 — Format and run the exact focused gate

Run:

```bash
gofmt -w \
  internal/api/handler_agent.go \
  internal/api/controller_http_test.go \
  internal/model/topology.go \
  internal/probepolicy/policy.go \
  internal/probepolicy/policy_test.go \
  internal/controller/telemetry_probe_name_delta_test.go \
  internal/wiredrift/drift_test.go

GOSUMDB=sum.golang.org GOPROXY=https://proxy.golang.org,direct \
  go test ./internal/api ./internal/probepolicy ./internal/controller ./internal/wiredrift \
  -run '^(TestHandleReport_UpdatesFleetStateWithoutFloodingAudit|TestControllerHTTP_AuditWireShape|TestValidate_TypedKindsBoundsAndDefaults|TestMarshal_DisplayNameDoesNotChangeExecutablePolicy|TestParse_StrictVersionedCanonicalPolicy|TestPolicyRejectsGenericJSONMarshal|TestTelemetryProbeNameOnlyChangeDoesNotRestageBundles|TestFEOmitemptyListsMatchModel)$' \
  -count=1

cd frontend
npm run vitest -- \
  src/components/deploy/AuditLog.test.tsx \
  src/components/deploy/TelemetryProbeEditor.test.tsx \
  src/lib/probeResults.test.ts \
  src/components/deploy/TelemetryProbeResults.test.tsx
cd ..
```

### Step 8 — Independent review, fixes, and clean re-review

- Spawn an independent reviewer after the exact focused gate is green.
- Require separate findings for backend chain retention/verification and frontend presentation
  filtering; reject any review claim that conflates them.
- Also review report state preservation, JSON fail-closed behavior, Go/TypeScript validation parity,
  display versus identity separation, no-restage behavior, i18n/accessibility, and test economy.
- Fix every actionable finding, rerun only affected commands from Step 7, then spawn a fresh reviewer.
- Do not commit until the re-review has no actionable finding.

### Step 9 — Commit and push the implementation

Stage complete plan-3 files and hunk-stage shared files that may contain plan-2 or plan-4+ work:

```bash
git add \
  internal/api/handler_agent.go \
  internal/api/controller_http_test.go \
  internal/probepolicy/policy.go \
  internal/probepolicy/policy_test.go \
  internal/controller/telemetry_probe_name_delta_test.go \
  frontend/src/components/deploy/AuditLog.tsx \
  frontend/src/components/deploy/AuditLog.test.tsx \
  frontend/src/components/deploy/TelemetryProbeEditor.tsx \
  frontend/src/components/deploy/TelemetryProbeEditor.test.tsx \
  frontend/src/components/deploy/TelemetryProbeResults.tsx \
  frontend/src/components/deploy/TelemetryProbeResults.test.tsx \
  frontend/src/components/deploy/NodeResourceHistory.tsx \
  frontend/src/lib/probeResults.ts \
  frontend/src/lib/probeResults.test.ts
git add -p internal/model/topology.go internal/wiredrift/drift_test.go
git add -p frontend/src/types/topology.ts frontend/src/stores/controller/helpers.ts
git add -p frontend/src/i18n/messages/en.ts frontend/src/i18n/messages/zh.ts
git add -p specs/controller-agent-api.md specs/controller-store.md specs/panel-deploy-fleet.md specs/model-validation.md
git diff --cached --check
git diff --cached --stat
```

The cached diff must contain no client-allocation, URL-probe, device-discovery, device-metric, chart,
release, or closure work. Commit exactly:

```bash
GIT_AUTHOR_NAME='KunoriKiku' GIT_AUTHOR_EMAIL='rokuyanlin@gmail.com' \
GIT_COMMITTER_NAME='KunoriKiku' GIT_COMMITTER_EMAIL='rokuyanlin@gmail.com' \
git commit -m "$(cat <<'EOF'
fix(telemetry): quiet reports and label probes
EOF
)"
```

Push exactly:

```bash
git push origin fix/rc12-telemetry-drafts
```

After the implementation push, let `execute-implementation-plan` create its separate outline-status
commit and invoke `close-phase`. Do not fold archive, decisions-log, `STATUS.md`, or specs-refresh
bookkeeping into the implementation commit.

## Tests produced by this plan

- `internal/api/controller_http_test.go` —
  `TestHandleReport_UpdatesFleetStateWithoutFloodingAudit`
  - **Lifetime:** perpetual (existing API-domain test file)
  - **Guards:** reports still advance Fleet state without adding durable audit noise.
  - **Retirement trigger:** never while routine agent reports and the durable audit chain coexist.
  - **Retirement destination:** none; remains in the API package.
- `internal/api/controller_http_test.go` — `TestControllerHTTP_AuditWireShape`
  - **Lifetime:** perpetual (existing API-domain test file)
  - **Guards:** the API returns and verifies the complete legacy-compatible chain, including old
    `report` rows.
  - **Retirement trigger:** only after an explicit audit-chain version migration removes legacy-row
    compatibility.
  - **Retirement destination:** migration-specific API compatibility tests, if that migration occurs.
- `internal/probepolicy/policy_test.go` — validation, exact v1 marshal, strict parse, and generic-marshal
  rejection tests
  - **Lifetime:** perpetual (policy wire/security boundary)
  - **Guards:** display names stay out of executable bytes and runtime `Policy` cannot become an
    accidental alternate serializer.
  - **Retirement trigger:** only when telemetry policy v1 is formally retired.
  - **Retirement destination:** the successor version's policy compatibility suite.
- `internal/controller/telemetry_probe_name_delta_test.go` —
  `TestTelemetryProbeNameOnlyChangeDoesNotRestageBundles`
  - **Lifetime:** perpetual (controller staging-domain test)
  - **Guards:** display-only saves persist without executable bundle/generation churn.
  - **Retirement trigger:** never while probe display metadata and staged deployment are separate.
  - **Retirement destination:** none; remains in the controller package.
- `frontend/src/components/deploy/AuditLog.test.tsx`
  - **Lifetime:** perpetual (frontend presentation-domain test)
  - **Guards:** legacy routine rows are hidden only in the table while meaningful rows and the full
    chain status remain visible.
  - **Retirement trigger:** only when legacy `report` rows can no longer be returned by supported
    controllers.
  - **Retirement destination:** none; remains with `AuditLog`.
- `frontend/src/components/deploy/TelemetryProbeEditor.test.tsx`
  - **Lifetime:** perpetual (frontend editor-domain test)
  - **Guards:** invalid display names receive localized, accessible feedback.
  - **Retirement trigger:** never while the editable optional name exists.
  - **Retirement destination:** none; remains with the editor.
- `frontend/src/lib/probeResults.test.ts` and
  `frontend/src/components/deploy/TelemetryProbeResults.test.tsx`
  - **Lifetime:** perpetual (shared identity/presentation and representative render tests)
  - **Guards:** display-name fallback and name-independent result identity.
  - **Retirement trigger:** only if the shared probe identity contract is replaced.
  - **Retirement destination:** the successor shared identity helper tests.
- `internal/wiredrift/drift_test.go` — `TestFEOmitemptyListsMatchModel`
  - **Lifetime:** perpetual (wire-drift guard)
  - **Guards:** Go and TypeScript optional probe-name JSON shape remain aligned.
  - **Retirement trigger:** never while DTOs are hand mirrored.
  - **Retirement destination:** generated-contract tests if hand mirroring is eliminated.

## Definition of done

- [ ] Repeated reports update applied generation, health, version, conditions/liveness, and Fleet
      state without creating new audit entries.
- [ ] The backend audit API still returns legacy `report` rows and verifies the complete raw chain.
- [ ] The frontend hides only legacy report rows after fetch while preserving the full-chain badge.
- [ ] Optional names obey the Go/TypeScript bounded printable contract and have accessible editor
      feedback.
- [ ] Live and history views use the shared display-name fallback without changing result identity.
- [ ] Generic `json.Marshal` of runtime `Policy` fails; canonical `Marshal` bytes and strict Parse stay
      version-1 compatible.
- [ ] A name-only Save persists metadata without staging bundles or advancing generation.
- [ ] The exact focused gates pass, every review finding is fixed, and a fresh re-review is clean.
- [ ] The scoped implementation commit is pushed; executor and close-phase bookkeeping complete
      separately.

## Out of scope for this plan

- Deleting, compacting, rehashing, or changing the wire format of historical audit entries.
- Adding new audit categories or moving high-frequency reports to another durable chain.
- Making probe names unique or using them in executable/result/history identity.
- Client-allocation repair from plan 2.
- URL probes, automatic disk/GPU discovery, device metrics, chart framework extensions, or releases.
- Full Playwright/CI/release gates; those run once on the final candidate.
