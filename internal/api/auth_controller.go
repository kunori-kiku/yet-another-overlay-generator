package api

// auth_controller.go is the single authentication chokepoint for the networked
// controller routes (plan-4.5). Every controller request that is not /enroll
// passes through this middleware, which derives the calling identity from a
// PER-NODE BEARER TOKEN presented in the Authorization header and rejects anything
// that does not resolve to an active node (agent routes) or the operator token
// (operator routes).
//
// Trust model. Authentication is a bearer token, NOT mTLS. The transport is plain
// HTTP; confidentiality (against token replay on the wire) is delegated to a
// reverse proxy's TLS (nginx/caddy) and is out of this app's scope — bearer tokens
// are replayable if leaked, so the deployment MUST terminate TLS in front of the
// controller. This is the conscious v1 model (plan-4.5).
//
// Identity. A node presents "Authorization: Bearer <token>". The middleware hashes
// the presented token (controller.HashToken) and resolves it via
// Store.LookupNodeByAPIToken to the owning Node — there is no tenant/node field in
// the URL or body. The tenant is the configured one (single-tenant v1, pinned from
// YAOG_TENANT_ID). A node acts ONLY as itself: the agent handlers read the node
// from the request context, never from a URL/body field, so a node cannot fetch or
// report for a different node.
//
// Roles. Two kinds of route:
//   - agent routes (/config,/poll,/report): any token that resolves to an active
//     (non-revoked) node is accepted (requireNode).
//   - operator routes (/update-topology,/stage,/promote,/nodes,/revoke,/audit,
//     /topology,/enrollment-token): the presented token's hash MUST equal the configured
//     operator token hash (constant-time compare); a node token on an operator
//     route is a 403 (operatorAuth).
//
// /enroll is NOT wrapped by this middleware at all (it must be reachable before the
// node has any API token).

import (
	"context"
	"crypto/subtle"
	"net/http"
	"strings"
	"time"

	"github.com/kunorikiku/yet-another-overlay-generator/internal/controller"
)

// ctxKey is an unexported context-key type so controller identity values never
// collide with keys from other packages sharing the request context.
type ctxKey int

const (
	ctxKeyTenant ctxKey = iota
	ctxKeyNode
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
func (h *ControllerHandler) authenticateNode(r *http.Request) (authResult, int, string) {
	tok, ok := bearerToken(r)
	if !ok {
		return authResult{}, http.StatusUnauthorized, "bearer token required"
	}
	node, err := h.store.LookupNodeByAPIToken(r.Context(), h.tenant, controller.HashToken(tok))
	if err != nil {
		// ErrTokenInvalid covers an unmapped/stale token AND any non-approved node
		// (Store contract); either way the caller is not an authenticated active node.
		return authResult{}, http.StatusUnauthorized, "invalid bearer token"
	}
	return authResult{tenant: h.tenant, node: node.NodeID}, 0, ""
}

// requireNode wraps an agent handler: it authenticates the caller via its per-node
// bearer token and injects tenant+node into the context. The wrapped handler reads
// the node from the context — never from the request — so a node can only act as
// itself.
func (h *ControllerHandler) requireNode(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		auth, status, msg := h.authenticateNode(r)
		if status != 0 {
			writeError(w, status, msg)
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
		tok, ok := bearerToken(r)
		if !ok {
			writeError(w, http.StatusUnauthorized, "bearer token required")
			return
		}
		operator, ok := h.resolveOperator(r.Context(), tok)
		if !ok {
			writeError(w, http.StatusForbidden, "operator privileges required")
			return
		}
		ctx := context.WithValue(r.Context(), ctxKeyTenant, h.tenant)
		ctx = context.WithValue(ctx, ctxKeyNode, operator)
		next(w, r.WithContext(ctx))
	}
}

// resolveOperator authenticates an operator bearer token, returning the operator
// identity. It accepts EITHER a valid (unexpired) login session OR — when a
// break-glass operator token is configured — that token (constant-time compared so a
// timing side channel cannot leak it). It returns ("", false) when neither matches.
//
// Both credentials are 256-bit unguessable secrets resolved by their hash, so the
// session lookup needs no constant-time compare (a Go map/file lookup by a hashed key
// does not leak the stored keys); only the break-glass compare against the pinned
// secret is constant-time. On any store error the session is simply not accepted
// (fail-closed) and the break-glass path is tried.
func (h *ControllerHandler) resolveOperator(ctx context.Context, tok string) (string, bool) {
	hash := controller.HashToken(tok)
	if sess, err := h.store.LookupSession(ctx, h.tenant, hash, time.Now().UTC()); err == nil {
		return sess.Operator, true
	}
	if h.operatorTokenHash != "" &&
		subtle.ConstantTimeCompare([]byte(hash), []byte(h.operatorTokenHash)) == 1 {
		return h.operatorName, true
	}
	return "", false
}
