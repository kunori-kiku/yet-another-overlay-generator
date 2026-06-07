package bundlesig

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"strings"
	"testing"
)

// parsePublicKeyPEM re-parses a PKIX PUBLIC KEY PEM block the way an external
// verifier (openssl) would, asserting it yields an Ed25519 public key. It is a
// test-only inverse of MarshalPublicKeyPEM.
func parsePublicKeyPEM(t *testing.T, data []byte) ed25519.PublicKey {
	t.Helper()
	block, _ := pem.Decode(data)
	if block == nil {
		t.Fatalf("parsePublicKeyPEM: no PEM block")
	}
	key, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		t.Fatalf("parsePublicKeyPEM: %v", err)
	}
	pub, ok := key.(ed25519.PublicKey)
	if !ok {
		t.Fatalf("parsePublicKeyPEM: got %T, want ed25519.PublicKey", key)
	}
	return pub
}

// marshalPrivateKeyPEM encodes a private key to PKCS#8 PEM, the inverse of
// LoadPrivateKeyPEM, for round-trip testing only.
func marshalPrivateKeyPEM(t *testing.T, priv ed25519.PrivateKey) []byte {
	t.Helper()
	der, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		t.Fatalf("marshalPrivateKeyPEM: %v", err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})
}

// sampleFiles returns a representative per-node bundle file set. It mirrors the
// real paths produced by the export path (wireguard/*.conf, babel, sysctl,
// install.sh) so the canonical output exercises realistic sorting.
func sampleFiles() map[string]string {
	return map[string]string{
		"wireguard/wg-beta.conf":  "beta config bytes",
		"wireguard/wg-alpha.conf": "alpha config bytes",
		"babel/babeld.conf":       "babel config bytes",
		"sysctl/99-overlay.conf":  "sysctl config bytes",
		"install.sh":              "#!/bin/sh\necho install\n",
	}
}

// TestCanonicalizeFormat checks the exact line format: lowercase 64-hex
// SHA-256, two spaces, path, terminated by LF; sorted by path; trailing
// newline; no CR.
func TestCanonicalizeFormat(t *testing.T) {
	files := sampleFiles()
	got := Canonicalize(files)

	if bytes.Contains(got, []byte{'\r'}) {
		t.Fatalf("canonical output contains CR; must be LF-only")
	}
	if len(got) == 0 || got[len(got)-1] != '\n' {
		t.Fatalf("canonical output must end with a trailing newline")
	}

	lines := strings.Split(strings.TrimSuffix(string(got), "\n"), "\n")
	if len(lines) != len(files) {
		t.Fatalf("got %d lines, want %d", len(lines), len(files))
	}

	var prevPath string
	for i, line := range lines {
		// "<64 hex>  <path>" — exactly two spaces between hash and path.
		const sep = "  "
		idx := strings.Index(line, sep)
		if idx != 64 {
			t.Fatalf("line %d: hash field must be 64 hex chars followed by two spaces, got %q", i, line)
		}
		hexPart := line[:64]
		path := line[idx+len(sep):]

		// Hex must be lowercase and match a recomputed SHA-256 of the content.
		if hexPart != strings.ToLower(hexPart) {
			t.Fatalf("line %d: hash %q is not lowercase", i, hexPart)
		}
		want := fmt.Sprintf("%x", sha256.Sum256([]byte(files[path])))
		if hexPart != want {
			t.Fatalf("line %d: hash for %q = %s, want %s", i, path, hexPart, want)
		}

		// Sorted strictly ascending by byte order.
		if i > 0 && !(prevPath < path) {
			t.Fatalf("paths not sorted ascending: %q then %q", prevPath, path)
		}
		prevPath = path
	}
}

// TestCanonicalizeDeterministicAndOrderIndependent builds the same logical map
// repeatedly and via shuffled insertion order, asserting byte-identical output.
func TestCanonicalizeDeterministicAndOrderIndependent(t *testing.T) {
	base := Canonicalize(sampleFiles())

	// Repeated calls are identical (no nondeterminism from map iteration).
	for i := 0; i < 50; i++ {
		if got := Canonicalize(sampleFiles()); !bytes.Equal(got, base) {
			t.Fatalf("call %d differs from base output", i)
		}
	}

	// Reconstruct the map with a deliberately different insertion order. Map
	// insertion order does not affect iteration order in Go, but this guards
	// against any future ordering dependency in the implementation.
	shuffled := map[string]string{}
	keys := []string{
		"install.sh",
		"sysctl/99-overlay.conf",
		"wireguard/wg-alpha.conf",
		"babel/babeld.conf",
		"wireguard/wg-beta.conf",
	}
	src := sampleFiles()
	for _, k := range keys {
		shuffled[k] = src[k]
	}
	if got := Canonicalize(shuffled); !bytes.Equal(got, base) {
		t.Fatalf("shuffled-insertion output differs from base:\n got=%q\nwant=%q", got, base)
	}
}

// TestCanonicalizeEmpty: an empty file set yields empty output (no spurious
// trailing newline), which is the only correct degenerate case.
func TestCanonicalizeEmpty(t *testing.T) {
	if got := Canonicalize(map[string]string{}); len(got) != 0 {
		t.Fatalf("empty file set must yield empty output, got %q", got)
	}
}

// TestSignVerifyRoundTrip: a signature over the canonical bytes verifies with
// the matching public key.
func TestSignVerifyRoundTrip(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	canonical := Canonicalize(sampleFiles())

	sig := Sign(canonical, priv)
	if len(sig) != ed25519.SignatureSize {
		t.Fatalf("signature length = %d, want %d", len(sig), ed25519.SignatureSize)
	}
	if !Verify(canonical, sig, pub) {
		t.Fatalf("Verify failed on a valid signature")
	}
}

// TestVerifyTamperFails: flipping a single byte of the canonical input breaks
// verification (the signature is bound to the exact bytes).
func TestVerifyTamperFails(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	canonical := Canonicalize(sampleFiles())
	sig := Sign(canonical, priv)

	tampered := bytes.Clone(canonical)
	tampered[0] ^= 0x01 // flip one bit of one byte
	if Verify(tampered, sig, pub) {
		t.Fatalf("Verify accepted tampered canonical bytes")
	}

	// Tampering the signature must also fail.
	badSig := bytes.Clone(sig)
	badSig[0] ^= 0x01
	if Verify(canonical, badSig, pub) {
		t.Fatalf("Verify accepted a tampered signature")
	}
}

// TestVerifyWrongKeyFails: a signature from one key does not verify against a
// different public key.
func TestVerifyWrongKeyFails(t *testing.T) {
	_, priv1, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate key 1: %v", err)
	}
	pub2, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate key 2: %v", err)
	}
	canonical := Canonicalize(sampleFiles())
	sig := Sign(canonical, priv1)
	if Verify(canonical, sig, pub2) {
		t.Fatalf("Verify accepted a signature against the wrong public key")
	}
}

// TestVerifyMalformedInputs: wrong-length keys/signatures return false rather
// than panicking.
func TestVerifyMalformedInputs(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	canonical := Canonicalize(sampleFiles())
	sig := Sign(canonical, priv)

	if Verify(canonical, sig[:len(sig)-1], pub) {
		t.Fatalf("Verify accepted a short signature")
	}
	if Verify(canonical, sig, pub[:len(pub)-1]) {
		t.Fatalf("Verify accepted a short public key")
	}
	if Verify(canonical, nil, pub) {
		t.Fatalf("Verify accepted a nil signature")
	}
}

// TestPublicKeyPEMRoundTrip: MarshalPublicKeyPEM then load via the private-key
// path equivalents; here we marshal the public key, then sign with the private
// key and verify with the public key derived from the same PEM-marshaled key.
func TestPublicKeyPEMRoundTrip(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}

	pemBytes := MarshalPublicKeyPEM(pub)
	if !bytes.Contains(pemBytes, []byte("-----BEGIN PUBLIC KEY-----")) {
		t.Fatalf("PEM output missing PUBLIC KEY header: %q", pemBytes)
	}

	// Re-parse the PEM via the standard x509 path the same way openssl would,
	// and confirm a signature verifies against the round-tripped public key.
	reparsed := parsePublicKeyPEM(t, pemBytes)
	canonical := Canonicalize(sampleFiles())
	sig := Sign(canonical, priv)
	if !Verify(canonical, sig, reparsed) {
		t.Fatalf("signature did not verify against PEM-round-tripped public key")
	}
}

// TestPrivateKeyPEMRoundTrip: marshal a private key to PKCS#8 PEM, load it back
// via LoadPrivateKeyPEM, and confirm it signs identically to the original.
func TestPrivateKeyPEMRoundTrip(t *testing.T) {
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	pemBytes := marshalPrivateKeyPEM(t, priv)

	loaded, err := LoadPrivateKeyPEM(pemBytes)
	if err != nil {
		t.Fatalf("LoadPrivateKeyPEM: %v", err)
	}

	canonical := Canonicalize(sampleFiles())
	if !bytes.Equal(Sign(canonical, priv), Sign(canonical, loaded)) {
		t.Fatalf("loaded private key produced a different signature")
	}
}

// TestLoadPrivateKeyPEMErrors: malformed and non-Ed25519 PEM inputs error
// cleanly instead of returning a bogus key.
func TestLoadPrivateKeyPEMErrors(t *testing.T) {
	if _, err := LoadPrivateKeyPEM([]byte("not a pem")); err == nil {
		t.Fatalf("expected error for non-PEM input")
	}
	if _, err := LoadPrivateKeyPEM(nil); err == nil {
		t.Fatalf("expected error for nil input")
	}
}

// TestNotComputeChecksumFormat is a guard asserting Canonicalize is NOT the
// non-canonical compiler.go computeChecksum digest. computeChecksum hashes a
// fmt.Sprintf("%v", ...) of a struct; the canonical form is per-file
// "<hash>  <path>\n" lines. This pins the contract that signing happens over
// the canonical checksums, never the %v digest.
func TestNotComputeChecksumFormat(t *testing.T) {
	got := string(Canonicalize(sampleFiles()))
	// Each line is "<64 hex>  <path>"; a single opaque %v digest would not
	// contain the per-path sha256sum structure with two-space separators.
	for _, line := range strings.Split(strings.TrimSuffix(got, "\n"), "\n") {
		if !strings.Contains(line, "  ") {
			t.Fatalf("line %q is not in sha256sum '<hash>  <path>' form", line)
		}
	}
}
