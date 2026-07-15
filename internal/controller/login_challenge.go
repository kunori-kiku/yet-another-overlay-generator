package controller

// login_challenge.go mints the single-use random nonces behind operator PASSKEY login
// (plan-5.2) and browser WebAuthn enrollment proof. A passkey assertion proves possession
// of a credential by signing a challenge the controller issued — the RANDOM-challenge
// sibling of the keystone, whose signing challenge is instead the CONTENT hash of the
// membership manifest.
//
// The discipline mirrors enrollment tokens (enrollment.go): 32 bytes of CSPRNG entropy,
// base64url (no padding) for transport, and HASH-not-plaintext at rest — the Store keeps
// only ChallengeHash, so a store read cannot recover a usable challenge. Unlike an
// enrollment token (scoped to a NodeID), this challenge is scoped through its Subject
// field and carries a short TTL; normal login uses the username directly, while credential
// enrollment uses a synthesized purpose+actor subject. ConsumeAssertionChallenge burns it
// atomically.

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"time"
)

// AssertionChallengeBytes is the number of crypto/rand bytes behind an assertion challenge: 32
// bytes (256 bits) makes the nonce unguessable. It is base64url-encoded for the browser
// (which feeds it to navigator.credentials.get) and hashed for storage.
const AssertionChallengeBytes = 32

// NewAssertionChallenge mints a fresh single-use assertion challenge for a subject.
//
// It returns the plaintext challenge (base64url, no padding — returned to the browser to
// drive navigator.credentials.get) and the AssertionChallenge record (to be persisted via
// Store.CreateAssertionChallenge). The plaintext is never stored: only record.ChallengeHash (hex
// SHA-256 of the base64url string) lives in the Store, so a store read cannot recover a
// usable challenge. The caller persists the record and returns the plaintext; this function
// performs no I/O.
//
// It panics if the system CSPRNG fails — the same loud-failure contract as
// NewEnrollmentToken: a security nonce cannot be safely minted without entropy.
func NewAssertionChallenge(subject string, ttl time.Duration, now time.Time) (challenge string, record AssertionChallenge) {
	raw := make([]byte, AssertionChallengeBytes)
	if _, err := rand.Read(raw); err != nil {
		panic(fmt.Sprintf("controller: system CSPRNG failed generating assertion challenge: %v", err))
	}
	challenge = base64.RawURLEncoding.EncodeToString(raw)
	record = AssertionChallenge{
		ChallengeHash: HashToken(challenge),
		Subject:       subject,
		ExpiresAt:     now.Add(ttl),
	}
	return challenge, record
}
