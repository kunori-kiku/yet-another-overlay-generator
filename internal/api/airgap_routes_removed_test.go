//go:build !airgap

package api

// airgap_routes_removed_test.go — plan-7 / 1.7, Phase 5.1. The DEFAULT (controller-only) build's
// perpetual negative-routing suite. It runs in the default `go test ./...` and is constrained to
// //go:build !airgap so it does NOT compile into the -tags airgap build, where the four compute
// routes ARE registered (the positive counterpart in airgap_routes_present_test.go covers that
// build). The constraint is purely a test-selection guard — the SECURITY guarantee it pins is the
// default build itself, which is what `go test ./...` exercises.
//
// This pins the milestone-1.7 security invariant: NO anonymous/unauthenticated route reaches the
// keygen/allocator/compiler pipeline in the shipped controller. The four anonymous air-gap compute
// routes (/api/validate, /api/compile, /api/export, /api/deploy-script) are registered ONLY under
// -tags airgap (registerExtraRoutes in airgap_routes.go); in the default build registerExtraRoutes
// is a no-op stub (airgap_stubs.go), so those routes are not registered AND their handlers are not
// even linked. We assert that on the controller Server.Handler():
//
//   - GET and POST to each of the four compute routes return 404 (route not registered), and
//   - GET /api/health still returns 200 and is still CORS-wrapped (the public liveness probe is
//     present in BOTH builds, via the un-tagged registerRoutes + wrap).
//
// Together with the build tag excluding the handlers from the controller binary at link time, this
// is the proof of the security delta (a Subject-4 audit input). The positive counterpart — that the
// four routes ARE registered under -tags airgap — lives in airgap_routes_present_test.go behind
// //go:build airgap.

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// airgapComputeRoutesRemoved are the four anonymous compute routes that must be ABSENT from the
// default/controller build. (airgapComputeRoutes() in airgap_auth_gate_test.go lists the same set
// but lives behind //go:build airgap, so the un-tagged default suite needs its own copy.)
var airgapComputeRoutesRemoved = []string{
	"/api/validate",
	"/api/compile",
	"/api/export",
	"/api/deploy-script",
}

// TestAirgapRoutes_NotRegisteredInDefaultBuild asserts the four anonymous air-gap compute routes
// return 404 on the controller Server.Handler() for BOTH GET and POST — i.e. they are not
// registered in the default build, so no anonymous request reaches the compile pipeline. (404 is
// the ServeMux response for an unregistered pattern; a registered-but-wrong-method route would
// return 405, which would fail this test and flag an accidental re-introduction.)
func TestAirgapRoutes_NotRegisteredInDefaultBuild(t *testing.T) {
	server := NewServer()
	handler := server.Handler()

	for _, route := range airgapComputeRoutesRemoved {
		for _, method := range []string{http.MethodGet, http.MethodPost} {
			req := httptest.NewRequest(method, route, nil)
			rec := httptest.NewRecorder()

			handler.ServeHTTP(rec, req)

			if rec.Code != http.StatusNotFound {
				t.Errorf("%s %s = %d, want 404 (route must NOT be registered in the default/controller build)",
					method, route, rec.Code)
			}
		}
	}
}

// TestHealthStillRegisteredInDefaultBuild asserts the public liveness probe survives the build-tag
// split: GET /api/health returns 200 and carries CORS headers (still wrapped by wrap()), in the
// default build where the compute routes are gone.
func TestHealthStillRegisteredInDefaultBuild(t *testing.T) {
	server := NewServer()
	handler := server.Handler()

	req := httptest.NewRequest(http.MethodGet, "/api/health", nil)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/health = %d, want 200 (public liveness probe must survive in both builds)", rec.Code)
	}
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "*" {
		t.Errorf("GET /api/health Access-Control-Allow-Origin = %q, want %q (health must stay CORS-wrapped)", got, "*")
	}
}
