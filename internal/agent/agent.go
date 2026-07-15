package agent

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Config holds the agent's runtime configuration, assembled from CLI flags. All
// filesystem paths are injectable so tests never touch real /etc/wireguard or
// /var/lib.
type Config struct {
	// NodeID is the configured identity (no enrollment): the bundle subdir/key.
	NodeID string
	// Source is where bundles are fetched from (DirSource or HTTPSource).
	Source Source
	// PinnedPubPEM is the operator-pinned signing public key in PKIX PEM, or nil
	// when no key is pinned (unsigned bundles then permitted).
	PinnedPubPEM []byte
	// OperatorCredPEM is the off-host operator credential's public key (PKIX PEM)
	// for the keystone trust-list gate, or nil when keystone is OFF (opt-in). When
	// set, Run requires a valid, off-host-signed trust-list in the bundle (see
	// VerifyMembership). OperatorCredAlg/RPID/Origin describe that credential.
	OperatorCredPEM []byte
	OperatorCredAlg string
	OperatorRPID    string
	OperatorOrigin  string
	// KeyPath is the local WireGuard private-key file (default DefaultKeyPath).
	KeyPath string
	// StateDir holds the agent's persisted last-applied state (default
	// DefaultStateDir).
	StateDir string
	// StateSaver overrides SaveState for Run's final success/failure bookkeeping.
	// Production callers leave it nil; the seam lets embedders and tests surface a
	// post-apply durability failure without replacing the rest of the verified run.
	StateSaver func(stateDir string, state *State) error
	// StagingDir is an optional securely-owned parent under which a fresh verified
	// bundle directory is materialized before install.sh runs. When empty, the OS
	// temporary directory is used. Operator-supplied parents retain the fresh child
	// after Run for inspection.
	StagingDir string
	// InstallArgs is the closed set of arguments the caller intentionally forwards to the
	// verified, staged install.sh. Normal managed applies leave it empty. The trusted manual
	// `kit apply --uninstall` path supplies only "--uninstall" after the same verification gates.
	InstallArgs []string
	// Stdout/Stderr receive install.sh's streamed output. When nil the process
	// stdio is used.
	Stdout io.Writer
	Stderr io.Writer
	// SelfUpdate, when non-nil, enables signed agent self-update (plan-9, controller mode):
	// after the bundle is verified, the agent may swap its own binary to the version pinned in
	// the bundle's (signed) artifacts.json. Nil in air-gap (DirSource / cmd/compiler) ⇒ no
	// self-update, Run behaves exactly as before.
	SelfUpdate *SelfUpdateParams
}

// RunResult summarizes one Run for the caller (and the status report).
type RunResult struct {
	// Applied is true when install.sh ran and exited 0.
	Applied bool
	// Action is "apply" or "uninstall", matching the verified install.sh action.
	Action string
	// CompiledAt is the manifest compiled_at of the bundle that was applied (or
	// considered).
	CompiledAt string
	// Checksum is the manifest checksum of that bundle.
	Checksum string
	// Verify is the verification outcome (signed/hash-only, file count).
	Verify *VerifyResult
	// StagingDir is where the bundle was materialized.
	StagingDir string
	// MembershipEpoch is the off-host-signed trust-list epoch verified for this apply
	// (zero for both keystone-off and the valid initial keystone epoch).
	MembershipEpoch int64
}

// Run executes the full control loop: pull -> verify -> anti-rollback -> apply ->
// report. It is fail-closed on a NEW apply (verify failure, rollback, bad bundle)
// but degradation-safe: it NEVER tears down a running tunnel. A failure before
// apply simply leaves the last-good configuration in place and returns an error;
// install.sh is only invoked once the Go-side gate has fully passed.
func Run(cfg *Config) (*RunResult, error) {
	return run(cfg, nil)
}

// run executes Run with an optional already-held state lease. Controller cycles acquire the lease
// before their rekey/idle/apply branch so key rotation and root apply share one ownership boundary;
// direct callers use Run, which acquires the lease here.
func run(cfg *Config, heldStateLease *stateLease) (*RunResult, error) {
	if cfg == nil {
		return nil, fmt.Errorf("agent: nil config")
	}
	if strings.TrimSpace(cfg.NodeID) == "" {
		return nil, fmt.Errorf("agent: empty node id")
	}
	if cfg.Source == nil {
		return nil, fmt.Errorf("agent: nil source")
	}
	if err := validateInstallArgs(cfg.InstallArgs); err != nil {
		return nil, err
	}
	if err := validateInstallerPlatform(); err != nil {
		return nil, err
	}

	// Serialize the complete custody transition across processes that share this state
	// directory. The daemon and a manual `kit apply` can otherwise both load the same
	// anti-rollback floors, run different root scripts, then let the last SaveState win —
	// regressing membership/compiled-at state or making the durable record describe a
	// different host configuration. The persistent lock file is only an inode and never
	// acts as a stale sentinel. While install.sh runs, its guardian inherits this exact
	// kernel lease so killing the Go parent cannot admit a competing root mutation.
	stateLease := heldStateLease
	if stateLease == nil {
		var err error
		stateLease, err = acquireStateLease(cfg.StateDir)
		if err != nil {
			return nil, err
		}
		defer func() { _ = stateLease.release() }()
	} else if stateLease.file == nil {
		return nil, fmt.Errorf("agent: held state lease is closed")
	}

	// Load prior state up front; needed for anti-rollback and so a failure can be
	// recorded without losing the last-good baseline.
	prev, err := LoadState(cfg.StateDir)
	if err != nil {
		return nil, err
	}
	// State carries the anti-rollback floors for exactly one node. Reusing it for another identity
	// would either import unrelated floors (availability failure) or overwrite the first node's
	// custody history. Fail before fetching/verifying/applying and leave the existing record intact.
	if prev.NodeID != "" && prev.NodeID != cfg.NodeID {
		return nil, fmt.Errorf("agent: state belongs to node %q, not configured node %q; use the correct --state-dir", prev.NodeID, cfg.NodeID)
	}
	if err := validatePendingApply(cfg, prev); err != nil {
		return nil, err
	}
	// Once this node has successfully applied an off-host-signed membership, keystone use is
	// sticky. Silently omitting the operator credential on a later invocation must not turn
	// VerifyMembership into a no-op and authorize an otherwise-unchecked install/uninstall. A
	// deliberate keystone retirement needs a separate reprovisioning ceremony, not a missing flag.
	if (prev.MembershipVerified || prev.MembershipEpoch > 0) && len(cfg.OperatorCredPEM) == 0 {
		return nil, fmt.Errorf("agent: state records keystone membership epoch %d but no operator credential is configured; refusing trust downgrade", prev.MembershipEpoch)
	}

	// 1. pull
	files, err := cfg.Source.Fetch(cfg.NodeID)
	if err != nil {
		// Source unreachable: degrade — keep last-good, record the failure, do not
		// touch the running tunnel.
		recordFailure(cfg, prev, fmt.Sprintf("fetch failed: %v", err))
		return nil, fmt.Errorf("agent: pull: %w", err)
	}
	// A verifier that claims this bundle is apply-ready must reject path sets that
	// cannot be materialized to one deterministic tree. Run checks immediately
	// after capture; stage repeats the check at the write boundary as defense in
	// depth against a buggy Source mutating its returned map concurrently.
	if err := PreflightBundleMaterialization(files); err != nil {
		recordFailure(cfg, prev, fmt.Sprintf("bundle materialization preflight failed: %v", err))
		return nil, fmt.Errorf("agent: bundle materialization preflight: %w", err)
	}

	// manifest.json is required for anti-rollback and reporting.
	manRaw, ok := files["manifest.json"]
	if !ok {
		recordFailure(cfg, prev, "bundle missing manifest.json")
		return nil, fmt.Errorf("agent: bundle missing manifest.json")
	}
	man, err := parseManifest(manRaw)
	if err != nil {
		recordFailure(cfg, prev, err.Error())
		return nil, err
	}

	// Fail closed if the bundle's manifest identifies a different node than this agent is
	// configured for: a misconfigured or malicious source must not get us to apply another
	// node's (validly-signed) bundle. An empty node_id is tolerated (older bundles may omit it).
	if man.NodeID != "" && man.NodeID != cfg.NodeID {
		recordFailure(cfg, prev, fmt.Sprintf("manifest node_id %q != configured node id %q", man.NodeID, cfg.NodeID))
		return nil, fmt.Errorf("agent: bundle manifest node_id %q does not match configured node id %q", man.NodeID, cfg.NodeID)
	}

	action := LastActionApply
	if len(cfg.InstallArgs) == 1 {
		action = LastActionUninstall
	}
	res := &RunResult{CompiledAt: man.CompiledAt, Checksum: man.Checksum, Action: action}

	// 2. verify (fail-closed, BEFORE anything root-side runs)
	vr, err := VerifyBundle(files, cfg.PinnedPubPEM)
	if err != nil {
		recordFailure(cfg, prev, fmt.Sprintf("verify failed: %v", err))
		return res, fmt.Errorf("agent: verify: %w", err)
	}
	res.Verify = vr

	// 2b. membership keystone (fail-closed, AFTER tier-1 integrity, BEFORE apply).
	// When an off-host operator credential is pinned, the bundle's membership must be
	// signed by that credential — a breached controller cannot forge it. No-op when
	// keystone is OFF (no OperatorCredPEM). On success it returns the verified epoch,
	// which a successful apply persists as the new anti-rollback floor.
	membershipEpoch, err := VerifyMembership(files, MembershipConfig{
		NodeID:          cfg.NodeID,
		OperatorCredPEM: cfg.OperatorCredPEM,
		OperatorCredAlg: cfg.OperatorCredAlg,
		OperatorRPID:    cfg.OperatorRPID,
		OperatorOrigin:  cfg.OperatorOrigin,
	}, effectiveMembershipFloor(prev))
	if err != nil {
		recordFailure(cfg, prev, fmt.Sprintf("membership verify failed: %v", err))
		return res, fmt.Errorf("agent: membership verify: %w", err)
	}
	res.MembershipEpoch = membershipEpoch

	// 3. anti-rollback. NOTE: compiled_at comes from manifest.json, which export deliberately
	// leaves OUT of the signed/checksummed set, so this stub only guards against an honest source
	// accidentally serving a stale bundle — NOT an active attacker/MITM, who could forge compiled_at
	// to force a rollback to any previously signed bundle. Attacker-resistant anti-rollback (a signed
	// version/generation bound into the bundle) is a Phase 2/3 item (docs/spec/controller/agent.md).
	// Runs BEFORE the self-update so a stale bundle never triggers an agent swap (F4).
	if err := CheckRollback(effectiveRollbackState(prev), man.CompiledAt); err != nil {
		recordFailure(cfg, prev, err.Error())
		return res, err
	}
	if err := validateCandidateAgainstPending(prev, man, files, action); err != nil {
		recordFailure(cfg, prev, err.Error())
		return res, err
	}

	// 3b. Signed self-update (plan-9), controller mode only (cfg.SelfUpdate != nil). The bundle's
	// artifacts.json — verified above (VerifyBundle's coverage guard) — may pin a newer agent. A
	// FORCED update (running < agent.min_version) must happen BEFORE applying an incompatible
	// bundle; a non-forced update is deferred until AFTER a successful apply. The download is
	// verified against the signed pin (custody) and self-tested before exec; on success the
	// process is replaced (performSelfUpdate does not return).
	var selfUpdateCatalog *agentCatalog
	deferSelfUpdate := false
	if cfg.SelfUpdate != nil {
		selfUpdateCatalog = parseAgentCatalog(files)
		running := cfg.SelfUpdate.RunningVersion
		dec, reason := decideSelfUpdate(selfUpdateCatalog, running, prev.AgentVersionFloor, prev.AbandonedAgentVersion)
		if isForced(selfUpdateCatalog, running) {
			if dec != updateForced {
				// Forced but the update is impermissible (downgrade / missing pin / target below
				// min / abandoned): refuse to apply an incompatible bundle, keep last-good.
				recordFailure(cfg, prev, "below min_version and cannot self-update: "+reason)
				return res, fmt.Errorf("agent: below required min_version, cannot self-update: %s", reason)
			}
			swapped, suErr := performSelfUpdate(cfg, selfUpdateCatalog, running, cfg.SelfUpdate.GithubProxy, stderrOf(cfg))
			if suErr != nil {
				// recordFailure ONLY when the binary was NOT swapped (pre-swap failure: download /
				// verify / self-test / in-flight). If swapped=true, performSelfUpdate already
				// replaced the binary + wrote the on-disk breadcrumb and only the re-exec failed —
				// recordFailure would rebuild State from the stale pre-swap `prev` and ERASE that
				// breadcrumb, leaving the swapped (possibly bad) binary with nothing for the
				// next-boot reconcile to roll back: an unbounded crash loop (R1-1).
				if !swapped {
					recordFailure(cfg, prev, fmt.Sprintf("below min_version, self-update failed: %v", suErr))
				}
				return res, fmt.Errorf("agent: below min_version, self-update failed: %w", suErr)
			}
			// performSelfUpdate execs on success and never returns; this is unreachable.
			return res, fmt.Errorf("agent: self-update re-exec failed")
		}
		deferSelfUpdate = dec == updateAfterApply
	}

	// 4. stage to disk (only after verify+rollback pass)
	staging, cleanup, err := stage(cfg, files)
	if err != nil {
		recordFailure(cfg, prev, fmt.Sprintf("stage failed: %v", err))
		return res, err
	}
	if cleanup != nil {
		defer cleanup()
	}
	res.StagingDir = staging

	// 5. Persist a write-ahead intent before install.sh can mutate the host. It keeps
	// last-known-good fields separate while making the candidate's rollback/membership
	// floors and verified bundle identity crash-durable. A restart can therefore retry
	// or supersede the operation safely, but can never fall back behind an interrupted apply.
	intentState, err := persistPendingApply(cfg, prev, man, files, membershipEpoch, action)
	if err != nil {
		recordFailure(cfg, prev, err.Error())
		return res, err
	}

	// 6. apply (run staged install.sh as the current root process)
	if err := apply(cfg, staging, stateLease); err != nil {
		// install.sh may have partially mutated the host before returning. Preserve the
		// pending intent so its floors remain effective and an exact retry can converge.
		recordFailure(cfg, intentState, fmt.Sprintf("install.sh failed: %v", err))
		return res, fmt.Errorf("agent: apply: %w", err)
	}
	res.Applied = true

	// 7. Commit the successful result, advancing the last-known-good fields and
	// clearing PendingApply in one durable replacement. A failure leaves the earlier
	// intent as the conservative recovery floor and is returned loudly.
	if err := recordSuccess(cfg, intentState, man, vr, membershipEpoch); err != nil {
		return res, fmt.Errorf("agent: install.sh completed but anti-rollback state is not durable: %w", err)
	}

	// 8. deferred self-update (plan-9): the bundle applied cleanly and a newer agent is pinned.
	// Best-effort — the bundle is already applied, so a download/verify failure is logged and the
	// next cycle retries; it never fails this Run. On success performSelfUpdate re-execs (no return).
	if deferSelfUpdate {
		// Best-effort: the bundle is already applied. On a pre-swap failure the next cycle retries;
		// on a post-swap re-exec failure (swapped=true) the on-disk breadcrumb survives for the
		// next-boot reconcile — either way we only log, never recordFailure (which never runs here).
		if _, err := performSelfUpdate(cfg, selfUpdateCatalog, cfg.SelfUpdate.RunningVersion, cfg.SelfUpdate.GithubProxy, stderrOf(cfg)); err != nil {
			fmt.Fprintf(stderrOf(cfg), "agent: post-apply self-update deferred: %v\n", err)
			// Surface WHY (observability only): persist a curated reason so the panel/telemetry shows
			// the stalled rollout as a `selfupdate` Blocked condition instead of the node silently
			// staying behind. recordSuccess above rebuilt the apply state WITHOUT this field, so it is
			// self-clearing — set only while the block persists. No custody state is touched.
			if reason := classifySelfUpdateBlock(err); reason != "" {
				recordSelfUpdateBlocked(cfg, reason)
			}
		}
	}
	return res, nil
}

// recordSelfUpdateBlocked persists the curated reason a post-apply self-update was deferred, so the
// panel/telemetry can surface a stalled rollout (see State.SelfUpdateBlocked). Load-modify-save over
// the just-persisted apply state; observability only — it touches no custody field. Best-effort: a
// state-write failure is non-fatal (the bundle is already applied; the next cycle retries).
func recordSelfUpdateBlocked(cfg *Config, reason string) {
	st, err := LoadState(cfg.StateDir)
	if err != nil || st == nil {
		// A read/parse error must NOT cause us to write a stripped state — that would zero the
		// custody floors recordSuccess just persisted (config + keystone anti-rollback,
		// AgentVersionFloor, the in-flight breadcrumb) and open an anti-rollback/downgrade hole.
		// SelfUpdateBlocked is observability only, so we'd rather skip recording it this cycle (the
		// next clean cycle re-derives it) than risk custody. Never fall back to a fresh &State{}.
		return
	}
	st.NodeID = cfg.NodeID
	st.SelfUpdateBlocked = reason
	_ = SaveState(cfg.StateDir, st)
}

// stage materializes the verified bundle into a staging directory, preserving
// relative paths. The dir is 0700; install.sh is 0755; everything else 0600
// except world-safe metadata (manifest/README/checksums/pubkey) at 0644. It
// returns a cleanup func that removes the staging dir (nil when StagingDir was
// an operator-supplied parent — operators may want to inspect the fresh child).
func stage(cfg *Config, files map[string][]byte) (string, func(), error) {
	paths, err := validateBundlePaths(files)
	if err != nil {
		return "", nil, err
	}

	var dir string
	var cleanup func()
	if strings.TrimSpace(cfg.StagingDir) != "" {
		if err := EnsureSecureOwnedDir(cfg.StagingDir); err != nil {
			return "", nil, fmt.Errorf("agent: secure staging parent: %w", err)
		}
		fresh, err := os.MkdirTemp(cfg.StagingDir, "yaog-agent-stage-")
		if err != nil {
			return "", nil, fmt.Errorf("agent: create staging child: %w", err)
		}
		dir = fresh
	} else {
		tmp, err := os.MkdirTemp("", "yaog-agent-stage-")
		if err != nil {
			return "", nil, fmt.Errorf("agent: create staging dir: %w", err)
		}
		dir = tmp
		cleanup = func() { _ = os.RemoveAll(tmp) }
	}

	for _, rel := range paths {
		content := files[rel]
		clean := filepath.FromSlash(rel)
		dst := filepath.Join(dir, clean)
		if err := os.MkdirAll(filepath.Dir(dst), 0700); err != nil {
			if cleanup != nil {
				cleanup()
			}
			return "", nil, fmt.Errorf("agent: mkdir for %s: %w", rel, err)
		}
		if err := os.WriteFile(dst, content, fileMode(rel)); err != nil {
			if cleanup != nil {
				cleanup()
			}
			return "", nil, fmt.Errorf("agent: write %s: %w", rel, err)
		}
	}
	return dir, cleanup, nil
}

// PreflightBundleMaterialization proves that every source-map key has one
// canonical, cross-platform destination and that no file aliases or conflicts
// with another file's destination. Any command that reports a bundle as ready
// to apply must run this preflight, even when it does not itself write the tree.
func PreflightBundleMaterialization(files map[string][]byte) error {
	_, err := validateBundlePaths(files)
	return err
}

// validateBundlePaths establishes one canonical, unambiguous destination for every source-map key
// before stage creates or writes a file. VerifyBundle authenticates the canonical checksummed names;
// without this preflight, an unlisted alias such as "x/../install.sh" or "./install.sh" could pass
// verification and overwrite the verified script later, depending on randomized map iteration.
func validateBundlePaths(files map[string][]byte) ([]string, error) {
	paths := make([]string, 0, len(files))
	seen := make(map[string]string, len(files))
	for rel := range files {
		if err := validateCanonicalBundlePath(rel); err != nil {
			return nil, err
		}
		// Case-fold the slash form as a conservative cross-platform destination key. Agent bundles
		// use lowercase machine filenames; accepting two names that collide on a case-insensitive
		// filesystem would make the bytes reaching install.sh platform/map-order dependent.
		normalized := strings.ToLower(filepath.ToSlash(filepath.Clean(filepath.FromSlash(rel))))
		if prior, exists := seen[normalized]; exists {
			return nil, fmt.Errorf("agent: bundle paths %q and %q normalize to the same destination", prior, rel)
		}
		seen[normalized] = rel
		paths = append(paths, rel)
	}

	// A file cannot also be the parent directory of another file. Reject the ambiguous tree here
	// instead of letting materialization outcome depend on whether the map yielded parent or child first.
	for normalized, rel := range seen {
		for parent := path.Dir(normalized); parent != "."; parent = path.Dir(parent) {
			if prior, exists := seen[parent]; exists {
				return nil, fmt.Errorf("agent: bundle path %q conflicts with child path %q", prior, rel)
			}
		}
	}
	sort.Strings(paths)
	return paths, nil
}

func validateCanonicalBundlePath(rel string) error {
	if rel == "" || strings.ContainsRune(rel, '\x00') || strings.Contains(rel, `\`) {
		return fmt.Errorf("agent: unsafe bundle path %q: path must be a non-empty canonical slash-relative name", rel)
	}
	// Reject drive-qualified paths on every build OS, not only on Windows where filepath.VolumeName
	// recognizes them. Bundles are portable and their keys must never acquire host-specific meaning.
	if len(rel) >= 2 && ((rel[0] >= 'A' && rel[0] <= 'Z') || (rel[0] >= 'a' && rel[0] <= 'z')) && rel[1] == ':' {
		return fmt.Errorf("agent: unsafe bundle path %q: volume-qualified paths are not allowed", rel)
	}
	clean := path.Clean(rel)
	if path.IsAbs(rel) || clean == "." || clean == ".." || strings.HasPrefix(clean, "../") || clean != rel {
		return fmt.Errorf("agent: unsafe bundle path %q: path must be canonical and slash-relative", rel)
	}
	return nil
}

// fileMode picks the on-disk permission for a staged bundle file: install.sh is
// executable, WireGuard confs are private (0600), and the rest are world-readable
// metadata (0644).
func fileMode(rel string) os.FileMode {
	switch {
	case rel == "install.sh":
		return 0755
	case strings.HasPrefix(rel, "wireguard/"):
		return 0600
	default:
		return 0644
	}
}

// apply runs the staged install.sh under a lease-holding guardian, streaming
// its output. install.sh performs its own verify + custody-gated splice
// (from cfg.KeyPath / /etc/wireguard/agent.key) + apply; the agent does not
// splice. The current process is expected to already be root.
func apply(cfg *Config, staging string, stateLease *stateLease) error {
	scriptPath := filepath.Join(staging, "install.sh")
	if _, err := os.Stat(scriptPath); err != nil {
		return fmt.Errorf("agent: staged install.sh missing: %w", err)
	}
	if err := validateInstallArgs(cfg.InstallArgs); err != nil {
		return err
	}
	cmd, err := newInstallerCommand(stateLease, scriptPath, cfg.InstallArgs)
	if err != nil {
		return err
	}
	cmd.Dir = staging
	stdout := cfg.Stdout
	if stdout == nil {
		stdout = os.Stdout
	}
	stderr := cfg.Stderr
	if stderr == nil {
		stderr = os.Stderr
	}
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("agent: install.sh exit: %w", err)
	}
	return nil
}

// validateInstallArgs is the single closed-set gate for root script arguments.
// Run invokes it before loading/fetching anything; apply repeats the same helper
// at the execution boundary as defense in depth.
func validateInstallArgs(args []string) error {
	if len(args) == 0 || (len(args) == 1 && args[0] == "--uninstall") {
		return nil
	}
	return fmt.Errorf("agent: unsupported install.sh arguments %q", args)
}

// recordSuccess persists a successful apply to the state file and POSTs the
// report to the source when it is a Reporter (best-effort). membershipEpoch is the
// verified trust-list epoch this apply locked in (also 0 when keystone is OFF); the
// separate MembershipVerified bit disambiguates a valid signed initial epoch zero.
func recordSuccess(cfg *Config, prev *State, man *manifestInfo, vr *VerifyResult, membershipEpoch int64) error {
	action := LastActionApply
	health := "applied"
	if len(cfg.InstallArgs) == 1 && cfg.InstallArgs[0] == "--uninstall" {
		action = LastActionUninstall
		health = "uninstalled"
	}
	s := &State{
		NodeID:             cfg.NodeID,
		LastCompiledAt:     man.CompiledAt,
		LastChecksum:       man.Checksum,
		LastResult:         LastResultOK,
		LastAction:         action,
		LastSigned:         vr != nil && vr.Signed,
		MembershipEpoch:    membershipEpoch,
		MembershipVerified: len(cfg.OperatorCredPEM) > 0,
		AppliedAt:          time.Now().UTC().Format(compiledAtLayout),
		Health:             health,
	}
	// Preserve the self-update custody state across the apply-state rebuild (plan-9): the
	// health-confirmed AgentVersionFloor must NOT be wiped by a routine apply (or a later signed
	// downgrade could slip below it), and a self-update breadcrumb / abandoned-target memory in
	// flight must survive (a normal apply does not own it — the reconcile/finalize does). Same
	// discipline as MembershipEpoch below.
	if prev != nil {
		s.AgentVersionFloor = prev.AgentVersionFloor
		s.PendingUpdate = prev.PendingUpdate
		s.AbandonedAgentVersion = prev.AbandonedAgentVersion
		s.AbandonedReason = prev.AbandonedReason
		// The membership anti-rollback floor is MONOTONIC: a successful apply must never LOWER it
		// (mirrors recordFailure). A keystone-OFF apply reports membershipEpoch==0 (VerifyMembership
		// is a no-op without a pinned credential), so without this a node that had a keystone-ON
		// floor of E, run once with the keystone disabled, would silently reset its floor to 0 — and
		// then accept a replayed older (E-1) but validly-signed membership once re-enabled. Persist
		// max(membershipEpoch, prev): advance on a real epoch bump, preserve E across a keystone-OFF run.
		if prev.MembershipEpoch > s.MembershipEpoch {
			s.MembershipEpoch = prev.MembershipEpoch
		}
		s.MembershipVerified = s.MembershipVerified || prev.MembershipVerified || prev.MembershipEpoch > 0
	}
	// Structured feedback (plan-1/3): configapply (mirrors Health) + selfupdate (from prev, whose
	// Health still holds a terminal marker this new state resets) + best-effort wireguard. Additive
	// to the existing Health string; regenerated each cycle; not custody state.
	s.Conditions = collectConditionsForAction(prev, true, action, time.Now().UTC())
	return persistAndReport(cfg, s)
}

// recordFailure persists a failed attempt WITHOUT clobbering the last-good
// baseline: LastCompiledAt/LastChecksum keep their prior values (so anti-rollback
// continues to protect the running config), and the failure detail is recorded
// alongside. The candidate's identity is intentionally NOT recorded as the
// last-known-good baseline. When root mutation may have started, prev carries a
// PendingApply write-ahead identity; that record is preserved as the effective
// recovery floor even though only a successful apply advances LastCompiledAt.
func recordFailure(cfg *Config, prev *State, detail string) {
	s := &State{
		LastResult: "error",
		LastError:  detail,
		AppliedAt:  time.Now().UTC().Format(compiledAtLayout),
		Health:     "degraded: keeping last-good",
	}
	if prev != nil {
		s.NodeID = prev.NodeID
		s.LastCompiledAt = prev.LastCompiledAt
		s.LastChecksum = prev.LastChecksum
		s.LastSigned = prev.LastSigned
		s.LastAction = prev.LastAction
		// Keep the membership anti-rollback floor: a failed apply must never lower it,
		// or a rejected older trust-list could be retried successfully afterward.
		s.MembershipEpoch = prev.MembershipEpoch
		s.MembershipVerified = prev.MembershipVerified || prev.MembershipEpoch > 0
		// Likewise preserve the self-update custody state (plan-9): a failed apply must not wipe
		// the health-confirmed AgentVersionFloor, a self-update breadcrumb in flight, or the
		// abandoned-target memory.
		s.AgentVersionFloor = prev.AgentVersionFloor
		s.PendingUpdate = prev.PendingUpdate
		s.PendingApply = prev.PendingApply
		s.AbandonedAgentVersion = prev.AbandonedAgentVersion
		s.AbandonedReason = prev.AbandonedReason
	}
	if s.NodeID == "" {
		s.NodeID = cfg.NodeID
	}
	// Structured feedback (plan-1/3): configapply (degraded) + selfupdate (from prev) + best-effort
	// wireguard. The self-update breadcrumb / abandoned-target memory survive in prev across a failed
	// apply, so the selfupdate condition stays correct even when the apply itself failed.
	s.Conditions = collectConditions(prev, false, time.Now().UTC())
	_ = persistAndReport(cfg, s)
}

// persistAndReport writes state to disk and, when the source is a Reporter, POSTs
// the same payload. A persistence error is returned because reporting an apply
// success without durable local anti-rollback floors would be dishonest. Remote
// reporting remains best-effort after local persistence succeeds.
func persistAndReport(cfg *Config, s *State) error {
	save := SaveState
	if cfg.StateSaver != nil {
		save = cfg.StateSaver
	}
	if err := save(cfg.StateDir, s); err != nil {
		fmt.Fprintf(stderrOf(cfg), "agent: warning: persist state: %v\n", err)
		return err
	}
	if reporter, ok := cfg.Source.(Reporter); ok {
		payload, err := json.Marshal(s)
		if err == nil {
			if err := reporter.Report(cfg.NodeID, payload); err != nil {
				fmt.Fprintf(stderrOf(cfg), "agent: warning: report: %v\n", err)
			}
		}
	}
	return nil
}

// stderrOf returns the configured stderr or os.Stderr.
func stderrOf(cfg *Config) io.Writer {
	if cfg.Stderr != nil {
		return cfg.Stderr
	}
	return os.Stderr
}
