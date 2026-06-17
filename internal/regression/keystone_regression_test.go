// Package regression is a NON-RELEASE, black-box adversarial regression suite for the
// controller↔agent KEYSTONE / membership trust path. It contains only _test.go files, so it is
// never compiled into any release binary (yaog-server / yaog-agent); it runs under `go test`.
//
// It drives the REAL exported surfaces end to end — controller.CompileAndStage / PromoteStaged
// over a MemStore, a software Ed25519 off-host signer standing in for the passkey, and the REAL
// agent.VerifyBundle / agent.VerifyMembership a node runs offline — to probe scenarios ADJACENT to
// the keystone-rotation fix that the per-package unit tests do not cover end to end: keystone
// rotation across a MIXED fleet, anti-rollback epoch behavior across a rotation, algorithm-
// confusion, the bundle-signing-anchor × keystone composition, and revoke-driven membership
// changes. A failure here is a candidate bug to FIX, not to suppress.
package regression

import (
	"context"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"golang.zx2c4.com/wireguard/wgctrl/wgtypes"

	"github.com/kunorikiku/yet-another-overlay-generator/internal/agent"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/bundlesig"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/controller"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/model"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/trustlist"
)

const tenant = controller.TenantID("acme")

// keystone is a software off-host signer (a passkey stand-in) + its pinned PKIX public-key PEM.
type keystone struct {
	signer *trustlist.Ed25519Signer
	pubPEM []byte
}

func newKeystone(t *testing.T) keystone {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("genkey: %v", err)
	}
	return keystone{signer: trustlist.NewEd25519Signer(priv), pubPEM: bundlesig.MarshalPublicKeyPEM(pub)}
}

// regEnv is a black-box controller env: a MemStore with a topology + enrolled nodes, driven via the
// exported CompileAndStage / sign / PromoteStaged flow.
type regEnv struct {
	t     *testing.T
	store controller.Store
	ctx   context.Context
}

// newRegEnv stores `topo` and approves each nodeID with a fresh real WG public key (the enrolled
// registry CompileAndStage compiles). Pin the keystone separately via pinKeystone.
func newRegEnv(t *testing.T, topo *model.Topology, nodeIDs ...string) *regEnv {
	t.Helper()
	ctx := context.Background()
	store := controller.NewMemStore()
	raw, err := json.Marshal(topo)
	if err != nil {
		t.Fatalf("marshal topo: %v", err)
	}
	if _, err := store.PutTopology(ctx, tenant, raw); err != nil {
		t.Fatalf("PutTopology: %v", err)
	}
	e := &regEnv{t: t, store: store, ctx: ctx}
	for _, id := range nodeIDs {
		e.approve(id)
	}
	return e
}

// approve enrolls nodeID with a fresh WG public key (NodeApproved) so it enters the compiled subgraph.
func (e *regEnv) approve(nodeID string) {
	e.t.Helper()
	priv, err := wgtypes.GeneratePrivateKey()
	if err != nil {
		e.t.Fatalf("wg genkey: %v", err)
	}
	if err := e.store.UpsertNode(e.ctx, tenant, controller.Node{
		NodeID: nodeID, Status: controller.NodeApproved, WGPublicKey: priv.PublicKey().String(),
	}); err != nil {
		e.t.Fatalf("UpsertNode(%s): %v", nodeID, err)
	}
}

// revoke flips a node to NodeRevoked (mirrors HandleRevoke), so it drops out of the next compiled
// subgraph + signed manifest (enrolledSubgraph admits only NodeApproved nodes).
func (e *regEnv) revoke(nodeID string) {
	e.t.Helper()
	if err := e.store.UpsertNode(e.ctx, tenant, controller.Node{NodeID: nodeID, Status: controller.NodeRevoked}); err != nil {
		e.t.Fatalf("revoke UpsertNode(%s): %v", nodeID, err)
	}
}

func (e *regEnv) pinKeystone(ks keystone) {
	e.t.Helper()
	if err := e.store.SetOperatorCredential(e.ctx, tenant, controller.OperatorCredential{
		Alg: string(trustlist.AlgEd25519), PublicKeyPEM: string(ks.pubPEM),
	}); err != nil {
		e.t.Fatalf("SetOperatorCredential: %v", err)
	}
}

// deploy runs the full keystone deploy: CompileAndStage → off-host sign the staged manifest with
// `ks` → PromoteStaged (the gate verifies the signature against the currently-pinned credential).
// It returns the promoted generation.
func (e *regEnv) deploy(ks keystone) int64 {
	e.t.Helper()
	if _, err := controller.CompileAndStage(e.ctx, e.store, tenant, time.Now()); err != nil {
		e.t.Fatalf("CompileAndStage: %v", err)
	}
	e.signStaged(ks)
	gen, err := controller.PromoteStaged(e.ctx, e.store, tenant)
	if err != nil {
		e.t.Fatalf("PromoteStaged: %v", err)
	}
	return gen
}

// signStaged signs the stored staged manifest off-host and writes the signature back (mirrors
// GET /trustlist → sign → POST /trustlist-signature), byte-preserving the canonical bytes + epoch.
func (e *regEnv) signStaged(ks keystone) {
	e.t.Helper()
	stored, err := e.store.GetCurrentSignedTrustList(e.ctx, tenant)
	if err != nil {
		e.t.Fatalf("GetCurrentSignedTrustList: %v", err)
	}
	var tl trustlist.TrustList
	if err := json.Unmarshal(stored.TrustListJSON, &tl); err != nil {
		e.t.Fatalf("unmarshal staged manifest: %v", err)
	}
	signed, err := ks.signer.Sign(tl)
	if err != nil {
		e.t.Fatalf("sign: %v", err)
	}
	sigJSON, err := json.Marshal(signed)
	if err != nil {
		e.t.Fatalf("marshal signed: %v", err)
	}
	if err := e.store.PutSignedTrustList(e.ctx, tenant, controller.StoredTrustList{
		TrustListJSON: stored.TrustListJSON, SignatureJSON: sigJSON, Epoch: stored.Epoch,
	}); err != nil {
		e.t.Fatalf("PutSignedTrustList: %v", err)
	}
}

// served assembles the file map the controller would serve a node at /config: the promoted bundle
// files PLUS trustlist.json (canonical) + trustlist.sig (the signed artifact), exactly as the HTTP
// /config handler appends them (never embedded in the bundle's checksum set). It reads the atomic
// GetServedConfig — the SERVED (last-promoted) slots — so it mirrors what a node actually fetches,
// not the in-flight STAGED manifest (which a mid-deploy re-stage may have left unsigned).
func (e *regEnv) served(nodeID string) map[string][]byte {
	e.t.Helper()
	sc, err := e.store.GetServedConfig(e.ctx, tenant, nodeID)
	if err != nil {
		e.t.Fatalf("GetServedConfig(%s): %v", nodeID, err)
	}
	files := make(map[string][]byte, len(sc.Bundle.Files)+2)
	for k, v := range sc.Bundle.Files {
		files[k] = v
	}
	if sc.HasTrustList {
		files["trustlist.json"] = sc.TrustList.TrustListJSON
		files["trustlist.sig"] = sc.TrustList.SignatureJSON
	}
	return files
}

// verifyAsNode runs the REAL agent membership gate against the served bundle for nodeID, pinned to
// `pinPEM` (alg ed25519) with the given anti-rollback floor. Returns (epoch, err).
func verifyAsNode(files map[string][]byte, nodeID string, pinPEM []byte, prevEpoch int64) (int64, error) {
	return agent.VerifyMembership(files, agent.MembershipConfig{
		NodeID:          nodeID,
		OperatorCredPEM: pinPEM,
		OperatorCredAlg: string(trustlist.AlgEd25519),
	}, prevEpoch)
}

// twoNodeTopo is a router + peer (no client — avoids the client-edge port-pin re-stage quirk).
func twoNodeTopo() *model.Topology {
	return &model.Topology{
		Project: model.Project{ID: "reg-001", Name: "Regression"},
		Domains: []model.Domain{{ID: "domain-1", Name: "net", CIDR: "10.80.0.0/24", AllocationMode: "auto", RoutingMode: "babel"}},
		Nodes: []model.Node{
			{ID: "node-1", Name: "router", Hostname: "r.example.com", Role: "router", DomainID: "domain-1", Capabilities: model.NodeCapabilities{CanAcceptInbound: true, CanForward: true, HasPublicIP: true}},
			{ID: "node-2", Name: "peer", Role: "peer", DomainID: "domain-1", Capabilities: model.NodeCapabilities{CanAcceptInbound: false, CanForward: false, HasPublicIP: false}},
		},
		Edges: []model.Edge{
			{ID: "e-1", FromNodeID: "node-2", ToNodeID: "node-1", Type: "public-endpoint", EndpointHost: "198.51.100.1", EndpointPort: 0, Transport: "udp", IsEnabled: true},
		},
	}
}

// --- Scenario 1: baseline sanity (pin A, deploy A, node-A verifies) ---

func TestRegression_Baseline(t *testing.T) {
	e := newRegEnv(t, twoNodeTopo(), "node-1", "node-2")
	a := newKeystone(t)
	e.pinKeystone(a)
	e.deploy(a)
	if _, err := verifyAsNode(e.served("node-1"), "node-1", a.pubPEM, 0); err != nil {
		t.Fatalf("baseline: node-1 must verify the A-signed bundle, got %v", err)
	}
}

// --- Scenario 2: keystone rotation across a MIXED fleet ---
// After rotate→B + redeploy(B): a node still pinned to A REFUSES (fail-closed, must re-provision),
// while a node re-provisioned to B ACCEPTS the SAME served bundle.
func TestRegression_MixedFleetRotation(t *testing.T) {
	e := newRegEnv(t, twoNodeTopo(), "node-1", "node-2")
	a, b := newKeystone(t), newKeystone(t)
	e.pinKeystone(a)
	e.deploy(a)

	e.pinKeystone(b) // rotate the keystone in the controller
	e.deploy(b)      // redeploy, now signed by B

	files := e.served("node-1")
	if _, err := verifyAsNode(files, "node-1", a.pubPEM, 0); err == nil {
		t.Fatal("a node still pinned to A must REFUSE the B-signed bundle (fail-closed)")
	}
	if _, err := verifyAsNode(files, "node-1", b.pubPEM, 0); err != nil {
		t.Fatalf("a node re-provisioned to B must ACCEPT the same served bundle, got %v", err)
	}
}

// --- Scenario 3: anti-rollback epoch across a rotation with UNCHANGED membership ---
// A keystone rotation with an identical topology REUSES the epoch (membership tuple unchanged), so a
// node that had applied that epoch under A (prevEpoch = E) must still ACCEPT the B-signed manifest
// at the SAME epoch E once re-provisioned to B (equal epoch is allowed — not a rollback). This pins
// the design's residual concern that equal-epoch re-applies stay accepted across a rotation.
//
// It deliberately works at a POSITIVE epoch (not the degenerate epoch 0 of a first deploy): we first
// change membership (revoke node-2, redeploy) to advance to epoch 1, THEN rotate the keystone with
// UNCHANGED membership so epoch 1 is REUSED across the rotation. Equal epoch 0==0 would still catch a
// `<`→`<=` regression, but only a positive equal epoch exercises the documented "a non-trivial epoch
// survives a rotation" invariant.
func TestRegression_EqualEpochAcrossRotation(t *testing.T) {
	e := newRegEnv(t, twoNodeTopo(), "node-1", "node-2")
	a, b := newKeystone(t), newKeystone(t)
	e.pinKeystone(a)
	e.deploy(a)

	// Advance to a POSITIVE epoch via a real membership change (revoke node-2).
	e.revoke("node-2")
	e.deploy(a)
	epochA := mustEpoch(t, e)
	if epochA == 0 {
		t.Fatalf("membership change must advance to a positive epoch, got %d", epochA)
	}
	// node-1 applied epoch epochA under A.
	if _, err := verifyAsNode(e.served("node-1"), "node-1", a.pubPEM, epochA); err != nil {
		t.Fatalf("node-1 under A at its own epoch must verify, got %v", err)
	}

	e.pinKeystone(b)
	e.deploy(b) // UNCHANGED membership (node-2 still revoked) → epoch REUSED at epochA
	epochB := mustEpoch(t, e)
	if epochB != epochA {
		t.Fatalf("unchanged-membership rotation must reuse the (positive) epoch: A=%d B=%d", epochA, epochB)
	}
	// node-1 (prevEpoch = epochA) re-provisioned to B must accept the B-signed manifest at epochB==epochA.
	if _, err := verifyAsNode(e.served("node-1"), "node-1", b.pubPEM, epochA); err != nil {
		t.Fatalf("equal-epoch B-signed manifest must be accepted by a re-provisioned node, got %v", err)
	}
}

// --- Scenario 4: anti-rollback rejects a strictly OLDER epoch ---
// Deploy at epoch E0, change membership (enroll a 3rd node) → epoch E1 > E0. A node whose floor is
// E1 must REFUSE a replay of the E0 manifest (a stale/rolled-back membership), even though its
// signature is valid.
func TestRegression_AntiRollbackRejectsOlderEpoch(t *testing.T) {
	topo := twoNodeTopo()
	topo.Nodes = append(topo.Nodes, model.Node{ID: "node-3", Name: "peer3", Role: "peer", DomainID: "domain-1", Capabilities: model.NodeCapabilities{}})
	topo.Edges = append(topo.Edges, model.Edge{ID: "e-3", FromNodeID: "node-3", ToNodeID: "node-1", Type: "public-endpoint", EndpointHost: "198.51.100.2", EndpointPort: 0, Transport: "udp", IsEnabled: true})
	e := newRegEnv(t, topo, "node-1", "node-2") // node-3 NOT yet enrolled
	a := newKeystone(t)
	e.pinKeystone(a)

	e.deploy(a)
	e0 := mustEpoch(t, e)
	oldFiles := e.served("node-1") // the E0 manifest, validly A-signed

	// Enroll node-3 → membership changes → next deploy advances the epoch.
	e.approve("node-3")
	e.deploy(a)
	e1 := mustEpoch(t, e)
	if e1 <= e0 {
		t.Fatalf("a membership change must advance the epoch: e0=%d e1=%d", e0, e1)
	}

	// A node at floor e1 fed the OLD (e0) manifest must refuse the rollback.
	if _, err := verifyAsNode(oldFiles, "node-1", a.pubPEM, e1); err == nil {
		t.Fatalf("anti-rollback: a node at floor %d must REFUSE the older epoch-%d manifest", e1, e0)
	}
}

// --- Scenario 5: algorithm confusion — dispatch is on the PINNED alg, not the artifact's ---
// A node provisioned with a WebAuthn-ES256 pin must REFUSE an Ed25519-signed served manifest: the
// verifier dispatches on the node's pinned alg, so the raw-Ed25519 artifact is never accepted by an
// ES256 pin (closing the algorithm-confusion door).
func TestRegression_AlgConfusionRejected(t *testing.T) {
	e := newRegEnv(t, twoNodeTopo(), "node-1", "node-2")
	a := newKeystone(t)
	e.pinKeystone(a)
	e.deploy(a) // served manifest is Ed25519-signed

	// Pin the node with a real ES256 credential instead of the Ed25519 one.
	es256PEM := freshES256PEM(t)
	_, err := agent.VerifyMembership(e.served("node-1"), agent.MembershipConfig{
		NodeID:          "node-1",
		OperatorCredPEM: es256PEM,
		OperatorCredAlg: string(trustlist.AlgWebAuthnES256),
	}, 0)
	if err == nil {
		t.Fatal("an ES256-pinned node must REFUSE an Ed25519-signed manifest (algorithm-confusion guard)")
	}
}

// --- Scenario 6: bundle-signing anchor × keystone compose ---
// With BOTH a bundle-signing key (YAOG_BUNDLE_SIGNING_KEY → bundle.sig + signing-anchor) AND the
// keystone ON, a deploy must stage+promote cleanly and the served bundle must pass BOTH the agent's
// tier-1 signature check (VerifyBundle against the pinned signing key) AND the membership gate
// (VerifyMembership against the keystone). The two anchors are independent and compose.
func TestRegression_SigningAnchorComposesWithKeystone(t *testing.T) {
	signPub := setBundleSigningKey(t) // sets YAOG_BUNDLE_SIGNING_KEY for this test
	e := newRegEnv(t, twoNodeTopo(), "node-1", "node-2")
	a := newKeystone(t)
	e.pinKeystone(a)
	e.deploy(a)

	files := e.served("node-1")
	// Tier-1: the bundle is signed by the configured bundle-signing key; the agent verifies it
	// against the pinned signing pubkey.
	if _, err := agent.VerifyBundle(files, signPub); err != nil {
		t.Fatalf("VerifyBundle against the pinned signing key must pass, got %v", err)
	}
	// Tier-2: membership still verifies against the keystone.
	if _, err := verifyAsNode(files, "node-1", a.pubPEM, 0); err != nil {
		t.Fatalf("VerifyMembership must still pass with bundle-signing on, got %v", err)
	}
}

// --- Scenario 7: revoke removes a node from the signed membership ---
// Revoking node-2 and redeploying must produce a manifest that no longer lists node-2 (it is no
// longer a signed member), advance the epoch (membership changed), and node-1 must still verify.
func TestRegression_RevokeUpdatesMembership(t *testing.T) {
	e := newRegEnv(t, twoNodeTopo(), "node-1", "node-2")
	a := newKeystone(t)
	e.pinKeystone(a)
	e.deploy(a)
	e0 := mustEpoch(t, e)
	if !membersContain(t, e, "node-2") {
		t.Fatal("node-2 must be a member before revoke")
	}

	e.revoke("node-2")
	e.deploy(a)
	e1 := mustEpoch(t, e)
	if e1 <= e0 {
		t.Fatalf("revoke must advance the epoch: e0=%d e1=%d", e0, e1)
	}
	if membersContain(t, e, "node-2") {
		t.Fatal("a revoked node must NOT remain a signed member")
	}
	if _, err := verifyAsNode(e.served("node-1"), "node-1", a.pubPEM, e1); err != nil {
		t.Fatalf("node-1 must still verify after node-2 revoke, got %v", err)
	}
}

// --- Scenario 8: a mid-deploy RE-STAGE must not brick the served fleet (bug #1) ---
// After a clean deploy(A), starting a NEW deploy that only RE-STAGES (CompileAndStage, not yet
// signed/promoted) must leave the SERVED slot intact: /config keeps serving the A-signed bundle and
// a node pinned to A still verifies. Before the served-slot split, the re-stage overwrote the single
// trust-list slot with an UNSIGNED manifest, so /config served no signature — every node was
// stranded (the agent's membership gate refused, /config 500'd) until the next promote landed. The
// served (last-promoted) slot now advances ONLY on promote, so a half-finished deploy is invisible
// to the fleet.
func TestRegression_RestageDoesNotBrickServedConfig(t *testing.T) {
	e := newRegEnv(t, twoNodeTopo(), "node-1", "node-2")
	a := newKeystone(t)
	e.pinKeystone(a)
	e.deploy(a)

	// Sanity: the freshly-deployed bundle verifies.
	if _, err := verifyAsNode(e.served("node-1"), "node-1", a.pubPEM, 0); err != nil {
		t.Fatalf("pre-restage: node-1 must verify the A-signed bundle, got %v", err)
	}

	// Start a new deploy that only RE-STAGES (no off-host signature, no promote yet).
	if _, err := controller.CompileAndStage(e.ctx, e.store, tenant, time.Now()); err != nil {
		t.Fatalf("re-stage CompileAndStage: %v", err)
	}

	// The served slot is unchanged: a node pinned to A still verifies the SAME promoted bundle.
	files := e.served("node-1")
	if len(files["trustlist.sig"]) == 0 {
		t.Fatal("re-stage stranded the fleet: the served trust-list signature went missing (bug #1)")
	}
	if _, err := verifyAsNode(files, "node-1", a.pubPEM, 0); err != nil {
		t.Fatalf("re-stage must not brick the served bundle; node-1 must still verify, got %v", err)
	}
}

// --- Scenario 9: GetServedConfig is an ATOMIC (bundle, trust-list) snapshot (bug #3) ---
// /config reads the bundle and the served trust-list under ONE store lock. A two-call reader could
// observe a TORN pair across a concurrent PromoteStaged — an old bundle with a new manifest (or vice
// versa) — whose digest binding then spuriously fails the agent. We hammer GetServedConfig from one
// goroutine while another promotes a sequence of membership-changing deploys, asserting every
// snapshot is internally consistent: the served manifest lists node-1 with a BundleSHA256 equal to
// hex(sha256(checksums.sha256)) of the bundle returned in the SAME snapshot. Run under -race.
func TestRegression_ServedConfigAtomicUnderConcurrentPromote(t *testing.T) {
	e := newRegEnv(t, twoNodeTopo(), "node-1", "node-2")
	a := newKeystone(t)
	e.pinKeystone(a)
	e.deploy(a) // an initial promoted snapshot must exist before the reader starts

	// The READER runs in a background goroutine: it only ever calls t.Errorf (which is safe from
	// non-test goroutines) and a PURE consistency check — never t.Fatalf, whose FailNow must stay on
	// the test goroutine. The WRITER (e.deploy, which uses t.Fatalf) therefore stays on the main
	// goroutine.
	stop := make(chan struct{})
	readerDone := make(chan struct{})
	go func() {
		defer close(readerDone)
		for {
			select {
			case <-stop:
				return
			default:
			}
			sc, err := e.store.GetServedConfig(e.ctx, tenant, "node-1")
			if err != nil {
				t.Errorf("GetServedConfig: %v", err)
				return
			}
			if cerr := servedSnapshotConsistent(sc); cerr != nil {
				t.Errorf("inconsistent served snapshot: %v", cerr)
				return
			}
		}
	}()

	// Writer (main goroutine): each round flips node-2's membership, which changes node-1's peer set
	// (hence its bundle) AND the manifest's member tuple — so a torn read would pair mismatched
	// generations. node-1 (the router) stays a member throughout, so its digest binding is always
	// assertable.
	const rounds = 12
	for i := 0; i < rounds; i++ {
		if i%2 == 0 {
			e.revoke("node-2")
		} else {
			e.approve("node-2")
		}
		e.deploy(a)
	}
	close(stop)
	<-readerDone
}

// --- helpers ---

// servedSnapshotConsistent is a PURE check (no *testing.T, safe to call from any goroutine): the
// served snapshot must carry a trust-list whose node-1 member entry binds the EXACT digest of the
// bundle in the SAME snapshot (BundleSHA256 == hex(sha256(checksums.sha256))). A torn (old-bundle,
// new-manifest) read returned by a non-atomic /config reader would violate this.
func servedSnapshotConsistent(sc controller.ServedConfig) error {
	if !sc.HasTrustList {
		return errors.New("served snapshot under keystone must carry a trust-list")
	}
	checksums, ok := sc.Bundle.Files["checksums.sha256"]
	if !ok {
		return errors.New("served bundle is missing checksums.sha256")
	}
	sum := sha256.Sum256(checksums)
	want := hex.EncodeToString(sum[:])
	var tl trustlist.TrustList
	if err := json.Unmarshal(sc.TrustList.TrustListJSON, &tl); err != nil {
		return fmt.Errorf("unmarshal served manifest: %w", err)
	}
	for _, m := range tl.Members {
		if m.NodeID == "node-1" {
			if m.BundleSHA256 != want {
				return fmt.Errorf("TORN read: served manifest binds node-1 digest %s but the snapshot bundle hashes to %s", m.BundleSHA256, want)
			}
			return nil
		}
	}
	return errors.New("served manifest does not list node-1")
}

func mustEpoch(t *testing.T, e *regEnv) int64 {
	t.Helper()
	stored, err := e.store.GetServedTrustList(e.ctx, tenant)
	if err != nil {
		t.Fatalf("GetServedTrustList(epoch): %v", err)
	}
	return stored.Epoch
}

func membersContain(t *testing.T, e *regEnv, nodeID string) bool {
	t.Helper()
	stored, err := e.store.GetServedTrustList(e.ctx, tenant)
	if err != nil {
		t.Fatalf("GetServedTrustList(members): %v", err)
	}
	var tl trustlist.TrustList
	if err := json.Unmarshal(stored.TrustListJSON, &tl); err != nil {
		t.Fatalf("unmarshal manifest: %v", err)
	}
	for _, m := range tl.Members {
		if m.NodeID == nodeID {
			return true
		}
	}
	return false
}

// freshES256PEM returns a real P-256 ECDSA PKIX public-key PEM (for the algorithm-confusion test).
func freshES256PEM(t *testing.T) []byte {
	t.Helper()
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("ecdsa gen: %v", err)
	}
	der, err := x509.MarshalPKIXPublicKey(&priv.PublicKey)
	if err != nil {
		t.Fatalf("marshal pkix: %v", err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der})
}

// setBundleSigningKey writes a fresh Ed25519 PKCS#8 PEM and points YAOG_BUNDLE_SIGNING_KEY at it for
// the duration of the test (t.Setenv restores it), returning the matching PKIX public-key PEM the
// agent verifies bundles against.
func setBundleSigningKey(t *testing.T) []byte {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("genkey: %v", err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		t.Fatalf("marshal pkcs8: %v", err)
	}
	path := filepath.Join(t.TempDir(), "bundle-signing.pem")
	if err := os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}), 0o600); err != nil {
		t.Fatalf("write signing key: %v", err)
	}
	t.Setenv(bundlesig.EnvSigningKey, path)
	return bundlesig.MarshalPublicKeyPEM(pub)
}
