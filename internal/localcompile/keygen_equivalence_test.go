package localcompile

import (
	"crypto/rand"
	"encoding/base64"
	"testing"
)

// TestKeygenEquivalence_WgtypesVsEcdh is the perpetual byte-identity oracle for the
// X25519 key seam: over 10k random private keys (plus the clamp-bit edge cases) the
// stdlib crypto/ecdh implementation (ecdhKeygen) MUST produce byte-identical output
// to the wgtypes/wgctrl implementation (wgtypesKeygen) on both:
//
//   - DerivePublic       — the public key derived from a private key, and
//   - ParseAndNormalize  — the canonical base64 round-trip wgtypes' String() does
//     (the air-gap case-a re-write that is persisted back onto
//     the node).
//
// This pins the determinism / byte-identity Principle (P1/P5) that the WASM engine's
// stdlib key derivation (ecdhKeygen) depends on: a divergence here would silently
// corrupt every checksum, bundle
// signature, and keystone digest. wgtypesKeygen is the byte-for-byte production
// default; ecdhKeygen is the wgctrl-free reference proven equal to it here.
func TestKeygenEquivalence_WgtypesVsEcdh(t *testing.T) {
	var (
		wg   wgtypesKeygen
		ecdh ecdhKeygen
	)

	assertEqual := func(t *testing.T, label, privB64 string) {
		t.Helper()

		wgNorm, wgNormErr := wg.ParseAndNormalize(privB64)
		ecdhNorm, ecdhNormErr := ecdh.ParseAndNormalize(privB64)
		if (wgNormErr == nil) != (ecdhNormErr == nil) {
			t.Fatalf("%s: ParseAndNormalize error mismatch: wgtypes err=%v ecdh err=%v", label, wgNormErr, ecdhNormErr)
		}
		if wgNormErr == nil && wgNorm != ecdhNorm {
			t.Fatalf("%s: ParseAndNormalize diverged:\n wgtypes=%q\n ecdh   =%q", label, wgNorm, ecdhNorm)
		}

		wgPub, wgPubErr := wg.DerivePublic(privB64)
		ecdhPub, ecdhPubErr := ecdh.DerivePublic(privB64)
		if (wgPubErr == nil) != (ecdhPubErr == nil) {
			t.Fatalf("%s: DerivePublic error mismatch: wgtypes err=%v ecdh err=%v", label, wgPubErr, ecdhPubErr)
		}
		if wgPubErr == nil && wgPub != ecdhPub {
			t.Fatalf("%s: DerivePublic diverged:\n wgtypes=%q\n ecdh   =%q", label, wgPub, ecdhPub)
		}
	}

	encode := func(raw []byte) string { return base64.StdEncoding.EncodeToString(raw) }

	// 1. 10k random 32-byte keys (unclamped — exercises the full input space, not
	//    just the clamped subset GeneratePrivateKey emits).
	const n = 10000
	for i := 0; i < n; i++ {
		raw := make([]byte, wgKeyLen)
		if _, err := rand.Read(raw); err != nil {
			t.Fatalf("rand.Read: %v", err)
		}
		assertEqual(t, "random", encode(raw))
	}

	// 2. The clamp-bit edge cases: each random key both BEFORE and AFTER the X25519
	//    clamp (key[0]&=248; key[31]&=127; key[31]|=64). ParseAndNormalize must NOT
	//    clamp (it mirrors wgtypes' String()), while DerivePublic clamps internally
	//    — both branches must agree across wgtypes and ecdh either way.
	for i := 0; i < 1000; i++ {
		raw := make([]byte, wgKeyLen)
		if _, err := rand.Read(raw); err != nil {
			t.Fatalf("rand.Read: %v", err)
		}
		assertEqual(t, "pre-clamp", encode(raw))

		clamped := make([]byte, wgKeyLen)
		copy(clamped, raw)
		clamped[0] &= 248
		clamped[31] &= 127
		clamped[31] |= 64
		assertEqual(t, "clamped", encode(clamped))
	}

	// 3. Fixed boundary keys: all-zero and all-0xFF (the curve edge cases).
	allZero := make([]byte, wgKeyLen)
	assertEqual(t, "all-zero", encode(allZero))

	allFF := make([]byte, wgKeyLen)
	for i := range allFF {
		allFF[i] = 0xff
	}
	assertEqual(t, "all-ff", encode(allFF))

	// 4. The exact clamp pattern applied to all-zero / all-FF, pinning each clamp
	//    bit independently.
	clampLowOnly := make([]byte, wgKeyLen)
	for i := range clampLowOnly {
		clampLowOnly[i] = 0xff
	}
	clampLowOnly[0] &= 248
	clampLowOnly[31] &= 127
	clampLowOnly[31] |= 64
	assertEqual(t, "all-ff-clamped", encode(clampLowOnly))
}
