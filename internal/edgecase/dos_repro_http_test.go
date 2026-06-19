//go:build airgap

// dos_repro_http_test.go — the DoS regression oracle's CURRENT-STATE HTTP tier (plan-16 / 3.4,
// Phase 4). It POSTs the worst adversarial corpus entries to the anonymous air-gap /api/compile
// route via httptest, exercising the real HTTP path (4 MiB body cap, the recover/cors/gateAirgap
// chain in internal/api/server.go) — not just the compiler.Compile entry the durable tier uses.
//
// This file is tagged //go:build airgap ON PURPOSE: /api/compile is registered/linked ONLY in the
// -tags airgap build (internal/api/airgap_routes.go), so this tier can only exist there. It is a
// TODAY-ONLY measurement of the anonymous air-gap amplification surface (report §3b / R7): when
// Subject 1's TS cutover removes the anonymous /api/compile route (4.3:170), this tier retires (or
// relocates to a controller-mode-only test). The DURABLE oracle is dos_repro_test.go's
// compiler.Compile tier, which survives that cutover.
//
// Invariant asserted: the HTTP surface answers every worst-case input with a bounded coded
// response within a deadline — never a hang, never a torn connection. S1 specifically must be the
// 422 scan-budget rejection (plan-8's cap, surfaced through the HTTP layer).

package edgecase

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/kunorikiku/yet-another-overlay-generator/internal/api"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/model"
)

// httpDeadline bounds a single /api/compile round-trip; exceeding it means the HTTP surface hung on
// an adversarial body (a finding), not a slow machine.
const httpDeadline = 20 * time.Second

// postCompile serializes topo and POSTs it to /api/compile on a fresh air-gap server, returning the
// status code and the wall time. It fails the test if the handler does not respond within the
// deadline.
func postCompile(t *testing.T, topo model.Topology) (int, time.Duration) {
	t.Helper()
	body, err := json.Marshal(topo)
	if err != nil {
		t.Fatalf("marshal topology: %v", err)
	}

	server := api.NewServer()
	ctx, cancel := context.WithTimeout(context.Background(), httpDeadline)
	defer cancel()
	req := httptest.NewRequest(http.MethodPost, "/api/compile", bytes.NewReader(body)).WithContext(ctx)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	done := make(chan struct{})
	start := time.Now()
	go func() {
		server.Handler().ServeHTTP(rec, req)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(httpDeadline):
		t.Fatalf("/api/compile did not respond within %s — HTTP-surface hang on an adversarial body", httpDeadline)
	}
	return rec.Code, time.Since(start)
}

// TestDoSHTTPScanBudget (S1, HTTP tier) — the /8 fixture POSTed to /api/compile must come back as a
// bounded 422 (the scan-budget rejection surfaced through the HTTP layer), fast.
func TestDoSHTTPScanBudget(t *testing.T) {
	code, elapsed := postCompile(t, dosAllocatorReserved(64, 64))
	t.Logf("S1 HTTP /api/compile (/8+reserved): %d in %s", code, elapsed)
	if code != http.StatusUnprocessableEntity {
		t.Fatalf("S1 HTTP: want 422 (scan-budget rejection), got %d", code)
	}
}

// TestDoSHTTPUnboundedDomains (S2, HTTP tier) — a many-domains body must return a bounded response
// (200 for a legal topology) within the deadline, not hang the request goroutine.
func TestDoSHTTPUnboundedDomains(t *testing.T) {
	if testing.Short() {
		t.Skip("heavy DoS repro; skipped under -short")
	}
	code, elapsed := postCompile(t, dosManyDomains(500))
	t.Logf("S2 HTTP /api/compile (500 domains): %d in %s", code, elapsed)
	if code != http.StatusOK {
		t.Fatalf("S2 HTTP: a large-but-legal many-domains body should compile to 200, got %d", code)
	}
}
