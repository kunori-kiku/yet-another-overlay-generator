package agent

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
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
	// StagingDir is where the verified bundle is materialized before install.sh
	// runs. When empty a fresh temp dir is created per Run.
	StagingDir string
	// Stdout/Stderr receive install.sh's streamed output. When nil the process
	// stdio is used.
	Stdout io.Writer
	Stderr io.Writer
}

// RunResult summarizes one Run for the caller (and the status report).
type RunResult struct {
	// Applied is true when install.sh ran and exited 0.
	Applied bool
	// CompiledAt is the manifest compiled_at of the bundle that was applied (or
	// considered).
	CompiledAt string
	// Checksum is the manifest checksum of that bundle.
	Checksum string
	// Verify is the verification outcome (signed/hash-only, file count).
	Verify *VerifyResult
	// StagingDir is where the bundle was materialized.
	StagingDir string
}

// Run executes the full control loop: pull -> verify -> anti-rollback -> apply ->
// report. It is fail-closed on a NEW apply (verify failure, rollback, bad bundle)
// but degradation-safe: it NEVER tears down a running tunnel. A failure before
// apply simply leaves the last-good configuration in place and returns an error;
// install.sh is only invoked once the Go-side gate has fully passed.
func Run(cfg *Config) (*RunResult, error) {
	if strings.TrimSpace(cfg.NodeID) == "" {
		return nil, fmt.Errorf("agent: empty node id")
	}
	if cfg.Source == nil {
		return nil, fmt.Errorf("agent: nil source")
	}

	// Load prior state up front; needed for anti-rollback and so a failure can be
	// recorded without losing the last-good baseline.
	prev, err := LoadState(cfg.StateDir)
	if err != nil {
		return nil, err
	}

	// 1. pull
	files, err := cfg.Source.Fetch(cfg.NodeID)
	if err != nil {
		// Source unreachable: degrade — keep last-good, record the failure, do not
		// touch the running tunnel.
		recordFailure(cfg, prev, fmt.Sprintf("fetch failed: %v", err))
		return nil, fmt.Errorf("agent: pull: %w", err)
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

	res := &RunResult{CompiledAt: man.CompiledAt, Checksum: man.Checksum}

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
	}, prev.MembershipEpoch)
	if err != nil {
		recordFailure(cfg, prev, fmt.Sprintf("membership verify failed: %v", err))
		return res, fmt.Errorf("agent: membership verify: %w", err)
	}

	// 3. anti-rollback. NOTE: compiled_at comes from manifest.json, which export deliberately
	// leaves OUT of the signed/checksummed set, so this stub only guards against an honest source
	// accidentally serving a stale bundle — NOT an active attacker/MITM, who could forge compiled_at
	// to force a rollback to any previously signed bundle. Attacker-resistant anti-rollback (a signed
	// version/generation bound into the bundle) is a Phase 2/3 item (docs/spec/controller/agent.md).
	if err := CheckRollback(prev, man.CompiledAt); err != nil {
		recordFailure(cfg, prev, err.Error())
		return res, err
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

	// 5. apply (run staged install.sh as the current root process)
	if err := apply(cfg, staging); err != nil {
		recordFailure(cfg, prev, fmt.Sprintf("install.sh failed: %v", err))
		return res, fmt.Errorf("agent: apply: %w", err)
	}
	res.Applied = true

	// 6. report (record success, POST best-effort)
	recordSuccess(cfg, man, vr, membershipEpoch)
	return res, nil
}

// stage materializes the verified bundle into a staging directory, preserving
// relative paths. The dir is 0700; install.sh is 0755; everything else 0600
// except world-safe metadata (manifest/README/checksums/pubkey) at 0644. It
// returns a cleanup func that removes the staging dir (nil when StagingDir was
// operator-supplied — operators may want to inspect it).
func stage(cfg *Config, files map[string][]byte) (string, func(), error) {
	var dir string
	var cleanup func()
	if strings.TrimSpace(cfg.StagingDir) != "" {
		dir = cfg.StagingDir
		if err := os.MkdirAll(dir, 0700); err != nil {
			return "", nil, fmt.Errorf("agent: create staging dir: %w", err)
		}
	} else {
		tmp, err := os.MkdirTemp("", "yaog-agent-stage-")
		if err != nil {
			return "", nil, fmt.Errorf("agent: create staging dir: %w", err)
		}
		dir = tmp
		cleanup = func() { _ = os.RemoveAll(tmp) }
	}

	for rel, content := range files {
		// Defense against a malicious source returning escaping paths: reject any
		// path that is absolute or climbs out of the staging dir.
		clean := filepath.Clean(rel)
		if filepath.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
			return "", cleanup, fmt.Errorf("agent: unsafe bundle path %q", rel)
		}
		dst := filepath.Join(dir, clean)
		if err := os.MkdirAll(filepath.Dir(dst), 0700); err != nil {
			return "", cleanup, fmt.Errorf("agent: mkdir for %s: %w", rel, err)
		}
		if err := os.WriteFile(dst, content, fileMode(clean)); err != nil {
			return "", cleanup, fmt.Errorf("agent: write %s: %w", rel, err)
		}
	}
	return dir, cleanup, nil
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

// apply runs the staged install.sh as the current process via `bash <path>`,
// streaming its output. install.sh performs its own verify + custody-gated splice
// (from cfg.KeyPath / /etc/wireguard/agent.key) + apply; the agent does not
// splice. The current process is expected to already be root.
func apply(cfg *Config, staging string) error {
	scriptPath := filepath.Join(staging, "install.sh")
	if _, err := os.Stat(scriptPath); err != nil {
		return fmt.Errorf("agent: staged install.sh missing: %w", err)
	}
	cmd := exec.Command("bash", scriptPath)
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

// recordSuccess persists a successful apply to the state file and POSTs the
// report to the source when it is a Reporter (best-effort). membershipEpoch is the
// verified trust-list epoch this apply locked in (0 when keystone is OFF); it becomes
// the new anti-rollback floor for the next VerifyMembership.
func recordSuccess(cfg *Config, man *manifestInfo, vr *VerifyResult, membershipEpoch int64) {
	s := &State{
		NodeID:          cfg.NodeID,
		LastCompiledAt:  man.CompiledAt,
		LastChecksum:    man.Checksum,
		LastResult:      "ok",
		LastSigned:      vr != nil && vr.Signed,
		MembershipEpoch: membershipEpoch,
		AppliedAt:       time.Now().UTC().Format(compiledAtLayout),
		Health:          "applied",
	}
	persistAndReport(cfg, s)
}

// recordFailure persists a failed attempt WITHOUT clobbering the last-good
// baseline: LastCompiledAt/LastChecksum keep their prior values (so anti-rollback
// continues to protect the running config), and the failure detail is recorded
// alongside. The candidate's identity is intentionally NOT recorded as the
// baseline — only a successful apply advances it.
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
		// Keep the membership anti-rollback floor: a failed apply must never lower it,
		// or a rejected older trust-list could be retried successfully afterward.
		s.MembershipEpoch = prev.MembershipEpoch
	}
	if s.NodeID == "" {
		s.NodeID = cfg.NodeID
	}
	persistAndReport(cfg, s)
}

// persistAndReport writes state to disk and, when the source is a Reporter, POSTs
// the same payload. Reporting is best-effort: failures are written to stderr but
// never surface as a Run error.
func persistAndReport(cfg *Config, s *State) {
	if err := SaveState(cfg.StateDir, s); err != nil {
		fmt.Fprintf(stderrOf(cfg), "agent: warning: persist state: %v\n", err)
	}
	if reporter, ok := cfg.Source.(Reporter); ok {
		payload, err := json.Marshal(s)
		if err == nil {
			if err := reporter.Report(cfg.NodeID, payload); err != nil {
				fmt.Fprintf(stderrOf(cfg), "agent: warning: report: %v\n", err)
			}
		}
	}
}

// stderrOf returns the configured stderr or os.Stderr.
func stderrOf(cfg *Config) io.Writer {
	if cfg.Stderr != nil {
		return cfg.Stderr
	}
	return os.Stderr
}
