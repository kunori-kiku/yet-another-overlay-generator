package controller

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/kunorikiku/yet-another-overlay-generator/internal/model"
)

// TestNewEnrollmentToken covers the single-use enrollment token mint: the plaintext
// is non-empty, the record is scoped to the node, the stored hash is the SHA-256 of
// the plaintext (never the plaintext itself), and the TTL stamps ExpiresAt forward.
func TestNewEnrollmentToken(t *testing.T) {
	now := time.Now()
	plaintext, tok := NewEnrollmentToken("node-1", time.Hour, now)
	if plaintext == "" {
		t.Fatalf("NewEnrollmentToken returned empty plaintext")
	}
	if tok.NodeID != "node-1" {
		t.Fatalf("token NodeID = %q, want node-1", tok.NodeID)
	}
	if tok.TokenHash == "" || tok.TokenHash == plaintext {
		t.Fatalf("token hash must be set and != plaintext (got hash=%q plaintext=%q)", tok.TokenHash, plaintext)
	}
	if tok.TokenHash != HashToken(plaintext) {
		t.Fatalf("token hash %q != HashToken(plaintext) %q", tok.TokenHash, HashToken(plaintext))
	}
	if !tok.ExpiresAt.Equal(now.Add(time.Hour)) {
		t.Fatalf("token ExpiresAt = %v, want %v", tok.ExpiresAt, now.Add(time.Hour))
	}
	if tok.ConsumedAt != nil {
		t.Fatalf("fresh token ConsumedAt = %v, want nil", tok.ConsumedAt)
	}
}

// TestNewNodeAPIToken covers the per-node bearer-token mint: the plaintext is
// non-empty, the returned hash equals HashToken(plaintext) (so the controller only
// ever stores the hash), and two mints yield distinct plaintexts/hashes (entropy).
func TestNewNodeAPIToken(t *testing.T) {
	now := time.Now()
	plaintext, hash := NewNodeAPIToken(now)
	if plaintext == "" {
		t.Fatalf("NewNodeAPIToken returned empty plaintext")
	}
	if hash == "" || hash == plaintext {
		t.Fatalf("API token hash must be set and != plaintext (got hash=%q plaintext=%q)", hash, plaintext)
	}
	if hash != HashToken(plaintext) {
		t.Fatalf("returned hash %q != HashToken(plaintext) %q", hash, HashToken(plaintext))
	}

	plaintext2, hash2 := NewNodeAPIToken(now)
	if plaintext == plaintext2 {
		t.Fatalf("two API token mints returned identical plaintext (no entropy)")
	}
	if hash == hash2 {
		t.Fatalf("two API token mints returned identical hash (no entropy)")
	}
}

// TestEnrollHappyPath covers the full enrollment ceremony against MemStore: an
// operator-created token authorizes the node, Enroll burns it, mints a per-node
// bearer API token, and records the node as approved with its WG public key + API
// token hash, writing an "enroll" audit entry. The returned plaintext token resolves
// back to the node via LookupNodeByAPIToken.
func TestEnrollHappyPath(t *testing.T) {
	const tnt = TenantID("enroll-tenant")
	ctx := context.Background()
	store := NewMemStore()
	now := time.Now()

	plaintext, tok := NewEnrollmentToken("node-1", time.Hour, now)
	if err := store.CreateEnrollmentToken(ctx, tnt, tok); err != nil {
		t.Fatalf("CreateEnrollmentToken: %v", err)
	}

	res, err := Enroll(ctx, store, tnt, EnrollRequest{
		Token:       plaintext,
		NodeID:      "node-1",
		WGPublicKey: "AetxbtqeRdq7xOMpbaVK3St4vAoSMsCzTSLvtqs8BTw=",
	}, now)
	if err != nil {
		t.Fatalf("Enroll: %v", err)
	}
	if res.NodeID != "node-1" {
		t.Fatalf("EnrollResult.NodeID = %q, want node-1", res.NodeID)
	}
	if res.APIToken == "" {
		t.Fatalf("EnrollResult.APIToken is empty")
	}

	// The registry record is approved, carries the WG public key, and stores the
	// hash of the returned token (never the plaintext).
	node, err := store.GetNode(ctx, tnt, "node-1")
	if err != nil {
		t.Fatalf("GetNode after enroll: %v", err)
	}
	if node.Status != NodeApproved {
		t.Fatalf("node Status = %q, want %q", node.Status, NodeApproved)
	}
	if node.WGPublicKey != "AetxbtqeRdq7xOMpbaVK3St4vAoSMsCzTSLvtqs8BTw=" {
		t.Fatalf("node WGPublicKey = %q, want wg-pub-node-1", node.WGPublicKey)
	}
	if node.APITokenHash == "" {
		t.Fatalf("node APITokenHash is empty after enroll")
	}
	if node.APITokenHash != HashToken(res.APIToken) {
		t.Fatalf("node APITokenHash = %q, want HashToken(APIToken) %q", node.APITokenHash, HashToken(res.APIToken))
	}

	// The returned bearer token resolves back to the node via the reverse index.
	got, err := store.LookupNodeByAPIToken(ctx, tnt, HashToken(res.APIToken))
	if err != nil {
		t.Fatalf("LookupNodeByAPIToken after enroll: %v", err)
	}
	if got.NodeID != "node-1" {
		t.Fatalf("LookupNodeByAPIToken NodeID = %q, want node-1", got.NodeID)
	}

	// An "enroll" audit entry was appended for the node.
	entries, err := store.ListAudit(ctx, tnt)
	if err != nil {
		t.Fatalf("ListAudit: %v", err)
	}
	var sawEnroll bool
	for _, e := range entries {
		if e.Action == "enroll" && e.NodeID == "node-1" {
			sawEnroll = true
		}
	}
	if !sawEnroll {
		t.Fatalf("no enroll audit entry for node-1 found in %+v", entries)
	}
}

// requireNotApproved asserts that nodeID is NOT recorded as approved for the
// tenant: either it has no registry record (ErrNotFound — the expected outcome,
// since the ceremony's UpsertNode(approved) runs only after the token burn
// succeeds) or, defensively, a record whose Status is anything but NodeApproved.
func requireNotApproved(t *testing.T, store Store, tnt TenantID, nodeID string) {
	t.Helper()
	node, err := store.GetNode(context.Background(), tnt, nodeID)
	if errors.Is(err, ErrNotFound) {
		return
	}
	if err != nil {
		t.Fatalf("GetNode(%s): %v", nodeID, err)
	}
	if node.Status == NodeApproved {
		t.Fatalf("node %s is approved after a failed enroll, want not approved", nodeID)
	}
}

// TestEnrollFailures covers the refusal paths: an unknown token leaves the node
// unapproved (and no bearer token resolvable); and re-using an already-burned token
// returns ErrTokenConsumed. After each refusal the node must NOT be approved.
func TestEnrollFailures(t *testing.T) {
	const tnt = TenantID("enroll-fail-tenant")
	ctx := context.Background()
	now := time.Now()

	t.Run("unknown-token", func(t *testing.T) {
		store := NewMemStore()
		// No CreateEnrollmentToken: the store has never heard of this token, so the
		// atomic consume that opens the ceremony fails and the node is never created.
		_, err := Enroll(ctx, store, tnt, EnrollRequest{
			Token:       "bogus-never-minted-token",
			NodeID:      "node-1",
			WGPublicKey: "AetxbtqeRdq7xOMpbaVK3St4vAoSMsCzTSLvtqs8BTw=",
		}, now)
		if !errors.Is(err, ErrTokenInvalid) {
			t.Fatalf("Enroll(unknown token): err = %v, want ErrTokenInvalid", err)
		}
		requireNotApproved(t, store, tnt, "node-1")
		// No bearer token was issued for the (never-created) node.
		if _, err := store.LookupNodeByAPIToken(ctx, tnt, HashToken("anything")); !errors.Is(err, ErrTokenInvalid) {
			t.Fatalf("LookupNodeByAPIToken after failed enroll: err = %v, want ErrTokenInvalid", err)
		}
	})

	t.Run("cannot-enroll-with-a-manual-nodes-key", func(t *testing.T) {
		// Cross-source dedupe (mixed-mode plan-2): the one-pubkey-one-node invariant spans the
		// agent-enrolled registry AND operator-asserted MANUAL nodes in the stored topology. A node
		// must not enroll to a key a manual node already claims (the enrolled→manual direction).
		store := NewMemStore()
		const manualKey = "X3ql2OijvFoFNeNgMq/dEyphEiguYDbGqUI/VXc55Uw="
		topo := model.Topology{
			Project: model.Project{ID: "p", Name: "p"},
			Nodes: []model.Node{{
				ID: "node-manual", Name: "mike", Role: "router", DomainID: "d1",
				DeploymentMode: model.DeploymentManual, WireGuardPublicKey: manualKey,
			}},
		}
		raw, err := json.Marshal(topo)
		if err != nil {
			t.Fatalf("marshal topology: %v", err)
		}
		if _, err := store.PutTopology(ctx, tnt, raw); err != nil {
			t.Fatalf("PutTopology: %v", err)
		}
		plaintext, tok := NewEnrollmentToken("node-new", time.Hour, now)
		if err := store.CreateEnrollmentToken(ctx, tnt, tok); err != nil {
			t.Fatalf("CreateEnrollmentToken: %v", err)
		}
		_, err = Enroll(ctx, store, tnt, EnrollRequest{Token: plaintext, NodeID: "node-new", WGPublicKey: manualKey}, now)
		if !errors.Is(err, ErrDuplicateWGKey) {
			t.Fatalf("Enroll with a manual node's key: err = %v, want ErrDuplicateWGKey", err)
		}
		requireNotApproved(t, store, tnt, "node-new")
	})

	t.Run("malformed-wg-key-rejected-without-burning-token", func(t *testing.T) {
		// A malformed WireGuard public key is rejected up front (before the token is consumed), so a
		// valid token is never wasted on a typo and a bad key never reaches the registry / a rendered
		// peer config (plan-4).
		store := NewMemStore()
		plaintext, tok := NewEnrollmentToken("node-x", time.Hour, now)
		if err := store.CreateEnrollmentToken(ctx, tnt, tok); err != nil {
			t.Fatalf("CreateEnrollmentToken: %v", err)
		}
		bad := EnrollRequest{Token: plaintext, NodeID: "node-x", WGPublicKey: "not-a-valid-key"}
		if _, err := Enroll(ctx, store, tnt, bad, now); !errors.Is(err, ErrInvalidWGKey) {
			t.Fatalf("Enroll(malformed key): err = %v, want ErrInvalidWGKey", err)
		}
		requireNotApproved(t, store, tnt, "node-x")
		// The token was NOT burned: a corrected retry with the SAME token succeeds.
		good := EnrollRequest{Token: plaintext, NodeID: "node-x", WGPublicKey: "AetxbtqeRdq7xOMpbaVK3St4vAoSMsCzTSLvtqs8BTw="}
		if _, err := Enroll(ctx, store, tnt, good, now); err != nil {
			t.Fatalf("Enroll(corrected key, same token) must succeed (token not burned by the reject): %v", err)
		}
	})

	t.Run("burned-token-cannot-re-enroll", func(t *testing.T) {
		store := NewMemStore()
		plaintext, tok := NewEnrollmentToken("node-1", time.Hour, now)
		if err := store.CreateEnrollmentToken(ctx, tnt, tok); err != nil {
			t.Fatalf("CreateEnrollmentToken: %v", err)
		}
		req := EnrollRequest{Token: plaintext, NodeID: "node-1", WGPublicKey: "AetxbtqeRdq7xOMpbaVK3St4vAoSMsCzTSLvtqs8BTw="}
		if _, err := Enroll(ctx, store, tnt, req, now); err != nil {
			t.Fatalf("Enroll(first): %v", err)
		}
		// Second enroll with the same (now-burned) token -> ErrTokenConsumed.
		req2 := EnrollRequest{Token: plaintext, NodeID: "node-1", WGPublicKey: "AetxbtqeRdq7xOMpbaVK3St4vAoSMsCzTSLvtqs8BTw="}
		_, err := Enroll(ctx, store, tnt, req2, now)
		if !errors.Is(err, ErrTokenConsumed) {
			t.Fatalf("Enroll(burned token): err = %v, want ErrTokenConsumed", err)
		}
	})
}
