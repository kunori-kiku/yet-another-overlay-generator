package controller

// totp.go is a stdlib-only RFC 6238 TOTP implementation for OPTIONAL operator-login
// two-factor (plan-5.2). It is HMAC-SHA1 (the algorithm every authenticator app
// defaults to) + RFC 4226 dynamic truncation — no third-party dependency.
//
// SCOPE: TOTP gates the PANEL login only. It is NOT a signing mechanism and must never
// be used for the keystone trust-list signature: TOTP is SYMMETRIC (the controller
// stores the same secret to verify, so a breached controller could forge codes) and
// produces a time-based code, not an asymmetric content-bound signature. Off-host
// keystone signing requires a passkey (incl. a synced/Bitwarden passkey — no hardware
// needed) or an off-host Ed25519 key. See docs/spec/controller/operator-auth.md.
//
// HONEST LIMIT: the TOTP shared secret is symmetric, so it is stored at rest (unlike a
// passkey, where only the public key is stored). A store breach reveals TOTP secrets.
// TOTP is a convenience second factor; a passkey is strictly stronger.

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1"
	"crypto/subtle"
	"encoding/base32"
	"encoding/binary"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	totpDigits      = 6
	totpPeriod      = 30 // seconds per step
	totpSecretBytes = 20 // 160-bit secret (RFC 4226 recommends >= 128 bits)
	totpSkewSteps   = 1  // accept the adjacent step on each side for clock drift
)

// totpEnc is the base32 alphabet authenticator apps use (RFC 4648, no padding).
var totpEnc = base32.StdEncoding.WithPadding(base32.NoPadding)

// GenerateTOTPSecret returns a fresh base32 TOTP shared secret. It panics only if the
// system CSPRNG fails (the same fail-loud contract as the token minters).
func GenerateTOTPSecret() string {
	raw := make([]byte, totpSecretBytes)
	if _, err := rand.Read(raw); err != nil {
		panic(fmt.Sprintf("controller: system CSPRNG failed generating TOTP secret: %v", err))
	}
	return totpEnc.EncodeToString(raw)
}

// TOTPCode returns the 6-digit code for secret at time t — the value an authenticator
// app would display. Exposed for tooling and tests; verification uses VerifyTOTP.
func TOTPCode(secret string, t time.Time) (string, error) {
	return totpCodeForStep(secret, uint64(t.Unix()/totpPeriod))
}

// totpCodeForStep computes the zero-padded 6-digit code for a step counter.
func totpCodeForStep(secret string, step uint64) (string, error) {
	key, err := totpEnc.DecodeString(strings.ToUpper(strings.TrimSpace(secret)))
	if err != nil {
		return "", fmt.Errorf("controller: decode TOTP secret: %w", err)
	}
	var msg [8]byte
	binary.BigEndian.PutUint64(msg[:], step)
	mac := hmac.New(sha1.New, key)
	mac.Write(msg[:])
	sum := mac.Sum(nil)
	// RFC 4226 dynamic truncation.
	offset := sum[len(sum)-1] & 0x0f
	bin := (uint32(sum[offset]&0x7f) << 24) |
		(uint32(sum[offset+1]) << 16) |
		(uint32(sum[offset+2]) << 8) |
		uint32(sum[offset+3])
	return fmt.Sprintf("%0*d", totpDigits, bin%1_000_000), nil
}

// VerifyTOTP reports whether code is valid for secret around time t, accepting the
// adjacent step on each side (clock drift), and REJECTS any step at or before lastStep
// (replay protection — a code already used cannot be reused). It returns (ok, step):
// the caller persists step as the new lastStep on success. The comparison is
// constant-time. A malformed secret or wrong-length code returns (false, 0).
func VerifyTOTP(secret, code string, t time.Time, lastStep int64) (ok bool, step int64) {
	code = strings.TrimSpace(code)
	if len(code) != totpDigits {
		return false, 0
	}
	current := t.Unix() / totpPeriod
	for delta := int64(-totpSkewSteps); delta <= totpSkewSteps; delta++ {
		s := current + delta
		if s <= lastStep {
			continue // already used (replay) or older than the last accepted code
		}
		want, err := totpCodeForStep(secret, uint64(s))
		if err != nil {
			return false, 0
		}
		if subtle.ConstantTimeCompare([]byte(want), []byte(code)) == 1 {
			return true, s
		}
	}
	return false, 0
}

// TOTPProvisioningURI builds the otpauth:// URI an authenticator app imports (shown as
// text and/or a QR). issuer/account label the entry in the app.
func TOTPProvisioningURI(secret, account, issuer string) string {
	label := url.PathEscape(issuer) + ":" + url.PathEscape(account)
	q := url.Values{}
	q.Set("secret", secret)
	q.Set("issuer", issuer)
	q.Set("algorithm", "SHA1")
	q.Set("digits", strconv.Itoa(totpDigits))
	q.Set("period", strconv.Itoa(totpPeriod))
	return "otpauth://totp/" + label + "?" + q.Encode()
}
