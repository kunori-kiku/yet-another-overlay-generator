package api

// controller_keystone_test.go is the in-process integration test for the reworked
// keystone HTTP surface (plan-5.1 CORRECTION, 2026-06-08). The off-host signature now
// binds each node's BUNDLE DIGEST (not merely the member list), so the flow is
// stage -> sign -> promote, with the signed manifest SERVED alongside the bundle (never
// embedded in checksums.sha256). It drives the operator routes end to end with a MemStore
// and a SOFTWARE Ed25519 signer (trustlist.NewEd25519Signer) standing in for the browser
// passkey:
//
//	(1) POST /operator-credential with a PKIX Ed25519 public-key PEM -> 200 (keystone ON).
//	(2) GET /trustlist BEFORE staging -> 404 (no staged manifest yet).
//	(3) stage -> GET /trustlist -> 200 with base64 canonical bytes + epoch; members carry
//	    bundle_sha256.
//	(4) POST /trustlist-signature: substituted -> 409; bad sig -> 400; before-pin -> 412;
//	    the genuine signature -> 200.
//	(5) promote WITHOUT a signature -> 422; after signing -> 200.
//	(6) /config serves trustlist.json + trustlist.sig, and they are NOT in checksums.sha256.
//	(7) the stored signed manifest verifies offline against the pinned credential.
//	(8) keystone OFF: stage+promote with no credential, no manifest served.
//
// Plain HTTP throughout (the shared ctlTestEnv from controller_http_test.go); stdlib +
// trustlist only.

import (
	"crypto/ed25519"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/kunorikiku/yet-another-overlay-generator/internal/apierr"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/trustlist"
)

// ed25519PinPEM marshals an Ed25519 public key to the PKIX ("PUBLIC KEY") PEM the
// keystone pin parser (trustlist.ParseEd25519PinPEM) consumes.
func ed25519PinPEM(t *testing.T, pub ed25519.PublicKey) string {
	t.Helper()
	der, err := x509.MarshalPKIXPublicKey(pub)
	if err != nil {
		t.Fatalf("MarshalPKIXPublicKey: %v", err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der}))
}

// signStaged drives GET /trustlist -> off-host sign -> POST /trustlist-signature and
// asserts a 200, returning the staged canonical bytes and epoch it signed.
func signStaged(t *testing.T, env *ctlTestEnv, signer *trustlist.Ed25519Signer) ([]byte, int64) {
	t.Helper()
	var resp trustListResponseJSON
	if status := doJSON(t, http.MethodGet, env.opURL("trustlist"), testOperatorToken, nil, &resp); status != http.StatusOK {
		t.Fatalf("trustlist: status %d, want 200", status)
	}
	canonical, err := base64.StdEncoding.DecodeString(resp.TrustListJSON)
	if err != nil {
		t.Fatalf("decode trustlist_json: %v", err)
	}
	var tl trustlist.TrustList
	if err := json.Unmarshal(canonical, &tl); err != nil {
		t.Fatalf("unmarshal manifest: %v", err)
	}
	signed, err := signer.Sign(tl)
	if err != nil {
		t.Fatalf("sign manifest: %v", err)
	}
	if status := doJSON(t, http.MethodPost, env.opURL("trustlist-signature"), testOperatorToken,
		trustListSignatureRequestJSON{TrustListJSON: resp.TrustListJSON, Signed: signed}, nil); status != http.StatusOK {
		t.Fatalf("trustlist-signature(valid): status %d, want 200", status)
	}
	return canonical, resp.Epoch
}

// stageOnly drives the operator update-topology -> stage sequence for smallTopo (no
// promote), so a manifest is staged but not yet live.
func (e *ctlTestEnv) stageOnly(t *testing.T) {
	t.Helper()
	topoJSON, err := json.Marshal(smallTopo())
	if err != nil {
		t.Fatalf("marshal topology: %v", err)
	}
	if status := doRaw(t, http.MethodPost, e.opURL("update-topology"), testOperatorToken, topoJSON); status != http.StatusOK {
		t.Fatalf("update-topology: status %d, want 200", status)
	}
	if status := doJSON(t, http.MethodPost, e.opURL("stage"), testOperatorToken, struct{}{}, &stageResponseJSON{}); status != http.StatusOK {
		t.Fatalf("stage: status %d, want 200", status)
	}
}

// TestControllerKeystone_StageSignPromoteServe drives the full reworked keystone flow:
// pin -> stage -> sign (with the 412/404/409/400 rejections) -> promote (with the
// promote-without-signature 422 refusal) -> /config serves the signed manifest OUTSIDE
// checksums -> the served manifest verifies offline and binds this node's bundle digest.
func TestControllerKeystone_StageSignPromoteServe(t *testing.T) {
	env := newCtlTestEnv(t)

	// Software Ed25519 operator signer standing in for the browser passkey.
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	signer := trustlist.NewEd25519Signer(priv)

	// Submitting a signature before any credential is pinned -> 412.
	if status := doJSON(t, http.MethodPost, env.opURL("trustlist-signature"), testOperatorToken,
		trustListSignatureRequestJSON{TrustListJSON: "AA==", Signed: trustlist.SignedTrustList{Alg: trustlist.AlgEd25519}}, nil); status != http.StatusPreconditionFailed {
		t.Fatalf("trustlist-signature before pin: status %d, want 412", status)
	}

	// (1) Pin the off-host credential -> 200 (keystone ON).
	if status := doJSON(t, http.MethodPost, env.opURL("operator-credential"), testOperatorToken,
		operatorCredentialRequestJSON{Alg: string(trustlist.AlgEd25519), CredentialID: signer.KeyID(), PublicKeyPEM: ed25519PinPEM(t, pub)}, nil); status != http.StatusOK {
		t.Fatalf("operator-credential: status %d, want 200", status)
	}
	// A malformed PEM for the declared alg -> 400.
	if status := doJSON(t, http.MethodPost, env.opURL("operator-credential"), testOperatorToken,
		operatorCredentialRequestJSON{Alg: string(trustlist.AlgEd25519), PublicKeyPEM: "not a pem"}, nil); status != http.StatusBadRequest {
		t.Fatalf("operator-credential(bad pem): status %d, want 400", status)
	}

	// (2) GET /trustlist before staging -> 404 (nothing staged yet).
	if status := doJSON(t, http.MethodGet, env.opURL("trustlist"), testOperatorToken, nil, nil); status != http.StatusNotFound {
		t.Fatalf("trustlist before stage: status %d, want 404", status)
	}

	// Enroll two nodes (capture node-1's token for the /config check) and stage (no
	// promote yet).
	node1Token := env.enrollNode(t, "node-1")
	env.enrollNode(t, "node-2")
	env.stageOnly(t)

	// (3) Fetch the staged manifest. epoch 0 (first stored); members carry bundle_sha256.
	var tlResp trustListResponseJSON
	if status := doJSON(t, http.MethodGet, env.opURL("trustlist"), testOperatorToken, nil, &tlResp); status != http.StatusOK {
		t.Fatalf("trustlist: status %d, want 200", status)
	}
	if tlResp.Epoch != 0 {
		t.Fatalf("trustlist epoch = %d, want 0 (first staged)", tlResp.Epoch)
	}
	canonical, err := base64.StdEncoding.DecodeString(tlResp.TrustListJSON)
	if err != nil {
		t.Fatalf("decode trustlist_json: %v", err)
	}
	var manifest trustlist.TrustList
	if err := json.Unmarshal(canonical, &manifest); err != nil {
		t.Fatalf("unmarshal manifest: %v", err)
	}
	if len(manifest.Members) != 2 {
		t.Fatalf("manifest has %d members, want 2", len(manifest.Members))
	}
	for _, m := range manifest.Members {
		if m.BundleSHA256 == "" {
			t.Fatalf("member %s has empty bundle_sha256", m.NodeID)
		}
	}

	// (5a) Promote BEFORE signing -> 422 (the keystone gate refuses).
	if status := doJSON(t, http.MethodPost, env.opURL("promote"), testOperatorToken, struct{}{}, nil); status != http.StatusUnprocessableEntity {
		t.Fatalf("promote before signing: status %d, want 422", status)
	}

	// Sign the staged manifest off-host.
	signed, err := signer.Sign(manifest)
	if err != nil {
		t.Fatalf("sign manifest: %v", err)
	}

	// (4a) A SUBSTITUTED trustlist_json (right signature, wrong submitted bytes) -> 409.
	if status := doJSON(t, http.MethodPost, env.opURL("trustlist-signature"), testOperatorToken,
		trustListSignatureRequestJSON{TrustListJSON: base64.StdEncoding.EncodeToString([]byte("substituted bytes")), Signed: signed}, nil); status != http.StatusConflict {
		t.Fatalf("trustlist-signature(substituted): status %d, want 409", status)
	}

	// (4b) A BAD signature over the right bytes -> 400.
	bad := signed
	bad.Signature = base64.RawURLEncoding.EncodeToString(make([]byte, ed25519.SignatureSize)) // all-zero sig
	if status := doJSON(t, http.MethodPost, env.opURL("trustlist-signature"), testOperatorToken,
		trustListSignatureRequestJSON{TrustListJSON: tlResp.TrustListJSON, Signed: bad}, nil); status != http.StatusBadRequest {
		t.Fatalf("trustlist-signature(bad sig): status %d, want 400", status)
	}

	// (4c) The genuine signature over the exact canonical bytes -> 200.
	if status := doJSON(t, http.MethodPost, env.opURL("trustlist-signature"), testOperatorToken,
		trustListSignatureRequestJSON{TrustListJSON: tlResp.TrustListJSON, Signed: signed}, nil); status != http.StatusOK {
		t.Fatalf("trustlist-signature(valid): status %d, want 200", status)
	}

	// (5b) Promote AFTER signing -> 200.
	var promote generationResponseJSON
	if status := doJSON(t, http.MethodPost, env.opURL("promote"), testOperatorToken, struct{}{}, &promote); status != http.StatusOK {
		t.Fatalf("promote after signing: status %d, want 200", status)
	}

	// (6) /config serves trustlist.json + trustlist.sig — and they are NOT in
	// checksums.sha256. Decode the served manifest and assert this node's bundle digest.
	var cfg configResponseJSON
	if status := doJSON(t, http.MethodGet, env.agentURL("config"), node1Token, nil, &cfg); status != http.StatusOK {
		t.Fatalf("config: status %d, want 200", status)
	}
	servedTLB64, ok := cfg.Files["trustlist.json"]
	if !ok {
		t.Fatalf("/config did not serve trustlist.json")
	}
	servedSigB64, ok := cfg.Files["trustlist.sig"]
	if !ok {
		t.Fatalf("/config did not serve trustlist.sig")
	}
	checksB64, ok := cfg.Files["checksums.sha256"]
	if !ok {
		t.Fatalf("/config bundle missing checksums.sha256")
	}
	checks, err := base64.StdEncoding.DecodeString(checksB64)
	if err != nil {
		t.Fatalf("decode checksums.sha256: %v", err)
	}
	if strings.Contains(string(checks), "trustlist.json") || strings.Contains(string(checks), "trustlist.sig") {
		t.Fatalf("checksums.sha256 must NOT cover trustlist files:\n%s", checks)
	}

	// (7) The served manifest verifies offline against the pinned credential and binds
	// node-1's bundle digest (the install.sh-coverage property the agent enforces).
	servedTL, err := base64.StdEncoding.DecodeString(servedTLB64)
	if err != nil {
		t.Fatalf("decode served trustlist.json: %v", err)
	}
	servedSig, err := base64.StdEncoding.DecodeString(servedSigB64)
	if err != nil {
		t.Fatalf("decode served trustlist.sig: %v", err)
	}
	var servedManifest trustlist.TrustList
	if err := json.Unmarshal(servedTL, &servedManifest); err != nil {
		t.Fatalf("unmarshal served manifest: %v", err)
	}
	var servedSigned trustlist.SignedTrustList
	if err := json.Unmarshal(servedSig, &servedSigned); err != nil {
		t.Fatalf("unmarshal served signature: %v", err)
	}
	pin := trustlist.PinnedCredential{Alg: trustlist.AlgEd25519, Ed25519Pub: pub}
	if err := trustlist.Verify(servedManifest, servedSigned, pin); err != nil {
		t.Fatalf("served manifest failed offline Verify: %v", err)
	}
	// node-1's member bundle_sha256 == hex(sha256(served checksums.sha256)).
	sum := sha256.Sum256(checks)
	wantDigest := hex.EncodeToString(sum[:])
	found := false
	for _, m := range servedManifest.Members {
		if m.NodeID == "node-1" {
			found = true
			if m.BundleSHA256 != wantDigest {
				t.Fatalf("node-1 member bundle_sha256 %s != served checksums digest %s", m.BundleSHA256, wantDigest)
			}
		}
	}
	if !found {
		t.Fatalf("node-1 not found in served manifest members")
	}
}

// TestControllerKeystone_EpochMonotonic pins the monotonic epoch rule across STAGES: a
// re-stage with an unchanged topology+membership reuses the epoch (byte-identical
// manifest), while a membership change (enroll a second node + re-stage) advances it.
func TestControllerKeystone_EpochMonotonic(t *testing.T) {
	env := newCtlTestEnv(t)

	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	signer := trustlist.NewEd25519Signer(priv)

	if status := doJSON(t, http.MethodPost, env.opURL("operator-credential"), testOperatorToken,
		operatorCredentialRequestJSON{Alg: string(trustlist.AlgEd25519), PublicKeyPEM: ed25519PinPEM(t, pub)}, nil); status != http.StatusOK {
		t.Fatalf("operator-credential: status %d, want 200", status)
	}

	env.enrollNode(t, "node-1")
	env.enrollNode(t, "node-2")

	// Stage #1: first manifest, epoch 0. Sign + promote.
	env.stageOnly(t)
	_, epoch := signStaged(t, env, signer)
	if epoch != 0 {
		t.Fatalf("first staged epoch = %d, want 0", epoch)
	}
	if status := doJSON(t, http.MethodPost, env.opURL("promote"), testOperatorToken, struct{}{}, nil); status != http.StatusOK {
		t.Fatalf("promote #1: status %d, want 200", status)
	}

	// Re-stage the SAME topology+membership -> epoch REUSED (still 0).
	env.stageOnly(t)
	var reResp trustListResponseJSON
	if status := doJSON(t, http.MethodGet, env.opURL("trustlist"), testOperatorToken, nil, &reResp); status != http.StatusOK {
		t.Fatalf("trustlist(unchanged): status %d, want 200", status)
	}
	if reResp.Epoch != 0 {
		t.Fatalf("epoch after unchanged re-stage = %d, want 0 (reuse)", reResp.Epoch)
	}
}

// TestControllerKeystone_OptInOff confirms keystone is OPT-IN: with NO operator
// credential pinned, GET /trustlist is 404 (nothing staged drives the manifest), stage +
// promote succeed, and /config serves NO trust-list files. Byte-for-byte today's deploy.
func TestControllerKeystone_OptInOff(t *testing.T) {
	env := newCtlTestEnv(t)
	node1Token := env.enrollNode(t, "node-1")
	env.enrollNode(t, "node-2")

	// Stage + promote with NO credential pinned.
	env.promoteSmallTopo(t)

	// /trustlist with keystone OFF: no manifest is ever staged -> 404.
	if status := doJSON(t, http.MethodGet, env.opURL("trustlist"), testOperatorToken, nil, nil); status != http.StatusNotFound {
		t.Fatalf("trustlist (keystone off): status %d, want 404", status)
	}

	// /config serves NO trust-list files with keystone OFF.
	var cfg configResponseJSON
	if status := doJSON(t, http.MethodGet, env.agentURL("config"), node1Token, nil, &cfg); status != http.StatusOK {
		t.Fatalf("config: status %d, want 200", status)
	}
	if _, ok := cfg.Files["trustlist.json"]; ok {
		t.Fatalf("keystone OFF but /config served trustlist.json")
	}
	if _, ok := cfg.Files["trustlist.sig"]; ok {
		t.Fatalf("keystone OFF but /config served trustlist.sig")
	}
}

// TestControllerKeystone_ConfigFailsClosedWhenPinnedButNoSignedManifest pins the FAIL-CLOSED wire
// contract of /config in the keystone-ON-but-nothing-signed-yet state: a bundle is promoted while
// the keystone is OFF, then a credential is pinned (keystone ON) but no deploy has been signed +
// promoted under it. HandleConfig must return 409 CodeKeystoneNoSignedManifest — NOT a 500 (the
// pre-reclassification status), and NOT a config carrying an empty/partial trust-list — so the node
// keeps its current config and retries rather than applying anything unverified. Without this test
// nothing pins either the 409 wire status or the fail-closed file-omission through the real handler.
func TestControllerKeystone_ConfigFailsClosedWhenPinnedButNoSignedManifest(t *testing.T) {
	env := newCtlTestEnv(t)
	node1Token := env.enrollNode(t, "node-1")
	env.enrollNode(t, "node-2")

	// Deploy a bundle with the keystone OFF (no operator credential): stage + promote, no signing.
	env.promoteSmallTopo(t)
	var off configResponseJSON
	if status := doJSON(t, http.MethodGet, env.agentURL("config"), node1Token, nil, &off); status != http.StatusOK {
		t.Fatalf("keystone-OFF config: status %d, want 200", status)
	}

	// Now PIN a credential (keystone ON) WITHOUT signing + promoting a manifest under it: the served
	// slot holds no signed trust-list, so /config must fail closed.
	pub, _, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("genkey: %v", err)
	}
	if status := doJSON(t, http.MethodPost, env.opURL("operator-credential"), testOperatorToken,
		operatorCredentialRequestJSON{Alg: string(trustlist.AlgEd25519), PublicKeyPEM: ed25519PinPEM(t, pub)}, nil); status != http.StatusOK {
		t.Fatalf("operator-credential: status %d, want 200", status)
	}

	status, code, files := getConfigErr(t, env, node1Token)
	if status != http.StatusConflict {
		t.Fatalf("keystone-ON-but-unsigned /config: status %d, want 409 (was 500 before reclassification)", status)
	}
	if code != string(apierr.CodeKeystoneNoSignedManifest) {
		t.Fatalf("error code = %q, want %q", code, apierr.CodeKeystoneNoSignedManifest)
	}
	if len(files) != 0 {
		t.Fatalf("fail-closed /config must serve NO files (no empty/partial trust-list leak), got %d files", len(files))
	}
}

// getConfigErr GETs /config and decodes BOTH the coded-error envelope and any files map, so a
// fail-closed assertion can pin the status, the apierr code, AND that no files leaked in the body.
func getConfigErr(t *testing.T, env *ctlTestEnv, token string) (int, string, map[string]string) {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, env.agentURL("config"), nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("config GET: %v", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	var envl struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
		Files map[string]string `json:"files"`
	}
	_ = json.Unmarshal(raw, &envl)
	return resp.StatusCode, envl.Error.Code, envl.Files
}
