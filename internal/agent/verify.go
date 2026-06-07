package agent

import (
	"crypto/ed25519"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/pem"
	"fmt"
	"strings"

	"github.com/kunorikiku/yet-another-overlay-generator/internal/bundlesig"
)

// VerifyResult records what verification established about a bundle. It is
// surfaced in the agent's status report so an operator can see whether the
// applied bundle was signature-verified or only hash-verified.
type VerifyResult struct {
	// Signed is true when a bundle.sig + signing-pubkey were present and the
	// signature verified against the pinned key.
	Signed bool
	// FileCount is the number of files whose SHA-256 was checked.
	FileCount int
}

// parseChecksums parses checksums.sha256 (bundlesig.Canonicalize / sha256sum
// format: "<64-hex>  <path>\n", two spaces) into a path -> lowercase-hex map. It
// is intentionally strict: a malformed line, a non-64-char hex field, or a
// duplicate path is an error, because this file is the integrity authority and a
// parse ambiguity must never silently weaken a check.
func parseChecksums(data []byte) (map[string]string, error) {
	out := make(map[string]string)
	for i, rawLine := range strings.Split(string(data), "\n") {
		line := rawLine
		if line == "" {
			continue
		}
		// sha256sum binary-mode separator is exactly two spaces between the hex
		// digest and the path. Split on the first occurrence; the path may itself
		// contain spaces, so do not split further.
		idx := strings.Index(line, "  ")
		if idx < 0 {
			return nil, fmt.Errorf("checksums line %d: missing two-space separator: %q", i+1, rawLine)
		}
		hexSum := line[:idx]
		path := line[idx+2:]
		if len(hexSum) != 64 {
			return nil, fmt.Errorf("checksums line %d: digest is %d hex chars, want 64", i+1, len(hexSum))
		}
		if _, err := hex.DecodeString(hexSum); err != nil {
			return nil, fmt.Errorf("checksums line %d: digest is not hex: %w", i+1, err)
		}
		if path == "" {
			return nil, fmt.Errorf("checksums line %d: empty path", i+1)
		}
		if _, dup := out[path]; dup {
			return nil, fmt.Errorf("checksums line %d: duplicate path %q", i+1, path)
		}
		out[path] = strings.ToLower(hexSum)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("checksums.sha256 lists no files")
	}
	return out, nil
}

// parsePinnedPublicKey decodes a PKIX ("PUBLIC KEY") PEM block into an Ed25519
// public key, the same way bundlesig.MarshalPublicKeyPEM produces it and openssl
// consumes it. It is used both for the operator-pinned key and for the bundle's
// own signing-pubkey.pem.
func parsePinnedPublicKey(pemBytes []byte) (ed25519.PublicKey, error) {
	block, _ := pem.Decode(pemBytes)
	if block == nil {
		return nil, fmt.Errorf("agent: no PEM block in public key")
	}
	key, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("agent: parse PKIX public key: %w", err)
	}
	pub, ok := key.(ed25519.PublicKey)
	if !ok {
		return nil, fmt.Errorf("agent: pinned key is %T, want ed25519.PublicKey", key)
	}
	return pub, nil
}

// VerifyBundle is the Go-side, fail-closed integrity gate run BEFORE install.sh.
// It is defense in depth: install.sh re-verifies as root, but verifying here
// means a tampered or rolled-back bundle never reaches a root-executed script.
//
// pinnedPubPEM is the operator-configured trust anchor (--pubkey), or nil when no
// key is pinned. Policy:
//
//   - A signature (bundle.sig + signing-pubkey.pem) present in the bundle is
//     always verified, against the pinned key when one is configured, otherwise
//     against the bundle's own signing-pubkey.pem (trust-on-first-supply — the
//     pin is what makes it strong, so configuring --pubkey is recommended).
//   - When a key IS pinned but the bundle carries NO signature, verification
//     fails closed: a pinned operator demands authenticity.
//   - When no key is pinned and no signature is present, the bundle is treated as
//     unsigned and only per-file hashes are checked (back-compatible).
//   - Regardless, EVERY file listed in checksums.sha256 must be present in files
//     and match its recorded SHA-256, and the signature (when checked) must be
//     valid over the EXACT bytes of checksums.sha256.
//
// Any mismatch returns an error; the caller must refuse to apply.
func VerifyBundle(files map[string][]byte, pinnedPubPEM []byte) (*VerifyResult, error) {
	checksums, ok := files["checksums.sha256"]
	if !ok {
		return nil, fmt.Errorf("agent: bundle has no checksums.sha256")
	}
	listed, err := parseChecksums(checksums)
	if err != nil {
		return nil, err
	}

	sigB64, hasSig := files["bundle.sig"]
	bundlePubPEM, hasPub := files["signing-pubkey.pem"]
	signaturePresent := hasSig && hasPub
	pinned := len(pinnedPubPEM) > 0

	res := &VerifyResult{}

	switch {
	case signaturePresent:
		// Decode the detached signature: base64 of the raw 64-byte Ed25519 sig.
		sigRaw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(sigB64)))
		if err != nil {
			return nil, fmt.Errorf("agent: decode bundle.sig: %w", err)
		}
		// Choose the verification key: pinned key wins; otherwise the bundle's own.
		var pubPEM []byte
		if pinned {
			pubPEM = pinnedPubPEM
		} else {
			pubPEM = bundlePubPEM
		}
		pub, err := parsePinnedPublicKey(pubPEM)
		if err != nil {
			return nil, err
		}
		// Verify over the EXACT bytes of checksums.sha256 (the canonical content).
		if !bundlesig.Verify(checksums, sigRaw, pub) {
			return nil, fmt.Errorf("agent: bundle signature verification failed")
		}
		res.Signed = true
	case pinned:
		// A key is pinned but the bundle is unsigned: fail closed.
		return nil, fmt.Errorf("agent: signing key pinned but bundle has no signature (bundle.sig/signing-pubkey.pem missing); refusing")
	default:
		// No pin, no signature: unsigned bundle, hash-only verification.
	}

	// Per-file SHA-256: every listed file must be present and match.
	for path, wantHex := range listed {
		content, ok := files[path]
		if !ok {
			return nil, fmt.Errorf("agent: checksums lists %q but it is missing from the bundle", path)
		}
		gotSum := sha256.Sum256(content)
		gotHex := hex.EncodeToString(gotSum[:])
		if gotHex != wantHex {
			return nil, fmt.Errorf("agent: checksum mismatch for %q: got %s, want %s", path, gotHex, wantHex)
		}
		res.FileCount++
	}

	// install.sh is the root-executed trust anchor and must always be present and
	// covered by checksums (the export path lists it). Guard explicitly so a
	// checksums file that somehow omits it cannot let an unverified script run.
	if _, ok := files["install.sh"]; !ok {
		return nil, fmt.Errorf("agent: bundle has no install.sh")
	}
	if _, ok := listed["install.sh"]; !ok {
		return nil, fmt.Errorf("agent: checksums.sha256 does not cover install.sh")
	}

	return res, nil
}
