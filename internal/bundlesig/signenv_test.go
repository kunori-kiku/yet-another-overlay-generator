package bundlesig

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"os"
	"path/filepath"
	"testing"
)

// writePKCS8Key writes a fresh Ed25519 PKCS#8 PEM to a temp file and returns the
// path plus the public half for assertions.
func writePKCS8Key(t *testing.T) (string, ed25519.PublicKey) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		t.Fatalf("marshal pkcs8: %v", err)
	}
	path := filepath.Join(t.TempDir(), "key.pem")
	if err := os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}), 0600); err != nil {
		t.Fatalf("write key: %v", err)
	}
	return path, pub
}

// TestLoadSigningFromEnv_Unset returns (nil, nil) when the var is unset/empty —
// signing is off, the opt-in default.
func TestLoadSigningFromEnv_Unset(t *testing.T) {
	t.Setenv(EnvSigningKey, "")
	s, err := LoadSigningFromEnv()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if s != nil {
		t.Fatalf("expected nil Signing when env unset, got %+v", s)
	}
}

// TestLoadSigningFromEnv_Valid loads the key and derives a PubKeyPEM that matches
// MarshalPublicKeyPEM of the same public key (the bytes shipped + embedded).
func TestLoadSigningFromEnv_Valid(t *testing.T) {
	path, pub := writePKCS8Key(t)
	t.Setenv(EnvSigningKey, path)

	s, err := LoadSigningFromEnv()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if s == nil {
		t.Fatal("expected non-nil Signing")
	}
	if !s.Priv.Public().(ed25519.PublicKey).Equal(pub) {
		t.Error("loaded private key's public half does not match the generated key")
	}
	if !bytes.Equal(s.PubKeyPEM, MarshalPublicKeyPEM(pub)) {
		t.Error("PubKeyPEM does not match MarshalPublicKeyPEM of the public key")
	}
	// The derived material must actually sign/verify the canonical bytes.
	canonical := Canonicalize(map[string]string{"install.sh": "echo hi\n"})
	if !Verify(canonical, Sign(canonical, s.Priv), pub) {
		t.Error("loaded key cannot sign/verify canonical bytes")
	}
}

// TestLoadSigningFromEnv_BadPath fails closed when the path is set but unreadable.
func TestLoadSigningFromEnv_BadPath(t *testing.T) {
	t.Setenv(EnvSigningKey, filepath.Join(t.TempDir(), "missing.pem"))
	if _, err := LoadSigningFromEnv(); err == nil {
		t.Fatal("expected an error for an unreadable signing key path")
	}
}

// TestLoadSigningFromEnv_NotEd25519 rejects a non-Ed25519 (malformed) PEM.
func TestLoadSigningFromEnv_Malformed(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bad.pem")
	if err := os.WriteFile(path, []byte("not a pem\n"), 0600); err != nil {
		t.Fatalf("write: %v", err)
	}
	t.Setenv(EnvSigningKey, path)
	if _, err := LoadSigningFromEnv(); err == nil {
		t.Fatal("expected an error for a malformed signing key")
	}
}
