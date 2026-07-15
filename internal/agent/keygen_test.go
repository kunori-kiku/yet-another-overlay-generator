package agent

import (
	"os"
	"path/filepath"
	"sync"
	"testing"

	"golang.zx2c4.com/wireguard/wgctrl/wgtypes"
)

func TestEnsureKeyConcurrentCallersConverge(t *testing.T) {
	keyPath := filepath.Join(t.TempDir(), "wg", "agent.key")
	const callers = 16

	type result struct {
		pub     string
		created bool
		err     error
	}
	start := make(chan struct{})
	results := make(chan result, callers)
	var ready sync.WaitGroup
	ready.Add(callers)
	for i := 0; i < callers; i++ {
		go func() {
			ready.Done()
			<-start
			pub, created, err := EnsureKey(keyPath)
			results <- result{pub: pub, created: created, err: err}
		}()
	}
	ready.Wait()
	close(start)

	createdCount := 0
	var expectedPub string
	for i := 0; i < callers; i++ {
		got := <-results
		if got.err != nil {
			t.Fatalf("EnsureKey caller %d: %v", i, got.err)
		}
		if got.created {
			createdCount++
		}
		if expectedPub == "" {
			expectedPub = got.pub
		} else if got.pub != expectedPub {
			t.Fatalf("caller %d returned public key %q, want converged key %q", i, got.pub, expectedPub)
		}
	}
	if createdCount != 1 {
		t.Fatalf("created=true count = %d, want exactly 1", createdCount)
	}

	raw, err := os.ReadFile(keyPath)
	if err != nil {
		t.Fatalf("read final key: %v", err)
	}
	priv, err := wgtypes.ParseKey(trimNL(raw))
	if err != nil {
		t.Fatalf("parse final key: %v", err)
	}
	if got := priv.PublicKey().String(); got != expectedPub {
		t.Fatalf("final private key public half = %q, want %q", got, expectedPub)
	}
}

func TestEnsureKeyRejectsPermissiveExistingModeWithoutTrustingContents(t *testing.T) {
	keyPath := filepath.Join(t.TempDir(), "agent.key")
	key, err := wgtypes.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate seed key: %v", err)
	}
	if err := os.WriteFile(keyPath, []byte(key.String()+"\n"), 0600); err != nil {
		t.Fatalf("write seed key: %v", err)
	}
	if err := os.Chmod(keyPath, 0666); err != nil {
		t.Fatalf("make seed key permissive: %v", err)
	}

	if _, _, err := EnsureKey(keyPath); err == nil {
		t.Fatal("EnsureKey trusted a group/world-accessible existing secret")
	}
	info, err := os.Stat(keyPath)
	if err != nil {
		t.Fatalf("stat key: %v", err)
	}
	if got := info.Mode().Perm(); got != 0666 {
		t.Fatalf("key mode = %04o, want unsafe file to remain untouched", got)
	}
}

func TestEnsureKeyRejectsSymlinkWithoutTouchingTarget(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target")
	keyPath := filepath.Join(dir, "agent.key")
	original := []byte("do-not-touch")
	if err := os.WriteFile(target, original, 0600); err != nil {
		t.Fatalf("write target: %v", err)
	}
	if err := os.Symlink(target, keyPath); err != nil {
		t.Fatalf("create key symlink: %v", err)
	}

	if _, _, err := EnsureKey(keyPath); err == nil {
		t.Fatal("EnsureKey accepted a symlink key path")
	}
	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("read target: %v", err)
	}
	if string(got) != string(original) {
		t.Fatalf("symlink target changed to %q", got)
	}
}

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
