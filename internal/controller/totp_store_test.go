package controller

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
)

// TestAdvanceTOTPStepAtomic proves the replay watermark is advanced atomically: many
// concurrent calls with the SAME step admit EXACTLY ONE (closing the login TOCTOU), an
// equal/older step is refused, a strictly greater step advances, and an absent operator
// is ErrNotFound. Run under -race. Both Store impls.
func TestAdvanceTOTPStepAtomic(t *testing.T) {
	for _, impl := range storeImpls() {
		impl := impl
		t.Run(impl.name, func(t *testing.T) {
			ctx := context.Background()
			s := impl.factory(t)
			const tn = TenantID("t1")
			if err := s.PutOperator(ctx, tn, Operator{Username: "admin", PasswordHash: "x"}); err != nil {
				t.Fatalf("PutOperator: %v", err)
			}

			const step = int64(100)
			const goroutines = 50
			var won int64
			var wg sync.WaitGroup
			start := make(chan struct{})
			for i := 0; i < goroutines; i++ {
				wg.Add(1)
				go func() {
					defer wg.Done()
					<-start
					if ok, _ := s.AdvanceTOTPStep(ctx, tn, "admin", step); ok {
						atomic.AddInt64(&won, 1)
					}
				}()
			}
			close(start)
			wg.Wait()
			if won != 1 {
				t.Fatalf("concurrent AdvanceTOTPStep(step=%d): %d winners, want exactly 1", step, won)
			}

			// Re-advancing to the same (already-consumed) step is refused.
			if ok, err := s.AdvanceTOTPStep(ctx, tn, "admin", step); err != nil || ok {
				t.Errorf("re-advance to same step: ok=%v err=%v, want false,nil", ok, err)
			}
			// A strictly greater step advances.
			if ok, err := s.AdvanceTOTPStep(ctx, tn, "admin", step+1); err != nil || !ok {
				t.Errorf("advance to step+1: ok=%v err=%v, want true,nil", ok, err)
			}
			// An absent operator is ErrNotFound.
			if _, err := s.AdvanceTOTPStep(ctx, tn, "ghost", 1); !errors.Is(err, ErrNotFound) {
				t.Errorf("AdvanceTOTPStep(ghost) err = %v, want ErrNotFound", err)
			}
		})
	}
}
