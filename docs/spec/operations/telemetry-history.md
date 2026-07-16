# Telemetry resource history (node-detail CPU/RAM/load charts)

This document defines how the controller **retains** the per-node host-resource readings that the
node-detail page charts over time, and how the operator **queries** them back as a bucketed series. It
is the durable backing for the CPU / RAM / load-average charts; it sits **strictly on top of** the live
telemetry heartbeat ([controller-api.md](../controller/controller-api.md) §`POST /telemetry`) and adds
**no** parallel agent transport — the agent already emits a `resource` metric over authenticated HTTP;
this layer keeps a bounded history of it and serves an aggregated query. Short reporting interruptions
are handled by protocol-v2 sequence headers and a bounded replay queue, not WebSocket or gRPC.

Three pieces, each independently testable:

1. The **resource sampler** on the agent (what a sample contains, including `cpu_pct`).
2. The **retention store** in the controller (`internal/controller/telemetry_history.go`) — an
   in-memory buffer per node with an off-heartbeat background flush to append-only JSONL, a
   configurable cap, and one hard invariant: **a heartbeat never touches disk**.
3. The **query API** (`internal/api/telemetry_history.go`, `GET …/node-history`) — server-side
   bucketed aggregation the charts render.

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
exact retry deduplication and advertised cadence are unavailable. This is the intended CDN/proxy
failure mode.

**`cpu_pct` is a jiffies delta, so the first beat is a gap.** The agent's `resourceSampler`
(`internal/agent/telemetry_resource.go`) is **stateful**: it computes CPU utilisation as the delta of
`/proc/stat` busy-vs-total jiffies **between consecutive heartbeats**. The **first** heartbeat after the
daemon starts has no prior snapshot, so it emits **no** `cpu_pct` (the pointer stays nil, omitempty
drops it). This is a deliberate **gap, never a fabricated `0`** — a real 0 % CPU and "we could not yet
measure CPU" must not look alike on the chart. Subsequent beats carry a real delta, except a beat whose
`/proc/stat` counter did not advance (or wrapped, or the read failed): those likewise **omit** `cpu_pct`
rather than reporting a misleading 0, while still carrying load and memory.

## The retention store

`telemetryHistory` holds a per-`(tenant, node)` `nodeHist{ buf, fileLines }`. Its mode is set by whether
a durable directory is configured:

- **FileStore** (`dir != ""`) — `buf` is the **not-yet-flushed** tail; the durable per-node JSONL under
  `dir` is the real history, capped at flush time. A background flusher drains `buf` to disk.
- **MemStore** (`dir == ""`) — there is nothing durable, so `buf` **is** the whole (capped) history
  (dev/parity mode).

### The invariant: a heartbeat never touches disk

`RecordTelemetry` calls `append`, which records the sample **in memory only** — take the history mutex
(its **own** lock, never the store-wide `mu` nor `telemetryMu`, so history can never stall or deadlock a
beat), append to `buf`, front-evict anything past the cap, unlock. **No disk IO, O(1).** This preserves
the standing DoS invariant that a high-frequency authenticated heartbeat can never be turned into an
fsync amplifier. The cap itself is read from an **in-memory cache** (`capByTenant`), never from settings
on disk — see the cap section.

### The off-heartbeat flusher (FileStore)

A single background goroutine (`historyFlushInterval`, **5 min** — well above the 30 s heartbeat, so a
burst of beats collapses into one batched append) runs `flushOnce`:

1. Under the lock, **drain** each node's `buf` into a job list and clear `buf`.
2. **Outside** the lock, write each job's samples to the node's append-only JSONL. So an `append` on the
   heartbeat path never blocks on disk, even mid-flush.
3. A write **failure re-queues** the drained samples at the front (retry next tick) and **never**
   surfaces to the heartbeat.

**Amortized compaction.** The common flush is a pure **append**. A node's JSONL is rewritten to its last
`cap` lines only once it grows past `cap × historyCompactSlack` (slack = **2**) lines — so the O(cap)
rewrite is amortized to roughly once per `cap` samples, not every flush. `fileLines` (`-1` until counted
once from disk) tracks the line count so compaction can decide without re-reading the file each tick.

On shutdown, `close` stops the ticker and does one **best-effort final drain**.

### The configurable cap

`DefaultTelemetryHistoryCap = 20160` samples per node ≈ **7 days at a 30 s heartbeat**. The operator
overrides it in `ControllerSettings.TelemetryHistoryCap` (`*int`), read through
`EffectiveHistoryCap()`:

- **`nil`** → the default (20160).
- **`0`** → history **disabled**: `append` drops every sample (no memory growth), the flusher writes
  nothing, and the query returns `disabled: true`.
- **`N > 0`** → retain the last `N` samples per node.

**A persisted `cap = 0` survives a controller restart.** The in-memory cap cache starts empty, and the
heartbeat path must never read settings from disk — so a naively-empty cache would fall back to the
default (`> 0`) and start writing history the operator had disabled. `capLoader` closes this: on a
tenant's **first flush** (off the heartbeat path), `ensureSeeded` reads the persisted cap from settings
**once** and seeds the cache, so a disabled tenant stays disabled across restarts. `append` never calls
it; the seed only ever happens in the flusher.

## The query API — `GET …/node-history`

Operator-gated (operator mux only — observability, but authenticated like every other node view).
`HandleNodeHistory` takes `?node=<id>&from=<RFC3339>&to=<RFC3339>&step=<duration>` and returns a
bucketed series the charts render directly:

```json
{ "step": "30s", "disabled": false,
  "buckets": [ { "t": "…", "load1": {"avg":…,"min":…,"max":…}, "load5": …, "load15": …,
                 "cpu_pct": {"avg":…,"min":…,"max":…}, "mem_used_pct": {"avg":…,"min":…,"max":…} } ] }
```

**Validation + step clamping (defense-in-depth — the retained history is capped anyway):**

- `from` / `to` must parse as RFC3339, `to` must be **after** `from`, and the window must be
  `≤ maxHistoryRange` (**366 days**).
- `step` is **optional**. With an explicit value, the legacy contract remains: floor it at **1 s** and
  widen it only when necessary to keep the response at no more than **1000** buckets.
- **Auto** first prefers the most recent valid advertised `IntervalMS`. If none exists, it sorts a copy
  of the samples, derives positive timestamp deltas, requires at least two, chooses the lower median to
  resist outage gaps, and rounds to the nearest second. Auto is floored at **30 s**, then widened for
  the same 1000-bucket cap. Insufficient data falls back to 30 s. The **effective** step is echoed back
  in `step`, so the panel renders and detects gaps using what the server actually chose.
- Unknown `node` → **404**. History disabled (cap 0) → **200** with `{ disabled: true, buckets: [] }`
  (the panel shows a "history off" hint instead of an empty chart).

**Aggregation (`aggregateHistory`, pure + table-tested).** Samples are bucketed on a stable Unix-epoch
grid rather than re-phased from each moving request's `from`; re-fetching a sliding window therefore
does not move existing bucket timestamps. Each bucket reports **avg / min / max** per metric with the
bucket **start** as `t`. The online mean stays finite for finite inputs instead of overflowing an
intermediate sum. Two honesty rules:

- **Empty buckets are OMITTED** — a gap in the data (node offline, history just enabled) stays a gap on
  the chart; no interpolation, no zero-fill.
- **`cpu_pct` and `mem_used_pct` are absent** from a bucket when **no** sample in it carried that metric
  (again: a gap, never a fabricated 0). `load*` is always present because every sample has it.

**`mem_used_pct` is derived at query time** from `mem_total_kb` / `mem_available_kb`:
`(total − avail) / total × 100`, clamped to `[0, 100]`, and `ok = false` (contributes nothing) when
total is unknown/zero. The raw sample stores the two absolute KB figures; the percentage is computed
only when charted, so the retention format stays close to what the agent measured.

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

## Cross-references

- Live heartbeat + the `Sampler` framework + the post-apply kick: [controller-api.md](../controller/controller-api.md) §`POST /telemetry`.
- Signed outbound ICMP/TCP checks carried in the same metrics framework: [active-telemetry.md](active-telemetry.md).
- Persistence / the store contract: [../controller/persistence.md](../controller/persistence.md).
- The node-detail charts (frontend): the reusable `TimeSeriesChart` + lazy-loaded Recharts chunk render this series; see the panel.
