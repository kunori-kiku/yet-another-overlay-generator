package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// handler_test.go — the DEFAULT (controller-only) build's handler tests. It covers only the
// surface present in BOTH builds: GET /api/health (liveness probe) and its CORS wrapper. The
// validate/compile/export handler tests and the validTopologyJSON helper moved behind
// //go:build airgap (handler_airgap_test.go) with the handlers they drive (plan-7 / 1.7).

func TestHandleHealth(t *testing.T) {
	server := NewServer()

	req := httptest.NewRequest(http.MethodGet, "/api/health", nil)
	rec := httptest.NewRecorder()

	server.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("want 200, got %d", rec.Code)
	}

	var resp HealthResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	if resp.Status != "ok" {
		t.Errorf("want status=ok, got %s", resp.Status)
	}
}

func TestHandleHealth_WrongMethod(t *testing.T) {
	server := NewServer()

	req := httptest.NewRequest(http.MethodPost, "/api/health", nil)
	rec := httptest.NewRecorder()

	server.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("want 405, got %d", rec.Code)
	}
}

// TestCORS_Preflight verifies the CORS preflight (OPTIONS → 204 + Access-Control-Allow-Origin)
// on /api/health — the route present in BOTH builds (retargeted off /api/compile in plan-7 / 1.7,
// since the air-gap compute routes are no longer registered in the default build). The OPTIONS
// short-circuit lives in the shared cors() middleware that wraps /api/health via wrap().
func TestCORS_Preflight(t *testing.T) {
	server := NewServer()

	req := httptest.NewRequest(http.MethodOptions, "/api/health", nil)
	rec := httptest.NewRecorder()

	server.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Errorf("CORS preflight want 204, got %d", rec.Code)
	}

	if rec.Header().Get("Access-Control-Allow-Origin") != "*" {
		t.Errorf("expected Access-Control-Allow-Origin: *")
	}
}

func TestCORS_Headers(t *testing.T) {
	server := NewServer()

	req := httptest.NewRequest(http.MethodGet, "/api/health", nil)
	rec := httptest.NewRecorder()

	server.Handler().ServeHTTP(rec, req)

	if rec.Header().Get("Access-Control-Allow-Origin") != "*" {
		t.Errorf("missing CORS header")
	}
}
