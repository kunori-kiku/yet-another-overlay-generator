package controller

import "testing"

// TestAgentRolloutNodeIDs is the direct guard for the canary-then-fleet membership rule
// (the per-node in_rollout flag the panel reads is a verbatim echo of this map). It pins the
// intersection: an unenrolled canary id must be dropped (not echoed raw), fleet-wide includes
// every enrolled node, and no configuration yields the empty set.
func TestAgentRolloutNodeIDs(t *testing.T) {
	nodes := []Node{{NodeID: "node-1"}, {NodeID: "node-2"}}

	// Canary subset INTERSECTED with the actual nodes: an unenrolled "ghost" canary id is
	// dropped. A regression that returned the raw canary set would leave ghost in the map.
	canary := AgentRolloutNodeIDs(ControllerSettings{
		AgentCanaryNodeIDs: []string{"node-1", "ghost"},
	}, nodes)
	if !canary["node-1"] {
		t.Error("node-1 (an enrolled canary) should be in the rollout")
	}
	if canary["node-2"] {
		t.Error("node-2 (not a canary) should not be in the rollout")
	}
	if canary["ghost"] {
		t.Error("an unenrolled canary id must be intersected out, not echoed raw")
	}

	// Fleet-wide promotion: every enrolled node, regardless of the canary list.
	fleet := AgentRolloutNodeIDs(ControllerSettings{
		AgentRolloutFleetWide: true,
		AgentCanaryNodeIDs:    []string{"node-1"},
	}, nodes)
	for _, id := range []string{"node-1", "node-2"} {
		if !fleet[id] {
			t.Errorf("fleet-wide rollout should include %s", id)
		}
	}

	// No canary + no fleet-wide ⇒ empty rollout.
	if none := AgentRolloutNodeIDs(ControllerSettings{}, nodes); len(none) != 0 {
		t.Errorf("no rollout configured should yield an empty set, got %v", none)
	}
}
