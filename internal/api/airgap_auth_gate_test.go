//go:build airgap

package api

// airgap_auth_gate_test.go — plan-7 / 1.7: tagged behind //go:build airgap. Its gateAirgap /
// operator-gate assertions against the four compute routes reference symbols (gateAirgap, the four
// route registrations) that exist only under -tags airgap, so the file compiles only in the air-gap
// build. Default-build operator-route auth is covered by controller_http_test.go.
//
// airgap_auth_gate_test.go — plan-12 / T6. In a CONTROLLER deployment the air-gap compute
// routes (/api/validate, /api/compile, /api/export, /api/deploy-script) live on the operator
// port and must be behind operator-auth — otherwise they are an unauthenticated compute /
// key-gen oracle (and DoS surface) on that port. In a pure AIR-GAP deployment they stay open
// exactly as before. /api/health is a public liveness probe in both modes.
//
// The gate is request-time (Server.gateAirgap reads Server.operatorAuth, armed by
// EnableController). This test exercises the real *Server* (NewServer + EnableController),
// unlike newCtlTestEnv which registers only the controller muxes.

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

// airgapComputeRoutes are the four POST compute endpoints gated in controller mode.
func airgapComputeRoutes() []string {
	return []string{"/api/validate", "/api/compile", "/api/export", "/api/deploy-script"}
}

// newGatedControllerServer builds the real *Server with controller mode enabled (so the
// air-gap compute routes are behind operator-auth), backed by a MemStore that has BOTH a
// break-glass operator token (for the Bearer path) and an "admin" operator account (for the
// login/cookie+CSRF path). Secure cookies are off because httptest is plain HTTP.
func newGatedControllerServer(t *testing.T) *httptest.Server {
	t.Helper()
	store := controller.NewMemStore()
	op, err := controller.NewOperator("admin", "correct-password", time.Now().UTC())
	if err != nil {
		t.Fatalf("NewOperator: %v", err)
	}
	if err := store.PutOperator(context.Background(), testTenant, op); err != nil {
		t.Fatalf("PutOperator: %v", err)
	}
	ch := NewControllerHandler(store, testTenant, controller.HashToken(testOperatorToken), DefaultOperatorName)
	ch.SetSecureCookie(false)
	s := NewServer()
	s.EnableController(ch)
	srv := httptest.NewServer(s.mux)
	t.Cleanup(srv.Close)
	return srv
}

// postValidate POSTs smallTopo() to /api/validate with the given cookies / CSRF header /
// bearer token, returning the status code. Used to exercise the cookie+CSRF gate path.
func postValidate(t *testing.T, srv *httptest.Server, cookies []*http.Cookie, csrfHeader, bearer string) int {
	t.Helper()
	raw, err := json.Marshal(smallTopo())
	if err != nil {
		t.Fatalf("marshal topo: %v", err)
	}
	req, err := http.NewRequest(http.MethodPost, srv.URL+"/api/validate", bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	for _, c := range cookies {
		if c != nil {
			req.AddCookie(c)
		}
	}
	if csrfHeader != "" {
		req.Header.Set(csrfHeaderName, csrfHeader)
	}
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatalf("POST /api/validate: %v", err)
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)
	return resp.StatusCode
}

// TestAirgapRoutes_OpenInAirgapMode: with no controller enabled, the compute routes are
// reachable without auth (the gate is a passthrough), and health is a public 200.
func TestAirgapRoutes_OpenInAirgapMode(t *testing.T) {
	s := NewServer()
	srv := httptest.NewServer(s.mux)
	t.Cleanup(srv.Close)

	if st := doJSON(t, http.MethodGet, srv.URL+"/api/health", "", nil, nil); st != http.StatusOK {
		t.Fatalf("/api/health must be a public 200 liveness probe in air-gap mode, got %d", st)
	}
	for _, route := range airgapComputeRoutes() {
		// Assert openness (not 401/403). Not ==200 because /api/deploy-script needs a ?format=
		// query param (would 400 without it); openness is what this test asserts.
		st := doJSON(t, http.MethodPost, srv.URL+route, "", smallTopo(), nil)
		if st == http.StatusUnauthorized || st == http.StatusForbidden {
			t.Errorf("%s is gated in air-gap mode (%d); it must stay open", route, st)
		}
	}
}

// TestAirgapRoutes_GatedInControllerMode: once EnableController arms the operator-auth gate,
// the compute routes return 401 without auth and PASS the gate with an operator bearer token;
// health stays a public 200.
func TestAirgapRoutes_GatedInControllerMode(t *testing.T) {
	srv := newGatedControllerServer(t)

	// Health is exempt — it must stay a public 200 liveness probe even in controller mode.
	if st := doJSON(t, http.MethodGet, srv.URL+"/api/health", "", nil, nil); st != http.StatusOK {
		t.Fatalf("/api/health must stay a public 200 in controller mode, got %d", st)
	}

	for _, route := range airgapComputeRoutes() {
		// No credentials → 401 (operator-auth: "who are you"). smallTopo() is valid, so an
		// ungated route would 200 — this strictly proves the gate rejected.
		if st := doJSON(t, http.MethodPost, srv.URL+route, "", smallTopo(), nil); st != http.StatusUnauthorized {
			t.Errorf("%s without auth = %d, want 401 (operator-auth gate)", route, st)
		}
		// With the break-glass operator bearer token → PASSES the gate (Bearer is CSRF-exempt).
		// Assert the gate was passed (not 401/403); the handler's eventual status (200/400/422)
		// is immaterial here.
		if st := doJSON(t, http.MethodPost, srv.URL+route, testOperatorToken, smallTopo(), nil); st == http.StatusUnauthorized || st == http.StatusForbidden {
			t.Errorf("%s with operator auth = %d, want it to PASS the gate", route, st)
		}
	}
}

// TestAirgapRoutes_ControllerCookieCSRF: the exact path the panel's validate() relies on after
// a refresh (in-memory token gone) — the httpOnly session cookie + the double-submit CSRF
// header. Through the gate-wrapped /api/validate: cookie + matching CSRF passes (200 for a valid
// topo); cookie with missing/mismatched CSRF is rejected (403). This pins the cookie path on the
// GATED compute route, not just on the operator routes (cookie_session_test.go covers those).
func TestAirgapRoutes_ControllerCookieCSRF(t *testing.T) {
	srv := newGatedControllerServer(t)
	session, csrf, csrfToken := loginForCookies(t, srv)
	cookies := []*http.Cookie{session, csrf}

	if st := postValidate(t, srv, cookies, csrfToken, ""); st != http.StatusOK {
		t.Errorf("/api/validate cookie + matching CSRF = %d, want 200 (passes gate, valid topo)", st)
	}
	if st := postValidate(t, srv, cookies, "", ""); st != http.StatusForbidden {
		t.Errorf("/api/validate cookie + NO CSRF = %d, want 403", st)
	}
	if st := postValidate(t, srv, cookies, "wrong-token", ""); st != http.StatusForbidden {
		t.Errorf("/api/validate cookie + wrong CSRF = %d, want 403", st)
	}
}
