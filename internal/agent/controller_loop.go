package agent

// controller_loop.go is the controller-mode daemon, extracted from cmd/agent's runControllerMode
// (plan-7 daemon decompose) so the loop SEQUENCING is unit-testable in-process. cmd/agent's
// runControllerMode is now a thin wrapper: it does the crash-prone setup (self-update reconcile, token
// load, client build, pubkey/cred reads, promote), builds the injectable seams from the parsed flags +
// client + pinned key, then constructs a ControllerLoop and runs it. The BEHAVIOR is byte-identical to
// the pre-decompose daemon; the seams below (the apply cycle, the self-update finalize/retry, the
// heartbeat kick, and an injectable clock) exist so a test drives the exact apply->kick->reconcile
// ordering, the deferred-retry timing, and the single-shot single-cycle invariant deterministically —
// no controller, no real clock, no sleeps.

import (
	"fmt"
	"io"
	"os"
	"time"
)

// ControllerLoop drives the controller-mode daemon (and its single-shot sibling): the poll->apply->
// report cycle, the post-apply live-telemetry kick, the boot-time self-update finalize, and the
// deferred-self-update retry. Every EXTERNAL effect is an injected seam so the sequencing is testable;
// production wires each seam to its real function in cmd/agent's runControllerMode.
//
// The BuildVersion the daemon reports/self-updates against is NOT held here — it is captured by the
// caller (cmd/agent's main.BuildVersion, injected via -ldflags) into the Cycle / Finalize / RetryDeferred
// closures the wrapper builds. This type never imports the release-injected var, so the ldflags seam
// stays in cmd/agent.
type ControllerLoop struct {
	// Cycle runs ONE poll->apply->report iteration FROM the given watermark (`after`) and returns the
	// generation to resume from, whether a new generation was applied, and any error. The watermark is
	// passed explicitly (rather than captured) so the loop owns it in loopState; production wires Cycle to
	// a closure over RunControllerCycle(client, CycleConfig{After: after, ...}), a test injects canned
	// (resumeGen, applied, err) tuples. REQUIRED (a nil Cycle is a wiring bug).
	Cycle func(after int64) (resumeGen int64, applied bool, err error)
	// Finalize promotes a probationary self-update (FinalizeSelfUpdate) after the FIRST completed cycle.
	// The single-shot path calls it exactly once; the daemon path calls it once, right after the first
	// cycle. Production wires it to a closure over agent.FinalizeSelfUpdate(stateDir, version, stderr).
	Finalize func()
	// Kick nudges the live heartbeat after an APPLY so the panel reflects the just-applied state within a
	// round-trip (gated on `applied` so an idle/rekey wake never inflates the beat rate). Nil = heartbeat
	// disabled (a safe no-op, mirroring the old tryKick(nil)). RunForever sets it to TryKick(kickCh) for
	// the heartbeat it spawns; a test injects a counter.
	Kick func()
	// RetryDeferred re-attempts a DEFERRED self-update on an idle cycle (see RetryDeferredSelfUpdate),
	// paced by RetryInterval. Production wires it to a closure over RetryDeferredSelfUpdate(...); the
	// returned `attempted` is for logging only. Nil disables the retry call.
	RetryDeferred func() (attempted bool, err error)

	// After is the initial resume watermark (the --after cursor). A daemon advances it per cycle; the
	// single-shot path compares the returned resumeGen against it to detect a timed-out long-poll.
	After int64

	// ErrBackoff paces an error / idle-or-rekey-wake re-poll (production: 5s).
	ErrBackoff time.Duration
	// RetryInterval paces the deferred-self-update retry; <=0 disables it.
	RetryInterval time.Duration

	// Poster/Telemetry/TelemetryInterval configure the daemon's LIVE health heartbeat, spawned by
	// RunForever. Poster is the controller client (satisfies TelemetryPoster via Telemetry); Telemetry is
	// BuildTelemetry(stateDir). TelemetryInterval <=0 disables the heartbeat. Unused by the single-shot
	// path (RunOnce) and by the sequencing tests (which drive Kick directly).
	Poster            TelemetryPoster
	Telemetry         *Telemetry
	TelemetryInterval time.Duration

	// NodeID labels the daemon startup log line only.
	NodeID string
	// Stderr receives the loop's log output (default os.Stderr).
	Stderr io.Writer

	// Now/Sleep are the injectable clock, defaulting to time.Now / time.Sleep. A test overrides them so
	// the deferred-retry interval gate and the backoff pause are deterministic (no real sleeping).
	Now   func() time.Time
	Sleep func(time.Duration)
}

// loopState is the daemon loop's mutable state carried across step iterations. It is separate from
// ControllerLoop so step is a pure function of (config seams, mutable state) — a test can construct a
// fresh state and drive step() directly, asserting on the seam-call counts after each iteration.
type loopState struct {
	// lastAppliedGen is the resume watermark: advanced on a successful apply, an idle skip, or a rekey
	// wake; unchanged on a timed-out poll or a failed cycle (keep-last-good).
	lastAppliedGen int64
	// finalized latches the once-only self-update finalize after the first completed cycle.
	finalized bool
	// lastSelfUpdateRetry paces the deferred-self-update retry; initialized to loop-start so the first
	// retry waits one interval (the apply path already made the initial attempt).
	lastSelfUpdateRetry time.Time
}

// RunOnce runs a SINGLE poll->apply->report cycle (the deterministic unit the daemon loops over) and
// returns the process exit code. It finalizes a probationary self-update unconditionally (the cycle
// returning proves this — possibly just-swapped — binary can run a full cycle), then reports an error
// (exit 1) or, on a timed-out long-poll (nothing applied and the watermark did not move), logs
// "nothing to do" (exit 0). This is cmd/agent's non-daemon `run --controller` path.
func (l *ControllerLoop) RunOnce() int {
	resumeGen, applied, err := l.Cycle(l.After)
	// FINALIZE a probationary self-update: the cycle returned, proving this (possibly just-swapped)
	// binary can actually RUN a full cycle, not merely pass `version`+verify. No-op unless a Confirmed
	// breadcrumb for this build exists.
	if l.Finalize != nil {
		l.Finalize()
	}
	if err != nil {
		fmt.Fprintf(l.stderr(), "agent: %v\n", err)
		return 1
	}
	if !applied && resumeGen == l.After {
		// A timed-out long-poll (no advance). A rekey wake advances resumeGen and is logged by
		// RunControllerCycle, so do not print "nothing to do" over a rotation.
		fmt.Fprintf(l.stderr(), "agent: no new generation (still at %d); nothing to do\n", resumeGen)
	}
	return 0
}

// RunForever runs the continuous daemon: it spawns the live health heartbeat (when enabled) and then
// loops step() forever. It NEVER returns — production has no graceful cancel; a self-update swap is a
// syscall.Exec inside Cycle that destroys this process image (and the heartbeat goroutine with it), and
// any other exit is a crash the systemd Restart=always relaunches. The heartbeat's `done` channel is
// nil here (test-only clean shutdown), exactly as before the decompose.
func (l *ControllerLoop) RunForever() {
	st := l.newState()
	fmt.Fprintf(l.stderr(), "agent: controller daemon started (node %s, resume @%d)\n", l.NodeID, st.lastAppliedGen)

	// LIVE health heartbeat (beta9-smoke-hardening plan-1): a DEDICATED goroutine re-samples the node's
	// conditions on an interval and POSTs them to /telemetry, so the panel reflects CURRENT health
	// instead of the frozen apply-time snapshot. It is decoupled from the poll loop and needs no lock.
	// No context/cancel is needed: this loop never returns, and a self-update swap is syscall.Exec
	// (which replaces the whole process image and destroys this goroutine with it).
	if l.TelemetryInterval > 0 {
		kick := make(chan struct{}, 1) // buffered+coalescing: the apply loop kicks non-blocking
		go RunHeartbeat(l.Poster, l.Telemetry, l.TelemetryInterval, kick, nil, l.stderr())
		l.Kick = func() { TryKick(kick) }
	}

	for {
		l.step(st)
	}
}

// step runs ONE iteration of the daemon loop over the mutable loopState. It is the extracted loop body:
// cycle -> finalize-once -> (error: log+backoff, done) -> idle/rekey pace -> advance watermark -> kick
// on apply -> deferred-retry when idle past the interval. A test calls step directly (with fake seams
// and a controllable clock) to pin the apply->kick->reconcile ordering without running RunForever.
func (l *ControllerLoop) step(st *loopState) {
	resumeGen, applied, err := l.Cycle(st.lastAppliedGen)
	// FINALIZE a probationary self-update after the FIRST completed cycle (once): the cycle returning
	// proves this (possibly just-swapped) binary RUNS its daemon loop, not merely that `version`+verify
	// pass. No-op without a Confirmed breadcrumb for this build.
	if !st.finalized {
		if l.Finalize != nil {
			l.Finalize()
		}
		st.finalized = true
	}
	if err != nil {
		fmt.Fprintf(l.stderr(), "agent: %v (keeping last-good; retrying in %s)\n", err, l.ErrBackoff)
		l.sleep(l.ErrBackoff)
		return
	}
	if !applied && resumeGen > st.lastAppliedGen {
		l.sleep(l.ErrBackoff) // idle/rekey wake: pace before re-polling
	}
	st.lastAppliedGen = resumeGen // advance on success, idle skip, or rekey wake; unchanged on a timed-out poll

	// Kick the heartbeat after an actual APPLY so the panel reflects the just-applied state within a
	// round-trip and history/metrics gain a sample at the deploy instant. Gated on `applied` so an idle
	// poll-timeout or a rekey/idle wake does NOT inflate the beat rate. Kick is non-blocking + coalescing
	// (TryKick), so the loop never stalls.
	if applied && l.Kick != nil {
		l.Kick()
	}

	// Deferred self-update retry (plan-8): re-attempt a self-update a prior cycle deferred WITHOUT
	// waiting for a new generation. Idle cycles only (an apply already ran the post-apply attempt), paced
	// by the interval, on the MAIN thread so a swap (syscall.Exec) never interrupts a mid-flight apply.
	if l.RetryInterval > 0 && !applied &&
		l.now().Sub(st.lastSelfUpdateRetry) >= l.RetryInterval {
		st.lastSelfUpdateRetry = l.now()
		if l.RetryDeferred != nil {
			if _, suErr := l.RetryDeferred(); suErr != nil {
				fmt.Fprintf(l.stderr(), "agent: deferred self-update retry: %v (will retry)\n", suErr)
			}
		}
	}
}

// newState builds the loop's initial mutable state: the watermark at After and the deferred-retry clock
// at loop-start (so the first retry waits one full interval).
func (l *ControllerLoop) newState() *loopState {
	return &loopState{
		lastAppliedGen:      l.After,
		lastSelfUpdateRetry: l.now(),
	}
}

// now returns the injected clock or the real time.Now.
func (l *ControllerLoop) now() time.Time {
	if l.Now != nil {
		return l.Now()
	}
	return time.Now()
}

// sleep pauses via the injected sleeper or the real time.Sleep.
func (l *ControllerLoop) sleep(d time.Duration) {
	if l.Sleep != nil {
		l.Sleep(d)
		return
	}
	time.Sleep(d)
}

// stderr returns the configured log writer or os.Stderr.
func (l *ControllerLoop) stderr() io.Writer {
	if l.Stderr != nil {
		return l.Stderr
	}
	return os.Stderr
}
