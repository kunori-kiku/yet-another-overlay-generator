package agent_test

// keystone_test.go is the agent-side end-to-end test for the off-host signed
// trust-list keystone (plan-5.1c). It proves the AGENT half of the contract: a node
// provisioned with a pinned OFF-HOST operator credential verifies the bundle's
// membership trust-list OFFLINE before applying, so a breached controller cannot
// forge membership; and that the gate is OPT-IN — with NO pinned credential the agent
// applies exactly as it did before the trust-list existed.
//
// It drives the REAL controller pipeline (the same two-mux httptest controller the
// sibling controller_client_test.go stands up, reusing its newCtlEnv/enrollViaAgent/
// doOperator helpers) so the wire is verified end to end:
//
//	pin operator credential (POST /operator-credential) -> enroll 2 nodes ->
//	GET /trustlist -> sign the canonical bytes off-host (a software Ed25519 signer
//	standing in for the browser/hardware authenticator) -> POST /trustlist-signature
//	(200; a SUBSTITUTED trustlist_json -> 409; a bad sig -> 400) -> CompileAndStage ->
//	promote -> the agent's ControllerClient Fetch returns the bundle (now carrying
//	trustlist.json + trustlist.sig covered by checksums) -> agent.VerifyMembership with
//	the pinned ed25519 credential PASSES.
//
// Negatives over that real bundle: a wg pubkey not in signed members -> FAIL; missing
// trustlist.sig when pinned -> FAIL; an older epoch -> FAIL. Plus CompileAndStage after
// a membership change with no re-sign -> error, and keystone-OFF deploy+verify behaves
// exactly as before (no trust-list required, VerifyMembership a no-op).
//
// The bundle apply (install.sh) is NOT executed — a unit test must not run a root
// script; asserting VerifyBundle + VerifyMembership pass is the documented stand-in for
// "would apply" (the same convention controller_client_test.go uses).

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/kunorikiku/yet-another-overlay-generator/internal/agent"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/bundlesig"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/model"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/trustlist"
)

// keystoneSigner bundles the software Ed25519 operator signer (stand-in for the
// browser/hardware authenticator) and the PKIX PEM a node pins out of band.
type keystoneSigner struct {
	signer *trustlist.Ed25519Signer
	pinPEM []byte // PKIX PEM of the operator's ed25519 public key
}

// newKeystoneSigner mints a fresh software operator signer + its pinned PEM.
func newKeystoneSigner(t *testing.T) keystoneSigner {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("ed25519.GenerateKey: %v", err)
	}
	return keystoneSigner{
		signer: trustlist.NewEd25519Signer(priv),
		pinPEM: bundlesig.MarshalPublicKeyPEM(pub),
	}
}

// pinOperatorCredential POSTs the operator's ed25519 public-key PEM to
// /operator-credential (turning keystone ON), asserting 200.
func (e *ctlEnv) pinOperatorCredential(t *testing.T, pinPEM []byte) {
	t.Helper()
	body, err := json.Marshal(map[string]string{
		"alg":            string(trustlist.AlgEd25519),
		"credential_id":  "test-operator",
		"public_key_pem": string(pinPEM),
	})
	if err != nil {
		t.Fatalf("marshal operator-credential: %v", err)
	}
	if status := doOperator(t, http.MethodPost, e.opSrv.URL+"/api/v1/controller/operator-credential", body); status != http.StatusOK {
		t.Fatalf("operator-credential: status %d, want 200", status)
	}
}

// fetchTrustListCanonical GETs /trustlist and returns the canonical bytes the operator
// must sign (base64-decoded) plus the epoch they carry.
func (e *ctlEnv) fetchTrustListCanonical(t *testing.T) (canonical []byte, epoch int64) {
	t.Helper()
	body := e.doOperatorJSON(t, http.MethodGet, e.opSrv.URL+"/api/v1/controller/trustlist", nil)
	var resp struct {
		TrustListJSON string `json:"trustlist_json"`
		Epoch         int64  `json:"epoch"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		t.Fatalf("decode /trustlist: %v", err)
	}
	canonical, err := base64.StdEncoding.DecodeString(resp.TrustListJSON)
	if err != nil {
		t.Fatalf("/trustlist trustlist_json not base64: %v", err)
	}
	return canonical, resp.Epoch
}

// signCanonical signs the canonical trust-list bytes with the software operator signer.
// It unmarshals the canonical bytes back into a TrustList (so the signer signs the same
// document the controller built) and returns the SignedTrustList artifact.
func (ks keystoneSigner) signCanonical(t *testing.T, canonical []byte) trustlist.SignedTrustList {
	t.Helper()
	var tl trustlist.TrustList
	if err := json.Unmarshal(canonical, &tl); err != nil {
		t.Fatalf("unmarshal canonical trust-list: %v", err)
	}
	signed, err := ks.signer.Sign(tl)
	if err != nil {
		t.Fatalf("signer.Sign: %v", err)
	}
	return signed
}

// postTrustListSignature POSTs a signed trust-list to /trustlist-signature and returns
// the status code. canonicalB64 is the base64 the operator claims to have signed
// (normally base64(canonical); a mismatch triggers the 409 substitution guard).
func (e *ctlEnv) postTrustListSignature(t *testing.T, canonicalB64 string, signed trustlist.SignedTrustList) int {
	t.Helper()
	body, err := json.Marshal(map[string]any{
		"trustlist_json": canonicalB64,
		"signed":         signed,
	})
	if err != nil {
		t.Fatalf("marshal trustlist-signature: %v", err)
	}
	return doOperator(t, http.MethodPost, e.opSrv.URL+"/api/v1/controller/trustlist-signature", body)
}

// keystoneBundle drives the full keystone happy path through the real controller and
// returns the target node's fetched bundle (carrying trustlist.json + trustlist.sig)
// plus the pinned credential PEM. It also exercises the 409 (substitution) and 400 (bad
// signature) rejections on /trustlist-signature before submitting the valid signature.
func keystoneBundle(t *testing.T) (files map[string][]byte, pinPEM []byte, epoch int64) {
	t.Helper()
	env := newCtlEnv(t)
	ks := newKeystoneSigner(t)

	// (1) Pin the operator credential (keystone ON).
	env.pinOperatorCredential(t, ks.pinPEM)

	// (2) Enroll both nodes so the whole graph compiles and both are signed members.
	node1Token := env.enrollViaAgent(t, "node-1")
	_ = env.enrollViaAgent(t, "node-2")

	// (3) Fetch the canonical trust-list bytes and sign them off-host.
	canonical, ep := env.fetchTrustListCanonical(t)
	signed := ks.signCanonical(t, canonical)
	canonicalB64 := base64.StdEncoding.EncodeToString(canonical)

	// (3a) A SUBSTITUTED trustlist_json (different bytes than signed) -> 409.
	substitutedB64 := base64.StdEncoding.EncodeToString(append([]byte(" "), canonical...))
	if status := env.postTrustListSignature(t, substitutedB64, signed); status != http.StatusConflict {
		t.Fatalf("trustlist-signature(substituted): status %d, want 409", status)
	}

	// (3b) A BAD signature (signed by an unrelated key) -> 400.
	otherKS := newKeystoneSigner(t)
	badSigned := otherKS.signCanonical(t, canonical)
	if status := env.postTrustListSignature(t, canonicalB64, badSigned); status != http.StatusBadRequest {
		t.Fatalf("trustlist-signature(bad sig): status %d, want 400", status)
	}

	// (3c) The valid signature -> 200.
	if status := env.postTrustListSignature(t, canonicalB64, signed); status != http.StatusOK {
		t.Fatalf("trustlist-signature(valid): status %d, want 200", status)
	}

	// (4) CompileAndStage (keystone gate: requires the signed trust-list to match the
	// approved membership) + promote, then fetch node-1's bundle via the agent client.
	env.stageAndPromote(t)

	agentClient, err := agent.NewControllerClient(env.agentSrv.URL, node1Token)
	if err != nil {
		t.Fatalf("NewControllerClient(bearer): %v", err)
	}
	files, err = agentClient.Fetch("node-1")
	if err != nil {
		t.Fatalf("Fetch(node-1): %v", err)
	}
	if _, ok := files["trustlist.json"]; !ok {
		t.Fatalf("fetched keystone bundle missing trustlist.json (keys: %v)", keysOf(files))
	}
	if _, ok := files["trustlist.sig"]; !ok {
		t.Fatalf("fetched keystone bundle missing trustlist.sig (keys: %v)", keysOf(files))
	}
	return files, ks.pinPEM, ep
}

// edCfg builds an ed25519 keystone MembershipConfig for the given node.
func edCfg(nodeID string, pinPEM []byte) agent.MembershipConfig {
	return agent.MembershipConfig{
		NodeID:          nodeID,
		OperatorCredPEM: pinPEM,
		OperatorCredAlg: string(trustlist.AlgEd25519),
	}
}

// --- tests ---

// TestKeystone_AgentVerifiesRealBundle is the agent-side happy path: a bundle produced
// by the REAL controller keystone pipeline (signed trust-list embedded + covered by
// checksums) passes both the agent's tier-1 VerifyBundle and the keystone
// VerifyMembership against the pinned ed25519 credential. VerifyMembership returns the
// trust-list epoch.
func TestKeystone_AgentVerifiesRealBundle(t *testing.T) {
	files, pinPEM, epoch := keystoneBundle(t)

	// Tier-1 integrity (unsigned in CI, so pin nothing): trustlist.json/.sig are covered
	// by checksums.sha256, so a complete bundle must still verify.
	if _, err := agent.VerifyBundle(files, nil); err != nil {
		t.Fatalf("VerifyBundle over real keystone bundle: %v", err)
	}

	// Keystone gate: PASSES, returning the embedded epoch.
	got, err := agent.VerifyMembership(files, edCfg("node-1", pinPEM), 0)
	if err != nil {
		t.Fatalf("VerifyMembership over real keystone bundle: %v", err)
	}
	if got != epoch {
		t.Fatalf("VerifyMembership returned epoch %d, want %d (the /trustlist epoch)", got, epoch)
	}
}

// TestKeystone_UnsignedPeerRejected: a peer public key that appears in the node's conf
// must be a signed member. We tamper the FETCHED trust-list to drop node-2 (node-1's
// peer) from the members, re-canonicalize, re-sign with the SAME pinned operator key
// (so the signature itself is valid), and rebuild the integrity layer — yet
// VerifyMembership must still refuse, because the conf's peer key is no longer a signed
// member.
func TestKeystone_UnsignedPeerRejected(t *testing.T) {
	// We need the operator's PRIVATE key to re-sign a tampered list, so drive the
	// keystone flow inline keeping the signer (keystoneBundle hides it). This is the
	// only negative that must re-sign, so it does not reuse the keystoneBundle helper.
	env := newCtlEnv(t)
	ks := newKeystoneSigner(t)
	env.pinOperatorCredential(t, ks.pinPEM)
	node1Token := env.enrollViaAgent(t, "node-1")
	_ = env.enrollViaAgent(t, "node-2")
	canonical, _ := env.fetchTrustListCanonical(t)
	if status := env.postTrustListSignature(t, base64.StdEncoding.EncodeToString(canonical), ks.signCanonical(t, canonical)); status != http.StatusOK {
		t.Fatalf("trustlist-signature(valid): status %d, want 200", status)
	}
	env.stageAndPromote(t)
	agentClient, err := agent.NewControllerClient(env.agentSrv.URL, node1Token)
	if err != nil {
		t.Fatalf("NewControllerClient: %v", err)
	}
	realFiles, err := agentClient.Fetch("node-1")
	if err != nil {
		t.Fatalf("Fetch(node-1): %v", err)
	}

	// Tamper: keep only node-1 as a member (drop the peer), re-canonicalize + re-sign.
	var tl trustlist.TrustList
	if err := json.Unmarshal(realFiles["trustlist.json"], &tl); err != nil {
		t.Fatalf("unmarshal trustlist.json: %v", err)
	}
	var selfOnly []trustlist.Member
	for _, m := range tl.Members {
		if m.NodeID == "node-1" {
			selfOnly = append(selfOnly, m)
		}
	}
	tl.Members = selfOnly
	tamperedCanonical, err := trustlist.Canonical(tl)
	if err != nil {
		t.Fatalf("Canonical(tampered): %v", err)
	}
	tamperedSigned, err := ks.signer.Sign(tl)
	if err != nil {
		t.Fatalf("Sign(tampered): %v", err)
	}
	tamperedSigJSON, err := json.Marshal(tamperedSigned)
	if err != nil {
		t.Fatalf("marshal tampered signed: %v", err)
	}
	tampered := augmentIntegrity(realFiles, map[string][]byte{
		"trustlist.json": tamperedCanonical,
		"trustlist.sig":  tamperedSigJSON,
	})

	// The tampered trust-list verifies cryptographically (valid pinned-key signature) but
	// the conf's peer key is not a signed member -> fail-closed.
	if _, err := agent.VerifyMembership(tampered, edCfg("node-1", ks.pinPEM), 0); err == nil {
		t.Fatalf("VerifyMembership accepted a conf peer absent from signed members; want failure")
	}
}

// TestKeystone_MissingSigRejected: with a pinned credential, a bundle that lacks
// trustlist.sig (or trustlist.json) must fail closed.
func TestKeystone_MissingSigRejected(t *testing.T) {
	files, pinPEM, _ := keystoneBundle(t)

	noSig := dropFile(files, "trustlist.sig")
	if _, err := agent.VerifyMembership(noSig, edCfg("node-1", pinPEM), 0); err == nil {
		t.Fatalf("VerifyMembership accepted a bundle with no trustlist.sig under a pinned credential; want fail-closed")
	}

	noTL := dropFile(files, "trustlist.json")
	if _, err := agent.VerifyMembership(noTL, edCfg("node-1", pinPEM), 0); err == nil {
		t.Fatalf("VerifyMembership accepted a bundle with no trustlist.json under a pinned credential; want fail-closed")
	}
}

// TestKeystone_OlderEpochRejected: a trust-list whose epoch is strictly below the
// last-applied floor is a membership rollback -> FAIL; an equal epoch is allowed.
func TestKeystone_OlderEpochRejected(t *testing.T) {
	files, pinPEM, epoch := keystoneBundle(t)
	cfg := edCfg("node-1", pinPEM)

	// Floor strictly above the bundle's epoch -> rollback, refuse.
	if _, err := agent.VerifyMembership(files, cfg, epoch+1); err == nil {
		t.Fatalf("VerifyMembership accepted epoch %d below floor %d; want rollback failure", epoch, epoch+1)
	}
	// Equal floor -> idempotent re-apply, allowed.
	if _, err := agent.VerifyMembership(files, cfg, epoch); err != nil {
		t.Fatalf("VerifyMembership rejected an equal epoch (idempotent re-apply): %v", err)
	}
}

// TestKeystone_SubstitutedTrustListJSONRejected: the distributed trustlist.json must be
// its own canonical form (the Verify CALLER CONTRACT). A re-encoding that parses to the
// same TrustList but has different bytes (a leading space) must be refused even though
// the signature over Canonical(parsed) still verifies.
func TestKeystone_SubstitutedTrustListJSONRejected(t *testing.T) {
	files, pinPEM, _ := keystoneBundle(t)

	// Replace trustlist.json with a non-canonical re-encoding (leading space: valid JSON,
	// different bytes, parses to the same TrustList). Keep the original signature.
	substituted := augmentIntegrity(files, map[string][]byte{
		"trustlist.json": append([]byte(" "), files["trustlist.json"]...),
	})
	if _, err := agent.VerifyMembership(substituted, edCfg("node-1", pinPEM), 0); err == nil {
		t.Fatalf("VerifyMembership accepted a trustlist.json that is not its own canonical form; want failure")
	}
}

// TestKeystone_OptInOff: keystone OFF (no operator credential pinned) deploys and
// verifies exactly as before. The controller bundle carries NO trust-list, and the
// agent's VerifyMembership (no OperatorCredPEM) is a no-op returning (0, nil), while
// VerifyBundle still passes — the full back-compat guarantee.
func TestKeystone_OptInOff(t *testing.T) {
	env := newCtlEnv(t)

	// No operator credential pinned: keystone OFF.
	node1Token := env.enrollViaAgent(t, "node-1")
	_ = env.enrollViaAgent(t, "node-2")
	env.stageAndPromote(t)

	agentClient, err := agent.NewControllerClient(env.agentSrv.URL, node1Token)
	if err != nil {
		t.Fatalf("NewControllerClient: %v", err)
	}
	files, err := agentClient.Fetch("node-1")
	if err != nil {
		t.Fatalf("Fetch(node-1): %v", err)
	}
	if _, ok := files["trustlist.json"]; ok {
		t.Fatalf("keystone-off bundle unexpectedly carries trustlist.json")
	}

	// Tier-1 verify passes (the unchanged today behavior).
	if _, err := agent.VerifyBundle(files, nil); err != nil {
		t.Fatalf("VerifyBundle over keystone-off bundle: %v", err)
	}
	// VerifyMembership with no pinned credential is a no-op: (0, nil), even though the
	// bundle has no trust-list at all.
	epoch, err := agent.VerifyMembership(files, agent.MembershipConfig{NodeID: "node-1"}, 0)
	if err != nil {
		t.Fatalf("VerifyMembership keystone-off returned error: %v", err)
	}
	if epoch != 0 {
		t.Fatalf("VerifyMembership keystone-off returned epoch %d, want 0", epoch)
	}
}

// TestKeystone_CompileAndStageRequiresResignAfterMembershipChange asserts the
// controller-side gate the agent depends on: once keystone is ON, enrolling a NEW node
// (changing membership) without re-signing the trust-list makes CompileAndStage refuse
// — so a bundle with a stale membership proof is never produced for an agent to reject.
// This is the controller half of the agent's fail-closed contract; we assert it here so
// the agent partition's E2E covers the membership-change path end to end.
func TestKeystone_CompileAndStageRequiresResignAfterMembershipChange(t *testing.T) {
	env := newCtlEnv(t)
	ks := newKeystoneSigner(t)
	env.pinOperatorCredential(t, ks.pinPEM)

	// Enroll two nodes, sign the trust-list over THAT membership, and stage+promote OK.
	_ = env.enrollViaAgent(t, "node-1")
	_ = env.enrollViaAgent(t, "node-2")
	canonical, _ := env.fetchTrustListCanonical(t)
	if status := env.postTrustListSignature(t, base64.StdEncoding.EncodeToString(canonical), ks.signCanonical(t, canonical)); status != http.StatusOK {
		t.Fatalf("trustlist-signature(valid): status %d, want 200", status)
	}
	if status := doOperator(t, http.MethodPost, env.opSrv.URL+"/api/v1/controller/stage", []byte("{}")); status != http.StatusOK {
		t.Fatalf("stage with matching signed trust-list: status %d, want 200", status)
	}

	// Now change membership (enroll a third node) WITHOUT re-signing. The smallTopo only
	// has node-1/node-2, so the third node has no topology slot; but the trust-list the
	// controller builds is over APPROVED registry nodes, so enrolling node-3 changes the
	// approved-member set and the previously-signed list no longer matches.
	//
	// We must enroll node-3 against a topology that includes it, so update the topology to
	// a 3-node graph first, then enroll node-3, then attempt stage -> must fail.
	if status := doOperator(t, http.MethodPost, env.opSrv.URL+"/api/v1/controller/update-topology", threeNodeTopoJSON(t)); status != http.StatusOK {
		t.Fatalf("update-topology(3 nodes): status %d, want 200", status)
	}
	_ = env.enrollViaAgent(t, "node-3")

	// Stage now: keystone is ON, membership changed, no re-sign -> the controller refuses
	// (the agent never gets a stale-membership bundle). HandleStage maps the error to 422.
	if status := doOperator(t, http.MethodPost, env.opSrv.URL+"/api/v1/controller/stage", []byte("{}")); status == http.StatusOK {
		t.Fatalf("stage after a membership change with no re-sign: status 200, want a refusal (membership changed)")
	}
}

// TestKeystone_RunInvokesMembershipGate proves agent.Run WIRES VerifyMembership into
// the control loop AFTER tier-1 verify and BEFORE stage/apply: given a pinned
// credential and a keystone bundle whose trust-list omits a conf peer, Run returns an
// error and never stages install.sh (keep-last-good — the running overlay is untouched).
// It complements the standalone VerifyMembership tests by exercising the Run path.
func TestKeystone_RunInvokesMembershipGate(t *testing.T) {
	// Build a keystone bundle inline keeping the signer, then tamper the trust-list to
	// drop the peer (a valid signature over a list that excludes node-1's conf peer).
	env := newCtlEnv(t)
	ks := newKeystoneSigner(t)
	env.pinOperatorCredential(t, ks.pinPEM)
	node1Token := env.enrollViaAgent(t, "node-1")
	_ = env.enrollViaAgent(t, "node-2")
	canonical, _ := env.fetchTrustListCanonical(t)
	if status := env.postTrustListSignature(t, base64.StdEncoding.EncodeToString(canonical), ks.signCanonical(t, canonical)); status != http.StatusOK {
		t.Fatalf("trustlist-signature(valid): status %d, want 200", status)
	}
	env.stageAndPromote(t)
	agentClient, err := agent.NewControllerClient(env.agentSrv.URL, node1Token)
	if err != nil {
		t.Fatalf("NewControllerClient: %v", err)
	}
	realFiles, err := agentClient.Fetch("node-1")
	if err != nil {
		t.Fatalf("Fetch(node-1): %v", err)
	}

	var tl trustlist.TrustList
	if err := json.Unmarshal(realFiles["trustlist.json"], &tl); err != nil {
		t.Fatalf("unmarshal trustlist.json: %v", err)
	}
	var selfOnly []trustlist.Member
	for _, m := range tl.Members {
		if m.NodeID == "node-1" {
			selfOnly = append(selfOnly, m)
		}
	}
	tl.Members = selfOnly
	tc, err := trustlist.Canonical(tl)
	if err != nil {
		t.Fatalf("Canonical(tampered): %v", err)
	}
	ts, err := ks.signer.Sign(tl)
	if err != nil {
		t.Fatalf("Sign(tampered): %v", err)
	}
	tsJSON, err := json.Marshal(ts)
	if err != nil {
		t.Fatalf("marshal tampered signed: %v", err)
	}
	tampered := augmentIntegrity(realFiles, map[string][]byte{
		"trustlist.json": tc,
		"trustlist.sig":  tsJSON,
	})

	root := writeNodeBundle(t, "node-1", tampered)
	stagingDir := t.TempDir()
	_, runErr := agent.Run(&agent.Config{
		NodeID:          "node-1",
		Source:          agent.NewDirSource(root),
		OperatorCredPEM: ks.pinPEM,
		OperatorCredAlg: string(trustlist.AlgEd25519),
		StateDir:        t.TempDir(),
		StagingDir:      stagingDir,
		Stdout:          discard{},
		Stderr:          discard{},
	})
	if runErr == nil {
		t.Fatalf("Run accepted a bundle whose trust-list omits a conf peer; want a membership failure")
	}
	if _, statErr := os.Stat(filepath.Join(stagingDir, "install.sh")); statErr == nil {
		t.Fatalf("Run staged install.sh despite a membership failure; the gate must run BEFORE stage/apply")
	}
}

// writeNodeBundle materializes a bundle file map under root/<nodeID>/ so a DirSource can
// fetch it (the agent.Run path). It returns the DirSource root.
func writeNodeBundle(t *testing.T, nodeID string, files map[string][]byte) string {
	t.Helper()
	root := t.TempDir()
	nodeRoot := filepath.Join(root, nodeID)
	for rel, content := range files {
		dst := filepath.Join(nodeRoot, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(dst), 0755); err != nil {
			t.Fatalf("mkdir %s: %v", dst, err)
		}
		if err := os.WriteFile(dst, content, 0644); err != nil {
			t.Fatalf("write %s: %v", dst, err)
		}
	}
	return root
}

// discard is an io.Writer that swallows install.sh output so a test never spews a
// failing root-script exec to the log.
type discard struct{}

func (discard) Write(p []byte) (int, error) { return len(p), nil }

// --- bundle-tampering helpers ---

// augmentIntegrity returns a copy of base with extra files overlaid, then rebuilds
// checksums.sha256 over the integrity-covered set (EXACTLY the files Export covers: all
// bundle files except manifest.json, bundle.sig, signing-pubkey.pem, README.txt and
// checksums.sha256 itself). It lets a test mutate a trust-list file and still produce a
// bundle whose tier-1 checksums are self-consistent, so VerifyMembership (not a checksum
// mismatch) is what rejects it.
func augmentIntegrity(base, extra map[string][]byte) map[string][]byte {
	out := make(map[string][]byte, len(base)+len(extra))
	for k, v := range base {
		cp := make([]byte, len(v))
		copy(cp, v)
		out[k] = cp
	}
	for k, v := range extra {
		cp := make([]byte, len(v))
		copy(cp, v)
		out[k] = cp
	}
	excluded := map[string]bool{
		"manifest.json":      true,
		"bundle.sig":         true,
		"signing-pubkey.pem": true,
		"README.txt":         true,
		"checksums.sha256":   true,
	}
	covered := make(map[string]string)
	for k, v := range out {
		if excluded[k] {
			continue
		}
		covered[k] = string(v)
	}
	out["checksums.sha256"] = bundlesig.Canonicalize(covered)
	return out
}

// dropFile returns a copy of files without the named entry, then rebuilds the integrity
// layer so the only thing missing is the dropped file (not a checksum mismatch).
func dropFile(files map[string][]byte, name string) map[string][]byte {
	out := make(map[string][]byte, len(files))
	for k, v := range files {
		if k == name {
			continue
		}
		out[k] = v
	}
	return augmentIntegrity(out, nil)
}

// threeNodeTopoJSON returns a router + 2-peer topology JSON (node-1/node-2/node-3) so a
// third node can enroll and change the approved-membership set. It extends smallTopo
// (router node-1 + peer node-2) with a second peer node-3 dialing the router.
func threeNodeTopoJSON(t *testing.T) []byte {
	t.Helper()
	topo := smallTopo()
	topo.Nodes = append(topo.Nodes, model.Node{
		ID: "node-3", Name: "peer3",
		Role: "peer", DomainID: "domain-1", ListenPort: 51820,
		Capabilities: model.NodeCapabilities{CanAcceptInbound: false, CanForward: false, HasPublicIP: false},
	})
	topo.Edges = append(topo.Edges, model.Edge{
		ID: "e-2", FromNodeID: "node-3", ToNodeID: "node-1",
		Type: "public-endpoint", EndpointHost: "198.51.100.1", EndpointPort: 0, Transport: "udp", IsEnabled: true,
	})
	b, err := json.Marshal(topo)
	if err != nil {
		t.Fatalf("marshal three-node topology: %v", err)
	}
	return b
}
