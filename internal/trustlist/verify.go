package trustlist

import (
	"encoding/base64"
	"errors"
	"fmt"

	"github.com/kunorikiku/yet-another-overlay-generator/internal/bundlesig"
)

// Sentinel errors for the most security-relevant rejection reasons. Callers can
// errors.Is against these; all other failures are descriptive fmt errors. A
// non-nil return from Verify ALWAYS means "do not trust this artifact".
var (
	// ErrAlgMismatch: the artifact's declared Alg disagrees with the pinned
	// credential's Alg. This is the algorithm-confusion guard — we dispatch on
	// the pinned (trusted) Alg and refuse to even consider a mismatching
	// attacker-supplied Alg.
	ErrAlgMismatch = errors.New("trustlist: artifact alg does not match pinned credential alg")
	// ErrUnsupportedAlg: the pinned credential names an algorithm this verifier
	// does not implement (e.g. RS256). Fail closed.
	ErrUnsupportedAlg = errors.New("trustlist: unsupported algorithm")
	// ErrBadSignature: the cryptographic signature check failed.
	ErrBadSignature = errors.New("trustlist: signature verification failed")
	// ErrChallengeMismatch: a WebAuthn assertion's challenge is not
	// base64url(Challenge(tl)) — the assertion does not bind to THIS trust list.
	ErrChallengeMismatch = errors.New("trustlist: webauthn challenge does not match trust list")
	// ErrMissingPin: the pinned credential lacks the public key required for its
	// algorithm.
	ErrMissingPin = errors.New("trustlist: pinned credential missing public key")
	// ErrUserVerification: an enrollment proof has User-Present set but NOT User-Verified.
	// YAOG verifies this once, server-side, before a new browser WebAuthn credential is stored.
	// Ordinary login/signing assertions deliberately do not inherit this enrollment policy.
	ErrUserVerification = errors.New("trustlist: webauthn enrollment proof has no User-Verified flag")
)

// Verify is the ONE function nodes embed. It is fail-closed: it returns nil
// ONLY when art is a valid signature over Canonical(tl) by the PINNED
// credential, and — for WebAuthn — the assertion binds to THIS trust list. Any
// parse error, field mismatch, missing field, or signature failure returns a
// non-nil error.
//
// Crucially, dispatch is on pin.Alg (the trusted, out-of-band-provisioned
// algorithm), NOT art.Alg (attacker-influenced). If art.Alg != pin.Alg we reject
// outright (ErrAlgMismatch) before any cryptographic work, closing the
// algorithm-confusion door.
//
// CALLER CONTRACT (load-bearing for the node consumer, plan-5.1c): the signed
// payload is Canonical(tl), NOT the raw distributed trustlist.json bytes. Verify
// re-canonicalizes tl, so it tolerates a distributed file that carries unknown
// fields, duplicate keys, or different whitespace — those are simply dropped from
// the canonical projection. A node that ACTS on the membership MUST act on
// Canonical(tl) (or assert Canonical(parsed) byte-equals the received file and use a
// strict decoder), so it never trusts bytes the user did not actually sign.
func Verify(tl TrustList, art SignedTrustList, pin PinnedCredential) error {
	// Algorithm-confusion guard: the artifact must declare the same algorithm
	// the verifier was pinned to. We still dispatch on pin.Alg below regardless.
	if art.Alg != pin.Alg {
		return fmt.Errorf("%w: pinned=%q artifact=%q", ErrAlgMismatch, pin.Alg, art.Alg)
	}

	switch pin.Alg {
	case AlgEd25519:
		return verifyEd25519(tl, art, pin)
	case AlgWebAuthnES256, AlgWebAuthnEdDSA:
		return verifyWebAuthn(tl, art, pin)
	default:
		return fmt.Errorf("%w: %q", ErrUnsupportedAlg, pin.Alg)
	}
}

// verifyEd25519 checks a raw detached Ed25519 signature over Canonical(tl)
// against the pinned Ed25519 public key.
func verifyEd25519(tl TrustList, art SignedTrustList, pin PinnedCredential) error {
	if len(pin.Ed25519Pub) == 0 {
		return ErrMissingPin
	}
	c, err := Canonical(tl)
	if err != nil {
		return err
	}
	sig, err := base64.RawURLEncoding.DecodeString(art.Signature)
	if err != nil {
		return fmt.Errorf("trustlist: decode ed25519 signature: %w", err)
	}
	if !bundlesig.Verify(c, sig, pin.Ed25519Pub) {
		return ErrBadSignature
	}
	return nil
}
