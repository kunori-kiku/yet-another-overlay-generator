package trustlist

import (
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"testing"
)

const (
	testRPID   = "panel.example"
	testOrigin = "https://panel.example"
)

// MarshalPKIXPinForTest encodes any PKIX-marshalable public key to a PEM block.
func MarshalPKIXPinForTest(t *testing.T, pub any) []byte {
	t.Helper()
	der, err := x509.MarshalPKIXPublicKey(pub)
	if err != nil {
		t.Fatalf("MarshalPKIXPublicKey: %v", err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der})
}

// MarshalEd25519PinForTest is a typed convenience over MarshalPKIXPinForTest.
func MarshalEd25519PinForTest(t *testing.T, pub ed25519.PublicKey) []byte {
	t.Helper()
	return MarshalPKIXPinForTest(t, pub)
}

// buildAuthData synthesizes WebAuthn authenticatorData: 32-byte rpIdHash =
// sha256(rpid), 1-byte flags, 4-byte signCount (we use 0, like a synced
// passkey).
func buildAuthData(rpid string, flags byte) []byte {
	rp := sha256.Sum256([]byte(rpid))
	ad := make([]byte, 0, 37)
	ad = append(ad, rp[:]...)
	ad = append(ad, flags)
	ad = append(ad, 0, 0, 0, 0) // signCount = 0
	return ad
}

// buildClientData synthesizes clientDataJSON for an assertion bound to a
// challenge.
func buildClientData(t *testing.T, typ, challengeB64, origin string) []byte {
	t.Helper()
	m := map[string]string{
		"type":      typ,
		"challenge": challengeB64,
		"origin":    origin,
	}
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatalf("marshal client data: %v", err)
	}
	return b
}

// signedMessage = authenticatorData || sha256(clientDataJSON).
func signedMessage(authData, clientData []byte) []byte {
	h := sha256.Sum256(clientData)
	out := make([]byte, 0, len(authData)+len(h))
	out = append(out, authData...)
	out = append(out, h[:]...)
	return out
}

// flags: 0x05 = User-Present (0x01) | User-Verified (0x04).
const flagsUPUV = 0x05

// buildES256Assertion constructs a valid ES256 WebAuthn artifact and its pin for
// the given trust list, signing with a fresh P-256 key.
func buildES256Assertion(t *testing.T, tl TrustList) (SignedTrustList, PinnedCredential) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate es256: %v", err)
	}
	chal, err := Challenge(tl)
	if err != nil {
		t.Fatalf("Challenge: %v", err)
	}
	challengeB64 := base64.RawURLEncoding.EncodeToString(chal)

	authData := buildAuthData(testRPID, flagsUPUV)
	clientData := buildClientData(t, "webauthn.get", challengeB64, testOrigin)
	signed := signedMessage(authData, clientData)
	digest := sha256.Sum256(signed)
	sig, err := ecdsa.SignASN1(rand.Reader, key, digest[:])
	if err != nil {
		t.Fatalf("SignASN1: %v", err)
	}

	art := SignedTrustList{
		Alg:               AlgWebAuthnES256,
		CredentialID:      "cred-es256",
		Signature:         base64.RawURLEncoding.EncodeToString(sig),
		AuthenticatorData: base64.RawURLEncoding.EncodeToString(authData),
		ClientDataJSON:    base64.RawURLEncoding.EncodeToString(clientData),
	}
	pin := PinnedCredential{
		Alg:          AlgWebAuthnES256,
		CredentialID: "cred-es256",
		ES256Pub:     &key.PublicKey,
		RPID:         testRPID,
		Origin:       testOrigin,
	}
	return art, pin
}

// buildEdDSAAssertion constructs a valid EdDSA WebAuthn artifact and its pin.
func buildEdDSAAssertion(t *testing.T, tl TrustList) (SignedTrustList, PinnedCredential) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate eddsa: %v", err)
	}
	chal, err := Challenge(tl)
	if err != nil {
		t.Fatalf("Challenge: %v", err)
	}
	challengeB64 := base64.RawURLEncoding.EncodeToString(chal)

	authData := buildAuthData(testRPID, flagsUPUV)
	clientData := buildClientData(t, "webauthn.get", challengeB64, testOrigin)
	signed := signedMessage(authData, clientData)
	// Ed25519 signs the message directly (no pre-hash).
	sig := ed25519.Sign(priv, signed)

	art := SignedTrustList{
		Alg:               AlgWebAuthnEdDSA,
		CredentialID:      "cred-eddsa",
		Signature:         base64.RawURLEncoding.EncodeToString(sig),
		AuthenticatorData: base64.RawURLEncoding.EncodeToString(authData),
		ClientDataJSON:    base64.RawURLEncoding.EncodeToString(clientData),
	}
	pin := PinnedCredential{
		Alg:          AlgWebAuthnEdDSA,
		CredentialID: "cred-eddsa",
		Ed25519Pub:   pub,
		RPID:         testRPID,
		Origin:       testOrigin,
	}
	return art, pin
}

// TestWebAuthnES256Valid: a correctly-built ES256 assertion verifies.
func TestWebAuthnES256Valid(t *testing.T) {
	tl := sampleTL()
	art, pin := buildES256Assertion(t, tl)
	if err := Verify(tl, art, pin); err != nil {
		t.Fatalf("Verify on valid ES256 assertion: %v", err)
	}
}

// TestWebAuthnEdDSAValid: a correctly-built EdDSA assertion verifies.
func TestWebAuthnEdDSAValid(t *testing.T) {
	tl := sampleTL()
	art, pin := buildEdDSAAssertion(t, tl)
	if err := Verify(tl, art, pin); err != nil {
		t.Fatalf("Verify on valid EdDSA assertion: %v", err)
	}
}

// TestWebAuthnWrongChallenge: an assertion signed for a DIFFERENT trust list
// fails the content binding when verified against this one.
func TestWebAuthnWrongChallenge(t *testing.T) {
	tlSigned := sampleTL()
	art, pin := buildES256Assertion(t, tlSigned)

	tlOther := sampleTL()
	tlOther.Epoch = tlSigned.Epoch + 1 // different content -> different challenge
	if err := Verify(tlOther, art, pin); !errors.Is(err, ErrChallengeMismatch) {
		t.Fatalf("Verify err = %v, want ErrChallengeMismatch", err)
	}
}

// TestWebAuthnTamperedTL: mutating the trust list after the assertion was built
// breaks the content binding (challenge no longer matches).
func TestWebAuthnTamperedTL(t *testing.T) {
	tl := sampleTL()
	art, pin := buildEdDSAAssertion(t, tl)
	tampered := sampleTL()
	tampered.Members[0].WGPublicKey = "EVIL="
	if err := Verify(tampered, art, pin); !errors.Is(err, ErrChallengeMismatch) {
		t.Fatalf("Verify err = %v, want ErrChallengeMismatch", err)
	}
}

// TestWebAuthnWrongType: clientData type "webauthn.create" is rejected.
func TestWebAuthnWrongType(t *testing.T) {
	tl := sampleTL()
	// Rebuild with the wrong type so the signature still covers the bytes.
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate es256: %v", err)
	}
	chal, _ := Challenge(tl)
	challengeB64 := base64.RawURLEncoding.EncodeToString(chal)
	authData := buildAuthData(testRPID, flagsUPUV)
	clientData := buildClientData(t, "webauthn.create", challengeB64, testOrigin)
	signed := signedMessage(authData, clientData)
	digest := sha256.Sum256(signed)
	sig, _ := ecdsa.SignASN1(rand.Reader, key, digest[:])
	art := SignedTrustList{
		Alg:               AlgWebAuthnES256,
		Signature:         base64.RawURLEncoding.EncodeToString(sig),
		AuthenticatorData: base64.RawURLEncoding.EncodeToString(authData),
		ClientDataJSON:    base64.RawURLEncoding.EncodeToString(clientData),
	}
	pin := PinnedCredential{Alg: AlgWebAuthnES256, ES256Pub: &key.PublicKey, RPID: testRPID}
	if err := Verify(tl, art, pin); err == nil {
		t.Fatalf("Verify accepted clientData type webauthn.create")
	}
}

// TestWebAuthnUPCleared: User-Present flag cleared (0x00) is rejected, even with
// a valid signature.
func TestWebAuthnUPCleared(t *testing.T) {
	tl := sampleTL()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate es256: %v", err)
	}
	chal, _ := Challenge(tl)
	challengeB64 := base64.RawURLEncoding.EncodeToString(chal)
	authData := buildAuthData(testRPID, 0x00) // UP cleared
	clientData := buildClientData(t, "webauthn.get", challengeB64, testOrigin)
	signed := signedMessage(authData, clientData)
	digest := sha256.Sum256(signed)
	sig, _ := ecdsa.SignASN1(rand.Reader, key, digest[:])
	art := SignedTrustList{
		Alg:               AlgWebAuthnES256,
		Signature:         base64.RawURLEncoding.EncodeToString(sig),
		AuthenticatorData: base64.RawURLEncoding.EncodeToString(authData),
		ClientDataJSON:    base64.RawURLEncoding.EncodeToString(clientData),
	}
	pin := PinnedCredential{Alg: AlgWebAuthnES256, ES256Pub: &key.PublicKey, RPID: testRPID}
	if err := Verify(tl, art, pin); err == nil {
		t.Fatalf("Verify accepted an assertion with User-Present cleared")
	}
}

// TestWebAuthnRPIDMismatch: a pinned RPID different from the one hashed into
// authData is rejected.
func TestWebAuthnRPIDMismatch(t *testing.T) {
	tl := sampleTL()
	art, pin := buildES256Assertion(t, tl)
	pin.RPID = "evil.example" // does not match sha256 baked into authData
	if err := Verify(tl, art, pin); err == nil {
		t.Fatalf("Verify accepted a mismatched RPID")
	}
}

// TestWebAuthnTruncatedAuthData: authenticatorData shorter than 37 bytes is
// rejected by the bounds check.
func TestWebAuthnTruncatedAuthData(t *testing.T) {
	tl := sampleTL()
	art, pin := buildES256Assertion(t, tl)
	// Decode, truncate to 36 bytes, re-encode.
	raw, _ := base64.RawURLEncoding.DecodeString(art.AuthenticatorData)
	art.AuthenticatorData = base64.RawURLEncoding.EncodeToString(raw[:36])
	if err := Verify(tl, art, pin); err == nil {
		t.Fatalf("Verify accepted truncated authenticator_data")
	}
}

// TestWebAuthnFlippedSignature: a corrupted signature is rejected.
func TestWebAuthnFlippedSignature(t *testing.T) {
	tl := sampleTL()
	art, pin := buildEdDSAAssertion(t, tl)
	raw, _ := base64.RawURLEncoding.DecodeString(art.Signature)
	raw[0] ^= 0x01
	art.Signature = base64.RawURLEncoding.EncodeToString(raw)
	if err := Verify(tl, art, pin); !errors.Is(err, ErrBadSignature) {
		t.Fatalf("Verify err = %v, want ErrBadSignature", err)
	}

	// Garbage (non-base64url) signature is also rejected.
	art2, pin2 := buildES256Assertion(t, tl)
	art2.Signature = "###garbage###"
	if err := Verify(tl, art2, pin2); err == nil {
		t.Fatalf("Verify accepted a non-base64url signature")
	}
}

// TestWebAuthnAlgConfusion: ES256 artifact against an EdDSA pin, and vice versa,
// is rejected by the alg-confusion guard (dispatch is on pin.Alg).
func TestWebAuthnAlgConfusion(t *testing.T) {
	tl := sampleTL()

	// ES256 artifact, EdDSA pin.
	esArt, _ := buildES256Assertion(t, tl)
	_, edPin := buildEdDSAAssertion(t, tl)
	if err := Verify(tl, esArt, edPin); !errors.Is(err, ErrAlgMismatch) {
		t.Fatalf("ES256 art vs EdDSA pin: err = %v, want ErrAlgMismatch", err)
	}

	// EdDSA artifact, ES256 pin.
	edArt, _ := buildEdDSAAssertion(t, tl)
	_, esPin := buildES256Assertion(t, tl)
	if err := Verify(tl, edArt, esPin); !errors.Is(err, ErrAlgMismatch) {
		t.Fatalf("EdDSA art vs ES256 pin: err = %v, want ErrAlgMismatch", err)
	}

	// Same declared alg but the artifact relabels to the OTHER WebAuthn alg
	// while the pin stays — also a mismatch caught before crypto.
	esArt2, esPin2 := buildES256Assertion(t, tl)
	esArt2.Alg = AlgWebAuthnEdDSA
	if err := Verify(tl, esArt2, esPin2); !errors.Is(err, ErrAlgMismatch) {
		t.Fatalf("relabeled ES256->EdDSA art vs ES256 pin: err = %v, want ErrAlgMismatch", err)
	}
}

// TestWebAuthnUnknownAlg: an RS256-ish / unknown algorithm is rejected.
func TestWebAuthnUnknownAlg(t *testing.T) {
	tl := sampleTL()
	art, pin := buildES256Assertion(t, tl)
	art.Alg = "webauthn-rs256"
	pin.Alg = "webauthn-rs256"
	if err := Verify(tl, art, pin); !errors.Is(err, ErrUnsupportedAlg) {
		t.Fatalf("Verify err = %v, want ErrUnsupportedAlg", err)
	}
}

// TestWebAuthnStdBase64Rejected: a standard-base64 (with '+' '/' '=') encoded
// field is rejected because the verifier uses RawURLEncoding throughout.
func TestWebAuthnStdBase64Rejected(t *testing.T) {
	tl := sampleTL()
	art, pin := buildES256Assertion(t, tl)
	// Re-encode authData with std base64 (padded); decoding as RawURL must fail
	// for inputs containing '=' padding or non-url-alphabet characters.
	raw, _ := base64.RawURLEncoding.DecodeString(art.AuthenticatorData)
	art.AuthenticatorData = base64.StdEncoding.EncodeToString(raw) // padded
	if err := Verify(tl, art, pin); err == nil {
		t.Fatalf("Verify accepted a std-base64 (padded) authenticator_data")
	}
}

// TestWebAuthnOriginAdvisory: a non-empty pinned origin that mismatches is
// rejected (advisory check is active when Origin is set), and an empty pinned
// origin skips the check.
func TestWebAuthnOriginAdvisory(t *testing.T) {
	tl := sampleTL()

	// Mismatching origin with a non-empty pin -> rejected.
	art, pin := buildES256Assertion(t, tl)
	pin.Origin = "https://evil.example"
	if err := Verify(tl, art, pin); err == nil {
		t.Fatalf("Verify accepted a mismatched origin with a non-empty pin")
	}

	// Empty pinned origin -> the origin check is skipped, assertion verifies.
	art2, pin2 := buildEdDSAAssertion(t, tl)
	pin2.Origin = ""
	if err := Verify(tl, art2, pin2); err != nil {
		t.Fatalf("Verify with empty pinned origin: %v", err)
	}
}

// TestWebAuthnMissingPin: a WebAuthn pin without the matching public key fails
// closed.
func TestWebAuthnMissingPin(t *testing.T) {
	tl := sampleTL()

	artES, pinES := buildES256Assertion(t, tl)
	pinES.ES256Pub = nil
	if err := Verify(tl, artES, pinES); !errors.Is(err, ErrMissingPin) {
		t.Fatalf("ES256 missing pin: err = %v, want ErrMissingPin", err)
	}

	artEd, pinEd := buildEdDSAAssertion(t, tl)
	pinEd.Ed25519Pub = nil
	if err := Verify(tl, artEd, pinEd); !errors.Is(err, ErrMissingPin) {
		t.Fatalf("EdDSA missing pin: err = %v, want ErrMissingPin", err)
	}
}

// TestWebAuthnBadClientDataJSON: clientDataJSON that is not valid JSON is
// rejected.
func TestWebAuthnBadClientDataJSON(t *testing.T) {
	tl := sampleTL()
	art, pin := buildES256Assertion(t, tl)
	art.ClientDataJSON = base64.RawURLEncoding.EncodeToString([]byte("{not json"))
	if err := Verify(tl, art, pin); err == nil {
		t.Fatalf("Verify accepted invalid clientDataJSON")
	}
}

// TestVerifyFailsClosedOnMalformedPin confirms the verifier returns an error (never
// PANICS) on a malformed pinned credential. A keystone verifier must fail closed even
// if a future caller builds a PinnedCredential by means other than the PEM parsers.
func TestVerifyFailsClosedOnMalformedPin(t *testing.T) {
	tl := TrustList{
		SchemaVersion: 1, Tenant: "acme", Epoch: 1,
		Members: []Member{{NodeID: "n1", WGPublicKey: "k1", BundleSHA256: "00112233445566778899aabbccddeeff00112233445566778899aabbccddeeff"}},
	}
	check := func(name string, pin PinnedCredential, art SignedTrustList) {
		defer func() {
			if r := recover(); r != nil {
				t.Errorf("%s: Verify PANICKED (%v); a keystone verifier must fail closed, not crash", name, r)
			}
		}()
		if err := Verify(tl, art, pin); err == nil {
			t.Errorf("%s: Verify returned nil for a malformed pin; want an error", name)
		}
	}

	esArt, esPin := buildES256Assertion(t, tl)
	edArt, edPin := buildEdDSAAssertion(t, tl)

	// ES256: nil pubkey, and a non-P-256 curve (would panic in ecdsa.VerifyASN1).
	check("es256 nil pub", PinnedCredential{Alg: AlgWebAuthnES256, RPID: testRPID, ES256Pub: nil}, esArt)
	p384, err := ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
	if err != nil {
		t.Fatalf("gen p384: %v", err)
	}
	check("es256 non-P256 curve", PinnedCredential{Alg: AlgWebAuthnES256, RPID: testRPID, ES256Pub: &p384.PublicKey}, esArt)

	// EdDSA: a wrong-length public key would PANIC in ed25519.Verify without the guard.
	check("eddsa short pub", PinnedCredential{Alg: AlgWebAuthnEdDSA, RPID: testRPID, Ed25519Pub: make([]byte, 31)}, edArt)

	// Empty RPID silently disables relying-party binding -> must be rejected.
	emptyRPID := esPin
	emptyRPID.RPID = ""
	check("empty rpid", emptyRPID, esArt)
	_ = edPin
}
