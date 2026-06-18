package controller

// filestore_durability_test.go is the no-regression net for the B2 crash-consistency
// hardening (plan-8 Phase 7): writeJSONAtomic now does OpenFile+Write+Sync+Close then
// fsyncs the parent dir after the rename, and AppendAudit now f.Sync()s before f.Close().
// Durability itself is verified by code-review + crash-reasoning (an fsync's effect is not
// observable from a userspace round-trip); these tests pin that the added Sync/dir-fsync
// code paths introduce NO behavioral regression — the happy path still round-trips and the
// error paths (a missing parent dir) still surface a coded error and leave no orphan temp.

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestWriteJSONAtomicRoundTrip: a write into an existing dir lands durably-readable bytes
// and leaves no leftover .tmp sidecar (the rename consumed it).
func TestWriteJSONAtomicRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "rec.json")
	type rec struct {
		A string `json:"a"`
		B int    `json:"b"`
	}
	want := rec{A: "hello", B: 7}
	if err := writeJSONAtomic(path, want); err != nil {
		t.Fatalf("writeJSONAtomic: %v", err)
	}
	var got rec
	if err := readJSON(path, &got); err != nil {
		t.Fatalf("readJSON: %v", err)
	}
	if got != want {
		t.Fatalf("round-trip = %+v, want %+v", got, want)
	}
	// The temp sidecar must be gone (rename consumed it).
	if _, err := os.Stat(path + ".tmp"); !os.IsNotExist(err) {
		t.Fatalf("leftover %s.tmp after a successful write (stat err = %v)", path, err)
	}
}

// TestWriteJSONAtomicMissingParentDir: with a parent dir that does NOT exist, the OpenFile
// of the temp file fails — the function must surface that error (not silently succeed) and
// not panic. This exercises the new OpenFile error path that replaced os.WriteFile, plus
// the new parent-dir fsync (which must not be reached / not panic on the error path).
func TestWriteJSONAtomicMissingParentDir(t *testing.T) {
	dir := t.TempDir()
	// "absent" is never created, so <absent>/rec.json has no parent dir.
	path := filepath.Join(dir, "absent", "rec.json")
	if err := writeJSONAtomic(path, map[string]string{"k": "v"}); err == nil {
		t.Fatal("writeJSONAtomic into a missing parent dir = nil, want an error")
	}
	// And no temp file leaked into the (also-absent) dir.
	if _, err := os.Stat(path + ".tmp"); !os.IsNotExist(err) {
		t.Fatalf("unexpected leftover temp file (stat err = %v)", err)
	}
}

// TestWriteJSONAtomicOverwritePreservesPerms: overwriting an existing file via the temp+
// rename path keeps the 0600 mode (the new OpenFile creates the temp 0600; the rename
// replaces the target). A regression that dropped the mode (e.g. switching to a default
// 0666 create) would be caught here.
func TestWriteJSONAtomicOverwritePreservesPerms(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "rec.json")
	if err := writeJSONAtomic(path, map[string]int{"v": 1}); err != nil {
		t.Fatalf("writeJSONAtomic (first): %v", err)
	}
	if err := writeJSONAtomic(path, map[string]int{"v": 2}); err != nil {
		t.Fatalf("writeJSONAtomic (overwrite): %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0600 {
		t.Fatalf("perm after overwrite = %o, want 600", perm)
	}
	var got map[string]int
	if err := readJSON(path, &got); err != nil {
		t.Fatalf("readJSON: %v", err)
	}
	if got["v"] != 2 {
		t.Fatalf("overwrite value = %d, want 2", got["v"])
	}
}

// TestAppendAuditDurableRoundTrip: an entry appended through AppendAudit (which now Sync()s
// before Close) is immediately readable back via ListAudit with its content intact, and a
// second append chains onto it. This is the no-regression net for the added f.Sync() — a
// Sync that errored or a Close-ordering regression would surface here as a lost or
// unreadable entry.
func TestAppendAuditDurableRoundTrip(t *testing.T) {
	ctx := context.Background()
	s, err := NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}
	const tn = TenantID("durability-tenant")
	ts := time.Date(2026, 6, 18, 9, 0, 0, 0, time.UTC)
	first, err := s.AppendAudit(ctx, tn, AuditEntry{
		Timestamp: ts, Actor: "operator:admin", Action: "passkey-registered", NodeID: "node-1",
	})
	if err != nil {
		t.Fatalf("AppendAudit (first): %v", err)
	}
	if first.Seq != 1 || first.Hash == "" {
		t.Fatalf("AppendAudit returned Seq=%d Hash=%q, want Seq=1 + non-empty hash", first.Seq, first.Hash)
	}
	second, err := s.AppendAudit(ctx, tn, AuditEntry{
		Timestamp: ts.Add(time.Second), Actor: "operator:admin", Action: "passkey-disabled", NodeID: "node-1",
	})
	if err != nil {
		t.Fatalf("AppendAudit (second): %v", err)
	}
	if second.Seq != 2 || second.PrevHash != first.Hash {
		t.Fatalf("second entry Seq=%d PrevHash=%q, want Seq=2 chaining onto %q", second.Seq, second.PrevHash, first.Hash)
	}
	entries, err := s.ListAudit(ctx, tn)
	if err != nil {
		t.Fatalf("ListAudit: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("ListAudit len = %d, want 2", len(entries))
	}
	if entries[0].Action != "passkey-registered" || entries[1].Action != "passkey-disabled" {
		t.Fatalf("listed actions = [%q,%q], want [passkey-registered,passkey-disabled]",
			entries[0].Action, entries[1].Action)
	}
	if entries[0].Hash != first.Hash || entries[1].Hash != second.Hash {
		t.Fatal("listed hashes do not match the appended entries (durability/round-trip regression)")
	}
}
