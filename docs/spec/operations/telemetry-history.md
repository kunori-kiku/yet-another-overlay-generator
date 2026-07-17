# Telemetry history (node-detail resource, active-probe, and device charts)

This document defines how the controller **retains** chartable per-node telemetry and how the operator
**queries** it back as bounded bucketed series. It is the durable backing for CPU / RAM /
load-average, active-probe latency/availability, and automatic-device numeric charts; it sits
**strictly on top of** the live telemetry heartbeat
([controller-api.md](../controller/controller-api.md) §`POST /telemetry`) and adds **no** parallel
agent transport. The agent already emits named metrics over authenticated HTTP; this layer projects
chartable definitions into one bounded per-node history and serves an aggregated query.
Short reporting interruptions are handled by protocol-v2 sequence headers and a bounded replay queue,
not WebSocket or gRPC.

Three pieces, each independently testable:

1. The agent **Sampler** registry and shared metric catalog (what each metric contains and whether it
   is charted or explicitly live-only).
2. The **retention store** in the controller (`internal/controller/telemetry_history.go`) — an
   in-memory buffer per node with an off-heartbeat background flush to append-only JSONL, a
   configurable cap, and one hard invariant: **a heartbeat never touches disk**.
3. The **query API** (`internal/api/telemetry_history.go`, `GET …/node-history`) — backward-compatible
   resource buckets plus additive exact per-probe and per-device series the charts render.

## Chart-first metric registration

Every production sampler declares its metric definitions from the leaf `internal/telemetrymetric`
catalog. A definition is either `charted` or `live-only`; a live-only entry must carry a non-empty
reason explaining why retention would be misleading, while a charted entry must have a registered
controller history projector. Agent and controller parity tests fail if a catalog definition has no
producer or a charted definition has no projector. Emitted keys are checked against the sampler's
declarations, preventing a future metric from bypassing the decision by returning an unreviewed raw
map key.

Current charted keys are `resource`, `probe_results` (rc.9 compatibility projection),
`probe_samples` (complete bounded recent-attempt projection), and `device_samples` (bounded numeric
device projection). `device_inventory` is deliberately live-only because identity/display/support
status is categorical current context rather than a numeric time series. WireGuard peer detail and
deployment capability advisories are also explicitly live-only.

## The resource sample

Each accepted heartbeat's `metrics["resource"]` is projected into a `ResourceSample`
(`resourceSampleFromMetrics`). A legacy sample is stamped with the **server-observed** time. A
protocol-v2 sample uses its agent `sampled_at` for history only when it is within 24 hours before or
five minutes after receipt; otherwise it safely falls back to receipt time. Controller receipt time
always remains authoritative for `LastSeen` and live condition observation. The sample carries **no**
endpoint / IP / key material — it is observability only:

| Field | JSON | Notes |
|---|---|---|
| `TS` | `ts` | Bounded observation time (RFC3339): trusted v2 sample time, else controller receipt time. |
| `IntervalMS` | `interval_ms` | Optional advisory sampling cadence accepted from the v2 header. |
| `CpuPct` | `cpu_pct` | `*float64`, **omitempty** — absent on the first beat after daemon start (see below). |
| `Load1` / `Load5` / `Load15` | `load1` / `load5` / `load15` | Load averages; always present. |
| `MemTotalKB` | `mem_total_kb` | omitempty. |
| `MemAvailKB` | `mem_available_kb` | omitempty. Used memory % is **derived** at query time (below). |

A heartbeat whose `resource` metric is absent or malformed simply adds **no** sample — tolerant, never
an error on the heartbeat path.

## Reliable delivery over the existing HTTP endpoint

Protocol v2 preserves the legacy JSON body exactly. Delivery metadata lives only in optional
`X-YAOG-Telemetry-*` headers: protocol version, a random per-process boot ID, a monotonically
increasing sequence, sample time, and advisory interval. An old controller ignores the headers and
accepts the unchanged body; a new controller treats a request without the protocol header as legacy.

High-fidelity probe windows are capability-negotiated rather than merely optimistic. The controller
advertises `probe-samples-v1` on a successful v2 receipt. Until that exact token is observed, the agent
omits `probe_samples` and keeps the rc.9 JSON metrics shape. A later successful receipt without the
token disables the extension again and triggers one coalesced clean heartbeat, so controller rollback
does not leave a new history-only key in an older controller's latest-value overlay. A proxy that strips
receipt headers therefore fails safely to the legacy shape.

The agent samples independently from upload. Each immutable observation enters a volatile queue whose
capacity is 32 total samples, including the in-flight head. Transport failures, HTTP 408/429, 5xx, and
invalid acknowledgements retry with bounded jittered backoff while the sample remains retained. A
deterministic other 4xx drops that sample and advances. If the queue fills, it front-evicts the oldest
sample—even one currently retrying—so upload cannot permanently block new sampling. Agent restart
loses unsent observations by design.

The controller acknowledges the exact submitted sequence and keeps at most four volatile boot cursors
per node. A duplicate retry can advance receipt-authoritative liveness but cannot append history or
replace metrics twice. Delayed samples from a known retired boot, and clearly stale unseen boots, may
contribute bounded history but cannot replace the active boot's live conditions or metrics. Cursor
state is deliberately not durable: persisting it would reintroduce heartbeat-path disk writes, so a
controller restart may admit a replay once.

Admission is bounded on both sides: at most 32 metric keys and 64 KiB of metrics. If an intermediary
strips the extension headers, the request degrades to legacy success: observations still arrive, but
exact retry deduplication, advertised cadence, and high-fidelity probe windows are unavailable. This is
the intended CDN/proxy failure mode.

**`cpu_pct` is a jiffies delta, so the first beat is a gap.** The agent's `resourceSampler`
(`internal/agent/telemetry_resource.go`) is **stateful**: it computes CPU utilisation as the delta of
`/proc/stat` busy-vs-total jiffies **between consecutive heartbeats**. The **first** heartbeat after the
daemon starts has no prior snapshot, so it emits **no** `cpu_pct` (the pointer stays nil, omitempty
drops it). This is a deliberate **gap, never a fabricated `0`** — a real 0 % CPU and "we could not yet
measure CPU" must not look alike on the chart. Subsequent beats carry a real delta, except a beat whose
`/proc/stat` counter did not advance (or wrapped, or the read failed): those likewise **omit** `cpu_pct`
rather than reporting a misleading 0, while still carrying load and memory.

## The retention store

`telemetryHistory` holds a per-`(tenant, node)` bounded record stream. A record may carry a resource
sample, newly observed completed probe attempts, newly observed numeric device samples, or any
combination of those projections. Its mode is set by whether
a durable directory is configured:

- **FileStore** (`dir != ""`) — `buf` is the **not-yet-flushed** tail; the durable per-node JSONL under
  `dir` is the real history. Its buffered plus in-flight tail is bounded to **8 MiB per node** and a
  background flusher drains it to disk.
- **MemStore** (`dir == ""`) — there is nothing durable, so `buf` **is** the whole (capped) history
  (dev/parity mode), bounded to the same **128 MiB** per-node ceiling as durable history.

### The invariant: a heartbeat never touches disk

`RecordTelemetry` projects charted keys and records the result **in memory only** — take the history
mutex (its **own** lock, never the store-wide `mu` nor `telemetryMu`, so history can never stall or
deadlock a beat), append to `buf`, front-evict anything past the logical/volatile limits, unlock.
**No disk IO.** This preserves the standing DoS invariant that a high-frequency authenticated
heartbeat can never be turned into an fsync amplifier. The cap itself is read from an **in-memory
cache** (`capByTenant`), never from settings on disk — see the cap section. If storage or memory bounds
must evict observations, the loss appears honestly as a chart gap and never fails liveness reporting.

### The off-heartbeat flusher (FileStore)

A single background goroutine (`historyFlushInterval`, **5 min** — well above the 30 s heartbeat, so a
burst of beats collapses into one batched append) runs `flushOnce`:

1. Under the lock, **drain** each node's `buf` into a job list and clear `buf`.
2. **Outside** the lock, write each job's records to the node's append-only JSONL one bounded line at a
   time. So an `append` on the heartbeat path never blocks on disk, even mid-flush.
3. A write **failure re-queues** the drained samples at the front (retry next tick) and **never**
   surfaces to the heartbeat.

The append path repairs only a torn final JSONL fragment, fsyncs the accepted batch, and never requeues
an already-committed batch merely because later close/maintenance diagnostics fail. Compaction and
byte-triggered rewrites stream a bounded suffix through a protected same-directory temporary file,
fsync it, atomically replace the old file, and synchronize the parent directory. They use fixed
64 KiB copy buffers; neither a full history file nor a full compacted output is materialized in memory.

**Two independent retention bounds.** The logical record target is applied immediately by every query,
even while physical compaction is amortized. Ordinary line-count compaction runs after a file grows
past `cap × historyCompactSlack` (slack = **2**). Independently, every node file has a hard
**128 MiB** physical ceiling; a byte-triggered rewrite aims for **96 MiB** to leave append headroom.
The byte ceiling wins over the configured record target because active-probe and device records are
variable width. One encoded record is bounded to 1 MiB.

Startup scans and cap-change work run on the same off-heartbeat maintenance goroutine. Existing files
from an older release and files for offline nodes therefore converge without waiting for another
heartbeat. Cap changes are coalesced in a lossless tenant set, rather than a fixed queue that could
drop one tenant under a burst.

On shutdown, `close` stops the ticker and does one **best-effort final drain**.

### The configurable cap

`DefaultTelemetryHistoryCap = 20160` records per node ≈ **7 days at a 30 s heartbeat**. The operator
overrides it in `ControllerSettings.TelemetryHistoryCap` (`*int`), read through
`EffectiveHistoryCap()`:

- **`nil`** → the default (20160).
- **`0`** → history **disabled**: `append` drops every sample (no memory growth), the flusher writes
  nothing, and the query returns `disabled: true`.
- **`N > 0`** → target the last `N` records per node, subject to the authoritative byte ceiling.

**A persisted `cap = 0` survives a controller restart.** The in-memory cap cache starts empty, and the
heartbeat path must never read settings from disk — so a naively-empty cache would fall back to the
default (`> 0`) and start writing history the operator had disabled. `capLoader` closes this: on a
tenant's **first flush** (off the heartbeat path), `ensureSeeded` reads the persisted cap from settings
**once** and seeds the cache, so a disabled tenant stays disabled across restarts. `append` never calls
it; the seed only ever happens in the flusher. The loader's read is side-effect-free, and the loaded
value is installed only if the tenant cache is still absent after that read. Explicit settings GET/PUT
operations publish their cap inside the same backend critical section as their persistent read/write.
Consequently, neither a stale startup read nor a stale GET can resume after and overwrite a newer
operator write.

## Typed active-probe attempts

The existing `probe_results` latest array remains the Fleet status source and the compatibility
projection for rc.9 agents. After `probe-samples-v1` negotiation, updated agents additionally emit a
maximum-64 `probe_samples` rolling window of completed attempts, including effective signed cadence.
The controller gives the high-fidelity key precedence, then deduplicates overlap with the latest
snapshot. Initial `pending` rows never enter history. Reaching 32 completions since the last snapshot
schedules at most one coalesced early collection; the remaining half-window provides bounded headroom
while the reliable uploader is busy.

Before retention, every ICMP, TCP, or URL attempt is validated against the closed probe result
contract. Its agent `checked_at` must parse and remain inside the same bounded replay/future-skew
window as the normalized outer heartbeat sample. Exact attempt identity includes executable series
identity and check time; repeated latest snapshots, overlapping rolling windows, and reliable retries
therefore add one attempt. The deduper is bounded and volatile on the heartbeat path; query-time exact
deduplication is the restart-safe backstop. One private per-node record stream is used rather than
attacker-selectable per-probe filenames.

`interval_ms` remains advisory. An out-of-range/future cadence is cleared to “unknown” while the
otherwise valid authenticated attempt is retained; untrusted cadence can therefore neither erase the
measurement nor stretch gap detection.

Series use an opaque SHA-256 identity derived from the exact executable selector:

- ICMP/TCP: `id + type + host + port`.
- URL: `id + type + exact URL + effective expected status`.

Changing a target or URL success contract under the same human ID creates a new series. Topology-only
`name` is absent from the executable result and never splits history. At most the sixteen
most-recent exact probe series in the requested window are returned for a node.

Availability and latency retain different facts:

- A successful attempt contributes one success and its measured latency.
- A URL response whose actual status differs from the predefined expected status contributes an
  `unexpected_status` failure **and** its measured response latency, because the transaction
  completed.
- A transport/DNS/permission/timeout failure contributes one attempted failure and a stable reason,
  but no latency.
- Missing telemetry contributes nothing and remains a chart gap.

The actual URL response code is useful in the latest-result view, but is intentionally not retained or
charted. History retains the exact expected-code contract, success/failure counts, stable failure
reason, and any valid latency. It never manufactures `0 ms` for a missing latency.

## Automatic device observations

Automatic device telemetry is opt-in signed policy, but collection is local and independent from
heartbeat upload. While `all-eligible-v1` is active, the agent discovers eligible disks,
filesystems, and GPUs and collects on its own 30-second cadence. A completed collection nudges the
ordinary heartbeat loop; it does not open a second transport or perform disk IO in the controller's
heartbeat handler.

The two live metrics have deliberately different history treatment:

- `device_inventory` carries kind, opaque device identity, display metadata, and categorical support
  status. It is live-only: retaining changing names/status blobs would not yield a useful numeric
  graph.
- `device_samples` carries only bounded numeric values associated with that inventory. It is charted
  and projected into `DeviceHistorySample`.

The closed numeric catalog is filesystem used %, disk read/write bytes per second, disk I/O busy %,
GPU utilization %, and GPU VRAM used %. Every sample must match one inventory entry; percentage
values are finite and within 0–100, rates are finite and non-negative, and an observed zero is valid
data. Unsupported or missing metrics, the first disk counter baseline, and failed/timed-out
collections emit no numeric point and therefore remain gaps.

Only numeric device values enter retained JSONL. Inventory display metadata, mount labels, vendor/model
text, provider status, and status details do not. The agent's `sampled_at` collection clock is accepted
only inside the same bounded replay/future-skew window as the outer telemetry sample; otherwise the
device payload is dropped rather than stamping a cached snapshot as freshly observed.

Device series identity is the exact `kind + opaque device_id` pair. Mutable labels cannot splice
series. Store and API queries support only one exact device selector at a time; there is deliberately
no broad all-device history query.

## The query API — `GET …/node-history`

Operator-gated (operator mux only — observability, but authenticated like every other node view).
`HandleNodeHistory` takes `?node=<id>&from=<RFC3339>&to=<RFC3339>&step=<duration>` and returns a
bucketed series the charts render directly.

Resource history is always part of the response. For compatibility, omitted probe selectors return up
to the bounded all-probe set; `include_probes=false` omits it. An exact probe selector is
type-specific:

- ICMP: `probe_id`, `probe_type=icmp`, and `probe_host`.
- TCP: `probe_id`, `probe_type=tcp`, `probe_host`, and `probe_port`.
- URL: `probe_id`, `probe_type=url`, `probe_url`, and the effective
  `probe_expected_status`.

Device history is exact-only and off by default. It requires `include_devices=true` together with one
closed `device_kind` (`block_device`, `filesystem`, or `gpu`) and the opaque `device_id`. Supplying a
device selector while `include_devices` is false is invalid. The node-detail panel uses these exact
selectors, so changing one picker does not fetch unrelated probe or device streams:

```json
{
  "step": "30s",
  "disabled": false,
  "buckets": [
    {
      "t": "…",
      "load1": {"avg": 0.2, "min": 0.1, "max": 0.3},
      "load5": {"avg": 0.2, "min": 0.1, "max": 0.3},
      "load15": {"avg": 0.2, "min": 0.1, "max": 0.3},
      "cpu_pct": {"avg": 8.0, "min": 4.0, "max": 12.0},
      "mem_used_pct": {"avg": 45.0, "min": 44.0, "max": 46.0}
    }
  ],
  "probes": [
    {
      "series_id": "…",
      "id": "service-health",
      "type": "url",
      "url": "https://api.example.net/health?full=0",
      "expected_status": 204,
      "interval_ms": 30000,
      "buckets": [
        {
          "t": "…",
          "attempts": 2,
          "successes": 1,
          "failures": 1,
          "latency_ms": {"avg": 15.5, "min": 12.4, "max": 18.6},
          "failure_reasons": {"unexpected_status": 1}
        }
      ]
    }
  ],
  "devices": [
    {
      "series_id": "…",
      "device_id": "…",
      "kind": "gpu",
      "buckets": [
        {
          "t": "…",
          "metrics": {
            "gpu_utilization_pct": {"avg": 21.0, "min": 10.0, "max": 32.0},
            "gpu_vram_used_pct": {"avg": 38.0, "min": 36.0, "max": 40.0}
          }
        }
      ]
    }
  ]
}
```

**Validation + step clamping (defense-in-depth — the retained history is capped anyway):**

- `from` / `to` must parse as RFC3339, `to` must be **after** `from`, and the window must be
  `≤ maxHistoryRange` (**366 days**).
- `step` is **optional**. With an explicit value, the legacy contract remains: floor it at **1 s** and
  widen it only when necessary to keep the response under the shared bucket budget.
- **Auto** first prefers the most recent valid advertised `IntervalMS`. If none exists, it sorts a copy
  of the samples, derives positive timestamp deltas, requires at least two, chooses the lower median to
  resist outage gaps, and rounds to the nearest second. Auto is floored at **30 s**, then widened for
  the same bucket cap. Insufficient data falls back to 30 s. The **effective** step is echoed back in
  `step`, so the panel renders and detects gaps using what the server actually chose.
- The response budget is **1000 bucket objects globally across the resource stream, every selected
  probe series, and the one selected device series**, not 1000 per series. The controller widens one
  shared epoch-stable step according to stream count. A wide range therefore returns compact rollups;
  narrowing the range or choosing a finer Resolution exposes more detail without downloading the
  full retained stream.
- Unknown `node` → **404**. History disabled (cap 0) → **200** with `{ disabled: true, buckets: [] }`
  (the panel shows a "history off" hint instead of an empty chart).

The FileStore query first discovers only the newest logical/byte-bounded JSONL suffix with a fixed-size
backward scan, then decodes it with request-context cancellation. Exact probe and device selectors
are pushed into that store query; an omitted device selector skips device materialization altogether,
and unrelated attempts/samples are not retained in the response snapshot. HTTP response compression
may be enabled by the deployment's reverse proxy/CDN, but payload correctness and the 1000-bucket
bound do not depend on intermediaries compressing or caching the request.

**Aggregation (`aggregateHistory`, pure + table-tested).** Samples are bucketed on a stable Unix-epoch
grid rather than re-phased from each moving request's `from`; re-fetching a sliding window therefore
does not move existing bucket timestamps. Each bucket reports **avg / min / max** per metric with the
bucket **start** as `t`. The online mean stays finite for finite inputs instead of overflowing an
intermediate sum. Two honesty rules:

- **Empty buckets are OMITTED** — a gap in the data (node offline, history just enabled) stays a gap on
  the chart; no interpolation, no zero-fill.
- **`cpu_pct` and `mem_used_pct` are absent** from a bucket when **no** sample in it carried that metric
  (again: a gap, never a fabricated 0). `load*` is always present because every sample has it.

Device aggregation uses the same rules independently for each closed numeric definition applicable to
the selected kind. A metric key is absent from a device bucket when it was not measured; an observed
zero is accumulated normally and remains zero. Unknown keys, wrong-kind values, non-finite values,
negative values, and percentages over 100 are discarded defensively.

**`mem_used_pct` is derived at query time** from `mem_total_kb` / `mem_available_kb`:
`(total − avail) / total × 100`, clamped to `[0, 100]`, and `ok = false` (contributes nothing) when
total is unknown/zero. The raw sample stores the two absolute KB figures; the percentage is computed
only when charted, so the retention format stays close to what the agent measured.

Probe buckets carry the effective cadence of their newest attempt. The panel uses cadence on both
sides of a schedule transition when deciding whether to insert a gap, and falls back to the exact
current signed policy only for rc.9 history that lacks bucket cadence. A slow current policy therefore
cannot retroactively connect an outage between older fast-cadence samples. URL buckets expose the
expected status and latency/availability aggregates, but never the transient actual response code.

## Freshness — why history is never frozen

The chart is only as honest as the heartbeat feeding it. Historically a class of bug had a new metric
fire **only at deploy time** and then freeze, because a metric got bolted onto the apply-time `/report`
path instead of the heartbeat. The fix (see [controller-api.md](../controller/controller-api.md)
§`POST /telemetry`) makes the **Sampler heartbeat the sole producer of `metrics`** — `/report` carries
**conditions only, never metrics**, so `resource` exists only on the heartbeat path — and adds a
**post-apply kick**: after each applied cycle the agent nudges the heartbeat loop so a fresh sample
(with the just-applied state) lands immediately rather than up to one interval later. So resource
history advances on the heartbeat cadence **and** promptly after every deploy — it is never a frozen
apply-time snapshot. The post-apply kick may create one short delta; cadence-aware Auto uses the lower
median or the latest advertised interval rather than mistaking that kick for the sustained reporting
rate. The chart receives the effective step. A metric-absent bucket is always a null point, and runs
longer than one empty effective bucket break the line instead of connecting across an outage. One
empty effective bucket is tolerated because healthy samples near opposite bucket boundaries can
produce that shape under ordinary scheduling jitter. (Conditions, unlike metrics, are still
dual-written by `/report` at
apply-time and refreshed live by the heartbeat — last-writer-wins — but that is the conditions path,
not this metric.)

Automatic device collection follows the same transport rule without being tied to heartbeat timing.
The collector runs every 30 seconds while signed opt-in policy is active, stores one bounded volatile
inventory/sample snapshot, and coalesces a heartbeat nudge after completion. The payload carries its
own bounded `sampled_at` collection time; replaying the same snapshot cannot turn old device readings
into fresh history. A collection with no supported numeric value therefore produces a gap, while a
real measured zero remains a point.

The Fleet card displays that effective step. If an explicit Resolution was widened to satisfy the
global response budget, the UI says so rather than leaving the finer requested value as an implied
claim about the rendered buckets. Axes use clock labels for short windows and add a date component for
24-hour/multi-day windows.

Fleet's opt-in **Live** control refreshes immediately and then schedules the next authenticated read
ten seconds after the previous read completes. Requests never overlap, hidden tabs pause, and the UI
shows refreshing/last-success/next-refresh plus delayed or stale state. The history card refetches when
that node's `last_seen` advances, keeps the last successful charts through a transient failure, and
shows its own updating, success-time, or stale-with-warning feedback; an explicit Retry works even for
an offline node whose `last_seen` cannot advance. The ten-second Live/manual request fetches only the
node observation snapshot—not the full audit chain, Settings, or keystone status—and its freshness
clock advances only after that node read succeeds. Routine animation honors the browser's
reduced-motion preference and routine ten-second transitions are not repeatedly announced to
assistive technology.

## Implementation verification (2026-07-17)

This contract was re-verified on 2026-07-17 against the shared telemetry catalog, protocol-v2
uploader/receipt path, `internal/controller/telemetry_history.go`, the exact-selector and aggregation
logic in `internal/api/telemetry_history.go`, FileStore/MemStore parity tests, and Fleet history query
and chart code. Those implementations and their focused contract tests are authoritative if a later
change makes this document stale.

## Cross-references

- Live heartbeat + the `Sampler` framework + the post-apply kick:
  [controller-api.md](../controller/controller-api.md) §`POST /telemetry`.
- Signed ICMP/TCP/URL checks and opt-in automatic device telemetry carried in the same metrics
  framework: [active-telemetry.md](active-telemetry.md).
- Persistence / the store contract: [../controller/persistence.md](../controller/persistence.md).
- The node-detail charts (frontend): the reusable `TimeSeriesChart` + lazy-loaded Recharts chunk
  render this series; see the panel.
