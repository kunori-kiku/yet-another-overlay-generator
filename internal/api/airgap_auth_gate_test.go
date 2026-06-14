package api

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
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/kunorikiku/yet-another-overlay-generator/internal/controller"
)

// airgapComputeRoutes are the four POST compute endpoints gated in controller mode.
func airgapComputeRoutes() []string {
	return []string{"/api/validate", "/api/compile", "/api/export", "/api/deploy-script"}
}

// TestAirgapRoutes_OpenInAirgapMode: with no controller enabled, the compute routes are
// reachable without auth (the gate is a passthrough), and health is public.
func TestAirgapRoutes_OpenInAirgapMode(t *testing.T) {
	s := NewServer()
	srv := httptest.NewServer(s.mux)
	t.Cleanup(srv.Close)

	if st := doJSON(t, http.MethodGet, srv.URL+"/api/health", "", nil, nil); st == http.StatusUnauthorized || st == http.StatusForbidden {
		t.Fatalf("/api/health must be public in air-gap mode, got %d", st)
	}
	for _, route := range airgapComputeRoutes() {
		st := doJSON(t, http.MethodPost, srv.URL+route, "", smallTopo(), nil)
		if st == http.StatusUnauthorized || st == http.StatusForbidden {
			t.Errorf("%s is gated in air-gap mode (%d); it must stay open", route, st)
		}
	}
}

// TestAirgapRoutes_GatedInControllerMode: once EnableController arms the operator-auth gate,
// the compute routes return 401 without auth and PASS the gate with an operator bearer token;
// health stays public.
func TestAirgapRoutes_GatedInControllerMode(t *testing.T) {
	s := NewServer()
	store := controller.NewMemStore()
	ch := NewControllerHandler(store, testTenant, controller.HashToken(testOperatorToken), DefaultOperatorName)
	s.EnableController(ch)
	srv := httptest.NewServer(s.mux)
	t.Cleanup(srv.Close)

	// Health is exempt — it must stay a public liveness probe even in controller mode.
	if st := doJSON(t, http.MethodGet, srv.URL+"/api/health", "", nil, nil); st == http.StatusUnauthorized || st == http.StatusForbidden {
		t.Fatalf("/api/health must stay public in controller mode, got %d", st)
	}

	for _, route := range airgapComputeRoutes() {
		// No credentials → 401 (operator-auth: "who are you").
		if st := doJSON(t, http.MethodPost, srv.URL+route, "", smallTopo(), nil); st != http.StatusUnauthorized {
			t.Errorf("%s without auth = %d, want 401 (operator-auth gate)", route, st)
		}
		// With the break-glass operator bearer token → PASSES the gate (Bearer is CSRF-exempt).
		// We only assert the gate was passed, not the handler's eventual status (200 / 400 /
		// 422 all mean "got past auth"); a 401/403 would mean the gate wrongly rejected.
		if st := doJSON(t, http.MethodPost, srv.URL+route, testOperatorToken, smallTopo(), nil); st == http.StatusUnauthorized || st == http.StatusForbidden {
			t.Errorf("%s with operator auth = %d, want it to PASS the gate", route, st)
		}
	}
}
