package api

// handler_login_test.go drives the operator-login HTTP surface (plan-5.2) over an
// in-process operator mux backed by a MemStore: success mints a session that
// authenticates an operator route; wrong-password / unknown-user fail uniformly; the
// rate limiter locks out; the break-glass token still authenticates; logout revokes.

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/kunorikiku/yet-another-overlay-generator/internal/controller"
)

const ctlBase = "/api/v1/operator/"

// newLoginEnv stands up the operator mux with a MemStore that already holds operator
// "admin" / "correct-password". breakGlassHash is the optional break-glass token hash
// ("" disables it).
func newLoginEnv(t *testing.T, breakGlassHash string) (*httptest.Server, controller.Store) {
	t.Helper()
	store := controller.NewMemStore()
	op, err := controller.NewOperator("admin", "correct-password", time.Now().UTC())
	if err != nil {
		t.Fatalf("NewOperator: %v", err)
	}
	if err := store.PutOperator(context.Background(), testTenant, op); err != nil {
		t.Fatalf("PutOperator: %v", err)
	}
	ch := NewControllerHandler(store, testTenant, breakGlassHash, DefaultOperatorName)
	mux := http.NewServeMux()
	ch.RegisterOperatorRoutes(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, store
}

func doLogin(t *testing.T, srv *httptest.Server, user, pass string) (*http.Response, string) {
	t.Helper()
	body, _ := json.Marshal(loginRequestJSON{Username: user, Password: pass})
	resp, err := srv.Client().Post(srv.URL+ctlBase+"login", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("login POST: %v", err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	return resp, string(b)
}

func sessionFrom(t *testing.T, body string) string {
	t.Helper()
	var r loginResponseJSON
	if err := json.Unmarshal([]byte(body), &r); err != nil {
		t.Fatalf("unmarshal login response: %v (%s)", err, body)
	}
	if r.SessionToken == "" {
		t.Fatalf("empty session_token in %s", body)
	}
	return r.SessionToken
}

func doAuthed(t *testing.T, srv *httptest.Server, method, path, bearer string) *http.Response {
	t.Helper()
	req, _ := http.NewRequest(method, srv.URL+ctlBase+path, nil)
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	return resp
}

// TestLoginSuccessAuthenticatesOperatorRoute: a session minted at /login authenticates
// an operator route.
func TestLoginSuccessAuthenticatesOperatorRoute(t *testing.T) {
	srv, _ := newLoginEnv(t, "")

	resp, body := doLogin(t, srv, "admin", "correct-password")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("login status = %d, want 200 (%s)", resp.StatusCode, body)
	}
	tok := sessionFrom(t, body)

	r := doAuthed(t, srv, http.MethodGet, "nodes", tok)
	r.Body.Close()
	if r.StatusCode != http.StatusOK {
		t.Fatalf("GET /nodes with session = %d, want 200", r.StatusCode)
	}
}

// TestLoginWrongPasswordAndUnknownUser: both fail with a uniform 401 and mint no
// session.
func TestLoginWrongPasswordAndUnknownUser(t *testing.T) {
	srv, _ := newLoginEnv(t, "")

	resp, _ := doLogin(t, srv, "admin", "WRONG")
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("wrong password status = %d, want 401", resp.StatusCode)
	}
	resp, _ = doLogin(t, srv, "ghost", "whatever")
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("unknown user status = %d, want 401", resp.StatusCode)
	}
}

// TestLoginLockout: repeated failures lock the account/IP out with a 429 + Retry-After,
// and even the correct password is then rejected until the window elapses.
func TestLoginLockout(t *testing.T) {
	srv, _ := newLoginEnv(t, "")

	for i := 0; i < maxLoginFailures; i++ {
		resp, _ := doLogin(t, srv, "admin", "WRONG")
		if resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("failure %d status = %d, want 401", i+1, resp.StatusCode)
		}
	}
	resp, _ := doLogin(t, srv, "admin", "WRONG")
	if resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("post-lockout status = %d, want 429", resp.StatusCode)
	}
	if resp.Header.Get("Retry-After") == "" {
		t.Error("429 response missing Retry-After header")
	}
	// The correct password is also blocked while locked out.
	resp, _ = doLogin(t, srv, "admin", "correct-password")
	if resp.StatusCode != http.StatusTooManyRequests {
		t.Errorf("correct password while locked = %d, want 429", resp.StatusCode)
	}
}

// TestBreakGlassTokenAuthenticates: with a break-glass token configured, presenting it
// authenticates an operator route (the recovery path), while no/garbage credentials are
// rejected.
func TestBreakGlassTokenAuthenticates(t *testing.T) {
	srv, _ := newLoginEnv(t, controller.HashToken("break-glass-secret"))

	r := doAuthed(t, srv, http.MethodGet, "nodes", "break-glass-secret")
	r.Body.Close()
	if r.StatusCode != http.StatusOK {
		t.Fatalf("break-glass token on /nodes = %d, want 200", r.StatusCode)
	}
	// No bearer -> 401; a wrong bearer -> 403.
	r = doAuthed(t, srv, http.MethodGet, "nodes", "")
	r.Body.Close()
	if r.StatusCode != http.StatusUnauthorized {
		t.Errorf("no bearer = %d, want 401", r.StatusCode)
	}
	r = doAuthed(t, srv, http.MethodGet, "nodes", "garbage-token")
	r.Body.Close()
	if r.StatusCode != http.StatusForbidden {
		t.Errorf("garbage bearer = %d, want 403", r.StatusCode)
	}
}

// TestLogoutRevokesSession: a session works until /logout, then is rejected.
func TestLogoutRevokesSession(t *testing.T) {
	srv, _ := newLoginEnv(t, "")

	_, body := doLogin(t, srv, "admin", "correct-password")
	tok := sessionFrom(t, body)

	// Works before logout.
	r := doAuthed(t, srv, http.MethodGet, "nodes", tok)
	r.Body.Close()
	if r.StatusCode != http.StatusOK {
		t.Fatalf("pre-logout /nodes = %d, want 200", r.StatusCode)
	}
	// Logout revokes it.
	r = doAuthed(t, srv, http.MethodPost, "logout", tok)
	r.Body.Close()
	if r.StatusCode != http.StatusNoContent {
		t.Fatalf("logout = %d, want 204", r.StatusCode)
	}
	// Now rejected.
	r = doAuthed(t, srv, http.MethodGet, "nodes", tok)
	r.Body.Close()
	if r.StatusCode != http.StatusForbidden {
		t.Fatalf("post-logout /nodes = %d, want 403", r.StatusCode)
	}
}
