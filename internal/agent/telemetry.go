package agent

import (
	"time"

	"github.com/kunorikiku/yet-another-overlay-generator/internal/model"
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
type Sampler interface {
	Name() string
	Sample(now time.Time) (conditions []model.Condition, metrics map[string]any)
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
func (t *Telemetry) Collect(now time.Time) (conditions []model.Condition, metrics map[string]any) {
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
func sampleGuarded(s Sampler, now time.Time) (conds []model.Condition, metrics map[string]any) {
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

func (s conditionSampler) Sample(now time.Time) ([]model.Condition, map[string]any) {
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
func BuildTelemetry(stateDir string) *Telemetry {
	return &Telemetry{samplers: []Sampler{
		conditionSampler{stateDir: stateDir},
		wireguardPeersSampler{}, // per-peer link detail → metrics["wireguard_peers"] (collapsible panel)
		resourceSampler{},       // host load + memory → metrics["resource"] (node detail readout)
	}}
}
