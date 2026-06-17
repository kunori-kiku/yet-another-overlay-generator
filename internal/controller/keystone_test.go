package controller

// keystone_test.go unit-tests the keystone-rotation helpers in compile.go:
// KeystoneFingerprint (canonical, re-encode-stable), SameKeystoneCredential (rotation
// detection, WebAuthn binding sensitivity), and KeystoneRedeployRequired (the
// rotated-but-not-redeployed operator signal).

import (
	"context"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"testing"

	"github.com/kunorikiku/yet-another-overlay-generator/internal/bundlesig"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/trustlist"
)

func ed25519Cred(t *testing.T, pub ed25519.PublicKey) OperatorCredential {
	t.Helper()
	return OperatorCredential{Alg: string(trustlist.AlgEd25519), PublicKeyPEM: string(bundlesig.MarshalPublicKeyPEM(pub))}
}

func es256CredPEM(t *testing.T) (string, *ecdsa.PublicKey) {
	t.Helper()
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("ecdsa gen: %v", err)
	}
	der, err := x509.MarshalPKIXPublicKey(&priv.PublicKey)
	if err != nil {
		t.Fatalf("marshal pkix: %v", err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der})), &priv.PublicKey
}

// promoteServedTrustList drives the store's real stage->promote flow so the SERVED trust-list slot
// (what KeystoneRedeployRequired and /config read after the served-slot split) ends up holding sl.
// PromoteStaged copies only a staged manifest carrying a NON-EMPTY signature into the served slot,
// so sl must include one. It stages one throwaway bundle at the fresh tenant's first generation (1)
// so PromoteStaged has something to flip; callers promote at most once.
func promoteServedTrustList(t *testing.T, s Store, tnt TenantID, sl StoredTrustList) {
	t.Helper()
	ctx := context.Background()
	if err := s.StageBundle(ctx, tnt, SignedBundle{NodeID: "n1", Generation: 1, Files: map[string][]byte{"checksums.sha256": []byte("x")}}); err != nil {
		t.Fatalf("stage bundle: %v", err)
	}
	if err := s.PutSignedTrustList(ctx, tnt, sl); err != nil {
		t.Fatalf("put signed trust-list: %v", err)
	}
	if _, err := s.PromoteStaged(ctx, tnt); err != nil {
		t.Fatalf("promote: %v", err)
	}
}

// TestKeystoneFingerprint_StableAcrossReencode: the fingerprint is over the parsed canonical DER,
// so a re-encoded PEM (extra whitespace, a leading comment pem.Decode skips) of the SAME key
// yields the SAME fingerprint — and two DIFFERENT keys yield different fingerprints.
func TestKeystoneFingerprint_StableAcrossReencode(t *testing.T) {
	pub, _, _ := ed25519.GenerateKey(rand.Reader)
	c := ed25519Cred(t, pub)
	fp1, err := KeystoneFingerprint(c)
	if err != nil || fp1 == "" {
		t.Fatalf("fingerprint: %v / %q", err, fp1)
	}
	// Re-encode: a leading comment + trailing blank lines; pem.Decode tolerates both.
	reenc := c
	reenc.PublicKeyPEM = "# operator keystone (public)\n" + c.PublicKeyPEM + "\n\n"
	fp2, err := KeystoneFingerprint(reenc)
	if err != nil {
		t.Fatalf("fingerprint reenc: %v", err)
	}
	if fp1 != fp2 {
		t.Fatalf("re-encode of the same key changed the fingerprint: %s != %s", fp1, fp2)
	}
	// A different key -> different fingerprint.
	pub2, _, _ := ed25519.GenerateKey(rand.Reader)
	fpOther, _ := KeystoneFingerprint(ed25519Cred(t, pub2))
	if fpOther == fp1 {
		t.Fatal("distinct keys collided on the fingerprint")
	}

	// ES256 fingerprints too (different alg path), and parse errors surface.
	pemES, _ := es256CredPEM(t)
	if _, err := KeystoneFingerprint(OperatorCredential{Alg: string(trustlist.AlgWebAuthnES256), PublicKeyPEM: pemES}); err != nil {
		t.Fatalf("es256 fingerprint: %v", err)
	}
	if _, err := KeystoneFingerprint(OperatorCredential{Alg: string(trustlist.AlgEd25519), PublicKeyPEM: "not a pem"}); err == nil {
		t.Fatal("a malformed PEM must surface a fingerprint error, not a silent value")
	}
}

// TestSameKeystoneCredential covers rotation detection: same key = same; different key = rotation;
// and for WebAuthn algs a changed rpid/credential_id is a rotation (the assertion binds them),
// while raw ed25519 ignores both.
func TestSameKeystoneCredential(t *testing.T) {
	pubA, _, _ := ed25519.GenerateKey(rand.Reader)
	pubB, _, _ := ed25519.GenerateKey(rand.Reader)
	a := ed25519Cred(t, pubA)

	if same, err := SameKeystoneCredential(a, a); err != nil || !same {
		t.Fatalf("identical ed25519 cred must be same: %v / %v", same, err)
	}
	if same, _ := SameKeystoneCredential(a, ed25519Cred(t, pubB)); same {
		t.Fatal("different ed25519 keys must be a rotation")
	}
	// ed25519 ignores rpid/credential_id.
	aR := a
	aR.RPID = "other"
	aR.CredentialID = "other"
	if same, err := SameKeystoneCredential(a, aR); err != nil || !same {
		t.Fatalf("ed25519 must ignore rpid/credential_id: %v / %v", same, err)
	}

	// WebAuthn ES256: same key but a changed rpid, origin, or credential_id is a rotation — all
	// three feed trustlist.Verify (rpIdHash + the fail-closed origin check + allowCredentials).
	pemES, _ := es256CredPEM(t)
	base := OperatorCredential{Alg: string(trustlist.AlgWebAuthnES256), PublicKeyPEM: pemES, RPID: "rp.example", Origin: "https://rp.example", CredentialID: "cred-1"}
	if same, err := SameKeystoneCredential(base, base); err != nil || !same {
		t.Fatalf("identical webauthn cred must be same: %v / %v", same, err)
	}
	diffRP := base
	diffRP.RPID = "evil.example"
	if same, _ := SameKeystoneCredential(base, diffRP); same {
		t.Fatal("webauthn: a changed rpid must be a rotation")
	}
	diffCred := base
	diffCred.CredentialID = "cred-2"
	if same, _ := SameKeystoneCredential(base, diffCred); same {
		t.Fatal("webauthn: a changed credential_id must be a rotation")
	}
	// Origin is load-bearing: trustlist.Verify fail-closes on an origin mismatch and SKIPS the
	// check when the pinned origin is empty, so a changed OR cleared origin is a real rebinding.
	diffOrigin := base
	diffOrigin.Origin = "https://evil.example"
	if same, _ := SameKeystoneCredential(base, diffOrigin); same {
		t.Fatal("webauthn: a changed origin must be a rotation (it changes what Verify accepts)")
	}
	clearedOrigin := base
	clearedOrigin.Origin = ""
	if same, _ := SameKeystoneCredential(base, clearedOrigin); same {
		t.Fatal("webauthn: clearing the origin must be a rotation (it loosens the origin binding)")
	}
	// A different alg is always a rotation.
	if same, _ := SameKeystoneCredential(a, base); same {
		t.Fatal("different algs must be a rotation")
	}
}

// TestKeystoneHelpers_ErrorPaths: an unparsable PEM or unknown alg must SURFACE an error from the
// helpers, never be masked as "same" / "not required" — masking would silently re-enable the
// silent-overwrite this PR removes.
func TestKeystoneHelpers_ErrorPaths(t *testing.T) {
	ctx := context.Background()
	const tnt = TenantID("acme")
	bad := OperatorCredential{Alg: string(trustlist.AlgEd25519), PublicKeyPEM: "not a pem"}
	pubA, privA, _ := ed25519.GenerateKey(rand.Reader)
	good := ed25519Cred(t, pubA)

	if _, err := KeystoneFingerprint(bad); err == nil {
		t.Fatal("KeystoneFingerprint(bad PEM) must error")
	}
	if _, err := KeystoneFingerprint(OperatorCredential{Alg: "bogus-alg", PublicKeyPEM: good.PublicKeyPEM}); err == nil {
		t.Fatal("KeystoneFingerprint(unknown alg) must error")
	}
	// SameKeystoneCredential must not mask an unparsable side as "same".
	if same, err := SameKeystoneCredential(bad, good); err == nil || same {
		t.Fatalf("SameKeystoneCredential(bad, good) = (%v, %v), want (false, err)", same, err)
	}

	// KeystoneRedeployRequired over a CORRUPT SERVED manifest (with a well-formed signature) must
	// surface the parse error, never silently report "not required". The check reads the SERVED
	// slot, so the corrupt record must be PROMOTED, not merely staged.
	s := NewMemStore()
	manifest := trustlist.TrustList{SchemaVersion: 1, Tenant: string(tnt), Epoch: 1, Members: []trustlist.Member{{NodeID: "n1", BundleSHA256: "abc"}}}
	signed, err := trustlist.NewEd25519Signer(privA).Sign(manifest)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	sigJSON, _ := json.Marshal(signed)
	promoteServedTrustList(t, s, tnt, StoredTrustList{TrustListJSON: []byte("{not json"), SignatureJSON: sigJSON, Epoch: 1})
	if _, err := KeystoneRedeployRequired(ctx, s, tnt, good); err == nil {
		t.Fatal("KeystoneRedeployRequired over a corrupt served manifest must surface the parse error")
	}
}

// TestKeystoneRedeployRequired walks the states: no manifest, staged-but-unsigned, signed and
// matching the pin, and signed under a DIFFERENT key than the pin (the rotated-but-not-redeployed
// case that must report true).
func TestKeystoneRedeployRequired(t *testing.T) {
	ctx := context.Background()
	const tnt = TenantID("acme")
	pubA, privA, _ := ed25519.GenerateKey(rand.Reader)
	pubB, privB, _ := ed25519.GenerateKey(rand.Reader)
	credA := ed25519Cred(t, pubA)
	credB := ed25519Cred(t, pubB)

	manifest := trustlist.TrustList{SchemaVersion: 1, Tenant: string(tnt), Epoch: 1, Members: []trustlist.Member{{NodeID: "n1", BundleSHA256: "abc"}}}
	canonical, err := trustlist.Canonical(manifest)
	if err != nil {
		t.Fatalf("canonical: %v", err)
	}

	mustRedeploy := func(t *testing.T, s Store, cred OperatorCredential, want bool) {
		t.Helper()
		got, err := KeystoneRedeployRequired(ctx, s, tnt, cred)
		if err != nil {
			t.Fatalf("KeystoneRedeployRequired: %v", err)
		}
		if got != want {
			t.Fatalf("redeploy_required = %v, want %v", got, want)
		}
	}

	// Nothing PROMOTED yet (served slot empty) -> not required, even with a credential pinned.
	s := NewMemStore()
	mustRedeploy(t, s, credA, false)

	// Staged but UNSIGNED, never promoted: the served slot stays empty, so the redeploy signal
	// reads the served slot and reports false (a deploy is mid-flight, the fleet is not stranded).
	if err := s.PutSignedTrustList(ctx, tnt, StoredTrustList{TrustListJSON: canonical, Epoch: 1}); err != nil {
		t.Fatalf("put unsigned: %v", err)
	}
	mustRedeploy(t, s, credA, false)

	// Promote a manifest SIGNED by A into the served slot: matches pin A -> not required; pin B
	// (rotated away from the served key) -> required.
	signedA, err := trustlist.NewEd25519Signer(privA).Sign(manifest)
	if err != nil {
		t.Fatalf("sign A: %v", err)
	}
	sigJSON, err := json.Marshal(signedA)
	if err != nil {
		t.Fatalf("marshal sig: %v", err)
	}
	promoteServedTrustList(t, s, tnt, StoredTrustList{TrustListJSON: canonical, SignatureJSON: sigJSON, Epoch: 1})
	mustRedeploy(t, s, credA, false)
	mustRedeploy(t, s, credB, true)

	// Bug #1 (re-stage must not strand the served fleet) — and load-bearing for the staged/served
	// SPLIT specifically: re-stage a NEW manifest signed by a DIFFERENT key B into the STAGED slot
	// without promoting. The SERVED slot is still the A-signed epoch-1 manifest, so the redeploy
	// signal (which reads served) must still report not-required for pin A. This DISTINGUISHES the
	// split from the pre-fix single-slot world: if KeystoneRedeployRequired read the staged slot, it
	// would see the B-signed manifest, fail to verify it against pin A, and wrongly report required.
	restaged := trustlist.TrustList{SchemaVersion: 1, Tenant: string(tnt), Epoch: 2, Members: []trustlist.Member{{NodeID: "n1", BundleSHA256: "def"}}}
	restagedCanonical, err := trustlist.Canonical(restaged)
	if err != nil {
		t.Fatalf("canonical restaged: %v", err)
	}
	signedB, err := trustlist.NewEd25519Signer(privB).Sign(restaged)
	if err != nil {
		t.Fatalf("sign B: %v", err)
	}
	sigBJSON, err := json.Marshal(signedB)
	if err != nil {
		t.Fatalf("marshal sig B: %v", err)
	}
	if err := s.PutSignedTrustList(ctx, tnt, StoredTrustList{TrustListJSON: restagedCanonical, SignatureJSON: sigBJSON, Epoch: 2}); err != nil {
		t.Fatalf("re-stage signed-by-B: %v", err)
	}
	mustRedeploy(t, s, credA, false) // served slot still A-signed -> not required (would be true if it read staged)
}
