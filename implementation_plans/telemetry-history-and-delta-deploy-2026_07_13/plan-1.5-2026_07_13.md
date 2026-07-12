# plan-1.5 — unify observability on the sampler heartbeat (post-apply kick) + freshness guards

**Why (inserted 2026-07-13):** the owner flagged a RECURRING bug — new telemetry "only fires at
deployment, then goes stale." A 3-trace Opus investigation (see
`framework-finding-telemetry-dual-path.md`) found the ROOT CAUSE: the agent has **two** observability
emitters with nothing forcing parity or freshness — apply-time `/report` (hand-assembled conditions,
its real job is deploy custody; NO metrics) and the 30s heartbeat `/telemetry` (a SEPARATE
hand-assembled sampler set: conditions + metrics). A new signal is "on both paths" only if the author
remembers two registration points, and "fresh" only if the author re-measures instead of reading a
deploy artifact. The natural authoring pattern (record the deploy outcome, read it back) satisfies
neither → appears at deploy, then the heartbeat omits it (metric) or re-emits its frozen value
(condition), and the store **wholesale-replaces** conditions each beat so a deploy-only condition
VANISHES. Historically patched one signal at a time (#177 the whole heartbeat; #242 mimic breadcrumb;
panel freezes 98f1498/8c584fe).

**The fix (the investigation's convergent recommendation, scoped safe):** collapse to ONE emission
path — **apply merely TRIGGERS the heartbeat**. A post-apply kick beats the SAME long-lived
`Telemetry` instance over ALL samplers, so any signal authored as a Sampler fires at deploy (the kick)
AND every interval, by construction. This must come BEFORE plan-2 (history): the kick also gives
history a sample at the deploy instant (the highest-CPU moment — install.sh Phase-0 teardown + WG
bounce — which today produces no sample).

Reads from specs: `(none — agent telemetry framework is unmodeled; investigation doc is the ground truth)`.

## Changes (AGENT-ONLY — custody wire/stores untouched; see Deferred)

### 1. Post-apply kick (`cmd/agent/main.go`)
- `runHeartbeat` gains a `kick <-chan struct{}` and `select`s over the ticker AND the kick, calling the
  same `beat()` on either. The beat still runs on the SINGLE heartbeat goroutine over the SAME
  `Telemetry` instance built once at `BuildTelemetry` — do NOT construct a second instance (that would
  corrupt `resourceSampler`'s stateful cpu_pct delta). The immediate startup `beat()` is unchanged.
- The daemon apply loop (`o.telemetryInterval > 0`) creates a **buffered, coalescing** `kick := make(chan
  struct{}, 1)` and, after each successful `cycle()` (at `lastAppliedGen = resumeGen`, reached only when
  `err == nil` — apply/idle/rekey-wake, State freshly persisted), sends **non-blocking**:
  `select { case kick <- struct{}{}: default: }`. So a busy loop never blocks on a slow beat, and at most
  one kick is pending. The heartbeat goroutine picks it up and emits a live sampler beat reflecting the
  just-persisted State + fresh metrics.
- `telemetryInterval <= 0` (heartbeat disabled) → no kick channel, no send (unchanged behavior).

### 2. Freshness principle, documented in the framework (`internal/agent/telemetry.go`)
- Extend the `Sampler` / `BuildTelemetry` doc: a Sampler MUST re-measure LIVE each beat and never cache
  a deploy-time value; a sampler that reads a deploy artifact (a breadcrumb) MUST reconcile it with live
  state (cite the mimic `mimicUnitActiveFn` reconcile, #242) or it re-emits a frozen value forever. This
  is the freshness half the kick alone does NOT fix (the kick fixes deploy-appearance + no-vanish; a
  breadcrumb-only condition still freezes between deploys without a live reconcile).

## Tests (perpetual — framework guards)
- `cmd/agent`: a `runHeartbeat` test proving the KICK triggers an extra beat (inject a fake
  `client.Telemetry` counter + a kick channel; send a kick → assert an additional POST beyond the
  startup beat + ticker), and that a non-blocking send never blocks when the buffer is full.
- `internal/agent` liveness guard (`telemetry_liveness_test.go`): for the re-measuring samplers with
  injectable inputs — `resourceSampler` (statFn/loadavgFn) and `wireguardPeersSampler`/`conditionSampler`
  (wgShowFn) — assert that MUTATING the injected input MUTATES the emitted signal (a frozen sampler that
  ignored its input would fail). Plus a registration-set assertion: `BuildTelemetry` registers exactly
  the expected sampler names (adding a sampler forces a conscious test update — the parity tripwire).
  NOTE (from the investigation): a type-parity test is necessary-but-INSUFFICIENT (it would not catch a
  value-frozen sampler whose Type is present); the KICK + this input-mutation liveness check are what
  actually bite.

## Verify + branch
`go build/vet/test` (default+airgap) + gofmt; FE untouched. No golden/conformance/drift impact (agent
runtime wiring only; the metric wire shape is unchanged). Branch `fix/telemetry-unify-apply-kick`.
Local uses `GOTOOLCHAIN=local` (1.26.4); CI runs the pinned 1.26.5.

## Deferred (documented follow-ons — NOT in this plan)
- **Make `/report` custody-only** (drop Conditions from the report wire + `SetAppliedGeneration`; delete
  `refreshTelemetryOverlayFromReport`). The kick makes /report's conditions redundant, but removing them
  touches the SECURITY-CRITICAL deploy-custody path — deferred to avoid destabilizing custody inside
  this subject. Follow-on subject.
- **Metrics durability across controller restart** (FileStore metrics are overlay-only). plan-2's history
  store already introduces bounded, periodically-flushed durable metrics; live-metric volatility (repop
  within one beat) is acceptable. Covered by plan-2, not here.
- **Conditions source-tagged merge / per-Type TTL** instead of wholesale-replace. The kick resolves the
  deploy-vanish case (apply IS a beat now); a partial-beat metric wipe is transient (next beat repops).
  Deferred as a subtle robustness change that could mask genuine signal loss.

## Insertion-point markers
- **plan-1.5.5** if the kick surfaces a concurrency issue with `Telemetry.Collect` running on the
  heartbeat goroutine while the apply loop mutates shared State (it should NOT — Collect reads persisted
  State via LoadState, and samplers own their state; but if a shared-memory hazard appears under `-race`,
  STOP and reconsider the trigger mechanism rather than adding locks that fight the framework's
  single-goroutine design).
