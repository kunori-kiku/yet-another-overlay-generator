// Package naming holds the portable node-ID and WireGuard interface naming
// contracts defined by Spec D (docs/spec/artifacts/naming.md). It is a leaf
// package: it imports only the Go standard library, so validators, exporters,
// renderers, and compilers can share these rules without an import cycle.
package naming

import (
	"crypto/sha256"
	"fmt"
	"strings"
)

// MaxPortableNodeIDLength leaves enough room for the deploy renderer's
// "yaog-<id>-XXXXXXXX" remote staging component under the 255-byte component
// limit common to Linux and Windows filesystems. Node IDs are ASCII by contract,
// so byte and character length are identical here.
const MaxPortableNodeIDLength = 240

// ValidPortableNodeID reports whether id is safe as the single canonical
// per-node directory key on Linux and Windows. In addition to the restricted
// ASCII charset, it rejects Windows device basenames and trailing dots, root
// project-helper collisions, and IDs too long for the remote staging template.
func ValidPortableNodeID(id string) bool {
	if id == "" || id == "." || id == ".." || len(id) > MaxPortableNodeIDLength || strings.HasSuffix(id, ".") {
		return false
	}
	for _, r := range id {
		if !((r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '.' || r == '_' || r == '-') {
			return false
		}
	}

	folded := PortableNodeIDKey(id)
	// Project-level helpers share the export root with per-node directories.
	if folded == "deploy-all.sh" || folded == "deploy-all.ps1" {
		return false
	}
	base := folded
	if dot := strings.IndexByte(base, '.'); dot >= 0 {
		base = base[:dot]
	}
	switch base {
	case "con", "prn", "aux", "nul":
		return false
	}
	if len(base) == 4 && (strings.HasPrefix(base, "com") || strings.HasPrefix(base, "lpt")) && base[3] >= '1' && base[3] <= '9' {
		return false
	}
	return true
}

// PortableNodeIDKey is the collision key for node directories on the supported
// case-insensitive Windows export/deploy surface. IDs that share this key cannot
// coexist even though a Linux map or filesystem could distinguish them.
func PortableNodeIDKey(id string) string {
	return strings.ToLower(id)
}

// WgInterfaceName maps a remote peer's node name to its WireGuard interface
// name, as defined by Spec D (docs/spec/artifacts/naming.md, "WireGuard
// interface-name algorithm"). It is the single authority for interface names;
// the compiler stamps the result onto PeerInfo.InterfaceName during peer
// derivation, and every consumer (ZIP config file names, Babel interface
// lines, deploy teardown, frontend lookups) MUST use the stamped value.
//
// The Linux kernel limits interface names to 15 characters, so the algorithm
// has a short path and a hashed long path. Given a remote node name
// remoteName:
//  1. clean := lowercase(remoteName), then map every rune outside [a-z0-9-]
//     to a hyphen. The interface cleaner does not preserve "_"; underscore maps to a hyphen.
//  2. name := "wg-" + clean.
//  3. Short path: if len(name) <= 15, return name.
//  4. Long path (>15 chars): return "wg-" + clean[:8] + sha256(remoteName)[:4],
//     i.e. the "wg-" prefix, the first 8 cleaned characters, and the first 4
//     hex characters of sha256(remoteName), for a total of 3 + 8 + 4 = 15
//     characters. The 8-character clean slice is bounded by the actual cleaned
//     length when it is shorter than 8 (a defensive guard that does not arise
//     on the long path). The hash suffix exists so that two distinct names
//     sharing a long common prefix do not truncate to the same name; plain
//     truncation is therefore wrong for names longer than 12 characters.
func WgInterfaceName(remoteName string) string {
	clean := cleanInterfaceName(remoteName)

	name := "wg-" + clean
	if len(name) <= 15 {
		return name
	}

	// For names that would exceed 15 chars, use a hash suffix to avoid
	// deterministic conflicts from truncation. sha256.Sum256 computes the full
	// hash but we only need 4 hex chars (16 bits); the full computation is
	// unavoidable with the standard library.
	const maxLen = 15
	const prefix = "wg-"
	const hashSuffixLen = 4 // 4 hex chars, low collision probability

	hash := fmt.Sprintf("%x", sha256.Sum256([]byte(remoteName)))
	// remainForClean = 15 - 3 - 4 = 8; the min/max guards are defensive.
	remainForClean := maxLen - len(prefix) - hashSuffixLen
	if remainForClean > len(clean) {
		remainForClean = len(clean)
	}
	return prefix + clean[:remainForClean] + hash[:hashSuffixLen]
}

// WgInterfaceNameForEdge maps a per-peer link to its WireGuard interface name,
// edge-aware so that a node hosting several interfaces toward the SAME remote
// peer (parallel links) gets a distinct, stable name per backup link. It is the
// single authority for backup-link interface names; both the compiler and the
// validator MUST obtain them here. See Spec D (docs/spec/artifacts/naming.md,
// "Edge-aware names for backup links").
//
//   - backup == false (primary link / primary class): returns
//     WgInterfaceName(remoteName) byte-identical, so deployed fleets see zero
//     interface renames.
//   - backup == true: returns "wg-" + clean[:8] + sha256(remoteName+"|"+edgeID)[:4]
//     UNCONDITIONALLY (the long-path shape with no short path), folding the edge
//     ID into the hash input so two backups toward the same peer differ in their
//     4-hex suffix while staying within the 3 + 8 + 4 = 15 char budget. Stable
//     across recompiles because the edge ID is stable. The 8-char clean slice is
//     bounded by the actual cleaned length when it is shorter than 8.
//
// The result is always <= 15 characters.
func WgInterfaceNameForEdge(remoteName, edgeID string, backup bool) string {
	if !backup {
		return WgInterfaceName(remoteName)
	}

	clean := cleanInterfaceName(remoteName)

	const maxLen = 15
	const prefix = "wg-"
	const hashSuffixLen = 4 // 4 hex chars, low collision probability

	// Fold the edge ID into the hash input so parallel backups toward the same
	// remote peer diverge in the 4-hex suffix.
	hash := fmt.Sprintf("%x", sha256.Sum256([]byte(remoteName+"|"+edgeID)))
	// remainForClean = 15 - 3 - 4 = 8; bound by the cleaned length when shorter.
	remainForClean := maxLen - len(prefix) - hashSuffixLen
	if remainForClean > len(clean) {
		remainForClean = len(clean)
	}
	return prefix + clean[:remainForClean] + hash[:hashSuffixLen]
}

// cleanInterfaceName applies the shared interface-name cleaner: lowercase the
// name, then map every rune outside [a-z0-9-] to a hyphen. It does not preserve
// "_"; underscore maps to a hyphen. It is the single rune map for both WgInterfaceName and
// WgInterfaceNameForEdge so the two never diverge.
func cleanInterfaceName(remoteName string) string {
	clean := strings.ToLower(remoteName)
	return strings.Map(func(r rune) rune {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' {
			return r
		}
		return '-'
	}, clean)
}
