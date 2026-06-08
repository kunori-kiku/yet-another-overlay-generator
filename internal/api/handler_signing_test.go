package api

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"strings"
	"testing"

	"github.com/kunorikiku/yet-another-overlay-generator/internal/bundlesig"
)

// extractQuoted returns the value of a `VAR="..."` assignment emitted by Go's %q.
// The signed wrapper carries the signature and pubkey as base64, whose charset
// contains no quotes or backslashes, so %q produces a plain double-quoted string.
func extractQuoted(t *testing.T, s, varName string) string {
	t.Helper()
	head := varName + `="`
	i := strings.Index(s, head)
	if i < 0 {
		t.Fatalf("wrapper missing %s assignment", varName)
	}
	rest := s[i+len(head):]
	j := strings.Index(rest, `"`)
	if j < 0 {
		t.Fatalf("wrapper %s assignment is unterminated", varName)
	}
	return rest[:j]
}

// TestMakeSelfExtractingInstaller_Signed asserts the signed wrapper verifies the
// payload's Ed25519 signature before the SHA-256 payload check, and that the
// embedded signature actually verifies over the payload under the embedded pubkey.
func TestMakeSelfExtractingInstaller_Signed(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	// The in-process *bundlesig.Signing is the default bundlesig.ConfigSigner.
	signer := &bundlesig.Signing{Priv: priv, PubKeyPEM: bundlesig.MarshalPublicKeyPEM(pub)}
	payload := []byte("pretend tar.gz payload bytes\x00\x01\x02")

	out, err := makeSelfExtractingInstaller("alpha", payload, signer)
	if err != nil {
		t.Fatalf("makeSelfExtractingInstaller: %v", err)
	}
	s := string(out)

	// Ordering: openssl verify must precede the SHA-256 payload check.
	vi := strings.Index(s, "openssl pkeyutl -verify")
	ci := strings.Index(s, "| sha256sum -c -")
	if vi < 0 {
		t.Fatal("signed wrapper missing the openssl verify block")
	}
	if ci < 0 {
		t.Fatal("wrapper missing the SHA-256 payload check")
	}
	if vi >= ci {
		t.Errorf("payload signature verify must precede the SHA-256 check (verify=%d, checksum=%d)", vi, ci)
	}

	// The embedded signature must verify over the payload under the embedded pubkey.
	sig, err := base64.StdEncoding.DecodeString(extractQuoted(t, s, "PAYLOAD_SIG_B64"))
	if err != nil {
		t.Fatalf("decode PAYLOAD_SIG_B64: %v", err)
	}
	pubPEM, err := base64.StdEncoding.DecodeString(extractQuoted(t, s, "SIGNING_PUBKEY_PEM_B64"))
	if err != nil {
		t.Fatalf("decode SIGNING_PUBKEY_PEM_B64: %v", err)
	}
	if !strings.Contains(string(pubPEM), "BEGIN PUBLIC KEY") {
		t.Errorf("embedded pubkey is not a PEM public key: %q", pubPEM)
	}
	if !bundlesig.Verify(payload, sig, pub) {
		t.Error("embedded payload signature does not verify under the signing key")
	}
	// Fail-clear when openssl is missing must be present.
	if !strings.Contains(s, "openssl is not installed") {
		t.Error("signed wrapper must fail clearly when openssl is missing")
	}
}

// TestMakeSelfExtractingInstaller_Unsigned asserts that with signing nil the
// wrapper carries no signature remnant but still performs the SHA-256 payload check.
func TestMakeSelfExtractingInstaller_Unsigned(t *testing.T) {
	out, err := makeSelfExtractingInstaller("alpha", []byte("payload"), nil)
	if err != nil {
		t.Fatalf("makeSelfExtractingInstaller: %v", err)
	}
	s := string(out)
	for _, remnant := range []string{"openssl pkeyutl", "PAYLOAD_SIG_B64", "SIGNING_PUBKEY_PEM_B64"} {
		if strings.Contains(s, remnant) {
			t.Errorf("unsigned wrapper must not contain %q", remnant)
		}
	}
	if !strings.Contains(s, "| sha256sum -c -") {
		t.Error("unsigned wrapper must still perform the SHA-256 payload check")
	}
}
