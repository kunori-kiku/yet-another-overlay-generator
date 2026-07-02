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
	"strings"
	"sync"
	"time"
)

// Login throttle parameters. After maxLoginFailures failures for a key within
// loginWindow, that key is blocked for the remainder of the window. The window is
// both the counting window and the lockout duration.
const (
	maxLoginFailures = 10
	loginWindow      = 15 * time.Minute
	// maxEnrollFailures is the per-IP failure cap for /enroll. It is HIGHER than the login cap
	// because the threat models differ: login guards a human password (low-entropy, so a tight
	// cap matters), whereas /enroll guards single-use, high-entropy enrollment tokens where
	// guessing is hopeless regardless. The higher cap accommodates a legitimate parallel
	// bootstrap of many nodes behind one NAT (whose concurrent enrolls all reserve a slot
	// before any refunds) without false 429s, while still bounding a token sprayer.
	maxEnrollFailures = 60
	// loginLimiterSweepAt bounds the tracked-key map: when it grows past this, a full
	// sweep drops expired records so a random-username/IP spray cannot grow it without
	// bound.
	loginLimiterSweepAt = 4096
	// maxNodeRequestsPerWindow / nodeRateWindow bound the REQUEST rate (not failures) per enrolled
	// node across the agent mux (/config, /poll, /report, /telemetry, /rekey), used as a fixed-window
	// limiter (registerAttempt with no succeed()). Keyed by NODE identity (not IP) so one abusive
	// enrolled node cannot force fsync'd/lock-contended controller work fleet-wide, and so the cap
	// survives a reverse-proxy IP collapse and isolates a single node from the rest. 60/min is ~10x a
	// healthy node's steady state (30s heartbeat + long-poll + the occasional report ≈ 5-6/min) and
	// clears every legitimate deploy burst, while capping a flood to ~1 req/s.
	maxNodeRequestsPerWindow = 60
	nodeRateWindow           = 1 * time.Minute
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

// newLimiter returns a limiter with the given failure cap and window.
func newLimiter(max int, window time.Duration) *loginLimiter {
	return &loginLimiter{
		attempts: make(map[string]*attemptRecord),
		max:      max,
		window:   window,
	}
}

// newLoginLimiter returns a limiter with the default operator-login thresholds.
func newLoginLimiter() *loginLimiter {
	return newLimiter(maxLoginFailures, loginWindow)
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

// clientIP returns the source IP to key rate-limits on. By DEFAULT (no trusted proxies configured) it
// is r.RemoteAddr's host and forwarding headers are IGNORED — a directly-connected client can forge
// X-Forwarded-For, so it must never be trusted from an untrusted peer. When trustedProxies is set AND
// the direct peer (RemoteAddr) is within it, the real client is recovered from X-Forwarded-For, walked
// RIGHT-TO-LEFT and skipping trusted-proxy hops so a client-forged left-most entry is never returned;
// it falls back to X-Real-IP (honored only from a trusted peer) and then RemoteAddr. This makes the
// per-IP enroll/login limiters meaningful behind a reverse proxy instead of collapsing to one bucket.
func (h *ControllerHandler) clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	if len(h.trustedProxies) == 0 || !ipInNets(host, h.trustedProxies) {
		return host // untrusted direct peer: never trust forwarding headers
	}
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		parts := strings.Split(xff, ",")
		for i := len(parts) - 1; i >= 0; i-- {
			ip := strings.TrimSpace(parts[i])
			if ip != "" && !ipInNets(ip, h.trustedProxies) {
				return ip // first (rightmost) untrusted hop = the real client at the trusted edge
			}
		}
	}
	if xr := strings.TrimSpace(r.Header.Get("X-Real-IP")); xr != "" {
		return xr // honored only because the direct peer is a trusted proxy
	}
	return host
}

// ipInNets reports whether ipStr parses to an IP contained in any of nets.
func ipInNets(ipStr string, nets []net.IPNet) bool {
	ip := net.ParseIP(ipStr)
	if ip == nil {
		return false
	}
	for i := range nets {
		if nets[i].Contains(ip) {
			return true
		}
	}
	return false
}
