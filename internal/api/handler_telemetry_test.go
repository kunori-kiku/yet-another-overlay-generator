package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/kunorikiku/yet-another-overlay-generator/internal/probemetric"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/runtimecontract"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/telemetrymetric"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/telemetryprotocol"
)

// TestHandleTelemetry covers the controller side of the LIVE health heartbeat (beta9-smoke-hardening
// plan-1): POST /telemetry (per-node bearer, identity from the token) updates the node's conditions +
// last-seen but leaves the deploy-custody fields (AppliedGeneration / LastChecksum / LastHealth)
// UNTOUCHED; a wrong method is 405; an unauthenticated call is rejected by requireNode.
func TestHandleTelemetry(t *testing.T) {
	env := newCtlTestEnv(t)
	ctx := context.Background()
	token := env.enrollNode(t, "node-1")

	// Deploy baseline the heartbeat must not disturb.
	baseAt := time.Date(2026, 6, 23, 12, 0, 0, 0, time.UTC)
	if err := env.store.SetAppliedGeneration(ctx, testTenant, "node-1", 7, "csum-7", "applied", "v-old", nil, baseAt); err != nil {
		t.Fatalf("SetAppliedGeneration(baseline): %v", err)
	}

	// Happy path: a heartbeat with fresh conditions + a new agent version.
	body := telemetryRequestJSON{
		AgentVersion: "v-new",
		Conditions: []runtimecontract.Condition{{
			Type: runtimecontract.ConditionTypeWireGuard, Status: runtimecontract.ConditionStatusOK,
			Reason: "AllPeersUp", Message: "2/2 peers up", Since: "2026-06-23T12:00:25Z",
		}},
	}
	var resp map[string]string
	if status := doJSON(t, http.MethodPost, env.agentURL("telemetry"), token, body, &resp); status != http.StatusOK {
		t.Fatalf("POST /telemetry: status %d, want 200", status)
	}

	node, err := env.store.GetNode(ctx, testTenant, "node-1")
	if err != nil {
		t.Fatalf("GetNode: %v", err)
	}
	if len(node.Conditions) != 1 || node.Conditions[0].Reason != "AllPeersUp" {
		t.Fatalf("Conditions = %+v, want the live AllPeersUp set", node.Conditions)
	}
	if node.Conditions[0].ObservedAt.IsZero() {
		t.Fatalf("condition ObservedAt is zero, want server-stamped with the controller clock")
	}
	if node.LastAgentVersion != "v-new" {
		t.Fatalf("LastAgentVersion = %q, want v-new", node.LastAgentVersion)
	}
	if node.LastSeen.IsZero() {
		t.Fatalf("LastSeen is zero, want stamped by the heartbeat")
	}
	// CUSTODY: deploy state untouched.
	if node.AppliedGeneration != 7 || node.LastChecksum != "csum-7" || node.LastHealth != "applied" {
		t.Fatalf("deploy custody fields changed by telemetry: gen=%d checksum=%q health=%q (want 7/csum-7/applied)",
			node.AppliedGeneration, node.LastChecksum, node.LastHealth)
	}

	// Wrong method → 405.
	if status := doJSON(t, http.MethodGet, env.agentURL("telemetry"), token, nil, nil); status != http.StatusMethodNotAllowed {
		t.Fatalf("GET /telemetry: status %d, want 405", status)
	}
	// No bearer → rejected by requireNode (401), never reaches the handler.
	if status := doJSON(t, http.MethodPost, env.agentURL("telemetry"), "", body, nil); status != http.StatusUnauthorized {
		t.Fatalf("POST /telemetry (no token): status %d, want 401", status)
	}
}

// TestHandleTelemetry_MetricsRoundTrip pins the full metrics seam: a heartbeat carrying the
// extensible metrics map is admitted by the agent endpoint. Live-visible metrics are served verbatim
// to the operator under node.telemetry, while probe_samples remains history-only and is not echoed by
// every Fleet refresh. A nil-metrics heartbeat then clears the latest map.
func TestHandleTelemetry_MetricsRoundTrip(t *testing.T) {
	env := newCtlTestEnv(t)
	token := env.enrollNode(t, "node-1")
	sampledAt := time.Now().UTC().Truncate(time.Second)
	latency := 6.25
	probeSamples, err := json.Marshal([]probemetric.Result{{
		ID: "dns", Type: "icmp", Host: "resolver.example", Status: probemetric.StatusSuccess,
		LatencyMS: &latency, CheckedAt: sampledAt.Format(time.RFC3339Nano), IntervalMS: 30_000,
	}})
	if err != nil {
		t.Fatal(err)
	}

	body := telemetryRequestJSON{
		Metrics: map[string]json.RawMessage{
			telemetrymetric.WireGuardPeers.Key: json.RawMessage(`[{"peer":"bravo","interface":"wg-bravo","last_handshake":1782820825,"status":"up"}]`),
			telemetrymetric.ProbeSamples.Key:   probeSamples,
		},
	}
	if status := doJSON(t, http.MethodPost, env.agentURL("telemetry"), token, body, nil); status != http.StatusOK {
		t.Fatalf("POST /telemetry (metrics): status %d, want 200", status)
	}

	// The operator /nodes view serves node.telemetry verbatim.
	var nodes []nodeJSON
	if status := doJSON(t, http.MethodGet, env.opURL("nodes"), testOperatorToken, nil, &nodes); status != http.StatusOK {
		t.Fatalf("GET /nodes: status %d, want 200", status)
	}
	var found *nodeJSON
	for i := range nodes {
		if nodes[i].NodeID == "node-1" {
			found = &nodes[i]
		}
	}
	if found == nil {
		t.Fatalf("node-1 not in /nodes response")
	}
	raw, ok := found.Telemetry[telemetrymetric.WireGuardPeers.Key]
	if !ok || !bytes.Contains(raw, []byte("bravo")) {
		t.Fatalf("served node.telemetry[wireguard_peers] = %s (ok=%v), want the per-peer payload", raw, ok)
	}
	if _, leaked := found.Telemetry[telemetrymetric.ProbeSamples.Key]; leaked {
		t.Fatalf("history-only probe_samples leaked through GET /nodes: %+v", found.Telemetry)
	}
	probeHistory, err := env.store.QueryTelemetryProbeHistory(context.Background(), testTenant, "node-1", sampledAt.Add(-time.Second), sampledAt.Add(time.Second))
	if err != nil || len(probeHistory) != 1 || probeHistory[0].ID != "dns" || probeHistory[0].IntervalMS != 30_000 {
		t.Fatalf("probe_samples hidden from /nodes but not retained in history: %+v err=%v", probeHistory, err)
	}

	// A nil-metrics heartbeat clears the served map.
	if status := doJSON(t, http.MethodPost, env.agentURL("telemetry"), token, telemetryRequestJSON{}, nil); status != http.StatusOK {
		t.Fatalf("POST /telemetry (no metrics): status %d, want 200", status)
	}
	nodes = nil
	if status := doJSON(t, http.MethodGet, env.opURL("nodes"), testOperatorToken, nil, &nodes); status != http.StatusOK {
		t.Fatalf("GET /nodes (after clear): status %d, want 200", status)
	}
	for i := range nodes {
		if nodes[i].NodeID == "node-1" && nodes[i].Telemetry != nil {
			t.Fatalf("node.telemetry = %+v, want nil after a nil-metrics heartbeat", nodes[i].Telemetry)
		}
	}
}

func TestHandleTelemetry_ReliableHeadersAndLegacyBodyCompatibility(t *testing.T) {
	env := newCtlTestEnv(t)
	token := env.enrollNode(t, "node-1")
	bootID := "00112233445566778899aabbccddeeff"
	sampledAt := time.Now().UTC().Add(-time.Minute).Truncate(time.Second)

	post := func(sequence string, sampled string, body telemetryRequestJSON) *http.Response {
		t.Helper()
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		req, err := http.NewRequest(http.MethodPost, env.agentURL("telemetry"), bytes.NewReader(raw))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set(telemetryprotocol.HeaderProtocol, telemetryprotocol.Version)
		req.Header.Set(telemetryprotocol.HeaderBootID, bootID)
		req.Header.Set(telemetryprotocol.HeaderSequence, sequence)
		req.Header.Set(telemetryprotocol.HeaderIntervalMillis, "17000")
		if sampled != "" {
			req.Header.Set(telemetryprotocol.HeaderSampledAt, sampled)
		}
		resp, err := env.agentSrv.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		return resp
	}

	probeLatency := 12.5
	probeRaw, err := json.Marshal([]map[string]any{{
		"id": "tcp-main", "type": "tcp", "host": "example.net", "port": 443,
		"status": "success", "latency_ms": 12.5, "checked_at": sampledAt.Format(time.RFC3339Nano),
	}})
	if err != nil {
		t.Fatal(err)
	}
	first := post("1", sampledAt.Format(time.RFC3339Nano), telemetryRequestJSON{
		Metrics: map[string]json.RawMessage{
			telemetrymetric.ProbeResults.Key: probeRaw,
			telemetrymetric.ProbeSamples.Key: apiProbeMetric(t, probemetric.Result{
				ID: "tcp-main", Type: "tcp", Host: "example.net", Port: 443,
				Status: probemetric.StatusSuccess, LatencyMS: &probeLatency,
				CheckedAt: sampledAt.Format(time.RFC3339Nano), IntervalMS: 30_000,
			}),
			telemetrymetric.Resource.Key: json.RawMessage(`{"load1":1}`),
		},
	})
	defer first.Body.Close()
	if first.StatusCode != http.StatusOK || first.Header.Get(telemetryprotocol.HeaderProtocol) != telemetryprotocol.Version ||
		first.Header.Get(telemetryprotocol.HeaderBootID) != bootID || first.Header.Get(telemetryprotocol.HeaderAckSequence) != "1" ||
		!telemetryprotocol.HasCapability(first.Header.Get(telemetryprotocol.HeaderCapabilities), telemetryprotocol.CapabilityProbeSamplesV1) {
		t.Fatalf("first response status=%d headers=%v", first.StatusCode, first.Header)
	}
	receivedAt, err := time.Parse(time.RFC3339Nano, first.Header.Get(telemetryprotocol.HeaderReceivedAt))
	if err != nil || receivedAt.IsZero() {
		t.Fatalf("received-at header %q: %v", first.Header.Get(telemetryprotocol.HeaderReceivedAt), err)
	}

	duplicate := post("1", sampledAt.Format(time.RFC3339Nano), telemetryRequestJSON{
		Metrics: map[string]json.RawMessage{telemetrymetric.ProbeResults.Key: json.RawMessage(`[{"id":"tcp-wrong","type":"tcp","host":"wrong.example","port":1,"status":"failure"}]`)},
	})
	defer duplicate.Body.Close()
	if duplicate.StatusCode != http.StatusOK || duplicate.Header.Get(telemetryprotocol.HeaderAckSequence) != "1" || duplicate.Header.Get(telemetryprotocol.HeaderDuplicate) != "true" ||
		!telemetryprotocol.HasCapability(duplicate.Header.Get(telemetryprotocol.HeaderCapabilities), telemetryprotocol.CapabilityProbeSamplesV1) {
		t.Fatalf("duplicate response status=%d headers=%v", duplicate.StatusCode, duplicate.Header)
	}
	node, err := env.store.GetNode(context.Background(), testTenant, "node-1")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(node.Telemetry[telemetrymetric.ProbeResults.Key], []byte("example.net")) || bytes.Contains(node.Telemetry[telemetrymetric.ProbeResults.Key], []byte("wrong.example")) {
		t.Fatalf("duplicate replaced probe payload: %s", node.Telemetry[telemetrymetric.ProbeResults.Key])
	}
	if _, leaked := node.Telemetry[telemetrymetric.ProbeSamples.Key]; leaked {
		t.Fatalf("sequenced heartbeat leaked history-only probe_samples onto live telemetry: %+v", node.Telemetry)
	}
	if node.LastSeen.Before(receivedAt) {
		t.Fatalf("LastSeen %v is before first controller received-at %v", node.LastSeen, receivedAt)
	}

	history, err := env.store.QueryTelemetryHistory(context.Background(), testTenant, "node-1", sampledAt.Add(-time.Second), sampledAt.Add(time.Second))
	if err != nil || len(history) != 1 || history[0].IntervalMS != 17000 {
		t.Fatalf("cadence-aware history=%+v err=%v", history, err)
	}
	probeHistory, err := env.store.QueryTelemetryProbeHistory(context.Background(), testTenant, "node-1", sampledAt.Add(-time.Second), sampledAt.Add(time.Second))
	if err != nil || len(probeHistory) != 1 || probeHistory[0].ID != "tcp-main" || probeHistory[0].LatencyMS == nil || *probeHistory[0].LatencyMS != 12.5 || probeHistory[0].IntervalMS != 30_000 {
		t.Fatalf("probe fallback history=%+v err=%v", probeHistory, err)
	}

	malformed := post("2", "", telemetryRequestJSON{})
	defer malformed.Body.Close()
	if malformed.StatusCode != http.StatusBadRequest {
		t.Fatalf("missing sampled-at status=%d, want 400", malformed.StatusCode)
	}

	// The JSON contract itself remains legacy-only; no reliable metadata field is added to the body,
	// so strict old controllers using DisallowUnknownFields continue to accept a new agent request.
	raw, _ := json.Marshal(telemetryRequestJSON{})
	var decoded map[string]json.RawMessage
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"boot_id", "sequence", "sampled_at", "received_at"} {
		if _, present := decoded[forbidden]; present {
			t.Fatalf("reliable metadata leaked into legacy JSON body as %q", forbidden)
		}
	}
}

func TestTelemetryInterval_IsAdvisoryAndTolerant(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want time.Duration
	}{
		{name: "missing"},
		{name: "valid", raw: "17000", want: 17 * time.Second},
		{name: "zero ignored", raw: "0"},
		{name: "negative ignored", raw: "-1"},
		{name: "malformed ignored", raw: "not-a-number"},
		{name: "duration overflow ignored", raw: "9223372036854775807"},
		{name: "oversized ignored", raw: "999999999999999999999"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/telemetry", nil)
			if tt.raw != "" {
				req.Header.Set(telemetryprotocol.HeaderIntervalMillis, tt.raw)
			}
			if got := telemetryInterval(req); got != tt.want {
				t.Fatalf("telemetryInterval(%q)=%v, want %v", tt.raw, got, tt.want)
			}
		})
	}
}
