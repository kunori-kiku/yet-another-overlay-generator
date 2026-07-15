package main

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// captureStdout runs fn with os.Stdout redirected to a pipe and returns what it printed.
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	os.Stdout = w
	fn()
	_ = w.Close()
	os.Stdout = old
	b, _ := io.ReadAll(r)
	return string(b)
}

// captureStderr runs fn with os.Stderr redirected to a pipe and returns what it printed.
func captureStderr(t *testing.T, fn func()) string {
	t.Helper()
	old := os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	os.Stderr = w
	fn()
	_ = w.Close()
	os.Stderr = old
	b, _ := io.ReadAll(r)
	return string(b)
}

// TestRunKit covers the `agent kit` one-shot manual-node provisioning helper: --node-id is required;
// the happy path ensures the on-box key and prints a {node_id, wireguard_public_key, endpoint}
// descriptor to stdout; and — the load-bearing custody invariant — the PRIVATE key never appears in
// the printed descriptor (it stays on the box for install.sh to splice).
func TestRunKit(t *testing.T) {
	if code := runKit([]string{}); code != 2 {
		t.Errorf("kit without --node-id = %d, want 2", code)
	}

	dir := t.TempDir()
	keyPath := filepath.Join(dir, "agent.key")
	var code int
	out := captureStdout(t, func() {
		code = runKit([]string{"--node-id", "node-mike", "--endpoint", "mike.example.com:51820", "--key", keyPath})
	})
	if code != 0 {
		t.Fatalf("kit happy path = %d, want 0\n%s", code, out)
	}

	var desc manualNodeDescriptor
	if err := json.Unmarshal([]byte(out), &desc); err != nil {
		t.Fatalf("stdout is not a JSON descriptor: %v\n%s", err, out)
	}
	if desc.NodeID != "node-mike" {
		t.Errorf("descriptor node_id = %q, want node-mike", desc.NodeID)
	}
	if desc.Endpoint != "mike.example.com:51820" {
		t.Errorf("descriptor endpoint = %q, want mike.example.com:51820", desc.Endpoint)
	}
	if desc.PublicKey == "" {
		t.Errorf("descriptor carries no wireguard_public_key")
	}

	// ZERO-KNOWLEDGE: the on-box private key must NOT appear in the printed descriptor.
	priv, err := os.ReadFile(keyPath)
	if err != nil {
		t.Fatalf("read key file the kit wrote: %v", err)
	}
	if pk := strings.TrimSpace(string(priv)); pk != "" && strings.Contains(out, pk) {
		t.Errorf("ZERO-KNOWLEDGE VIOLATION: the private key appears in the kit's stdout descriptor")
	}

	// Re-running reuses the existing key (idempotent), yielding the same public key.
	out2 := captureStdout(t, func() {
		code = runKit([]string{"--node-id", "node-mike", "--key", keyPath})
	})
	if code != 0 {
		t.Fatalf("kit re-run = %d, want 0", code)
	}
	var desc2 manualNodeDescriptor
	if err := json.Unmarshal([]byte(out2), &desc2); err != nil {
		t.Fatalf("re-run stdout not JSON: %v", err)
	}
	if desc2.PublicKey != desc.PublicKey {
		t.Errorf("re-run public key changed (%q -> %q); the key must be reused, not regenerated", desc.PublicKey, desc2.PublicKey)
	}
	if desc2.Endpoint != "" {
		t.Errorf("re-run without --endpoint should omit endpoint, got %q", desc2.Endpoint)
	}
}

func TestWriteTokenFileAtomicallyTightensExistingPermissions(t *testing.T) {
	dir := t.TempDir()
	tokenPath := filepath.Join(dir, "agent-token")
	if err := os.WriteFile(tokenPath, []byte("old-token"), 0644); err != nil {
		t.Fatalf("seed token: %v", err)
	}
	if err := os.Chmod(tokenPath, 0644); err != nil {
		t.Fatalf("make seeded token permissive: %v", err)
	}

	if err := writeTokenFile(tokenPath, "replacement-token"); err != nil {
		t.Fatalf("writeTokenFile: %v", err)
	}
	got, err := os.ReadFile(tokenPath)
	if err != nil {
		t.Fatalf("read replacement token: %v", err)
	}
	if string(got) != "replacement-token" {
		t.Fatalf("replacement token = %q, want replacement-token", got)
	}
	info, err := os.Stat(tokenPath)
	if err != nil {
		t.Fatalf("stat replacement token: %v", err)
	}
	if mode := info.Mode().Perm(); mode != 0600 {
		t.Fatalf("replacement token mode = %04o, want 0600", mode)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read token dir: %v", err)
	}
	if len(entries) != 1 || entries[0].Name() != filepath.Base(tokenPath) {
		t.Fatalf("token write leaked temporary files: %v", entries)
	}
}
