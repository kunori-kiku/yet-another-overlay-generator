package agent

import (
	"io"
)

// selfupdate_retry.go makes a DEFERRED self-update recover on its own (plan-8). The apply path
// attempts a post-apply self-update once (agent.go step 7); the daemon only re-runs that path on a
// NEW generation, so a download failure on a stable generation used to wedge the rollout until a
// manual `systemctl restart yaog-agent`. RetryDeferredSelfUpdate re-attempts it on the daemon's idle
// cycles (on a backoff), reusing the SAME verified-fetch → decide → perform path the apply uses — no
// persisted or unverified pins, so the signed-self-update custody model is unchanged.

// RetryDeferredSelfUpdate re-attempts a post-apply self-update that a prior cycle deferred, WITHOUT
// requiring a new generation. It is a no-op unless State.SelfUpdateBlocked is set (an armed deferral)
// and no swap is already in flight. verifiedFetch returns the cryptographically VERIFIED bundle file
// map (client.Fetch + VerifyBundle — the same primitive the startup health-gate uses), so every retry
// re-verifies before any swap. When the target is no longer armed (already updated on a prior
// re-exec, or abandoned/refused) it clears the stale SelfUpdateBlocked latch.
//
// It MUST be called on the daemon's MAIN loop thread, never a goroutine: performSelfUpdate re-execs
// via syscall.Exec on success, which must never interrupt a mid-flight install.sh apply.
//
// attempted reports whether a download/swap was actually tried (for the caller's logging/pacing); err
// is the fetch/verify/download error — best-effort, the caller keeps last-good and retries next tick.
func RetryDeferredSelfUpdate(p *SelfUpdateParams, nodeID, stateDir string, verifiedFetch func() (map[string][]byte, error), stderr io.Writer) (attempted bool, err error) {
	if p == nil {
		return false, nil
	}
	st, _ := LoadState(stateDir)
	if st == nil || st.SelfUpdateBlocked == "" {
		return false, nil // nothing armed → do not even fetch
	}
	if st.PendingUpdate != nil {
		return false, nil // a swap is in flight; the boot reconcile owns it, not the retry
	}

	files, ferr := verifiedFetch()
	if ferr != nil {
		return false, ferr // keep last-good; retry next tick
	}
	cat := parseAgentCatalog(files)
	dec, _ := decideSelfUpdate(cat, p.RunningVersion, st.AgentVersionFloor, st.AbandonedAgentVersion)
	if dec != updateAfterApply && dec != updateForced {
		// No longer armed: the update already took effect on a prior re-exec, or the target was
		// abandoned/refused. Drop the stale Blocked latch so the condition clears (the new-generation
		// path clears it via recordSuccess; this is the missing clear for a stable generation).
		clearSelfUpdateBlocked(stateDir)
		return false, nil
	}

	cfg := &Config{NodeID: nodeID, StateDir: stateDir, SelfUpdate: p}
	swapped, suErr := performSelfUpdate(cfg, cat, p.RunningVersion, p.GithubProxy, stderr)
	if suErr != nil {
		// Pre-swap failure: refresh the curated Blocked reason so the panel stays accurate and the
		// next tick retries. A POST-swap failure (swapped) must NOT be reclassified — its on-disk
		// breadcrumb has to survive for the next-boot reconcile (mirrors agent.go's post-apply path).
		if !swapped {
			if reason := classifySelfUpdateBlock(suErr); reason != "" {
				recordSelfUpdateBlocked(cfg, reason)
			}
		}
		return true, suErr
	}
	return true, nil // unreachable on success (performSelfUpdate re-execs and never returns)
}

// clearSelfUpdateBlocked drops the curated Blocked reason once a deferred self-update is no longer
// armed (it succeeded, or its target was abandoned/refused). Load-modify-save; a transient read
// failure is non-fatal (the next pass retries). No-op when nothing is set.
func clearSelfUpdateBlocked(stateDir string) {
	st, err := LoadState(stateDir)
	if err != nil || st == nil || st.SelfUpdateBlocked == "" {
		return
	}
	st.SelfUpdateBlocked = ""
	_ = SaveState(stateDir, st)
}
