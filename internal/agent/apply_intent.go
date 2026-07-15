package agent

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"time"
)

// savePendingApply is a narrow failure-injection seam for tests. Production always
// points at SaveState: an embedder's final StateSaver must not be able to bypass the
// write-ahead record that authorizes root-side mutation.
var savePendingApply = SaveState

// validatePendingApply checks the durable record before any fetch, verification, self-update,
// or root-side apply. In addition to rejecting corrupt records, it keeps an interrupted
// operation under the same configured trust anchors. Anchor rotation remains an explicit
// reprovisioning operation; it cannot happen accidentally in the middle of recovery.
func validatePendingApply(cfg *Config, state *State) error {
	if state == nil || state.PendingApply == nil {
		return nil
	}
	p := state.PendingApply
	if _, err := time.Parse(compiledAtLayout, p.CompiledAt); err != nil {
		return fmt.Errorf("agent: pending apply has invalid compiled_at %q: %w", p.CompiledAt, err)
	}
	if _, err := time.Parse(compiledAtLayout, p.StartedAt); err != nil {
		return fmt.Errorf("agent: pending apply has invalid started_at %q: %w", p.StartedAt, err)
	}
	if p.Action != LastActionApply && p.Action != LastActionUninstall {
		return fmt.Errorf("agent: pending apply has invalid action %q", p.Action)
	}
	if !validSHA256Hex(p.BundleSHA256) {
		return fmt.Errorf("agent: pending apply has invalid bundle_sha256 %q", p.BundleSHA256)
	}
	if p.MembershipEpoch < 0 {
		return fmt.Errorf("agent: pending apply has negative membership epoch %d", p.MembershipEpoch)
	}

	if p.SigningKeyFingerprint != "" {
		if !validSHA256Hex(p.SigningKeyFingerprint) {
			return fmt.Errorf("agent: pending apply has invalid signing-key fingerprint %q", p.SigningKeyFingerprint)
		}
		if len(cfg.PinnedPubPEM) == 0 {
			return fmt.Errorf("agent: pending apply used pinned signing key %s but no signing key is configured; refusing trust downgrade", shortFingerprint(p.SigningKeyFingerprint))
		}
		got, err := credFingerprint(cfg.PinnedPubPEM)
		if err != nil {
			return fmt.Errorf("agent: parse signing key while recovering pending apply: %w", err)
		}
		if !strings.EqualFold(got, p.SigningKeyFingerprint) {
			return fmt.Errorf("agent: pending apply used signing key %s but configured key is %s; refusing anchor change during recovery", shortFingerprint(p.SigningKeyFingerprint), shortFingerprint(got))
		}
	}

	if p.MembershipVerified {
		if !validSHA256Hex(p.OperatorCredentialFingerprint) {
			return fmt.Errorf("agent: pending verified apply has invalid operator-credential fingerprint %q", p.OperatorCredentialFingerprint)
		}
		if len(cfg.OperatorCredPEM) == 0 {
			return fmt.Errorf("agent: pending apply verified keystone epoch %d but no operator credential is configured; refusing trust downgrade", p.MembershipEpoch)
		}
		got, err := credFingerprint(cfg.OperatorCredPEM)
		if err != nil {
			return fmt.Errorf("agent: parse operator credential while recovering pending apply: %w", err)
		}
		if !strings.EqualFold(got, p.OperatorCredentialFingerprint) ||
			cfg.OperatorCredAlg != p.OperatorCredentialAlg ||
			cfg.OperatorRPID != p.OperatorRPID ||
			cfg.OperatorOrigin != p.OperatorOrigin {
			return fmt.Errorf("agent: pending apply used operator credential %s (%s, rpid %q, origin %q) but the configured anchor/binding changed; refusing recovery under a different keystone",
				shortFingerprint(p.OperatorCredentialFingerprint), p.OperatorCredentialAlg, p.OperatorRPID, p.OperatorOrigin)
		}
	} else if p.OperatorCredentialFingerprint != "" || p.OperatorCredentialAlg != "" || p.OperatorRPID != "" || p.OperatorOrigin != "" {
		return fmt.Errorf("agent: pending non-keystone apply carries operator-credential metadata")
	}

	return nil
}

// effectiveMembershipFloor includes an unresolved root mutation. A crash after
// install.sh may have put that candidate on the host, so a subsequent verification
// must never accept a membership epoch below either the last-known-good state or the
// write-ahead intent.
func effectiveMembershipFloor(state *State) int64 {
	if state == nil {
		return 0
	}
	floor := state.MembershipEpoch
	if state.PendingApply != nil && state.PendingApply.MembershipEpoch > floor {
		floor = state.PendingApply.MembershipEpoch
	}
	return floor
}

// effectiveRollbackState returns a shallow state copy whose compiled-at floor is
// the newest of the last-known-good apply and the unresolved write-ahead intent.
// The caller only reads this copy through CheckRollback.
func effectiveRollbackState(state *State) *State {
	if state == nil || state.PendingApply == nil {
		return state
	}
	out := *state
	pendingTime, pendingErr := time.Parse(compiledAtLayout, state.PendingApply.CompiledAt)
	lastTime, lastErr := time.Parse(compiledAtLayout, state.LastCompiledAt)
	if pendingErr == nil && (lastErr != nil || pendingTime.After(lastTime)) {
		out.LastCompiledAt = state.PendingApply.CompiledAt
	}
	return &out
}

// validateCandidateAgainstPending prevents an unresolved operation from changing
// semantic action, or from substituting different verified root bytes at the same
// compiled-at floor. A strictly newer, fully verified candidate of the same action may
// supersede it and will receive its own durable intent before execution.
func validateCandidateAgainstPending(state *State, man *manifestInfo, files map[string][]byte, action string) error {
	if state == nil || state.PendingApply == nil {
		return nil
	}
	p := state.PendingApply
	if action != p.Action {
		return fmt.Errorf("agent: unresolved %s from %s exists; refusing %s until the same action converges", p.Action, p.CompiledAt, action)
	}
	candidateTime, err := time.Parse(compiledAtLayout, man.CompiledAt)
	if err != nil {
		return err // CheckRollback will normally have returned this first.
	}
	pendingTime, err := time.Parse(compiledAtLayout, p.CompiledAt)
	if err != nil {
		return err // validatePendingApply will normally have returned this first.
	}
	if candidateTime.Equal(pendingTime) {
		digest, err := verifiedBundleDigest(files)
		if err != nil {
			return err
		}
		if !strings.EqualFold(digest, p.BundleSHA256) || man.Checksum != p.ManifestChecksum {
			return fmt.Errorf("agent: unresolved apply at compiled_at %s is bundle %s; refusing different same-version candidate %s", p.CompiledAt, shortFingerprint(p.BundleSHA256), shortFingerprint(digest))
		}
	}
	return nil
}

// persistPendingApply writes (or confirms) the root-mutation intent. Exact retries
// reuse the existing durable record. A newer candidate first replaces it atomically,
// so there is never a root-side interval whose effective floors exist only in memory.
func persistPendingApply(cfg *Config, prev *State, man *manifestInfo, files map[string][]byte, membershipEpoch int64, action string) (*State, error) {
	digest, err := verifiedBundleDigest(files)
	if err != nil {
		return nil, err
	}

	next := &State{}
	if prev != nil {
		copyState := *prev
		next = &copyState
	}
	pending := &PendingApply{
		CompiledAt:         man.CompiledAt,
		ManifestChecksum:   man.Checksum,
		BundleSHA256:       digest,
		Action:             action,
		MembershipEpoch:    membershipEpoch,
		MembershipVerified: len(cfg.OperatorCredPEM) > 0,
		StartedAt:          time.Now().UTC().Format(compiledAtLayout),
	}
	if len(cfg.PinnedPubPEM) > 0 {
		pending.SigningKeyFingerprint, err = credFingerprint(cfg.PinnedPubPEM)
		if err != nil {
			return nil, fmt.Errorf("agent: fingerprint configured signing key: %w", err)
		}
	}
	if pending.MembershipVerified {
		pending.OperatorCredentialFingerprint, err = credFingerprint(cfg.OperatorCredPEM)
		if err != nil {
			return nil, fmt.Errorf("agent: fingerprint configured operator credential: %w", err)
		}
		pending.OperatorCredentialAlg = cfg.OperatorCredAlg
		pending.OperatorRPID = cfg.OperatorRPID
		pending.OperatorOrigin = cfg.OperatorOrigin
	}

	// Preserve the old identity and StartedAt for an exact retry, but re-save it before
	// root execution. A prior SaveState may have published the rename and then returned a
	// directory-sync error, leaving a record that is visible to this process but not proven
	// crash-durable. Reconfirming the same bytes closes that failure seam without pretending
	// this is a new operation.
	if prev != nil && samePendingApply(prev.PendingApply, pending) {
		if err := savePendingApply(cfg.StateDir, prev); err != nil {
			return nil, fmt.Errorf("agent: re-persist pending apply before install.sh retry: %w", err)
		}
		return prev, nil
	}

	next.NodeID = cfg.NodeID
	next.PendingApply = pending
	next.LastResult = LastResultPending
	next.LastError = ""
	next.AppliedAt = pending.StartedAt
	next.Health = "apply in progress; last-good retained"
	next.Conditions = collectConditionsForAction(prev, false, action, time.Now().UTC())
	if err := savePendingApply(cfg.StateDir, next); err != nil {
		return nil, fmt.Errorf("agent: persist pending apply before install.sh: %w", err)
	}
	return next, nil
}

func samePendingApply(a, b *PendingApply) bool {
	if a == nil || b == nil {
		return false
	}
	return a.CompiledAt == b.CompiledAt &&
		a.ManifestChecksum == b.ManifestChecksum &&
		strings.EqualFold(a.BundleSHA256, b.BundleSHA256) &&
		a.Action == b.Action &&
		a.MembershipEpoch == b.MembershipEpoch &&
		a.MembershipVerified == b.MembershipVerified &&
		strings.EqualFold(a.SigningKeyFingerprint, b.SigningKeyFingerprint) &&
		strings.EqualFold(a.OperatorCredentialFingerprint, b.OperatorCredentialFingerprint) &&
		a.OperatorCredentialAlg == b.OperatorCredentialAlg &&
		a.OperatorRPID == b.OperatorRPID &&
		a.OperatorOrigin == b.OperatorOrigin
}

func verifiedBundleDigest(files map[string][]byte) (string, error) {
	checksums, ok := files["checksums.sha256"]
	if !ok {
		return "", fmt.Errorf("agent: verified bundle has no checksums.sha256")
	}
	sum := sha256.Sum256(checksums)
	return hex.EncodeToString(sum[:]), nil
}

func validSHA256Hex(value string) bool {
	if len(value) != sha256.Size*2 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func shortFingerprint(value string) string {
	if len(value) <= 12 {
		return value
	}
	return value[:12]
}
