package controller

// operator.go holds the operator-account and operator-session constructors for the
// login flow (plan-5.2). The Operator/Session record types live in store.go beside
// the other persisted records; the password primitive is in password.go.

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"time"
)

// DefaultSessionTTL is the lifetime of an operator login session. A logged-in panel
// re-authenticates after this window. It is deliberately modest (a panel session is
// a standing admin credential); a deployment may shorten it.
const DefaultSessionTTL = 12 * time.Hour

// minPasswordLen is the hard floor for an operator password. argon2id makes each
// guess expensive, but a too-short password is still weak; 8 is the OWASP minimum.
// (Length, not composition rules, is what matters — long passphrases are encouraged.)
const minPasswordLen = 8

// maxOperatorUsernameLen bounds a username (also a FileStore path component).
const maxOperatorUsernameLen = 64

// ValidateOperatorUsername rejects an empty, over-long, or unsafe operator username.
// The username is both the audit actor and a FileStore path component
// (operators/<username>.json), so it is restricted to a conservative charset
// (letters, digits, '.', '_', '-') and the path-traversal sentinels "." / ".." are
// rejected outright.
//
// Usernames are CASE-SENSITIVE (a map key in MemStore, a filename in FileStore). The
// controller targets Linux, where the FileStore lives on a case-sensitive filesystem;
// on a case-folding filesystem (macOS/Windows) "Admin" and "admin" would collide on
// disk. That is out of the supported deployment surface — see operator-auth.md.
func ValidateOperatorUsername(username string) error {
	if username == "" {
		return errors.New("controller: operator username must not be empty")
	}
	if len(username) > maxOperatorUsernameLen {
		return fmt.Errorf("controller: operator username too long (max %d characters)", maxOperatorUsernameLen)
	}
	if username == "." || username == ".." {
		return fmt.Errorf("controller: operator username %q is not allowed", username)
	}
	for _, r := range username {
		ok := (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') ||
			(r >= '0' && r <= '9') || r == '.' || r == '_' || r == '-'
		if !ok {
			return fmt.Errorf("controller: operator username %q has an invalid character (allowed: letters, digits, '.', '_', '-')", username)
		}
	}
	return nil
}

// NewOperator builds an Operator from a username and plaintext password, hashing the
// password with argon2id (HashPassword). The plaintext is consumed here and never
// returned or stored. It validates the username and enforces minPasswordLen.
func NewOperator(username, password string, now time.Time) (Operator, error) {
	if err := ValidateOperatorUsername(username); err != nil {
		return Operator{}, err
	}
	if len(password) < minPasswordLen {
		return Operator{}, fmt.Errorf("controller: password must be at least %d characters", minPasswordLen)
	}
	hash, err := HashPassword(password)
	if err != nil {
		return Operator{}, err
	}
	return Operator{
		Username:     username,
		PasswordHash: hash,
		CreatedAt:    now,
		UpdatedAt:    now,
	}, nil
}

// SeedOperator is the shared write path behind `yaog-server create-operator` and the
// e2e harness's operator seed: it builds the argon2id-hashed account (NewOperator,
// which validates the username + password floor) and writes it to the store for tenant
// t. Both call sites therefore produce a byte-identical account, so the harness logs in
// against exactly the credential the production bootstrap would create.
//
// PutOperator is a blind upsert, so SeedOperator OVERWRITES any existing account for
// username (the ephemeral e2e store wants idempotent re-seeding). A caller that must NOT
// clobber an existing account — `create-operator` without --force — guards with
// GetOperator before calling this (the guard, the password prompt, and the --force flag
// are CLI-specific and stay in runCreateOperator).
func SeedOperator(ctx context.Context, store Store, t TenantID, username, password string, now time.Time) error {
	op, err := NewOperator(username, password, now)
	if err != nil {
		return err
	}
	return store.PutOperator(ctx, t, op)
}

// NewSession mints a fresh operator session for operator. It returns the plaintext
// bearer token (returned to the browser exactly once, held in memory there) and the
// Session record (persisted via Store.CreateSession; it carries only the token's
// hash). It mirrors NewNodeAPIToken's entropy/encoding: 32 bytes of crypto/rand,
// base64url (no padding), hashed with HashToken for storage.
//
// It panics if the system CSPRNG fails (the same fail-loud contract as the other
// token minters: a session secret cannot be derived without entropy).
func NewSession(operator string, ttl time.Duration, now time.Time) (plaintext string, s Session) {
	raw := make([]byte, enrollTokenBytes)
	if _, err := rand.Read(raw); err != nil {
		panic(fmt.Sprintf("controller: system CSPRNG failed generating session token: %v", err))
	}
	plaintext = base64.RawURLEncoding.EncodeToString(raw)
	s = Session{
		TokenHash: HashToken(plaintext),
		Operator:  operator,
		CreatedAt: now,
		ExpiresAt: now.Add(ttl),
	}
	return plaintext, s
}
