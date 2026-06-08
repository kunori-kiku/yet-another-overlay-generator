package api

// cookie_session_test.go covers the panel-appshell P5 httpOnly-cookie auth path: login
// sets the session + CSRF cookies and the cookie (no Bearer) authenticates an operator
// route; the double-submit CSRF gate rejects a cookie-path state-changing request with a
// missing/mismatched token but a Bearer request is exempt; logout clears the cookies; the
// /session probe reports the operator; and credentialed CORS reflects only allowlisted
// origins (never "*" with credentials).

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/kunorikiku/yet-another-overlay-generator/internal/controller"
)

// newCookieEnv stands up the operator mux (admin/correct-password) with Secure cookies
// disabled (httptest is plain HTTP) and the given credentialed-CORS origin allowlist.
func newCookieEnv(t *testing.T, origins []string) *httptest.Server {
	t.Helper()
	store := controller.NewMemStore()
	op, err := controller.NewOperator("admin", "correct-password", time.Now().UTC())
	if err != nil {
		t.Fatalf("NewOperator: %v", err)
	}
	if err := store.PutOperator(context.Background(), testTenant, op); err != nil {
		t.Fatalf("PutOperator: %v", err)
	}
	ch := NewControllerHandler(store, testTenant, "", DefaultOperatorName)
	ch.SetSecureCookie(false)
	if len(origins) > 0 {
		ch.SetPanelOrigins(origins)
	}
	mux := http.NewServeMux()
	ch.RegisterOperatorRoutes(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// loginForCookies logs in and returns the session + CSRF cookies and the csrf_token from
// the JSON body (which must equal the readable CSRF cookie value).
func loginForCookies(t *testing.T, srv *httptest.Server) (session, csrf *http.Cookie, csrfToken string) {
	t.Helper()
	resp, body := doLogin(t, srv, "admin", "correct-password")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("login = %d, want 200 (%s)", resp.StatusCode, body)
	}
	for _, c := range resp.Cookies() {
		switch c.Name {
		case sessionCookieName:
			session = c
		case csrfCookieName:
			csrf = c
		}
	}
	if session == nil || csrf == nil {
		t.Fatalf("login did not set both cookies (session=%v csrf=%v)", session, csrf)
	}
	if !session.HttpOnly {
		t.Errorf("session cookie is not HttpOnly")
	}
	if csrf.HttpOnly {
		t.Errorf("csrf cookie must be readable (not HttpOnly)")
	}
	var r loginResponseJSON
	if err := json.Unmarshal([]byte(body), &r); err != nil {
		t.Fatalf("unmarshal login body: %v", err)
	}
	if r.CSRFToken == "" || r.CSRFToken != csrf.Value {
		t.Fatalf("csrf_token (%q) must equal the csrf cookie (%q)", r.CSRFToken, csrf.Value)
	}
	return session, csrf, r.CSRFToken
}

func doCookieReq(t *testing.T, srv *httptest.Server, method, path string, cookies []*http.Cookie, csrfHeader string) *http.Response {
	t.Helper()
	req, _ := http.NewRequest(method, srv.URL+ctlBase+path, nil)
	for _, c := range cookies {
		if c != nil {
			req.AddCookie(c)
		}
	}
	if csrfHeader != "" {
		req.Header.Set(csrfHeaderName, csrfHeader)
	}
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	return resp
}

// TestCookieAuthenticatesGet: the session cookie alone (no Bearer) authenticates a GET
// operator route — login survives a refresh that drops the in-memory token.
func TestCookieAuthenticatesGet(t *testing.T) {
	srv := newCookieEnv(t, nil)
	session, _, _ := loginForCookies(t, srv)

	r := doCookieReq(t, srv, http.MethodGet, "nodes", []*http.Cookie{session}, "")
	r.Body.Close()
	if r.StatusCode != http.StatusOK {
		t.Fatalf("GET /nodes with session cookie = %d, want 200", r.StatusCode)
	}
}

// TestCookieCSRFGate: a cookie-path state-changing request needs a valid double-submit
// CSRF token. Missing header → 403; mismatched header → 403; matching header → 200.
func TestCookieCSRFGate(t *testing.T) {
	srv := newCookieEnv(t, nil)
	session, csrf, csrfToken := loginForCookies(t, srv)
	cookies := []*http.Cookie{session, csrf}

	// No CSRF header → 403.
	r := doCookieReq(t, srv, http.MethodPost, "rekey-all", cookies, "")
	r.Body.Close()
	if r.StatusCode != http.StatusForbidden {
		t.Fatalf("cookie POST without CSRF = %d, want 403", r.StatusCode)
	}
	// Wrong CSRF header → 403.
	r = doCookieReq(t, srv, http.MethodPost, "rekey-all", cookies, "not-the-token")
	r.Body.Close()
	if r.StatusCode != http.StatusForbidden {
		t.Fatalf("cookie POST with wrong CSRF = %d, want 403", r.StatusCode)
	}
	// Matching CSRF header → 200.
	r = doCookieReq(t, srv, http.MethodPost, "rekey-all", cookies, csrfToken)
	r.Body.Close()
	if r.StatusCode != http.StatusOK {
		t.Fatalf("cookie POST with valid CSRF = %d, want 200", r.StatusCode)
	}
}

// TestBearerExemptFromCSRF: a Bearer (break-glass) state-changing request needs NO CSRF
// token (Bearer auth is not CSRF-vulnerable).
func TestBearerExemptFromCSRF(t *testing.T) {
	srv, _ := newLoginEnv(t, controller.HashToken("break-glass-secret"))
	r := doAuthed(t, srv, http.MethodPost, "rekey-all", "break-glass-secret")
	r.Body.Close()
	if r.StatusCode != http.StatusOK {
		t.Fatalf("break-glass POST /rekey-all without CSRF = %d, want 200", r.StatusCode)
	}
}

// TestLogoutClearsCookies: logout (cookie-authed + CSRF) returns 204, emits cookie-clear
// Set-Cookie headers, and the session cookie no longer authenticates.
func TestLogoutClearsCookies(t *testing.T) {
	srv := newCookieEnv(t, nil)
	session, csrf, csrfToken := loginForCookies(t, srv)
	cookies := []*http.Cookie{session, csrf}

	r := doCookieReq(t, srv, http.MethodPost, "logout", cookies, csrfToken)
	if r.StatusCode != http.StatusNoContent {
		r.Body.Close()
		t.Fatalf("logout = %d, want 204", r.StatusCode)
	}
	clearedSession := false
	for _, c := range r.Cookies() {
		if c.Name == sessionCookieName && c.MaxAge < 0 {
			clearedSession = true
		}
	}
	r.Body.Close()
	if !clearedSession {
		t.Errorf("logout did not emit a clearing Set-Cookie for the session")
	}
	// The revoked session cookie no longer authenticates.
	r = doCookieReq(t, srv, http.MethodGet, "nodes", []*http.Cookie{session}, "")
	r.Body.Close()
	if r.StatusCode != http.StatusForbidden {
		t.Fatalf("post-logout cookie GET /nodes = %d, want 403", r.StatusCode)
	}
}

// TestSessionProbe: GET /session reports the operator when cookie-authed; 401 without.
func TestSessionProbe(t *testing.T) {
	srv := newCookieEnv(t, nil)
	session, _, _ := loginForCookies(t, srv)

	r := doCookieReq(t, srv, http.MethodGet, "session", []*http.Cookie{session}, "")
	defer r.Body.Close()
	if r.StatusCode != http.StatusOK {
		t.Fatalf("GET /session with cookie = %d, want 200", r.StatusCode)
	}
	var sr sessionResponseJSON
	if err := json.NewDecoder(r.Body).Decode(&sr); err != nil {
		t.Fatalf("decode session response: %v", err)
	}
	if sr.Operator != "admin" {
		t.Errorf("session operator = %q, want admin", sr.Operator)
	}
	if sr.ExpiresAt == "" {
		t.Errorf("session probe did not report an expiry")
	}

	r2 := doCookieReq(t, srv, http.MethodGet, "session", nil, "")
	r2.Body.Close()
	if r2.StatusCode != http.StatusUnauthorized {
		t.Fatalf("GET /session unauthenticated = %d, want 401", r2.StatusCode)
	}
}

// TestCredentialedCORS: an allowlisted Origin is reflected with Allow-Credentials + Vary;
// a non-allowlisted Origin gets "*" with NO credentials (never "*"+credentials).
func TestCredentialedCORS(t *testing.T) {
	const allowed = "https://panel.example.com"
	srv := newCookieEnv(t, []string{allowed})

	preflight := func(origin string) *http.Response {
		req, _ := http.NewRequest(http.MethodOptions, srv.URL+ctlBase+"nodes", nil)
		req.Header.Set("Origin", origin)
		req.Header.Set("Access-Control-Request-Method", "GET")
		resp, err := srv.Client().Do(req)
		if err != nil {
			t.Fatalf("preflight: %v", err)
		}
		return resp
	}

	r := preflight(allowed)
	r.Body.Close()
	if got := r.Header.Get("Access-Control-Allow-Origin"); got != allowed {
		t.Errorf("allowlisted ACAO = %q, want %q", got, allowed)
	}
	if got := r.Header.Get("Access-Control-Allow-Credentials"); got != "true" {
		t.Errorf("allowlisted Allow-Credentials = %q, want true", got)
	}
	if got := r.Header.Get("Vary"); got == "" {
		t.Errorf("allowlisted response missing Vary: Origin")
	}

	r = preflight("https://evil.example.com")
	r.Body.Close()
	if got := r.Header.Get("Access-Control-Allow-Origin"); got != "*" {
		t.Errorf("non-allowlisted ACAO = %q, want *", got)
	}
	if got := r.Header.Get("Access-Control-Allow-Credentials"); got != "" {
		t.Errorf("non-allowlisted must NOT set Allow-Credentials, got %q", got)
	}
}
