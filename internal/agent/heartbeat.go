package agent

// heartbeat.go is the daemon's LIVE health heartbeat, moved verbatim from cmd/agent (plan-7 daemon
// decompose). It is the telemetry side of the controller-mode daemon (ControllerLoop): a dedicated
// goroutine that re-samples the node's conditions/metrics on an interval and on a post-apply kick, then
// POSTs them to /telemetry via a TelemetryPoster. It carries no generation/checksum — observability is
// kept strictly separate from deploy custody. The behavior is unchanged from the pre-decompose
// cmd/agent runHeartbeat/tryKick; only the home package + the exported names changed.

import (
	"fmt"
	"io"
	"time"

	"github.com/kunorikiku/yet-another-overlay-generator/internal/runtimecontract"
)

// TelemetryPoster is the subset of *ControllerClient that RunHeartbeat needs — indirected so a
// test injects a counting fake (no controller) and drives the kick/tick loop directly. *ControllerClient
// satisfies it via its Telemetry method.
type TelemetryPoster interface {
	Telemetry(conditions []runtimecontract.Condition, metrics map[string]any) error
}

// TryKick delivers a non-blocking, COALESCING nudge to the heartbeat: if a kick is already pending
// (the buffered-size-1 channel is full) it is a no-op, so a busy apply loop never blocks on a slow
// beat, and a burst of cycles collapses to at most one extra beat. Nil channel (heartbeat disabled) is
// a safe no-op.
func TryKick(ch chan<- struct{}) {
	if ch == nil {
		return
	}
	select {
	case ch <- struct{}{}:
	default:
	}
}

// RunHeartbeat is the daemon's LIVE health heartbeat loop (beta9-smoke-hardening plan-1). It samples
// the registered telemetry probes and POSTs the result to /telemetry every `interval`, once
// immediately at start, AND on every `kick` (plan-1.5: the apply loop kicks after each completed cycle
// so a just-applied state + fresh metrics surface within a round-trip instead of after a full interval,
// and so history/cpu_pct gain a sample at the deploy instant). The kick makes the deploy emission a
// REAL sampler beat — so a signal authored as a Sampler fires at deploy AND on the interval by
// construction, ending the "new telemetry only fires at deploy, then goes stale" recurrence at its
// framework root (see the subject's framework-finding doc). `done` is test-only clean shutdown;
// production passes nil so the loop runs until the process exits/exec's.
// Best-effort: a transport error is logged and swallowed (never disturbs the running overlay / poll
// loop). It skips a post when there is nothing to report — a never-applied node, or a transient
// State-read failure — so a momentary empty sample never WIPES the node's last-known conditions
// (the controller replaces the set wholesale; an applied node always yields at least the configapply
// condition, so this only ever skips genuinely-empty samples). Runs until the process exits/exec's.
func RunHeartbeat(poster TelemetryPoster, tel *Telemetry, interval time.Duration, kick, done <-chan struct{}, stderr io.Writer) {
	if reliable, ok := poster.(ReliableTelemetryPoster); ok {
		runReliableHeartbeat(reliable, tel, interval, kick, done, stderr)
		return
	}
	beat := func() {
		// A panic anywhere in a beat (a sampler outside its own guard, the merge, or the POST) must
		// NOT kill this goroutine — it is the ONLY thing that refreshes Last Seen after apply time, so
		// a silent death would freeze the controller's view of a live node. Recover, log, and let the
		// next tick try again (beta.16 heartbeat-resilience hardening).
		defer func() {
			if r := recover(); r != nil {
				fmt.Fprintf(stderr, "agent: telemetry heartbeat: recovered from panic: %v\n", r)
			}
		}()
		conds, metrics := tel.Collect(time.Now().UTC())
		// A poster without protocol-v2 receipts has no way to negotiate the additive recent-attempt
		// contract. Preserve the rc.9/legacy JSON shape even for wrappers and alternate posters that do
		// not expose ReliableTelemetryPoster; latest probe_results remains available as before.
		metrics = withoutProbeSamples(metrics)
		if len(conds) == 0 && len(metrics) == 0 {
			return
		}
		if err := poster.Telemetry(conds, metrics); err != nil {
			fmt.Fprintf(stderr, "agent: telemetry heartbeat: %v\n", err)
		}
	}
	beat()
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-done:
			// Test-only clean shutdown; production passes a nil done (this case never fires) so the
			// loop runs until the process exits/exec's, exactly as before.
			return
		case <-t.C:
			beat()
		case <-kick:
			// A post-apply kick (or any external nudge): beat NOW so the just-applied state and its
			// fresh metrics reach the controller within a round-trip instead of after up to a full
			// interval — and history/cpu_pct gain a sample at the deploy instant. The SAME beat() on the
			// SAME single goroutine over the SAME Telemetry instance, so resourceSampler's cpu delta and
			// the "no locking needed" invariant stay intact.
			beat()
		}
	}
}
