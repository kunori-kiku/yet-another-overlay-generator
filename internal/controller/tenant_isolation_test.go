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
			// Pin a keystone credential and a SIGNED staged trust-list under A before the promote, so
			// the promote populates A's SERVED trust-list slot — letting B's served reads below assert
			// the served slot is tenant-scoped too.
			if err := s.CompareAndSetOperatorCredential(ctx, tenantA, nil, OperatorCredential{Alg: "ed25519", PublicKeyPEM: "pub-a-keystone"}); err != nil {
				t.Fatalf("CompareAndSetOperatorCredential(A): %v", err)
			}
			if err := s.PutSignedTrustList(ctx, tenantA, StoredTrustList{
				TrustListJSON: []byte(`{"epoch":1,"members":[{"node_id":"alpha"}]}` + "\n"),
				SignatureJSON: []byte(`{"alg":"ed25519","signature":"sig-a"}`),
				Epoch:         1,
			}); err != nil {
				t.Fatalf("PutSignedTrustList(A): %v", err)
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
			if err := s.CreatePendingKeystoneTransition(ctx, tenantA, PendingKeystoneTransition{
				Expected: &OperatorCredential{Alg: "ed25519", PublicKeyPEM: "pub-a-keystone"},
				Next:     OperatorCredential{Alg: "ed25519", PublicKeyPEM: "pub-a-next"},
				Audit: AuditEntry{
					Timestamp: time.Date(2026, 6, 8, 0, 1, 0, 0, time.UTC),
					Actor:     "operator-a",
					Action:    "rotate-operator-credential",
					EventID:   "0000000000000000000000000000000a",
				},
			}); err != nil {
				t.Fatalf("CreatePendingKeystoneTransition(A): %v", err)
			}
			if err := s.CreateEnrollmentToken(ctx, tenantA, EnrollmentToken{
				TokenHash: tokenHash("a-secret"),
				NodeID:    "alpha",
				ExpiresAt: time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC),
			}); err != nil {
				t.Fatalf("CreateEnrollmentToken(A): %v", err)
			}
			if err := s.IssueNodeAPIToken(ctx, tenantA, "alpha", tokenHash("a-api-secret")); err != nil {
				t.Fatalf("IssueNodeAPIToken(A): %v", err)
			}
			if err := s.PutOperator(ctx, tenantA, Operator{
				Username:     "admin",
				PasswordHash: "$argon2id$v=19$m=65536,t=3,p=1$c2FsdHNhbHQ$aGFzaGhhc2g",
			}); err != nil {
				t.Fatalf("PutOperator(A): %v", err)
			}
			if err := s.CreateSession(ctx, tenantA, Session{
				TokenHash: tokenHash("a-session"),
				Operator:  "admin",
				ExpiresAt: time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC),
			}); err != nil {
				t.Fatalf("CreateSession(A): %v", err)
			}
			if err := s.PutSettings(ctx, tenantA, ControllerSettings{PublicAgentURL: "https://a"}); err != nil {
				t.Fatalf("PutSettings(A): %v", err)
			}
			if err := s.PutSigningAnchor(ctx, tenantA, SigningAnchor{PubKeyPEM: "pub-a"}); err != nil {
				t.Fatalf("PutSigningAnchor(A): %v", err)
			}

			// Tenant B must see nothing: point reads -> ErrNotFound.
			if _, err := s.GetNode(ctx, tenantB, "alpha"); !errors.Is(err, ErrNotFound) {
				t.Fatalf("GetNode(B, alpha): err = %v, want ErrNotFound", err)
			}
			if _, err := s.GetTopology(ctx, tenantB); !errors.Is(err, ErrNotFound) {
				t.Fatalf("GetTopology(B): err = %v, want ErrNotFound", err)
			}
			// Topology version history (plan-2) is tenant-scoped like everything else.
			if _, err := s.GetTopologyVersion(ctx, tenantB, 1); !errors.Is(err, ErrNotFound) {
				t.Fatalf("GetTopologyVersion(B, 1): err = %v, want ErrNotFound", err)
			}
			if versions, err := s.ListTopologyVersions(ctx, tenantB); err != nil || len(versions) != 0 {
				t.Fatalf("ListTopologyVersions(B) = %v, %v; want empty, nil", versions, err)
			}
			if _, err := s.GetCurrentBundle(ctx, tenantB, "alpha"); !errors.Is(err, ErrNotFound) {
				t.Fatalf("GetCurrentBundle(B, alpha): err = %v, want ErrNotFound", err)
			}
			// The keystone is tenant-scoped: B sees neither A's pinned credential, A's staged
			// trust-list, A's served trust-list, nor a served config for A's node.
			if _, err := s.GetOperatorCredential(ctx, tenantB); !errors.Is(err, ErrNotFound) {
				t.Fatalf("GetOperatorCredential(B): err = %v, want ErrNotFound", err)
			}
			if _, err := s.GetPendingKeystoneTransition(ctx, tenantB); !errors.Is(err, ErrNotFound) {
				t.Fatalf("GetPendingKeystoneTransition(B): err = %v, want ErrNotFound", err)
			}
			if _, err := s.GetCurrentSignedTrustList(ctx, tenantB); !errors.Is(err, ErrNotFound) {
				t.Fatalf("GetCurrentSignedTrustList(B): err = %v, want ErrNotFound", err)
			}
			if _, err := s.GetLastStagedTrustList(ctx, tenantB); !errors.Is(err, ErrNotFound) {
				t.Fatalf("GetLastStagedTrustList(B): err = %v, want ErrNotFound", err)
			}
			if _, err := s.GetServedTrustList(ctx, tenantB); !errors.Is(err, ErrNotFound) {
				t.Fatalf("GetServedTrustList(B): err = %v, want ErrNotFound", err)
			}
			if _, err := s.GetServedConfig(ctx, tenantB, "alpha"); !errors.Is(err, ErrNotFound) {
				t.Fatalf("GetServedConfig(B, alpha): err = %v, want ErrNotFound", err)
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

			// Tenant B must NOT be able to resolve tenant A's node API token, even
			// with the exact hash (the reverse index lives only under tenant A).
			if _, err := s.LookupNodeByAPIToken(ctx, tenantB, tokenHash("a-api-secret")); !errors.Is(err, ErrTokenInvalid) {
				t.Fatalf("LookupNodeByAPIToken(B, A's api token): err = %v, want ErrTokenInvalid", err)
			}

			// Tenant B must not see tenant A's operator account or login session.
			if _, err := s.GetOperator(ctx, tenantB, "admin"); !errors.Is(err, ErrNotFound) {
				t.Fatalf("GetOperator(B, admin): err = %v, want ErrNotFound", err)
			}
			ops, err := s.ListOperators(ctx, tenantB)
			if err != nil {
				t.Fatalf("ListOperators(B): %v", err)
			}
			if len(ops) != 0 {
				t.Fatalf("ListOperators(B) = %v, want empty", ops)
			}
			sessNow := time.Date(2026, 6, 8, 0, 0, 0, 0, time.UTC)
			if _, err := s.LookupSession(ctx, tenantB, tokenHash("a-session"), sessNow); !errors.Is(err, ErrTokenInvalid) {
				t.Fatalf("LookupSession(B, A's session): err = %v, want ErrTokenInvalid", err)
			}
			if _, err := s.GetSettings(ctx, tenantB); !errors.Is(err, ErrNotFound) {
				t.Fatalf("GetSettings(B): err = %v, want ErrNotFound", err)
			}
			if _, err := s.GetSigningAnchor(ctx, tenantB); !errors.Is(err, ErrNotFound) {
				t.Fatalf("GetSigningAnchor(B): err = %v, want ErrNotFound", err)
			}

			// Sanity: tenant A still sees its own data (isolation is symmetric, not
			// a blanket wipe).
			if _, err := s.GetNode(ctx, tenantA, "alpha"); err != nil {
				t.Fatalf("GetNode(A, alpha) after isolation checks: %v", err)
			}
			if a, err := s.GetSigningAnchor(ctx, tenantA); err != nil || a.PubKeyPEM != "pub-a" {
				t.Fatalf("GetSigningAnchor(A): got %+v err %v, want pub-a", a, err)
			}
			if _, err := s.GetPendingKeystoneTransition(ctx, tenantA); err != nil {
				t.Fatalf("GetPendingKeystoneTransition(A): %v", err)
			}
			// ...and tenant A CAN resolve its own node API token to its node.
			if n, err := s.LookupNodeByAPIToken(ctx, tenantA, tokenHash("a-api-secret")); err != nil {
				t.Fatalf("LookupNodeByAPIToken(A, A's api token): %v", err)
			} else if n.NodeID != "alpha" {
				t.Fatalf("LookupNodeByAPIToken(A) NodeID = %q, want alpha", n.NodeID)
			}
			if gen, err := s.CurrentGeneration(ctx, tenantA); err != nil || gen != 1 {
				t.Fatalf("CurrentGeneration(A) = (%d, %v), want (1, nil)", gen, err)
			}
			// ...and tenant A CAN consume its own token (the gate isolates, it does
			// not break the owning tenant).
			if err := s.ConsumeEnrollmentToken(ctx, tenantA, tokenHash("a-secret"), "alpha", at); err != nil {
				t.Fatalf("ConsumeEnrollmentToken(A, A's token): %v", err)
			}
			// ...and tenant A still sees its own operator account + session.
			if _, err := s.GetOperator(ctx, tenantA, "admin"); err != nil {
				t.Fatalf("GetOperator(A, admin): %v", err)
			}
			if sess, err := s.LookupSession(ctx, tenantA, tokenHash("a-session"), at); err != nil {
				t.Fatalf("LookupSession(A, A's session): %v", err)
			} else if sess.Operator != "admin" {
				t.Fatalf("LookupSession(A) Operator = %q, want admin", sess.Operator)
			}

			// Tenant B's prune (keep=nothing) must not touch tenant A's staged
			// bundles (plan-3's PruneStagedBundles is tenant-scoped like the rest).
			if err := s.StageBundle(ctx, tenantA, SignedBundle{
				NodeID:     "alpha",
				Generation: 2,
				Files:      map[string][]byte{"install.sh": []byte("a2")},
				IsStaged:   true,
			}); err != nil {
				t.Fatalf("StageBundle(A, second): %v", err)
			}
			if purged, err := s.PruneStagedBundles(ctx, tenantB, nil); err != nil || len(purged) != 0 {
				t.Fatalf("PruneStagedBundles(B) = %v, %v; want none, nil", purged, err)
			}
			if gen, err := s.PromoteStaged(ctx, tenantA); err != nil || gen != 2 {
				t.Fatalf("PromoteStaged(A) after B's prune = (%d, %v), want (2, nil) — B's prune must not eat A's staged bundle", gen, err)
			}
		})
	}
}
