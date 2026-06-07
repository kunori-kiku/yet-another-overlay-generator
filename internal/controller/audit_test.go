package controller

import (
	"context"
	"testing"
	"time"
)

// buildAuditChainViaStore appends n entries through a FileStore's AppendAudit (so
// the chain is produced by real Store wiring, not just chainAudit in isolation)
// and returns the listed entries in Seq order. FileStore uses t.TempDir(), so no
// real /var or /etc path is ever touched.
func buildAuditChainViaStore(t *testing.T, n int) []AuditEntry {
	t.Helper()
	ctx := context.Background()
	s, err := NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}
	const tn = TenantID("audit-tenant")
	base := time.Date(2026, 6, 8, 7, 0, 0, 0, time.UTC)
	for i := 0; i < n; i++ {
		if _, err := s.AppendAudit(ctx, tn, AuditEntry{
			Timestamp: base.Add(time.Duration(i) * time.Second),
			Actor:     "operator",
			Action:    "action-" + string(rune('A'+i)),
			NodeID:    "node-1",
		}); err != nil {
			t.Fatalf("AppendAudit[%d]: %v", i, err)
		}
	}
	entries, err := s.ListAudit(ctx, tn)
	if err != nil {
		t.Fatalf("ListAudit: %v", err)
	}
	if len(entries) != n {
		t.Fatalf("ListAudit len = %d, want %d", len(entries), n)
	}
	return entries
}

// TestVerifyAuditChainIntact asserts a chain built by the store verifies clean
// (VerifyAuditChain returns -1).
func TestVerifyAuditChainIntact(t *testing.T) {
	entries := buildAuditChainViaStore(t, 4)
	if bad := VerifyAuditChain(entries); bad != -1 {
		t.Fatalf("VerifyAuditChain(intact) = %d, want -1", bad)
	}
	// An empty chain is trivially intact.
	if bad := VerifyAuditChain(nil); bad != -1 {
		t.Fatalf("VerifyAuditChain(nil) = %d, want -1", bad)
	}
}

// TestVerifyAuditChainDetectsActionTamper mutates one entry's Action. The stored
// Hash no longer recomputes over the new canonical bytes, so VerifyAuditChain
// flags that entry's index.
func TestVerifyAuditChainDetectsActionTamper(t *testing.T) {
	entries := buildAuditChainViaStore(t, 4)
	const idx = 2

	// Sanity: clean before mutation.
	if bad := VerifyAuditChain(entries); bad != -1 {
		t.Fatalf("precondition VerifyAuditChain = %d, want -1", bad)
	}

	entries[idx].Action = "tampered-action"
	if bad := VerifyAuditChain(entries); bad != idx {
		t.Fatalf("VerifyAuditChain(action tamper at %d) = %d, want %d", idx, bad, idx)
	}
}

// TestVerifyAuditChainDetectsHashTamper overwrites one entry's stored Hash. The
// recompute over its (unchanged) canonical bytes no longer matches the forged
// Hash, so that same entry is flagged first.
func TestVerifyAuditChainDetectsHashTamper(t *testing.T) {
	entries := buildAuditChainViaStore(t, 4)
	const idx = 1

	if bad := VerifyAuditChain(entries); bad != -1 {
		t.Fatalf("precondition VerifyAuditChain = %d, want -1", bad)
	}

	entries[idx].Hash = "deadbeef"
	if bad := VerifyAuditChain(entries); bad != idx {
		t.Fatalf("VerifyAuditChain(hash tamper at %d) = %d, want %d", idx, bad, idx)
	}
}

// TestVerifyAuditChainViaChainAuditDirectly builds a chain with chainAudit alone
// (no Store) to assert the helper and verifier agree end to end, then mutates the
// first entry's Action to confirm index 0 is reported.
func TestVerifyAuditChainViaChainAuditDirectly(t *testing.T) {
	base := time.Date(2026, 6, 8, 6, 0, 0, 0, time.UTC)
	var chain []AuditEntry
	prev := ""
	for i := 0; i < 3; i++ {
		e := AuditEntry{
			Seq:       int64(i + 1),
			Timestamp: base.Add(time.Duration(i) * time.Second),
			Actor:     "operator",
			Action:    "act-" + string(rune('A'+i)),
			NodeID:    "node-1",
		}
		e = chainAudit(e, prev)
		prev = e.Hash
		chain = append(chain, e)
	}
	if bad := VerifyAuditChain(chain); bad != -1 {
		t.Fatalf("VerifyAuditChain(chainAudit-built) = %d, want -1", bad)
	}

	chain[0].Action = "forged"
	if bad := VerifyAuditChain(chain); bad != 0 {
		t.Fatalf("VerifyAuditChain(tamper at 0) = %d, want 0", bad)
	}
}
