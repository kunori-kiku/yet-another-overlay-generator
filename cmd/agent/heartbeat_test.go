package main

import (
	"io"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kunorikiku/yet-another-overlay-generator/internal/agent"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/model"
)

// fakePoster counts Telemetry POSTs (the beats that actually had something to send).
type fakePoster struct{ n int64 }

func (f *fakePoster) Telemetry(_ []model.Condition, _ map[string]any) error {
	atomic.AddInt64(&f.n, 1)
	return nil
}

// alwaysSampler always emits a metric so beat() posts (rather than skipping the empty sample).
type alwaysSampler struct{}

func (alwaysSampler) Name() string { return "test" }
func (alwaysSampler) Sample(time.Time) ([]model.Condition, map[string]any) {
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
	tel := agent.NewTelemetryForTest(alwaysSampler{})
	kick := make(chan struct{}, 1)
	done := make(chan struct{})
	defer close(done)

	go runHeartbeat(poster, tel, time.Hour, kick, done, io.Discard)

	waitForCount(t, &poster.n, 1, "startup beat")
	kick <- struct{}{}
	waitForCount(t, &poster.n, 2, "first kick beat")
	kick <- struct{}{}
	waitForCount(t, &poster.n, 3, "second kick beat")
}

// TestTryKick_NonBlocking proves the coalescing, never-block guarantee the apply loop relies on: a
// second kick against a full buffer is a no-op (not a block), and a nil channel is a safe no-op.
func TestTryKick_NonBlocking(t *testing.T) {
	ch := make(chan struct{}, 1)
	tryKick(ch) // fills the buffer
	tryKick(ch) // buffer full → coalesced no-op, MUST NOT block
	if len(ch) != 1 {
		t.Fatalf("tryKick should coalesce to 1 pending, got %d", len(ch))
	}
	tryKick(nil) // nil channel (heartbeat disabled) → no-op, no panic
}
