package agent

import (
	"fmt"
	"os"
	"path/filepath"
)

// ReprovisionKeystone atomically rewrites the node's pinned off-host operator credential
// (credPath) to newPEM, after VALIDATING that newPEM parses for alg. It is the node-side
// adoption of a ROTATED keystone: the new public key is supplied OUT OF BAND by the operator
// (a local file or stdin), NEVER fetched from the controller — so this is a guided, single-action
// rename of the existing manual re-provisioning, not an automated trust transfer. The off-host
// anchor stays off-host; nothing here lets a controller change what a node trusts.
//
// Fail-closed: a malformed or empty PEM is REFUSED before any write, so a botched rotation can
// never blank the pin and silently turn the keystone OFF. The write is atomic (temp file in the
// destination dir + rename) at 0600 under a 0700 parent, so a crash mid-write leaves the prior
// pin intact. It performs NO network I/O.
//
// NOTE: the running daemon reads the pinned credential ONCE at process start, so the caller must
// restart yaog-agent after a successful rewrite for it to take effect (the CLI does this).
func ReprovisionKeystone(credPath, alg string, newPEM []byte) error {
	if len(newPEM) == 0 {
		return fmt.Errorf("agent: reprovision-keystone: empty credential PEM")
	}
	// Validate the PEM parses for the declared algorithm BEFORE touching disk — the SAME gate
	// VerifyMembership applies to the pinned anchor, so an unverifiable pin is rejected here
	// rather than silently bricking membership verification on the next poll.
	if _, err := pinnedCredential(MembershipConfig{OperatorCredPEM: newPEM, OperatorCredAlg: alg}); err != nil {
		return fmt.Errorf("agent: reprovision-keystone: refusing to pin an unparsable credential: %w", err)
	}

	dir := filepath.Dir(credPath)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf("agent: reprovision-keystone: create %s: %w", dir, err)
	}
	tmp, err := os.CreateTemp(dir, ".operator-cred-*.tmp")
	if err != nil {
		return fmt.Errorf("agent: reprovision-keystone: temp file: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // best-effort cleanup; a no-op after a successful rename
	if err := tmp.Chmod(0600); err != nil {
		tmp.Close()
		return fmt.Errorf("agent: reprovision-keystone: chmod temp: %w", err)
	}
	if _, err := tmp.Write(newPEM); err != nil {
		tmp.Close()
		return fmt.Errorf("agent: reprovision-keystone: write temp: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("agent: reprovision-keystone: close temp: %w", err)
	}
	if err := os.Rename(tmpName, credPath); err != nil {
		return fmt.Errorf("agent: reprovision-keystone: rename into place: %w", err)
	}
	return nil
}
