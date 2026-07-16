package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/kunorikiku/yet-another-overlay-generator/internal/apierr"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/model"
)

// TestHandleDeployPreview_Wiring verifies the plan-6 preview endpoint HTTP wiring (the changed/unchanged
// LOGIC is proven end-to-end in internal/regression). Operator-gated POST of the current canvas.
func TestHandleDeployPreview_Wiring(t *testing.T) {
	env := newCtlTestEnv(t)
	// A minimal valid canvas (public-keys-only). No enrolled nodes → an empty preview, still 200.
	body := map[string]any{
		"project": map[string]any{"id": "p", "name": "P"},
		"domains": []any{map[string]any{"id": "d1", "name": "net", "cidr": "10.0.0.0/24", "allocation_mode": "auto", "routing_mode": "babel"}},
		"nodes":   []any{},
		"edges":   []any{},
	}
	var pv deployPreviewResponseJSON
	if status := doJSON(t, http.MethodPost, env.opURL("deploy-preview"), testOperatorToken, body, &pv); status != http.StatusOK {
		t.Fatalf("deploy-preview auth POST: status %d, want 200", status)
	}
	// No token → 401 (operator-gated).
	if status := doJSON(t, http.MethodPost, env.opURL("deploy-preview"), "", body, nil); status != http.StatusUnauthorized {
		t.Errorf("deploy-preview no-token: status %d, want 401", status)
	}
	// Wrong method (GET) → 405.
	if status := doJSON(t, http.MethodGet, env.opURL("deploy-preview"), testOperatorToken, nil, nil); status != http.StatusMethodNotAllowed {
		t.Errorf("deploy-preview GET: status %d, want 405", status)
	}
}

// TestHandleStage_ForceBodyAccepted verifies HandleStage parses the optional plan-6 force body without
// error (an empty topology makes the stage a benign no-op; the force LOGIC is proven in regression).
func TestHandleStage_ForceBodyAccepted(t *testing.T) {
	env := newCtlTestEnv(t)
	var resp stageResponseJSON
	if status := doJSON(t, http.MethodPost, env.opURL("stage"), testOperatorToken, map[string]any{"force_all": true}, &resp); status != http.StatusOK {
		t.Fatalf("stage with a force_all body: status %d, want 200", status)
	}
	if status := doJSON(t, http.MethodPost, env.opURL("stage"), testOperatorToken, map[string]any{"force_nodes": []string{"node-1"}}, &resp); status != http.StatusOK {
		t.Fatalf("stage with a force_nodes body: status %d, want 200", status)
	}
	// A stage with NO body is still accepted (force is optional).
	if status := doJSON(t, http.MethodPost, env.opURL("stage"), testOperatorToken, nil, &resp); status != http.StatusOK {
		t.Fatalf("stage with no body: status %d, want 200", status)
	}
}

func TestActiveTelemetryHTTPRequiresKeystone(t *testing.T) {
	env := newCtlTestEnv(t)
	env.enrollNode(t, "node-1")
	env.enrollNode(t, "node-2")

	topo := smallTopo()
	topo.Nodes[0].TelemetryProbes = []model.TelemetryProbe{{
		ID: "dns", Type: model.TelemetryProbeICMP, Host: "resolver.example",
	}}

	type codedEnvelope struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	post := func(url string, body any) (int, string) {
		t.Helper()
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(raw))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+testOperatorToken)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var envelope codedEnvelope
		if err := json.NewDecoder(resp.Body).Decode(&envelope); err != nil {
			t.Fatalf("decode coded response: %v", err)
		}
		return resp.StatusCode, envelope.Error.Code
	}

	status, code := post(env.opURL("deploy-preview"), topo)
	if status != http.StatusPreconditionFailed || code != string(apierr.CodeTelemetryProbesRequireKeystone) {
		t.Fatalf("deploy-preview = %d/%q, want 412/%q", status, code, apierr.CodeTelemetryProbesRequireKeystone)
	}

	raw, err := json.Marshal(topo)
	if err != nil {
		t.Fatal(err)
	}
	if status := doRaw(t, http.MethodPost, env.opURL("update-topology"), testOperatorToken, raw); status != http.StatusOK {
		t.Fatalf("update-topology: status %d, want 200", status)
	}
	status, code = post(env.opURL("stage"), struct{}{})
	if status != http.StatusPreconditionFailed || code != string(apierr.CodeTelemetryProbesRequireKeystone) {
		t.Fatalf("stage = %d/%q, want 412/%q", status, code, apierr.CodeTelemetryProbesRequireKeystone)
	}
	if status := doJSON(t, http.MethodPost, env.opURL("promote"), testOperatorToken, struct{}{}, nil); status != http.StatusConflict {
		t.Fatalf("promote after refused stage: status %d, want 409", status)
	}
}
