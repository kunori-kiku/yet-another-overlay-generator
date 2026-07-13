package api

// no_anonymous_compute_test.go — the perpetual attack-surface guard (framework-refactor plan-9).
//
// plan-9 retired the //go:build airgap two-deployment split: WASM is the proven in-browser local
// engine, so the four anonymous air-gap compute routes (POST /api/validate, /api/compile,
// /api/export, /api/deploy-script) and their handlers were DELETED, collapsing internal/api to ONE
// build. This test is the permanent proof that the anonymous compute route class stays gone. It
// replaces the retired build-tagged pair (airgap_routes_removed_test.go / airgap_routes_present_
// test.go) with a single un-tagged guard — there is only one build now, so the negative assertion
// is the whole story.
//
// It pins the milestone-1.7 security invariant that outlived the airgap build: NO anonymous /
// unauthenticated route reaches the keygen/allocator/compiler pipeline in the shipped controller.
// Two prongs:
//
//   1. Route-table guard: on the default Server.Handler(), GET and POST to each of the four routes
//      return 404 (the pattern is not registered), so a re-introduced anonymous route reds here.
//      /api/health stays a public 200 (+ CORS) — the one route that IS registered.
//   2. Link guard: no api-package PRODUCTION file declares one of the four anonymous compute
//      handlers, so a handler re-added to the package (even before it is wired to a route) reds
//      here too — the deletion cannot silently creep back at link time.

import (
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

// anonymousComputeRoutes are the four anonymous compute routes that must stay ABSENT from the
// (single) server build. Their presence would re-open the unauthenticated compute / key-gen oracle
// plan-9 deleted.
var anonymousComputeRoutes = []string{
	"/api/validate",
	"/api/compile",
	"/api/export",
	"/api/deploy-script",
}

// TestNoAnonymousComputeRoutes asserts the four anonymous compute routes return 404 on the default
// Server.Handler() for BOTH GET and POST — i.e. they are not registered, so no anonymous request
// reaches the compile pipeline. (404 is the ServeMux response for an unregistered pattern; a
// registered-but-wrong-method route would return 405, which would fail this test and flag an
// accidental re-introduction.)
func TestNoAnonymousComputeRoutes(t *testing.T) {
	server := NewServer()
	handler := server.Handler()

	for _, route := range anonymousComputeRoutes {
		for _, method := range []string{http.MethodGet, http.MethodPost} {
			req := httptest.NewRequest(method, route, nil)
			rec := httptest.NewRecorder()

			handler.ServeHTTP(rec, req)

			if rec.Code != http.StatusNotFound {
				t.Errorf("%s %s = %d, want 404 (the anonymous compute route class must stay absent — "+
					"framework-refactor plan-9 deleted the air-gap compute surface)", method, route, rec.Code)
			}
		}
	}
}

// TestHealthStillRegistered asserts the public liveness probe survives the airgap retirement:
// GET /api/health returns 200 and carries CORS headers (still wrapped by wrap()).
func TestHealthStillRegistered(t *testing.T) {
	server := NewServer()
	handler := server.Handler()

	req := httptest.NewRequest(http.MethodGet, "/api/health", nil)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/health = %d, want 200 (public liveness probe must survive)", rec.Code)
	}
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "*" {
		t.Errorf("GET /api/health Access-Control-Allow-Origin = %q, want %q (health must stay CORS-wrapped)", got, "*")
	}
}

// TestNoAnonymousComputeHandlersLinked scans the api package's PRODUCTION (.go, non-test) source
// and asserts none declares one of the four anonymous compute handlers. This is the link-level
// counterpart to the route-table guard above: it reds if a handler is re-added to the package even
// before it is wired to a route (dead-but-linked), so the deletion cannot silently creep back. The
// `func (h *Handler) HandleX(` declaration form (not the bare name) is matched, so a comment
// mentioning a handler does not trip it, and the surviving *ControllerHandler methods (e.g.
// HandleCompilePreview) never match.
func TestNoAnonymousComputeHandlersLinked(t *testing.T) {
	forbidden := []string{
		"func (h *Handler) HandleValidate(",
		"func (h *Handler) HandleCompile(",
		"func (h *Handler) HandleExport(",
		"func (h *Handler) HandleDeployScript(",
	}

	// `go test` runs with the package directory as the working directory, so "." is internal/api.
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read package dir: %v", err)
	}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue // production Go files only (skip this and every other _test.go)
		}
		src, err := os.ReadFile(name)
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		text := string(src)
		for _, sig := range forbidden {
			if strings.Contains(text, sig) {
				handlerName := strings.TrimSuffix(strings.TrimPrefix(sig, "func (h *Handler) "), "(")
				t.Errorf("%s declares %s — the anonymous compute handlers were deleted in "+
					"framework-refactor plan-9 and must not be re-added to the api package "+
					"(they are the unauthenticated compute / key-gen oracle plan-9 removed)", name, handlerName)
			}
		}
	}
}
