package trustlist

import (
	"crypto/ecdsa"
	"crypto/ed25519"
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

// verifyWebAuthn verifies a WebAuthn (FIDO2) assertion over a trust list,
// stdlib only, and binds the assertion to THIS trust list's content.
//
// Security-critical sequence (every failure is fail-closed):
//  1. Decode authenticatorData and clientDataJSON from base64url. Note: the
//     EXACT received clientDataJSON bytes are hashed in step 8 — we never
//     re-marshal the parsed struct, because re-marshaling could change bytes and
//     break the signature binding (and would let a forger smuggle extra fields).
//  2. Bounds-check authenticatorData length (>= 37) before indexing.
//  3. Parse clientDataJSON for type / challenge / origin.
//  4. type must be "webauthn.get" (an assertion, not a registration).
//  5. challenge must equal base64url(Challenge(tl)) — the CONTENT BINDING that
//     proves the user authorized these exact trust-list bytes.
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
func verifyWebAuthn(tl TrustList, art SignedTrustList, pin PinnedCredential) error {
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

	// 5. CONTENT BINDING: challenge must be base64url(Challenge(tl)).
	chal, err := Challenge(tl)
	if err != nil {
		return err
	}
	want := base64.RawURLEncoding.EncodeToString(chal)
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
		if pin.ES256Pub == nil {
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
		if len(pin.Ed25519Pub) == 0 {
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
