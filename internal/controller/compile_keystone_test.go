package controller

// compile_keystone_test.go covers the reworked keystone gate (plan-5.1 CORRECTION,
// 2026-06-08) across CompileAndStage + the controller-layer PromoteStaged:
//
//   - STAGE never requires a signature: with a pinned credential, CompileAndStage stages
//     the bundles AND stores the off-host-signable membership MANIFEST (unsigned). The
//     manifest's members carry bundle_sha256 = hex(sha256(checksums.sha256)), which
//     matches the staged bundle's actual checksums digest (the install.sh-coverage bind).
//   - The staged bundles DO NOT carry trustlist.json/.sig in their checksums set — the
//     manifest binds the checksums digest, so it cannot live inside it.
//   - PROMOTE requires a signature: controller.PromoteStaged refuses (keystone ON) until
//     a valid off-host signature over the staged manifest exists; after signing it
//     promotes. Keystone OFF promotes with no manifest at all.
//   - The monotonic epoch advances when a node's bundle digest changes (a changed
//     config/install.sh), and is reused when the staged manifest is byte-identical.

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/kunorikiku/yet-another-overlay-generator/internal/bundlesig"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/model"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/trustlist"
)

// noClientTopo is a router + peer topology (no client). It avoids the stageTestTopo
// client-edge port-pin quirk, where persistAllocations stamps a port pin onto a client
// edge that a later re-compile rejects — irrelevant to the keystone behaviour but fatal
// to any test that re-stages the SAME topology twice (the epoch-reuse test does). All
// keystone behaviour under test is independent of whether the fixture has a client.
func noClientTopo() *model.Topology {
	return &model.Topology{
		Project: model.Project{ID: "ctrl-keystone-001", Name: "Keystone Test"},
		Domains: []model.Domain{{
			ID: "domain-1", Name: "net", CIDR: "10.55.0.0/24",
			AllocationMode: "auto", RoutingMode: "babel",
		}},
		Nodes: []model.Node{
			{
				ID: "node-router", Name: "router", Hostname: "router.example.com",
				Role: "router", DomainID: "domain-1", ListenPort: 51820,
				Capabilities: model.NodeCapabilities{CanAcceptInbound: true, CanForward: true, HasPublicIP: true},
			},
			{
				ID: "node-peer", Name: "peer",
				Role: "peer", DomainID: "domain-1", ListenPort: 51820,
				Capabilities: model.NodeCapabilities{CanAcceptInbound: false, CanForward: false, HasPublicIP: false},
			},
		},
		Edges: []model.Edge{
			{ID: "e-peer", FromNodeID: "node-peer", ToNodeID: "node-router", Type: "public-endpoint", EndpointHost: "198.51.100.1", EndpointPort: 0, Transport: "udp", IsEnabled: true},
		},
	}
}

// putNoClientTopo stores noClientTopo via PutTopology and returns the test context.
func putNoClientTopo(t *testing.T, store Store) context.Context {
	t.Helper()
	ctx := context.Background()
	raw, err := json.Marshal(noClientTopo())
	if err != nil {
		t.Fatalf("marshal topology: %v", err)
	}
	if _, err := store.PutTopology(ctx, tenant, raw); err != nil {
		t.Fatalf("PutTopology: %v", err)
	}
	return ctx
}

// keystoneSetup pins a fresh Ed25519 operator credential (with a real PKIX PEM so the
// promote gate can verify against it) and approves the three stageTestTopo nodes. It
// returns the test context, the store, the signer, and the pinned public key.
func keystoneSetup(t *testing.T) (context.Context, Store, *trustlist.Ed25519Signer, ed25519.PublicKey) {
	t.Helper()
	store := NewMemStore()
	ctx := putStageTopo(t, store, tenant)

	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	if err := store.SetOperatorCredential(ctx, tenant, OperatorCredential{
		Alg:          string(trustlist.AlgEd25519),
		PublicKeyPEM: string(bundlesig.MarshalPublicKeyPEM(pub)),
	}); err != nil {
		t.Fatalf("SetOperatorCredential: %v", err)
	}

	approveNode(t, ctx, store, tenant, "node-router", genWGPubKey(t))
	approveNode(t, ctx, store, tenant, "node-peer", genWGPubKey(t))
	approveNode(t, ctx, store, tenant, "node-client", genWGPubKey(t))

	return ctx, store, trustlist.NewEd25519Signer(priv), pub
}

// signStagedManifest reads the staged (unsigned) manifest the controller stored, signs it
// off-host with the test signer, and writes the signature back onto the stored manifest
// record — mirroring the GET /trustlist -> sign -> POST /trustlist-signature flow without
// the HTTP layer. It byte-preserves the staged canonical bytes + epoch (only the
// signature changes), so a later PromoteStaged matches and verifies.
func signStagedManifest(t *testing.T, ctx context.Context, store Store, signer *trustlist.Ed25519Signer) {
	t.Helper()
	stored, err := store.GetCurrentSignedTrustList(ctx, tenant)
	if err != nil {
		t.Fatalf("GetCurrentSignedTrustList (staged manifest): %v", err)
	}
	var manifest trustlist.TrustList
	if err := json.Unmarshal(stored.TrustListJSON, &manifest); err != nil {
		t.Fatalf("unmarshal staged manifest: %v", err)
	}
	signed, err := signer.Sign(manifest)
	if err != nil {
		t.Fatalf("Sign staged manifest: %v", err)
	}
	sigJSON, err := json.Marshal(signed)
	if err != nil {
		t.Fatalf("marshal signature: %v", err)
	}
	if err := store.PutSignedTrustList(ctx, tenant, StoredTrustList{
		TrustListJSON: stored.TrustListJSON,
		SignatureJSON: sigJSON,
		Epoch:         stored.Epoch,
	}); err != nil {
		t.Fatalf("PutSignedTrustList (signed): %v", err)
	}
}

// TestCompileAndStage_KeystoneManifestBindsBundleDigest confirms the keystone success
// path: with a pinned credential, CompileAndStage stages the bundles WITHOUT trust-list
// files in their checksums set, and stores an unsigned membership manifest whose members
// carry bundle_sha256 = hex(sha256(that node's staged checksums.sha256)).
func TestCompileAndStage_KeystoneManifestBindsBundleDigest(t *testing.T) {
	ctx, store, _, _ := keystoneSetup(t)

	res, err := CompileAndStage(ctx, store, tenant, time.Now())
	if err != nil {
		t.Fatalf("CompileAndStage (keystone on): %v", err)
	}
	if len(res.Staged) == 0 {
		t.Fatalf("nothing staged")
	}

	// The staged manifest exists, is UNSIGNED, and binds each staged node's bundle digest.
	stored, err := store.GetCurrentSignedTrustList(ctx, tenant)
	if err != nil {
		t.Fatalf("GetCurrentSignedTrustList: %v", err)
	}
	if len(stored.SignatureJSON) != 0 {
		t.Fatalf("staged manifest must be UNSIGNED after stage; got a signature")
	}
	var manifest trustlist.TrustList
	if err := json.Unmarshal(stored.TrustListJSON, &manifest); err != nil {
		t.Fatalf("unmarshal staged manifest: %v", err)
	}
	if manifest.SchemaVersion != 1 || manifest.Tenant != string(tenant) {
		t.Fatalf("manifest header wrong: %+v", manifest)
	}
	digestByID := make(map[string]string, len(manifest.Members))
	for _, m := range manifest.Members {
		if m.BundleSHA256 == "" {
			t.Fatalf("member %s has empty bundle_sha256", m.NodeID)
		}
		digestByID[m.NodeID] = m.BundleSHA256
	}
	if len(digestByID) != len(res.Staged) {
		t.Fatalf("manifest members %d != staged nodes %d", len(digestByID), len(res.Staged))
	}

	// Promote once (raw store method — bypasses the gate, fine for a read-back) so the
	// staged bundles become readable via GetCurrentBundle.
	if _, err := store.PromoteStaged(ctx, tenant); err != nil {
		t.Fatalf("PromoteStaged (to read staged bundles): %v", err)
	}

	// For each staged node, the staged bundle's checksums.sha256 digest equals its member
	// bundle_sha256 (the install.sh-coverage bind), and the bundle does NOT carry
	// trustlist files inside its checksums set.
	for _, nodeID := range res.Staged {
		bundle, err := store.GetCurrentBundle(ctx, tenant, nodeID)
		if err != nil {
			t.Fatalf("GetCurrentBundle(%s): %v", nodeID, err)
		}
		checks, ok := bundle.Files["checksums.sha256"]
		if !ok {
			t.Fatalf("%s staged bundle missing checksums.sha256", nodeID)
		}
		sum := sha256.Sum256(checks)
		got := hex.EncodeToString(sum[:])
		if got != digestByID[nodeID] {
			t.Fatalf("%s digest mismatch: bundle %s, manifest %s", nodeID, got, digestByID[nodeID])
		}
		// The manifest binds the checksums digest, so the trust-list must NOT be inside it.
		if strings.Contains(string(checks), "trustlist.json") || strings.Contains(string(checks), "trustlist.sig") {
			t.Fatalf("%s checksums.sha256 must NOT cover trustlist files:\n%s", nodeID, checks)
		}
		if _, ok := bundle.Files["trustlist.json"]; ok {
			t.Fatalf("%s staged bundle must NOT embed trustlist.json (served at /config time)", nodeID)
		}
	}
}

// TestPromoteStaged_KeystoneRequiresSignature confirms the deploy chokepoint: with a
// pinned credential, PromoteStaged refuses until the staged manifest is signed off-host,
// then succeeds after signing.
func TestPromoteStaged_KeystoneRequiresSignature(t *testing.T) {
	ctx, store, signer, _ := keystoneSetup(t)

	if _, err := CompileAndStage(ctx, store, tenant, time.Now()); err != nil {
		t.Fatalf("CompileAndStage: %v", err)
	}

	// Promote with the manifest UNSIGNED -> refused.
	if _, err := PromoteStaged(ctx, store, tenant); err == nil {
		t.Fatalf("PromoteStaged with unsigned manifest: err = nil, want refusal")
	} else if !strings.Contains(err.Error(), "sign") {
		t.Fatalf("refusal %q does not mention signing", err.Error())
	}

	// Sign the staged manifest off-host, then promote succeeds.
	signStagedManifest(t, ctx, store, signer)
	gen, err := PromoteStaged(ctx, store, tenant)
	if err != nil {
		t.Fatalf("PromoteStaged after signing: %v", err)
	}
	if gen < 1 {
		t.Fatalf("PromoteStaged generation %d, want >= 1", gen)
	}
}

// TestPromoteStaged_KeystoneRejectsBadSignature confirms PromoteStaged refuses a manifest
// whose stored signature does not verify against the pinned credential (a forged or stale
// signature). It is the property that a breached controller cannot forge.
func TestPromoteStaged_KeystoneRejectsBadSignature(t *testing.T) {
	ctx, store, _, _ := keystoneSetup(t)
	if _, err := CompileAndStage(ctx, store, tenant, time.Now()); err != nil {
		t.Fatalf("CompileAndStage: %v", err)
	}

	// Forge a signature with a DIFFERENT key (the attacker lacks the pinned key).
	_, roguePriv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	rogue := trustlist.NewEd25519Signer(roguePriv)
	stored, err := store.GetCurrentSignedTrustList(ctx, tenant)
	if err != nil {
		t.Fatalf("GetCurrentSignedTrustList: %v", err)
	}
	var manifest trustlist.TrustList
	if err := json.Unmarshal(stored.TrustListJSON, &manifest); err != nil {
		t.Fatalf("unmarshal staged manifest: %v", err)
	}
	signed, err := rogue.Sign(manifest)
	if err != nil {
		t.Fatalf("rogue Sign: %v", err)
	}
	sigJSON, _ := json.Marshal(signed)
	if err := store.PutSignedTrustList(ctx, tenant, StoredTrustList{
		TrustListJSON: stored.TrustListJSON,
		SignatureJSON: sigJSON,
		Epoch:         stored.Epoch,
	}); err != nil {
		t.Fatalf("PutSignedTrustList: %v", err)
	}

	if _, err := PromoteStaged(ctx, store, tenant); err == nil {
		t.Fatalf("PromoteStaged with rogue-signed manifest: err = nil, want refusal")
	}
}

// TestCompileAndStage_KeystoneEpochAdvancesWithBundle confirms the monotonic epoch:
// re-staging an unchanged topology reuses the epoch (byte-identical manifest), while a
// changed config (which changes a node's bundle digest) advances it. The epoch is what
// the agent's anti-rollback floor uses to admit a fresh deploy and reject a stale one.
func TestCompileAndStage_KeystoneEpochAdvancesWithBundle(t *testing.T) {
	// Use the no-client fixture so re-staging the SAME topology compiles cleanly (the
	// stageTestTopo client edge would trip a re-compile port-pin quirk unrelated to the
	// keystone). Pin a real Ed25519 credential and approve the two nodes.
	store := NewMemStore()
	ctx := putNoClientTopo(t, store)
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	if err := store.SetOperatorCredential(ctx, tenant, OperatorCredential{
		Alg:          string(trustlist.AlgEd25519),
		PublicKeyPEM: string(bundlesig.MarshalPublicKeyPEM(pub)),
	}); err != nil {
		t.Fatalf("SetOperatorCredential: %v", err)
	}
	approveNode(t, ctx, store, tenant, "node-router", genWGPubKey(t))
	approveNode(t, ctx, store, tenant, "node-peer", genWGPubKey(t))
	signer := trustlist.NewEd25519Signer(priv)

	// Stage 1, sign, promote at epoch 0.
	if _, err := CompileAndStage(ctx, store, tenant, time.Now()); err != nil {
		t.Fatalf("CompileAndStage #1: %v", err)
	}
	if e := storedEpoch(t, ctx, store); e != 0 {
		t.Fatalf("epoch after first stage = %d, want 0", e)
	}
	signStagedManifest(t, ctx, store, signer)
	if _, err := PromoteStaged(ctx, store, tenant); err != nil {
		t.Fatalf("PromoteStaged #1: %v", err)
	}

	// Re-stage the SAME topology+membership: byte-identical manifest -> epoch REUSED (0).
	if _, err := CompileAndStage(ctx, store, tenant, time.Now()); err != nil {
		t.Fatalf("CompileAndStage #2 (unchanged): %v", err)
	}
	if e := storedEpoch(t, ctx, store); e != 0 {
		t.Fatalf("epoch after unchanged re-stage = %d, want 0 (reuse)", e)
	}

	// Membership change: re-approve node-peer with a NEW WireGuard public key (a rekey).
	// This changes node-peer's member entry AND node-router's rendered [Peer] block, so
	// both their manifest tuples (wg key and/or bundle digest) change -> the staged
	// manifest differs -> the epoch must advance to 1. The topology is unchanged, so the
	// subgraph still compiles cleanly.
	approveNode(t, ctx, store, tenant, "node-peer", genWGPubKey(t))
	if _, err := CompileAndStage(ctx, store, tenant, time.Now()); err != nil {
		t.Fatalf("CompileAndStage #3 (rekey): %v", err)
	}
	if e := storedEpoch(t, ctx, store); e != 1 {
		t.Fatalf("epoch after membership change = %d, want 1 (advance)", e)
	}
}

// TestPromoteStaged_KeystoneOff confirms the opt-in: with NO operator credential pinned,
// CompileAndStage stores NO manifest and PromoteStaged promotes with no extra gate.
func TestPromoteStaged_KeystoneOff(t *testing.T) {
	store := NewMemStore()
	ctx := putStageTopo(t, store, tenant)
	approveNode(t, ctx, store, tenant, "node-router", genWGPubKey(t))
	approveNode(t, ctx, store, tenant, "node-peer", genWGPubKey(t))
	approveNode(t, ctx, store, tenant, "node-client", genWGPubKey(t))

	res, err := CompileAndStage(ctx, store, tenant, time.Now())
	if err != nil {
		t.Fatalf("CompileAndStage (keystone off): %v", err)
	}
	// No manifest is stored with keystone OFF.
	if _, err := store.GetCurrentSignedTrustList(ctx, tenant); err == nil {
		t.Fatalf("keystone OFF but a manifest was stored")
	}
	// Promote with no gate.
	if _, err := PromoteStaged(ctx, store, tenant); err != nil {
		t.Fatalf("PromoteStaged (keystone off): %v", err)
	}
	// The promoted bundles carry no trust-list files (served-only, keystone OFF -> none).
	for _, nodeID := range res.Staged {
		bundle, err := store.GetCurrentBundle(ctx, tenant, nodeID)
		if err != nil {
			t.Fatalf("GetCurrentBundle(%s): %v", nodeID, err)
		}
		if _, ok := bundle.Files["trustlist.json"]; ok {
			t.Fatalf("%s bundle carries trustlist.json with keystone OFF", nodeID)
		}
	}
}

// storedEpoch returns the tenant's currently stored manifest epoch (0 helpers fail the
// test if none is stored).
func storedEpoch(t *testing.T, ctx context.Context, store Store) int64 {
	t.Helper()
	stored, err := store.GetCurrentSignedTrustList(ctx, tenant)
	if err != nil {
		t.Fatalf("GetCurrentSignedTrustList (epoch): %v", err)
	}
	return stored.Epoch
}
