package controller

// password.go is the operator-login password primitive (plan-5.2): argon2id
// hashing + constant-time verification, stdlib + golang.org/x/crypto/argon2 only.
//
// The plaintext password is NEVER stored. An operator account holds only a
// self-describing argon2id PHC string ($argon2id$v=19$m=..,t=..,p=..$salt$hash), so
// the parameters can be raised later without invalidating existing hashes, and a
// store/DB read can only recover the hash — not a usable password.
//
// Parameters are at/above the OWASP Password Storage Cheat Sheet floor for argon2id
// (m=19 MiB, t=2, p=1). We pick a stronger m=64 MiB, t=3, p=1: the controller has a
// tiny operator set and very low login concurrency, so a ~0.1–0.3 s verify is
// affordable and meaningfully raises GPU-cracking cost. argon2id resists both
// side-channel (argon2i) and GPU/TMTO (argon2d) attacks, the recommended default.

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"

	"golang.org/x/crypto/argon2"
)

// argon2id cost parameters. Stored inside each PHC string, so changing these only
// affects NEWLY created hashes; existing hashes verify against their own embedded
// parameters (VerifyPassword reads them back from the PHC string).
const (
	argon2Time    uint32 = 3         // iterations
	argon2Memory  uint32 = 64 * 1024 // KiB => 64 MiB
	argon2Threads uint8  = 1         // lanes (single-lane: low-concurrency server)
	argon2KeyLen  uint32 = 32        // derived-key length in bytes
	argon2SaltLen        = 16        // random salt length in bytes
)

// ErrMalformedPasswordHash is returned by VerifyPassword when the stored PHC string
// cannot be parsed (corrupted record). It is fail-closed: a malformed hash never
// verifies any password.
var ErrMalformedPasswordHash = errors.New("controller: malformed argon2id password hash")

// HashPassword derives an argon2id hash of password with a fresh random salt and
// returns it as a self-describing PHC string. It panics only if the system CSPRNG
// fails (the same fail-loud contract as the token minters: a security secret cannot
// be derived without entropy).
func HashPassword(password string) (string, error) {
	salt := make([]byte, argon2SaltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("controller: csprng for password salt: %w", err)
	}
	hash := argon2.IDKey([]byte(password), salt, argon2Time, argon2Memory, argon2Threads, argon2KeyLen)
	return encodePHC(salt, hash, argon2Memory, argon2Time, argon2Threads), nil
}

// VerifyPassword reports whether password matches the argon2id PHC string phc. It
// re-derives the key using the parameters and salt embedded in phc and compares in
// constant time. A malformed PHC string returns (false, ErrMalformedPasswordHash) —
// it never panics and never reports a match.
func VerifyPassword(phc, password string) (bool, error) {
	memory, time32, threads, salt, want, err := parsePHC(phc)
	if err != nil {
		return false, err
	}
	got := argon2.IDKey([]byte(password), salt, time32, memory, threads, uint32(len(want)))
	return subtle.ConstantTimeCompare(got, want) == 1, nil
}

// dummySalt is a fixed, non-secret salt used only by DummyVerifyPassword to burn an
// equivalent amount of argon2 work for an unknown user, so a login endpoint does not
// become a username oracle via timing. It protects nothing — it only equalizes time.
var dummySalt = []byte("yaog.dummy.salt!")

// DummyVerifyPassword performs the same argon2id derivation a real verify would, then
// discards the result. Callers run it on the unknown-user branch of a login so the
// response time does not reveal whether the username exists.
func DummyVerifyPassword(password string) {
	_ = argon2.IDKey([]byte(password), dummySalt, argon2Time, argon2Memory, argon2Threads, argon2KeyLen)
}

// encodePHC renders the standard argon2 PHC string. Salt and hash use base64
// RawStdEncoding (no padding), matching the argon2 reference encoding.
func encodePHC(salt, hash []byte, memory, time32 uint32, threads uint8) string {
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, memory, time32, threads,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(hash),
	)
}

// parsePHC parses an argon2id PHC string into its parameters, salt, and hash. It is
// strict: an unexpected field count, algorithm, version, parameter shape, or base64
// is ErrMalformedPasswordHash (fail-closed).
func parsePHC(phc string) (memory, time32 uint32, threads uint8, salt, hash []byte, err error) {
	// "$argon2id$v=19$m=65536,t=3,p=1$<salt>$<hash>" splits into 6 fields, the first
	// empty (the string starts with '$').
	parts := strings.Split(phc, "$")
	if len(parts) != 6 || parts[0] != "" || parts[1] != "argon2id" {
		return 0, 0, 0, nil, nil, ErrMalformedPasswordHash
	}
	var version int
	if _, e := fmt.Sscanf(parts[2], "v=%d", &version); e != nil || version != argon2.Version {
		return 0, 0, 0, nil, nil, ErrMalformedPasswordHash
	}
	if _, e := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &memory, &time32, &threads); e != nil {
		return 0, 0, 0, nil, nil, ErrMalformedPasswordHash
	}
	if salt, err = base64.RawStdEncoding.DecodeString(parts[4]); err != nil {
		return 0, 0, 0, nil, nil, ErrMalformedPasswordHash
	}
	if hash, err = base64.RawStdEncoding.DecodeString(parts[5]); err != nil {
		return 0, 0, 0, nil, nil, ErrMalformedPasswordHash
	}
	if len(salt) == 0 || len(hash) == 0 {
		return 0, 0, 0, nil, nil, ErrMalformedPasswordHash
	}
	return memory, time32, threads, salt, hash, nil
}
