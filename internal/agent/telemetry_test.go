package agent

import (
	"testing"
	"time"

	"github.com/kunorikiku/yet-another-overlay-generator/internal/runtimecontract"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/telemetrymetric"
)

// fakeSampler is a test Sampler with canned output (or a panic, to prove the recover guard).
type fakeSampler struct {
	name    string
	conds   []runtimecontract.Condition
	metrics map[string]any
	panics  bool
	defs    []telemetrymetric.Definition
}

func testMetricDefinition(key string) telemetrymetric.Definition {
	return telemetrymetric.Definition{
		Key: key, History: telemetrymetric.HistoryCharted,
		ChartFamily: telemetrymetric.ChartFamilyResource, HistoryPriority: 1,
		LiveSurface: telemetrymetric.LiveSurfaceVisible,
	}
}

func (f fakeSampler) Name() string { return f.name }
func (f fakeSampler) MetricDefinitions() []telemetrymetric.Definition {
	return f.defs
}
func (f fakeSampler) Sample(now time.Time) ([]runtimecontract.Condition, map[string]any) {
	if f.panics {
		panic("sampler boom")
	}
	return f.conds, f.metrics
}

// TestTelemetryCollect_MergeRecover pins the aggregator contract (beta9-smoke-hardening plan-1):
// conditions merge by Type (a later sampler supersedes an earlier same-Type condition, stable order),
// metrics union (later keys win), and a PANICKING sampler is recovered so the others still produce
// output — a probe must never take the daemon down.
func TestTelemetryCollect_MergeRecover(t *testing.T) {
	now := time.Now().UTC()
	wgDown := runtimecontract.Condition{Type: runtimecontract.ConditionTypeWireGuard, Status: runtimecontract.ConditionStatusWarn, Reason: "LinkDown"}
	wgUp := runtimecontract.Condition{Type: runtimecontract.ConditionTypeWireGuard, Status: runtimecontract.ConditionStatusOK, Reason: "AllPeersUp"}
	cfg := runtimecontract.Condition{Type: runtimecontract.ConditionTypeConfigApply, Status: runtimecontract.ConditionStatusOK, Reason: "Applied"}

	tel := &Telemetry{samplers: []Sampler{
		fakeSampler{name: "a", conds: []runtimecontract.Condition{cfg, wgDown}, metrics: map[string]any{"x": 1, "y": 2}, defs: []telemetrymetric.Definition{testMetricDefinition("x"), testMetricDefinition("y")}},
		fakeSampler{name: "panicky", panics: true}, // recovered → contributes nothing
		fakeSampler{name: "b", conds: []runtimecontract.Condition{wgUp}, metrics: map[string]any{"y": 9, "z": 3}, defs: []telemetrymetric.Definition{testMetricDefinition("y"), testMetricDefinition("z")}},
	}}

	conds, metrics := tel.Collect(now)

	// Two distinct Types: configapply (from a) + wireguard (superseded by b's AllPeersUp). Stable order:
	// configapply first (first-seen), wireguard second (its slot, last value).
	if len(conds) != 2 {
		t.Fatalf("len(conds) = %d, want 2 (configapply + merged wireguard); got %+v", len(conds), conds)
	}
	if conds[0].Type != runtimecontract.ConditionTypeConfigApply {
		t.Fatalf("conds[0].Type = %q, want configapply (stable first-seen order)", conds[0].Type)
	}
	if conds[1].Type != runtimecontract.ConditionTypeWireGuard || conds[1].Reason != "AllPeersUp" {
		t.Fatalf("conds[1] = %+v, want wireguard/AllPeersUp (last-writer-wins by Type)", conds[1])
	}
	if metrics["x"] != 1 || metrics["y"] != 9 || metrics["z"] != 3 {
		t.Fatalf("metrics = %+v, want union with later keys winning (x:1, y:9, z:3)", metrics)
	}
}

func TestTelemetryCollect_RejectsUndeclaredAndDriftedMetricsWithoutStoppingOthers(t *testing.T) {
	goodCondition := runtimecontract.Condition{Type: runtimecontract.ConditionTypeConfigApply, Status: runtimecontract.ConditionStatusOK, Reason: "Applied"}
	badCondition := runtimecontract.Condition{Type: runtimecontract.ConditionTypeWireGuard, Status: runtimecontract.ConditionStatusWarn, Reason: "MustBeDropped"}
	tel := &Telemetry{samplers: []Sampler{
		fakeSampler{
			name: "good", conds: []runtimecontract.Condition{goodCondition},
			metrics: map[string]any{"good": 1}, defs: []telemetrymetric.Definition{testMetricDefinition("good")},
		},
		// No declaration at all: both its condition and raw metric are rejected under the guard.
		fakeSampler{name: "undeclared", conds: []runtimecontract.Condition{badCondition}, metrics: map[string]any{"raw": 2}},
		// A declaration whose key drifted from the emitted wire key is equally rejected.
		fakeSampler{
			name: "drifted", conds: []runtimecontract.Condition{badCondition},
			metrics: map[string]any{"renamed": 3}, defs: []telemetrymetric.Definition{testMetricDefinition("original")},
		},
		// An invalid live-only declaration without its required rationale is rejected before sampling.
		fakeSampler{
			name: "invalid-definition", conds: []runtimecontract.Condition{badCondition},
			metrics: map[string]any{"invalid": 4},
			defs:    []telemetrymetric.Definition{{Key: "invalid", History: telemetrymetric.HistoryLiveOnly}},
		},
	}}

	conditions, metrics := tel.Collect(time.Now().UTC())
	if len(conditions) != 1 || conditions[0].Reason != "Applied" {
		t.Fatalf("conditions = %+v, want only the valid sampler", conditions)
	}
	if len(metrics) != 1 || metrics["good"] != 1 {
		t.Fatalf("metrics = %+v, want only declared good=1", metrics)
	}
}

// TestTelemetryCollect_AllEmpty: a telemetry with samplers that yield nothing returns no conditions and
// nil metrics — the heartbeat caller uses this to SKIP a post (so a transient empty sample never clears
// the node's last-known conditions on the controller).
func TestTelemetryCollect_AllEmpty(t *testing.T) {
	tel := &Telemetry{samplers: []Sampler{fakeSampler{name: "empty"}}}
	conds, metrics := tel.Collect(time.Now().UTC())
	if len(conds) != 0 || metrics != nil {
		t.Fatalf("Collect(empty) = (%+v, %+v), want (nil, nil)", conds, metrics)
	}
}

// TestConditionSampler re-samples the node's conditions from the persisted State + a live `wg show`
// (the un-freezing fix): a state with a clean apply + a good WG dump yields configapply:Applied AND a
// FRESH wireguard:AllPeersUp (not the stale apply-time LinkDown). A node with no persisted state yet
// yields nothing.
func TestConditionSampler(t *testing.T) {
	dir := t.TempDir()
	s := conditionSampler{stateDir: dir}
	now := time.Now().UTC()

	// No state yet → nothing to report (never fabricate a configapply for a node that never applied).
	if conds, _ := s.Sample(now); conds != nil {
		t.Fatalf("Sample(no state) = %+v, want nil", conds)
	}

	// Persist a clean-apply state; point wgShowFn at an all-peers-up dump (override the hermetic default).
	if err := SaveState(dir, &State{LastResult: LastResultOK, Health: "applied"}); err != nil {
		t.Fatalf("SaveState: %v", err)
	}
	orig := wgShowFn
	t.Cleanup(func() { wgShowFn = orig })
	dump := ifaceLine("wg-a") + "\n" + peerLine("wg-a", now.Add(-10*time.Second).Unix())
	wgShowFn = func() ([]byte, error) { return []byte(dump), nil }

	conds, metrics := s.Sample(now)
	if metrics != nil {
		t.Fatalf("conditionSampler metrics = %+v, want nil", metrics)
	}
	var haveConfig, haveWGUp bool
	for _, c := range conds {
		if c.Type == runtimecontract.ConditionTypeConfigApply && c.Status == runtimecontract.ConditionStatusOK {
			haveConfig = true
		}
		if c.Type == runtimecontract.ConditionTypeWireGuard && c.Reason == reasonWGAllPeersUp {
			haveWGUp = true
		}
	}
	if !haveConfig || !haveWGUp {
		t.Fatalf("Sample(clean apply + good wg) conds = %+v, want configapply:OK + wireguard:AllPeersUp", conds)
	}
}
