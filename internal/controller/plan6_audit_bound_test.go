package controller

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// appendN appends n audit entries to store under tenant tn and returns the last stored
// entry. Timestamps are deterministic (no wall clock) so the test is reproducible.
func appendN(t *testing.T, store Store, tn TenantID, n int) AuditEntry {
	t.Helper()
	ctx := context.Background()
	base := time.Date(2026, 6, 15, 0, 0, 0, 0, time.UTC)
	var last AuditEntry
	for i := 0; i < n; i++ {
		e, err := store.AppendAudit(ctx, tn, AuditEntry{
			Timestamp: base.Add(time.Duration(i) * time.Second),
			Actor:     "operator",
			Action:    "act",
			NodeID:    "node-1",
		})
		if err != nil {
			t.Fatalf("AppendAudit[%d]: %v", i, err)
		}
		last = e
	}
	return last
}

// TestAuditLogBoundedAndChainedAfterRotation drives BOTH stores past the rotation
// high-water mark and asserts the plan-6 contract: the log is bounded to auditRetain, Seq
// stays monotonic across the rotation, and the retained window still verifies clean under
// VerifyAuditChain's first-entry anchoring (the dropped prefix does not break the chain).
func TestAuditLogBoundedAndChainedAfterRotation(t *testing.T) {
	ctx := context.Background()
	const tn = TenantID("bound-tenant")

	stores := map[string]Store{}
	fs, err := NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}
	stores["FileStore"] = fs
	stores["MemStore"] = NewMemStore()

	for name, store := range stores {
		t.Run(name, func(t *testing.T) {
			last := appendN(t, store, tn, auditRotateAt+1) // one past the high-water mark → one rotation

			entries, err := store.ListAudit(ctx, tn)
			if err != nil {
				t.Fatalf("ListAudit: %v", err)
			}
			// Bounded: trimmed to auditRetain (never grows past auditRotateAt).
			if len(entries) != auditRetain {
				t.Fatalf("after rotation len = %d, want %d", len(entries), auditRetain)
			}
			// Seq is monotonic and reflects ALL appends (the dropped prefix still consumed Seq).
			if last.Seq != int64(auditRotateAt+1) {
				t.Fatalf("last Seq = %d, want %d", last.Seq, auditRotateAt+1)
			}
			if entries[len(entries)-1].Seq != last.Seq {
				t.Fatalf("listed last Seq = %d, want %d", entries[len(entries)-1].Seq, last.Seq)
			}
			// The first retained entry has a NON-empty PrevHash (its predecessor was trimmed),
			// yet the retained window verifies clean under first-entry anchoring.
			if entries[0].PrevHash == "" {
				t.Errorf("expected a non-genesis first entry after rotation; PrevHash is empty")
			}
			if bad := VerifyAuditChain(entries); bad != -1 {
				t.Fatalf("VerifyAuditChain(rotated) = %d, want -1", bad)
			}
		})
	}
}

// TestFileStoreAuditAppendIsO1NoFullRewrite asserts the FileStore append path is JSONL and
// does NOT keep a full-array audit.json around: after a handful of appends the on-disk log
// is audit.jsonl with one line per entry, and the legacy audit.json is absent.
func TestFileStoreAuditAppendIsJSONL(t *testing.T) {
	root := t.TempDir()
	fs, err := NewFileStore(root)
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}
	const tn = TenantID("jsonl-tenant")
	appendN(t, fs, tn, 5)

	dir := filepath.Join(root, string(tn))
	if _, err := os.Stat(filepath.Join(dir, auditFileName)); err != nil {
		t.Fatalf("expected %s to exist: %v", auditFileName, err)
	}
	if _, err := os.Stat(filepath.Join(dir, legacyAuditFileName)); !os.IsNotExist(err) {
		t.Errorf("legacy %s must not be written by the append path (err=%v)", legacyAuditFileName, err)
	}
	// The JSONL file has exactly one line per entry.
	parsed, err := readAuditJSONL(filepath.Join(dir, auditFileName))
	if err != nil {
		t.Fatalf("readAuditJSONL: %v", err)
	}
	if len(parsed) != 5 {
		t.Fatalf("JSONL lines = %d, want 5", len(parsed))
	}
}

// TestFileStoreAuditLegacyMigration writes a legacy audit.json array, then appends through a
// fresh FileStore and asserts the one-time migration: the legacy history is preserved, the
// new entry continues the chain, audit.jsonl now holds everything, and audit.json is gone.
func TestFileStoreAuditLegacyMigration(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	fs, err := NewFileStore(root)
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}
	const tn = TenantID("legacy-tenant")
	dir, err := fs.ensureTenantDir(tn)
	if err != nil {
		t.Fatalf("ensureTenantDir: %v", err)
	}

	// Hand-craft a valid legacy chain (the old store wrote a JSON array via writeJSONAtomic).
	base := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	var legacy []AuditEntry
	prev := ""
	for i := 0; i < 3; i++ {
		e := AuditEntry{Seq: int64(i + 1), Timestamp: base.Add(time.Duration(i) * time.Second), Actor: "operator", Action: "legacy", NodeID: "node-1"}
		e = chainAudit(e, prev)
		prev = e.Hash
		legacy = append(legacy, e)
	}
	if err := writeJSONAtomic(filepath.Join(dir, legacyAuditFileName), legacy); err != nil {
		t.Fatalf("seed legacy audit.json: %v", err)
	}

	// First append migrates legacy → JSONL and continues the chain.
	if _, err := fs.AppendAudit(ctx, tn, AuditEntry{Timestamp: base.Add(10 * time.Second), Actor: "operator", Action: "new", NodeID: "node-1"}); err != nil {
		t.Fatalf("AppendAudit (post-migration): %v", err)
	}

	entries, err := fs.ListAudit(ctx, tn)
	if err != nil {
		t.Fatalf("ListAudit: %v", err)
	}
	if len(entries) != 4 {
		t.Fatalf("migrated history len = %d, want 4 (3 legacy + 1 new)", len(entries))
	}
	if entries[3].Seq != 4 {
		t.Errorf("new entry Seq = %d, want 4 (continues legacy)", entries[3].Seq)
	}
	if bad := VerifyAuditChain(entries); bad != -1 {
		t.Fatalf("VerifyAuditChain(migrated) = %d, want -1", bad)
	}
	if _, err := os.Stat(filepath.Join(dir, auditFileName)); err != nil {
		t.Errorf("expected %s after migration: %v", auditFileName, err)
	}
	if _, err := os.Stat(filepath.Join(dir, legacyAuditFileName)); !os.IsNotExist(err) {
		t.Errorf("legacy %s must be removed after migration (err=%v)", legacyAuditFileName, err)
	}
}
