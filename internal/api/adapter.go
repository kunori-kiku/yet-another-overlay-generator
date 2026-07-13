package api

// adapter.go is the typed operator-handler adapter. Every identity-bearing operator route
// hand-rolled the SAME preamble — a method guard, then `tenant,_,ok := identity(ctx); if
// !ok { CodeInternalIdentityMissing }` — before doing any work. That duplication made the
// identity() check a per-handler CONVENTION: a new handler could silently forget it. The
// adapter lifts the preamble into ONE place so it is STRUCTURAL instead.
//
// INVARIANT 4 (backend sole authority for status/identity): a handler routed through op /
// opRaw CANNOT run its body without identity() having succeeded — the adapter calls it and
// rejects (CodeInternalIdentityMissing) before fn is ever invoked. This is the auth
// chokepoint's second line: operatorAuth/requireNode set the identity onto the context
// (auth_controller.go); the adapter refuses to dispatch a handler that reached it WITHOUT
// one. Handlers that legitimately need no identity (release-pins, settings-GET) are NOT
// routed through the adapter and keep their hand-rolled shape — the adapter is never
// weakened to fit them.
//
// The response contract is byte-identical to the guards it replaces: the 405 body carries
// the exact `methods` string as its {method} param, the identity-missing 500 is the same
// CodeInternalIdentityMissing envelope, a success is writeJSON(200, result), and a coded
// error is writeAPIError(aerr) at the error's own status.

import (
	"context"
	"net/http"
	"strings"

	"github.com/kunorikiku/yet-another-overlay-generator/internal/apierr"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/controller"
)

// op adapts a typed operator handler into an http.HandlerFunc, applying the shared
// preamble ONCE: the method guard, then the structural identity() check, then dispatch.
// On a nil error it writes fn's result as a 200 JSON body (identical to the writeJSON(w,
// 200, …) the migrated handlers used to end with); on a coded error it writes the nested
// envelope at the error's own status. `methods` is the comma-space-separated allow-list
// ("POST", or "GET, POST"), used BOTH as the guard and as the CodeMethodNotAllowed
// {method} param so the 405 body matches the hand-rolled guard byte-for-byte.
//
// fn receives w PURELY so it can drive the size-capping body readers (decodeJSON /
// readTopology / readControllerBody, which wrap r.Body in http.MaxBytesReader(w, …)); it
// RETURNS its response and must not write the success body itself — op owns that.
func (h *ControllerHandler) op(methods string, fn func(ctx context.Context, tenant controller.TenantID, actor string, w http.ResponseWriter, r *http.Request) (any, *apierr.Error)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !methodAllowed(methods, r.Method) {
			writeAPIError(w, apierr.New(apierr.CodeMethodNotAllowed).With("method", methods))
			return
		}
		tenant, actor, ok := identity(r.Context())
		if !ok {
			writeAPIError(w, apierr.New(apierr.CodeInternalIdentityMissing))
			return
		}
		result, aerr := fn(r.Context(), tenant, actor, w, r)
		if aerr != nil {
			writeAPIError(w, aerr)
			return
		}
		writeJSON(w, http.StatusOK, result)
	}
}

// opRaw is op for handlers that write their OWN success body — raw bytes with a custom
// Content-Type that writeJSON cannot produce (verbatim stored topology JSON with a
// charset, a ZIP with a Content-Disposition). It applies the IDENTICAL method +
// structural identity() preamble and the same coded-error path; on success fn has already
// written the response, so opRaw adds nothing. fn writes its body only AFTER all error
// checks, so an early coded return never collides with a partial write.
func (h *ControllerHandler) opRaw(methods string, fn func(ctx context.Context, tenant controller.TenantID, actor string, w http.ResponseWriter, r *http.Request) *apierr.Error) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !methodAllowed(methods, r.Method) {
			writeAPIError(w, apierr.New(apierr.CodeMethodNotAllowed).With("method", methods))
			return
		}
		tenant, actor, ok := identity(r.Context())
		if !ok {
			writeAPIError(w, apierr.New(apierr.CodeInternalIdentityMissing))
			return
		}
		if aerr := fn(r.Context(), tenant, actor, w, r); aerr != nil {
			writeAPIError(w, aerr)
			return
		}
	}
}

// methodAllowed reports whether m is one of the comma-space-separated methods in `allowed`
// ("POST"; "GET, POST"). The exact `allowed` string doubles as the CodeMethodNotAllowed
// {method} param, so a multi-verb endpoint's 405 lists every verb verbatim — the same
// string the hand-rolled guards passed.
func methodAllowed(allowed, m string) bool {
	for _, a := range strings.Split(allowed, ", ") {
		if a == m {
			return true
		}
	}
	return false
}
