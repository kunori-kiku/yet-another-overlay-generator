package conformance

import (
	"crypto/ecdh"
	"encoding/base64"
	"encoding/json"
	"os"
	"testing"
)

// keygen_kat_test.go — X25519 known-answer test (step 4 of plan-5 / 1.5).
//
// It pins the WireGuard key-derivation definition the whole conformance program rests on:
// the public key is X25519(private, basepoint), base64.StdEncoding-encoded. The test derives
// public keys here by calling crypto/ecdh X25519 DIRECTLY — the SAME stdlib derivation
// internal/localcompile.ecdhKeygen.DerivePublic wraps. ecdhKeygen is unexported (a deliberate
// reference/test implementation not wired into production), so the KAT cannot call it across the
// package boundary; calling ecdh.X25519().NewPrivateKey(raw).PublicKey() reproduces its derivation
// byte-for-byte (ecdhKeygen IS that one line plus base64). plan-3 proved ecdhKeygen byte-equal to
// the default wgtypesKeygen (the production path) over 10k inputs, so this single definition is the
// anchor plans 3, 4, and 5 all pin: the harness here, plan-4's @noble/curves port (which asserts
// against testdata/keygen_kat.json), and the live pipeline.
//
// crypto/ecdh's X25519 clamps the scalar internally during the multiplication (exactly as
// curve25519.ScalarBaseMult does inside wgtypes' Key.PublicKey), so an UNCLAMPED private key and
// its clamped form derive the identical public key — the clamp_equivalence group asserts that
// boundary property a TS port must reproduce.

// keygenKAT is the on-disk testdata/keygen_kat.json shape.
type keygenKAT struct {
	Note    string `json:"note"`
	Vectors []struct {
		Name       string `json:"name"`
		Doc        string `json:"doc"`
		PrivateB64 string `json:"private_b64"`
		PublicB64  string `json:"public_b64"`
	} `json:"vectors"`
	ClampEquivalence []struct {
		Name       string `json:"name"`
		Doc        string `json:"doc"`
		RawB64     string `json:"raw_b64"`
		ClampedB64 string `json:"clamped_b64"`
		PublicB64  string `json:"public_b64"`
	} `json:"clamp_equivalence"`
}

// derivePublicB64 reproduces internal/localcompile.ecdhKeygen.DerivePublic: decode the base64
// X25519 private key, derive the public half via crypto/ecdh (which clamps internally), re-encode
// base64.StdEncoding. Keeping this one-liner local (rather than importing the unexported ecdhKeygen)
// is what makes the KAT a SECOND independent witness of the derivation — if ecdhKeygen ever drifts
// from the X25519 definition, the equivalence test in localcompile catches it there and this KAT
// catches it here.
func derivePublicB64(t *testing.T, privB64 string) string {
	t.Helper()
	raw, err := base64.StdEncoding.DecodeString(privB64)
	if err != nil {
		t.Fatalf("decode private key %q: %v", privB64, err)
	}
	priv, err := ecdh.X25519().NewPrivateKey(raw)
	if err != nil {
		t.Fatalf("build X25519 private key from %q: %v", privB64, err)
	}
	return base64.StdEncoding.EncodeToString(priv.PublicKey().Bytes())
}

func loadKAT(t *testing.T) keygenKAT {
	t.Helper()
	raw, err := os.ReadFile("testdata/keygen_kat.json")
	if err != nil {
		t.Fatalf("read keygen_kat.json: %v", err)
	}
	var kat keygenKAT
	if err := json.Unmarshal(raw, &kat); err != nil {
		t.Fatalf("parse keygen_kat.json: %v", err)
	}
	if len(kat.Vectors) == 0 {
		t.Fatal("keygen_kat.json has no vectors")
	}
	if len(kat.ClampEquivalence) == 0 {
		t.Fatal("keygen_kat.json has no clamp_equivalence vectors")
	}
	return kat
}

// TestKeygenKAT is the known-answer test: every vector's private key must derive its frozen public
// key, and every clamp-equivalence pair (a raw private key and its X25519-clamped form) must derive
// the SAME public key. RFC 7748 section 6.1's Alice/Bob vectors anchor the derivation against the
// authoritative IETF answer; the clamp-boundary vectors exercise key[0] &= 248, key[31] &= 127, and
// key[31] |= 64.
func TestKeygenKAT(t *testing.T) {
	kat := loadKAT(t)

	t.Run("vectors", func(t *testing.T) {
		for _, v := range kat.Vectors {
			v := v
			t.Run(v.Name, func(t *testing.T) {
				got := derivePublicB64(t, v.PrivateB64)
				if got != v.PublicB64 {
					t.Errorf("X25519 public-key mismatch for %s:\n  priv: %s\n  want: %s\n  got:  %s",
						v.Name, v.PrivateB64, v.PublicB64, got)
				}
			})
		}
	})

	t.Run("clamp_equivalence", func(t *testing.T) {
		for _, c := range kat.ClampEquivalence {
			c := c
			t.Run(c.Name, func(t *testing.T) {
				rawPub := derivePublicB64(t, c.RawB64)
				clampedPub := derivePublicB64(t, c.ClampedB64)
				if rawPub != clampedPub {
					t.Errorf("clamp equivalence broken for %s: raw and clamped private keys derived DIFFERENT public keys (X25519 must clamp internally)\n  raw_pub:     %s\n  clamped_pub: %s",
						c.Name, rawPub, clampedPub)
				}
				if rawPub != c.PublicB64 {
					t.Errorf("clamp equivalence %s: derived public %s does not match the frozen public %s",
						c.Name, rawPub, c.PublicB64)
				}
			})
		}
	})
}
