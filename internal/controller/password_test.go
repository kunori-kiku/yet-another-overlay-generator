package controller

import (
	"strings"
	"testing"
)

// TestHashAndVerifyPassword: a fresh hash verifies the correct password and rejects
// a wrong one, and the PHC string carries the expected argon2id parameters.
func TestHashAndVerifyPassword(t *testing.T) {
	const pw = "correct horse battery staple"
	phc, err := HashPassword(pw)
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	if !strings.HasPrefix(phc, "$argon2id$v=19$m=65536,t=3,p=1$") {
		t.Errorf("unexpected PHC prefix: %q", phc)
	}
	ok, err := VerifyPassword(phc, pw)
	if err != nil || !ok {
		t.Errorf("verify correct password: ok=%v err=%v", ok, err)
	}
	ok, err = VerifyPassword(phc, "wrong password")
	if err != nil {
		t.Errorf("verify wrong password returned err: %v", err)
	}
	if ok {
		t.Error("a wrong password verified as true")
	}
}

// TestHashPasswordUniqueSalt: two hashes of the same password differ (random salt).
func TestHashPasswordUniqueSalt(t *testing.T) {
	a, err := HashPassword("same-password")
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	b, err := HashPassword("same-password")
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	if a == b {
		t.Error("two hashes of the same password are identical (salt is not random)")
	}
}

// TestVerifyPasswordMalformed: a malformed PHC fails closed (false + error), never a
// match and never a panic.
func TestVerifyPasswordMalformed(t *testing.T) {
	bad := []string{
		"",
		"notaphc",
		"$argon2id$v=19$m=65536,t=3,p=1$onlyfivefields",          // too few fields
		"$argon2i$v=19$m=65536,t=3,p=1$c2FsdHNhbHQ$aGFzaGhhc2g",  // wrong algorithm
		"$argon2id$v=18$m=65536,t=3,p=1$c2FsdHNhbHQ$aGFzaGhhc2g", // wrong version
		"$argon2id$v=19$m=bad,t=3,p=1$c2FsdHNhbHQ$aGFzaGhhc2g",   // unparsable params
		"$argon2id$v=19$m=65536,t=3,p=1$!!!notb64$aGFzaGhhc2g",   // bad base64 salt
		"$argon2id$v=19$m=65536,t=3,p=1$c2FsdHNhbHQ$!!!notb64",   // bad base64 hash
	}
	for _, phc := range bad {
		ok, err := VerifyPassword(phc, "whatever")
		if ok {
			t.Errorf("malformed PHC %q verified true", phc)
		}
		if err == nil {
			t.Errorf("malformed PHC %q: expected an error", phc)
		}
	}
}

// TestDummyVerifyPasswordNoPanic: the unknown-user timing-equalizer never panics.
func TestDummyVerifyPasswordNoPanic(t *testing.T) {
	DummyVerifyPassword("anything at all")
}
