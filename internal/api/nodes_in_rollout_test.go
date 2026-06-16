package api

import (
	"context"
	"net/http"
	"testing"

	"github.com/kunorikiku/yet-another-overlay-generator/internal/controller"
)

// TestNodes_InRollout asserts HandleNodes stamps each node's in_rollout flag from
// AgentRolloutNodeIDs: empty when no rollout is configured, the canary subset during canary, and
// the whole fleet once promoted. Membership is target-independent (the panel's chip gates on the
// configured target separately) — this verifies only the server-computed membership echo.
func TestNodes_InRollout(t *testing.T) {
	env := newCtlTestEnv(t)
	env.enrollNode(t, "node-1")
	env.enrollNode(t, "node-2")

	getNodes := func() []nodeJSON {
		t.Helper()
		var nodes []nodeJSON
		if status := doJSON(t, http.MethodGet, env.opURL("nodes"), testOperatorToken, nil, &nodes); status != http.StatusOK {
			t.Fatalf("GET nodes: status %d, want 200", status)
		}
		return nodes
	}
	assertInRollout := func(nodes []nodeJSON, want map[string]bool) {
		t.Helper()
		got := make(map[string]bool, len(nodes))
		for _, n := range nodes {
			got[n.NodeID] = n.InRollout
		}
		for id, w := range want {
			if got[id] != w {
				t.Errorf("node %s in_rollout = %v, want %v", id, got[id], w)
			}
		}
	}
	putSettings := func(cs controller.ControllerSettings) {
		t.Helper()
		if err := env.store.PutSettings(context.Background(), testTenant, cs); err != nil {
			t.Fatalf("PutSettings: %v", err)
		}
	}

	// No settings record → no rollout → both not-targeted.
	assertInRollout(getNodes(), map[string]bool{"node-1": false, "node-2": false})

	// Canary = node-1 → only node-1 in rollout.
	putSettings(controller.ControllerSettings{
		TargetAgentVersion: "v2.0.0-beta.3",
		AgentCanaryNodeIDs: []string{"node-1"},
	})
	assertInRollout(getNodes(), map[string]bool{"node-1": true, "node-2": false})

	// Promote fleet-wide → every enrolled node in rollout.
	putSettings(controller.ControllerSettings{
		TargetAgentVersion:    "v2.0.0-beta.3",
		AgentRolloutFleetWide: true,
	})
	assertInRollout(getNodes(), map[string]bool{"node-1": true, "node-2": true})
}
