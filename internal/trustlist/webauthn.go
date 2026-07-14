package trustlist

import (
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
)

// authDataMinLen is the minimum WebAuthn authenticatorData length: 32 bytes
// rpIdHash + 1 byte flags + 4 bytes signCount = 37. We bounds-check before
// indexing any of those fields.
const authDataMinLen = 37

// flagUserPresent is the User-Present (UP) bit in authenticatorData flags. We
// require it. We deliberately do NOT gate on the signature counter
// (authData[33:37]) because synced passkeys legitimately emit a counter of 0.
const flagUserPresent = 0x01

// flagUserVerified is the User-Verified (UV) bit. We require it too: both ceremonies pin
// userVerification:"required" client-side, so the SERVER — the sole enforcement authority — must
// enforce UV. A User-Present-only assertion otherwise degrades the declared "user-verified" factor
// to mere possession. Unlike the counter, a UV-capable authenticator DOES set this under UV=required.
const flagUserVerified = 0x04

// verifyWebAuthn verifies a WebAuthn (FIDO2) assertion over a trust list, stdlib
// only, and binds the assertion to THIS trust list's CONTENT: the expected challenge
// is Challenge(tl) = SHA256(Canonical(tl)). It is a thin content-bound wrapper over
// verifyAssertion (the challenge-agnostic core); the operator passkey LOGIN path
// reuses that same core through VerifyAssertion with a server-issued RANDOM nonce.
func verifyWebAuthn(tl TrustList, art SignedTrustList, pin PinnedCredential) error {
	// CONTENT BINDING: the challenge the assertion must carry is base64url(Challenge(tl)),
	// proving the user authorized these exact trust-list bytes (not a random nonce).
	chal, err := Challenge(tl)
	if err != nil {
		return err
	}
	return verifyAssertion(art, pin, chal)
}

// VerifyAssertion verifies a WebAuthn (FIDO2) assertion against a pinned credential and
// an EXPECTED challenge — the RAW challenge bytes; clientDataJSON.challenge must equal
// their base64url encoding. It is the challenge-agnostic sibling of the keystone path,
// exported for callers whose challenge is NOT content-bound: operator passkey LOGIN,
// where the challenge is a server-issued, single-use RANDOM nonce rather than the
// manifest hash. The structural, relying-party, user-presence, and signature checks are
// byte-for-byte identical to the keystone verifier; ONLY the source of the expected
// challenge differs.
//
// Like Verify, dispatch is on pin.Alg and an art.Alg != pin.Alg is rejected
// (ErrAlgMismatch) before any cryptography. Only the WebAuthn algorithms are accepted: a
// raw (non-WebAuthn) Ed25519 pin has no assertion to verify and is rejected as
// unsupported here.
func VerifyAssertion(art SignedTrustList, pin PinnedCredential, challenge []byte) error {
	if art.Alg != pin.Alg {
		return fmt.Errorf("%w: pinned=%q artifact=%q", ErrAlgMismatch, pin.Alg, art.Alg)
	}
	switch pin.Alg {
	case AlgWebAuthnES256, AlgWebAuthnEdDSA:
		return verifyAssertion(art, pin, challenge)
	default:
		return fmt.Errorf("%w: %q (passkey login requires a WebAuthn credential)", ErrUnsupportedAlg, pin.Alg)
	}
}

// AssertionChallenge extracts the base64url challenge string embedded in a WebAuthn
// assertion's clientDataJSON. A caller that issued a RANDOM server-side challenge (e.g.
// operator passkey login) uses it as the lookup key for the single-use challenge record
// it stored, then passes the DECODED bytes to VerifyAssertion. The keystone path does
// NOT need this — its expected challenge is recomputed from the manifest, never looked
// up. It does no signature work; it only parses the (still-to-be-verified) client data.
func AssertionChallenge(art SignedTrustList) (string, error) {
	cData, err := base64.RawURLEncoding.DecodeString(art.ClientDataJSON)
	if err != nil {
		return "", fmt.Errorf("trustlist: decode client_data_json: %w", err)
	}
	var cd struct {
		Challenge string `json:"challenge"`
	}
	if err := json.Unmarshal(cData, &cd); err != nil {
		return "", fmt.Errorf("trustlist: parse client_data_json: %w", err)
	}
	return cd.Challenge, nil
}

// verifyAssertion is the challenge-agnostic WebAuthn assertion verifier, stdlib only.
// The caller supplies the EXPECTED challenge bytes (wantChallenge); everything else is
// shared between the content-bound keystone path (wantChallenge = Challenge(tl)) and the
// random-nonce login path (wantChallenge = the issued nonce).
//
// Security-critical sequence (every failure is fail-closed):
//  1. Decode authenticatorData and clientDataJSON from base64url. Note: the
//     EXACT received clientDataJSON bytes are hashed in step 8 — we never
//     re-marshal the parsed struct, because re-marshaling could change bytes and
//     break the signature binding (and would let a forger smuggle extra fields).
//  2. Bounds-check authenticatorData length (>= 37) before indexing.
//  3. Parse clientDataJSON for type / challenge / origin.
//  4. type must be "webauthn.get" (an assertion, not a registration).
//  5. challenge must equal base64url(wantChallenge) — the binding to the exact bytes
//     the verifier expected (content hash for the keystone, random nonce for login).
//  6. rpIdHash (authData[0:32]) must equal sha256(pin.RPID).
//  7. User-Present flag must be set.
//  8. signedMessage = authenticatorData || sha256(clientDataJSON).
//  9. Decode the signature from base64url.
//  10. Verify per pinned alg: ES256 -> ecdsa.VerifyASN1 over sha256(signed);
//     EdDSA -> ed25519.Verify over signed directly (Ed25519 signs the message,
//     not a pre-hash). Anything else (RS256, etc.) is rejected.
//
// pin.Origin, when non-empty, is checked against clientDataJSON.origin as an
// ADVISORY measure. It is not authoritative on a node (a node cannot prove which
// browser origin a user used months earlier), so a mismatch is reported but the
// origin check is documented as advisory; the content binding (step 5) is the
// real authority.
func verifyAssertion(art SignedTrustList, pin PinnedCredential, wantChallenge []byte) error {
	// 0. Pin config precondition: an empty RPID makes the rpIdHash check meaningless
	//    (sha256("") is a known constant), silently disabling relying-party binding.
	//    A keystone anchor must have a real RPID — reject fail-closed.
	if pin.RPID == "" {
		return fmt.Errorf("trustlist: webauthn pin has empty RPID (relying-party binding disabled)")
	}

	// 1. Decode the assertion components.
	authData, err := base64.RawURLEncoding.DecodeString(art.AuthenticatorData)
	if err != nil {
		return fmt.Errorf("trustlist: decode authenticator_data: %w", err)
	}
	cData, err := base64.RawURLEncoding.DecodeString(art.ClientDataJSON)
	if err != nil {
		return fmt.Errorf("trustlist: decode client_data_json: %w", err)
	}

	// 2. Bounds-check before indexing rpIdHash / flags.
	if len(authData) < authDataMinLen {
		return fmt.Errorf("trustlist: authenticator_data too short: %d bytes, want >= %d", len(authData), authDataMinLen)
	}

	// 3. Parse the client data. We only need these three fields; extra fields are
	//    ignored for parsing but remain part of the hashed bytes (step 8).
	var cd struct {
		Type      string `json:"type"`
		Challenge string `json:"challenge"`
		Origin    string `json:"origin"`
	}
	if err := json.Unmarshal(cData, &cd); err != nil {
		return fmt.Errorf("trustlist: parse client_data_json: %w", err)
	}

	// 4. Must be an assertion ("get"), not a registration ("create").
	if cd.Type != "webauthn.get" {
		return fmt.Errorf("trustlist: client_data type = %q, want \"webauthn.get\"", cd.Type)
	}

	// 5. CHALLENGE BINDING: challenge must be base64url(wantChallenge).
	want := base64.RawURLEncoding.EncodeToString(wantChallenge)
	if cd.Challenge != want {
		return ErrChallengeMismatch
	}

	// 6. rpIdHash must equal sha256(pin.RPID).
	wantRP := sha256.Sum256([]byte(pin.RPID))
	gotRP := authData[0:32]
	if !constEqual(gotRP, wantRP[:]) {
		return fmt.Errorf("trustlist: rpIdHash mismatch for RPID %q", pin.RPID)
	}

	// 7. User-Present flag must be set. (Counter authData[33:37] is intentionally
	//    NOT checked — synced passkeys emit 0.)
	if authData[32]&flagUserPresent == 0 {
		return fmt.Errorf("trustlist: User-Present flag not set in authenticator_data")
	}

	// 7b. User-Verified flag must ALSO be set — the ONE gate shared by operator login, 2FA, and
	//     keystone off-host signing (webauthn.ts runAssertion pins userVerification:"required" for
	//     all). The server is the sole enforcement authority; a User-Present-ONLY assertion (a PIN-less
	//     authenticator, or a tampered client requesting UV=discouraged) would otherwise mint a full
	//     operator session / sign a manifest with mere "touch", collapsing the "user-verified" factor
	//     to possession. A UV-capable authenticator sets this under UV=required, so honest ceremonies
	//     are unaffected. (This gate ALSO runs node-side in VerifyMembership — the operator's manifest
	//     signature must carry UV — so a live fleet's existing manifests must have been UV-signed.)
	if authData[32]&flagUserVerified == 0 {
		return ErrUserVerification
	}

	// Advisory origin check (not authoritative on a node).
	if pin.Origin != "" && cd.Origin != pin.Origin {
		return fmt.Errorf("trustlist: client_data origin = %q, want %q (advisory)", cd.Origin, pin.Origin)
	}

	// 8. signedMessage = authenticatorData || SHA-256(clientDataJSON). Use the
	//    EXACT received cData bytes, never a re-marshal.
	cHash := sha256.Sum256(cData)
	signed := make([]byte, 0, len(authData)+len(cHash))
	signed = append(signed, authData...)
	signed = append(signed, cHash[:]...)

	// 9. Decode the assertion signature.
	sig, err := base64.RawURLEncoding.DecodeString(art.Signature)
	if err != nil {
		return fmt.Errorf("trustlist: decode webauthn signature: %w", err)
	}

	// 10. Verify per the PINNED algorithm.
	switch pin.Alg {
	case AlgWebAuthnES256:
		// Re-assert the pin shape at the verify site (defense in depth): a nil key or
		// nil/non-P-256 curve would panic inside ecdsa.VerifyASN1. ParseES256Pin already
		// enforces P-256, but a pin constructed by other means must still fail closed.
		if pin.ES256Pub == nil || pin.ES256Pub.Curve != elliptic.P256() {
			return ErrMissingPin
		}
		// ES256 = ECDSA P-256 with SHA-256; the signature is DER (ASN.1) over
		// SHA-256(signedMessage).
		digest := sha256.Sum256(signed)
		if !ecdsa.VerifyASN1(pin.ES256Pub, digest[:], sig) {
			return ErrBadSignature
		}
		return nil
	case AlgWebAuthnEdDSA:
		// Require the exact key size: ed25519.Verify PANICS on a wrong-length public
		// key (vs returning false), so a malformed pin must fail closed here, not crash.
		if len(pin.Ed25519Pub) != ed25519.PublicKeySize {
			return ErrMissingPin
		}
		// Ed25519 signs the message directly (it hashes internally); do NOT
		// pre-hash signedMessage.
		if !ed25519.Verify(pin.Ed25519Pub, signed, sig) {
			return ErrBadSignature
		}
		return nil
	default:
		// RS256 or any other algorithm is explicitly excluded.
		return fmt.Errorf("%w: webauthn alg %q", ErrUnsupportedAlg, pin.Alg)
	}
}

// constEqual is a constant-time byte-slice comparison wrapper kept local to
// avoid pulling crypto/subtle into the public surface; both inputs here are
// non-secret hashes, but constant-time is a harmless good habit.
func constEqual(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	var v byte
	for i := range a {
		v |= a[i] ^ b[i]
	}
	return v == 0
}
