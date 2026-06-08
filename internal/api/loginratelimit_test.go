package api

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TestLoginLimiterLockoutAndWindow: the atomic gate admits exactly maxLoginFailures
// attempts, reports the lockout transition on the threshold attempt, rejects the next
// (with a positive Retry-After), re-admits once the window elapses, and succeed()
// refunds a key.
func TestLoginLimiterLockoutAndWindow(t *testing.T) {
	l := newLoginLimiter()
	now := time.Date(2026, 6, 8, 12, 0, 0, 0, time.UTC)
	const key = "user:alice"

	// Attempts 1..max-1: admitted, not the lockout transition.
	for i := 1; i < maxLoginFailures; i++ {
		allowed, justLocked, _ := l.registerAttempt(now, key)
		if !allowed || justLocked {
			t.Fatalf("attempt %d: allowed=%v justLocked=%v, want true,false", i, allowed, justLocked)
		}
	}
	// The threshold attempt: admitted AND flagged as the lockout transition.
	allowed, justLocked, _ := l.registerAttempt(now, key)
	if !allowed || !justLocked {
		t.Fatalf("threshold attempt: allowed=%v justLocked=%v, want true,true", allowed, justLocked)
	}
	// The next attempt is rejected with a positive, bounded Retry-After.
	allowed, _, retry := l.registerAttempt(now, key)
	if allowed {
		t.Fatal("expected rejection after the threshold")
	}
	if retry <= 0 || retry > loginWindow {
		t.Fatalf("retry = %v, want 0 < retry <= window", retry)
	}
	// Once the window elapses, the key is admitted again (fresh window).
	if allowed, _, _ := l.registerAttempt(now.Add(loginWindow+time.Second), key); !allowed {
		t.Fatal("still rejected after the window elapsed")
	}
	// succeed() refunds the key so it is admitted again.
	l.succeed(key)
	if allowed, _, _ := l.registerAttempt(now, key); !allowed {
		t.Fatal("rejected after succeed() refunded the key")
	}
}

// TestLoginLimiterCapIsHard: at most maxLoginFailures attempts are ever admitted for a
// key before the window resets — the check-and-reserve gate has no overshoot.
func TestLoginLimiterCapIsHard(t *testing.T) {
	l := newLoginLimiter()
	now := time.Date(2026, 6, 8, 12, 0, 0, 0, time.UTC)
	const key = "user:alice"
	admitted := 0
	for i := 0; i < maxLoginFailures*3; i++ {
		if allowed, _, _ := l.registerAttempt(now, key); allowed {
			admitted++
		}
	}
	if admitted != maxLoginFailures {
		t.Fatalf("admitted %d attempts, want exactly %d", admitted, maxLoginFailures)
	}
}

// TestLoginLimiterCapIsHardConcurrent proves the check-and-reserve gate has no
// overshoot UNDER CONCURRENCY — the actual TOCTOU the atomic registerAttempt closed.
// Many goroutines hammering one key in one window admit EXACTLY maxLoginFailures in
// total; the old non-atomic blocked()+fail() pair would overshoot here. Run under
// -race to also assert there is no data race in the gate.
func TestLoginLimiterCapIsHardConcurrent(t *testing.T) {
	l := newLoginLimiter()
	now := time.Date(2026, 6, 8, 12, 0, 0, 0, time.UTC)
	const key = "user:alice"
	const goroutines = 100

	var admitted int64
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start // line everyone up to maximize contention on the gate
			if allowed, _, _ := l.registerAttempt(now, key); allowed {
				atomic.AddInt64(&admitted, 1)
			}
		}()
	}
	close(start)
	wg.Wait()

	if admitted != int64(maxLoginFailures) {
		t.Fatalf("admitted %d attempts under concurrency, want exactly %d", admitted, maxLoginFailures)
	}
}

// TestLoginLimiterKeysIndependent: locking one key does not affect another (so a
// per-username lockout does not lock a different user, and vice versa).
func TestLoginLimiterKeysIndependent(t *testing.T) {
	l := newLoginLimiter()
	now := time.Date(2026, 6, 8, 12, 0, 0, 0, time.UTC)
	for i := 0; i < maxLoginFailures; i++ {
		l.registerAttempt(now, "user:a")
	}
	if allowed, _, _ := l.registerAttempt(now, "user:a"); allowed {
		t.Fatal("user:a should be locked out")
	}
	if allowed, _, _ := l.registerAttempt(now, "user:b"); !allowed {
		t.Fatal("user:b should be independent of user:a")
	}
}
