package controller

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// TestValidateOperatorUsername: accepts the conservative charset, rejects empties,
// path-traversal sentinels, separators, control/non-ASCII, and over-long names.
func TestValidateOperatorUsername(t *testing.T) {
	good := []string{"admin", "alice.bob", "op_1", "a-b-c", "X"}
	for _, u := range good {
		if err := ValidateOperatorUsername(u); err != nil {
			t.Errorf("ValidateOperatorUsername(%q) = %v, want nil", u, err)
		}
	}
	bad := []string{"", ".", "..", "a/b", "a b", "a\x00b", "naïve", strings.Repeat("x", maxOperatorUsernameLen+1)}
	for _, u := range bad {
		if err := ValidateOperatorUsername(u); err == nil {
			t.Errorf("ValidateOperatorUsername(%q) = nil, want error", u)
		}
	}
}

// TestNewOperatorRejectsShortPassword: NewOperator enforces the minimum length.
func TestNewOperatorRejectsShortPassword(t *testing.T) {
	if _, err := NewOperator("admin", "short", time.Now()); err == nil {
		t.Error("expected an error for a too-short password")
	}
}

// TestNewOperatorAndSession: NewOperator produces a verifiable hash, and NewSession's
// stored hash equals HashToken(plaintext).
func TestNewOperatorAndSession(t *testing.T) {
	op, err := NewOperator("admin", "a-good-password", time.Unix(100, 0).UTC())
	if err != nil {
		t.Fatalf("NewOperator: %v", err)
	}
	if op.Username != "admin" {
		t.Errorf("Username = %q", op.Username)
	}
	ok, err := VerifyPassword(op.PasswordHash, "a-good-password")
	if err != nil || !ok {
		t.Errorf("operator password verify: ok=%v err=%v", ok, err)
	}

	plaintext, sess := NewSession("admin", time.Hour, time.Unix(100, 0).UTC())
	if HashToken(plaintext) != sess.TokenHash {
		t.Error("session TokenHash != HashToken(plaintext)")
	}
	if sess.Operator != "admin" {
		t.Errorf("session Operator = %q, want admin", sess.Operator)
	}
	if !sess.ExpiresAt.Equal(time.Unix(100, 0).UTC().Add(time.Hour)) {
		t.Errorf("session ExpiresAt = %v", sess.ExpiresAt)
	}
}

// TestStoreOperatorRoundTrip: PutOperator/GetOperator/ListOperators/DeleteOperator on
// both Store impls.
func TestStoreOperatorRoundTrip(t *testing.T) {
	for _, impl := range storeImpls() {
		impl := impl
		t.Run(impl.name, func(t *testing.T) {
			ctx := context.Background()
			s := impl.factory(t)
			const tn = TenantID("t1")

			if _, err := s.GetOperator(ctx, tn, "admin"); !errors.Is(err, ErrNotFound) {
				t.Fatalf("GetOperator(absent) = %v, want ErrNotFound", err)
			}
			op := Operator{Username: "admin", PasswordHash: "phc-admin", CreatedAt: time.Unix(1, 0).UTC()}
			if err := s.PutOperator(ctx, tn, op); err != nil {
				t.Fatalf("PutOperator: %v", err)
			}
			got, err := s.GetOperator(ctx, tn, "admin")
			if err != nil {
				t.Fatalf("GetOperator: %v", err)
			}
			if got.Username != "admin" || got.PasswordHash != "phc-admin" {
				t.Errorf("round-trip mismatch: %+v", got)
			}
			if err := s.PutOperator(ctx, tn, Operator{Username: "bob", PasswordHash: "phc-bob"}); err != nil {
				t.Fatalf("PutOperator(bob): %v", err)
			}
			ops, err := s.ListOperators(ctx, tn)
			if err != nil {
				t.Fatalf("ListOperators: %v", err)
			}
			if len(ops) != 2 || ops[0].Username != "admin" || ops[1].Username != "bob" {
				t.Fatalf("ListOperators = %+v, want sorted [admin bob]", ops)
			}
			if err := s.DeleteOperator(ctx, tn, "bob"); err != nil {
				t.Fatalf("DeleteOperator: %v", err)
			}
			if _, err := s.GetOperator(ctx, tn, "bob"); !errors.Is(err, ErrNotFound) {
				t.Fatalf("GetOperator(deleted) = %v, want ErrNotFound", err)
			}
			// DeleteOperator is idempotent.
			if err := s.DeleteOperator(ctx, tn, "bob"); err != nil {
				t.Fatalf("DeleteOperator(idempotent): %v", err)
			}
		})
	}
}

// TestStoreSessionRoundTripAndExpiry: CreateSession/LookupSession/DeleteSession on
// both Store impls, including expiry (ErrTokenInvalid + lazy delete).
func TestStoreSessionRoundTripAndExpiry(t *testing.T) {
	for _, impl := range storeImpls() {
		impl := impl
		t.Run(impl.name, func(t *testing.T) {
			ctx := context.Background()
			s := impl.factory(t)
			const tn = TenantID("t1")
			base := time.Date(2026, 6, 8, 12, 0, 0, 0, time.UTC)

			if _, err := s.LookupSession(ctx, tn, tokenHash("nope"), base); !errors.Is(err, ErrTokenInvalid) {
				t.Fatalf("LookupSession(absent) = %v, want ErrTokenInvalid", err)
			}
			sess := Session{TokenHash: tokenHash("s1"), Operator: "admin", CreatedAt: base, ExpiresAt: base.Add(time.Hour)}
			if err := s.CreateSession(ctx, tn, sess); err != nil {
				t.Fatalf("CreateSession: %v", err)
			}
			got, err := s.LookupSession(ctx, tn, tokenHash("s1"), base.Add(30*time.Minute))
			if err != nil {
				t.Fatalf("LookupSession(valid): %v", err)
			}
			if got.Operator != "admin" {
				t.Errorf("session Operator = %q, want admin", got.Operator)
			}
			// At/after ExpiresAt -> ErrTokenInvalid (and lazily deleted).
			if _, err := s.LookupSession(ctx, tn, tokenHash("s1"), base.Add(2*time.Hour)); !errors.Is(err, ErrTokenInvalid) {
				t.Fatalf("LookupSession(expired) = %v, want ErrTokenInvalid", err)
			}
			// After the lazy delete, even a before-expiry lookup is gone.
			if _, err := s.LookupSession(ctx, tn, tokenHash("s1"), base.Add(30*time.Minute)); !errors.Is(err, ErrTokenInvalid) {
				t.Fatalf("LookupSession(after lazy delete) = %v, want ErrTokenInvalid", err)
			}
			// Explicit delete + idempotency.
			if err := s.CreateSession(ctx, tn, Session{TokenHash: tokenHash("s2"), Operator: "bob", ExpiresAt: base.Add(time.Hour)}); err != nil {
				t.Fatalf("CreateSession(s2): %v", err)
			}
			if err := s.DeleteSession(ctx, tn, tokenHash("s2")); err != nil {
				t.Fatalf("DeleteSession: %v", err)
			}
			if _, err := s.LookupSession(ctx, tn, tokenHash("s2"), base); !errors.Is(err, ErrTokenInvalid) {
				t.Fatalf("LookupSession(deleted) = %v, want ErrTokenInvalid", err)
			}
			if err := s.DeleteSession(ctx, tn, tokenHash("s2")); err != nil {
				t.Fatalf("DeleteSession(idempotent): %v", err)
			}
		})
	}
}
