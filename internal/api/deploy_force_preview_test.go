package api

import (
	"net/http"
	"testing"
)

// TestHandleDeployPreview_Wiring verifies the plan-6 preview endpoint HTTP wiring (the changed/unchanged
// LOGIC is proven end-to-end in internal/regression). Operator-gated GET only.
func TestHandleDeployPreview_Wiring(t *testing.T) {
	env := newCtlTestEnv(t)
	// Auth GET → 200 (no stored topology → an empty preview, still 200).
	var pv deployPreviewResponseJSON
	if status := doJSON(t, http.MethodGet, env.opURL("deploy-preview"), testOperatorToken, nil, &pv); status != http.StatusOK {
		t.Fatalf("deploy-preview auth GET: status %d, want 200", status)
	}
	// No token → 401 (operator-gated).
	if status := doJSON(t, http.MethodGet, env.opURL("deploy-preview"), "", nil, nil); status != http.StatusUnauthorized {
		t.Errorf("deploy-preview no-token: status %d, want 401", status)
	}
	// Wrong method → 405.
	if status := doJSON(t, http.MethodPost, env.opURL("deploy-preview"), testOperatorToken, nil, nil); status != http.StatusMethodNotAllowed {
		t.Errorf("deploy-preview POST: status %d, want 405", status)
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
