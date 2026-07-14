package trustlist

// verify_assertion_test.go covers the exported VerifyAssertion / AssertionChallenge —
// the challenge-AGNOSTIC sibling of the content-bound keystone verifier, used by operator
// passkey login where the challenge is a server-issued RANDOM nonce (not Challenge(tl)).
// It reuses the in-package WebAuthn test builders (buildAuthData / buildClientData /
// signedMessage / MarshalEd25519PinForTest).

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"testing"
)

func TestVerifyAssertionRandomChallenge(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("keygen: %v", err)
	}
	const rpid = "panel.example.com"
	const origin = "https://panel.example.com"
	challenge := []byte("0123456789abcdef0123456789abcdef") // 32 bytes of "random" nonce
	challengeB64 := base64.RawURLEncoding.EncodeToString(challenge)

	authData := buildAuthData(rpid, flagUserPresent|flagUserVerified)
	clientData := buildClientData(t, "webauthn.get", challengeB64, origin)
	sig := ed25519.Sign(priv, signedMessage(authData, clientData))
	art := SignedTrustList{
		Alg:               AlgWebAuthnEdDSA,
		CredentialID:      "login-cred",
		PublicKey:         string(MarshalEd25519PinForTest(t, pub)),
		Signature:         base64.RawURLEncoding.EncodeToString(sig),
		AuthenticatorData: base64.RawURLEncoding.EncodeToString(authData),
		ClientDataJSON:    base64.RawURLEncoding.EncodeToString(clientData),
	}
	pin := PinnedCredential{Alg: AlgWebAuthnEdDSA, Ed25519Pub: pub, RPID: rpid, Origin: origin}

	// Valid assertion over the expected random challenge.
	if err := VerifyAssertion(art, pin, challenge); err != nil {
		t.Fatalf("VerifyAssertion(valid) = %v, want nil", err)
	}

	// AssertionChallenge recovers the embedded base64url challenge (the login lookup key).
	if got, err := AssertionChallenge(art); err != nil || got != challengeB64 {
		t.Fatalf("AssertionChallenge = (%q,%v), want (%q,nil)", got, err, challengeB64)
	}

	// A DIFFERENT expected challenge is a mismatch — the assertion does not bind to it.
	if err := VerifyAssertion(art, pin, []byte("a-totally-different-32-byte-value")); !errors.Is(err, ErrChallengeMismatch) {
		t.Fatalf("VerifyAssertion(wrong challenge) = %v, want ErrChallengeMismatch", err)
	}

	// Algorithm confusion: artifact alg disagreeing with the pinned alg is rejected.
	confused := art
	confused.Alg = AlgWebAuthnES256
	if err := VerifyAssertion(confused, pin, challenge); !errors.Is(err, ErrAlgMismatch) {
		t.Fatalf("VerifyAssertion(alg confusion) = %v, want ErrAlgMismatch", err)
	}

	// A raw (non-WebAuthn) Ed25519 pin has no assertion to verify — unsupported here.
	rawArt, rawPin := art, pin
	rawArt.Alg, rawPin.Alg = AlgEd25519, AlgEd25519
	if err := VerifyAssertion(rawArt, rawPin, challenge); !errors.Is(err, ErrUnsupportedAlg) {
		t.Fatalf("VerifyAssertion(raw ed25519) = %v, want ErrUnsupportedAlg", err)
	}

	// User-Present-ONLY (UV cleared): a possession-only assertion is REJECTED. Both ceremonies pin
	// userVerification:"required", so the server must enforce UV — a PIN-less authenticator, or a
	// tampered client requesting UV=discouraged, must not mint a session / sign a manifest. (Before the
	// fix, this test's own "valid" case used a UP-only assertion and PASSED — the live vulnerability.)
	upOnlyAuth := buildAuthData(rpid, flagUserPresent) // UV bit cleared
	upOnly := art
	upOnly.AuthenticatorData = base64.RawURLEncoding.EncodeToString(upOnlyAuth)
	upOnly.Signature = base64.RawURLEncoding.EncodeToString(ed25519.Sign(priv, signedMessage(upOnlyAuth, clientData)))
	if err := VerifyAssertion(upOnly, pin, challenge); !errors.Is(err, ErrUserVerification) {
		t.Fatalf("VerifyAssertion(User-Present only) = %v, want ErrUserVerification", err)
	}
}
