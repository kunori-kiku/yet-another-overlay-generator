package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// handler_test.go — the server handler tests. It covers the public liveness surface:
// GET /api/health and its CORS wrapper. The former validate/compile/export handler tests were
// deleted with the anonymous air-gap compute handlers they drove (framework-refactor plan-9);
// no_anonymous_compute_test.go now pins that those routes stay absent.

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
// on /api/health — the sole always-registered public route (retargeted off /api/compile in plan-7 / 1.7,
// since the air-gap compute routes are no longer registered). The OPTIONS
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
