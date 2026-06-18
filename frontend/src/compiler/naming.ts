// Artifact-naming authority — the TypeScript mirror of internal/naming/naming.go (Spec D,
// docs/spec/artifacts/naming.md). The ZIP installer name and the WireGuard interface names MUST be
// byte-identical to the Go oracle, so the export bundle and deploy-script lookups agree across the
// Go controller and the TS local engine.
//
// SHA-256 is SYNCHRONOUS (@noble/hashes sha256 + bytesToHex, lowercase hex), matching the synchronous
// crypto/sha256 calls deep in the Go pipeline (naming.go:103, :143). Keeping this a pure sync function
// is load-bearing: the peer-derivation core calls it inline and must not become async.

import { sha256 } from '@noble/hashes/sha2.js';
import { bytesToHex } from '@noble/hashes/utils.js';

// utf8 encodes a string to its UTF-8 bytes, mirroring Go's []byte(s) on a string (which is the UTF-8
// encoding). sha256 hashes over these exact bytes.
const utf8 = new TextEncoder();

// sha256Hex returns the lowercase hex SHA-256 of the UTF-8 bytes of s, mirroring Go's
// fmt.Sprintf("%x", sha256.Sum256([]byte(s))). bytesToHex emits lowercase hex (matching %x).
function sha256Hex(s: string): string {
  return bytesToHex(sha256(utf8.encode(s)));
}

// SafeInstallerFileName maps a node name to the canonical installer file name. Mirrors
// naming.SafeInstallerFileName (naming.go:47-62), in order:
//   1. Lowercase the node name.
//   2. Map every rune outside [a-z0-9-_] to a hyphen.
//   3. Collapse every run of two or more consecutive hyphens to a single one.
//   4. Trim leading and trailing hyphens.
//   5. If the result is empty, substitute the literal "node".
//   6. Append the suffix ".install.sh".
// "Web 1" and "web-1" both produce "web-1.install.sh"; "  ***  " produces "node.install.sh".
export function safeInstallerFileName(nodeName: string): string {
  const lowered = nodeName.toLowerCase();
  // Map per code point (Go strings.Map iterates runes); a non-[a-z0-9-_] rune becomes one '-'.
  let safe = '';
  for (const r of lowered) {
    if ((r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r === '-' || r === '_') {
      safe += r;
    } else {
      safe += '-';
    }
  }
  // Collapse multiple consecutive hyphens (Go's multiHyphen = regexp `-{2,}`).
  safe = safe.replace(/-{2,}/g, '-');
  // Trim leading/trailing hyphens (Go strings.Trim(safe, "-")).
  safe = safe.replace(/^-+/, '').replace(/-+$/, '');
  if (safe === '') {
    safe = 'node';
  }
  return safe + '.install.sh';
}

// cleanInterfaceName applies the shared interface-name cleaner: lowercase the name, then map every
// rune outside [a-z0-9-] to a hyphen. Unlike safeInstallerFileName, this cleaner does NOT preserve
// "_"; underscore maps to a hyphen. Mirrors naming.cleanInterfaceName (naming.go:157-165).
function cleanInterfaceName(remoteName: string): string {
  const lowered = remoteName.toLowerCase();
  let clean = '';
  for (const r of lowered) {
    if ((r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r === '-') {
      clean += r;
    } else {
      clean += '-';
    }
  }
  return clean;
}

// WgInterfaceName maps a remote peer's node name to its WireGuard interface name. Mirrors
// naming.WgInterfaceName (naming.go:87-110). The Linux kernel limits interface names to 15 chars:
//   - short path: clean = cleanInterfaceName(remoteName); name = "wg-" + clean; if len(name) <= 15
//     return name.
//   - long path (>15): "wg-" + clean[:8] + sha256(remoteName)[:4], total 3 + 8 + 4 = 15 chars.
//     The 8-char clean slice is bounded by the cleaned length when it is shorter than 8.
//
// Byte note: cleanInterfaceName emits only ASCII (every char is one byte), so the Go byte-slice
// clean[:remainForClean] coincides with the JS code-unit slice here.
export function wgInterfaceName(remoteName: string): string {
  const clean = cleanInterfaceName(remoteName);

  const name = 'wg-' + clean;
  if (name.length <= 15) {
    return name;
  }

  const maxLen = 15;
  const prefix = 'wg-';
  const hashSuffixLen = 4; // 4 hex chars, low collision probability

  const hash = sha256Hex(remoteName);
  // remainForClean = 15 - 3 - 4 = 8; bound by the cleaned length when shorter (defensive guard).
  let remainForClean = maxLen - prefix.length - hashSuffixLen;
  if (remainForClean > clean.length) {
    remainForClean = clean.length;
  }
  return prefix + clean.slice(0, remainForClean) + hash.slice(0, hashSuffixLen);
}

// WgInterfaceNameForEdge maps a per-peer link to its WireGuard interface name, edge-aware so that a
// node hosting several interfaces toward the SAME remote peer (parallel links) gets a distinct, stable
// name per backup link. Mirrors naming.WgInterfaceNameForEdge (naming.go:130-150):
//   - backup === false: returns wgInterfaceName(remoteName) byte-identical (zero interface renames).
//   - backup === true: returns "wg-" + clean[:8] + sha256(remoteName+"|"+edgeID)[:4] UNCONDITIONALLY
//     (the long-path shape with no short path), folding the edge ID into the hash input so two backups
//     toward the same peer differ in their 4-hex suffix while staying within 3 + 8 + 4 = 15 chars.
// The result is always <= 15 characters.
export function wgInterfaceNameForEdge(remoteName: string, edgeID: string, backup: boolean): string {
  if (!backup) {
    return wgInterfaceName(remoteName);
  }

  const clean = cleanInterfaceName(remoteName);

  const maxLen = 15;
  const prefix = 'wg-';
  const hashSuffixLen = 4; // 4 hex chars, low collision probability

  // Fold the edge ID into the hash input so parallel backups toward the same remote peer diverge.
  const hash = sha256Hex(remoteName + '|' + edgeID);
  // remainForClean = 15 - 3 - 4 = 8; bound by the cleaned length when shorter.
  let remainForClean = maxLen - prefix.length - hashSuffixLen;
  if (remainForClean > clean.length) {
    remainForClean = clean.length;
  }
  return prefix + clean.slice(0, remainForClean) + hash.slice(0, hashSuffixLen);
}
