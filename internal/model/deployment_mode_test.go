package model

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestNodeIsManual covers the deployment-mode helper: only an explicit "manual" is manual; empty
// (the back-compat default) and "managed" are managed.
func TestNodeIsManual(t *testing.T) {
	cases := map[string]bool{"": false, "managed": false, "manual": true, "Manual": false, "bogus": false}
	for mode, want := range cases {
		n := Node{DeploymentMode: mode}
		if got := n.IsManual(); got != want {
			t.Errorf("IsManual(deployment_mode=%q) = %v, want %v", mode, got, want)
		}
	}
}

// TestNodeDeploymentModeOmitempty pins the wire back-compat: a managed (empty) node serializes WITHOUT
// the deployment_mode key, so every pre-existing topology is byte-unchanged; a manual node carries it.
func TestNodeDeploymentModeOmitempty(t *testing.T) {
	managed, err := json.Marshal(Node{ID: "n1", Role: "router"})
	if err != nil {
		t.Fatalf("marshal managed: %v", err)
	}
	if strings.Contains(string(managed), "deployment_mode") {
		t.Errorf("a managed (empty) node must omit deployment_mode, got %s", managed)
	}

	manual, err := json.Marshal(Node{ID: "n2", Role: "router", DeploymentMode: DeploymentManual})
	if err != nil {
		t.Fatalf("marshal manual: %v", err)
	}
	if !strings.Contains(string(manual), `"deployment_mode":"manual"`) {
		t.Errorf("a manual node must carry deployment_mode, got %s", manual)
	}

	// Round-trip: an absent key rehydrates to empty (managed).
	var back Node
	if err := json.Unmarshal([]byte(`{"id":"n3","role":"router"}`), &back); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if back.DeploymentMode != "" || back.IsManual() {
		t.Errorf("absent deployment_mode must rehydrate as managed, got %q", back.DeploymentMode)
	}
}
