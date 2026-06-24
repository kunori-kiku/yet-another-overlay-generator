package controller

import (
	"testing"

	"github.com/kunorikiku/yet-another-overlay-generator/internal/model"
)

// manualMixedTopo models a controller topology with one MANAGED node (alpha, which enrolls) and one
// MANUAL node (mike, deployment_mode=manual, carrying its own pre-known public key + endpoint in the
// topology), joined by an edge. The manual node never enrolls — it is hand-deployed — so the only way
// it can compile is via its topology-carried identity.
func manualMixedTopo(manualPub string) *model.Topology {
	return &model.Topology{
		Project: model.Project{ID: "ctrl-manual-001", Name: "Mixed Mode"},
		Domains: []model.Domain{{
			ID: "domain-1", Name: "net", CIDR: "10.70.0.0/24",
			AllocationMode: "auto", RoutingMode: "babel",
		}},
		Nodes: []model.Node{
			{
				ID: "node-alpha", Name: "alpha", Role: "router", DomainID: "domain-1",
				Capabilities:    model.NodeCapabilities{CanAcceptInbound: true, CanForward: true, HasPublicIP: true},
				PublicEndpoints: []model.PublicEndpoint{{ID: "a-ep", Host: "alpha.example.com", Port: 51820}},
			},
			{
				ID: "node-mike", Name: "mike", Role: "router", DomainID: "domain-1",
				DeploymentMode:     model.DeploymentManual,
				WireGuardPublicKey: manualPub,
				// A manual node that accepts inbound carries its own endpoint (no enrollment supplies one).
				Capabilities:    model.NodeCapabilities{CanAcceptInbound: true, CanForward: true, HasPublicIP: true},
				PublicEndpoints: []model.PublicEndpoint{{ID: "m-ep", Host: "mike.example.com", Port: 51820}},
			},
		},
		Edges: []model.Edge{
			{
				ID: "e-am", FromNodeID: "node-alpha", ToNodeID: "node-mike",
				Type: "public-endpoint", EndpointHost: "mike.example.com", Transport: "udp", IsEnabled: true,
			},
		},
	}
}

// TestEnrolledSubgraph_AdmitsManualNode is the plan-1 admission core: a manual node is compiled from
// its TOPOLOGY public key (never enrolling), its edges are kept, and it is NOT reported as skipped.
func TestEnrolledSubgraph_AdmitsManualNode(t *testing.T) {
	manualPub := genWGPubKey(t)
	full := manualMixedTopo(manualPub)

	// Only the MANAGED node enrolls. The manual node has no registry record at all.
	nodes := []Node{
		{NodeID: "node-alpha", WGPublicKey: genWGPubKey(t), Status: NodeApproved},
	}

	sub, skipped := enrolledSubgraph(full, nodes)

	// The manual node is admitted (not skipped) even though it never enrolled.
	if containsStr(skipped, "node-mike") {
		t.Errorf("manual node must NOT be reported as skipped/unenrolled, got skipped=%v", skipped)
	}
	if len(skipped) != 0 {
		t.Errorf("expected no skipped nodes (alpha enrolled, mike manual), got %v", skipped)
	}

	// Both nodes are in the subgraph.
	var alpha, mike *model.Node
	for i := range sub.Nodes {
		switch sub.Nodes[i].ID {
		case "node-alpha":
			alpha = &sub.Nodes[i]
		case "node-mike":
			mike = &sub.Nodes[i]
		}
	}
	if alpha == nil {
		t.Fatalf("managed node alpha missing from subgraph")
	}
	if mike == nil {
		t.Fatalf("manual node mike missing from subgraph (admission failed)")
	}

	// The manual node's public key is its TOPOLOGY key (not a registry value — it has none).
	if mike.WireGuardPublicKey != manualPub {
		t.Errorf("manual node pubkey = %q, want the topology key %q", mike.WireGuardPublicKey, manualPub)
	}

	// Its edge survives (the whole point — managed alpha must carry mike as a peer).
	if len(sub.Edges) != 1 || sub.Edges[0].ID != "e-am" {
		t.Errorf("edge to the manual node must be kept, got edges=%v", sub.Edges)
	}
}

// TestEnrolledSubgraph_ManualNodeZeroKnowledge guards the inviolable custody rule: even if the stored
// topology carried a private key for the manual node, the subgraph node must have it CLEARED — a
// private key must never reach a controller-rendered bundle.
func TestEnrolledSubgraph_ManualNodeZeroKnowledge(t *testing.T) {
	manualPub := genWGPubKey(t)
	full := manualMixedTopo(manualPub)
	// Simulate a stray private key on the manual node (e.g. an imported air-gap topology).
	for i := range full.Nodes {
		if full.Nodes[i].ID == "node-mike" {
			full.Nodes[i].WireGuardPrivateKey = "STRAY_PRIVATE_KEY_MUST_BE_CLEARED"
		}
	}

	nodes := []Node{{NodeID: "node-alpha", WGPublicKey: genWGPubKey(t), Status: NodeApproved}}
	sub, _ := enrolledSubgraph(full, nodes)

	for i := range sub.Nodes {
		if sub.Nodes[i].ID == "node-mike" && sub.Nodes[i].WireGuardPrivateKey != "" {
			t.Errorf("manual node private key must be cleared in the subgraph, got %q", sub.Nodes[i].WireGuardPrivateKey)
		}
	}
}

// TestEnrolledSubgraph_ManualWithoutPubkeyExcludedNotSkipped: a manual node missing its topology
// pubkey (a design error the validator rejects pre-compile) is excluded, but is NEVER listed as a
// transient "skipped/unenrolled" node — that status is for managed nodes awaiting enrollment.
func TestEnrolledSubgraph_ManualWithoutPubkeyExcludedNotSkipped(t *testing.T) {
	full := manualMixedTopo("") // manual node mike has no pubkey
	nodes := []Node{{NodeID: "node-alpha", WGPublicKey: genWGPubKey(t), Status: NodeApproved}}

	sub, skipped := enrolledSubgraph(full, nodes)

	if containsStr(skipped, "node-mike") {
		t.Errorf("a pubkey-less manual node must not be reported as skipped (it is a design error, not a transient skip), got %v", skipped)
	}
	for i := range sub.Nodes {
		if sub.Nodes[i].ID == "node-mike" {
			t.Errorf("a pubkey-less manual node must be excluded from the subgraph, but it was admitted")
		}
	}
	// Its edge is dropped (far end not ready), exactly like a not-yet-enrolled managed far end.
	if len(sub.Edges) != 0 {
		t.Errorf("edge to a not-ready manual node must be dropped, got edges=%v", sub.Edges)
	}
}
