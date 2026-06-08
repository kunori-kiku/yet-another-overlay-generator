package trustlist

import (
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"testing"
)

// newEd25519Pin generates a key pair, returns a signer plus a matching pinned
// credential.
func newEd25519Pin(t *testing.T) (*Ed25519Signer, PinnedCredential) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	signer := NewEd25519Signer(priv)
	pin := PinnedCredential{
		Alg:          AlgEd25519,
		CredentialID: signer.KeyID(),
		Ed25519Pub:   pub,
	}
	return signer, pin
}

// TestEd25519SignVerifyRoundTrip: a freshly signed trust list verifies.
func TestEd25519SignVerifyRoundTrip(t *testing.T) {
	signer, pin := newEd25519Pin(t)
	tl := sampleTL()
	art, err := signer.Sign(tl)
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}
	if art.Alg != AlgEd25519 {
		t.Fatalf("art.Alg = %q, want %q", art.Alg, AlgEd25519)
	}
	if art.CredentialID != signer.KeyID() {
		t.Fatalf("art.CredentialID = %q, want %q", art.CredentialID, signer.KeyID())
	}
	// Signature must be valid base64url and decode to the raw Ed25519 size.
	sig, err := base64.RawURLEncoding.DecodeString(art.Signature)
	if err != nil {
		t.Fatalf("signature not base64url: %v", err)
	}
	if len(sig) != ed25519.SignatureSize {
		t.Fatalf("signature length = %d, want %d", len(sig), ed25519.SignatureSize)
	}
	if err := Verify(tl, art, pin); err != nil {
		t.Fatalf("Verify on valid artifact: %v", err)
	}
}

// TestEd25519TamperFails: mutating any signed field after signing breaks Verify.
func TestEd25519TamperFails(t *testing.T) {
	signer, pin := newEd25519Pin(t)
	tl := sampleTL()
	art, err := signer.Sign(tl)
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}

	mutations := map[string]func(tl *TrustList){
		"member": func(tl *TrustList) { tl.Members[0].WGPublicKey = "EVIL=" },
		"epoch":  func(tl *TrustList) { tl.Epoch = 999 },
		"tenant": func(tl *TrustList) { tl.Tenant = "attacker" },
	}
	for name, mut := range mutations {
		tampered := sampleTL()
		mut(&tampered)
		if err := Verify(tampered, art, pin); err == nil {
			t.Fatalf("%s: Verify accepted a tampered trust list", name)
		} else if !errors.Is(err, ErrBadSignature) {
			t.Fatalf("%s: Verify err = %v, want ErrBadSignature", name, err)
		}
	}
}

// TestEd25519WrongPinFails: a signature does not verify against a different
// pinned key.
func TestEd25519WrongPinFails(t *testing.T) {
	signer, _ := newEd25519Pin(t)
	tl := sampleTL()
	art, err := signer.Sign(tl)
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}
	otherPub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate other key: %v", err)
	}
	wrong := PinnedCredential{Alg: AlgEd25519, Ed25519Pub: otherPub}
	if err := Verify(tl, art, wrong); !errors.Is(err, ErrBadSignature) {
		t.Fatalf("Verify err = %v, want ErrBadSignature", err)
	}
}

// TestEd25519AlgMismatch: an Ed25519 pin with a WebAuthn artifact (or vice
// versa) is rejected by the algorithm-confusion guard.
func TestEd25519AlgMismatch(t *testing.T) {
	signer, pin := newEd25519Pin(t)
	tl := sampleTL()
	art, err := signer.Sign(tl)
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}
	// Attacker relabels the artifact's alg to a WebAuthn variant.
	art.Alg = AlgWebAuthnES256
	if err := Verify(tl, art, pin); !errors.Is(err, ErrAlgMismatch) {
		t.Fatalf("Verify err = %v, want ErrAlgMismatch", err)
	}
}

// TestEd25519MissingPin: an Ed25519 pin without a public key fails closed.
func TestEd25519MissingPin(t *testing.T) {
	signer, _ := newEd25519Pin(t)
	tl := sampleTL()
	art, err := signer.Sign(tl)
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}
	pin := PinnedCredential{Alg: AlgEd25519} // no Ed25519Pub
	if err := Verify(tl, art, pin); !errors.Is(err, ErrMissingPin) {
		t.Fatalf("Verify err = %v, want ErrMissingPin", err)
	}
}

// TestEd25519BadSignatureEncoding: a non-base64url signature is rejected
// (asserting standard-base64 input would be rejected too where it differs).
func TestEd25519BadSignatureEncoding(t *testing.T) {
	signer, pin := newEd25519Pin(t)
	tl := sampleTL()
	art, err := signer.Sign(tl)
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}
	art.Signature = "!!!not base64!!!"
	if err := Verify(tl, art, pin); err == nil {
		t.Fatalf("Verify accepted a non-base64url signature")
	}
}

// TestUnsupportedAlg: a pin naming an unimplemented algorithm fails closed.
func TestUnsupportedAlg(t *testing.T) {
	tl := sampleTL()
	art := SignedTrustList{Alg: "rs256"}
	pin := PinnedCredential{Alg: "rs256"}
	if err := Verify(tl, art, pin); !errors.Is(err, ErrUnsupportedAlg) {
		t.Fatalf("Verify err = %v, want ErrUnsupportedAlg", err)
	}
}

// TestParsePinsRoundTrip: the PEM pin parsers accept well-formed keys and reject
// mismatched ones.
func TestParsePinsRoundTrip(t *testing.T) {
	// Ed25519 pin via the bundlesig PEM marshaler shape.
	signer, pin := newEd25519Pin(t)
	_ = signer
	pemEd := MarshalEd25519PinForTest(t, pin.Ed25519Pub)
	gotEd, err := ParseEd25519PinPEM(pemEd)
	if err != nil {
		t.Fatalf("ParseEd25519PinPEM: %v", err)
	}
	if !gotEd.Equal(pin.Ed25519Pub) {
		t.Fatalf("parsed ed25519 pin does not match")
	}
	// Feeding an Ed25519 PEM to the ES256 parser must fail.
	if _, err := ParseES256Pin(pemEd); err == nil {
		t.Fatalf("ParseES256Pin accepted an Ed25519 key")
	}

	// ES256 pin (P-256).
	es, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate es256: %v", err)
	}
	pemES := MarshalPKIXPinForTest(t, &es.PublicKey)
	gotES, err := ParseES256Pin(pemES)
	if err != nil {
		t.Fatalf("ParseES256Pin: %v", err)
	}
	if !gotES.Equal(&es.PublicKey) {
		t.Fatalf("parsed es256 pin does not match")
	}
	// Feeding an ES256 PEM to the Ed25519 parser must fail.
	if _, err := ParseEd25519PinPEM(pemES); err == nil {
		t.Fatalf("ParseEd25519PinPEM accepted an ES256 key")
	}

	// A non-P-256 ECDSA key must be rejected by ParseES256Pin.
	es384, err := ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
	if err != nil {
		t.Fatalf("generate p384: %v", err)
	}
	pemES384 := MarshalPKIXPinForTest(t, &es384.PublicKey)
	if _, err := ParseES256Pin(pemES384); err == nil {
		t.Fatalf("ParseES256Pin accepted a non-P-256 key")
	}

	// Garbage input is rejected by both.
	if _, err := ParseEd25519PinPEM([]byte("not a pem")); err == nil {
		t.Fatalf("ParseEd25519PinPEM accepted garbage")
	}
	if _, err := ParseES256Pin([]byte("not a pem")); err == nil {
		t.Fatalf("ParseES256Pin accepted garbage")
	}
}
