package controller

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
)

// TestCompareAndSetOperatorCredentialContract runs the keystone transition primitive against both
// stores. A handler may classify outside the store lock, but only the exact snapshot it classified
// is allowed to commit; a concurrent transition must never become last-write-wins.
func TestCompareAndSetOperatorCredentialContract(t *testing.T) {
	for _, impl := range storeImpls() {
		impl := impl
		t.Run(impl.name, func(t *testing.T) {
			ctx := context.Background()
			s := impl.factory(t)
			const tenant = TenantID("cas")
			a := OperatorCredential{Alg: "ed25519", PublicKeyPEM: "a"}
			b := OperatorCredential{Alg: "ed25519", PublicKeyPEM: "b"}
			c := OperatorCredential{Alg: "ed25519", PublicKeyPEM: "c"}

			if err := s.CompareAndSetOperatorCredential(ctx, tenant, nil, a); err != nil {
				t.Fatalf("first CAS: %v", err)
			}
			if err := s.CompareAndSetOperatorCredential(ctx, tenant, nil, b); !errors.Is(err, ErrOperatorCredentialChanged) {
				t.Fatalf("second nil-expected CAS = %v, want ErrOperatorCredentialChanged", err)
			}
			if err := s.CompareAndSetOperatorCredential(ctx, tenant, &a, b); err != nil {
				t.Fatalf("A -> B CAS: %v", err)
			}
			if err := s.CompareAndSetOperatorCredential(ctx, tenant, &a, c); !errors.Is(err, ErrOperatorCredentialChanged) {
				t.Fatalf("stale A -> C CAS = %v, want ErrOperatorCredentialChanged", err)
			}
			got, err := s.GetOperatorCredential(ctx, tenant)
			if err != nil {
				t.Fatalf("GetOperatorCredential: %v", err)
			}
			if got != b {
				t.Fatalf("failed stale CAS mutated credential: got %+v, want %+v", got, b)
			}
			// An idempotent compare-only call succeeds without weakening the expectation check.
			if err := s.CompareAndSetOperatorCredential(ctx, tenant, &b, b); err != nil {
				t.Fatalf("compare-only CAS: %v", err)
			}
		})
	}
}

// TestCompareAndSetOperatorCredentialConcurrentFirstPin proves two simultaneous TOFU pins cannot
// both win. This is the store-level race that the old Get+Set handler sequence left open.
func TestCompareAndSetOperatorCredentialConcurrentFirstPin(t *testing.T) {
	for _, impl := range storeImpls() {
		impl := impl
		t.Run(impl.name, func(t *testing.T) {
			ctx := context.Background()
			s := impl.factory(t)
			const tenant = TenantID("cas-race")
			candidates := []OperatorCredential{
				{Alg: "ed25519", PublicKeyPEM: "a"},
				{Alg: "ed25519", PublicKeyPEM: "b"},
			}
			start := make(chan struct{})
			var wg sync.WaitGroup
			var wins atomic.Int32
			var conflicts atomic.Int32
			for _, candidate := range candidates {
				candidate := candidate
				wg.Add(1)
				go func() {
					defer wg.Done()
					<-start
					err := s.CompareAndSetOperatorCredential(ctx, tenant, nil, candidate)
					switch {
					case err == nil:
						wins.Add(1)
					case errors.Is(err, ErrOperatorCredentialChanged):
						conflicts.Add(1)
					default:
						t.Errorf("CAS: %v", err)
					}
				}()
			}
			close(start)
			wg.Wait()
			if wins.Load() != 1 || conflicts.Load() != 1 {
				t.Fatalf("wins=%d conflicts=%d, want 1/1", wins.Load(), conflicts.Load())
			}
			got, err := s.GetOperatorCredential(ctx, tenant)
			if err != nil {
				t.Fatalf("GetOperatorCredential: %v", err)
			}
			if got != candidates[0] && got != candidates[1] {
				t.Fatalf("stored non-candidate credential: %+v", got)
			}
		})
	}
}
