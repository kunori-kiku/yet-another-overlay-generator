# Controller telemetry

<!-- last-verified: 2026-07-17 -->

## Responsibility

Accept authenticated agent observations, keep receipt-authoritative live state, deduplicate reliable
replays, retain charted metrics within bounded history, and serve globally bounded chart rollups
(`internal/api/handler_agent.go:336-383`, `internal/api/telemetry_history.go:23-44`).

## Files

- `internal/controller/storecore_telemetry.go:24-348` — owns the volatile latest-state overlay and
  bounded per-boot sequence cursors.
- `internal/telemetrymetric/catalog.go:1-65,117-185` — declares every production metric's charted or
  live-only disposition, chart family, priority, and live-surface visibility.
- `internal/controller/telemetry_history.go:25-80,320-451` — projects catalogued metrics into shared
  resource, probe, and device history and validates projector/catalog parity at startup.
- `internal/controller/telemetry_history.go:569-838,900-1062,1071-1304` — enforces the volatile tail,
  logical cap, background maintenance, durable FileStore flush, and physical file ceiling.
- `internal/api/telemetry_history.go:849-941` — validates operator history selectors and produces
  effective-resolution chart responses under one response-wide bucket budget.

## Inputs

`controller-agent-api` supplies a bearer-authenticated node identity and a legacy-compatible
`telemetryRequestJSON{conditions, metrics, agent_version}`; protocol v2 adds boot, sequence,
sample-time, and cadence headers without changing that JSON body
(`internal/api/routes_controller.go:244-270`, `internal/api/wire_controller.go:68-80`,
`internal/api/handler_agent.go:278-307`). `panel-telemetry` supplies authenticated operator history
queries containing a time window and optional exact probe/device selectors
(`internal/api/routes_controller.go:277-330`, `internal/api/telemetry_history.go:849-884`).

## Outputs

The live path exposes controller-stamped `LastSeen`, conditions, agent version, and the catalogued
latest metrics through Fleet reads, while the history path returns one coherent typed snapshot for
server-side rollup (`internal/controller/storecore_telemetry.go:86-110,218-348`,
`internal/controller/store.go:570-623`). A valid v2 upload receives its exact acknowledged sequence,
controller receipt time, duplicate flag, and `probe-samples-v1` capability; see
`specs/agent-telemetry.md` for queue retirement and capability rollback on the agent
(`internal/api/handler_agent.go:325-334,375-383`, `internal/telemetryprotocol/protocol.go:8-25`).

## Decision points (if any)

- With no protocol header, the controller accepts the legacy heartbeat and returns the legacy success
  shape; with protocol `2`, it validates reliable-delivery headers and emits a receipt. Missing,
  stripped, or non-advertised receipt capability safely leaves the agent on its legacy probe-result
  contract (`internal/api/handler_agent.go:278-307,348-383`,
  `internal/agent/heartbeat_reliable.go:43-53,83-120`).
- A repeated sequence for the same boot advances receipt-based liveness but cannot replace newer live
  metrics or append history again; a new boot is selected by bounded receipt-order rules, and ambiguous
  restarts drop executable capability evidence (`internal/controller/storecore_telemetry.go:218-348`).
- History cap `0` returns an explicit disabled response. Otherwise exact probe/device selectors are
  pushed into `QueryTelemetryHistorySnapshotFiltered`, and the number of selected streams can widen
  the shared step to keep the whole response within 1000 buckets
  (`internal/api/telemetry_history.go:893-941`, `internal/api/telemetry_history.go:115-137`).

## Invariants

- Controller receipt time, not an agent clock, is authoritative for liveness and condition age.
  Bounded sample time timestamps history and is advisory input to stale or ambiguous cross-boot
  live-overlay ownership; it never establishes liveness
  (`internal/controller/storecore_telemetry.go:218-224,254-348,366-397`).
- Every charted catalog metric has exactly one matching history projector and accumulator; live-only
  metrics carry a reason instead of silently disappearing before charts
  (`internal/telemetrymetric/catalog.go:43-53,117-170`,
  `internal/controller/telemetry_history.go:335-430`).
- Heartbeat ingestion performs no synchronous history disk I/O. A cap change trims the logical view
  immediately; FileStore retains at most an 8 MiB volatile tail and a 128 MiB per-node JSONL, rewrites
  toward 96 MiB, and runs physical startup/cap-change convergence on its background worker
  (`internal/controller/telemetry_history.go:25-55,686-700,770-810`,
  `internal/controller/telemetry_history.go:900-1011,1171-1235,1723-1815`).

## Gotchas (optional)

- Sequence cursors and the live overlay are intentionally volatile, so a controller restart may admit
  one replay again; bounded history and query-time deduplication remain the safety backstop
  (`internal/controller/store.go:194-203`, `internal/controller/telemetry_history.go:1691-1720`).
- Probe and device query selection is exact-series state, not a display-name match; FileStore filters
  records during its bounded scan and decodes only the selected device row
  (`internal/controller/telemetry_history.go:137-150,1618-1661`,
  `internal/controller/telemetry_history.go:2070-2122`).
- Routine `/report` and `/telemetry` calls update Fleet state without appending to the durable audit
  chain; old `report` rows remain a compatibility concern of the raw audit reader
  (`internal/api/handler_agent.go:240-268,336-383`, `internal/api/handler_audit.go:15-35`).
