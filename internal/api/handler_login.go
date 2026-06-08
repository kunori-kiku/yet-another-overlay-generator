package api

// handler_login.go is the operator-login HTTP surface (plan-5.2): POST /login
// (password -> session) and POST /logout (revoke session). Login replaces the single
// shared operator token as the PRIMARY operator-auth path; the env token remains an
// optional break-glass credential (see operatorAuth in auth_controller.go).

import (
	"context"
	"net/http"
	"strconv"
	"time"

	"github.com/kunorikiku/yet-another-overlay-generator/internal/controller"
)

// loginRequestJSON is the POST /login body: operator username + plaintext password.
// The password is consumed to verify against the stored argon2id hash; it is never
// stored or logged.
type loginRequestJSON struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

// loginResponseJSON is returned on a successful login: the plaintext session bearer
// token (returned ONCE; the controller stores only its hash), the operator identity,
// and the session expiry (RFC3339).
type loginResponseJSON struct {
	SessionToken string `json:"session_token"`
	Operator     string `json:"operator"`
	ExpiresAt    string `json:"expires_at"`
}

// HandleLogin authenticates an operator by username + password and mints a session.
// It is UNAUTHENTICATED (reachable before the operator has a session) and POST only.
//
// Defenses: per-username + per-IP rate limiting (429 + Retry-After on lockout); a
// UNIFORM "invalid username or password" 401 for both unknown-user and wrong-password
// (with a dummy argon2 verify on the unknown-user branch so response timing does not
// reveal which); a 256-bit session token returned once and stored only as a hash.
//
// Transport: this carries a plaintext password, so the deployment MUST front the
// controller with a TLS-terminating proxy (a sniffed password is worse than a sniffed
// scoped token). See docs/spec/controller/operator-auth.md.
func (h *ControllerHandler) HandleLogin(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "only POST is supported")
		return
	}
	var req loginRequestJSON
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	now := time.Now().UTC()
	userKey := "user:" + req.Username
	ipKey := "ip:" + clientIP(r)

	// Rate-limit gate: if either the username or the source IP is locked out, reject
	// before doing any (expensive) password work.
	if locked, retry := h.loginLimiter.blocked(now, userKey, ipKey); locked {
		w.Header().Set("Retry-After", strconv.Itoa(int(retry.Seconds())+1))
		writeError(w, http.StatusTooManyRequests, "too many login attempts; try again later")
		return
	}

	op, err := h.store.GetOperator(r.Context(), h.tenant, req.Username)
	if err != nil {
		// Unknown user: run a dummy argon2 verify so the response time matches the
		// wrong-password branch (no username oracle), then fail uniformly.
		controller.DummyVerifyPassword(req.Password)
		h.failLogin(r.Context(), now, req.Username, userKey, ipKey)
		writeError(w, http.StatusUnauthorized, "invalid username or password")
		return
	}

	ok, err := controller.VerifyPassword(op.PasswordHash, req.Password)
	if err != nil || !ok {
		h.failLogin(r.Context(), now, req.Username, userKey, ipKey)
		writeError(w, http.StatusUnauthorized, "invalid username or password")
		return
	}

	// Success: mint a session, persist it (hash only), clear the limiter for these
	// keys. The audit write is best-effort: login availability must not depend on the
	// audit log (operational visibility only, not a security boundary — see audit.go),
	// and the session is the real artifact.
	plaintext, sess := controller.NewSession(op.Username, h.sessionTTL, now)
	if err := h.store.CreateSession(r.Context(), h.tenant, sess); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create session")
		return
	}
	h.loginLimiter.succeed(userKey, ipKey)
	_, _ = h.store.AppendAudit(r.Context(), h.tenant, controller.AuditEntry{
		Timestamp: now,
		Actor:     "operator:" + op.Username,
		Action:    "login-success",
	})
	writeJSON(w, http.StatusOK, loginResponseJSON{
		SessionToken: plaintext,
		Operator:     op.Username,
		ExpiresAt:    sess.ExpiresAt.Format(time.RFC3339),
	})
}

// failLogin records a failed login against the limiter and, if this failure triggered
// a lockout, appends a single login-lockout audit entry. Individual non-lockout
// failures are intentionally NOT audited: they are bounded only by the limiter, so
// auditing each one would let an attacker grow the audit log; the lockout transition
// is the signal worth recording. The audit write is best-effort.
func (h *ControllerHandler) failLogin(ctx context.Context, now time.Time, username, userKey, ipKey string) {
	if locked := h.loginLimiter.fail(now, userKey, ipKey); locked {
		_, _ = h.store.AppendAudit(ctx, h.tenant, controller.AuditEntry{
			Timestamp: now,
			Actor:     "operator:" + username,
			Action:    "login-lockout",
		})
	}
}

// HandleLogout revokes the presented session. It is authenticated (operatorAuth) and
// POST only, and idempotent: deleting an unknown session hash is a no-op. A logout
// performed with the break-glass token deletes nothing (the token is not a session)
// and still returns 204.
func (h *ControllerHandler) HandleLogout(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "only POST is supported")
		return
	}
	tok, ok := bearerToken(r)
	if !ok {
		// operatorAuth already required a token to reach here; defensive no-op.
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if err := h.store.DeleteSession(r.Context(), h.tenant, controller.HashToken(tok)); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to revoke session")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
