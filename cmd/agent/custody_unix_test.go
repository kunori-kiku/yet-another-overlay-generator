//go:build !windows

package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestPinnedCredentialReadersEnforceIntegrityCustody(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "pin.pem")
	if err := os.WriteFile(path, []byte("public pin"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := readPinnedPubkey(path); err != nil {
		t.Fatalf("read-only signing pin rejected: %v", err)
	}
	if _, err := readOperatorCred(path, "ed25519"); err != nil {
		t.Fatalf("read-only operator pin rejected: %v", err)
	}
	if err := os.Chmod(path, 0o666); err != nil {
		t.Fatal(err)
	}
	if _, err := readPinnedPubkey(path); err == nil {
		t.Fatal("signing-pin reader accepted a group/world-writable pin")
	}
	if _, err := readOperatorCred(path, "ed25519"); err == nil {
		t.Fatal("operator-pin reader accepted a group/world-writable pin")
	}
}
