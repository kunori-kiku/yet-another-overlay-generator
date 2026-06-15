package controller

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"time"
)

// Audit-log bound (plan-6). The append-only log is capped so a long-lived controller (or
// an abuse pattern that appends many entries) cannot grow it without bound. The cap uses
// hysteresis: the log is allowed to reach auditRotateAt entries, then a single rotation
// trims it back to the most-recent auditRetain. The gap between the two is what amortizes
// the rotation — a full rewrite happens once per (auditRotateAt-auditRetain) appends, so
// steady-state appends never rewrite the whole file (FileStore appends one line; MemStore
// appends to a slice). Both Store implementations share these constants so their bound —
// and the shared compat test — stay identical.
const (
	auditRetain   = 10000
	auditRotateAt = 12000
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
//
// Anchoring: the chain is verified relative to the FIRST entry's PrevHash, not the empty
// genesis, so a bounded log that has rotated out its oldest entries (plan-6) — whose first
// retained entry carries a non-empty PrevHash — still verifies its retained window instead
// of false-positiving at index 0. Trade-off (honest): prefix-truncation of an un-rotated log
// is therefore NO LONGER detected (the trimmed window verifies clean). That is acceptable
// under the operational-only guarantee above — an actor with store write access can re-forge
// the chain forward regardless, so truncation was never cryptographically prevented; a clean
// result must not be read as proof the head was not trimmed.
func VerifyAuditChain(entries []AuditEntry) int {
	if len(entries) == 0 {
		return -1
	}
	prev := entries[0].PrevHash
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
