package agent

// cycle.go holds the controller-mode per-cycle logic, extracted from cmd/agent so it
// is unit-testable in-process (cmd/agent's runControllerMode now just sequences the
// token load, client build, and the daemon/single-shot loop around RunControllerCycle).
//
// One cycle is the deterministic unit a daemon loops over: long-poll for a newer
// generation, fetch the bundle, then either ROTATE (the operator flagged this node for
// a WireGuard key rotation) or APPLY (run the verified bundle). It is keep-last-good on
// error: a transport or apply failure returns the unchanged watermark so the running
// overlay is never torn down and the caller never advances past a failed cycle.

import (
	"fmt"
	"io"
	"os"
)

// CycleConfig groups the inputs one RunControllerCycle needs. It mirrors the subset of
// agent.Config a controller-pull deploy uses, plus the resume watermark (After) the
// caller advances each cycle. The Source is the live ControllerClient (passed
// separately to RunControllerCycle so the cycle can read its post-Fetch signals via
// LastRekeyRequested / LastFetchedGeneration); these fields configure the apply.
type CycleConfig struct {
	// NodeID is the configured identity (bundle subdir / state key).
	NodeID string
	// After is the resume cursor: poll for a generation strictly greater than this.
	After int64
	// PinnedPubPEM is the operator-pinned signing public key (PKIX PEM), or nil when
	// no key is pinned (unsigned bundles then permitted).
	PinnedPubPEM []byte
	// OperatorCredPEM is the off-host operator credential's public key (PKIX PEM) for
	// the keystone trust-list gate, or nil when keystone is OFF (opt-in). When set,
	// the apply requires a valid off-host-signed trust-list in the bundle.
	// OperatorCredAlg/RPID/Origin describe that credential.
	OperatorCredPEM []byte
	OperatorCredAlg string
	OperatorRPID    string
	OperatorOrigin  string
	// StateDir holds the agent's persisted last-applied state.
	StateDir string
	// StagingDir is where the verified bundle is materialized before install.sh runs
	// (empty -> a fresh temp dir per apply).
	StagingDir string
	// KeyPath is the local WireGuard private-key file rotated on a rekey signal
	// (empty -> DefaultKeyPath). Injectable so a test never touches /etc/wireguard.
	KeyPath string
	// Stdout/Stderr receive install.sh's streamed output and the cycle's log lines.
	// When nil, the process stdio is used.
	Stdout io.Writer
	Stderr io.Writer
}

// RunControllerCycle runs ONE controller-pull cycle from cfg.After against client and
// returns the generation to resume from, whether a new generation was applied, and any
// error. It is the testable core of cmd/agent's controller mode; the daemon and
// single-shot loops both call it.
//
// Sequence:
//
//	Poll(After) -> on change, Fetch (records the bundle's generation + the rekey
//	signal on client) -> if LastRekeyRequested(): RegenerateKey + Rekey, then return
//	(wake generation, false, nil) — SKIP apply, advancing the watermark PAST the wake so
//	this (now-stale, pre-rekey) bundle is never re-applied; else agent.Run
//	(verify+apply+report), then return (LastFetchedGeneration(), true, nil).
//
// The rekey-branch watermark advance is the BLOCKER-2 fix. A rekey-all BUMPS the tenant
// generation WITHOUT changing the bundle, so /config still reports the OLD bundle's
// (smaller) generation; the resume cursor is therefore max(polled, LastFetchedGeneration())
// — the polled (wake) generation is the one that actually advanced. Resuming from the
// unchanged After (or from the smaller fetched bundle generation) would leave the bumped
// generation strictly greater than the cursor, so the next Poll returns immediately and
// re-fetches+re-rotates in a tight loop, or — worse — re-applies the wake bundle (compiled
// with peers' OLD pubkeys), causing a sustained full-mesh outage. Resuming from the wake
// guarantees the next generation the agent applies is strictly greater: the operator's
// POST-rekey Deploy, recompiled with every node's new public keys. Skipping any
// intermediate generations between the wake and that Deploy is correct: the post-rekey
// full Deploy supersedes them.
//
// Keep-last-good: every error path (poll/fetch/regenerate/rekey/run) returns the
// UNCHANGED After with applied=false, so the running overlay is untouched and the caller
// never advances its cursor past a failed cycle. A timed-out long-poll returns (After,
// false, nil) — nothing new to do.
func RunControllerCycle(client *ControllerClient, cfg CycleConfig) (resumeGen int64, applied bool, err error) {
	after := cfg.After
	stderr := cfg.Stderr
	if stderr == nil {
		stderr = os.Stderr
	}
	keyPath := cfg.KeyPath
	if keyPath == "" {
		keyPath = DefaultKeyPath
	}

	// On the APPLY path the polled generation is only a change SIGNAL — the resume cursor
	// comes from the generation actually FETCHED (LastFetchedGeneration), closing the
	// poll->fetch race. On the REKEY-WAKE path it is load-bearing: a rekey-all BUMPS the
	// tenant generation WITHOUT changing the bundle, so the polled (wake) generation is
	// strictly greater than the bundle's own generation that /config reports. We keep it
	// so the rekey branch can resume from the wake — see that branch for why.
	polledGen, changed, err := client.Poll(after)
	if err != nil {
		return after, false, fmt.Errorf("poll: %w", err)
	}
	if !changed {
		return after, false, nil // long-poll timed out; nothing new
	}

	// Fetch the bundle FIRST so we can inspect the controller's rekey signal before
	// deciding whether to apply. /config is idempotent (it returns the current bundle +
	// flags for this node). The rekey branch never applies these files, and the apply
	// path re-fetches inside agent.Run; the idle check below reads manifest.json from
	// this fetch to decide whether there is anything new to apply at all.
	files, err := client.Fetch(cfg.NodeID)
	if err != nil {
		return after, false, fmt.Errorf("fetch: %w", err) // keep-last-good
	}

	// REKEY branch: the operator flagged this node for a WireGuard key rotation
	// (zero-knowledge — the controller never sees the private key). Regenerate the LOCAL
	// key, register the NEW public key via /rekey (which clears the flag), and SKIP
	// applying this (now-stale, pre-rekey) bundle.
	//
	// Resume from the WAKE generation (BLOCKER-2 fix) so this wake bundle is never re-
	// applied and the next generation the agent applies is strictly greater — the
	// operator's post-rekey Deploy carrying everyone's new public keys. The resume cursor
	// is max(polledGen, LastFetchedGeneration()): a rekey-all BUMPS the tenant generation
	// without changing the bundle, so /config still reports the OLD bundle's (smaller)
	// generation in LastFetchedGeneration(); resuming from that would leave the bumped
	// generation strictly greater than the cursor, so the very next Poll would return
	// immediately and re-enter this branch in a tight loop. The polled (wake) generation
	// is the one that actually advanced, so we resume from it (the max also handles a
	// promote racing in between, where the fetched bundle generation could exceed the
	// poll). A brief per-link flap during the operator's rolling redeploy is the accepted
	// cost.
	if client.LastRekeyRequested() {
		newPub, err := RegenerateKey(keyPath)
		if err != nil {
			return after, false, fmt.Errorf("rekey: regenerate key: %w", err) // keep-last-good
		}
		if err := client.Rekey(newPub); err != nil {
			return after, false, fmt.Errorf("rekey: register new public key: %w", err) // keep-last-good
		}
		fmt.Fprintln(stderr, "agent: rekeyed; awaiting redeploy")
		resume := polledGen
		if fetched := client.LastFetchedGeneration(); fetched > resume {
			resume = fetched
		}
		return resume, false, nil
	}

	// IDLE branch (plan-3, "root install.sh never re-runs without a new bundle"): the
	// poll woke us, but the bundle the controller serves is one this node ALREADY
	// applied successfully — its generation is not past our cursor AND its manifest
	// checksum equals the persisted last-applied checksum. This is the orphaned-node
	// shape: the node left the design, its current bundle is frozen, and every promote
	// for OTHER nodes bumps the tenant generation and wakes us. Without this branch the
	// cycle re-runs install.sh as root on every wake and — because the frozen bundle's
	// generation lags the tenant's — resumes from a stale cursor, so the next poll
	// returns instantly: a root-executing busy loop. Skip the apply and resume from the
	// WAKE generation (same rationale as the rekey branch), restoring long-poll pacing.
	//
	// Both clauses matter. Checksum equality alone would also skip a single-shot rerun
	// from a cold cursor (--after 0), killing the operator's force-reapply workflow;
	// requiring the fetched generation to be ≤ the live cursor confines the skip to a
	// daemon that has already applied this exact content this run. A failed last apply
	// (LastResult != "ok") never skips — the retry is wanted.
	if fetchedGen := client.LastFetchedGeneration(); fetchedGen <= after {
		if man, perr := parseManifest(files["manifest.json"]); perr == nil && man.Checksum != "" {
			if prev, serr := LoadState(cfg.StateDir); serr == nil && prev != nil &&
				prev.LastResult == LastResultOK && prev.LastChecksum == man.Checksum {
				fmt.Fprintf(stderr,
					"agent: woke at generation %d but the served bundle (gen %d) is already applied (checksum %s); idling\n",
					polledGen, fetchedGen, man.Checksum)
				// Resume from the WAKE generation. (polledGen > after ≥ fetchedGen
				// here: Poll only reports change for a strictly greater generation,
				// and this branch requires fetchedGen ≤ after — no max() needed.)
				return polledGen, false, nil
			}
		}
	}

	// APPLY branch. Record the prior watermark so a FAILED apply reports it unchanged
	// (never falsely advancing); a successful apply reports the generation actually
	// fetched. agent.Run fetches the bundle (setting the fetched generation) and fires
	// the auto-Report itself, since this client is a Reporter.
	client.SetPriorGeneration(after)
	res, runErr := Run(&Config{
		NodeID:          cfg.NodeID,
		Source:          client,
		PinnedPubPEM:    cfg.PinnedPubPEM,
		OperatorCredPEM: cfg.OperatorCredPEM,
		OperatorCredAlg: cfg.OperatorCredAlg,
		OperatorRPID:    cfg.OperatorRPID,
		OperatorOrigin:  cfg.OperatorOrigin,
		StateDir:        cfg.StateDir,
		StagingDir:      cfg.StagingDir,
		Stdout:          cfg.Stdout,
		Stderr:          cfg.Stderr,
	})
	if runErr != nil {
		return after, false, fmt.Errorf("run: %w", runErr) // keep-last-good
	}
	if res != nil {
		printAppliedTo(stderr, res)
	}
	// Resume from the generation actually FETCHED and applied, not the one the poll
	// observed: a promote landing between Poll returning gen N and Fetch returning gen
	// N+1 means the bundle carried N+1, and resuming from N would re-fetch+re-apply it
	// next cycle. Advancing the watermark to LastFetchedGeneration() keeps it from
	// lagging under that poll->fetch race.
	appliedGen := client.LastFetchedGeneration()
	fmt.Fprintf(stderr, "agent: applied controller generation %d\n", appliedGen)
	return appliedGen, true, nil
}

// printAppliedTo logs a one-line apply summary to w (the cycle's stderr). It mirrors
// cmd/agent's printApplied so the daemon/single-shot loops can move into the agent
// package without dragging the formatting helper along.
func printAppliedTo(w io.Writer, res *RunResult) {
	signed := false
	count := 0
	if res.Verify != nil {
		signed = res.Verify.Signed
		count = res.Verify.FileCount
	}
	fmt.Fprintf(w, "agent: applied generation compiled_at=%s checksum=%s signed=%t files=%d\n",
		res.CompiledAt, res.Checksum, signed, count)
}
