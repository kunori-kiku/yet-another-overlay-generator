package controller

// login_challenge.go mints the single-use random nonces behind operator PASSKEY login
// (plan-5.2). A passkey login proves possession of a registered WebAuthn credential by
// signing a challenge the controller issued — the RANDOM-challenge sibling of the
// keystone, whose challenge is instead the CONTENT hash of the membership manifest.
//
// The discipline mirrors enrollment tokens (enrollment.go): 32 bytes of CSPRNG entropy,
// base64url (no padding) for transport, and HASH-not-plaintext at rest — the Store keeps
// only ChallengeHash, so a store read cannot recover a usable challenge. Unlike an
// enrollment token (scoped to a NodeID), a login challenge is scoped to an operator
// USERNAME and carries a short TTL; ConsumeLoginChallenge burns it atomically.

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"time"
)

// loginChallengeBytes is the number of crypto/rand bytes behind a login challenge: 32
// bytes (256 bits) makes the nonce unguessable. It is base64url-encoded for the browser
// (which feeds it to navigator.credentials.get) and hashed for storage.
const loginChallengeBytes = 32

// NewLoginChallenge mints a fresh single-use login challenge for an operator.
//
// It returns the plaintext challenge (base64url, no padding — returned to the browser to
// drive navigator.credentials.get) and the LoginChallenge record (to be persisted via
// Store.CreateLoginChallenge). The plaintext is never stored: only lc.ChallengeHash (hex
// SHA-256 of the base64url string) lives in the Store, so a store read cannot recover a
// usable challenge. The caller persists lc and returns the plaintext; this function
// performs no I/O.
//
// It panics if the system CSPRNG fails — the same loud-failure contract as
// NewEnrollmentToken: a security nonce cannot be safely minted without entropy.
func NewLoginChallenge(operator string, ttl time.Duration, now time.Time) (challenge string, lc LoginChallenge) {
	raw := make([]byte, loginChallengeBytes)
	if _, err := rand.Read(raw); err != nil {
		panic(fmt.Sprintf("controller: system CSPRNG failed generating login challenge: %v", err))
	}
	challenge = base64.RawURLEncoding.EncodeToString(raw)
	lc = LoginChallenge{
		ChallengeHash: HashToken(challenge),
		Operator:      operator,
		ExpiresAt:     now.Add(ttl),
	}
	return challenge, lc
}
