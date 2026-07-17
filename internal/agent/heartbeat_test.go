package agent

import (
	"io"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kunorikiku/yet-another-overlay-generator/internal/probemetric"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/runtimecontract"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/telemetrymetric"
)

// fakePoster counts Telemetry POSTs (the beats that actually had something to send).
type fakePoster struct{ n int64 }

func (f *fakePoster) Telemetry(_ []runtimecontract.Condition, _ map[string]any) error {
	atomic.AddInt64(&f.n, 1)
	return nil
}

type cadenceCompletionSampler struct {
	mu   sync.Mutex
	kick func()
}

func (*cadenceCompletionSampler) Name() string { return "cadence-completion" }
func (*cadenceCompletionSampler) MetricDefinitions() []telemetrymetric.Definition {
	return []telemetrymetric.Definition{testMetricDefinition("test")}
}
func (*cadenceCompletionSampler) Sample(time.Time) ([]runtimecontract.Condition, map[string]any) {
	return nil, map[string]any{"test": 1}
}
func (s *cadenceCompletionSampler) setTelemetryCompletionKick(kick func()) {
	s.mu.Lock()
	s.kick = kick
	s.mu.Unlock()
}
func (s *cadenceCompletionSampler) complete() {
	s.mu.Lock()
	kick := s.kick
	s.mu.Unlock()
	if kick != nil {
		kick()
	}
}

// alwaysSampler always emits a metric so beat() posts (rather than skipping the empty sample).
type alwaysSampler struct{}

func (alwaysSampler) Name() string { return "test" }
func (alwaysSampler) MetricDefinitions() []telemetrymetric.Definition {
	return []telemetrymetric.Definition{testMetricDefinition("test")}
}
func (alwaysSampler) Sample(time.Time) ([]runtimecontract.Condition, map[string]any) {
	return nil, map[string]any{"test": 1}
}

func waitForCount(t *testing.T, n *int64, want int64, what string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if atomic.LoadInt64(n) >= want {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s (got %d, want >= %d)", what, atomic.LoadInt64(n), want)
}

// TestRunHeartbeat_Kick proves the plan-1.5 unification: a post-apply kick triggers an EXTRA live
// sampler beat beyond the startup beat, with the ticker set long enough that it never fires — so the
// only beats are the startup one and the kick-driven ones.
func TestRunHeartbeat_Kick(t *testing.T) {
	poster := &fakePoster{}
	tel := NewTelemetryForTest(alwaysSampler{})
	kick := make(chan struct{}, 1)
	done := make(chan struct{})
	defer close(done)

	go RunHeartbeat(poster, tel, time.Hour, kick, done, io.Discard)

	waitForCount(t, &poster.n, 1, "startup beat")
	kick <- struct{}{}
	waitForCount(t, &poster.n, 2, "first kick beat")
	kick <- struct{}{}
	waitForCount(t, &poster.n, 3, "second kick beat")
}

func TestRunHeartbeat_CadenceCompletionBeatsSlowerUploadInterval(t *testing.T) {
	poster := &fakePoster{}
	sampler := &cadenceCompletionSampler{}
	done := make(chan struct{})
	defer close(done)
	go RunHeartbeat(poster, NewTelemetryForTest(sampler), time.Hour, nil, done, io.Discard)

	waitForCount(t, &poster.n, 1, "startup beat")
	sampler.complete()
	waitForCount(t, &poster.n, 2, "independent cadence completion beat")
}

// TestTryKick_NonBlocking proves the coalescing, never-block guarantee the apply loop relies on: a
// second kick against a full buffer is a no-op (not a block), and a nil channel is a safe no-op.
func TestTryKick_NonBlocking(t *testing.T) {
	ch := make(chan struct{}, 1)
	TryKick(ch) // fills the buffer
	TryKick(ch) // buffer full → coalesced no-op, MUST NOT block
	if len(ch) != 1 {
		t.Fatalf("TryKick should coalesce to 1 pending, got %d", len(ch))
	}
	TryKick(nil) // nil channel (heartbeat disabled) → no-op, no panic
}

type legacyProbePoster struct {
	posted chan map[string]any
}

func (p *legacyProbePoster) Telemetry(_ []runtimecontract.Condition, metrics map[string]any) error {
	p.posted <- metrics
	return nil
}

type legacyProbeSampler struct{}

func (legacyProbeSampler) Name() string { return "legacy-probe" }

func (legacyProbeSampler) MetricDefinitions() []telemetrymetric.Definition {
	return []telemetrymetric.Definition{telemetrymetric.ProbeResults, telemetrymetric.ProbeSamples}
}

func (legacyProbeSampler) Sample(time.Time) ([]runtimecontract.Condition, map[string]any) {
	latest := []probemetric.Result{{ID: "dns", Type: "icmp", Host: "resolver.example", Status: probemetric.StatusPending}}
	completed := []probemetric.Result{{
		ID: "dns", Type: "icmp", Host: "resolver.example", Status: probemetric.StatusFailure,
		CheckedAt:     time.Date(2026, time.July, 16, 10, 0, 0, 0, time.UTC).Format(time.RFC3339Nano),
		FailureReason: probemetric.FailureTimeout, IntervalMS: 30_000,
	}}
	return nil, map[string]any{
		probemetric.LatestMetricKey:  latest,
		probemetric.SamplesMetricKey: completed,
	}
}

func TestRunHeartbeat_LegacyPosterNeverSendsProbeSamples(t *testing.T) {
	poster := &legacyProbePoster{posted: make(chan map[string]any, 1)}
	done := make(chan struct{})
	stopped := make(chan struct{})
	go func() {
		RunHeartbeat(poster, NewTelemetryForTest(legacyProbeSampler{}), time.Hour, nil, done, io.Discard)
		close(stopped)
	}()

	select {
	case metrics := <-poster.posted:
		if _, ok := metrics[probemetric.LatestMetricKey]; !ok {
			t.Fatalf("legacy heartbeat lost backward-compatible probe_results: %+v", metrics)
		}
		if _, ok := metrics[probemetric.SamplesMetricKey]; ok {
			t.Fatalf("legacy heartbeat sent unnegotiated probe_samples: %+v", metrics)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("legacy heartbeat was not posted")
	}
	close(done)
	select {
	case <-stopped:
	case <-time.After(3 * time.Second):
		t.Fatal("legacy heartbeat did not stop")
	}
}
