package api

// auth_controller.go is the single authentication chokepoint for the networked
// controller routes (plan-4.3b). Every controller request that is not /enroll
// passes through this middleware, which derives the calling identity from the
// VERIFIED mTLS client certificate and rejects anything that does not match the
// configured tenant / required role.
//
// Trust model. The TLS layer (controller.DevCA.ServerTLSConfig) uses
// ClientAuth=VerifyClientCertIfGiven: a client cert is optional at the handshake
// (so certless /enroll is reachable) but, if presented, MUST chain to the dev CA.
// Therefore r.TLS.PeerCertificates is either empty (no cert) or a verified chain.
// This middleware never re-verifies the chain — it only enforces PRESENCE and the
// identity parsed from the leaf's Common Name.
//
// Identity. The client-cert CN is "<tenant>:<node>" (IssueClientCert binds it).
// The middleware splits on the first ':' (strings.Cut), pins the tenant to the
// configured YAOG_TENANT_ID (single-tenant v1; a cert for another tenant is a 403),
// and puts tenant+node into the request context. A node acts ONLY as itself: the
// agent handlers read the node from the context, never from a URL/body field, so a
// node cannot fetch or report for a different node.
//
// Roles. Two kinds of route:
//   - agent routes (/config,/poll,/report): any verified node cert is accepted.
//   - operator routes (/update-topology,/stage,/promote): the cert's node component
//     MUST equal the configured operator identity ("operator"); a normal node cert
//     on an operator route is a 403.
//
// /enroll is NOT wrapped by this middleware at all (it must be reachable certless).

import (
	"context"
	"net/http"
	"strings"

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

// nodeFromCtx returns the node identity (the cert CN's node component) pinned onto
// the request context by the auth middleware. For agent routes this is the calling
// node; for operator routes it is the operator identity. The boolean is false if
// no node was set.
func nodeFromCtx(ctx context.Context) (string, bool) {
	n, ok := ctx.Value(ctxKeyNode).(string)
	return n, ok
}

// authResult carries the parsed, verified identity from a client cert.
type authResult struct {
	tenant controller.TenantID
	node   string
}

// authenticate extracts and validates the caller's identity from the request's
// verified mTLS client cert. It returns the parsed identity, or an HTTP status +
// message to reject with. It enforces, in order:
//
//	401 — no client cert presented, or the cert has no parseable "<tenant>:<node>" CN.
//	403 — the cert's tenant != the configured tenant (cross-tenant access).
//
// It does NOT enforce the operator-vs-node distinction; that is the caller's
// responsibility (requireNode vs requireOperator) since it is route-specific.
func (h *ControllerHandler) authenticate(r *http.Request) (authResult, int, string) {
	// VerifyClientCertIfGiven means a presented cert is already verified against the
	// CA; an EMPTY PeerCertificates means no cert was presented at all.
	if r.TLS == nil || len(r.TLS.PeerCertificates) == 0 {
		return authResult{}, http.StatusUnauthorized, "client certificate required"
	}
	cn := r.TLS.PeerCertificates[0].Subject.CommonName
	tenantStr, node, ok := strings.Cut(cn, ":")
	if !ok || tenantStr == "" || node == "" {
		return authResult{}, http.StatusUnauthorized, "client certificate has no valid identity"
	}
	if controller.TenantID(tenantStr) != h.tenant {
		// A cert minted for a different tenant must never act in this tenant. Under
		// single-tenant v1 every legitimate cert carries the configured tenant; a
		// mismatch is an authorization failure, not a malformed request.
		return authResult{}, http.StatusForbidden, "certificate tenant not authorized"
	}
	return authResult{tenant: h.tenant, node: node}, 0, ""
}

// requireNode wraps an agent handler: it authenticates the caller (any verified
// node cert is accepted) and injects tenant+node into the context. The wrapped
// handler reads the node from the context — never from the request — so a node can
// only act as itself.
func (h *ControllerHandler) requireNode(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		auth, status, msg := h.authenticate(r)
		if status != 0 {
			writeError(w, status, msg)
			return
		}
		ctx := context.WithValue(r.Context(), ctxKeyTenant, auth.tenant)
		ctx = context.WithValue(ctx, ctxKeyNode, auth.node)
		next(w, r.WithContext(ctx))
	}
}

// requireOperator wraps an operator-only handler: it authenticates the caller and
// additionally requires the cert's node component to equal the configured operator
// identity. A normal node cert on an operator route is a 403 — a node can never
// perform an operator action (update-topology/stage/promote).
func (h *ControllerHandler) requireOperator(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		auth, status, msg := h.authenticate(r)
		if status != 0 {
			writeError(w, status, msg)
			return
		}
		if auth.node != h.operatorName {
			writeError(w, http.StatusForbidden, "operator privileges required")
			return
		}
		ctx := context.WithValue(r.Context(), ctxKeyTenant, auth.tenant)
		ctx = context.WithValue(ctx, ctxKeyNode, auth.node)
		next(w, r.WithContext(ctx))
	}
}
