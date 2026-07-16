package agent

import (
	"fmt"
	"time"

	"github.com/kunorikiku/yet-another-overlay-generator/internal/runtimecontract"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/telemetrymetric"
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
// This is a FRAMEWORK, not a one-off: a Sampler is one monitored signal. Conditions, resources,
// WireGuard detail, and signed active probes already share it; a future typed signal implements
// Sampler, is registered in BuildTelemetry, and rides the same heartbeat with no transport fork.

// Sampler produces one monitoring signal: a set of conditions and/or a map of named metrics. Sample is
// called once per heartbeat tick; it must be cheap and self-contained (it runs best-effort under a
// recover guard, so a panic in one probe never crashes the daemon). Either return may be nil.
//
// FRESHNESS CONTRACT (plan-1.5): a Sampler normally re-measures the LIVE system on each call and must
// never cache a value captured at apply/deploy time and re-emit it unchanged. A sampler with its own
// explicit bounded cadence may expose its most recent result between due attempts (activeProbeSampler
// does this so a 60-second signed probe is not repeated on every heartbeat), but it must advance that
// schedule and re-measure when due; it cannot turn a deploy-time result into permanent truth. A
// sampler that reads a deploy-time artifact (a breadcrumb file, a persisted apply outcome) MUST
// reconcile it against live state (see readMimicCondition's `systemctl is-active` reconcile) or it
// re-emits a FROZEN value forever — which reads as "works at deploy, then goes stale," the exact
// recurring defect this framework exists to prevent. Registering a signal HERE (a Sampler in
// BuildTelemetry) is now the SOLE wiring needed: the daemon beats every interval AND on a post-apply
// kick (cmd/agent runHeartbeat), so a registered signal fires at deploy AND live by construction —
// there is no second apply-path list to keep in parity. telemetry_liveness_test.go asserts the
// freshness contract for the injectable samplers (mutate the input → the output must change).
type Sampler interface {
	Name() string
	// MetricDefinitions declares every top-level metrics key this sampler may emit and whether that
	// signal is retained for charts or intentionally live-only. Even condition-only samplers declare
	// an explicit empty set. BuildTelemetry validates the complete production registry against the
	// shared catalog, so adding a metric cannot silently stop at the latest-value overlay.
	MetricDefinitions() []telemetrymetric.Definition
	Sample(now time.Time) (conditions []runtimecontract.Condition, metrics map[string]any)
}

// Telemetry aggregates the registered Samplers into a single heartbeat payload. It is constructed once
// per daemon (BuildTelemetry) and Collect-ed each tick from the single heartbeat goroutine, so it
// needs no internal locking.
type Telemetry struct {
	samplers []Sampler
}

// probeCompletionKicker is implemented by cadence-owning samplers whose bounded result window may
// need an early heartbeat flush. It remains an internal framework seam: ordinary samplers do not need
// to know about delivery negotiation, and the heartbeat installs a capability-gated callback only for
// the reliable protocol path.
type probeCompletionKicker interface {
	setProbeCompletionKick(func())
}

func (t *Telemetry) setProbeCompletionKick(kick func()) {
	if t == nil {
		return
	}
	for _, sampler := range t.samplers {
		if kicker, ok := sampler.(probeCompletionKicker); ok {
			kicker.setProbeCompletionKick(kick)
		}
	}
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
	declared := make(map[string]struct{})
	for _, definition := range s.MetricDefinitions() {
		if err := validateMetricDefinition(definition); err != nil {
			panic(err)
		}
		if _, duplicate := declared[definition.Key]; duplicate {
			panic("duplicate sampler metric declaration: " + definition.Key)
		}
		declared[definition.Key] = struct{}{}
	}
	conds, metrics = s.Sample(now)
	for key := range metrics {
		if _, ok := declared[key]; !ok {
			panic("sampler emitted undeclared metric: " + key)
		}
	}
	return conds, metrics
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

func (conditionSampler) MetricDefinitions() []telemetrymetric.Definition { return nil }

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
	samplers := []Sampler{
		conditionSampler{stateDir: stateDir},
		agentCapabilitiesSampler{},      // exact executable feature tokens → metrics["agent_capabilities"]
		newActiveProbeSampler(stateDir), // signed last-known-good policy; asynchronous and bounded
		wireguardPeersSampler{},         // per-peer link detail → metrics["wireguard_peers"] (collapsible panel)
		&resourceSampler{},              // host CPU% + load + memory → metrics["resource"] (STATEFUL: cpu_pct is a /proc/stat delta, so the pointer's snapshot survives across beats)
		nativeXDPSampler{},              // egress NIC native-XDP capability heuristic → metrics["native_xdp"] (pre-deploy warning)
		mimicCapabilitySampler{},        // can this node build/load the mimic kernel module → metrics["mimic_capability"] (pre-deploy warning)
	}
	if err := validateProductionMetricDefinitions(samplers); err != nil {
		// This is a static programmer invariant over BuildTelemetry plus telemetrymetric.All, not an
		// operator-controlled runtime condition. Failing at construction is safer than shipping a new
		// metric whose history disposition was never implemented or reviewed.
		panic("agent: invalid production telemetry metric declarations: " + err.Error())
	}
	return &Telemetry{samplers: samplers}
}

// validateProductionMetricDefinitions proves that the production sampler registry declares the
// shared metric catalog exactly once and without locally changing a definition's history semantics.
// It stays a pure seam so tests can exercise malformed registries without constructing live samplers.
func validateProductionMetricDefinitions(samplers []Sampler) error {
	definitions := telemetrymetric.All()
	if err := telemetrymetric.ValidateCatalog(definitions); err != nil {
		return fmt.Errorf("catalog metric: %w", err)
	}
	catalog := make(map[string]telemetrymetric.Definition, len(definitions))
	for _, definition := range definitions {
		catalog[definition.Key] = definition
	}

	declared := make(map[string]string)
	for _, sampler := range samplers {
		if sampler == nil {
			return fmt.Errorf("nil sampler")
		}
		for _, definition := range sampler.MetricDefinitions() {
			if err := validateMetricDefinition(definition); err != nil {
				return fmt.Errorf("sampler %q: %w", sampler.Name(), err)
			}
			authoritative, ok := catalog[definition.Key]
			if !ok {
				return fmt.Errorf("sampler %q declares unknown metric %q", sampler.Name(), definition.Key)
			}
			if definition != authoritative {
				return fmt.Errorf("sampler %q changes catalog definition for metric %q", sampler.Name(), definition.Key)
			}
			if owner, duplicate := declared[definition.Key]; duplicate {
				return fmt.Errorf("metric %q is declared by both %q and %q", definition.Key, owner, sampler.Name())
			}
			declared[definition.Key] = sampler.Name()
		}
	}
	for key := range catalog {
		if _, ok := declared[key]; !ok {
			return fmt.Errorf("catalog metric %q has no production sampler", key)
		}
	}
	return nil
}

func validateMetricDefinition(definition telemetrymetric.Definition) error {
	return telemetrymetric.ValidateDefinition(definition)
}
