# plan-5 — Add constrained URL probes end to end

**Outline:** [outline.md](./outline.md) — read it completely before this plan, especially Principles,
Standing rules, the plan-5 milestone, and the plan-status table.

**Subject:** deployment-stability-and-charted-telemetry-2026_07_17
**Track:** Go + frontend
**Depends on:** plan-4

## Prerequisites

- Plan 4 is `done`, pushed, and closed. Its exact v1 golden, exclusive successor member, latest-heartbeat
  `agent_capabilities`, feature-specific installer markers, and upgrade-agents-first projection are green.
- The current branch is `fix/rc12-telemetry-drafts`; preserve unrelated dirty-tree changes and stage only
  paths named here.
- URL mechanics in this file are implementation decisions, not additions to `PRINCIPLES.md`.
- Do not start if plan 4 collapsed v1 and successor parsing into one permissive decoder or if URL support
  cannot have its own `url-probes-v1` heartbeat and installer capability. Repair plan 4 first.

## Goal

Add a distinct signed URL probe with fixed GET semantics. One configured exact HTTP status determines
success, defaulting to 200. Completed HTTP responses contribute latency and availability history even
when the code mismatches; the actual returned code remains bounded latest-result metadata and is never
turned into a chart series.

## Reads from specs

Reads from specs: model-validation, agent, controller-agent-api, controller-store, panel-deploy-fleet

## Read first

Line anchors are against the pre-plan-5 tree; relocate by symbol after plan 4 if necessary.

1. `internal/model/topology.go:128-170` and `frontend/src/types/topology.ts:69-95` — typed probe model.
2. `internal/probepolicy/policy.go:20-260` — frozen v1 API plus plan-4 successor DTO,
   `RequiredCapabilities`, and `ProjectLegacy`.
3. `internal/runtimecontract/installer_capability.go:1-45`,
   `internal/agent/installer_command_unix.go:33-75`, and
   `internal/renderer/script.go:100-125,230-250,465-525` — feature capability plumbing.
4. `internal/agent/probe_runner.go:27-220,258-505` — scheduler, attempt seam, elapsed measurement, DNS,
   TCP, and ICMP.
5. `internal/probemetric/result.go:20-190` — strict latest/recent result, completed-attempt rules, and
   exact series identity.
6. `internal/controller/telemetry_history.go:77-137,216-405,629-715,1401-1515` — probe projection,
   dedupe, storage, and exact query filter.
7. `internal/api/telemetry_history.go:27-112,153-197,310-620` — 1000-bucket budget, probe aggregation,
   family encoder, and query parsing.
8. `frontend/src/components/deploy/TelemetryProbeEditor.tsx:1-263`,
   `frontend/src/components/deploy/TelemetryProbeResults.tsx:1-188`, and
   `frontend/src/components/deploy/NodeResourceHistory.tsx:94-545` — Fleet editor/live/history.
9. `frontend/src/lib/probeResults.ts:1-220` and `frontend/src/lib/telemetryHistory.ts:23-460` — strict
   browser mapping, identity matching, query construction, and chart-family parser.
10. `internal/agent/probe_runner_test.go:1-821`, `internal/probemetric/result_test.go:1-120`,
    `internal/controller/telemetry_history_test.go:1-1081`,
    `internal/api/telemetry_history_test.go:1-581`,
    `frontend/src/components/deploy/TelemetryProbeEditor.test.tsx:1-62`,
    `frontend/src/components/deploy/TelemetryProbeResults.test.tsx:1-75`, and
    `frontend/src/components/deploy/NodeResourceHistory.test.tsx:1-239` — existing fixtures to extend
    rather than duplicate.

## Implementation steps

### Step 1 — Add a closed URL policy type only to the successor schema

At `internal/model/topology.go:143-165`, extend the runtime topology probe while keeping legacy JSON
fields optional:

```go
const TelemetryProbeURL = "url"

type TelemetryProbe struct {
    ID                  string `json:"id"`
    Name                string `json:"name,omitempty"`
    Type                string `json:"type"`
    Host                string `json:"host,omitempty"`
    Port                int    `json:"port,omitempty"`
    URL                 string `json:"url,omitempty"`
    ExpectedStatus      int    `json:"expected_status,omitempty"`
    IntervalSeconds     int    `json:"interval_seconds,omitempty"`
    TimeoutMilliseconds int    `json:"timeout_milliseconds,omitempty"`
}
```

Mirror it as a discriminated TypeScript union so impossible field combinations are not normal editor
state:

```ts
export type TelemetryProbe = TelemetryProbeBase & (
  | { type: 'icmp'; host: string; port?: never; url?: never; expected_status?: never }
  | { type: 'tcp'; host: string; port: number; url?: never; expected_status?: never }
  | { type: 'url'; url: string; expected_status?: number; host?: never; port?: never }
);
```

At the plan-4 successor policy seam, add:

```go
const (
    DefaultExpectedStatus = 200
    MaxURLBytes            = 2048
)

func EffectiveExpectedStatus(probe model.TelemetryProbe) int
func ValidateURL(raw string) error
```

Validation rules:

- URL is non-empty, already trimmed, valid UTF-8, at most 2048 bytes, absolute, and has scheme exactly
  `http` or `https` plus a non-empty host.
- Hostname, IP literal, DNS name, query, path, and explicit port are allowed. A separate DNS field is
  neither required nor added.
- Reject userinfo, fragments, control characters, unsupported schemes, malformed ports, and any
  simultaneous host/port fields.
- `expected_status == 0` means 200 in topology/runtime form; successor canonical JSON always writes the
  effective value explicitly. A configured value must be 100–599.
- `Marshal`/`Parse` v1 reject URL and remain byte-identical. `MarshalSuccessor`/`ParseSuccessor` use the
  URL fields. `RequiresSuccessor`, `RequiredCapabilities`, and `ProjectLegacy` respectively identify URL,
  require `telemetry-policy-v2` plus `url-probes-v1`, and omit URL rows from the phase-one v1 projection.
- Optional display `Name` remains excluded from both executable policy versions and result identity.

### Step 2 — Require the exact URL execution capability before mutation

Update plan-4 capability producers so this binary now advertises `url-probes-v1` in the bounded
`agent_capabilities` metric and sets `YAOG_AGENT_CAP_URL_PROBES_V1=1` after stripping inherited values.

Successor installers carrying any URL row require both generic v2 and URL-v1 markers. A generic-v2
agent built before this plan therefore fails before ordinary host mutation instead of applying the
network generation and silently ignoring URL policy. Device capability remains absent.

Extend the plan-4 readiness test with one compact case: generic v2 alone still blocks a URL design;
generic v2 plus URL-v1 allows it.

### Step 3 — Return structured attempt outcomes from the scheduler

The current `probeAttemptFunc` returns only a failure string, which cannot represent a completed HTTP
response whose status mismatched. Replace it at `internal/agent/probe_runner.go:45-70` with:

```go
type probeAttemptOutcome struct {
    FailureReason    string
    ResponseComplete bool
    ActualStatus     int
}

type probeAttemptFunc func(context.Context, model.TelemetryProbe) probeAttemptOutcome
```

ICMP/TCP wrappers preserve their current classifications with `ResponseComplete=false` on failure and
an empty outcome on success. In `activeProbeSampler.execute`:

- Measure elapsed with the existing monotonic clock.
- Attach latency when the outcome is success or `ResponseComplete`; therefore an unexpected HTTP
  status retains latency.
- Attach actual status only for a completed URL response.
- Cancellation caused by policy replacement still discards the in-flight result.
- Panic recovery returns the closed `network_error` outcome without raw error text.

Update `configuredProbeResult` and `probeResultMatchesProbe` to be type-aware. URL matching uses exact
URL plus effective expected status; legacy matching remains exact id/type/host/port.

### Step 4 — Implement one bounded HTTP GET attempt

Add `internal/agent/probe_url.go` with:

```go
const maxURLResponseHeaderBytes = 32 << 10

func newURLProbeClient(timeout time.Duration) *http.Client
func performURLProbeAttempt(ctx context.Context, probe model.TelemetryProbe) probeAttemptOutcome
```

`newURLProbeClient` uses a dedicated `http.Transport` and `http.Client`:

- `Proxy` is nil; never call `http.ProxyFromEnvironment` and never honor ambient proxy variables.
- Ordinary Go TLS certificate and hostname verification remains enabled; no insecure override.
- `performURLProbeAttempt` resolves the already-validated effective signed probe timeout and passes it
  to `newURLProbeClient(timeout)`. Request context bounds DNS, connect, TLS handshake, and
  response-header wait. Set bounded transport TLS/header timeouts no longer than that timeout and set
  `MaxResponseHeaderBytes` to 32 KiB.
- Disable automatic compression and keepalive for this one-shot measurement so the agent does not
  download/decompress content or reuse an old connection as a different latency contract.
- `CheckRedirect` returns `http.ErrUseLastResponse`. The first 3xx is a completed response whose code
  may itself be the configured expected code; no redirect target is contacted.

`performURLProbeAttempt` creates exactly one `GET` with no body and no operator-configurable headers,
authentication, cookies, client certificate, method, or redirect behavior. Close `resp.Body` immediately
without reading it. Compare the first response status to the effective expected status:

- equal: success, `ResponseComplete=true`, actual code present;
- unequal: `unexpected_status`, `ResponseComplete=true`, actual code present;
- DNS/connect/TLS/timeout/transport error: existing bounded failure category, no latency fabricated by
  the outcome and no actual code.

Private, loopback, link-local, overlay, and internal DNS destinations are intentionally allowed when
explicitly included in the signed off-host-keystone-bound policy. The controller and browser never
perform the request. Safety comes from the fixed request shape, policy limits, no proxy, no redirects,
and normal TLS—not from client-side address blocking that would defeat internal monitoring.

### Step 5 — Extend strict result identity without charting HTTP codes

At `internal/probemetric/result.go:22-185`, add:

```go
const FailureUnexpectedStatus = "unexpected_status"

type Result struct {
    ID             string   `json:"id"`
    Type           string   `json:"type"`
    Host           string   `json:"host,omitempty"`
    Port           int      `json:"port,omitempty"`
    URL            string   `json:"url,omitempty"`
    ExpectedStatus int      `json:"expected_status,omitempty"`
    ActualStatus   int      `json:"actual_status,omitempty"`
    Status         string   `json:"status"`
    LatencyMS      *float64 `json:"latency_ms,omitempty"`
    CheckedAt      string   `json:"checked_at,omitempty"`
    FailureReason  string   `json:"failure_reason,omitempty"`
    IntervalMS     int64    `json:"interval_ms,omitempty"`
}
```

Strict invariants:

- ICMP/TCP accept none of the URL/status fields; their existing series hash bytes remain unchanged.
- URL pending has exact URL/expected status but no checked time, latency, actual code, or failure.
- URL success requires checked time, finite nonnegative latency, actual status equal to expected, and no
  failure reason.
- `unexpected_status` requires checked time, finite nonnegative latency, actual status 100–599 unequal
  to expected.
- URL transport failures have checked time but no latency or actual code.
- URL `SeriesID` hashes id, type, exact URL, and effective expected status. Changing the success contract
  starts a new history; it never changes ICMP/TCP IDs.

Actual status is categorical latest metadata despite its integer representation. Do not add it to a
numeric metric registry or history bucket.

### Step 6 — Reuse probe history for response latency and availability

Extend the probe history DTO/filter at `internal/controller/telemetry_history.go:77-137,377-405` with
URL and expected-status identity. Do not persist `ActualStatus`; retain only status/failure and latency.
Controller ingestion continues bounding agent clocks against the outer telemetry sample.

At `internal/api/telemetry_history.go:354-488`:

- Aggregate availability from every completed attempt.
- Aggregate latency from every sample carrying `LatencyMS`, including `unexpected_status`; transport
  failures remain latency gaps.
- Keep `failure_reasons.unexpected_status` counts.
- Add exact URL selector fields `probe_url` and `probe_expected_status`; URL selection forbids host/port,
  while existing ICMP/TCP query semantics remain unchanged.
- Return URL and expected status in series metadata. Never return actual status in buckets.
- Keep the existing global 1000-bucket budget and exact selected-series pushdown.

Update `frontend/src/lib/telemetryHistory.ts` so a latency aggregate is valid whenever present and
nonnegative, not only when `successes > 0`. Extend its strict URL series parser/query builder and keep
the existing probe chart family.

### Step 7 — Add the Fleet editor and live/result presentation

At `TelemetryProbeEditor.tsx`, add URL to the type selector. A new URL row defaults to expected status
200; changing type clears fields not valid for the destination kind. Expose optional name, URL, expected
status, interval, and timeout with accessible validation. Do not add DNS as another mandatory field.

At `probeResults.ts` and `TelemetryProbeResults.tsx`:

- Parse the strict URL shape and `unexpected_status`.
- Match drafts using id + exact URL + effective expected status.
- Display expected and latest actual code as text/badges. Distinguish mismatch from transport failure.
- Keep actual code outside charts and persisted controller state.

At `NodeResourceHistory.tsx`, URL rows use the existing selected-probe latency and availability charts;
do not add a chart family or status-code chart. Add English and Chinese strings together.

### Step 8 — Verification gate

Run exactly:

```bash
gofmt -w \
  internal/model/topology.go internal/probepolicy/policy.go \
  internal/runtimecontract/installer_capability.go internal/agent/installer_command_unix.go \
  internal/agent/probe_runner.go internal/agent/probe_url.go internal/probemetric/result.go \
  internal/controller/telemetry_history.go internal/api/telemetry_history.go

GOSUMDB=sum.golang.org GOPROXY=https://proxy.golang.org,direct \
go test ./internal/probepolicy ./internal/runtimecontract ./internal/renderer ./internal/agent \
  ./internal/probemetric ./internal/controller ./internal/api ./internal/wiredrift \
  -run 'URL|HTTP|UnexpectedStatus|ProbeSeries|InstallerCapability|Readiness|History' -count=1

cd frontend
npm run vitest -- \
  src/components/deploy/TelemetryProbeEditor.test.tsx \
  src/components/deploy/TelemetryProbeResults.test.tsx \
  src/components/deploy/NodeResourceHistory.test.tsx \
  src/lib/probeResults.test.ts \
  src/lib/telemetryHistory.test.ts
```

The focused HTTP matrix must include default 200, configured 204, mismatched 500 retaining latency and
code, a redirect whose target is never contacted, timeout, malformed/oversized response headers, and
an ambient proxy variable that is ignored. Use one table/subtests rather than separate test files.

Stop and fix before review if URL can enter v1, a generic-v2-only agent can apply it, a redirect is
followed, a proxy is honored, internal signed targets are rejected, status mismatch loses latency, or
actual status appears in history buckets.

### Step 9 — Independent review, fix, re-review

Review specifically for the fixed request surface, ordinary TLS, context/header bounds, response-body
handling, private/internal target intent, capability gating, type-aware strict decoding, unchanged
legacy identities, chart semantics, browser custody, accessibility/i18n, and redundant tests. Fix all
findings and obtain a clean re-review before commit.

### Step 10 — Exact commit and push

Verify `git diff --name-only` is plan-owned, then run:

```bash
git add \
  internal/model/topology.go internal/probepolicy internal/runtimecontract/installer_capability.go \
  internal/agent/installer_command_unix.go internal/agent/telemetry_capabilities.go \
  internal/agent/probe_runner.go internal/agent/probe_url.go internal/agent/probe_runner_url_test.go \
  internal/probemetric internal/renderer/script.go \
  internal/renderer/script_telemetry_capability_test.go \
  internal/renderer/templates/install.sh.tmpl internal/renderer/templates/client-install.sh.tmpl \
  internal/controller/telemetry_history.go internal/controller/telemetry_history_test.go \
  internal/controller/telemetry_policy_readiness_test.go internal/api/telemetry_history.go \
  internal/api/telemetry_history_test.go internal/wiredrift/drift_test.go \
  frontend/src/types frontend/src/lib/probeResults.ts frontend/src/lib/probeResults.test.ts \
  frontend/src/lib/telemetryHistory.ts frontend/src/lib/telemetryHistory.test.ts \
  frontend/src/components/deploy/TelemetryProbeEditor.tsx \
  frontend/src/components/deploy/TelemetryProbeEditor.test.tsx \
  frontend/src/components/deploy/TelemetryProbeResults.tsx \
  frontend/src/components/deploy/TelemetryProbeResults.test.tsx \
  frontend/src/components/deploy/NodeResourceHistory.tsx \
  frontend/src/components/deploy/NodeResourceHistory.test.tsx \
  frontend/src/i18n/messages/en.ts frontend/src/i18n/messages/zh.ts
GIT_AUTHOR_NAME='KunoriKiku' GIT_AUTHOR_EMAIL='rokuyanlin@gmail.com' \
GIT_COMMITTER_NAME='KunoriKiku' GIT_COMMITTER_EMAIL='rokuyanlin@gmail.com' \
git commit -F - <<'EOF'
feat(telemetry): add URL probes

Add signed fixed-GET URL checks with exact expected-status success, latest actual-code metadata, and
shared latency/availability history. Keep redirects, proxies, arbitrary requests, and status-code
charts outside the execution surface.
EOF
git push origin fix/rc12-telemetry-drafts
```

Then allow the executor’s separate outline-status commit and close-phase handoff.

## Tests produced by this plan

- `internal/probepolicy/policy_url_test.go`
  - **Lifecycle:** perpetual
  - **Guards:** URL is successor-only, default 200 is canonical, invalid request surfaces are rejected, and legacy v1 bytes remain unchanged.
  - **Retirement trigger:** never while URL probes or v1 compatibility are supported.
  - **Retirement destination:** `tests/legacy/perpetual/internal/probepolicy/policy_url_test.go`.
- `internal/agent/probe_runner_url_test.go`
  - **Lifecycle:** perpetual
  - **Guards:** the bounded fixed GET transport, redirect/proxy/TLS/header behavior, and mismatch latency/code contract.
  - **Retirement trigger:** never while agents execute URL policy.
  - **Retirement destination:** `tests/legacy/perpetual/internal/agent/probe_runner_url_test.go`.
- `internal/probemetric/result_test.go`
  - **Lifecycle:** perpetual
  - **Guards:** strict URL latest/recent rows, mismatch latency, categorical actual code, and unchanged ICMP/TCP series identities.
  - **Retirement trigger:** never while probe history remains backward compatible.
  - **Retirement destination:** `tests/legacy/perpetual/internal/probemetric/result_test.go`.
- `internal/api/telemetry_history_test.go`
  - **Lifecycle:** perpetual
  - **Guards:** exact URL-series selection, latency for completed mismatches, availability/failure counts, bucket budget, and absence of actual status from history.
  - **Retirement trigger:** never while URL charts use the probe family.
  - **Retirement destination:** `tests/legacy/perpetual/internal/api/telemetry_history_test.go`.
- `frontend/src/components/deploy/TelemetryProbeEditor.test.tsx`
  - **Lifecycle:** perpetual
  - **Guards:** accessible URL editing with default expected status and type-safe field clearing.
  - **Retirement trigger:** never while Fleet edits URL probes.
  - **Retirement destination:** `tests/legacy/perpetual/frontend/TelemetryProbeEditor.test.tsx`.
- `frontend/src/components/deploy/TelemetryProbeResults.test.tsx`
  - **Lifecycle:** perpetual
  - **Guards:** latest expected/actual status presentation and mismatch versus transport-failure distinction.
  - **Retirement trigger:** never while URL latest results are shown.
  - **Retirement destination:** `tests/legacy/perpetual/frontend/TelemetryProbeResults.test.tsx`.
- `frontend/src/components/deploy/NodeResourceHistory.test.tsx`
  - **Lifecycle:** perpetual
  - **Guards:** URL latency/availability reuse the shared probe charts and no status-code chart is rendered.
  - **Retirement trigger:** never while chart-first URL telemetry is supported.
  - **Retirement destination:** `tests/legacy/perpetual/frontend/NodeResourceHistory.test.tsx`.

## Definition of done

- [ ] URL is a strict successor-only probe and requires generic-v2 plus URL-v1 capabilities.
- [ ] Fixed GET uses ordinary TLS, no proxy, no redirects, bounded time/headers, and no response-body read.
- [ ] Explicitly signed internal/private/DNS/IP destinations work; controller and browser never probe them.
- [ ] Default/configured exact status determines success; mismatch retains latency and actual code.
- [ ] Actual code is latest categorical metadata only; latency and availability use the shared probe history/charts.
- [ ] URL/expected-status changes separate history, while all legacy ICMP/TCP identities remain unchanged.
- [ ] Focused tests, independent review, fix, and re-review pass; commit/push and close-phase handoff complete.

## Out of scope

- Redirect following, arbitrary methods/headers/body/authentication, cookies, content assertions,
  certificate overrides, URL-body storage, or status-code charts.
- Device collection/history/UI.
- WebSocket/gRPC telemetry transport, controller-side probing, or client-side private-address blocking.
- Editing the outline, STATUS, plans other than this plan during implementation, release tags, releases,
  or container references.
