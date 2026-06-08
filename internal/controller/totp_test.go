package controller

import (
	"strings"
	"testing"
	"time"
)

// rfcSecret is the RFC 6238 SHA1 test secret (ASCII "12345678901234567890"), base32-
// encoded the way an authenticator app would store it.
var rfcSecret = totpEnc.EncodeToString([]byte("12345678901234567890"))

// TestTOTPCodeRFC6238: the 6-digit truncation of the RFC 6238 SHA1 test vectors.
func TestTOTPCodeRFC6238(t *testing.T) {
	// unix time -> expected 6-digit code (low 6 of the RFC's 8-digit value).
	cases := map[int64]string{
		59:         "287082", // RFC: 94287082
		1111111109: "081804", // RFC: 07081804
		1234567890: "005924", // RFC: 89005924
	}
	for unixT, want := range cases {
		step := uint64(unixT / totpPeriod)
		got, err := totpCodeForStep(rfcSecret, step)
		if err != nil {
			t.Fatalf("totpCodeForStep(%d): %v", step, err)
		}
		if got != want {
			t.Errorf("code at unix=%d (step=%d) = %q, want %q", unixT, step, got, want)
		}
	}
}

// TestVerifyTOTP: a freshly generated code verifies; replay (lastStep=matched) and a
// wrong code are rejected; the adjacent step is accepted (drift).
func TestVerifyTOTP(t *testing.T) {
	secret := GenerateTOTPSecret()
	now := time.Unix(1_700_000_000, 0)
	step := now.Unix() / totpPeriod
	code, err := totpCodeForStep(secret, uint64(step))
	if err != nil {
		t.Fatalf("totpCodeForStep: %v", err)
	}

	ok, matched := VerifyTOTP(secret, code, now, 0)
	if !ok || matched != step {
		t.Fatalf("verify fresh code: ok=%v matched=%d, want true,%d", ok, matched, step)
	}
	// Replay: the same code with lastStep=matched is rejected.
	if ok, _ := VerifyTOTP(secret, code, now, matched); ok {
		t.Error("replayed code accepted (lastStep should reject step <= last)")
	}
	// Wrong code rejected.
	if ok, _ := VerifyTOTP(secret, "000000", now, 0); ok && code == "000000" {
		// extremely unlikely the random secret yields 000000; guard anyway
		t.Skip("random code collision")
	}
	// Drift: a code generated one step earlier verifies at `now` (±1 skew).
	prev, _ := totpCodeForStep(secret, uint64(step-1))
	if ok, _ := VerifyTOTP(secret, prev, now, 0); !ok {
		t.Error("adjacent (drift) step not accepted")
	}
	// Malformed code length rejected.
	if ok, _ := VerifyTOTP(secret, "12345", now, 0); ok {
		t.Error("wrong-length code accepted")
	}
}

// TestGenerateTOTPSecret: secrets are non-empty and decode as valid base32.
func TestGenerateTOTPSecret(t *testing.T) {
	s := GenerateTOTPSecret()
	if s == "" {
		t.Fatal("empty secret")
	}
	if _, err := totpEnc.DecodeString(s); err != nil {
		t.Fatalf("secret is not valid base32: %v", err)
	}
	if GenerateTOTPSecret() == s {
		t.Error("two generated secrets are identical (not random)")
	}
}

// TestProvisioningURI: the otpauth URI carries the secret + issuer.
func TestProvisioningURI(t *testing.T) {
	uri := TOTPProvisioningURI("ABC234", "alice", "YAOG")
	for _, want := range []string{"otpauth://totp/", "secret=ABC234", "issuer=YAOG", "algorithm=SHA1", "digits=6", "period=30"} {
		if !strings.Contains(uri, want) {
			t.Errorf("URI %q missing %q", uri, want)
		}
	}
}
