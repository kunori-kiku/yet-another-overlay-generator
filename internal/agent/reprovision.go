package agent

import (
	"fmt"
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

	if err := WritePrivateFileAtomic(credPath, newPEM); err != nil {
		return fmt.Errorf("agent: reprovision-keystone: persist credential: %w", err)
	}
	return nil
}
