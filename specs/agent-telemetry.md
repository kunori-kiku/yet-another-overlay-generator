# Agent telemetry

<!-- last-verified: 2026-07-17 -->

## Responsibility

Sample signed active probes and opt-in automatic devices on node-owned cadences, turn their results
into typed live and chart metrics, and deliver immutable observations through the authenticated,
legacy-compatible telemetry heartbeat (`internal/agent/probe_runner.go:65-105`,
`internal/agent/telemetry_device.go:24-45`, `internal/agent/heartbeat_reliable.go:477-550`).

## Files

- `internal/agent/telemetry.go:25-120` — defines the sampler contract, panic-isolated aggregation,
  and completion-kick seams.
- `internal/agent/telemetry.go:176-204` — registers active probes, automatic devices, resource,
  WireGuard, capability, XDP, and mimic samplers in one catalog-checked framework.
- `internal/agent/probe_runner.go:65-337` — reconciles last-known-good probe policy, schedules bounded
  asynchronous attempts, and retains latest plus recent completed results.
- `internal/agent/probe_runner.go:445-580` and `internal/agent/probe_url.go:14-78` — implement raw
  ICMP, TCP connect, and fixed-shape HTTP(S) attempts.
- `internal/agent/telemetry_device.go:24-225` — owns device-policy activation, the fixed collection
  cadence, snapshots, cancellation, and completion delivery.
- `internal/agent/device_collector.go:20-24` and `internal/agent/device_collector.go:103-232` — bound
  provider time/concurrency and preserve status-only inventory when collection cannot finish.
- `internal/agent/device_collector_linux.go:112-328` and
  `internal/agent/device_collector_linux.go:538-927` — discover Linux block devices, filesystems,
  and GPUs and collect their numeric readings.
- `internal/devicemetric/metric.go:21-113` — defines device identity, status, and numeric-value contracts.
- `internal/agent/heartbeat_reliable.go:24-120` and
  `internal/agent/heartbeat_reliable.go:159-351` — sequence, size-admit, negotiate, and queue bounded
  immutable observations for replay.

## Inputs

The sole runtime authority is `State.ActiveTelemetryPolicy`: exactly one strictly parsed legacy or
successor policy committed with a successful apply; failed candidates preserve the prior bytes and a
successful signed omission or uninstall clears them (`internal/agent/state.go:124-128`,
`internal/agent/agent.go:318-326`, `internal/agent/agent.go:626-659`). Policy construction and rollout
readiness belong to [Telemetry policy](telemetry-policy.md), while apply custody belongs to
[Agent](agent.md).

Probe policy supplies at most 16 typed ICMP, TCP, or URL checks with bounded cadence and timeout.
ICMP has one IP/DNS host and no port, TCP adds one port, and URL has a distinct absolute HTTP(S)
target plus an exact expected status whose omitted value becomes 200
(`internal/probepolicy/policy.go:26-48`, `internal/probepolicy/policy.go:421-505`,
`internal/probepolicy/policy.go:515-585`). The optional device input is the closed
`all-eligible-v1` selector in the successor policy (`internal/probepolicy/policy.go:62-77`,
`internal/probepolicy/policy.go:347-390`).

Linux device collection reads bounded `/sys` block, DRM, and PCI state plus `/proc/self/mountinfo`;
the only external provider is a directly executed, trusted absolute `nvidia-smi` selected from three
fixed paths (`internal/agent/device_collector.go:70-92`,
`internal/agent/device_collector_linux.go:261-328`,
`internal/agent/device_collector_linux.go:727-821`,
`internal/agent/device_collector_linux.go:1154-1175`).

## Outputs

The sampler registry merges current conditions and metrics into one heartbeat snapshot. Active checks
emit backward-compatible latest `probe_results` and a bounded `probe_samples` completion window;
automatic devices emit paired `device_inventory` and `device_samples`
(`internal/agent/telemetry.go:52-120`, `internal/agent/probe_runner.go:242-262`,
`internal/agent/telemetry_device.go:201-210`).

Inventory carries opaque series IDs, labels, capacity, parent/mount/provider metadata, and categorical
status. Numeric samples carry filesystem use, block read/write rate and busy percentage, GPU
utilization, and VRAM use; inventory is live-only while numeric device and probe measurements are
charted (`internal/devicemetric/metric.go:72-113`, `internal/devicemetric/metric.go:130-159`,
`internal/telemetrymetric/catalog.go:117-157`). Retention and rollup belong to
[Controller telemetry](controller-telemetry.md), and rendering belongs to
[Panel telemetry](panel-telemetry.md).

Accepted snapshots leave through the per-node authenticated `POST /telemetry` contract as the legacy
conditions/metrics/version JSON body plus optional boot, sequence, sample-time, and interval headers
(`internal/agent/controller_client.go:463-490`). Receipt handling and route authentication are
cross-referenced in [Controller agent API](controller-agent-api.md).

## Decision points (if any)

- Each ICMP/TCP hostname is resolved afresh and bounded on every attempt; TCP performs a context-bound
  connect and ICMP sends a raw IPv4/IPv6 echo. URL performs one direct GET with no ambient proxy,
  redirects, keepalive, compression, or response-body read; a completed response records latency and
  actual status, but succeeds only when that status exactly matches the configured expected value
  (`internal/agent/probe_runner.go:445-580`, `internal/agent/probe_url.go:14-64`,
  `internal/agent/probe_runner.go:301-313`).
- Eligible disks are sysfs block devices except `loop*`, `ram*`, and `zram*`; filesystem rows come only
  from mounts backed by a retained block identity. GPUs are discovered through DRM/PCI sysfs and
  unmatched rows from the trusted fixed `nvidia-smi` query; AMD `amdgpu` utilization/VRAM comes from
  bounded sysfs reads, while NVIDIA utilization/VRAM comes from that fixed query
  (`internal/agent/device_collector_linux.go:261-328`,
  `internal/agent/device_collector_linux.go:538-600`,
  `internal/agent/device_collector_linux.go:658-875`,
  `internal/agent/device_collector_linux.go:897-927`,
  `internal/agent/device_collector_linux.go:1397-1410`).
- A protocol-v2 receipt may enable `probe-samples-v1`; legacy delivery, header stripping, or capability
  rollback removes only that additive completion window and immediately returns to the legacy shape.
  The latest probe result and device metrics continue through the ordinary heartbeat
  (`internal/agent/heartbeat_reliable.go:43-120`,
  `internal/agent/heartbeat_reliable.go:477-550`,
  `internal/agent/controller_client.go:505-530`).

## Invariants

- Probe and device collection cadence is independent from heartbeat upload and the deployment poll:
  probes perform no network I/O in `Sample`, devices have one cancellable 30-second worker, and the
  reliable uploader drains the replay queue separately from collection
  (`internal/agent/controller_loop.go:117-139`, `internal/agent/probe_runner.go:65-105`,
  `internal/agent/telemetry_device.go:135-188`, `internal/agent/heartbeat_reliable.go:477-550`).
- Only the last-known-good durable policy authorizes work. Changed or removed probes cancel in-flight
  attempts and discard late results; device deactivation cancels and generation-fences its provider,
  clears snapshots, and resets rate baselines (`internal/agent/probe_runner.go:173-231`,
  `internal/agent/probe_runner.go:265-337`, `internal/agent/telemetry_device.go:110-133`,
  `internal/agent/device_collector.go:172-180`).
- Absent numeric observations remain chart gaps, never fabricated zeros: transport-level probe failures
  omit latency unless a URL response completed, while device collection failure may preserve known
  inventory only after changing its status to `collection_error`
  (`internal/agent/device_collector.go:103-106`, `internal/agent/device_collector.go:224-232`,
  `internal/agent/probe_runner.go:301-313`).

## Gotchas (optional)

- Replay is volatile and bounded to 32 samples of at most 128 KiB each. A restart drops it, queue
  pressure evicts the oldest sample, and retry keeps the current oldest sample until acknowledgement,
  permanent rejection, or eviction (`internal/agent/heartbeat_reliable.go:3-5`,
  `internal/agent/heartbeat_reliable.go:24-29`, `internal/agent/heartbeat_reliable.go:284-351`,
  `internal/agent/heartbeat_reliable.go:371-429`).
- Raw ICMP can report `permission_denied`; NVIDIA can report `tool_missing` or `driver_unavailable`;
  AMD devices without the `amdgpu` driver remain visible as unsupported rather than disappearing
  (`internal/agent/probe_runner.go:475-482`, `internal/agent/probe_runner.go:616-635`,
  `internal/agent/device_collector_linux.go:814-875`,
  `internal/agent/device_collector_linux.go:897-927`).
- Disk rates require two authorized counter observations, so first collection and reactivation produce
  an intentional gap; collection timeout or a stuck provider returns last-known inventory with no
  numeric values and cannot accumulate concurrent provider workers
  (`internal/agent/device_collector_linux.go:181-207`,
  `internal/agent/device_collector.go:103-180`).
