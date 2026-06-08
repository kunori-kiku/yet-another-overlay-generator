package api

import (
	"testing"
	"time"
)

// TestLoginLimiterLockoutAndWindow: a key is not blocked until it crosses the
// threshold, the crossing failure reports the lockout transition, and the block
// clears once the window elapses; succeed() resets a key.
func TestLoginLimiterLockoutAndWindow(t *testing.T) {
	l := newLoginLimiter()
	now := time.Date(2026, 6, 8, 12, 0, 0, 0, time.UTC)
	const key = "user:alice"

	if b, _ := l.blocked(now, key); b {
		t.Fatal("blocked before any failure")
	}
	// Failures 1..max-1: no lockout, not blocked.
	for i := 1; i < maxLoginFailures; i++ {
		if locked := l.fail(now, key); locked {
			t.Fatalf("unexpected lockout transition at failure %d", i)
		}
	}
	if b, _ := l.blocked(now, key); b {
		t.Fatal("blocked before reaching the threshold")
	}
	// The threshold-th failure triggers the lockout transition.
	if locked := l.fail(now, key); !locked {
		t.Fatal("expected lockout transition at the threshold failure")
	}
	b, retry := l.blocked(now, key)
	if !b || retry <= 0 || retry > loginWindow {
		t.Fatalf("expected blocked with 0 < retry <= window, got b=%v retry=%v", b, retry)
	}
	// Once the window elapses, the key is no longer blocked.
	if b, _ := l.blocked(now.Add(loginWindow+time.Second), key); b {
		t.Fatal("still blocked after the window elapsed")
	}
	// A success clears the record.
	l.fail(now, key)
	l.succeed(key)
	if b, _ := l.blocked(now, key); b {
		t.Fatal("blocked after succeed() cleared the key")
	}
}

// TestLoginLimiterKeysIndependent: locking one key does not affect another (so a
// per-username lockout does not lock a different user, and vice versa).
func TestLoginLimiterKeysIndependent(t *testing.T) {
	l := newLoginLimiter()
	now := time.Date(2026, 6, 8, 12, 0, 0, 0, time.UTC)
	for i := 0; i < maxLoginFailures; i++ {
		l.fail(now, "user:a")
	}
	if b, _ := l.blocked(now, "user:a"); !b {
		t.Fatal("user:a should be locked out")
	}
	if b, _ := l.blocked(now, "user:b"); b {
		t.Fatal("user:b should be independent of user:a")
	}
}
