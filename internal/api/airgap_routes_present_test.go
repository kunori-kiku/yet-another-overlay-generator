//go:build airgap

package api

// airgap_routes_present_test.go — plan-7 / 1.7, Phase 5.1 (positive counterpart). Tagged behind
// //go:build airgap. It is the mirror of airgap_routes_removed_test.go: under -tags airgap the four
// anonymous air-gap compute routes ARE registered (registerExtraRoutes wires them on s.mux), so the
// local-design oracle that plan-13's --mode airgap E2E and plan-21's -tags airgap DAST boot is not
// silently rotted away. We assert each route is reachable (NOT 404) on a pure air-gap Server (no
// EnableController, so gateAirgap is a passthrough); the handler's exact status is immaterial here —
// only that the route is registered.

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestAirgapRoutes_RegisteredInAirgapBuild asserts the four compute routes are registered under
// -tags airgap: each returns a non-404 status (i.e. ServeMux matched the pattern and the handler
// ran). An empty-body POST hits the handler and returns 400/422/400-format, all of which are NOT
// 404 — proving registration without asserting handler semantics (covered by handler_airgap_test.go).
func TestAirgapRoutes_RegisteredInAirgapBuild(t *testing.T) {
	server := NewServer()
	handler := server.Handler()

	for _, route := range airgapComputeRoutes() {
		req := httptest.NewRequest(http.MethodPost, route, nil)
		rec := httptest.NewRecorder()

		handler.ServeHTTP(rec, req)

		if rec.Code == http.StatusNotFound {
			t.Errorf("POST %s = 404 under -tags airgap, want it REGISTERED (the air-gap oracle must retain the four compute routes)", route)
		}
	}
}

// TestHealthRegisteredInAirgapBuild asserts the public liveness probe is also present under
// -tags airgap (it lives in the un-tagged registerRoutes, so it is in BOTH builds).
func TestHealthRegisteredInAirgapBuild(t *testing.T) {
	server := NewServer()
	handler := server.Handler()

	req := httptest.NewRequest(http.MethodGet, "/api/health", nil)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/health = %d under -tags airgap, want 200", rec.Code)
	}
}
