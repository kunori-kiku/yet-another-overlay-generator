package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/kunorikiku/yet-another-overlay-generator/internal/model"
)

// TestNodeLimiter: the per-node request-rate limiter (F1) admits exactly maxNodeRequestsPerWindow
// requests per node per window (used WITHOUT succeed(), so every request counts), rejects the next with
// a positive, bounded Retry-After, re-admits after the window, and keeps distinct node buckets
// independent (one abusive node cannot lock out the fleet).
func TestNodeLimiter(t *testing.T) {
	l := newLimiter(maxNodeRequestsPerWindow, nodeRateWindow)
	now := time.Date(2026, 7, 2, 12, 0, 0, 0, time.UTC)
	const alpha, bravo = "node:alpha", "node:bravo"

	for i := 0; i < maxNodeRequestsPerWindow; i++ {
		if allowed, _, _ := l.registerAttempt(now, alpha); !allowed {
			t.Fatalf("alpha request %d rejected, want admitted (cap=%d)", i+1, maxNodeRequestsPerWindow)
		}
	}
	allowed, _, retry := l.registerAttempt(now, alpha)
	if allowed {
		t.Fatal("alpha admitted past the per-node cap")
	}
	if retry <= 0 || retry > nodeRateWindow {
		t.Fatalf("retry = %v, want 0 < retry <= window", retry)
	}
	// A DIFFERENT node is unaffected — buckets are independent.
	if allowed, _, _ := l.registerAttempt(now, bravo); !allowed {
		t.Fatal("bravo rejected — per-node buckets must be independent")
	}
	// After the window, alpha is admitted again (fresh window).
	if allowed, _, _ := l.registerAttempt(now.Add(nodeRateWindow+time.Second), alpha); !allowed {
		t.Fatal("alpha still rejected after the window elapsed")
	}
}

// TestClientIP covers trusted-proxy-aware source-IP resolution (F4): without trusted proxies the direct
// peer is used and forwarding headers are ignored (unspoofable); with a trusted direct peer the real
// client is the rightmost UNtrusted X-Forwarded-For hop (skipping trusted proxies); a forged XFF from
// an untrusted peer is ignored; X-Real-IP is honored only from a trusted peer.
func TestClientIP(t *testing.T) {
	mk := func(remote, xff, xrealip string) *http.Request {
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		r.RemoteAddr = remote
		if xff != "" {
			r.Header.Set("X-Forwarded-For", xff)
		}
		if xrealip != "" {
			r.Header.Set("X-Real-IP", xrealip)
		}
		return r
	}
	cases := []struct {
		name    string
		trusted []string
		remote  string
		xff     string
		xrealip string
		want    string
	}{
		{"no trusted proxies: direct peer, XFF ignored", nil, "203.0.113.9:5000", "1.2.3.4", "", "203.0.113.9"},
		{"untrusted direct peer: forged XFF ignored", []string{"10.0.0.0/8"}, "203.0.113.9:5000", "1.2.3.4", "", "203.0.113.9"},
		{"trusted proxy: rightmost-untrusted XFF is the client", []string{"10.0.0.0/8"}, "10.1.2.3:443", "9.9.9.9, 198.51.100.7", "", "198.51.100.7"},
		{"trusted chain: skip trusted hops right-to-left", []string{"10.0.0.0/8"}, "10.1.2.3:443", "198.51.100.7, 10.4.5.6", "", "198.51.100.7"},
		{"trusted proxy, X-Real-IP fallback", []string{"10.0.0.0/8"}, "10.1.2.3:443", "", "198.51.100.7", "198.51.100.7"},
		{"trusted proxy, no forwarding headers: RemoteAddr", []string{"10.0.0.0/8"}, "10.1.2.3:443", "", "", "10.1.2.3"},
		{"bare-IP trusted entry", []string{"10.1.2.3"}, "10.1.2.3:443", "198.51.100.7", "", "198.51.100.7"},
		{"malformed XFF entry skipped", []string{"10.0.0.0/8"}, "10.1.2.3:443", "not-an-ip, 198.51.100.7", "", "198.51.100.7"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := &ControllerHandler{}
			h.SetTrustedProxies(tc.trusted)
			if got := h.clientIP(mk(tc.remote, tc.xff, tc.xrealip)); got != tc.want {
				t.Fatalf("clientIP = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestHandleTelemetryBounds (F2): the /telemetry handler rejects an over-count conditions slice, an
// over-long condition Message, and an over-count / over-size metrics map with 400; an at-limit payload
// succeeds.
func TestHandleTelemetryBounds(t *testing.T) {
	env := newCtlTestEnv(t)
	token := env.enrollNode(t, "node-1")

	manyConds := func(n int) []model.Condition {
		out := make([]model.Condition, n)
		for i := range out {
			out[i] = model.Condition{Type: model.ConditionTypeWireGuard, Status: model.ConditionStatusOK, Reason: "R", Message: "ok"}
		}
		return out
	}
	manyMetrics := func(n int) map[string]json.RawMessage {
		out := make(map[string]json.RawMessage, n)
		for i := 0; i < n; i++ {
			out["k"+strings.Repeat("x", i%3)+string(rune('a'+i%26))+string(rune('0'+i/26))] = json.RawMessage(`1`)
		}
		return out
	}

	// Over-count conditions → 400.
	if status := doJSON(t, http.MethodPost, env.agentURL("telemetry"), token,
		telemetryRequestJSON{Conditions: manyConds(maxReportedConditions + 1)}, nil); status != http.StatusBadRequest {
		t.Fatalf("over-count conditions: status %d, want 400", status)
	}
	// Over-long condition Message → 400.
	if status := doJSON(t, http.MethodPost, env.agentURL("telemetry"), token,
		telemetryRequestJSON{Conditions: []model.Condition{{Type: model.ConditionTypeWireGuard, Status: model.ConditionStatusOK, Message: strings.Repeat("x", model.ConditionMessageMax+1)}}}, nil); status != http.StatusBadRequest {
		t.Fatalf("over-long message: status %d, want 400", status)
	}
	// Over-count metrics → 400.
	if status := doJSON(t, http.MethodPost, env.agentURL("telemetry"), token,
		telemetryRequestJSON{Metrics: manyMetrics(maxTelemetryMetrics + 1)}, nil); status != http.StatusBadRequest {
		t.Fatalf("over-count metrics: status %d, want 400", status)
	}
	// Over-size metrics → 400 (one huge value).
	if status := doJSON(t, http.MethodPost, env.agentURL("telemetry"), token,
		telemetryRequestJSON{Metrics: map[string]json.RawMessage{"big": json.RawMessage(`"` + strings.Repeat("A", maxTelemetryMetricsBytes+1) + `"`)}}, nil); status != http.StatusBadRequest {
		t.Fatalf("over-size metrics: status %d, want 400", status)
	}
	// At-limit conditions succeed.
	if status := doJSON(t, http.MethodPost, env.agentURL("telemetry"), token,
		telemetryRequestJSON{Conditions: manyConds(maxReportedConditions)}, nil); status != http.StatusOK {
		t.Fatalf("at-limit conditions: status %d, want 200", status)
	}
}
