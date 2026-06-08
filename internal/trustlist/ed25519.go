package trustlist

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"

	"github.com/kunorikiku/yet-another-overlay-generator/internal/bundlesig"
)

// Ed25519Signer signs trust lists with a raw detached Ed25519 signature over
// Canonical(tl), reusing bundlesig's signing primitives.
//
// THIS IS A SOFTWARE, ON-HOST SIGNER — for development and CI ONLY. Because the
// private key lives on the host, it must NEVER be the production trust anchor.
// Production signing should use a hardware WebAuthn authenticator (see
// verifyWebAuthn) so the signing key never touches a node or a build machine.
type Ed25519Signer struct {
	priv ed25519.PrivateKey
}

// NewEd25519Signer wraps an Ed25519 private key as a Signer.
func NewEd25519Signer(priv ed25519.PrivateKey) *Ed25519Signer {
	return &Ed25519Signer{priv: priv}
}

// KeyID is hex(sha256(public key)) — a stable identifier for the signing key,
// recorded as the artifact's CredentialID for audit and credential matching.
func (s *Ed25519Signer) KeyID() string {
	pub := s.priv.Public().(ed25519.PublicKey)
	sum := sha256.Sum256(pub)
	return hex.EncodeToString(sum[:])
}

// Sign produces a detached Ed25519 SignedTrustList over Canonical(tl).
//
// The Signature is base64url (RawURLEncoding, no padding) of the raw 64-byte
// signature; PublicKey is the PKIX PEM of the verifying key (audit/convenience
// only — the verifier checks against the pinned credential, not this field).
func (s *Ed25519Signer) Sign(tl TrustList) (SignedTrustList, error) {
	c, err := Canonical(tl)
	if err != nil {
		return SignedTrustList{}, fmt.Errorf("trustlist: ed25519 sign: %w", err)
	}
	pub := s.priv.Public().(ed25519.PublicKey)
	sig := bundlesig.Sign(c, s.priv)
	return SignedTrustList{
		Alg:          AlgEd25519,
		CredentialID: s.KeyID(),
		PublicKey:    string(bundlesig.MarshalPublicKeyPEM(pub)),
		Signature:    base64.RawURLEncoding.EncodeToString(sig),
	}, nil
}
