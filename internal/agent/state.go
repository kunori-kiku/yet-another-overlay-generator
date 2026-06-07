package agent

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// compiledAtLayout is the timestamp format the export path writes into
// manifest.json's compiled_at field (internal/artifacts/export.go uses
// time.Time.Format("2006-01-02T15:04:05Z")). The agent parses it back for the
// anti-rollback comparison.
const compiledAtLayout = "2006-01-02T15:04:05Z"

// DefaultStateDir is where the agent persists last-applied bookkeeping. State is
// host-local mutable state (not a secret) and lives outside the bundle so it
// survives re-applies.
const DefaultStateDir = "/var/lib/yaog-agent"

// stateFileName is the file under the state dir holding the agent's persisted
// last-applied record.
const stateFileName = "state.json"

// State is the agent's persisted bookkeeping: what it last applied and the
// outcome. It backs both anti-rollback (LastCompiledAt) and reporting.
type State struct {
	// NodeID is the identity this state belongs to (sanity check on reuse).
	NodeID string `json:"node_id"`
	// LastCompiledAt is the manifest compiled_at of the last successfully applied
	// bundle, in compiledAtLayout. Empty means nothing applied yet.
	LastCompiledAt string `json:"last_compiled_at"`
	// LastChecksum is the manifest checksum of the last applied bundle.
	LastChecksum string `json:"last_checksum"`
	// LastResult is "ok" or "error".
	LastResult string `json:"last_result"`
	// LastError is the failure detail when LastResult is "error".
	LastError string `json:"last_error,omitempty"`
	// LastSigned records whether the last applied bundle was signature-verified.
	LastSigned bool `json:"last_signed"`
	// AppliedAt is the agent-side wall-clock time of the last apply attempt.
	AppliedAt string `json:"applied_at"`
	// Health is a short human-readable health line.
	Health string `json:"health"`
}

// statePath returns the state file path inside stateDir.
func statePath(stateDir string) string {
	return filepath.Join(stateDir, stateFileName)
}

// LoadState reads the agent state from stateDir. A missing file is NOT an error:
// it returns a zero State (nothing applied yet), which is the first-run case.
func LoadState(stateDir string) (*State, error) {
	data, err := os.ReadFile(statePath(stateDir))
	if err != nil {
		if os.IsNotExist(err) {
			return &State{}, nil
		}
		return nil, fmt.Errorf("agent: read state: %w", err)
	}
	var s State
	if err := json.Unmarshal(data, &s); err != nil {
		return nil, fmt.Errorf("agent: parse state: %w", err)
	}
	return &s, nil
}

// SaveState writes the agent state into stateDir (creating it 0700), via a
// temp-file rename so a crash cannot leave a truncated state file. State is
// world-unreadable (0600) as a matter of hygiene even though it holds no secret.
func SaveState(stateDir string, s *State) error {
	if err := os.MkdirAll(stateDir, 0700); err != nil {
		return fmt.Errorf("agent: create state dir: %w", err)
	}
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return fmt.Errorf("agent: marshal state: %w", err)
	}
	p := statePath(stateDir)
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, data, 0600); err != nil {
		return fmt.Errorf("agent: write state: %w", err)
	}
	if err := os.Rename(tmp, p); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("agent: install state: %w", err)
	}
	return nil
}

// manifestInfo is the subset of manifest.json the agent needs for anti-rollback
// and reporting.
type manifestInfo struct {
	NodeID     string `json:"node_id"`
	CompiledAt string `json:"compiled_at"`
	Checksum   string `json:"checksum"`
}

// parseManifest extracts the rollback-relevant fields from manifest.json.
func parseManifest(data []byte) (*manifestInfo, error) {
	var m manifestInfo
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("agent: parse manifest.json: %w", err)
	}
	if strings.TrimSpace(m.CompiledAt) == "" {
		return nil, fmt.Errorf("agent: manifest.json has no compiled_at")
	}
	return &m, nil
}

// CheckRollback compares the candidate bundle's compiled_at against the
// last-applied value in prev. It refuses (returns an error) when the candidate is
// STRICTLY OLDER than the last applied bundle — a rollback. An equal timestamp is
// allowed (idempotent re-apply of the same generation), and a newer one is the
// normal forward case. A first-run state (empty LastCompiledAt) always allows.
//
// An unparseable last-applied timestamp is treated as "no baseline" rather than a
// hard error so a corrupted state file cannot permanently wedge the agent; the
// candidate must still parse.
func CheckRollback(prev *State, candidateCompiledAt string) error {
	cand, err := time.Parse(compiledAtLayout, strings.TrimSpace(candidateCompiledAt))
	if err != nil {
		return fmt.Errorf("agent: candidate compiled_at %q unparseable: %w", candidateCompiledAt, err)
	}
	if prev == nil || strings.TrimSpace(prev.LastCompiledAt) == "" {
		return nil
	}
	last, err := time.Parse(compiledAtLayout, strings.TrimSpace(prev.LastCompiledAt))
	if err != nil {
		// Corrupt baseline: allow forward progress rather than wedging.
		return nil
	}
	if cand.Before(last) {
		return fmt.Errorf("agent: anti-rollback: candidate compiled_at %s is older than last applied %s; refusing",
			cand.Format(compiledAtLayout), last.Format(compiledAtLayout))
	}
	return nil
}
