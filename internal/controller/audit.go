package controller

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"time"
)

// canonicalAuditBytes returns the stable byte encoding of an audit entry's content
// (every field EXCEPT its own Hash), used as the SHA-256 input for the hash chain.
// The field order and the RFC3339Nano/UTC timestamp formatting are fixed so the
// hash is deterministic across processes and Store implementations.
func canonicalAuditBytes(e AuditEntry) []byte {
	return []byte(fmt.Sprintf("%d\n%s\n%s\n%s\n%s\n%s\n",
		e.Seq,
		e.Timestamp.UTC().Format(time.RFC3339Nano),
		e.Actor,
		e.Action,
		e.NodeID,
		e.PrevHash,
	))
}

// chainAudit links a new entry into the chain: it sets PrevHash to the prior
// entry's Hash (empty for the first entry) and computes this entry's Hash over its
// canonical bytes (which include PrevHash). Seq and Timestamp must already be set
// by the caller. Store implementations call this in AppendAudit.
func chainAudit(e AuditEntry, prevHash string) AuditEntry {
	e.PrevHash = prevHash
	sum := sha256.Sum256(canonicalAuditBytes(e))
	e.Hash = hex.EncodeToString(sum[:])
	return e
}

// VerifyAuditChain reports the index of the first entry that breaks the chain
// (its PrevHash does not match the prior entry's Hash, or its Hash does not
// recompute), or -1 if the whole chain is intact. The chain is tamper-EVIDENT for
// operational visibility only: an actor with write access to the backing store can
// recompute every Hash, so this is not a cryptographic anti-tamper guarantee
// (that is Plan 5). See the AuditEntry doc in store.go.
func VerifyAuditChain(entries []AuditEntry) int {
	prev := ""
	for i, e := range entries {
		if e.PrevHash != prev {
			return i
		}
		if chainAudit(e, prev).Hash != e.Hash {
			return i
		}
		prev = e.Hash
	}
	return -1
}
