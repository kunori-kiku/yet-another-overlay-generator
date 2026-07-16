# plan-6 — Build bounded automatic disk/GPU collector primitives

**Outline:** [outline.md](./outline.md) — read it completely before this plan, especially Principles,
Standing rules, the plan-6 milestone, and the plan-status table.

**Subject:** deployment-stability-and-charted-telemetry-2026_07_17
**Track:** Go/agent
**Depends on:** plan-4

## Prerequisites

- Plan 4 is `done`, pushed, and closed. Its successor policy contains the opt-in
  `all-eligible-v1` device mode and requires both `telemetry-policy-v2` and
  `device-telemetry-v1`; do not add a second topology switch or policy format here.
- Plan 5 is not a code prerequisite and may run before or after this plan, but it must be closed
  before plan 8.
- The current branch is `fix/rc12-telemetry-drafts`. Preserve unrelated dirty-worktree changes,
  stage only the paths named in this plan, and do not register production telemetry in this plan.
- Read the official NVIDIA and kernel AMD interfaces linked at `outline.md:123-124`. No external GPU
  library, shell pipeline, privileged helper, runtime daemon, or new Go module is permitted.
- If bounded, stable device identity cannot be derived without transmitting a hardware secret or raw
  host path, stop and request plan-6.5 rather than substituting display labels as history identity.

## Goal

Implement deterministic, bounded, injectable Linux disk/filesystem and GPU inventory plus numeric
sampling primitives. Keep them dormant until plan 7 atomically registers their latest values,
history projectors, API encoders, exact-series queries, and Fleet charts.

## Reads from specs

Reads from specs: agent, controller-agent-api, controller-store, model-validation, panel-deploy-fleet

## Read first

Line anchors are against the pre-plan-6 tree; relocate by named symbol after plan 4 rather than
guessing if line numbers shifted.

1. `internal/agent/telemetry.go:21-226` and `internal/agent/telemetry_resource.go:14-110` — sampler
   contract, production registry parity, stateful delta precedent, and absent-value semantics.
2. `internal/agent/probe_runner.go:45-220,258-420` — injected clock/runner patterns, deadlines, bounded
   asynchronous work, and last-known-result cadence.
3. `internal/probemetric/result.go:20-190` — dependency-light typed metric, strict validation, bounded
   identity, and latest/recent separation.
4. `internal/telemetrymetric/catalog.go:13-221` — chart-first catalog invariant; do not change it in
   this plan.
5. `internal/agent/heartbeat_reliable.go:24-281` and `internal/telemetryprotocol/protocol.go:27-40` — replay
   and encoded-metric ceilings that bound the new DTOs.
6. `internal/model/topology.go:128-170` and `internal/probepolicy/policy.go:20-300` — plan-4 opt-in
   device policy and successor capability boundary.
7. `internal/agent/telemetry_resource_test.go:1-157`,
   `internal/agent/probe_runner_test.go:1-821`, `internal/probemetric/result_test.go:1-120`, and
   `internal/telemetrymetric/catalog_test.go:1-117` — fixture and contract-test styles to extend without
   duplicating whole-system coverage.
8. `outline.md:123-124` — official `nvidia-smi` query/output reference and AMD
   `gpu_busy_percent` sysfs reference.

## Implementation steps

### Step 1 — Define one closed, bounded leaf contract

Add `internal/devicemetric/metric.go` with no dependency on agent, controller, API, or frontend
packages. Use separate inventory and numeric sample DTOs so categorical state is not accidentally
charted.

```go
package devicemetric

type Kind string

const (
    KindBlockDevice Kind = "block_device"
    KindFilesystem  Kind = "filesystem"
    KindGPU         Kind = "gpu"
)

type Status string

const (
    StatusOK                 Status = "ok"
    StatusToolMissing        Status = "tool_missing"
    StatusDriverUnavailable  Status = "driver_unavailable"
    StatusMetricsUnavailable Status = "metrics_unavailable"
    StatusUnsupported        Status = "unsupported"
    StatusCollectionError    Status = "collection_error"
)

type NumericKey string

const (
    DiskFilesystemUsedPct   NumericKey = "disk_filesystem_used_pct"
    DiskReadBytesPerSecond  NumericKey = "disk_read_bytes_per_second"
    DiskWriteBytesPerSecond NumericKey = "disk_write_bytes_per_second"
    DiskIOBusyPct           NumericKey = "disk_io_busy_pct"
    GPUUtilizationPct       NumericKey = "gpu_utilization_pct"
    GPUVRAMUsedPct          NumericKey = "gpu_vram_used_pct"
)

type NumericDefinition struct {
    Key  NumericKey
    Kind Kind
    Unit string
}

type InventoryEntry struct {
    SeriesID       string `json:"series_id"`
    Kind           Kind   `json:"kind"`
    Label          string `json:"label"`
    ParentSeriesID string `json:"parent_series_id,omitempty"`
    MountPoint     string `json:"mount_point,omitempty"`
    FSType         string `json:"fs_type,omitempty"`
    Vendor         string `json:"vendor,omitempty"`
    Model          string `json:"model,omitempty"`
    CapacityBytes  uint64 `json:"capacity_bytes,omitempty"`
    VRAMTotalBytes uint64 `json:"vram_total_bytes,omitempty"`
    Status         Status `json:"status"`
}

type Sample struct {
    SeriesID string                 `json:"series_id"`
    Kind     Kind                   `json:"kind"`
    Values   map[NumericKey]float64 `json:"values"`
}

type InventoryMetric struct {
    Devices   []InventoryEntry `json:"devices"`
    Truncated int              `json:"truncated,omitempty"`
}

type SamplesMetric struct {
    Samples   []Sample `json:"samples"`
    Truncated int      `json:"truncated,omitempty"`
}

func NumericDefinitions() []NumericDefinition
func SeriesID(kind Kind, canonicalIdentity []byte) string
func ValidateInventory(metric InventoryMetric) error
func ValidateSamples(metric SamplesMetric) error
```

- `NumericDefinitions` is the closed plan-7 chart contract and returns exactly six deterministic
  definitions: filesystem `disk_filesystem_used_pct`; block-device
  `disk_read_bytes_per_second`, `disk_write_bytes_per_second`, and `disk_io_busy_pct`; GPU
  `gpu_utilization_pct` and `gpu_vram_used_pct`. Validation rejects a key on the wrong kind, unknown
  keys, NaN/Inf, percentages outside 0–100, negative rates, duplicate series, invalid status/kind, and
  oversize strings. Stable block/filesystem capacity and total GPU VRAM belong only in bounded
  inventory metadata, not in dynamic samples or history.
- `SeriesID` returns lowercase hexadecimal SHA-256 over the exact domain-separated input
  `"yaog-device-series-v1\x00" + string(kind) + "\x00" + canonicalIdentity`. Transmitted IDs are these
  hashes, never the canonical identity itself.
- Raw serials, WWIDs, filesystem UUIDs, canonical/raw sysfs paths, GPU UUIDs, PCI identities, and raw
  command stdout/stderr never leave the agent. They may be read locally only to derive a hash and then
  discarded. The wire may contain bounded sanitized display label, vendor/model, mount point,
  filesystem type, status, relationship hashes, and declared numeric values.
- Sort by `(kind, series_id)` before truncation. Cap disk-related inventory/samples at 64 entries and
  GPU inventory/samples at 16 entries; return an exact positive `Truncated` count. Bound every string
  and prove the separately encoded inventory and sample metrics remain below the existing 64 KiB
  metric ceiling.

### Step 2 — Add injectable collector seams without production registration

Add these files:

- `internal/agent/device_collector.go` — platform-neutral interfaces, bounds, result merge, and
  sanitizers;
- `internal/agent/device_collector_linux.go` — Linux proc/sysfs/statfs and optional GPU tool provider;
- `internal/agent/device_collector_stub.go` — `//go:build !linux` unsupported result;
- `internal/agent/device_collector_linux_test.go` — synthetic Linux filesystem and command fixtures;
- `internal/devicemetric/metric_test.go` — leaf contract tests.

Use these concrete seams:

```go
type deviceCommandRunner interface {
    Run(ctx context.Context, path string, args ...string) (stdout []byte, err error)
}

type deviceCollectorDeps struct {
    ProcRoot         string
    SysRoot          string
    Now              func() time.Time
    Run              deviceCommandRunner
    ResolveNvidiaSMI func() (path string, ok bool)
}

type diskCounterSnapshot struct {
    ReadSectors  uint64
    WriteSectors uint64
    IOMillis     uint64
    SampledAt    time.Time
}

type deviceCollector struct {
    deps     deviceCollectorDeps
    previous map[string]diskCounterSnapshot
}

func newDeviceCollector(deps deviceCollectorDeps) *deviceCollector
func (c *deviceCollector) Collect(
    ctx context.Context,
    now time.Time,
) (devicemetric.InventoryMetric, devicemetric.SamplesMetric)
```

- Production defaults use `/proc`, `/sys`, the passed monotonic-bearing `time.Time`, and a direct
  `exec.CommandContext` runner whose stdout is streamed into a bounded buffer; it must terminate and
  reap the child on overflow rather than reading unbounded output and checking afterward. Tests replace
  roots and the runner; production code has no test-only branches.
- Bound the entire collection with a short context deadline and return partial, validated inventory
  with explicit statuses when an independent provider fails. A missing optional GPU tool must never
  suppress disk/filesystem data.
- Do not add a `Sampler`, modify `BuildTelemetry`, add catalog definitions, or emit metric keys in this
  plan. Plan 7 owns that atomic activation.

### Step 3 — Discover block devices and filesystems as different kinds

- Enumerate `/sys/class/block`; exclude `loop*`, `ram*`, `zram*`, and non-device entries. Include
  physical disks, partitions, device-mapper/LVM, MD, and eligible unmounted devices.
- Resolve parent/child relationships locally and transmit only `ParentSeriesID`. Keep block devices and
  filesystems as distinct kinds so the UI can never silently sum a partition, stacked dm device,
  filesystem, and parent disk as if they were disjoint capacity.
- Derive a block canonical identity in this exact precedence: block `wwid`, device `wwid`, serial,
  device-mapper UUID, partition parent identity plus partition number, then resolved sysfs identity
  plus major:minor as a last-resort local input. Hash immediately with `SeriesID`; do not place any raw
  input in DTOs, errors, or logs.
- Parse `/proc/self/mountinfo`; admit only block-backed major:minor devices resolvable through
  `/sys/dev/block`. Dedupe bind mounts by `(major:minor, mount-root)`, choosing the shortest mount point
  and then lexical order only as display metadata. Derive filesystem identity from the associated
  block identity plus mount root; never transmit filesystem UUID.
- Use the Linux stdlib `syscall.Statfs` boundary (or an already-present project wrapper over it) to
  derive filesystem total and used values with overflow checks; do not add a module dependency for
  this call. Put total capacity in `InventoryEntry.CapacityBytes`; emit only
  `disk_filesystem_used_pct` dynamically. Do not transmit used/available byte samples.
- Read per-device counters from `/sys/class/block/<name>/stat`; Linux sectors are exactly 512 bytes for
  this accounting. Compute read/write B/s and I/O busy percentage from the prior snapshot and real
  elapsed duration.
- A positive, valid elapsed interval with unchanged monotonic counters emits numeric `0` for each
  corresponding rate/busy value: true idle must chart at zero. The first observation, a reset or
  counter decrease, missing/malformed counters, a remapped identity, or zero/negative/invalid elapsed
  time omits the affected value so plan 7 renders a gap. Never convert those unknown states to zero.

### Step 4 — Union GPU inventory and collect vendor-specific numeric values

- Enumerate DRM/PCI graphics devices through sysfs, including unsupported vendors. Locally derive a
  stable identity from the strongest available device identity and PCI location, hash it, and transmit
  only bounded sanitized vendor/model/label metadata.
- Independently query NVIDIA so compute-only or container-exposed GPUs absent from DRM/sysfs still
  appear. Resolve only these trusted absolute paths, in order, unless the injected test resolver is
  used: `/usr/bin/nvidia-smi`, `/usr/local/bin/nvidia-smi`,
  `/usr/local/nvidia/bin/nvidia-smi`. Do not search inherited `PATH`, invoke a shell, or accept a
  deployment-provided executable path.
- Invoke exactly:

```text
nvidia-smi --query-gpu=uuid,pci.bus_id,name,utilization.gpu,memory.used,memory.total --format=csv,noheader,nounits
```

- Use `encoding/csv`, a two-second child context, a 64 KiB stdout cap, and direct process cleanup.
  Ignore stderr except for bounded internal error classification; raw stdout/stderr and parse fragments
  must never enter telemetry, logs, or returned errors. Convert total MiB to bounded bytes for inventory
  metadata, and compute only `gpu_vram_used_pct` for dynamic samples.
- Union NVIDIA sysfs and tool rows, deduplicating locally by GPU UUID first and normalized PCI bus ID
  second before hashing. A tool-only GPU is included. A sysfs NVIDIA GPU with no tool is retained as
  `tool_missing`; a present but unusable driver is `driver_unavailable`; malformed/bounded rows are
  `collection_error`, without fake numeric zeros.
- For AMD vendor `0x1002`, read documented `gpu_busy_percent`, `mem_info_vram_used`, and
  `mem_info_vram_total` attributes from the correct DRM device. Emit utilization and VRAM values only
  when bounded and internally consistent.
- Intel and other vendors remain visible with `unsupported` or `metrics_unavailable`. Do not add
  `intel_gpu_top`, per-process accounting, inferred global utilization, or vendor-specific dependencies.
- A genuine parsed GPU utilization or VRAM usage of zero is a valid numeric zero. Missing tool/driver,
  absent attributes, parse failure, or invalid totals omit the affected numeric value and retain a
  categorical status.

### Step 5 — Verify focused behavior and portability

Run exactly:

```bash
gofmt -w internal/devicemetric/*.go internal/agent/device_collector*.go
go test ./internal/devicemetric ./internal/agent -run 'DeviceMetric|DeviceCollector|Disk|Filesystem|NVIDIA|AMD|ZeroDelta|Truncat' -count=1
GOOS=windows GOARCH=amd64 go build ./internal/devicemetric ./internal/agent
GOOS=linux GOARCH=arm64 go build ./internal/devicemetric ./internal/agent
git diff --check
git status --short
```

The focused agent test uses one temporary proc/sysfs fixture for a parent disk, partition, dm node,
bind mount, unmounted device, and excluded pseudo device. It also covers stable relationships,
filesystem dedupe, sector conversion, first/reset/invalid-elapsed gaps, valid unchanged-counter zeros,
and deterministic truncation. Inject one NVIDIA CSV table containing a tool-only GPU plus duplicate
sysfs GPU, and cover malformed/oversize output, timeout, untrusted-path rejection, and missing tool.
Use one AMD sysfs row and one unsupported-vendor row.

### Step 6 — Review, fix, re-review, commit, and push

- First review: command execution and process cleanup, proc/sysfs parsing, stable identity/privacy,
  cardinality/string/output/time bounds, counter math, zero-versus-gap behavior, portability,
  dependency direction, and Go hygiene.
- Fix every actionable finding, rerun the focused commands, then perform a fresh review of the final
  diff. Do not mark the plan done with an unresolved correctness, privacy, or compatibility finding.
- Stage only this plan's implementation and tests, then commit and push exactly:

```bash
git add internal/devicemetric \
  internal/agent/device_collector.go \
  internal/agent/device_collector_linux.go \
  internal/agent/device_collector_stub.go \
  internal/agent/device_collector_linux_test.go
GIT_AUTHOR_NAME='KunoriKiku' GIT_AUTHOR_EMAIL='rokuyanlin@gmail.com' \
GIT_COMMITTER_NAME='KunoriKiku' GIT_COMMITTER_EMAIL='rokuyanlin@gmail.com' \
git commit -F - <<'EOF'
feat(agent): add bounded device collectors

- define hashed, bounded disk/filesystem/GPU metric contracts
- collect Linux disk deltas and vendor-aware GPU observations
- keep production telemetry activation for the atomic chart plan
EOF
git push origin fix/rc12-telemetry-drafts
```

After the push, update the outline status/evidence and invoke `close-phase`; those bookkeeping changes
are a separate close-phase commit and are not folded into the implementation commit.

## Tests produced by this plan

### `internal/devicemetric/metric_test.go`

- **Lifecycle:** perpetual contract test.
- **Guards:** numeric-definition closure; kind/key compatibility; domain-separated deterministic hashes;
  duplicate/invalid/non-finite rejection; deterministic caps/truncation; 64 KiB encoding ceiling; no
  raw serial/WWID/sysfs/UUID/PCI/command material in encoded DTOs.
- **Retirement trigger:** only when the device metric contract is replaced by a versioned successor and
  no supported agent/controller accepts this contract.
- **Retirement destination:** move to `tests/legacy/device-telemetry-v1/metric_test.go` with the
  migration/removal decision documented by the closing plan.

### `internal/agent/device_collector_linux_test.go`

- **Lifecycle:** perpetual Linux collector regression test using synthetic proc/sysfs and command
  fixtures; it is not a hardware certification matrix.
- **Guards:** eligible/unmounted/stacked block discovery, bind dedupe, hashed parent identity, delta
  arithmetic, valid idle zero versus unknown gap, NVIDIA sysfs/tool union and tool-only GPU, trusted
  executable policy, output/deadline bounds, AMD metrics, unsupported inventory, and partial-failure
  isolation.
- **Retirement trigger:** only when Linux collection moves behind a different provider abstraction and
  equivalent fixture coverage exists at that replacement boundary.
- **Retirement destination:** move to `tests/legacy/device-telemetry-v1/device_collector_linux_test.go`
  after replacement coverage is green.

The Windows and Linux/arm64 build commands are verification gates, not committed test artifacts.

## Definition of done

- [ ] Plan 4's single device opt-in and capability contract are reused; no production sampler is
      registered.
- [ ] Block device, filesystem, and GPU identities are stable domain-separated hashes; raw hardware
      identifiers, sysfs paths, and command output never leave the agent.
- [ ] Eligible physical, partitioned, stacked, unmounted, and mounted block storage is discovered with
      explicit non-double-countable kinds and deterministic bounds.
- [ ] Valid unchanged disk deltas and genuine GPU idle values emit zero; first/reset/missing/invalid
      observations emit gaps/status rather than fabricated zeros.
- [ ] NVIDIA inventory is the union of sysfs and the fixed bounded `nvidia-smi` query, including
      tool-only GPUs; AMD documented sysfs values and unsupported vendors are handled explicitly.
- [ ] Leaf and Linux fixture tests pass, Windows and Linux/arm64 package builds pass, `git diff --check`
      is clean, and final review has no unresolved finding.
- [ ] The implementation commit is pushed, outline evidence is recorded separately, and close-phase
      closes plan 6.

## Out of scope

- Production sampler/catalog registration, controller retention/projectors/encoders, exact-series API
  queries, and Fleet graphs; plan 7 activates all of them atomically.
- SMART health, temperatures, filesystem-content scanning, filesystem UUID display, per-process GPU
  data, driver installation, Intel tooling, vendor libraries, arbitrary commands, or hardware-matrix
  certification.
- Changing the successor policy schema, URL probes, telemetry transport, history retention budgets, or
  release/tag/docker work.
