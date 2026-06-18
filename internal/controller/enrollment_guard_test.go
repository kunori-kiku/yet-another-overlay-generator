package controller

// enrollment_guard_test.go — beta.8 S4/S5 enrollment-lifecycle guards. A REVOKED node-id
// must NOT be silently resurrected by a still-valid enrollment token (ErrNodeRevoked +
// audited refusal); a re-enroll over an APPROVED node (legitimate reinstall) stays allowed
// but is recorded with a DISTINCT audit action so the identity overwrite is never silent;
// and revoke purges a node's outstanding enrollment tokens. Runs against both Store impls.

import (
	"context"
	"errors"
	"testing"
	"time"
)

// auditHas reports whether an audit entry with the given action + node-id exists.
func auditHas(t *testing.T, ctx context.Context, s Store, action, nodeID string) bool {
	t.Helper()
	entries, err := s.ListAudit(ctx, tenant)
	if err != nil {
		t.Fatalf("ListAudit: %v", err)
	}
	for _, e := range entries {
		if e.Action == action && e.NodeID == nodeID {
			return true
		}
	}
	return false
}

// TestEnroll_RefusesRevokedNodeResurrection: enroll node-a, flip it to revoked, then a
// fresh-token re-enroll is refused with ErrNodeRevoked, audited, and does not overwrite the
// revoked record.
func TestEnroll_RefusesRevokedNodeResurrection(t *testing.T) {
	for _, impl := range storeImpls() {
		impl := impl
		t.Run(impl.name, func(t *testing.T) {
			ctx := context.Background()
			s := impl.factory(t)

			if err := mintAndEnroll(t, ctx, s, tenant, "node-a", freshPub(t)); err != nil {
				t.Fatalf("first enroll: %v", err)
			}
			// Simulate revoke's status flip (the lifecycle the guard keys on).
			n, err := s.GetNode(ctx, tenant, "node-a")
			if err != nil {
				t.Fatalf("GetNode: %v", err)
			}
			n.Status = NodeRevoked
			if err := s.UpsertNode(ctx, tenant, n); err != nil {
				t.Fatalf("UpsertNode revoked: %v", err)
			}

			err = mintAndEnroll(t, ctx, s, tenant, "node-a", freshPub(t))
			if !errors.Is(err, ErrNodeRevoked) {
				t.Fatalf("re-enroll of revoked node: err = %v, want ErrNodeRevoked", err)
			}
			after, err := s.GetNode(ctx, tenant, "node-a")
			if err != nil {
				t.Fatalf("GetNode after: %v", err)
			}
			if after.Status != NodeRevoked {
				t.Errorf("node status = %q after refused re-enroll, want revoked (not resurrected)", after.Status)
			}
			if !auditHas(t, ctx, s, "enroll-rejected-revoked", "node-a") {
				t.Errorf("missing enroll-rejected-revoked audit for node-a")
			}
		})
	}
}

// TestEnroll_ReenrollApprovedIsAudited: re-enrolling an APPROVED node (legit reinstall) is
// allowed, updates the key, and records the distinct "enroll-reenroll-approved" action.
func TestEnroll_ReenrollApprovedIsAudited(t *testing.T) {
	for _, impl := range storeImpls() {
		impl := impl
		t.Run(impl.name, func(t *testing.T) {
			ctx := context.Background()
			s := impl.factory(t)

			if err := mintAndEnroll(t, ctx, s, tenant, "node-a", freshPub(t)); err != nil {
				t.Fatalf("first enroll: %v", err)
			}
			newPub := freshPub(t)
			if err := mintAndEnroll(t, ctx, s, tenant, "node-a", newPub); err != nil {
				t.Fatalf("re-enroll approved node: %v", err)
			}
			after, err := s.GetNode(ctx, tenant, "node-a")
			if err != nil {
				t.Fatalf("GetNode: %v", err)
			}
			if after.WGPublicKey != newPub {
				t.Errorf("re-enroll did not update pubkey: got %q, want %q", after.WGPublicKey, newPub)
			}
			if !auditHas(t, ctx, s, "enroll-reenroll-approved", "node-a") {
				t.Errorf("missing enroll-reenroll-approved audit for node-a")
			}
		})
	}
}

// TestPurgeEnrollmentTokensForNode: purge removes only the target node's tokens, returns
// the count, leaves other nodes' tokens consumable, and is idempotent.
func TestPurgeEnrollmentTokensForNode(t *testing.T) {
	for _, impl := range storeImpls() {
		impl := impl
		t.Run(impl.name, func(t *testing.T) {
			ctx := context.Background()
			s := impl.factory(t)
			now := time.Now()

			mkTok := func(node string) string {
				pt, tok := NewEnrollmentToken(node, time.Hour, now)
				if err := s.CreateEnrollmentToken(ctx, tenant, tok); err != nil {
					t.Fatalf("CreateEnrollmentToken(%s): %v", node, err)
				}
				return pt
			}
			_ = mkTok("node-a")
			_ = mkTok("node-a")
			ptB := mkTok("node-b")

			removed, err := s.PurgeEnrollmentTokensForNode(ctx, tenant, "node-a")
			if err != nil {
				t.Fatalf("Purge: %v", err)
			}
			if removed != 2 {
				t.Errorf("purged %d tokens for node-a, want 2", removed)
			}
			// node-b's token survives (still consumable).
			if err := s.ConsumeEnrollmentToken(ctx, tenant, HashToken(ptB), "node-b", now); err != nil {
				t.Errorf("node-b token should survive a node-a purge: %v", err)
			}
			// Idempotent: a second purge removes zero.
			removed, err = s.PurgeEnrollmentTokensForNode(ctx, tenant, "node-a")
			if err != nil {
				t.Fatalf("Purge (2nd): %v", err)
			}
			if removed != 0 {
				t.Errorf("second purge removed %d, want 0", removed)
			}
		})
	}
}
