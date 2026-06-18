package localcompile

import (
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"fmt"

	"golang.zx2c4.com/wireguard/wgctrl/wgtypes"
)

// wgKeyLen is the X25519 key length in bytes (private and public). It mirrors
// wgtypes.KeyLen; declared here so the stdlib ecdhKeygen path does not import a
// constant for it.
const wgKeyLen = 32

// wgtypesKeygen is the default Keygen: it wraps today's exact wgtypes calls so the
// production pipeline is byte-for-byte unchanged. It is the implementation
// CompileRequest.Keygen defaults to (nil ⇒ wgtypesKeygen) and the byte-identity
// oracle the equivalence test pins ecdhKeygen against.
//
// It satisfies both the localcompile.Keygen contract member and render.Keygen
// (the same method set declared on the render consumer side — render must not
// import this package, so the seam interface is duplicated there by design).
type wgtypesKeygen struct{}

var _ Keygen = wgtypesKeygen{}

// DerivePublic reproduces wgtypes.ParseKey(privB64).PublicKey().String().
func (wgtypesKeygen) DerivePublic(privB64 string) (string, error) {
	priv, err := wgtypes.ParseKey(privB64)
	if err != nil {
		return "", err
	}
	return priv.PublicKey().String(), nil
}

// Generate reproduces wgtypes.GeneratePrivateKey() + .String() / .PublicKey().String().
func (wgtypesKeygen) Generate() (string, string, error) {
	priv, err := wgtypes.GeneratePrivateKey()
	if err != nil {
		return "", "", err
	}
	return priv.String(), priv.PublicKey().String(), nil
}

// ParseAndNormalize reproduces wgtypes.ParseKey(privB64).String() — the air-gap
// case-a re-canonicalization that is persisted back onto the node. wgtypes does
// NOT clamp on parse/String, so this is a length-validated base64 round-trip.
func (wgtypesKeygen) ParseAndNormalize(privB64 string) (string, error) {
	priv, err := wgtypes.ParseKey(privB64)
	if err != nil {
		return "", err
	}
	return priv.String(), nil
}

// ecdhKeygen is the stdlib reference Keygen: it derives X25519 public keys through
// crypto/ecdh instead of wgtypes/wgctrl, giving plan-5 (conformance) and plan-4
// (the TypeScript port) a wgctrl-free, stdlib-anchored definition of the key
// derivation and unblocking a future js/wasm Go oracle. It is proven byte-equal to
// wgtypesKeygen over 10k inputs (keygen_equivalence_test.go) on DerivePublic and
// ParseAndNormalize.
//
// It is a REFERENCE/TEST implementation: it is deliberately NOT wired into any
// production caller (production defaults to wgtypesKeygen).
type ecdhKeygen struct{}

var _ Keygen = ecdhKeygen{}

// DerivePublic decodes the base64 private key, derives the public key via
// crypto/ecdh X25519 (which clamps the scalar internally, exactly as
// curve25519.ScalarBaseMult does inside wgtypes' Key.PublicKey), and re-encodes.
func (ecdhKeygen) DerivePublic(privB64 string) (string, error) {
	raw, err := decodeKey(privB64)
	if err != nil {
		return "", err
	}
	priv, err := ecdh.X25519().NewPrivateKey(raw)
	if err != nil {
		return "", fmt.Errorf("ecdh: derive public from X25519 private key: %w", err)
	}
	return base64.StdEncoding.EncodeToString(priv.PublicKey().Bytes()), nil
}

// Generate produces a fresh X25519 key pair using the same clamp wgtypes applies
// in GeneratePrivateKey (key[0]&=248; key[31]&=127; key[31]|=64), then derives the
// public half via crypto/ecdh. The private key is random, so its bytes are never
// byte-asserted; only that the derived public matches what DerivePublic would
// yield for the same private key.
func (ecdhKeygen) Generate() (string, string, error) {
	raw := make([]byte, wgKeyLen)
	if _, err := rand.Read(raw); err != nil {
		return "", "", fmt.Errorf("ecdh: read random bytes: %w", err)
	}
	// Same X25519 clamp as wgtypes.GeneratePrivateKey (https://cr.yp.to/ecdh.html).
	raw[0] &= 248
	raw[31] &= 127
	raw[31] |= 64

	priv, err := ecdh.X25519().NewPrivateKey(raw)
	if err != nil {
		return "", "", fmt.Errorf("ecdh: build X25519 private key: %w", err)
	}
	privB64 := base64.StdEncoding.EncodeToString(raw)
	pubB64 := base64.StdEncoding.EncodeToString(priv.PublicKey().Bytes())
	return privB64, pubB64, nil
}

// ParseAndNormalize round-trips a private key to its canonical base64 form. It
// mirrors wgtypes' ParseKey(...).String(), which is a length-validated base64
// round-trip with NO clamping, so this decodes (validating the 32-byte length) and
// re-encodes without going through ecdh — clamping here would diverge from
// wgtypes' String() and corrupt the air-gap case-a re-write byte-for-byte.
func (ecdhKeygen) ParseAndNormalize(privB64 string) (string, error) {
	raw, err := decodeKey(privB64)
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(raw), nil
}

// decodeKey base64-decodes a key and validates the X25519 length, matching the two
// failure modes of wgtypes.ParseKey (bad base64, wrong length).
func decodeKey(b64 string) ([]byte, error) {
	raw, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		return nil, fmt.Errorf("decode base64 X25519 key: %w", err)
	}
	if len(raw) != wgKeyLen {
		return nil, fmt.Errorf("X25519 key has length %d, want %d", len(raw), wgKeyLen)
	}
	return raw, nil
}
