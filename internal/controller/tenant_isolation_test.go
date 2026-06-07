package controller

import (
	"context"
	"errors"
	"testing"
	"time"
)

// TestTenantIsolation is the perpetual cross-tenant gate: everything written
// under tenant "a" must be invisible under tenant "b" for BOTH Store impls. This
// guards the structural tenant-isolation invariant (every Store method takes a
// TenantID predicate) against any implementation that leaks across tenants.
func TestTenantIsolation(t *testing.T) {
	const (
		tenantA = TenantID("tenant-a")
		tenantB = TenantID("tenant-b")
	)

	for _, impl := range storeImpls() {
		impl := impl
		t.Run(impl.name, func(t *testing.T) {
			ctx := context.Background()
			s := impl.factory(t)

			// Populate every kind of record under tenant A.
			if err := s.UpsertNode(ctx, tenantA, Node{NodeID: "alpha", Status: NodeApproved}); err != nil {
				t.Fatalf("UpsertNode(A): %v", err)
			}
			if _, err := s.PutTopology(ctx, tenantA, []byte(`{"owner":"a"}`)); err != nil {
				t.Fatalf("PutTopology(A): %v", err)
			}
			if err := s.StageBundle(ctx, tenantA, SignedBundle{
				NodeID:     "alpha",
				Generation: 1,
				Files:      map[string][]byte{"install.sh": []byte("a")},
				IsStaged:   true,
			}); err != nil {
				t.Fatalf("StageBundle(A): %v", err)
			}
			if _, err := s.PromoteStaged(ctx, tenantA); err != nil {
				t.Fatalf("PromoteStaged(A): %v", err)
			}
			if _, err := s.AppendAudit(ctx, tenantA, AuditEntry{
				Timestamp: time.Date(2026, 6, 8, 0, 0, 0, 0, time.UTC),
				Actor:     "operator-a",
				Action:    "promote",
				NodeID:    "alpha",
			}); err != nil {
				t.Fatalf("AppendAudit(A): %v", err)
			}
			if err := s.CreateEnrollmentToken(ctx, tenantA, EnrollmentToken{
				TokenHash: tokenHash("a-secret"),
				NodeID:    "alpha",
				ExpiresAt: time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC),
			}); err != nil {
				t.Fatalf("CreateEnrollmentToken(A): %v", err)
			}

			// Tenant B must see nothing: point reads -> ErrNotFound.
			if _, err := s.GetNode(ctx, tenantB, "alpha"); !errors.Is(err, ErrNotFound) {
				t.Fatalf("GetNode(B, alpha): err = %v, want ErrNotFound", err)
			}
			if _, err := s.GetTopology(ctx, tenantB); !errors.Is(err, ErrNotFound) {
				t.Fatalf("GetTopology(B): err = %v, want ErrNotFound", err)
			}
			if _, err := s.GetCurrentBundle(ctx, tenantB, "alpha"); !errors.Is(err, ErrNotFound) {
				t.Fatalf("GetCurrentBundle(B, alpha): err = %v, want ErrNotFound", err)
			}

			// Tenant B must see nothing: list reads -> empty.
			nodes, err := s.ListNodes(ctx, tenantB)
			if err != nil {
				t.Fatalf("ListNodes(B): %v", err)
			}
			if len(nodes) != 0 {
				t.Fatalf("ListNodes(B) = %v, want empty", nodes)
			}
			audit, err := s.ListAudit(ctx, tenantB)
			if err != nil {
				t.Fatalf("ListAudit(B): %v", err)
			}
			if len(audit) != 0 {
				t.Fatalf("ListAudit(B) = %v, want empty", audit)
			}

			// Tenant B's generation must be untouched by tenant A's promote.
			if gen, err := s.CurrentGeneration(ctx, tenantB); err != nil || gen != 0 {
				t.Fatalf("CurrentGeneration(B) = (%d, %v), want (0, nil)", gen, err)
			}

			// Tenant B must NOT be able to consume tenant A's enrollment token, even
			// with the exact hash + nodeID (the token lives only under tenant A).
			at := time.Date(2026, 6, 8, 0, 0, 0, 0, time.UTC)
			if err := s.ConsumeEnrollmentToken(ctx, tenantB, tokenHash("a-secret"), "alpha", at); !errors.Is(err, ErrTokenInvalid) {
				t.Fatalf("ConsumeEnrollmentToken(B, A's token): err = %v, want ErrTokenInvalid", err)
			}

			// Sanity: tenant A still sees its own data (isolation is symmetric, not
			// a blanket wipe).
			if _, err := s.GetNode(ctx, tenantA, "alpha"); err != nil {
				t.Fatalf("GetNode(A, alpha) after isolation checks: %v", err)
			}
			if gen, err := s.CurrentGeneration(ctx, tenantA); err != nil || gen != 1 {
				t.Fatalf("CurrentGeneration(A) = (%d, %v), want (1, nil)", gen, err)
			}
			// ...and tenant A CAN consume its own token (the gate isolates, it does
			// not break the owning tenant).
			if err := s.ConsumeEnrollmentToken(ctx, tenantA, tokenHash("a-secret"), "alpha", at); err != nil {
				t.Fatalf("ConsumeEnrollmentToken(A, A's token): %v", err)
			}
		})
	}
}
