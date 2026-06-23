package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/kunorikiku/yet-another-overlay-generator/internal/model"
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
		Conditions: []model.Condition{{
			Type: model.ConditionTypeWireGuard, Status: model.ConditionStatusOK,
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
// extensible metrics map (wireguard_peers — the per-peer link detail) is persisted by the agent
// endpoint and served VERBATIM to the operator under node.telemetry, so the panel can render the
// collapsible per-link panel. A nil-metrics heartbeat then clears it.
func TestHandleTelemetry_MetricsRoundTrip(t *testing.T) {
	env := newCtlTestEnv(t)
	token := env.enrollNode(t, "node-1")

	body := telemetryRequestJSON{
		Metrics: map[string]json.RawMessage{
			"wireguard_peers": json.RawMessage(`[{"peer":"bravo","interface":"wg-bravo","last_handshake":1782820825,"status":"up"}]`),
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
	raw, ok := found.Telemetry["wireguard_peers"]
	if !ok || !bytes.Contains(raw, []byte("bravo")) {
		t.Fatalf("served node.telemetry[wireguard_peers] = %s (ok=%v), want the per-peer payload", raw, ok)
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
