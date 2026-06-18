package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestRequestBodySizeCap_Returns413 verifies that a POST request body exceeding the 4 MiB
// cap is rejected by http.MaxBytesReader and mapped by the handler to 413 Payload Too
// Large (D34).
//
// The request body is the prefix of valid JSON (padded with one huge string field until it
// exceeds the cap), ensuring the size limit is what triggers, not a JSON parse error -- the
// read phase fails on the cap before parsing begins.
func TestRequestBodySizeCap_Returns413(t *testing.T) {
	server := NewServer()

	// Build a request body slightly larger than maxRequestBodyBytes.
	oversized := bytes.Repeat([]byte("a"), int(maxRequestBodyBytes)+1024)
	req := httptest.NewRequest(http.MethodPost, "/api/validate", bytes.NewReader(oversized))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	server.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("want 413, got %d, body: %s", rec.Code, rec.Body.String())
	}

	// The error response must be of the form {"error":{code,message,params}} for the
	// frontend to display/localize.
	var resp apiError
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("failed to decode error response: %v", err)
	}
	if resp.Error.Message == "" {
		t.Errorf("413 response should contain a non-empty error.message field")
	}
}

// TestRecoverPanics_Returns500JSON tests the recoverPanics middleware directly: an
// http.HandlerFunc that deliberately panics, once wrapped by the middleware, should return
// 500 with a {"error": ...} JSON body rather than tearing the connection (D60).
// TestRecovered_MuxPanicReturns500JSON pins B1: recovered() — the top-level wrapper applied
// to BOTH the operator and agent muxes (not just the air-gap routes) — converts a handler
// panic into a coded 500 JSON instead of a torn connection. The operator/agent routes had no
// per-route recovery before this, so a panic in a fleet/agent handler degraded the
// controller in exactly the mode rc.1 gates on.
func TestRecovered_MuxPanicReturns500JSON(t *testing.T) {
	server := NewServer()

	mux := http.NewServeMux()
	mux.HandleFunc("/boom", func(w http.ResponseWriter, r *http.Request) {
		panic("deliberate panic on a controller mux route")
	})
	h := server.recovered(mux)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/boom", nil))

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("panic on a mux route: status %d, want 500", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}
	var resp apiError
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode error body: %v", err)
	}
	if resp.Error.Message == "" {
		t.Errorf("recovered 500 must carry a non-empty error.message")
	}
}

func TestRecoverPanics_Returns500JSON(t *testing.T) {
	server := NewServer()

	panicking := func(w http.ResponseWriter, r *http.Request) {
		panic("deliberate panic for recovery test")
	}

	wrapped := server.recoverPanics(panicking)

	req := httptest.NewRequest(http.MethodPost, "/api/compile", nil)
	rec := httptest.NewRecorder()

	// The panic should not propagate up; the middleware must catch it and convert it to 500.
	wrapped(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("want 500, got %d", rec.Code)
	}

	ct := rec.Header().Get("Content-Type")
	if ct != "application/json" {
		t.Errorf("want Content-Type=application/json, got %q", ct)
	}

	var resp apiError
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("failed to decode error response: %v", err)
	}
	if resp.Error.Message == "" {
		t.Errorf("500 response should contain a non-empty error.message field")
	}
}

// TestRecoverPanics_PassesThroughNonPanicking verifies that a non-panicking handler
// behaves unchanged once wrapped by recoverPanics: both the status code and the response
// body pass through unmodified.
func TestRecoverPanics_PassesThroughNonPanicking(t *testing.T) {
	server := NewServer()

	ok := func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	}

	wrapped := server.recoverPanics(ok)

	req := httptest.NewRequest(http.MethodGet, "/api/health", nil)
	rec := httptest.NewRecorder()

	wrapped(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rec.Code)
	}

	var resp map[string]string
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if resp["status"] != "ok" {
		t.Errorf("want status=ok, got %q", resp["status"])
	}
}
