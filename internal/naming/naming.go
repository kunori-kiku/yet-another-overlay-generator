// Package naming holds the canonical artifact-naming functions defined by
// Spec D (docs/spec/artifacts/naming.md). It is a leaf package: it imports
// ONLY the Go standard library.
//
// The reason this package exists is the import graph. The export ZIP writer
// (internal/api + internal/artifacts), the deploy-script renderer
// (internal/renderer), the peer-derivation compiler (internal/compiler), and
// the semantic validator (internal/validator) all need to agree, byte for
// byte, on the installer file name and the WireGuard interface name. The
// compiler already imports the validator, so the validator cannot import the
// compiler to reuse a name function living there — that would be an import
// cycle. Hoisting the canonical name functions into this dependency-free leaf
// package lets every layer import the single source of truth without any
// cycle, eliminating the divergent duplicate implementations that Spec D's
// uniqueness invariants (N1–N3) require to be impossible.
package naming

import (
	"crypto/sha256"
	"fmt"
	"regexp"
	"strings"
)

// multiHyphen collapses every run of two or more consecutive hyphens to one.
var multiHyphen = regexp.MustCompile(`-{2,}`)

// SafeInstallerFileName maps a node name to the canonical installer file name.
//
// This is the single source of truth required by Spec D
// (docs/spec/artifacts/naming.md, "Canonical installer name"): the ZIP entry
// name written by the export endpoint and the file name the deploy script
// looks up and uploads MUST both come from this one function applied to the
// same node name. Neither side may apply its own sanitization, truncation, or
// suffixing.
//
// The algorithm, in order:
//  1. Lowercase the node name.
//  2. Map every rune outside [a-z0-9-_] to a hyphen.
//  3. Collapse every run of two or more consecutive hyphens to a single one.
//  4. Trim leading and trailing hyphens.
//  5. If the result is empty, substitute the literal "node".
//  6. Append the suffix ".install.sh".
//
// For example, "Web 1" and "web-1" both produce "web-1.install.sh", and
// "  ***  " produces "node.install.sh".
func SafeInstallerFileName(nodeName string) string {
	safe := strings.ToLower(nodeName)
	safe = strings.Map(func(r rune) rune {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' || r == '_' {
			return r
		}
		return '-'
	}, safe)
	// Collapse multiple consecutive hyphens
	safe = multiHyphen.ReplaceAllString(safe, "-")
	safe = strings.Trim(safe, "-")
	if safe == "" {
		safe = "node"
	}
	return safe + ".install.sh"
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
//     to a hyphen. Unlike SafeInstallerFileName, the interface cleaner does
//     NOT preserve "_"; underscore maps to a hyphen.
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
// name, then map every rune outside [a-z0-9-] to a hyphen. Unlike
// SafeInstallerFileName, this cleaner does NOT preserve "_"; underscore maps to
// a hyphen. It is the single rune map for both WgInterfaceName and
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
