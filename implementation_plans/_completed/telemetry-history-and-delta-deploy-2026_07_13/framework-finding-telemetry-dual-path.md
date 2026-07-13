# Framework finding — the recurring "new telemetry only fires at deploy" defect

> Root-cause investigation (2026-07-13, 3 parallel Opus traces: agent emission parity · controller
> storage/projection · structural root cause). Owner-prompted: the bug has recurred multiple times and
> been patched one signal at a time. All three traces converged.

## The two emitters (the structural trap)

| | Apply-time `/report` | Heartbeat `/telemetry` (30s) |
|---|---|---|
| Trigger | each `cycle()` apply (`agent.go` persistAndReport → `ControllerClient.Report`) | `runHeartbeat` immediate + every `telemetry-interval` |
| Conditions | hand-assembled by `collectConditions` (`internal/agent/conditions.go`) | `conditionSampler` **re-runs** `collectConditions` |
| Metrics | **none** (`reportRequestWire` has no metrics field) | the sampler set (`resource`/`native_xdp`/`mimic_capability`/`wireguard_peers`) |
| Controller sink | `SetAppliedGeneration` — writes `{AppliedGeneration, LastChecksum, LastHealth, LastAgentVersion, Conditions}` | `RecordTelemetry` — writes `{Conditions, Telemetry(metrics), LastSeen, version}` |
| Conditions slot | `n.Conditions` (wholesale) | `n.Conditions` (wholesale) — **the same slot** |

## Why it recurs (unenforced accidents, not guarantees)

1. **PARITY** — a signal rides both paths only if the author remembers to add it to `collectConditions`
   AND (for a metric) a heartbeat sampler. Nothing (types / framework / CI) fails when they don't.
2. **FRESHNESS** — an emitter is live only if the author re-measures. `conditionSampler` re-running
   `collectConditions` gives *invocation* parity, not *freshness*: `readMimicCondition` reads a
   deploy-time breadcrumb, `selfUpdateCondition`/`configApplyCondition` read frozen apply state; only
   `sampleWireGuardCondition` truly re-measures (`wg show`).
3. **Store wholesale-replace** — the 30s heartbeat REPLACES `n.Conditions` every beat, so a deploy-only
   condition is destroyed on the first beat. Metrics are heartbeat-ONLY (and FileStore-overlay-only, so
   also volatile on restart) → invisible at deploy until the first ≤30s beat (the mirror symptom).

The natural authoring pattern — "record the deploy outcome, read it back" — satisfies NEITHER, reads as
"working" in dev (appears at deploy), and fails only on live-fleet smoke. Hence the recurrence:
`#177` (the original heartbeat, for all conditions) → `#242` (mimic breadcrumb: a live systemctl
reconcile bolted in) → panel-side `98f1498`/`8c584fe`. Each a per-signal patch of one framework defect.

## Convergent fix — collapse to ONE path; apply TRIGGERS the heartbeat

- **plan-1.5 (this subject):** a post-apply **kick** beats the SAME `Telemetry` instance over all
  samplers → any signal authored as a Sampler fires at deploy (kick) AND every interval, structurally.
  Plus an input-mutation **liveness guard** test + a documented freshness principle. (A type-parity test
  alone is necessary-but-insufficient — it would not catch a value-frozen sampler.)
- **Deferred follow-ons (custody-sensitive / owned by plan-2):** make `/report` custody-only (drop
  conditions); durable metrics slot (plan-2's history store); conditions source-tagged merge vs
  wholesale-replace.

## Specifics for THIS subject
- `cpu_pct` (plan-1) is a stateful `/proc/stat` delta — structurally incompatible with a one-off apply
  emission; it NEEDS regular beats. Correctly heartbeat-only; the kick makes the resource metric visible
  at deploy and primes the cpu snapshot there.
- plan-2 history appends metrics inside `RecordTelemetry` (heartbeat) only → without the kick it LOSES
  the deploy-instant sample (the highest-CPU moment). The kick restores it.
