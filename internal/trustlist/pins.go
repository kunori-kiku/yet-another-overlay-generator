package trustlist

import (
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/x509"
	"encoding/pem"
	"fmt"
)

// ParseEd25519PinPEM decodes a PKIX ("PUBLIC KEY") PEM block into an Ed25519
// public key, used to load the pinned anchor for AlgEd25519 and AlgWebAuthnEdDSA.
//
// Expected input: a single PEM block of the form produced by
// bundlesig.MarshalPublicKeyPEM / `openssl pkey -pubout` for an Ed25519 key.
func ParseEd25519PinPEM(data []byte) (ed25519.PublicKey, error) {
	block, _ := pem.Decode(data)
	if block == nil {
		return nil, fmt.Errorf("trustlist: no PEM block in ed25519 pin")
	}
	key, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("trustlist: parse PKIX public key: %w", err)
	}
	pub, ok := key.(ed25519.PublicKey)
	if !ok {
		return nil, fmt.Errorf("trustlist: pin is %T, want ed25519.PublicKey", key)
	}
	return pub, nil
}

// ParseES256Pin decodes a PKIX ("PUBLIC KEY") PEM block into an ECDSA P-256
// public key, used to load the pinned anchor for AlgWebAuthnES256.
//
// Expected input: a single PKIX PEM block holding an ECDSA key on the NIST P-256
// curve (the curve WebAuthn ES256 uses), e.g. `openssl ec -pubout`. The curve is
// enforced — a key on any other curve is rejected, so the pin can only ever be a
// genuine ES256 anchor.
func ParseES256Pin(data []byte) (*ecdsa.PublicKey, error) {
	block, _ := pem.Decode(data)
	if block == nil {
		return nil, fmt.Errorf("trustlist: no PEM block in ES256 pin")
	}
	key, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("trustlist: parse PKIX public key: %w", err)
	}
	pub, ok := key.(*ecdsa.PublicKey)
	if !ok {
		return nil, fmt.Errorf("trustlist: pin is %T, want *ecdsa.PublicKey", key)
	}
	if pub.Curve != elliptic.P256() {
		return nil, fmt.Errorf("trustlist: ES256 pin is on curve %s, want P-256", pub.Curve.Params().Name)
	}
	return pub, nil
}
