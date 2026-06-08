package controller

// login_challenge_test.go exercises the passkey login-challenge store contract on BOTH
// Store impls: single-use consumption, operator scoping, expiry, atomic burn under
// concurrency, and the deep-copy of an Operator's *LoginCredential (no aliasing).

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestLoginChallengeSingleUseAndScope(t *testing.T) {
	for _, impl := range storeImpls() {
		impl := impl
		t.Run(impl.name, func(t *testing.T) {
			ctx := context.Background()
			s := impl.factory(t)
			const tn = TenantID("t1")
			now := time.Unix(1_700_000_000, 0).UTC()

			challenge, lc := NewLoginChallenge("admin", 5*time.Minute, now)
			// The plaintext is base64url; ONLY its hash is stored.
			if lc.ChallengeHash != HashToken(challenge) {
				t.Fatalf("ChallengeHash != HashToken(challenge)")
			}
			if lc.Operator != "admin" || lc.ConsumedAt != nil {
				t.Fatalf("unexpected challenge record: %+v", lc)
			}
			if err := s.CreateLoginChallenge(ctx, tn, lc); err != nil {
				t.Fatalf("CreateLoginChallenge: %v", err)
			}

			// Wrong operator -> invalid (scope check), and it does NOT burn the challenge.
			if err := s.ConsumeLoginChallenge(ctx, tn, lc.ChallengeHash, "mallory", now); !errors.Is(err, ErrChallengeInvalid) {
				t.Fatalf("wrong-operator consume = %v, want ErrChallengeInvalid", err)
			}
			// Right operator -> ok (single use).
			if err := s.ConsumeLoginChallenge(ctx, tn, lc.ChallengeHash, "admin", now); err != nil {
				t.Fatalf("consume = %v, want nil", err)
			}
			// Replay -> already consumed.
			if err := s.ConsumeLoginChallenge(ctx, tn, lc.ChallengeHash, "admin", now); !errors.Is(err, ErrChallengeConsumed) {
				t.Fatalf("replay consume = %v, want ErrChallengeConsumed", err)
			}
			// Unknown hash -> invalid.
			if err := s.ConsumeLoginChallenge(ctx, tn, "deadbeef", "admin", now); !errors.Is(err, ErrChallengeInvalid) {
				t.Fatalf("unknown consume = %v, want ErrChallengeInvalid", err)
			}

			// Expired -> invalid (consumed after ExpiresAt).
			_, lc2 := NewLoginChallenge("admin", time.Minute, now)
			if err := s.CreateLoginChallenge(ctx, tn, lc2); err != nil {
				t.Fatalf("CreateLoginChallenge(2): %v", err)
			}
			if err := s.ConsumeLoginChallenge(ctx, tn, lc2.ChallengeHash, "admin", now.Add(2*time.Minute)); !errors.Is(err, ErrChallengeInvalid) {
				t.Fatalf("expired consume = %v, want ErrChallengeInvalid", err)
			}
		})
	}
}

// TestConsumeLoginChallengeAtomic proves the single-use burn is atomic: many concurrent
// consumers of the SAME challenge admit EXACTLY ONE (so a captured assertion cannot be
// replayed and two concurrent logins cannot both win). Run under -race. Both stores.
func TestConsumeLoginChallengeAtomic(t *testing.T) {
	for _, impl := range storeImpls() {
		impl := impl
		t.Run(impl.name, func(t *testing.T) {
			ctx := context.Background()
			s := impl.factory(t)
			const tn = TenantID("t1")
			now := time.Unix(1_700_000_000, 0).UTC()
			_, lc := NewLoginChallenge("admin", 5*time.Minute, now)
			if err := s.CreateLoginChallenge(ctx, tn, lc); err != nil {
				t.Fatalf("CreateLoginChallenge: %v", err)
			}

			const goroutines = 50
			var won int64
			var wg sync.WaitGroup
			start := make(chan struct{})
			for i := 0; i < goroutines; i++ {
				wg.Add(1)
				go func() {
					defer wg.Done()
					<-start
					if err := s.ConsumeLoginChallenge(ctx, tn, lc.ChallengeHash, "admin", now); err == nil {
						atomic.AddInt64(&won, 1)
					}
				}()
			}
			close(start)
			wg.Wait()
			if won != 1 {
				t.Fatalf("concurrent ConsumeLoginChallenge: %d winners, want exactly 1", won)
			}
		})
	}
}

// TestCloneOperatorNoAlias proves MemStore deep-copies an Operator's *LoginCredential so
// the stored value and a caller's value never share the pointer (FileStore re-marshals
// from disk, so it cannot alias). A store that aliased would let a GetOperator caller
// mutate the stored credential, or a PutOperator caller reach in via a retained pointer.
func TestCloneOperatorNoAlias(t *testing.T) {
	ctx := context.Background()
	s := NewMemStore()
	const tn = TenantID("t1")
	op := Operator{
		Username:        "admin",
		PasswordHash:    "x",
		LoginCredential: &LoginCredential{Alg: "webauthn-eddsa", CredentialID: "c1", RPID: "host"},
	}
	if err := s.PutOperator(ctx, tn, op); err != nil {
		t.Fatalf("PutOperator: %v", err)
	}
	// Mutating the caller's copy AFTER Put must not reach the store.
	op.LoginCredential.CredentialID = "MUTATED"
	got, err := s.GetOperator(ctx, tn, "admin")
	if err != nil {
		t.Fatalf("GetOperator: %v", err)
	}
	if got.LoginCredential.CredentialID != "c1" {
		t.Fatalf("store aliased the caller's pointer: got %q, want c1", got.LoginCredential.CredentialID)
	}
	// Mutating the RETURNED copy must not reach a subsequent Get.
	got.LoginCredential.CredentialID = "MUTATED2"
	got2, _ := s.GetOperator(ctx, tn, "admin")
	if got2.LoginCredential.CredentialID != "c1" {
		t.Fatalf("returned value aliased the store: got %q, want c1", got2.LoginCredential.CredentialID)
	}
}
