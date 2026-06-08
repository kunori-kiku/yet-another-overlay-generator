package agent_test

// keystone_test.go is the agent-side end-to-end test for the off-host signed
// trust-list keystone (plan-5.1c + the install.sh-coverage CORRECTION, 2026-06-08).
// It proves the AGENT half of the contract: a node provisioned with a pinned OFF-HOST
// operator credential verifies the bundle's membership AND its exact contents OFFLINE
// before applying, so a breached controller can forge neither membership nor what RUNS;
// and that the gate is OPT-IN — with NO pinned credential the agent applies exactly as
// it did before the trust-list existed.
//
// The corrected design binds, per member, the node's BUNDLE DIGEST
// (bundle_sha256 = hex(sha256(checksums.sha256))) into the off-host-signed manifest, and
// the manifest (trustlist.json) + its signature (trustlist.sig) are served ALONGSIDE the
// bundle, OUTSIDE checksums.sha256 (they bind the checksums digest, so they cannot live
// inside it). The deploy flow is therefore stage-then-sign-then-promote:
//
//	pin operator credential (POST /operator-credential) -> enroll 2 nodes ->
//	update-topology + stage (CompileAndStage renders bundles, computes each node's
//	bundle digest, and STORES the to-be-signed manifest) -> GET /trustlist (the staged
//	manifest canonical) -> sign off-host (a software Ed25519 signer standing in for the
//	browser/hardware authenticator) -> POST /trustlist-signature (200; a SUBSTITUTED
//	trustlist_json -> 409; a bad sig -> 400) -> promote (refused without a matching
//	signed manifest) -> the agent's ControllerClient Fetch returns the bundle (now
//	carrying trustlist.json + trustlist.sig NOT covered by checksums) -> agent.
//	VerifyMembership with the pinned ed25519 credential PASSES.
//
// Negatives over that real bundle: a TAMPERED install.sh (rebuilt checksums + tier-1
// bundle.sig, off-host manifest unchanged) -> FAIL (the load-bearing bypass-closed test);
// a wg pubkey not in signed members -> FAIL; missing trustlist.sig when pinned -> FAIL;
// an older epoch -> FAIL; a non-canonical trustlist.json -> FAIL. Plus promote-without-a-
// matching-signature -> refused, and keystone-OFF deploy+verify behaves exactly as before
// (no trust-list required, VerifyMembership a no-op).
//
// The bundle apply (install.sh) is NOT executed — a unit test must not run a root
// script; asserting VerifyBundle + VerifyMembership pass is the documented stand-in for
// "would apply" (the same convention controller_client_test.go uses).

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/kunorikiku/yet-another-overlay-generator/internal/agent"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/bundlesig"
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

// updateTopology POSTs the given topology JSON to /update-topology, asserting 200. It is
// the operator step that makes a topology available for a later stage.
func (e *ctlEnv) updateTopology(t *testing.T, topoJSON []byte) {
	t.Helper()
	if status := doOperator(t, http.MethodPost, e.opSrv.URL+"/api/v1/controller/update-topology", topoJSON); status != http.StatusOK {
		t.Fatalf("update-topology: status %d, want 200", status)
	}
}

// stageSmallTopo updates the topology to smallTopo and stages it (NO promote). Under the
// corrected keystone flow staging is what renders the bundles and stores the to-be-signed
// manifest, so the operator stages BEFORE signing. It asserts the stage returns 200.
func (e *ctlEnv) stageSmallTopo(t *testing.T) {
	t.Helper()
	topoJSON, err := json.Marshal(smallTopo())
	if err != nil {
		t.Fatalf("marshal topology: %v", err)
	}
	e.updateTopology(t, topoJSON)
	if status := doOperator(t, http.MethodPost, e.opSrv.URL+"/api/v1/controller/stage", []byte("{}")); status != http.StatusOK {
		t.Fatalf("stage: status %d, want 200", status)
	}
}

// promote POSTs /promote and returns (generation, statusCode). Under keystone the gate
// refuses to promote (non-200) unless a valid off-host signature over the staged manifest
// exists, so the caller asserts the status it expects.
func (e *ctlEnv) promote(t *testing.T) (gen int64, status int) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, e.opSrv.URL+"/api/v1/controller/promote", bytes.NewReader([]byte("{}")))
	if err != nil {
		t.Fatalf("promote NewRequest: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+operatorPlaintext)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("promote: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 0, resp.StatusCode
	}
	var out struct {
		Generation int64 `json:"generation"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode promote response: %v", err)
	}
	return out.Generation, resp.StatusCode
}

// fetchTrustListCanonical GETs /trustlist and returns the canonical bytes of the STAGED
// to-be-signed manifest (base64-decoded) plus the epoch they carry. The operator signs
// exactly these bytes.
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
// document the controller built, bundle digests and all) and returns the SignedTrustList.
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

// signStaged is the happy-path glue: GET the staged manifest, sign it, POST the valid
// signature (asserting 200). It returns the epoch the manifest carried.
func (e *ctlEnv) signStaged(t *testing.T, ks keystoneSigner) int64 {
	t.Helper()
	canonical, ep := e.fetchTrustListCanonical(t)
	signed := ks.signCanonical(t, canonical)
	if status := e.postTrustListSignature(t, base64.StdEncoding.EncodeToString(canonical), signed); status != http.StatusOK {
		t.Fatalf("trustlist-signature(valid): status %d, want 200", status)
	}
	return ep
}

// keystoneBundle drives the full corrected keystone happy path through the real
// controller and returns the target node's fetched bundle (carrying trustlist.json +
// trustlist.sig OUTSIDE checksums) plus the pinned credential PEM and the manifest epoch.
// It also exercises the 409 (substitution) and 400 (bad signature) rejections on
// /trustlist-signature before submitting the valid signature.
func keystoneBundle(t *testing.T) (files map[string][]byte, pinPEM []byte, epoch int64) {
	t.Helper()
	env := newCtlEnv(t)
	ks := newKeystoneSigner(t)

	// (1) Pin the operator credential (keystone ON).
	env.pinOperatorCredential(t, ks.pinPEM)

	// (2) Enroll both nodes so the whole graph compiles and both are signed members.
	node1Token := env.enrollViaAgent(t, "node-1")
	_ = env.enrollViaAgent(t, "node-2")

	// (3) STAGE first: the staged bundles' checksums are what the to-be-signed manifest's
	// bundle digests bind, so the manifest cannot exist until the bundles are rendered.
	env.stageSmallTopo(t)

	// (4) Fetch the staged manifest canonical bytes and sign them off-host.
	canonical, ep := env.fetchTrustListCanonical(t)
	signed := ks.signCanonical(t, canonical)
	canonicalB64 := base64.StdEncoding.EncodeToString(canonical)

	// (4a) A SUBSTITUTED trustlist_json (different bytes than signed) -> 409.
	substitutedB64 := base64.StdEncoding.EncodeToString(append([]byte(" "), canonical...))
	if status := env.postTrustListSignature(t, substitutedB64, signed); status != http.StatusConflict {
		t.Fatalf("trustlist-signature(substituted): status %d, want 409", status)
	}

	// (4b) A BAD signature (signed by an unrelated key) -> 400.
	otherKS := newKeystoneSigner(t)
	badSigned := otherKS.signCanonical(t, canonical)
	if status := env.postTrustListSignature(t, canonicalB64, badSigned); status != http.StatusBadRequest {
		t.Fatalf("trustlist-signature(bad sig): status %d, want 400", status)
	}

	// (4c) The valid signature -> 200.
	if status := env.postTrustListSignature(t, canonicalB64, signed); status != http.StatusOK {
		t.Fatalf("trustlist-signature(valid): status %d, want 200", status)
	}

	// (5) PROMOTE: the keystone gate now admits the staged bundles because a valid
	// off-host signature over the staged manifest exists. Then fetch node-1's bundle.
	if _, status := env.promote(t); status != http.StatusOK {
		t.Fatalf("promote with a valid signed manifest: status %d, want 200", status)
	}

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
// by the REAL controller keystone pipeline (signed manifest served alongside, bundle
// digest bound per member) passes both the agent's tier-1 VerifyBundle and the keystone
// VerifyMembership against the pinned ed25519 credential. VerifyMembership returns the
// trust-list epoch.
func TestKeystone_AgentVerifiesRealBundle(t *testing.T) {
	files, pinPEM, epoch := keystoneBundle(t)

	// Tier-1 integrity (unsigned in CI, so pin nothing). trustlist.json/.sig live OUTSIDE
	// checksums.sha256 now, so they are NOT listed there; a complete bundle still verifies.
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

// TestKeystone_InstallShTamperRejected is the LOAD-BEARING bypass-closed test: it proves
// the off-host signature now covers what RUNS, not just the membership list. It simulates
// exactly what a breached controller (holding the operator bearer token + the host-held
// tier-1 signing key) can do — rewrite install.sh to splice a rogue peer, then REBUILD
// checksums.sha256 over the tampered bytes so tier-1 VerifyBundle still passes — while the
// OFF-HOST signed manifest is left UNCHANGED (the attacker lacks the off-host key). With
// the corrected design the node's signed member.BundleSHA256 no longer matches the
// tampered bundle's checksums digest, so agent.VerifyMembership REJECTS even though
// VerifyBundle passes. Before the fix, both passed and the rogue install.sh ran as root.
func TestKeystone_InstallShTamperRejected(t *testing.T) {
	files, pinPEM, _ := keystoneBundle(t)

	// Sanity: the unmodified bundle passes both gates (so a later rejection is the tamper,
	// not a pre-existing fault).
	if _, err := agent.VerifyBundle(files, nil); err != nil {
		t.Fatalf("VerifyBundle over the clean keystone bundle: %v", err)
	}
	if _, err := agent.VerifyMembership(files, edCfg("node-1", pinPEM), 0); err != nil {
		t.Fatalf("VerifyMembership over the clean keystone bundle: %v", err)
	}

	// A breached host rewrites install.sh as root: append a rogue WireGuard splice that
	// would add an unsigned peer with a default route. This is the concrete attack the
	// CORRECTION closes.
	original, ok := files["install.sh"]
	if !ok {
		t.Fatalf("keystone bundle has no install.sh to tamper")
	}
	tamperedInstall := append(append([]byte(nil), original...),
		[]byte("\nwg set wg-overlay peer ROGUEKEYBASE64= allowed-ips 0.0.0.0/0\n")...)

	// REBUILD checksums.sha256 over the tampered install.sh — what a breached host does so
	// its tier-1 integrity layer still self-verifies. trustlist.json/.sig are NOT in the
	// checksummed set (they bind the digest and so live outside it), matching the served
	// bundle exactly.
	tampered := augmentIntegrity(files, map[string][]byte{
		"install.sh": tamperedInstall,
	})

	// The tampered install.sh is now genuinely covered by the rebuilt checksums, so the
	// host-forgeable tier-1 gate (no off-host key needed) still passes — the precise
	// bypass the old membership-only signing left open.
	if _, err := agent.VerifyBundle(tampered, nil); err != nil {
		t.Fatalf("rebuilt-checksums tampered bundle must still pass tier-1 VerifyBundle (a breached host can do this): %v", err)
	}

	// But the OFF-HOST manifest is unchanged: its signed member.BundleSHA256 still binds
	// the ORIGINAL checksums.sha256 digest, which the tampered bundle no longer matches.
	// VerifyMembership must REJECT. THIS is the closed bypass.
	if _, err := agent.VerifyMembership(tampered, edCfg("node-1", pinPEM), 0); err == nil {
		t.Fatalf("VerifyMembership accepted a bundle with a tampered install.sh (off-host signature unchanged); the bundle-digest binding must reject it")
	}
}

// TestKeystone_UnsignedPeerRejected: a peer public key that appears in the node's conf
// must be a signed member. We tamper the FETCHED trust-list to drop node-2 (node-1's
// peer) from the members, re-canonicalize, re-sign with the SAME pinned operator key
// (so the signature itself is valid) and keep node-1's own bundle digest (so the
// bundle-digest binding still passes) — yet VerifyMembership must still refuse, because
// the conf's peer key is no longer a signed member.
func TestKeystone_UnsignedPeerRejected(t *testing.T) {
	env, ks, node1Token := keystoneInlineSetup(t)
	realFiles := fetchNode1(t, env, node1Token)

	// Tamper: keep only node-1 as a member (drop the peer), preserving node-1's
	// bundle_sha256 so the self-digest check passes and the peer-subset check is what
	// rejects. Re-canonicalize + re-sign with the valid pinned key.
	tampered := resignWithMembers(t, ks, realFiles, "node-1")

	if _, err := agent.VerifyMembership(tampered, edCfg("node-1", ks.pinPEM), 0); err == nil {
		t.Fatalf("VerifyMembership accepted a conf peer absent from signed members; want failure")
	}
}

// TestKeystone_MissingSigRejected: with a pinned credential, a bundle that lacks
// trustlist.sig, trustlist.json, or checksums.sha256 must fail closed.
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

	// checksums.sha256 is the manifest the bundle-digest binding hashes; absent it the
	// digest check cannot run, so it must fail closed too.
	noChecksums := dropFileRaw(files, "checksums.sha256")
	if _, err := agent.VerifyMembership(noChecksums, edCfg("node-1", pinPEM), 0); err == nil {
		t.Fatalf("VerifyMembership accepted a bundle with no checksums.sha256 under a pinned credential; want fail-closed")
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
	// trustlist.json is outside checksums, so just swap the served bytes.
	substituted := overlayFiles(files, map[string][]byte{
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

	// No operator credential pinned: keystone OFF. The shared stageAndPromote helper
	// (update-topology + stage + promote) works unchanged because no signature is required.
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

// TestKeystone_PromoteRefusedWithoutSignature asserts the controller-side gate the agent
// depends on: once keystone is ON, staging renders bundles but PROMOTE refuses until a
// valid off-host signature over the staged manifest exists — so a bundle with no (or a
// stale) membership proof is never promoted for an agent to fetch. This is the controller
// half of the agent's fail-closed contract; we assert it here so the agent partition's
// E2E covers the promote-gate path end to end.
func TestKeystone_PromoteRefusedWithoutSignature(t *testing.T) {
	env := newCtlEnv(t)
	ks := newKeystoneSigner(t)
	env.pinOperatorCredential(t, ks.pinPEM)

	_ = env.enrollViaAgent(t, "node-1")
	_ = env.enrollViaAgent(t, "node-2")
	env.stageSmallTopo(t)

	// No signature submitted yet: promote must be refused (keystone ON).
	if _, status := env.promote(t); status == http.StatusOK {
		t.Fatalf("promote with no signed manifest: status 200, want a refusal (keystone requires a signature to promote)")
	}

	// Sign the staged manifest, then promote succeeds.
	env.signStaged(t, ks)
	if _, status := env.promote(t); status != http.StatusOK {
		t.Fatalf("promote after signing the staged manifest: status %d, want 200", status)
	}
}

// TestKeystone_RunInvokesMembershipGate proves agent.Run WIRES VerifyMembership into
// the control loop AFTER tier-1 verify and BEFORE stage/apply: given a pinned
// credential and a keystone bundle whose install.sh was tampered (rebuilt checksums,
// off-host manifest unchanged), Run returns an error and never stages install.sh
// (keep-last-good — the running overlay is untouched). It complements the standalone
// VerifyMembership tests by exercising the Run path with the bundle-digest binding.
func TestKeystone_RunInvokesMembershipGate(t *testing.T) {
	files, pinPEM, _ := keystoneBundle(t)

	// Tamper install.sh + rebuild checksums (the breached-host attack); leave the off-host
	// manifest untouched. The bundle-digest binding makes VerifyMembership reject.
	tamperedInstall := append(append([]byte(nil), files["install.sh"]...),
		[]byte("\nwg set wg-overlay peer ROGUEKEYBASE64= allowed-ips 0.0.0.0/0\n")...)
	tampered := augmentIntegrity(files, map[string][]byte{"install.sh": tamperedInstall})

	root := writeNodeBundle(t, "node-1", tampered)
	stagingDir := t.TempDir()
	_, runErr := agent.Run(&agent.Config{
		NodeID:          "node-1",
		Source:          agent.NewDirSource(root),
		OperatorCredPEM: pinPEM,
		OperatorCredAlg: string(trustlist.AlgEd25519),
		StateDir:        t.TempDir(),
		StagingDir:      stagingDir,
		Stdout:          discard{},
		Stderr:          discard{},
	})
	if runErr == nil {
		t.Fatalf("Run accepted a bundle with a tampered install.sh; want a membership failure")
	}
	if _, statErr := os.Stat(filepath.Join(stagingDir, "install.sh")); statErr == nil {
		t.Fatalf("Run staged install.sh despite a membership failure; the gate must run BEFORE stage/apply")
	}
}

// --- inline keystone setup (for negatives that re-sign with the operator's private key) ---

// keystoneInlineSetup drives the keystone flow inline (keeping the signer, which
// keystoneBundle hides) up to a promoted, signed deploy, and returns the env, signer, and
// node-1's bearer token. Negatives that must RE-SIGN a tampered manifest use this instead
// of keystoneBundle.
func keystoneInlineSetup(t *testing.T) (*ctlEnv, keystoneSigner, string) {
	t.Helper()
	env := newCtlEnv(t)
	ks := newKeystoneSigner(t)
	env.pinOperatorCredential(t, ks.pinPEM)
	node1Token := env.enrollViaAgent(t, "node-1")
	_ = env.enrollViaAgent(t, "node-2")
	env.stageSmallTopo(t)
	env.signStaged(t, ks)
	if _, status := env.promote(t); status != http.StatusOK {
		t.Fatalf("promote with a valid signed manifest: status %d, want 200", status)
	}
	return env, ks, node1Token
}

// fetchNode1 fetches node-1's promoted bundle via a bearer agent client.
func fetchNode1(t *testing.T, env *ctlEnv, node1Token string) map[string][]byte {
	t.Helper()
	agentClient, err := agent.NewControllerClient(env.agentSrv.URL, node1Token)
	if err != nil {
		t.Fatalf("NewControllerClient: %v", err)
	}
	files, err := agentClient.Fetch("node-1")
	if err != nil {
		t.Fatalf("Fetch(node-1): %v", err)
	}
	return files
}

// resignWithMembers rebuilds the served trust-list to contain ONLY the named members
// (each carrying its ORIGINAL bundle_sha256 from the fetched manifest, so the self-node
// digest binding still passes), re-signs with the operator key, and overlays the new
// trustlist.json/.sig. checksums.sha256 is untouched (trustlist files live outside it).
func resignWithMembers(t *testing.T, ks keystoneSigner, files map[string][]byte, keepNodeIDs ...string) map[string][]byte {
	t.Helper()
	var tl trustlist.TrustList
	if err := json.Unmarshal(files["trustlist.json"], &tl); err != nil {
		t.Fatalf("unmarshal trustlist.json: %v", err)
	}
	keep := make(map[string]bool, len(keepNodeIDs))
	for _, id := range keepNodeIDs {
		keep[id] = true
	}
	var members []trustlist.Member
	for _, m := range tl.Members {
		if keep[m.NodeID] {
			members = append(members, m) // preserves the original BundleSHA256
		}
	}
	tl.Members = members
	canonical, err := trustlist.Canonical(tl)
	if err != nil {
		t.Fatalf("Canonical(re-signed): %v", err)
	}
	signed, err := ks.signer.Sign(tl)
	if err != nil {
		t.Fatalf("Sign(re-signed): %v", err)
	}
	sigJSON, err := json.Marshal(signed)
	if err != nil {
		t.Fatalf("marshal re-signed: %v", err)
	}
	return overlayFiles(files, map[string][]byte{
		"trustlist.json": canonical,
		"trustlist.sig":  sigJSON,
	})
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

// keystoneChecksumExcluded names the bundle files that are NOT part of the
// checksums.sha256 integrity set. It MATCHES the controller export exactly: manifest.json
// (compile-time timestamps), bundle.sig + signing-pubkey.pem (the authenticity layer over
// the checksums), README.txt, checksums.sha256 itself, AND — under the corrected keystone
// design — trustlist.json + trustlist.sig (they bind the checksums digest, so they cannot
// live inside it). A test that rebuilds checksums must use exactly this exclusion set or it
// would diverge from the bytes the controller actually produces.
var keystoneChecksumExcluded = map[string]bool{
	"manifest.json":      true,
	"bundle.sig":         true,
	"signing-pubkey.pem": true,
	"README.txt":         true,
	"checksums.sha256":   true,
	"trustlist.json":     true,
	"trustlist.sig":      true,
}

// overlayFiles returns a deep copy of base with extra files overlaid, WITHOUT touching
// checksums.sha256. Use it to swap a file that lives OUTSIDE the integrity set
// (trustlist.json/.sig) so the existing checksums stay self-consistent.
func overlayFiles(base, extra map[string][]byte) map[string][]byte {
	out := make(map[string][]byte, len(base)+len(extra))
	for k, v := range base {
		out[k] = append([]byte(nil), v...)
	}
	for k, v := range extra {
		out[k] = append([]byte(nil), v...)
	}
	return out
}

// augmentIntegrity returns a copy of base with extra files overlaid, then REBUILDS
// checksums.sha256 over the integrity-covered set (everything except
// keystoneChecksumExcluded). It lets a test mutate a checksummed file (e.g. install.sh)
// and still produce a bundle whose tier-1 checksums are self-consistent — exactly what a
// breached host can do — so VerifyMembership's bundle-digest binding (not a tier-1
// checksum mismatch) is what rejects it.
func augmentIntegrity(base, extra map[string][]byte) map[string][]byte {
	out := overlayFiles(base, extra)
	covered := make(map[string]string)
	for k, v := range out {
		if keystoneChecksumExcluded[k] {
			continue
		}
		covered[k] = string(v)
	}
	out["checksums.sha256"] = bundlesig.Canonicalize(covered)
	return out
}

// dropFile returns a copy of files without the named entry, then rebuilds the integrity
// layer so the only thing missing is the dropped file (not a checksum mismatch). It is for
// dropping a file OUTSIDE the integrity set (trustlist.json/.sig); the rebuild simply
// re-emits the same checksums minus nothing.
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

// dropFileRaw returns a copy of files without the named entry and does NOT rebuild
// checksums. Use it to drop checksums.sha256 itself (rebuilding would re-create it).
func dropFileRaw(files map[string][]byte, name string) map[string][]byte {
	out := make(map[string][]byte, len(files))
	for k, v := range files {
		if k == name {
			continue
		}
		out[k] = append([]byte(nil), v...)
	}
	return out
}

// --- assertions about the manifest shape (match the controller EXACTLY) ---

// TestKeystone_ManifestShape pins the exact wire shape the agent and controller share, so
// a drift in either partition fails loudly here. The served trustlist.json must be the
// canonical TrustList with per-member bundle_sha256 = hex(sha256(checksums.sha256)), and
// the agent's own digest computation must reproduce it.
func TestKeystone_ManifestShape(t *testing.T) {
	files, _, _ := keystoneBundle(t)

	var tl trustlist.TrustList
	if err := json.Unmarshal(files["trustlist.json"], &tl); err != nil {
		t.Fatalf("unmarshal trustlist.json: %v", err)
	}
	if tl.SchemaVersion != 1 {
		t.Fatalf("manifest schema_version = %d, want 1", tl.SchemaVersion)
	}
	if len(tl.Members) == 0 {
		t.Fatalf("manifest has no members")
	}

	// The served trustlist.json must be its own canonical form.
	canonical, err := trustlist.Canonical(tl)
	if err != nil {
		t.Fatalf("Canonical: %v", err)
	}
	if !bytes.Equal(canonical, files["trustlist.json"]) {
		t.Fatalf("served trustlist.json is not its own canonical form")
	}

	// node-1's member.bundle_sha256 must equal hex(sha256(this bundle's checksums.sha256)) —
	// the EXACT computation the agent's VerifyMembership performs.
	sum := sha256.Sum256(files["checksums.sha256"])
	want := hex.EncodeToString(sum[:])
	var node1 *trustlist.Member
	for i := range tl.Members {
		if tl.Members[i].NodeID == "node-1" {
			node1 = &tl.Members[i]
		}
	}
	if node1 == nil {
		t.Fatalf("manifest has no member for node-1")
	}
	if node1.BundleSHA256 != want {
		t.Fatalf("node-1 member.bundle_sha256 = %q, want %q (hex(sha256(checksums.sha256)))", node1.BundleSHA256, want)
	}

	// trustlist.json / trustlist.sig must NOT appear in checksums.sha256 (they bind it).
	listed, err := parseChecksumsForTest(files["checksums.sha256"])
	if err != nil {
		t.Fatalf("parse checksums.sha256: %v", err)
	}
	if _, ok := listed["trustlist.json"]; ok {
		t.Fatalf("checksums.sha256 must NOT cover trustlist.json (it binds the digest)")
	}
	if _, ok := listed["trustlist.sig"]; ok {
		t.Fatalf("checksums.sha256 must NOT cover trustlist.sig (it binds the digest)")
	}
}

// parseChecksumsForTest parses the "<64-hex>  <path>" lines of checksums.sha256 into a
// path set, mirroring the agent's parser shape without depending on the unexported one.
func parseChecksumsForTest(data []byte) (map[string]bool, error) {
	out := make(map[string]bool)
	for _, line := range bytes.Split(data, []byte("\n")) {
		if len(line) == 0 {
			continue
		}
		idx := bytes.Index(line, []byte("  "))
		if idx < 0 {
			return nil, errMalformedChecksums
		}
		out[string(line[idx+2:])] = true
	}
	return out, nil
}

// errMalformedChecksums is returned by parseChecksumsForTest on a line without the
// two-space separator.
var errMalformedChecksums = errStringChecksums("malformed checksums line")

type errStringChecksums string

func (e errStringChecksums) Error() string { return string(e) }
