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

func trimNL(b []byte) string {
	s := string(b)
	for len(s) > 0 && (s[len(s)-1] == '\n' || s[len(s)-1] == '\r') {
		s = s[:len(s)-1]
	}
	return s
}
