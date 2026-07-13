package agent

import (
	"time"

	"github.com/kunorikiku/yet-another-overlay-generator/internal/runtimecontract"
)

// telemetry.go is the agent's LIVE monitoring framework (beta9-smoke-hardening plan-1). The Node
// Conditions channel was apply-time-only — sampled once when a bundle applied (agent.go recordSuccess/
// recordFailure) and never refreshed — so the panel froze a worst-case post-apply snapshot (a
// pre-handshake wireguard:LinkDown, a mid-probation selfupdate:HealthConfirmedProbationary) even
// though the overlay + self-update were healthy. The fix is a DEDICATED heartbeat (cmd/agent's daemon
// runHeartbeat → ControllerClient.Telemetry → POST /telemetry) that re-samples on an interval, so the
// controller always reflects current health. /telemetry carries conditions + an extensible metrics map
// and DELIBERATELY no generation/checksum — observability is kept strictly separate from deploy
// custody (the controller's RecordTelemetry never touches applied_generation).
//
// This is a FRAMEWORK, not a one-off: a Sampler is one monitored signal. The condition sampler is the
// first; a future probe (e.g. per-peer handshake RTT) implements Sampler, is registered in
// BuildTelemetry, and rides the same heartbeat — writing into the metrics map (which already travels
// on the wire) with zero transport change.

// Sampler produces one monitoring signal: a set of conditions and/or a map of named metrics. Sample is
// called once per heartbeat tick; it must be cheap and self-contained (it runs best-effort under a
// recover guard, so a panic in one probe never crashes the daemon). Either return may be nil.
//
// FRESHNESS CONTRACT (plan-1.5): a Sampler MUST re-measure the LIVE system on each call — it must NEVER
// cache a value captured at apply/deploy time and re-emit it unchanged. A sampler that reads a
// deploy-time artifact (a breadcrumb file, a persisted apply outcome) MUST reconcile it against live
// state (see readMimicCondition's `systemctl is-active` reconcile) or it re-emits a FROZEN value
// forever — which reads as "works at deploy, then goes stale," the exact recurring defect this
// framework exists to prevent. Registering a signal HERE (a Sampler in BuildTelemetry) is now the SOLE
// wiring needed: the daemon beats every interval AND on a post-apply kick (cmd/agent runHeartbeat), so a
// registered signal fires at deploy AND live by construction — there is no second apply-path list to
// keep in parity. telemetry_liveness_test.go asserts the freshness contract for the injectable samplers
// (mutate the input → the output must change).
type Sampler interface {
	Name() string
	Sample(now time.Time) (conditions []runtimecontract.Condition, metrics map[string]any)
}

// Telemetry aggregates the registered Samplers into a single heartbeat payload. It is constructed once
// per daemon (BuildTelemetry) and Collect-ed each tick from the single heartbeat goroutine, so it
// needs no internal locking.
type Telemetry struct {
	samplers []Sampler
}

// Collect runs every Sampler under a recover guard and merges their output. Conditions are merged by
// Type (a later sampler's condition of the same Type supersedes an earlier one — mirroring the
// controller's wholesale per-report replace), preserving first-seen order for a stable display.
// Metrics are unioned (later keys win). A panicking sampler contributes nothing and is skipped; the
// others still produce output (best-effort observability must never take the daemon down).
func (t *Telemetry) Collect(now time.Time) (conditions []runtimecontract.Condition, metrics map[string]any) {
	condIdx := make(map[string]int) // Condition.Type -> position in conditions (stable order, last-value-wins)
	for _, s := range t.samplers {
		conds, m := sampleGuarded(s, now)
		for _, c := range conds {
			if i, ok := condIdx[c.Type]; ok {
				conditions[i] = c
				continue
			}
			condIdx[c.Type] = len(conditions)
			conditions = append(conditions, c)
		}
		for k, v := range m {
			if metrics == nil {
				metrics = make(map[string]any)
			}
			metrics[k] = v
		}
	}
	return conditions, metrics
}

// sampleGuarded runs one Sampler under a recover so a panicking probe yields (nil, nil) instead of
// crashing the heartbeat goroutine (and with it the daemon process).
func sampleGuarded(s Sampler, now time.Time) (conds []runtimecontract.Condition, metrics map[string]any) {
	defer func() {
		if r := recover(); r != nil {
			conds, metrics = nil, nil
		}
	}()
	return s.Sample(now)
}

// conditionSampler reports the agent's Node Conditions LIVE — the same set collectConditions builds at
// apply time, but re-sampled each tick from the freshly-loaded persisted State. This is what un-freezes
// the panel: re-running collectConditions re-probes `wg show` (so wireguard:LinkDown clears once
// handshakes complete) and re-derives the self-update condition from the now-finalized State (so
// selfupdate:HealthConfirmedProbationary clears after FinalizeSelfUpdate dropped the breadcrumb).
type conditionSampler struct {
	stateDir string
}

func (conditionSampler) Name() string { return "conditions" }

func (s conditionSampler) Sample(now time.Time) ([]runtimecontract.Condition, map[string]any) {
	prev, err := LoadState(s.stateDir)
	if err != nil || prev == nil || prev.LastResult == "" {
		// No state file, a transient read error, OR a node that has never completed an apply cycle
		// (LoadState returns a zero State with LastResult "" for a missing file). configApplyCondition
		// describes an APPLY outcome, so for a never-applied node collectConditions would fabricate a
		// false "degraded keeping last-good" condition — skip instead (this mirrors apply-time, where
		// collectConditions only ever runs AFTER an apply set LastResult).
		return nil, nil
	}
	return collectConditions(prev, prev.LastResult == LastResultOK, now), nil
}

// BuildTelemetry constructs the daemon's Telemetry with the default samplers registered. A future
// monitoring probe is added HERE (e.g. append a latencySampler) — the heartbeat transport, wire shape,
// and controller endpoint already carry whatever it emits.
// NewTelemetryForTest builds a Telemetry from explicit samplers, for CROSS-PACKAGE tests (e.g. cmd/agent
// exercising runHeartbeat) that need a deterministic emit without the real /proc-reading samplers.
// Production always uses BuildTelemetry.
func NewTelemetryForTest(samplers ...Sampler) *Telemetry {
	return &Telemetry{samplers: samplers}
}

func BuildTelemetry(stateDir string) *Telemetry {
	return &Telemetry{samplers: []Sampler{
		conditionSampler{stateDir: stateDir},
		wireguardPeersSampler{},  // per-peer link detail → metrics["wireguard_peers"] (collapsible panel)
		&resourceSampler{},       // host CPU% + load + memory → metrics["resource"] (STATEFUL: cpu_pct is a /proc/stat delta, so the pointer's snapshot survives across beats)
		nativeXDPSampler{},       // egress NIC native-XDP capability heuristic → metrics["native_xdp"] (pre-deploy warning)
		mimicCapabilitySampler{}, // can this node build/load the mimic kernel module → metrics["mimic_capability"] (pre-deploy warning)
	}}
}
