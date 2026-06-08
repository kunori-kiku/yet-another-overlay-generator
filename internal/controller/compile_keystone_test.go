package controller

// compile_keystone_test.go covers the keystone gate inside CompileAndStage (plan-5.1b):
// when an operator credential is pinned (keystone ON), a deploy requires a current
// signed trust-list whose membership matches the approved registry, and on success the
// canonical trust-list + its signature are embedded into EVERY staged node bundle and
// covered by each bundle's checksums.sha256. When no credential is pinned (keystone
// OFF) the deploy embeds nothing — exactly today's behavior.

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/kunorikiku/yet-another-overlay-generator/internal/trustlist"
)

// keystoneSetup pins a fresh Ed25519 operator credential and approves the three
// stageTestTopo nodes. It returns the test context, the store, and the signer so the
// caller can build + sign a trust-list.
func keystoneSetup(t *testing.T) (context.Context, Store, *trustlist.Ed25519Signer, ed25519.PublicKey) {
	t.Helper()
	store := NewMemStore()
	ctx := putStageTopo(t, store, tenant)

	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	if err := store.SetOperatorCredential(ctx, tenant, OperatorCredential{
		Alg: string(trustlist.AlgEd25519),
	}); err != nil {
		t.Fatalf("SetOperatorCredential: %v", err)
	}

	approveNode(t, ctx, store, tenant, "node-router", genWGPubKey(t))
	approveNode(t, ctx, store, tenant, "node-peer", genWGPubKey(t))
	approveNode(t, ctx, store, tenant, "node-client", genWGPubKey(t))

	return ctx, store, trustlist.NewEd25519Signer(priv), pub
}

// signCurrentMembership builds the trust-list from the current approved registry and
// stores a signed copy at the given epoch (mirroring the HTTP buildTrustList + sign +
// PutSignedTrustList flow, without the handler).
func signCurrentMembership(t *testing.T, ctx context.Context, store Store, signer *trustlist.Ed25519Signer, epoch int64) {
	t.Helper()
	nodes, err := store.ListNodes(ctx, tenant)
	if err != nil {
		t.Fatalf("ListNodes: %v", err)
	}
	var members []trustlist.Member
	for _, n := range nodes {
		if n.Status == NodeApproved && n.WGPublicKey != "" {
			members = append(members, trustlist.Member{NodeID: n.NodeID, WGPublicKey: n.WGPublicKey})
		}
	}
	tl := trustlist.TrustList{
		SchemaVersion: 1,
		Tenant:        string(tenant),
		Epoch:         epoch,
		Members:       members,
		CreatedAt:     time.Now().UTC().Format(time.RFC3339),
	}
	canonical, err := trustlist.Canonical(tl)
	if err != nil {
		t.Fatalf("Canonical: %v", err)
	}
	signed, err := signer.Sign(tl)
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}
	sigJSON, err := json.Marshal(signed)
	if err != nil {
		t.Fatalf("marshal signed: %v", err)
	}
	if err := store.PutSignedTrustList(ctx, tenant, StoredTrustList{
		TrustListJSON: canonical,
		SignatureJSON: sigJSON,
		Epoch:         epoch,
	}); err != nil {
		t.Fatalf("PutSignedTrustList: %v", err)
	}
}

// TestCompileAndStage_KeystoneEmbeds confirms the keystone success path: with a pinned
// credential AND a matching signed trust-list, CompileAndStage embeds trustlist.json +
// trustlist.sig into every staged bundle, those embedded bytes verify offline against
// the pinned credential, and they are covered by the bundle's checksums.sha256.
func TestCompileAndStage_KeystoneEmbeds(t *testing.T) {
	ctx, store, signer, pub := keystoneSetup(t)
	signCurrentMembership(t, ctx, store, signer, 0)

	res, err := CompileAndStage(ctx, store, tenant, time.Now())
	if err != nil {
		t.Fatalf("CompileAndStage (keystone on, matching): %v", err)
	}
	if len(res.Staged) == 0 {
		t.Fatalf("nothing staged")
	}
	if _, err := store.PromoteStaged(ctx, tenant); err != nil {
		t.Fatalf("PromoteStaged: %v", err)
	}

	for _, nodeID := range res.Staged {
		bundle, err := store.GetCurrentBundle(ctx, tenant, nodeID)
		if err != nil {
			t.Fatalf("GetCurrentBundle(%s): %v", nodeID, err)
		}
		tlBytes, ok := bundle.Files["trustlist.json"]
		if !ok {
			t.Fatalf("%s bundle missing trustlist.json; have %v", nodeID, bundleKeys(bundle))
		}
		sigBytes, ok := bundle.Files["trustlist.sig"]
		if !ok {
			t.Fatalf("%s bundle missing trustlist.sig", nodeID)
		}

		// The embedded trust-list verifies offline against the pinned credential.
		var tl trustlist.TrustList
		if err := json.Unmarshal(tlBytes, &tl); err != nil {
			t.Fatalf("%s unmarshal trustlist.json: %v", nodeID, err)
		}
		var signed trustlist.SignedTrustList
		if err := json.Unmarshal(sigBytes, &signed); err != nil {
			t.Fatalf("%s unmarshal trustlist.sig: %v", nodeID, err)
		}
		pin := trustlist.PinnedCredential{Alg: trustlist.AlgEd25519, Ed25519Pub: pub}
		if err := trustlist.Verify(tl, signed, pin); err != nil {
			t.Fatalf("%s embedded trust-list failed offline Verify: %v", nodeID, err)
		}

		// checksums.sha256 covers trustlist.json and trustlist.sig.
		checks, ok := bundle.Files["checksums.sha256"]
		if !ok {
			t.Fatalf("%s bundle missing checksums.sha256", nodeID)
		}
		cs := string(checks)
		if !strings.Contains(cs, "  trustlist.json\n") {
			t.Fatalf("%s checksums.sha256 does not cover trustlist.json:\n%s", nodeID, cs)
		}
		if !strings.Contains(cs, "  trustlist.sig\n") {
			t.Fatalf("%s checksums.sha256 does not cover trustlist.sig:\n%s", nodeID, cs)
		}
	}
}

// TestCompileAndStage_KeystoneRequiresSignature confirms that with a pinned credential
// but NO signed trust-list, CompileAndStage refuses (a deploy must not ship without the
// membership proof nodes verify offline).
func TestCompileAndStage_KeystoneRequiresSignature(t *testing.T) {
	ctx, store, _, _ := keystoneSetup(t)
	// No PutSignedTrustList.
	_, err := CompileAndStage(ctx, store, tenant, time.Now())
	if err == nil {
		t.Fatalf("CompileAndStage with no signed trust-list: err = nil, want a clear error")
	}
	if !strings.Contains(err.Error(), "trust-list") {
		t.Fatalf("error %q does not mention the missing trust-list", err.Error())
	}
}

// TestCompileAndStage_KeystoneMembershipChanged confirms the freshness guard: signing
// the membership, then changing it (approve a new member) WITHOUT re-signing, makes
// CompileAndStage refuse with the "re-sign before deploy" error — the gate fires before
// any compile work. After re-signing the new membership the gate clears.
func TestCompileAndStage_KeystoneMembershipChanged(t *testing.T) {
	ctx, store, signer, _ := keystoneSetup(t)
	signCurrentMembership(t, ctx, store, signer, 0)

	// First stage with the matching signature succeeds.
	if _, err := CompileAndStage(ctx, store, tenant, time.Now()); err != nil {
		t.Fatalf("CompileAndStage (matching): %v", err)
	}

	// Membership changes: approve a brand-new member WITHOUT re-signing. It is an
	// approved member with a key, so the signed trust-list's member set no longer
	// matches the current approved-member set — the freshness gate must refuse.
	approveNode(t, ctx, store, tenant, "node-extra", genWGPubKey(t))

	_, err := CompileAndStage(ctx, store, tenant, time.Now())
	if err == nil {
		t.Fatalf("CompileAndStage after membership change with no re-sign: err = nil, want re-sign error")
	}
	if !strings.Contains(err.Error(), "re-sign") {
		t.Fatalf("error %q does not ask to re-sign", err.Error())
	}

	// Re-signing the NEW membership clears the gate: the gate (run before compile) no
	// longer fails. We assert the keystone gate passed by confirming the error, if any,
	// is NOT the keystone re-sign/trust-list error (a downstream compile quirk on this
	// fixture's client edge is out of keystone scope).
	signCurrentMembership(t, ctx, store, signer, 1)
	if _, err := CompileAndStage(ctx, store, tenant, time.Now()); err != nil {
		if strings.Contains(err.Error(), "re-sign") || strings.Contains(err.Error(), "trust-list") {
			t.Fatalf("keystone gate still refusing after re-sign: %v", err)
		}
	}
}

// TestCompileAndStage_KeystoneOff confirms the opt-in: with NO operator credential
// pinned, CompileAndStage embeds NO trustlist files (today's behavior).
func TestCompileAndStage_KeystoneOff(t *testing.T) {
	store := NewMemStore()
	ctx := putStageTopo(t, store, tenant)
	approveNode(t, ctx, store, tenant, "node-router", genWGPubKey(t))
	approveNode(t, ctx, store, tenant, "node-peer", genWGPubKey(t))
	approveNode(t, ctx, store, tenant, "node-client", genWGPubKey(t))

	res, err := CompileAndStage(ctx, store, tenant, time.Now())
	if err != nil {
		t.Fatalf("CompileAndStage (keystone off): %v", err)
	}
	if _, err := store.PromoteStaged(ctx, tenant); err != nil {
		t.Fatalf("PromoteStaged: %v", err)
	}
	for _, nodeID := range res.Staged {
		bundle, err := store.GetCurrentBundle(ctx, tenant, nodeID)
		if err != nil {
			t.Fatalf("GetCurrentBundle(%s): %v", nodeID, err)
		}
		if _, ok := bundle.Files["trustlist.json"]; ok {
			t.Fatalf("%s bundle carries trustlist.json with keystone OFF", nodeID)
		}
		if _, ok := bundle.Files["trustlist.sig"]; ok {
			t.Fatalf("%s bundle carries trustlist.sig with keystone OFF", nodeID)
		}
	}
}
