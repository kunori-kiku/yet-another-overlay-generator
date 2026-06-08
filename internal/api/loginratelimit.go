package api

// loginratelimit.go is the in-process throttle for operator logins (plan-5.2). A
// password endpoint introduces an online brute-force surface that the random per-node
// bearer tokens never had; argon2id makes each guess expensive (~0.1–0.3 s) but an
// attacker parallelizes, so a guess-count limiter is still required.
//
// The limiter tracks failures per KEY in a fixed window and blocks a key once it
// exceeds a threshold, until the window elapses. It is applied to BOTH the username
// and the source IP: per-username stops credential stuffing against one account;
// per-IP stops one source spraying many usernames. State is in-process and ephemeral
// (rate-limit state need not survive a restart) and pruned lazily.
//
// Proxy caveat: the source IP is taken from r.RemoteAddr. Behind a reverse proxy
// that is the proxy's IP, so per-IP limiting collapses to one bucket for all clients;
// a deployment fronting the controller should forward the real client IP (and/or rate-
// limit at the proxy). Per-username limiting is unaffected.

import (
	"net"
	"net/http"
	"sync"
	"time"
)

// Login throttle parameters. After maxLoginFailures failures for a key within
// loginWindow, that key is blocked for the remainder of the window. The window is
// both the counting window and the lockout duration.
const (
	maxLoginFailures = 10
	loginWindow      = 15 * time.Minute
	// loginLimiterSweepAt bounds the tracked-key map: when it grows past this, a full
	// sweep drops expired records so a random-username/IP spray cannot grow it without
	// bound.
	loginLimiterSweepAt = 4096
)

// attemptRecord counts failures for one key within the current window.
type attemptRecord struct {
	count       int
	windowStart time.Time
}

// loginLimiter is a mutex-guarded per-key failure counter with lockout.
type loginLimiter struct {
	mu       sync.Mutex
	attempts map[string]*attemptRecord
	max      int
	window   time.Duration
}

// newLoginLimiter returns a limiter with the default thresholds.
func newLoginLimiter() *loginLimiter {
	return &loginLimiter{
		attempts: make(map[string]*attemptRecord),
		max:      maxLoginFailures,
		window:   loginWindow,
	}
}

// registerAttempt is the single atomic check-and-reserve gate for one login attempt.
// Under one lock acquisition it (a) reports whether any key is already locked out
// and, if so, rejects WITHOUT counting (allowed=false + the longest remaining lockout
// for Retry-After); otherwise (b) reserves a slot by incrementing each key's count and
// returns allowed=true, with justLocked=true when this attempt tipped a key to exactly
// the threshold (the transition the caller audits once).
//
// Counting at the GATE — not after the (slow, unlocked) argon2 verify — closes the
// check-then-record TOCTOU: N concurrent requests serialize through this lock, so at
// most maxLoginFailures of them are ever admitted before the rest are rejected. A
// SUCCESSFUL login refunds its reservation via succeed(), so the steady-state count
// tracks failures; a key whose window has elapsed restarts at 1.
func (l *loginLimiter) registerAttempt(now time.Time, keys ...string) (allowed, justLocked bool, retryAfter time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.attempts) > loginLimiterSweepAt {
		l.pruneLocked(now)
	}

	// Pass 1: is any key already locked out within its live window?
	var blocked bool
	for _, k := range keys {
		rec := l.attempts[k]
		if rec == nil {
			continue
		}
		elapsed := now.Sub(rec.windowStart)
		if elapsed >= l.window {
			continue // stale; reset on increment below
		}
		if rec.count >= l.max {
			blocked = true
			if remain := l.window - elapsed; remain > retryAfter {
				retryAfter = remain
			}
		}
	}
	if blocked {
		return false, false, retryAfter
	}

	// Pass 2: reserve a slot on each key (resetting a stale window to a fresh count).
	for _, k := range keys {
		rec := l.attempts[k]
		if rec == nil || now.Sub(rec.windowStart) >= l.window {
			l.attempts[k] = &attemptRecord{count: 1, windowStart: now}
			rec = l.attempts[k]
		} else {
			rec.count++
		}
		if rec.count == l.max {
			justLocked = true
		}
	}
	return true, justLocked, 0
}

// succeed clears the failure records for keys (a successful login refunds its
// reserved slot and resets the count).
func (l *loginLimiter) succeed(keys ...string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, k := range keys {
		delete(l.attempts, k)
	}
}

// pruneLocked drops records whose window has elapsed. The caller must hold l.mu.
func (l *loginLimiter) pruneLocked(now time.Time) {
	for k, rec := range l.attempts {
		if now.Sub(rec.windowStart) >= l.window {
			delete(l.attempts, k)
		}
	}
}

// clientIP returns the source IP of a request (r.RemoteAddr with the port stripped).
// See the proxy caveat at the top of this file.
func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
