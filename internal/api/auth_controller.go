package api

// auth_controller.go is the authentication and identity chokepoint for the
// networked controller's protected routes. Agent operations use per-node bearer
// tokens; operator operations use named login sessions as the primary path and an
// optional constant-time-compared break-glass bearer as the recovery path.
//
// The pre-auth routes deliberately bypass these wrappers: agent /enroll (authorized
// by a single-use enrollment token) and /bootstrap, plus operator password/passkey
// login endpoints. Route registration in routes_controller.go is the authoritative
// list; every other agent/operator operation is wrapped by requireNode/operatorAuth.
//
// Identity. A node presents "Authorization: Bearer <token>". The middleware hashes
// the token and resolves it through Store.LookupNodeByAPIToken to an approved node.
// Tenant and node are pinned into request context, never accepted as a target in an
// agent request, so a node can act only as itself. Invalid, stale, and revoked node
// credentials intentionally produce the same opaque unauthorized result.
//
// Operator auth accepts a bearer header or the httpOnly session cookie. A
// state-changing cookie request must pass the double-submit CSRF check; explicit
// bearer auth is not ambient and is exempt. The context records session versus
// break-glass auth separately from the display name so a name collision cannot give
// recovery auth access to account-bound TOTP/passkey management.
//
// Transport is plain HTTP. Production must terminate TLS at a reverse proxy because
// node, session, and break-glass bearer credentials are replayable if exposed.

import (
	"context"
	"crypto/subtle"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/kunorikiku/yet-another-overlay-generator/internal/apierr"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/controller"
)

// ctxKey is an unexported context-key type so controller identity values never
// collide with keys from other packages sharing the request context.
type ctxKey int

const (
	ctxKeyTenant ctxKey = iota
	ctxKeyNode
	ctxKeyOperatorAuthKind
)

// operatorAuthKind preserves how an operator request authenticated. The display
// identity alone is not enough: the configured break-glass identity may have the
// same name as a real account, but recovery-token requests must still be barred
// from account-bound TOTP and login-passkey management.
type operatorAuthKind uint8

const (
	operatorAuthUnknown operatorAuthKind = iota
	operatorAuthSession
	operatorAuthBreakGlass
)

// tenantFromCtx returns the tenant pinned onto the request context by the auth
// middleware. The boolean is false if no tenant was set (the request did not pass
// through the middleware) — handlers should treat that as an internal error.
func tenantFromCtx(ctx context.Context) (controller.TenantID, bool) {
	t, ok := ctx.Value(ctxKeyTenant).(controller.TenantID)
	return t, ok
}

// nodeFromCtx returns the node identity pinned onto the request context by the auth
// middleware. For agent routes this is the calling node (resolved from its API
// token); for operator routes it is the operator identity. The boolean is false if
// no node was set.
func nodeFromCtx(ctx context.Context) (string, bool) {
	n, ok := ctx.Value(ctxKeyNode).(string)
	return n, ok
}

func operatorAuthKindFromCtx(ctx context.Context) (operatorAuthKind, bool) {
	kind, ok := ctx.Value(ctxKeyOperatorAuthKind).(operatorAuthKind)
	return kind, ok
}

// authResult carries the parsed, verified identity from a bearer token.
type authResult struct {
	tenant controller.TenantID
	node   string
}

// bearerToken extracts the token from an "Authorization: Bearer <token>" header.
// The boolean is false if the header is absent or not a non-empty Bearer scheme.
func bearerToken(r *http.Request) (string, bool) {
	h := r.Header.Get("Authorization")
	if h == "" {
		return "", false
	}
	const prefix = "Bearer "
	// Scheme match is case-insensitive per RFC 7235; the token itself is opaque.
	if len(h) <= len(prefix) || !strings.EqualFold(h[:len(prefix)], prefix) {
		return "", false
	}
	tok := strings.TrimSpace(h[len(prefix):])
	if tok == "" {
		return "", false
	}
	return tok, true
}

// authenticateNode resolves the caller's per-node bearer token to its node
// identity. It returns the parsed identity, or an HTTP status + message to reject
// with. It enforces:
//
//	401 — no bearer token, or the token does not resolve to an active node.
//
// The lookup is hash-keyed: only the hex SHA-256 of the token is ever compared (the
// Store holds APITokenHash, never plaintext). Per the Store contract,
// LookupNodeByAPIToken returns ErrTokenInvalid whenever the presented hash does not
// resolve to an APPROVED node whose own APITokenHash still matches — i.e. an unmapped
// hash, a stale/rotated hash, or a non-approved (pending/revoked) node all surface as
// the same opaque 401. There is therefore no separate revoked-node branch here: a
// revoked node's token is indistinguishable from any other invalid token, which is
// the desired behaviour (a revoked credential simply stops resolving).
func (h *ControllerHandler) authenticateNode(r *http.Request) (authResult, *apierr.Error) {
	tok, ok := bearerToken(r)
	if !ok {
		return authResult{}, apierr.New(apierr.CodeReqBearerRequired)
	}
	node, err := h.store.LookupNodeByAPIToken(r.Context(), h.tenant, controller.HashToken(tok))
	if err != nil {
		// ErrTokenInvalid covers an unmapped/stale token AND any non-approved node
		// (Store contract); either way the caller is not an authenticated active node.
		return authResult{}, apierr.New(apierr.CodeReqBearerRequired).Wrap(err)
	}
	return authResult{tenant: h.tenant, node: node.NodeID}, nil
}

// requireNode wraps an agent handler: it authenticates the caller via its per-node
// bearer token and injects tenant+node into the context. The wrapped handler reads
// the node from the context — never from the request — so a node can only act as
// itself.
func (h *ControllerHandler) requireNode(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		auth, ae := h.authenticateNode(r)
		if ae != nil {
			writeAPIError(w, ae)
			return
		}
		// Per-node request-rate gate (fixed window, no refund): bound how fast an authenticated node
		// can hit the agent mux so one abusive/compromised node cannot DoS the controller (e.g. a
		// /telemetry flood forcing fsync'd, lock-contended writes). Keyed by node identity, so it
		// survives a reverse-proxy IP collapse and isolates the offending node from the rest.
		if allowed, _, retry := h.nodeLimiter.registerAttempt(time.Now().UTC(), "node:"+auth.node); !allowed {
			w.Header().Set("Retry-After", strconv.Itoa(int(retry.Seconds())+1))
			writeAPIError(w, apierr.New(apierr.CodeNodeRateLimited))
			return
		}
		ctx := context.WithValue(r.Context(), ctxKeyTenant, auth.tenant)
		ctx = context.WithValue(ctx, ctxKeyNode, auth.node)
		next(w, r.WithContext(ctx))
	}
}

// operatorAuth wraps an operator-only handler. It authenticates the bearer token
// as EITHER a valid login session (the primary path, plan-5.2) OR — when configured —
// the break-glass operator token (constant-time compared). A missing token is a 401
// ("who are you"); a present-but-unrecognized token (including a normal node token,
// an expired session, or — when no break-glass token is set — any non-session token)
// is a 403 ("you may not"). It injects the configured tenant and the resolved
// operator identity into the context so the wrapped handlers read a uniform identity.
// A node can never perform an operator action.
func (h *ControllerHandler) operatorAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// Primary: the Authorization Bearer header (session token or break-glass token).
		// Bearer auth is not CSRF-vulnerable (a cross-site form cannot set it), so it is
		// exempt from the CSRF check.
		tok, ok := bearerToken(r)
		if !ok {
			// Fallback: the httpOnly session cookie. Because the browser attaches it
			// AMBIENTLY, a state-changing request on the cookie path must carry a valid
			// double-submit CSRF token (X-CSRF-Token == yaog_csrf cookie). Safe methods
			// (GET/HEAD/OPTIONS) are exempt.
			tok, ok = sessionCookieToken(r)
			if ok && isStateChanging(r.Method) && !csrfValid(r) {
				writeAPIError(w, apierr.New(apierr.CodeReqCSRFInvalid))
				return
			}
		}
		if !ok {
			writeAPIError(w, apierr.New(apierr.CodeReqBearerRequired))
			return
		}
		operator, authKind, ok := h.resolveOperator(r.Context(), tok)
		if !ok {
			writeAPIError(w, apierr.New(apierr.CodeReqOperatorRequired))
			return
		}
		ctx := context.WithValue(r.Context(), ctxKeyTenant, h.tenant)
		ctx = context.WithValue(ctx, ctxKeyNode, operator)
		ctx = context.WithValue(ctx, ctxKeyOperatorAuthKind, authKind)
		next(w, r.WithContext(ctx))
	}
}

// resolveOperator authenticates an operator bearer token, returning the operator
// identity. It accepts EITHER a valid (unexpired) login session OR — when a
// break-glass operator token is configured — that token (constant-time compared so a
// timing side channel cannot leak it). It returns ("", unknown, false) when neither
// matches. The explicit kind must travel with the identity so an account whose name
// collides with the configured break-glass actor does not turn recovery auth into a
// login session.
//
// Both credentials are 256-bit unguessable secrets resolved by their hash, so the
// session lookup needs no constant-time compare (a Go map/file lookup by a hashed key
// does not leak the stored keys); only the break-glass compare against the pinned
// secret is constant-time. On any store error the session is simply not accepted
// (fail-closed) and the break-glass path is tried.
func (h *ControllerHandler) resolveOperator(ctx context.Context, tok string) (string, operatorAuthKind, bool) {
	hash := controller.HashToken(tok)
	if sess, err := h.store.LookupSession(ctx, h.tenant, hash, time.Now().UTC()); err == nil {
		return sess.Operator, operatorAuthSession, true
	}
	if h.operatorTokenHash != "" &&
		subtle.ConstantTimeCompare([]byte(hash), []byte(h.operatorTokenHash)) == 1 {
		return h.operatorName, operatorAuthBreakGlass, true
	}
	return "", operatorAuthUnknown, false
}
