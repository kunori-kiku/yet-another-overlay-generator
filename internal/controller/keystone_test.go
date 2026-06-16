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

	// WebAuthn ES256: same key but a changed rpid or credential_id is a rotation.
	pemES, _ := es256CredPEM(t)
	base := OperatorCredential{Alg: string(trustlist.AlgWebAuthnES256), PublicKeyPEM: pemES, RPID: "rp.example", CredentialID: "cred-1"}
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
	// A different alg is always a rotation.
	if same, _ := SameKeystoneCredential(a, base); same {
		t.Fatal("different algs must be a rotation")
	}
}

// TestKeystoneRedeployRequired walks the states: no manifest, staged-but-unsigned, signed and
// matching the pin, and signed under a DIFFERENT key than the pin (the rotated-but-not-redeployed
// case that must report true).
func TestKeystoneRedeployRequired(t *testing.T) {
	ctx := context.Background()
	const tnt = TenantID("acme")
	pubA, privA, _ := ed25519.GenerateKey(rand.Reader)
	pubB, _, _ := ed25519.GenerateKey(rand.Reader)
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

	// No manifest stored at all -> not required.
	s := NewMemStore()
	mustRedeploy(t, s, credA, false)

	// Staged but UNSIGNED (empty signature) -> mid-deploy window, not required.
	if err := s.PutSignedTrustList(ctx, tnt, StoredTrustList{TrustListJSON: canonical, Epoch: 1}); err != nil {
		t.Fatalf("put unsigned: %v", err)
	}
	mustRedeploy(t, s, credA, false)

	// Signed by A: matches pin A -> not required; pin B -> rotated, required.
	signedA, err := trustlist.NewEd25519Signer(privA).Sign(manifest)
	if err != nil {
		t.Fatalf("sign A: %v", err)
	}
	sigJSON, err := json.Marshal(signedA)
	if err != nil {
		t.Fatalf("marshal sig: %v", err)
	}
	if err := s.PutSignedTrustList(ctx, tnt, StoredTrustList{TrustListJSON: canonical, SignatureJSON: sigJSON, Epoch: 1}); err != nil {
		t.Fatalf("put signed: %v", err)
	}
	mustRedeploy(t, s, credA, false)
	mustRedeploy(t, s, credB, true)
}
