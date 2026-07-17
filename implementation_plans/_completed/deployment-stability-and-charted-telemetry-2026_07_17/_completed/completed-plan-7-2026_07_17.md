# plan-7 — Activate device telemetry, history, and Fleet charts

**Outline:** [outline.md](./outline.md)
**Subject:** deployment-stability-and-charted-telemetry-2026_07_17
**Track:** Go + frontend
**Depends on:** plan-6 (and transitively plan-4)

## Prerequisites

- Plans 4 and 6 are completed, pushed, and closed. Plan 6's bounded collectors and stable opaque
  device identifiers are the source of truth; do not widen their cardinality, command, output, or
  deadline limits during UI integration.
- Read `outline.md` and root `PRINCIPLES.md` before editing.
- Confirm the worktree contains no unrelated changes. Preserve any user-owned changes and stop if the
  target files overlap unexpectedly.
- Keep plan 5 independent: URL probes may already be present, but device history must not alter the
  existing resource/probe wire shape or selector semantics.

## Reads from specs

Reads from specs: agent, controller-agent-api, controller-store, panel-deploy-fleet, artifacts-signing

## Goal

Atomically connect the signed device-policy opt-in, plan-6 collectors, controller latest/history path,
exact-series API, browser custody boundary, and Fleet live/history UI. Production activation must use
two separate telemetry metrics: `device_inventory` for bounded categorical live state and
`device_samples` for numeric chart data. Every numeric definition must have exact producer,
projector, encoder, frontend parser, and renderer parity before the sampler is registered.

## Read first

Read these files in order, re-grepping the named symbol if prior plans shifted an anchor:

1. `internal/devicemetric/` — all plan-6 files; collector DTOs, stable IDs, validation, limits, and
   fixtures. This package does not exist on the pre-plan-6 baseline, so read it in full.
2. `internal/telemetrymetric/catalog.go:20-100,131-210` — closed chart families, metric definitions,
   and exact catalog validation.
3. `internal/agent/telemetry.go:42-49,158-226` — sampler declarations, production registration, and
   catalog parity.
4. `internal/agent/heartbeat_reliable.go` — bounded replay payload and negotiated telemetry delivery;
   re-grep `metrics` and the v2 receipt capability before editing.
5. `internal/controller/telemetry_history.go:253-367` — history projection/accumulation registries and
   exact catalog parity; also read `TelemetryHistoryQueryOptions` and
   `querySnapshotFilteredContext` in the same file before adding device pushdown.
6. `internal/controller/telemetry_history.go:898-940` — existing disk-scan exact-series regression.
7. `internal/api/telemetry_history.go:27-44,490-625` — global 1000-bucket budget, family encoders, and
   exact-selector parsing.
8. `frontend/src/lib/telemetryHistory.ts:23-37,47-120,178-199,387-417` — exhaustive family literal,
   typed response, request options/query, and parser registry.
9. `frontend/src/components/deploy/NodeResourceHistory.tsx:41-125,340-543,545-564` — production renderer
   registry, component-local request state, probe selector, and shared `TimeSeriesChart` adapter.
10. `frontend/src/components/pages/FleetNodeDetailPage.tsx:20-24,45-76,149-193,254-265` — Fleet-owned
    policy, latest observations, lazy history, and Save-versus-Deploy boundary.
11. `frontend/src/api/controller/fleet.ts:21-55,121-164` and
    `frontend/src/types/controller.ts:35-77` — live node wire mapping and live DTO ownership.
12. `frontend/src/lib/custody.ts:77-100` and `frontend/src/stores/controller/persist.ts:14-45` — the
    browser persistence boundary.
13. `internal/wiredrift/drift_test.go:361-398,400-471` — probe/family/controller DTO parity gates.
14. `internal/telemetrymetric/catalog_test.go`, `internal/agent/telemetry_liveness_test.go:58-113`,
    `internal/controller/telemetry_history_test.go:64-115,898-940`,
    `internal/api/telemetry_history_test.go:36-99,385-422`,
    `frontend/src/lib/telemetryHistory.test.ts`,
    `frontend/src/components/deploy/NodeResourceHistory.test.tsx`, and
    `frontend/src/lib/custody.test.ts` — existing high-value test seams to extend rather than duplicate.

## Implementation steps

### Step 1 — Make the leaf metric split explicit

In `internal/telemetrymetric`, add the new closed family and two top-level metric definitions:

```go
const ChartFamilyDevice ChartFamily = "device"

const (
    DeviceInventoryKey = "device_inventory"
    DeviceSamplesKey   = "device_samples"
)

var DeviceInventory = Definition{
    Key: DeviceInventoryKey,
    History: HistoryLiveOnly,
    LiveSurface: LiveSurfaceVisible,
    LiveOnlyReason: "device identity, support, mount, and truncation state are current categorical inventory rather than numeric time series",
}

var DeviceSamples = Definition{
    Key: DeviceSamplesKey,
    History: HistoryCharted,
    ChartFamily: ChartFamilyDevice,
    HistoryPriority: /* next unique positive priority */,
    LiveSurface: LiveSurfaceVisible,
}
```

- `device_inventory` contains only bounded device identity/display metadata, support/tool status, and
  truncation state. It must not contain changing utilization/throughput values and must never be
  retained as history.
- `device_samples` contains only bounded numeric readings keyed by stable opaque device ID and is both
  live-visible and charted.
- Unknown/unsupported devices remain present in inventory; they simply omit unavailable numeric values.
- Extend `ValidateDefinition` to accept `ChartFamilyDevice`, and keep exact key/priority/family
  uniqueness.

Plan 6's `internal/devicemetric` package is the only Go authority for the complete numeric catalog.
Use its exact types and constants; do not introduce a second kind type or hand-maintained Go list:

```go
type NumericKey string

const (
    KindBlockDevice Kind = "block_device"
    KindFilesystem  Kind = "filesystem"
    KindGPU         Kind = "gpu"

    DiskFilesystemUsedPct NumericKey = "disk_filesystem_used_pct"
    DiskReadBytesPerSecond NumericKey = "disk_read_bytes_per_second"
    DiskWriteBytesPerSecond NumericKey = "disk_write_bytes_per_second"
    DiskIOBusyPct NumericKey = "disk_io_busy_pct"
    GPUUtilizationPct NumericKey = "gpu_utilization_pct"
    GPUVRAMUsedPct NumericKey = "gpu_vram_used_pct"
)

type NumericDefinition struct {
    Key  NumericKey
    Kind Kind
    Unit string // "%" or "B/s"
}

func NumericDefinitions() []NumericDefinition
```

`NumericDefinitions()` must return exactly those six definitions, sorted deterministically. Sample
validation rejects an unknown key, a key on the wrong device kind, duplicate device/key pairs,
non-finite values, percent values outside `0..100`, and any count/encoded-size violation. Missing
values are gaps. A valid later interval with a zero counter delta is a real numeric zero; only first,
reset/wrap, missing, or invalid-elapsed samples are gaps.

### Step 2 — Register the plan-6 payload once

Use `devicemetric.InventoryMetric`, `devicemetric.InventoryEntry`, `devicemetric.SamplesMetric`, and
`devicemetric.Sample` exactly as delivered by plan 6. Their `SeriesID` is the bounded opaque device ID
shown in Fleet and supplied back to exact history queries. Do not create alternate disk/GPU payloads,
rename it to a raw hardware ID, or split the three kinds into a second wire hierarchy.

- The signed successor policy field remains one closed opt-in: `devices.mode=all-eligible-v1`.
- Disabled policy means no device rediscovery, no `device_inventory`, no `device_samples`, and no
  collector work.
- Enabled policy periodically rediscovers devices independently of heartbeat upload and emits both
  declared keys from one sampler. The latest successful inventory may be reused between bounded
  discovery intervals, while numeric sampling continues at its configured cadence.
- Add the sampler to `BuildTelemetry` only after all downstream registries in this plan compile.
- `MetricDefinitions()` returns exactly `DeviceInventory` and `DeviceSamples`; the existing production
  parity gate must reject an undeclared emission, missing catalog key, duplicate owner, or changed
  disposition.
- Do not send raw WWID, serial, sysfs path, PCI command output, or `nvidia-smi` stderr. History identity
  uses only the plan-6 domain-separated opaque ID.

### Step 3 — Retain only numeric samples and push down one exact device

Add a device projection to controller history:

```go
type DeviceHistorySample struct {
    SeriesID string
    DeviceID string
    Kind     devicemetric.Kind
    TS       time.Time
    Values   map[devicemetric.NumericKey]float64
}

type TelemetryHistoryQueryOptions struct {
    ProbeSeriesID  string
    DeviceSeriesID string
}
```

- `device_inventory` has no history projector by design because it is cataloged live-only.
- `device_samples` has exactly one `ChartFamilyDevice` projector. It strictly parses
  `devicemetric.SamplesMetric`, uses controller-bounded sample time, and appends only valid numeric
  samples.
- Treat the plan-6 opaque `InventoryEntry.SeriesID` as `DeviceID`; derive the history `SeriesID` from
  exact `(kind, DeviceID)` through one leaf helper. Labels, mountpoints, vendor names, and support
  status never participate in history identity.
- Extend in-memory and streaming FileStore query filtering so `DeviceSeriesID` is pushed into the disk
  scan, as probe filtering already is. A selected query must not deserialize/return other devices.
- Keep all existing byte caps, logical caps, tail limits, rewrite targets, dedupe, and timestamp bounds.

The operator API accepts only these device query states:

```text
no include_devices and no device_* fields       -> no device history
include_devices=false and no device_* fields    -> no device history
include_devices=true + device_kind + device_id  -> exactly one device series
anything partial, broad, duplicated, or mixed  -> structured req_field_invalid/required
```

Use the exact names `include_devices`, `device_kind`, and `device_id`. `device_kind` is exactly one of
`block_device`, `filesystem`, or `gpu`; `device_id` is the selected inventory entry's bounded opaque
`series_id`. Do not add an “all devices” history query.
The additive response is:

```go
type deviceHistorySeries struct {
    SeriesID string                `json:"series_id"`
    DeviceID string                `json:"device_id"`
    Kind     string                `json:"kind"`
    Buckets  []deviceHistoryBucket `json:"buckets"`
}

type deviceHistoryBucket struct {
    T       time.Time            `json:"t"`
    Metrics map[string]metricAgg `json:"metrics"`
}
```

The response contains zero or one device series. Its bucket count participates in the same global
`maxHistoryBuckets == 1000` calculation as resource and probe buckets; the total returned bucket
objects across selected families must stay at or below 1000.

### Step 4 — Enforce `NumericDefinitions()` parity through every layer

- Controller projector fixtures must cover every key returned by `devicemetric.NumericDefinitions()`
  and reject an extra/missing field.
- The API device encoder must iterate/validate against the same Go definitions and emit no unknown key.
- Add a frontend authority with the same exact key/kind/unit triples:

```ts
export const DEVICE_NUMERIC_DEFINITIONS = [
  { key: 'disk_filesystem_used_pct', kind: 'filesystem', unit: '%' },
  { key: 'disk_read_bytes_per_second', kind: 'block_device', unit: 'B/s' },
  { key: 'disk_write_bytes_per_second', kind: 'block_device', unit: 'B/s' },
  { key: 'disk_io_busy_pct', kind: 'block_device', unit: '%' },
  { key: 'gpu_utilization_pct', kind: 'gpu', unit: '%' },
  { key: 'gpu_vram_used_pct', kind: 'gpu', unit: '%' },
] as const;
```

- Extend wiredrift with an exact bidirectional comparison of the Go definitions and this TypeScript
  literal. Missing, extra, wrong-kind, or wrong-unit definitions are release-blocking.
- Add `device` atomically to `HISTORY_CHART_FAMILIES`, the controller accumulator, API encoder,
  TypeScript parser, and `HISTORY_CHART_RENDERERS`. Existing exhaustive `satisfies` registries must fail
  until the device implementation exists.
- Defensive frontend parsing accepts at most one exact device series, at most 1000 buckets, only known
  numeric keys for the series kind, finite aggregates, and the stable opaque-ID grammar.

### Step 5 — Keep Fleet as the policy and observation home

- Broaden the current Fleet node-detail telemetry card into two sections: connectivity probes and
  automatic device telemetry. Do not rename Fleet or move the editor into Design.
- Add one checkbox, “Automatically discover eligible disks and GPUs,” backed only by
  `devices.mode=all-eligible-v1` in the successor signed policy.
- Preserve Save as design-draft persistence and Deploy as sign/activate. Show plan-4 capability
  readiness next to the checkbox.
- Project `device_inventory` and current `device_samples` into optional live `ControllerNode` fields.
  Show a compact disk/GPU list with status, truncation, missing-tool/unsupported explanations, and
  current numeric readings where present.
- Populate the history selector from current inventory. Selection is exact `(kind, device_id)` and the
  component sends `include_devices=true` with both selector fields; no selection sends
  `include_devices=false`.
- Render the selected disk/GPU definitions through the existing `HistoryChart`/
  `TimeSeriesChart`. Percent definitions use the existing `0..100` domain; throughput auto-fits.
- Preserve range, effective-resolution, widened-resolution, loading, Retry, stale/error, gap, and
  date-aware axis behavior. Fleet Live remains the existing non-overlapping ten-second refresh;
  history remains component-local.
- Add English and Chinese strings together through keyed i18n; do not parse backend strings.

### Step 6 — Preserve browser custody

- `stripLiveTelemetry` must clear device inventory, device samples/current readings, capability
  readiness, and any future mapped device field before Zustand persistence.
- `partialize` remains the only persistence allowlist. Do not add fetched history or selectors to the
  persisted controller store.
- Extend the existing leak-oracle/custody test so a serialized controller state contains none of the
  new device IDs, labels, mountpoints, vendor data, numeric samples, or capability tokens.

### Step 7 — Minimal focused verification

Run formatting and the smallest checks that jointly pin the vertical contract:

```bash
gofmt -w ./internal/devicemetric ./internal/telemetrymetric ./internal/agent ./internal/controller ./internal/api ./internal/wiredrift
GOSUMDB=sum.golang.org GOPROXY=https://proxy.golang.org,direct \
  go test ./internal/devicemetric ./internal/telemetrymetric ./internal/agent ./internal/controller ./internal/api ./internal/wiredrift \
  -run 'Device|NumericDefinitions|MetricCatalog|Projector|ChartFamily|ExactSeries|History|Custody' -count=1
(
  cd frontend
  npm run vitest -- \
    src/lib/telemetryHistory.test.ts \
    src/lib/custody.test.ts \
    src/components/deploy/NodeResourceHistory.test.tsx \
    src/components/pages/FleetNodeDetailPage.test.tsx
  npm run build
)
```

Do not duplicate plan-6 collector fixtures in browser tests. Plan 8 owns the complete repository and
Playwright gates.

### Step 8 — Independent review, fix, and re-review

Use independent reviewers with no tree edits:

- controller/history/storage: projector parsing, timestamp bounds, exact disk-scan pushdown, cap math,
  gaps versus valid zeros;
- frontend/custody/product: Fleet placement, Save-versus-Deploy, selector exactness, chart reuse,
  live refresh, i18n/accessibility, persistence stripping;
- framework/hygiene: `device_inventory`/`device_samples` separation, `NumericDefinitions()` exact parity,
  dependency direction, stale aliases, and test economy.

Fix every actionable finding, rerun only affected focused checks, and obtain a clean re-review before
committing.

### Step 9 — Commit, push, and close the plan

Stage only this plan's implementation and tests. Inspect `git status --short` and
`git diff --cached --check` before committing.

```bash
git add \
  internal/devicemetric \
  internal/telemetrymetric \
  internal/agent \
  internal/controller/telemetry_history.go \
  internal/controller/telemetry_history_test.go \
  internal/api/telemetry_history.go \
  internal/api/telemetry_history_test.go \
  internal/wiredrift/drift_test.go \
  frontend/src/types \
  frontend/src/api/controller \
  frontend/src/lib \
  frontend/src/stores/controller \
  frontend/src/components/deploy \
  frontend/src/components/pages/FleetNodeDetailPage.tsx \
  frontend/src/components/pages/FleetNodeDetailPage.test.tsx \
  frontend/src/i18n/messages/en.ts \
  frontend/src/i18n/messages/zh.ts
git diff --cached --check
GIT_AUTHOR_NAME='KunoriKiku' GIT_AUTHOR_EMAIL='rokuyanlin@gmail.com' \
GIT_COMMITTER_NAME='KunoriKiku' GIT_COMMITTER_EMAIL='rokuyanlin@gmail.com' \
git commit -m "$(cat <<'EOF'
feat(telemetry): chart automatic devices
EOF
)"
git push origin HEAD:fix/rc12-telemetry-drafts
```

Then let `execute-implementation-plan` update the outline status in its separate bookkeeping commit and
invoke `close-phase` for plan 7. Do not archive the subject; plans 8 and the terminal publication
checklist remain.

## Tests produced by this plan

- `internal/telemetrymetric/catalog_test.go`
  - **Lifetime:** perpetual
  - **Guards:** exact `device_inventory` live-only and `device_samples` charted catalog declarations,
    including the closed device family.
  - **Retirement trigger:** never; this is the chart-first registration invariant.
  - **Retirement destination:** not applicable while active; if the catalog framework is replaced,
    `tests/legacy/telemetry-catalog/`.
- `internal/agent/telemetry_device_test.go`
  - **Lifetime:** perpetual
  - **Guards:** signed opt-in is the only production activation path and the sampler emits exactly the
    two declared metrics within bounds.
  - **Retirement trigger:** never while automatic device telemetry is supported.
  - **Retirement destination:** `tests/legacy/agent-device-telemetry/`.
- `internal/controller/telemetry_history_test.go`
  - **Lifetime:** perpetual
  - **Guards:** every numeric definition projects to retained history, gaps/valid zeros stay distinct,
    and exact device filtering reaches the FileStore scan.
  - **Retirement trigger:** never while controller history exists.
  - **Retirement destination:** `tests/legacy/controller-telemetry-history/`.
- `internal/api/telemetry_history_test.go`
  - **Lifetime:** perpetual
  - **Guards:** exact selector validation, one-series response, numeric-definition encoder parity, and
    the shared 1000-bucket ceiling.
  - **Retirement trigger:** never while the operator history API exists.
  - **Retirement destination:** `tests/legacy/api-telemetry-history/`.
- `internal/wiredrift/drift_test.go`
  - **Lifetime:** perpetual
  - **Guards:** Go/TypeScript chart-family and numeric-definition key/kind/unit parity.
  - **Retirement trigger:** never while the frontend mirrors Go telemetry contracts.
  - **Retirement destination:** `tests/legacy/wiredrift/`.
- `frontend/src/lib/telemetryHistory.test.ts`
  - **Lifetime:** perpetual
  - **Guards:** exact device query generation, bounded parsing, and gap-aware series projection.
  - **Retirement trigger:** never while the Fleet history API is consumed by the panel.
  - **Retirement destination:** `tests/legacy/frontend-telemetry-history/`.
- `frontend/src/lib/custody.test.ts`
  - **Lifetime:** perpetual
  - **Guards:** device inventory, readings, identifiers, capabilities, and fetched history cannot enter
    persisted browser state.
  - **Retirement trigger:** never; this pins the browser custody boundary.
  - **Retirement destination:** `tests/legacy/frontend-custody/`.
- `frontend/src/components/deploy/NodeResourceHistory.test.tsx`
  - **Lifetime:** perpetual
  - **Guards:** the device renderer registry reaches the shared `TimeSeriesChart` for one disk and one
    GPU fixture without duplicating collector tests.
  - **Retirement trigger:** when this history component is replaced by another production renderer.
  - **Retirement destination:** `tests/legacy/frontend-device-history/`.
- `frontend/src/components/pages/FleetNodeDetailPage.test.tsx`
  - **Lifetime:** perpetual
  - **Guards:** one vertical Fleet interaction covers signed opt-in, readiness/current inventory, exact
    device selection, and Save-versus-Deploy ownership.
  - **Retirement trigger:** when Fleet node detail is replaced by a different policy/observation surface.
  - **Retirement destination:** `tests/legacy/frontend-fleet-device-telemetry/`.

## Definition of done

- `device_inventory` is bounded, live-visible, explicitly live-only, and contains no dynamic numeric
  series.
- `device_samples` is bounded, live-visible, charted in `ChartFamilyDevice`, and contains no mutable
  display identity.
- `devicemetric.NumericDefinitions()` contains exactly six key/kind/unit definitions and has exact
  producer/projector/encoder/TypeScript/renderer parity.
- Automatic collection runs only under the signed successor-policy opt-in; disabled nodes do no device
  work and emit neither key.
- Controller history retains only numeric samples, preserves gaps and legitimate zeros, and pushes one
  exact `(kind, device_id)` series into memory/FileStore queries.
- The operator API has no broad all-device history mode and keeps the global response at or below 1000
  bucket objects.
- Fleet remains the editor/live/history home, reuses the shared chart/refresh framework, and keeps
  history component-local.
- Live device/capability/history data is absent from browser persistence.
- Focused tests and frontend build pass; independent re-review is clean; the implementation commit and
  outline/close-phase commits are pushed.

## Out of scope

- A global Telemetry page, renaming Fleet, alerting, URL body checks, SMART health, temperature,
  per-process telemetry, driver installation, or new vendor daemons.
- Fetching all device histories in one browser request.
- Charting categorical inventory/status/truncation or raw URL response codes.
- Reworking plan-6 collector limits without a separately reviewed insertion plan.
