package agent

import (
	"os"
	"path/filepath"
	"testing"

	"golang.zx2c4.com/wireguard/wgctrl/wgtypes"
)

// TestEnsureKeyIdempotent verifies that a second keygen keeps the same key (never
// rotates identity) and that the key file is created mode 0600.
func TestEnsureKeyIdempotent(t *testing.T) {
	keyPath := filepath.Join(t.TempDir(), "wg", "agent.key")

	pub1, created1, err := EnsureKey(keyPath)
	if err != nil {
		t.Fatalf("first EnsureKey: %v", err)
	}
	if !created1 {
		t.Fatalf("first EnsureKey: expected created=true")
	}
	if pub1 == "" {
		t.Fatalf("first EnsureKey: empty public key")
	}

	// Mode must be exactly 0600.
	info, err := os.Stat(keyPath)
	if err != nil {
		t.Fatalf("stat key: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0600 {
		t.Fatalf("key mode = %o, want 0600", perm)
	}

	// The stored content must be a valid WireGuard private key whose public half
	// matches what EnsureKey returned.
	raw, err := os.ReadFile(keyPath)
	if err != nil {
		t.Fatalf("read key: %v", err)
	}
	priv, err := wgtypes.ParseKey(trimNL(raw))
	if err != nil {
		t.Fatalf("stored key not parseable: %v", err)
	}
	if priv.PublicKey().String() != pub1 {
		t.Fatalf("returned pubkey does not match stored private key")
	}

	// Second call: same key, created=false.
	pub2, created2, err := EnsureKey(keyPath)
	if err != nil {
		t.Fatalf("second EnsureKey: %v", err)
	}
	if created2 {
		t.Fatalf("second EnsureKey: expected created=false (idempotent)")
	}
	if pub2 != pub1 {
		t.Fatalf("second EnsureKey rotated the key: %s != %s", pub2, pub1)
	}
}

// TestEnsureKeyRejectsCorruptKey ensures a corrupt existing key is a hard error
// (never silently overwritten / rotated).
func TestEnsureKeyRejectsCorruptKey(t *testing.T) {
	keyPath := filepath.Join(t.TempDir(), "agent.key")
	if err := os.WriteFile(keyPath, []byte("not-a-valid-key"), 0600); err != nil {
		t.Fatalf("seed corrupt key: %v", err)
	}
	if _, _, err := EnsureKey(keyPath); err == nil {
		t.Fatalf("expected error on corrupt existing key, got nil")
	}
}

// TestRegenerateKeyRotates verifies RegenerateKey always rotates: it overwrites an
// existing key with a fresh one (new public key differs), the stored private key matches
// the returned public key, and the file is written mode 0600. This is the explicit
// rotation path driven by a controller-requested fleet rekey (unlike EnsureKey, which is
// idempotent and never rotates).
func TestRegenerateKeyRotates(t *testing.T) {
	keyPath := filepath.Join(t.TempDir(), "wg", "agent.key")

	// Seed an initial key via EnsureKey (created on disk, mode 0600).
	pub1, created, err := EnsureKey(keyPath)
	if err != nil {
		t.Fatalf("EnsureKey seed: %v", err)
	}
	if !created {
		t.Fatalf("EnsureKey seed: expected created=true")
	}

	// RegenerateKey must produce a DIFFERENT public key (it rotated the identity).
	pub2, err := RegenerateKey(keyPath)
	if err != nil {
		t.Fatalf("RegenerateKey: %v", err)
	}
	if pub2 == "" {
		t.Fatalf("RegenerateKey: empty public key")
	}
	if pub2 == pub1 {
		t.Fatalf("RegenerateKey did not rotate the key: pub %s unchanged", pub2)
	}

	// The on-disk key must be the NEW one (overwritten), mode 0600, and its public half
	// must match what RegenerateKey returned.
	info, err := os.Stat(keyPath)
	if err != nil {
		t.Fatalf("stat key: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0600 {
		t.Fatalf("key mode = %o, want 0600", perm)
	}
	raw, err := os.ReadFile(keyPath)
	if err != nil {
		t.Fatalf("read key: %v", err)
	}
	priv, err := wgtypes.ParseKey(trimNL(raw))
	if err != nil {
		t.Fatalf("stored key not parseable: %v", err)
	}
	if priv.PublicKey().String() != pub2 {
		t.Fatalf("returned pubkey does not match stored private key after regenerate")
	}

	// A second RegenerateKey rotates again (each call is a fresh key).
	pub3, err := RegenerateKey(keyPath)
	if err != nil {
		t.Fatalf("second RegenerateKey: %v", err)
	}
	if pub3 == pub2 {
		t.Fatalf("second RegenerateKey did not rotate: pub %s unchanged", pub3)
	}
}

// TestRegenerateKeyCreatesWhenAbsent confirms RegenerateKey also serves as a create when
// no key exists yet (it does not require a prior key to rotate from).
func TestRegenerateKeyCreatesWhenAbsent(t *testing.T) {
	keyPath := filepath.Join(t.TempDir(), "nested", "agent.key")
	pub, err := RegenerateKey(keyPath)
	if err != nil {
		t.Fatalf("RegenerateKey on absent path: %v", err)
	}
	if pub == "" {
		t.Fatalf("RegenerateKey on absent path: empty public key")
	}
	raw, err := os.ReadFile(keyPath)
	if err != nil {
		t.Fatalf("read key: %v", err)
	}
	priv, err := wgtypes.ParseKey(trimNL(raw))
	if err != nil {
		t.Fatalf("stored key not parseable: %v", err)
	}
	if priv.PublicKey().String() != pub {
		t.Fatalf("returned pubkey does not match stored private key")
	}
}

// TestRegenerateKeyRejectsEmptyPath confirms RegenerateKey guards the empty path the same
// way EnsureKey does.
func TestRegenerateKeyRejectsEmptyPath(t *testing.T) {
	if _, err := RegenerateKey("   "); err == nil {
		t.Fatalf("expected error on empty key path, got nil")
	}
}

func trimNL(b []byte) string {
	s := string(b)
	for len(s) > 0 && (s[len(s)-1] == '\n' || s[len(s)-1] == '\r') {
		s = s[:len(s)-1]
	}
	return s
}
