package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/kunorikiku/yet-another-overlay-generator/internal/controller"
)

// newVersionEnv stands up the operator mux with a specific controller build version (plan-7),
// mirroring newCookieEnv but threading the version through NewControllerHandler.
func newVersionEnv(t *testing.T, version string) *httptest.Server {
	t.Helper()
	store := controller.NewMemStore()
	op, err := controller.NewOperator("admin", "correct-password", time.Now().UTC())
	if err != nil {
		t.Fatalf("NewOperator: %v", err)
	}
	if err := store.PutOperator(context.Background(), testTenant, op); err != nil {
		t.Fatalf("PutOperator: %v", err)
	}
	ch := NewControllerHandler(store, testTenant, "", DefaultOperatorName, version)
	ch.SetSecureCookie(false)
	mux := http.NewServeMux()
	ch.RegisterOperatorRoutes(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func sessionControllerVersion(t *testing.T, srv *httptest.Server, session, csrf *http.Cookie) string {
	t.Helper()
	resp := doCookieReq(t, srv, http.MethodGet, "session", []*http.Cookie{session, csrf}, "")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("session = %d, want 200", resp.StatusCode)
	}
	var r sessionResponseJSON
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		t.Fatalf("decode session: %v", err)
	}
	return r.ControllerVersion
}

// TestSessionReportsControllerVersion: a stamped build surfaces its version on the operator session.
func TestSessionReportsControllerVersion(t *testing.T) {
	srv := newVersionEnv(t, "v2.0.0-beta.9")
	session, csrf, _ := loginForCookies(t, srv)
	if got := sessionControllerVersion(t, srv, session, csrf); got != "v2.0.0-beta.9" {
		t.Fatalf("controller_version = %q, want v2.0.0-beta.9", got)
	}
}

// TestSessionVersionDefaultsToDev: an unstamped build ("" → ctor normalizes) reports "dev".
func TestSessionVersionDefaultsToDev(t *testing.T) {
	srv := newVersionEnv(t, "")
	session, csrf, _ := loginForCookies(t, srv)
	if got := sessionControllerVersion(t, srv, session, csrf); got != "dev" {
		t.Fatalf("controller_version = %q, want dev (empty→dev normalization)", got)
	}
}

// TestSessionVersionRequiresAuth: the version rides ONLY the authenticated session — an
// unauthenticated /session is refused (never a 200 that would leak the version). The controller
// version lives solely in sessionResponseJSON (HandleSession, operator-gated); no anonymous surface
// (e.g. /api/health, a fixed body that never references it) can emit it.
func TestSessionVersionRequiresAuth(t *testing.T) {
	srv := newVersionEnv(t, "v2.0.0-beta.9")
	resp := doCookieReq(t, srv, http.MethodGet, "session", nil, "")
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusOK {
		t.Fatalf("unauthenticated /session must not be 200 (would leak the controller version)")
	}
}
