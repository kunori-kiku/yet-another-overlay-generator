package controller

// enrollment_dedupe_test.go — PERPETUAL guard pinning the identity principle
// (controller-server-authority-redesign plan-6): one APPROVED WireGuard public key
// binds to exactly one node-id. Enrolling a pubkey already approved under a DIFFERENT
// node-id is refused (ErrDuplicateWGKey, 409 at the HTTP layer) — the
// duplicate-fleet-rows vector. Same-id re-enroll (a reinstalled host with a fresh
// token) stays allowed. Never retire this test.

import (
	"context"
	"errors"
	"testing"
	"time"

	"golang.zx2c4.com/wireguard/wgctrl/wgtypes"
)

// mintAndEnroll runs the operator-side token mint + the agent-side Enroll for one
// node, returning the Enroll error (nil on success).
func mintAndEnroll(t *testing.T, ctx context.Context, s Store, tnt TenantID, nodeID, pub string) error {
	t.Helper()
	plaintext, tok := NewEnrollmentToken(nodeID, time.Hour, time.Now())
	if err := s.CreateEnrollmentToken(ctx, tnt, tok); err != nil {
		t.Fatalf("CreateEnrollmentToken(%s): %v", nodeID, err)
	}
	_, err := Enroll(ctx, s, tnt, EnrollRequest{Token: plaintext, NodeID: nodeID, WGPublicKey: pub}, time.Now())
	return err
}

func freshPub(t *testing.T) string {
	t.Helper()
	k, err := wgtypes.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("GeneratePrivateKey: %v", err)
	}
	return k.PublicKey().String()
}

// TestEnrollDedupe_RejectsSamePubkeyDifferentNode: enrolling pubkey P under node-A
// succeeds; enrolling the SAME P under node-B is refused with ErrDuplicateWGKey and
// leaves node-B unregistered + an audit entry.
func TestEnrollDedupe_RejectsSamePubkeyDifferentNode(t *testing.T) {
	for _, impl := range storeImpls() {
		impl := impl
		t.Run(impl.name, func(t *testing.T) {
			ctx := context.Background()
			s := impl.factory(t)
			pub := freshPub(t)

			if err := mintAndEnroll(t, ctx, s, tenant, "node-a", pub); err != nil {
				t.Fatalf("first enroll (node-a): %v", err)
			}
			err := mintAndEnroll(t, ctx, s, tenant, "node-b", pub)
			if !errors.Is(err, ErrDuplicateWGKey) {
				t.Fatalf("enroll node-b with node-a's pubkey: err = %v, want ErrDuplicateWGKey", err)
			}
			// node-b must not have been registered.
			if _, err := s.GetNode(ctx, tenant, "node-b"); !errors.Is(err, ErrNotFound) {
				t.Errorf("node-b was registered despite the duplicate-key refusal (err=%v)", err)
			}
			// The refusal is audited.
			entries, err := s.ListAudit(ctx, tenant)
			if err != nil {
				t.Fatalf("ListAudit: %v", err)
			}
			found := false
			for _, e := range entries {
				if e.Action == "enroll-rejected-duplicate-key" && e.NodeID == "node-b" {
					found = true
				}
			}
			if !found {
				t.Errorf("no enroll-rejected-duplicate-key audit entry for node-b")
			}
		})
	}
}

// TestEnrollDedupe_SameNodeReenrollAllowed: re-enrolling the SAME node-id (reinstalled
// host) with a fresh token is allowed — whether it reuses its old pubkey or presents a
// new one. The dedupe matches pubkey-equal AND id-different, so same-id never trips it.
func TestEnrollDedupe_SameNodeReenrollAllowed(t *testing.T) {
	for _, impl := range storeImpls() {
		impl := impl
		t.Run(impl.name, func(t *testing.T) {
			ctx := context.Background()
			s := impl.factory(t)
			pub := freshPub(t)

			if err := mintAndEnroll(t, ctx, s, tenant, "node-a", pub); err != nil {
				t.Fatalf("first enroll: %v", err)
			}
			// Same id, same pubkey (key persisted across reinstall) → allowed.
			if err := mintAndEnroll(t, ctx, s, tenant, "node-a", pub); err != nil {
				t.Fatalf("same-id same-key re-enroll: err = %v, want nil", err)
			}
			// Same id, NEW pubkey (fresh key on reinstall) → allowed.
			if err := mintAndEnroll(t, ctx, s, tenant, "node-a", freshPub(t)); err != nil {
				t.Fatalf("same-id new-key re-enroll: err = %v, want nil", err)
			}
		})
	}
}

// TestEnrollDedupe_WhitespaceCannotEvade: a pubkey padded with surrounding
// whitespace must not slip past the dedupe (the compare is whitespace-insensitive).
func TestEnrollDedupe_WhitespaceCannotEvade(t *testing.T) {
	for _, impl := range storeImpls() {
		impl := impl
		t.Run(impl.name, func(t *testing.T) {
			ctx := context.Background()
			s := impl.factory(t)
			pub := freshPub(t)

			if err := mintAndEnroll(t, ctx, s, tenant, "node-a", pub); err != nil {
				t.Fatalf("first enroll: %v", err)
			}
			// Same key, padded — must still be rejected under a different id.
			if err := mintAndEnroll(t, ctx, s, tenant, "node-b", "  "+pub+"\n"); !errors.Is(err, ErrDuplicateWGKey) {
				t.Fatalf("padded duplicate enroll: err = %v, want ErrDuplicateWGKey", err)
			}
		})
	}
}

// TestRekeyDedupe: Rekey enforces the same identity invariant as Enroll — rotating a
// node's key TO another approved node's key is refused; a fresh key (or the node's own
// key) is allowed. Also covers ErrNotFound for an unknown node.
func TestRekeyDedupe(t *testing.T) {
	for _, impl := range storeImpls() {
		impl := impl
		t.Run(impl.name, func(t *testing.T) {
			ctx := context.Background()
			s := impl.factory(t)
			ka, kb := freshPub(t), freshPub(t)

			if err := mintAndEnroll(t, ctx, s, tenant, "node-a", ka); err != nil {
				t.Fatalf("enroll node-a: %v", err)
			}
			if err := mintAndEnroll(t, ctx, s, tenant, "node-b", kb); err != nil {
				t.Fatalf("enroll node-b: %v", err)
			}

			// Rekey node-b TO node-a's key → refused.
			if err := Rekey(ctx, s, tenant, "node-b", ka, time.Now()); !errors.Is(err, ErrDuplicateWGKey) {
				t.Fatalf("rekey node-b to node-a's key: err = %v, want ErrDuplicateWGKey", err)
			}
			// node-b's stored key is unchanged (the refusal did not write).
			if n, err := s.GetNode(ctx, tenant, "node-b"); err != nil || n.WGPublicKey != kb {
				t.Fatalf("node-b key after refused rekey = %q (err %v), want unchanged %q", n.WGPublicKey, err, kb)
			}
			// Rekey node-b to a FRESH key → allowed, clears RekeyRequested.
			kbNew := freshPub(t)
			if err := Rekey(ctx, s, tenant, "node-b", kbNew, time.Now()); err != nil {
				t.Fatalf("rekey node-b to a fresh key: %v", err)
			}
			if n, err := s.GetNode(ctx, tenant, "node-b"); err != nil || n.WGPublicKey != kbNew {
				t.Fatalf("node-b key after rekey = %q (err %v), want %q", n.WGPublicKey, err, kbNew)
			}
			// Rekey an unknown node → ErrNotFound.
			if err := Rekey(ctx, s, tenant, "ghost", freshPub(t), time.Now()); !errors.Is(err, ErrNotFound) {
				t.Fatalf("rekey unknown node: err = %v, want ErrNotFound", err)
			}
		})
	}
}

// TestEnrollDedupe_RevokedKeyFreesTheBinding: a revoked node's key no longer blocks
// re-use under a new id — dedupe checks only APPROVED nodes (revoke is the operator's
// way to free a key for re-binding, D10's manual path).
func TestEnrollDedupe_RevokedKeyFreesTheBinding(t *testing.T) {
	for _, impl := range storeImpls() {
		impl := impl
		t.Run(impl.name, func(t *testing.T) {
			ctx := context.Background()
			s := impl.factory(t)
			pub := freshPub(t)

			if err := mintAndEnroll(t, ctx, s, tenant, "node-a", pub); err != nil {
				t.Fatalf("first enroll: %v", err)
			}
			// Revoke node-a (status → revoked).
			n, err := s.GetNode(ctx, tenant, "node-a")
			if err != nil {
				t.Fatalf("GetNode(node-a): %v", err)
			}
			n.Status = NodeRevoked
			if err := s.UpsertNode(ctx, tenant, n); err != nil {
				t.Fatalf("revoke node-a: %v", err)
			}
			// node-b may now take the freed key.
			if err := mintAndEnroll(t, ctx, s, tenant, "node-b", pub); err != nil {
				t.Fatalf("enroll node-b after node-a revoked: err = %v, want nil", err)
			}
		})
	}
}
