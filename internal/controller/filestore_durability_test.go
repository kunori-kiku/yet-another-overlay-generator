package controller

// filestore_durability_test.go is the no-regression net for the B2 crash-consistency
// hardening (plan-8 Phase 7): writeJSONAtomic does CreateTemp+Write+Sync+Close then
// fsyncs the parent dir after the rename, AppendAudit now f.Sync()s before f.Close(), and
// the audit-log rotation/migration rewrite (writeAuditJSONL, via the shared writeBytesDurable
// primitive) is durable the same way (review #5). Durability itself is verified by
// code-review + crash-reasoning (an fsync's effect is not observable from a userspace
// round-trip); these tests pin that the added Sync/dir-fsync code paths introduce NO
// behavioral regression — the happy path still round-trips and the error paths (a missing
// parent dir) still surface a coded error and leave no orphan temp.

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestWriteJSONAtomicRoundTrip: a write into an existing dir lands durably-readable bytes
// and leaves no historical predictable .tmp sidecar.
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
	// The historical predictable temp sidecar must not be created.
	if _, err := os.Stat(path + ".tmp"); !os.IsNotExist(err) {
		t.Fatalf("leftover %s.tmp after a successful write (stat err = %v)", path, err)
	}
}

// TestWriteJSONAtomicMissingParentDir: with a parent dir that does NOT exist, custody
// validation fails before temp creation — the function must surface that error (not silently
// succeed) and not panic. The parent-dir fsync must not be reached on this error path.
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
// rename path keeps the 0600 mode (CreateTemp creates the temp 0600; the rename
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

// TestWriteAuditJSONLDurableRoundTrip: the rotation/migration rewrite primitive (now routed
// through writeBytesDurable, review #5) lands all entries durably-readable as JSONL, in order,
// and leaves no historical predictable .tmp sidecar. This is the no-regression net for moving writeAuditJSONL
// off the pre-B2 non-durable os.WriteFile onto the shared CreateTemp+Sync+Close+rename+dir-fsync
// path — a regression that dropped or reordered an entry would surface here.
func TestWriteAuditJSONLDurableRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, auditFileName)
	in := []AuditEntry{
		{Seq: 1, Actor: "operator:admin", Action: "node-approved", NodeID: "node-1"},
		{Seq: 2, Actor: "operator:admin", Action: "node-revoked", NodeID: "node-2"},
		{Seq: 3, Actor: "system", Action: "rotated", NodeID: ""},
	}
	if err := writeAuditJSONL(path, in); err != nil {
		t.Fatalf("writeAuditJSONL: %v", err)
	}
	got, torn, err := readAuditJSONL(path)
	if err != nil {
		t.Fatalf("readAuditJSONL: %v", err)
	}
	if torn {
		t.Fatal("readAuditJSONL reported a torn tail after a clean durable rewrite")
	}
	if len(got) != len(in) {
		t.Fatalf("round-trip len = %d, want %d", len(got), len(in))
	}
	for i := range in {
		if got[i].Seq != in[i].Seq || got[i].Action != in[i].Action || got[i].NodeID != in[i].NodeID {
			t.Fatalf("entry %d round-trip = %+v, want %+v", i, got[i], in[i])
		}
	}
	// The historical predictable temp sidecar must not be created.
	if _, err := os.Stat(path + ".tmp"); !os.IsNotExist(err) {
		t.Fatalf("leftover %s.tmp after a successful rewrite (stat err = %v)", path, err)
	}
}

// TestWriteAuditJSONLMissingParentDir: with a parent dir that does NOT exist, the temp-file
// custody validation inside writeBytesDurable fails — writeAuditJSONL must surface that error (not
// silently succeed) and not leak a temp file. This is the error-path no-regression net for
// the rotation/migration rewrite after it was moved onto the durable primitive (review #5).
func TestWriteAuditJSONLMissingParentDir(t *testing.T) {
	dir := t.TempDir()
	// "absent" is never created, so <absent>/audit.jsonl has no parent dir.
	path := filepath.Join(dir, "absent", auditFileName)
	if err := writeAuditJSONL(path, []AuditEntry{{Seq: 1, Action: "x"}}); err == nil {
		t.Fatal("writeAuditJSONL into a missing parent dir = nil, want an error")
	}
	if _, err := os.Stat(path + ".tmp"); !os.IsNotExist(err) {
		t.Fatalf("unexpected leftover temp file (stat err = %v)", err)
	}
}

// TestRotateAuditDurableTrim: the rotation path (rotateAudit, which rewrites the log via the
// now-durable writeAuditJSONL) trims an over-cap log down to the most-recent auditRetain
// entries, durably, and updates the cached count. It seeds auditRotateAt+1 entries directly on
// disk (bypassing the slow per-append cap so the test stays fast), then rotates and verifies
// the trimmed window is the newest auditRetain entries and is durably re-readable with no torn
// tail or orphan temp. This is the no-regression net for the rotation rewrite being durable
// (review #5) — a rewrite that lost the trimmed prefix or left a torn file would surface here.
func TestRotateAuditDurableTrim(t *testing.T) {
	fs, err := NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}
	const tn = TenantID("rotate-tenant")
	dir, err := fs.ensureTenantDir(tn)
	if err != nil {
		t.Fatalf("ensureTenantDir: %v", err)
	}

	// Build a clean, chained over-cap log and write it directly (the per-append path would
	// take auditRotateAt+1 AppendAudit calls — too slow for a unit test).
	total := auditRotateAt + 1
	entries := make([]AuditEntry, 0, total)
	prevHash := ""
	for i := 0; i < total; i++ {
		e := AuditEntry{Seq: int64(i + 1), Actor: "system", Action: "seed", NodeID: ""}
		e = chainAudit(e, prevHash)
		prevHash = e.Hash
		entries = append(entries, e)
	}
	path := filepath.Join(dir, auditFileName)
	if err := writeAuditJSONL(path, entries); err != nil {
		t.Fatalf("seed writeAuditJSONL: %v", err)
	}

	tail := &auditTail{count: total, seq: entries[total-1].Seq, hash: entries[total-1].Hash}
	if err := fs.rotateAudit(dir, tail); err != nil {
		t.Fatalf("rotateAudit: %v", err)
	}
	if tail.count != auditRetain {
		t.Fatalf("rotated tail.count = %d, want %d", tail.count, auditRetain)
	}

	got, torn, err := readAuditJSONL(path)
	if err != nil {
		t.Fatalf("readAuditJSONL after rotate: %v", err)
	}
	if torn {
		t.Fatal("readAuditJSONL reported a torn tail after a clean durable rotation rewrite")
	}
	if len(got) != auditRetain {
		t.Fatalf("on-disk entry count after rotate = %d, want %d", len(got), auditRetain)
	}
	// The kept window must be the newest auditRetain entries: first kept Seq is (total-auditRetain+1),
	// last kept Seq is total.
	wantFirstSeq := int64(total - auditRetain + 1)
	if got[0].Seq != wantFirstSeq {
		t.Fatalf("first kept Seq = %d, want %d (newest %d retained)", got[0].Seq, wantFirstSeq, auditRetain)
	}
	if got[len(got)-1].Seq != int64(total) {
		t.Fatalf("last kept Seq = %d, want %d", got[len(got)-1].Seq, total)
	}
	if _, err := os.Stat(path + ".tmp"); !os.IsNotExist(err) {
		t.Fatalf("leftover %s.tmp after rotation (stat err = %v)", path, err)
	}
}
