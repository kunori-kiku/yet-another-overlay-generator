package api

// controller_keystone_test.go is the in-process integration test for the keystone
// HTTP surface (plan-5.1b): the operator pins an OFF-HOST signing credential, fetches
// the canonical membership trust-list bytes, signs them off-host, and submits the
// signature. It exercises the three keystone operator routes end to end with a MemStore
// and a SOFTWARE Ed25519 signer (trustlist.NewEd25519Signer) standing in for the
// browser passkey:
//
//	(1) POST /operator-credential with a PKIX Ed25519 public-key PEM -> 200 (keystone ON).
//	(2) GET /trustlist after enrolling 2 nodes -> 200 with base64 canonical bytes + epoch.
//	(3) sign those exact bytes off-host; POST /trustlist-signature -> 200.
//	(4) a SUBSTITUTED trustlist_json -> 409 (substitution guard).
//	(5) a BAD signature over the right bytes -> 400 (verification failure).
//	(6) the stored, signed trust-list verifies offline against the pinned credential.
//	(7) /trustlist-signature before any credential is pinned -> 412.
//	(8) the monotonic epoch rule: re-fetch with unchanged membership reuses the epoch;
//	    a membership change advances it.
//
// Plain HTTP throughout (the shared ctlTestEnv from controller_http_test.go); stdlib +
// trustlist only.

import (
	"context"
	"crypto/ed25519"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"testing"

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

// TestControllerKeystone_SignFlow drives the full pin -> fetch -> sign -> submit happy
// path plus the 409 substitution and 400 bad-signature rejections, and confirms the
// stored signed trust-list verifies offline against the pinned Ed25519 credential.
func TestControllerKeystone_SignFlow(t *testing.T) {
	env := newCtlTestEnv(t)

	// Software Ed25519 operator signer standing in for the browser passkey.
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	signer := trustlist.NewEd25519Signer(priv)

	// (7) Submitting a signature before any credential is pinned -> 412.
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

	// Enroll two nodes so the trust-list has members.
	env.enrollNode(t, "node-1")
	env.enrollNode(t, "node-2")

	// (2) Fetch the canonical bytes to sign.
	var tlResp trustListResponseJSON
	if status := doJSON(t, http.MethodGet, env.opURL("trustlist"), testOperatorToken, nil, &tlResp); status != http.StatusOK {
		t.Fatalf("trustlist: status %d, want 200", status)
	}
	canonical, err := base64.StdEncoding.DecodeString(tlResp.TrustListJSON)
	if err != nil {
		t.Fatalf("decode trustlist_json: %v", err)
	}
	if len(canonical) == 0 {
		t.Fatalf("trustlist: empty canonical bytes")
	}
	// First signed trust-list with no prior stored -> epoch 0.
	if tlResp.Epoch != 0 {
		t.Fatalf("trustlist epoch = %d, want 0 (no prior stored)", tlResp.Epoch)
	}

	// Parse the canonical bytes back into a TrustList so the signer signs the same logical
	// document the controller built (the signer re-canonicalizes internally, so signing
	// the parsed TL produces a signature over the identical canonical bytes).
	var tl trustlist.TrustList
	if err := json.Unmarshal(canonical, &tl); err != nil {
		t.Fatalf("unmarshal canonical trust-list: %v", err)
	}
	signed, err := signer.Sign(tl)
	if err != nil {
		t.Fatalf("sign trust-list: %v", err)
	}

	// (4) A SUBSTITUTED trustlist_json (right signature, wrong submitted bytes) -> 409.
	if status := doJSON(t, http.MethodPost, env.opURL("trustlist-signature"), testOperatorToken,
		trustListSignatureRequestJSON{TrustListJSON: base64.StdEncoding.EncodeToString([]byte("substituted bytes")), Signed: signed}, nil); status != http.StatusConflict {
		t.Fatalf("trustlist-signature(substituted): status %d, want 409", status)
	}

	// (5) A BAD signature over the right bytes -> 400.
	bad := signed
	bad.Signature = base64.RawURLEncoding.EncodeToString(make([]byte, ed25519.SignatureSize)) // all-zero sig
	if status := doJSON(t, http.MethodPost, env.opURL("trustlist-signature"), testOperatorToken,
		trustListSignatureRequestJSON{TrustListJSON: tlResp.TrustListJSON, Signed: bad}, nil); status != http.StatusBadRequest {
		t.Fatalf("trustlist-signature(bad sig): status %d, want 400", status)
	}

	// (3) The genuine signature over the exact canonical bytes -> 200.
	if status := doJSON(t, http.MethodPost, env.opURL("trustlist-signature"), testOperatorToken,
		trustListSignatureRequestJSON{TrustListJSON: tlResp.TrustListJSON, Signed: signed}, nil); status != http.StatusOK {
		t.Fatalf("trustlist-signature(valid): status %d, want 200", status)
	}

	// (6) The stored signed trust-list verifies OFFLINE against the pinned credential —
	// exactly what a node does. Pull it from the store and re-verify.
	stored, err := env.store.GetCurrentSignedTrustList(context.Background(), testTenant)
	if err != nil {
		t.Fatalf("GetCurrentSignedTrustList: %v", err)
	}
	if !equalBytes(stored.TrustListJSON, canonical) {
		t.Fatalf("stored TrustListJSON != canonical bytes the operator signed")
	}
	var storedTL trustlist.TrustList
	if err := json.Unmarshal(stored.TrustListJSON, &storedTL); err != nil {
		t.Fatalf("unmarshal stored trust-list: %v", err)
	}
	var storedSig trustlist.SignedTrustList
	if err := json.Unmarshal(stored.SignatureJSON, &storedSig); err != nil {
		t.Fatalf("unmarshal stored signature: %v", err)
	}
	pin := trustlist.PinnedCredential{Alg: trustlist.AlgEd25519, Ed25519Pub: pub}
	if err := trustlist.Verify(storedTL, storedSig, pin); err != nil {
		t.Fatalf("offline Verify of stored trust-list failed: %v", err)
	}
}

// TestControllerKeystone_EpochMonotonic pins the monotonic epoch rule: a re-fetch with
// an UNCHANGED approved membership reuses the stored epoch, while a membership change
// advances it by one. This is what makes the agent's anti-rollback (epoch >= last
// applied) admit a new list yet reject a stale one.
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

	// First fetch + sign: epoch 0 (no prior stored).
	signAt := func(wantEpoch int64) {
		t.Helper()
		var resp trustListResponseJSON
		if status := doJSON(t, http.MethodGet, env.opURL("trustlist"), testOperatorToken, nil, &resp); status != http.StatusOK {
			t.Fatalf("trustlist: status %d, want 200", status)
		}
		if resp.Epoch != wantEpoch {
			t.Fatalf("trustlist epoch = %d, want %d", resp.Epoch, wantEpoch)
		}
		canonical, err := base64.StdEncoding.DecodeString(resp.TrustListJSON)
		if err != nil {
			t.Fatalf("decode trustlist_json: %v", err)
		}
		var tl trustlist.TrustList
		if err := json.Unmarshal(canonical, &tl); err != nil {
			t.Fatalf("unmarshal trust-list: %v", err)
		}
		signed, err := signer.Sign(tl)
		if err != nil {
			t.Fatalf("sign: %v", err)
		}
		if status := doJSON(t, http.MethodPost, env.opURL("trustlist-signature"), testOperatorToken,
			trustListSignatureRequestJSON{TrustListJSON: resp.TrustListJSON, Signed: signed}, nil); status != http.StatusOK {
			t.Fatalf("trustlist-signature: status %d, want 200 (epoch %d)", status, wantEpoch)
		}
	}

	signAt(0) // first sign, one member, epoch 0

	// Re-fetch with UNCHANGED membership -> epoch is REUSED (still 0).
	var reResp trustListResponseJSON
	if status := doJSON(t, http.MethodGet, env.opURL("trustlist"), testOperatorToken, nil, &reResp); status != http.StatusOK {
		t.Fatalf("trustlist(unchanged): status %d, want 200", status)
	}
	if reResp.Epoch != 0 {
		t.Fatalf("trustlist epoch after unchanged membership = %d, want 0 (reuse)", reResp.Epoch)
	}

	// Membership CHANGES: enroll a second node. The next fetch must advance the epoch.
	env.enrollNode(t, "node-2")
	var bumpResp trustListResponseJSON
	if status := doJSON(t, http.MethodGet, env.opURL("trustlist"), testOperatorToken, nil, &bumpResp); status != http.StatusOK {
		t.Fatalf("trustlist(changed): status %d, want 200", status)
	}
	if bumpResp.Epoch != 1 {
		t.Fatalf("trustlist epoch after membership change = %d, want 1 (advance)", bumpResp.Epoch)
	}
	signAt(1) // sign the new membership at epoch 1
}

// TestControllerKeystone_OptInOff confirms keystone is OPT-IN: with NO operator
// credential pinned, GET /trustlist still builds (epoch 0) and CompileAndStage embeds
// NO trustlist files — the deploy path is byte-for-byte today's behavior. This is the
// backward-compat guard.
func TestControllerKeystone_OptInOff(t *testing.T) {
	env := newCtlTestEnv(t)
	env.enrollNode(t, "node-1")
	env.enrollNode(t, "node-2")

	// /trustlist works even with keystone OFF (it is a pure projection of membership).
	var resp trustListResponseJSON
	if status := doJSON(t, http.MethodGet, env.opURL("trustlist"), testOperatorToken, nil, &resp); status != http.StatusOK {
		t.Fatalf("trustlist (keystone off): status %d, want 200", status)
	}
	if resp.Epoch != 0 {
		t.Fatalf("trustlist epoch (keystone off) = %d, want 0", resp.Epoch)
	}

	// Stage + promote with NO credential pinned: no error, and no trustlist file in the
	// bundle (keystone OFF -> today's behavior).
	env.promoteSmallTopo(t)
	bundle, err := env.store.GetCurrentBundle(context.Background(), testTenant, "node-1")
	if err != nil {
		t.Fatalf("GetCurrentBundle: %v", err)
	}
	if _, ok := bundle.Files["trustlist.json"]; ok {
		t.Fatalf("keystone OFF but bundle carries trustlist.json (must be absent)")
	}
	if _, ok := bundle.Files["trustlist.sig"]; ok {
		t.Fatalf("keystone OFF but bundle carries trustlist.sig (must be absent)")
	}
}

// equalBytes is a tiny byte-slice comparator kept local to avoid pulling bytes into the
// test's top-level imports for a single use.
func equalBytes(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
