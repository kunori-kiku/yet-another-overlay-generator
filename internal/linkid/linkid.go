// Package linkid is the single link-identity authority shared by the
// peer-derivation compiler (internal/compiler) and the semantic validator
// (internal/validator). It defines how an Edge maps to a stable link key, so
// that allocation, validator grouping, and write-back all agree on what counts
// as "the same link" — there is exactly one place that encodes the rule.
//
// The reason this package exists is the import graph. Both the compiler and the
// validator must agree, byte for byte, on the link key used to reserve and look
// up per-peer allocations, and the compiler already imports the validator — so
// the validator cannot import the compiler to reuse a link-key function living
// there without an import cycle. Hoisting the link-identity rule into this
// dependency-free leaf package (it imports ONLY internal/model + stdlib) lets
// every layer share the single source of truth, exactly as internal/naming
// does for artifact names. Duplicating these literals in two packages is the
// failure mode this package exists to make impossible.
//
// Spec: docs/spec/compiler/allocation-stability.md
// ("Canonical link key", "Link identity with parallel edges").
package linkid

import "github.com/kunorikiku/yet-another-overlay-generator/internal/model"

// PinKey computes a link's canonical identity: the two node IDs sorted and
// joined with "|". It is direction-agnostic — PinKey(A, B) == PinKey(B, A) —
// so reversing an edge's draw direction does not change its allocation identity
// (I3). The semantics are byte-identical to the historical pinKey that lived in
// internal/compiler/peers.go; this is now the single authority for it.
func PinKey(a, b string) string {
	if a <= b {
		return a + "|" + b
	}
	return b + "|" + a
}

// LinkKey computes the link identity of a single edge, generalizing PinKey from
// the node pair to the edge so that parallel links (a pair carrying one primary
// link plus backups) are distinguishable.
//
//	LinkKey(e) = PinKey(from, to)                  // e.Role != "backup"  (primary class)
//	LinkKey(e) = PinKey(from, to) + "#" + e.ID      // e.Role == "backup"
//
// All enabled non-backup edges of a pair share the same LinkKey (they collapse
// to one primary link under the unify rule); every backup edge gets a distinct
// LinkKey keyed by its own edge ID, so two backups toward the same peer — and a
// backup vs. the pair's primary — never share an allocation. Backups are ALWAYS
// discriminated by edge ID, even when a backup is the pair's only edge, so that
// adding or removing a backup never re-keys any other link (stability property
// 3 in the spec).
//
// Precondition: e is an enabled edge with both FromNodeID and ToNodeID set
// (i.e. it survived schema/semantic validation). nil-safety is NOT a contract
// of this function; callers pass real edges from the validated topology.
func LinkKey(e *model.Edge) string {
	pair := PinKey(e.FromNodeID, e.ToNodeID)
	if e.Role != model.EdgeRoleBackup {
		return pair
	}
	return pair + "#" + e.ID
}

// IsBackup reports whether an edge is a backup link (its own discriminated link
// identity), i.e. e.Role == model.EdgeRoleBackup. Empty role and "primary" are
// both primary class and return false.
func IsBackup(e *model.Edge) bool {
	return e.Role == model.EdgeRoleBackup
}
