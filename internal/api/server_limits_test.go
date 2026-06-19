package api

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestRequestBodySizeCap_Returns413 verifies that a POST request body exceeding the 4 MiB
// cap is rejected by http.MaxBytesReader and mapped to 413 Payload Too Large (D34).
//
// plan-7 / 1.7: the body cap lives in readTopology, which is shared by the operator-only
// HandleCompilePreview (default build) and the air-gap compute routes (-tags airgap). With the
// air-gap routes gated out of the default build, this test retargets the cap assertion onto
// HandleCompilePreview (operator route /api/v1/operator/compile-preview) so it keeps guarding the
// readTopology body cap in the default suite. The oversized body is a single huge run of bytes, so
// http.MaxBytesReader trips on the cap during the read phase, before any JSON parse — and before
// CompileSubgraph — proving the cap is what rejects.
func TestRequestBodySizeCap_Returns413(t *testing.T) {
	env := newCtlTestEnv(t)

	// Build a request body slightly larger than maxRequestBodyBytes.
	oversized := bytes.Repeat([]byte("a"), int(maxRequestBodyBytes)+1024)
	req, err := http.NewRequest(http.MethodPost, env.opURL("compile-preview"), bytes.NewReader(oversized))
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	// Authenticate so the request reaches HandleCompilePreview → readTopology (the cap fires
	// inside the handler, after operator auth, before the compile).
	req.Header.Set("Authorization", "Bearer "+testOperatorToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST compile-preview: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusRequestEntityTooLarge {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("want 413, got %d, body: %s", resp.StatusCode, body)
	}

	// The error response must be of the form {"error":{code,message,params}} for the
	// frontend to display/localize.
	var env413 apiError
	if err := json.NewDecoder(resp.Body).Decode(&env413); err != nil {
		t.Fatalf("failed to decode error response: %v", err)
	}
	if env413.Error.Message == "" {
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

	// The path is a neutral label: recoverPanics is invoked directly on the handler (not via the
	// mux), so this exercises the middleware regardless of route. Retargeted off /api/compile in
	// plan-7 / 1.7 (that air-gap route is no longer registered in the default build).
	req := httptest.NewRequest(http.MethodPost, "/recover-test", nil)
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
