package controller

// login_challenge_test.go exercises the shared assertion-challenge store contract on BOTH
// Store impls: single-use consumption, subject scoping, expiry, bounded enrollment replacement,
// atomic burn under concurrency, and deep-copying an Operator's *LoginCredential (no aliasing).

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestAssertionChallengeSingleUseAndScope(t *testing.T) {
	for _, impl := range storeImpls() {
		impl := impl
		t.Run(impl.name, func(t *testing.T) {
			ctx := context.Background()
			s := impl.factory(t)
			const tn = TenantID("t1")
			now := time.Unix(1_700_000_000, 0).UTC()

			challenge, lc := NewAssertionChallenge("admin", 5*time.Minute, now)
			// The plaintext is base64url; ONLY its hash is stored.
			if lc.ChallengeHash != HashToken(challenge) {
				t.Fatalf("ChallengeHash != HashToken(challenge)")
			}
			if lc.Subject != "admin" {
				t.Fatalf("unexpected challenge record: %+v", lc)
			}
			if err := s.CreateAssertionChallenge(ctx, tn, lc, now); err != nil {
				t.Fatalf("CreateAssertionChallenge: %v", err)
			}

			// Wrong operator -> invalid (scope check), and it does NOT burn the challenge.
			if err := s.ConsumeAssertionChallenge(ctx, tn, lc.ChallengeHash, "mallory", now); !errors.Is(err, ErrChallengeInvalid) {
				t.Fatalf("wrong-subject consume = %v, want ErrChallengeInvalid", err)
			}
			// Right operator -> ok (single use; the record is deleted).
			if err := s.ConsumeAssertionChallenge(ctx, tn, lc.ChallengeHash, "admin", now); err != nil {
				t.Fatalf("consume = %v, want nil", err)
			}
			// Replay -> the consumed challenge was deleted, so it is now simply invalid.
			if err := s.ConsumeAssertionChallenge(ctx, tn, lc.ChallengeHash, "admin", now); !errors.Is(err, ErrChallengeInvalid) {
				t.Fatalf("replay consume = %v, want ErrChallengeInvalid", err)
			}
			// Unknown hash -> invalid.
			if err := s.ConsumeAssertionChallenge(ctx, tn, "deadbeef", "admin", now); !errors.Is(err, ErrChallengeInvalid) {
				t.Fatalf("unknown consume = %v, want ErrChallengeInvalid", err)
			}

			// Expired -> invalid (consumed after ExpiresAt).
			_, lc2 := NewAssertionChallenge("admin", time.Minute, now)
			if err := s.CreateAssertionChallenge(ctx, tn, lc2, now.Add(2*time.Minute)); err != nil {
				t.Fatalf("CreateAssertionChallenge(2): %v", err)
			}
			if err := s.ConsumeAssertionChallenge(ctx, tn, lc2.ChallengeHash, "admin", now.Add(2*time.Minute)); !errors.Is(err, ErrChallengeInvalid) {
				t.Fatalf("expired consume = %v, want ErrChallengeInvalid", err)
			}
		})
	}
}

// TestConsumeAssertionChallengeAtomic proves the single-use assertion burn is atomic: many concurrent
// consumers of the SAME challenge admit EXACTLY ONE (so a captured assertion cannot be
// replayed and two concurrent logins cannot both win). Run under -race. Both stores.
func TestConsumeAssertionChallengeAtomic(t *testing.T) {
	for _, impl := range storeImpls() {
		impl := impl
		t.Run(impl.name, func(t *testing.T) {
			ctx := context.Background()
			s := impl.factory(t)
			const tn = TenantID("t1")
			now := time.Unix(1_700_000_000, 0).UTC()
			_, lc := NewAssertionChallenge("admin", 5*time.Minute, now)
			if err := s.CreateAssertionChallenge(ctx, tn, lc, now); err != nil {
				t.Fatalf("CreateAssertionChallenge: %v", err)
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
					if err := s.ConsumeAssertionChallenge(ctx, tn, lc.ChallengeHash, "admin", now); err == nil {
						atomic.AddInt64(&won, 1)
					}
				}()
			}
			close(start)
			wg.Wait()
			if won != 1 {
				t.Fatalf("concurrent ConsumeAssertionChallenge: %d winners, want exactly 1", won)
			}
		})
	}
}

// TestReplaceAssertionChallengeForSubject pins the enrollment-specific lifecycle: one live challenge
// per actor+purpose, with other actors/purposes independent. This bounds repeated or cancelled
// browser prompts without changing ordinary login's concurrent-challenge behavior.
func TestReplaceAssertionChallengeForSubject(t *testing.T) {
	for _, impl := range storeImpls() {
		impl := impl
		t.Run(impl.name, func(t *testing.T) {
			ctx := context.Background()
			s := impl.factory(t)
			const tn = TenantID("replace")
			now := time.Unix(1_700_000_000, 0).UTC()

			_, first := NewAssertionChallenge("webauthn-enrollment:login:admin", 10*time.Minute, now)
			_, other := NewAssertionChallenge("webauthn-enrollment:keystone:admin", 10*time.Minute, now)
			_, second := NewAssertionChallenge("webauthn-enrollment:login:admin", 10*time.Minute, now.Add(time.Second))
			if err := s.ReplaceAssertionChallengeForSubject(ctx, tn, first, now); err != nil {
				t.Fatalf("first replace: %v", err)
			}
			if err := s.ReplaceAssertionChallengeForSubject(ctx, tn, other, now); err != nil {
				t.Fatalf("other-purpose replace: %v", err)
			}
			if err := s.ReplaceAssertionChallengeForSubject(ctx, tn, second, now.Add(time.Second)); err != nil {
				t.Fatalf("second replace: %v", err)
			}

			if err := s.ConsumeAssertionChallenge(ctx, tn, first.ChallengeHash, first.Subject, now.Add(2*time.Second)); !errors.Is(err, ErrChallengeInvalid) {
				t.Fatalf("replaced challenge consume = %v, want ErrChallengeInvalid", err)
			}
			if err := s.ConsumeAssertionChallenge(ctx, tn, second.ChallengeHash, second.Subject, now.Add(2*time.Second)); err != nil {
				t.Fatalf("new challenge consume: %v", err)
			}
			if err := s.ConsumeAssertionChallenge(ctx, tn, other.ChallengeHash, other.Subject, now.Add(2*time.Second)); err != nil {
				t.Fatalf("other purpose was disturbed: %v", err)
			}
		})
	}
}

// TestAssertionChallengeCreatePurgesExpiredRecords inspects the shared storage port so the assertion is
// physical, not merely "expired records are rejected". This specifically protects FileStore from
// accumulating one abandoned JSON file per cancelled browser prompt forever.
func TestAssertionChallengeCreatePurgesExpiredRecords(t *testing.T) {
	for _, impl := range storeImpls() {
		impl := impl
		t.Run(impl.name, func(t *testing.T) {
			ctx := context.Background()
			s := impl.factory(t)
			const tn = TenantID("purge")
			now := time.Unix(1_700_000_000, 0).UTC()
			_, expired := NewAssertionChallenge("old", time.Minute, now)
			if err := s.CreateAssertionChallenge(ctx, tn, expired, now); err != nil {
				t.Fatalf("create expired candidate: %v", err)
			}
			_, live := NewAssertionChallenge("live", time.Minute, now.Add(2*time.Minute))
			if err := s.CreateAssertionChallenge(ctx, tn, live, now.Add(2*time.Minute)); err != nil {
				t.Fatalf("create live: %v", err)
			}

			var core *storeCore
			switch concrete := s.(type) {
			case *MemStore:
				core = concrete.storeCore
			case *FileStore:
				core = concrete.storeCore
			default:
				t.Fatalf("unexpected store %T", s)
			}
			var recs []kvRecord
			if err := core.kv.withLock(func() error {
				var err error
				recs, err = core.kv.list(tn, collLoginChal)
				return err
			}); err != nil {
				t.Fatalf("list challenges: %v", err)
			}
			if len(recs) != 1 || recs[0].key != live.ChallengeHash {
				t.Fatalf("stored challenges = %+v, want only live %s", recs, live.ChallengeHash)
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
