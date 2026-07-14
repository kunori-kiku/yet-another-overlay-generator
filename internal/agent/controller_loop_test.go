package agent

import (
	"errors"
	"io"
	"testing"
	"time"
)

// controller_loop_test.go pins the LOOP SEQUENCING invariants the pre-decompose god-runControllerMode
// made untestable (plan-7). It deliberately does NOT re-test heartbeat coalescing (that lives in
// heartbeat_test.go's TestRunHeartbeat_Kick / TestTryKick_NonBlocking); it drives the injectable seams
// (a fake cycle/finalize/kick/retry + a controllable clock) so the apply->kick->reconcile ordering, the
// single-shot single-cycle rule, and the deferred-retry timing are deterministic — no controller, no
// real clock, no sleeps.

// fixedClock returns a Now func reading *t and a Sleep func that swallows the pause (so the idle-wake and
// error backoffs never actually sleep in a test).
func fixedClock(t *time.Time) (func() time.Time, func(time.Duration)) {
	return func() time.Time { return *t }, func(time.Duration) {}
}

// TestControllerLoop_RunOnce pins the single-shot invariant: EXACTLY ONE cycle and EXACTLY ONE finalize
// per invocation (the finalize firing even when the cycle errors, since it runs before the error check),
// the watermark passed to the cycle is --after, and the exit code maps outcome->code.
func TestControllerLoop_RunOnce(t *testing.T) {
	tests := []struct {
		name     string
		resume   int64
		applied  bool
		cerr     error
		wantCode int
	}{
		{"applied advances", 5, true, nil, 0},
		{"idle no advance -> nothing to do", 3, false, nil, 0},
		{"cycle error -> exit 1", 3, false, errors.New("boom"), 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var cycles, finalizes int
			loop := &ControllerLoop{
				Cycle: func(after int64) (int64, bool, error) {
					cycles++
					if after != 3 {
						t.Fatalf("RunOnce passed after=%d to the cycle, want the --after cursor 3", after)
					}
					return tt.resume, tt.applied, tt.cerr
				},
				Finalize: func() { finalizes++ },
				After:    3,
				Stderr:   io.Discard,
			}
			if code := loop.RunOnce(); code != tt.wantCode {
				t.Fatalf("RunOnce code=%d, want %d", code, tt.wantCode)
			}
			if cycles != 1 {
				t.Fatalf("RunOnce ran %d cycles, want EXACTLY 1", cycles)
			}
			if finalizes != 1 {
				t.Fatalf("RunOnce finalized %d times, want EXACTLY 1 (even on cycle error)", finalizes)
			}
		})
	}
}

// TestControllerLoop_Step_KickGatedOnApplied pins that a daemon step nudges the heartbeat EXACTLY ONCE
// on an APPLY and NOT AT ALL on an idle/rekey wake or a timed-out long-poll — the plan-1.5 gate that
// keeps an idle wake from inflating the beat rate.
func TestControllerLoop_Step_KickGatedOnApplied(t *testing.T) {
	tests := []struct {
		name      string
		resume    int64 // what the cycle returns as the resume watermark
		applied   bool
		wantKicks int
	}{
		{"apply kicks exactly once", 6, true, 1},
		{"idle/rekey wake (watermark advances) does NOT kick", 9, false, 0},
		{"timed-out poll (no advance) does NOT kick", 5, false, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			now := time.Unix(0, 0)
			nowFn, sleepFn := fixedClock(&now)
			var kicks int
			loop := &ControllerLoop{
				Cycle:    func(int64) (int64, bool, error) { return tt.resume, tt.applied, nil },
				Finalize: func() {},
				Kick:     func() { kicks++ },
				After:    5,
				Now:      nowFn,
				Sleep:    sleepFn,
				Stderr:   io.Discard,
			}
			loop.step(loop.newState())
			if kicks != tt.wantKicks {
				t.Fatalf("step kicks=%d, want %d", kicks, tt.wantKicks)
			}
		})
	}
}

// TestControllerLoop_Step_NilKickNoPanic proves the heartbeat-disabled path: an APPLY with a nil Kick
// seam (telemetry off) is a safe no-op, mirroring the old tryKick(nil).
func TestControllerLoop_Step_NilKickNoPanic(t *testing.T) {
	now := time.Unix(0, 0)
	nowFn, sleepFn := fixedClock(&now)
	loop := &ControllerLoop{
		Cycle:    func(after int64) (int64, bool, error) { return after + 1, true, nil }, // applied
		Finalize: func() {},
		Kick:     nil, // heartbeat disabled
		After:    0,
		Now:      nowFn,
		Sleep:    sleepFn,
		Stderr:   io.Discard,
	}
	loop.step(loop.newState()) // must not panic on applied + nil Kick
}

// TestControllerLoop_Step_FinalizeOnce pins that the daemon finalizes a probationary self-update after
// the FIRST completed step only — never again on subsequent steps.
func TestControllerLoop_Step_FinalizeOnce(t *testing.T) {
	now := time.Unix(0, 0)
	nowFn, sleepFn := fixedClock(&now)
	var finalizes int
	loop := &ControllerLoop{
		Cycle:    func(after int64) (int64, bool, error) { return after, false, nil },
		Finalize: func() { finalizes++ },
		After:    0,
		Now:      nowFn,
		Sleep:    sleepFn,
		Stderr:   io.Discard,
	}
	st := loop.newState()
	loop.step(st)
	loop.step(st)
	loop.step(st)
	if finalizes != 1 {
		t.Fatalf("daemon finalized %d times across 3 steps, want EXACTLY 1", finalizes)
	}
}

// TestControllerLoop_Step_DeferredRetryTiming pins the plan-8 deferred-self-update retry pacing: it
// fires only on an idle cycle PAST the interval, resets its clock when it fires, and NEVER runs during
// an apply cycle (the apply path already made its own post-apply attempt) — even long past the interval.
func TestControllerLoop_Step_DeferredRetryTiming(t *testing.T) {
	now := time.Unix(1000, 0)
	nowFn, sleepFn := fixedClock(&now)
	applied := false
	var retries int
	loop := &ControllerLoop{
		Cycle:         func(after int64) (int64, bool, error) { return after, applied, nil }, // idle: no advance
		Finalize:      func() {},
		RetryDeferred: func() (bool, error) { retries++; return true, nil },
		After:         0,
		RetryInterval: time.Minute,
		Now:           nowFn,
		Sleep:         sleepFn,
		Stderr:        io.Discard,
	}
	st := loop.newState() // lastSelfUpdateRetry = T0 = 1000

	// Idle, interval not yet elapsed -> no retry.
	loop.step(st)
	if retries != 0 {
		t.Fatalf("retry fired at T+0: retries=%d, want 0", retries)
	}

	// Idle, just under the interval -> still no retry.
	now = now.Add(59 * time.Second)
	loop.step(st)
	if retries != 0 {
		t.Fatalf("retry fired under the interval (T+59s): retries=%d, want 0", retries)
	}

	// Idle, past the interval -> retry fires exactly once.
	now = now.Add(2 * time.Second) // T+61s
	loop.step(st)
	if retries != 1 {
		t.Fatalf("retry did not fire past the interval (T+61s): retries=%d, want 1", retries)
	}

	// The retry clock reset on that fire: another idle step at the same instant does NOT retry again.
	loop.step(st)
	if retries != 1 {
		t.Fatalf("retry re-fired immediately after resetting its clock: retries=%d, want 1", retries)
	}

	// Far past the interval, an APPLY cycle must NOT run the deferred retry (gated on !applied): the
	// apply path owns its own post-apply attempt, and a swap must never interrupt a mid-flight apply.
	now = now.Add(10 * time.Minute)
	applied = true
	loop.Cycle = func(after int64) (int64, bool, error) { return after + 1, true, nil } // a real apply (advances)
	loop.step(st)
	if retries != 1 {
		t.Fatalf("deferred retry ran during an apply cycle: retries=%d, want 1", retries)
	}
}
