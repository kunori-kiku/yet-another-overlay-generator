package api

// cookie_session.go adds an httpOnly session-cookie auth path alongside the existing
// Bearer/break-glass path (plan panel-appshell P5), so an operator login survives a
// page refresh WITHOUT the panel ever persisting a token in web storage (the session
// lives in an httpOnly cookie JS cannot read; login state is re-derived from GET
// /session). Cross-site request forgery is countered with a double-submit token: a
// readable yaog_csrf cookie that the panel echoes in the X-CSRF-Token header on
// state-changing requests, constant-time compared server-side.
//
// SCOPE: cookies/CSRF apply to OPERATOR routes only. Agent (machine-to-machine) routes
// keep Bearer-only auth with no cookies, no CSRF, and no credentialed CORS — they are
// not browser-reachable and must not gain an ambient-credential surface.

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"net/http"
	"time"

	"github.com/kunorikiku/yet-another-overlay-generator/internal/apierr"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/controller"
)

const (
	// sessionCookieName holds the operator session bearer (httpOnly; JS cannot read it).
	sessionCookieName = "yaog_session"
	// csrfCookieName holds the double-submit CSRF token (readable by JS on purpose, so
	// the panel can echo it in the X-CSRF-Token header).
	csrfCookieName = "yaog_csrf"
	// csrfHeaderName is the header the panel echoes the CSRF cookie value in.
	csrfHeaderName = "X-CSRF-Token"
)

// cookieSameSite picks the SameSite policy. Cross-origin panel hosting (a configured
// origin allowlist) needs SameSite=None, which browsers only honor together with
// Secure; absent that, the same-origin Lax default is both sufficient and safer.
func (h *ControllerHandler) cookieSameSite() http.SameSite {
	if len(h.panelOriginAllowlist) > 0 && h.secureCookie {
		return http.SameSiteNoneMode
	}
	return http.SameSiteLaxMode
}

// setSessionCookies writes the httpOnly session cookie and the readable CSRF cookie,
// both scoped to Path=/ and expiring with the session TTL. Call BEFORE writing the
// response status (Set-Cookie is a header).
func (h *ControllerHandler) setSessionCookies(w http.ResponseWriter, sessionPlaintext, csrf string) {
	maxAge := int(h.sessionTTL.Seconds())
	sameSite := h.cookieSameSite()
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    sessionPlaintext,
		Path:     "/",
		HttpOnly: true,
		Secure:   h.secureCookie,
		SameSite: sameSite,
		MaxAge:   maxAge,
	})
	http.SetCookie(w, &http.Cookie{
		Name:     csrfCookieName,
		Value:    csrf,
		Path:     "/",
		HttpOnly: false, // readable by the panel for the double-submit echo
		Secure:   h.secureCookie,
		SameSite: sameSite,
		MaxAge:   maxAge,
	})
}

// clearSessionCookies expires both cookies (logout). Call BEFORE writing the status.
func (h *ControllerHandler) clearSessionCookies(w http.ResponseWriter) {
	sameSite := h.cookieSameSite()
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookieName, Value: "", Path: "/",
		HttpOnly: true, Secure: h.secureCookie, SameSite: sameSite, MaxAge: -1,
	})
	http.SetCookie(w, &http.Cookie{
		Name: csrfCookieName, Value: "", Path: "/",
		HttpOnly: false, Secure: h.secureCookie, SameSite: sameSite, MaxAge: -1,
	})
}

// sessionCookieToken extracts the session bearer from the httpOnly cookie. The boolean
// is false if the cookie is absent or empty.
func sessionCookieToken(r *http.Request) (string, bool) {
	c, err := r.Cookie(sessionCookieName)
	if err != nil || c.Value == "" {
		return "", false
	}
	return c.Value, true
}

// csrfValid checks the double-submit CSRF: the X-CSRF-Token header must be present and
// constant-time equal to the yaog_csrf cookie. A missing header or cookie fails closed.
func csrfValid(r *http.Request) bool {
	c, err := r.Cookie(csrfCookieName)
	if err != nil || c.Value == "" {
		return false
	}
	header := r.Header.Get(csrfHeaderName)
	if header == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(header), []byte(c.Value)) == 1
}

// isStateChanging reports whether the method mutates state and is therefore subject to
// the CSRF check on the cookie path. Safe (read-only) methods are exempt.
func isStateChanging(method string) bool {
	switch method {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return false
	default:
		return true
	}
}

// newCSRFToken returns a 256-bit CSPRNG token, base64url (no padding).
func newCSRFToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// sessionResponseJSON is the GET /session probe body: the operator identity, the
// session expiry (RFC3339), and the double-submit CSRF token (echoed from the yaog_csrf
// cookie). The panel derives login state from the httpOnly cookie after a refresh
// without reading a token in JS, and recovers the in-memory CSRF token from csrf_token
// (the cross-origin yaog_csrf cookie is not readable via document.cookie).
type sessionResponseJSON struct {
	Operator  string `json:"operator"`
	ExpiresAt string `json:"expires_at"`
	CSRFToken string `json:"csrf_token"`
	// ControllerVersion is the controller's build version (handler.version; "dev" for a non-release
	// build). Surfaced ONLY here on the AUTHENTICATED operator session — never on /api/health or any
	// anonymous surface. The panel displays it and (plan-8) uses it to default the agent-rollout
	// target + reject a target newer than the controller. omitempty: additive wire shape.
	ControllerVersion string `json:"controller_version,omitempty"`
}

// HandleSession reports the current operator session (operator-authed via Bearer OR
// the session cookie). It is the panel's refresh-time probe: a 200 means "still logged
// in" (and reveals the operator + expiry); operatorAuth answers 401/403 otherwise. Routed
// through the op() adapter, which applies the method guard + structural identity() check;
// the operator identity arrives as `actor`. It reads its tenant from h.tenant (the pinned
// single-tenant), so the adapter's tenant arg is unused.
func (h *ControllerHandler) HandleSession(ctx context.Context, _ controller.TenantID, actor string, _ http.ResponseWriter, r *http.Request) (any, *apierr.Error) {
	// Best-effort expiry: only a cookie/session bearer resolves to a stored session with
	// an ExpiresAt (the break-glass token has none). A lookup miss just omits the field.
	expiresAt := ""
	if tok, ok := bearerOrCookieToken(r); ok {
		if sess, err := h.store.LookupSession(ctx, h.tenant, controller.HashToken(tok), time.Now().UTC()); err == nil {
			expiresAt = sess.ExpiresAt.Format(time.RFC3339)
		}
	}
	// Echo the CSRF cookie so the panel can recover its in-memory double-submit token
	// after a refresh (the cross-origin cookie is not readable via document.cookie).
	csrf := ""
	if c, err := r.Cookie(csrfCookieName); err == nil {
		csrf = c.Value
	}
	return sessionResponseJSON{Operator: actor, ExpiresAt: expiresAt, CSRFToken: csrf, ControllerVersion: h.version}, nil
}

// bearerOrCookieToken returns the operator credential from the Authorization header if
// present, else the session cookie. Used where either path is acceptable.
func bearerOrCookieToken(r *http.Request) (string, bool) {
	if tok, ok := bearerToken(r); ok {
		return tok, true
	}
	return sessionCookieToken(r)
}
