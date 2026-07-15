package agent

import (
	"fmt"
	"io"
)

// selfupdate_retry.go makes a DEFERRED self-update recover on its own (plan-8). The apply path
// attempts a post-apply self-update once (agent.go step 7); the daemon only re-runs that path on a
// NEW generation, so a download failure on a stable generation used to wedge the rollout until a
// manual `systemctl restart yaog-agent`. RetryDeferredSelfUpdate re-attempts it on the daemon's idle
// cycles (on a backoff), reusing the SAME verified-fetch → decide → perform path the apply uses. The
// daemon passes a membership-verifying fetch (VerifyBundle + VerifyMembership) — the SAME keystone
// binding the apply path enforces before a swap — so a swap decision is bound to the off-host operator
// credential, not merely the tier-1 bundle signature; the signed-self-update custody model holds.

// RetryDeferredSelfUpdate re-attempts a post-apply self-update that a prior cycle deferred, WITHOUT
// requiring a new generation. It is a no-op unless State.SelfUpdateBlocked is set (an armed deferral)
// and no swap is already in flight. verifiedFetch returns the cryptographically VERIFIED bundle file
// map — the daemon passes a fetch that runs VerifyBundle AND VerifyMembership (the apply path's full
// pre-swap verification), so a keystone-ON node binds the swap pin to the off-host credential and a
// breached controller / MITM cannot drive a retry-path swap; every retry re-verifies before any swap.
// When the target is no longer armed (already updated on a prior re-exec, or abandoned/refused) it
// clears the stale SelfUpdateBlocked latch.
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
	if st.PendingApply != nil {
		// Do not introduce a binary swap while a root-side configuration mutation is
		// unresolved. The main cycle first retries/supersedes that verified operation;
		// its successful commit clears PendingApply and re-derives any deferred update.
		return false, nil
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

// WithMembershipGate wraps a bundle-verified fetch (one returning a VerifyBundle-verified file map)
// with the off-host keystone membership check — the SAME gate the apply path runs before a swap
// (agent.go: VerifyBundle then VerifyMembership). It loads the anti-rollback epoch floor from stateDir.
// When keystone is OFF (cfg.OperatorCredPEM empty) VerifyMembership is a no-op and the wrapped fetch
// passes through unchanged. On a membership failure it returns an error and NO files, so a caller that
// swaps on the returned files fails closed: a breached controller or MITM that can serve a
// VerifyBundle-passing bundle still cannot drive a self-update swap on a keystone-ON node. This is what
// the deferred-retry path uses so a swap decision is bound to the off-host credential, not merely the
// tier-1 bundle signature.
func WithMembershipGate(fetch func() (map[string][]byte, error), cfg MembershipConfig, stateDir string) func() (map[string][]byte, error) {
	return func() (map[string][]byte, error) {
		files, err := fetch()
		if err != nil {
			return nil, err
		}
		st, stateErr := LoadState(stateDir)
		if stateErr != nil {
			// A self-update pin must never be accepted against a reset epoch after a
			// mid-operation state read failure. Return no files so the swap caller fails
			// closed and the existing custody state remains untouched.
			return nil, fmt.Errorf("load membership anti-rollback state: %w", stateErr)
		}
		prevEpoch := effectiveMembershipFloor(st)
		if _, err := VerifyMembership(files, cfg, prevEpoch); err != nil {
			return nil, fmt.Errorf("membership: %w", err)
		}
		return files, nil
	}
}
