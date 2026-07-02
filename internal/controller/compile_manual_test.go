package controller

import (
	"context"
	"strings"
	"testing"

	"golang.zx2c4.com/wireguard/wgctrl/wgtypes"

	"github.com/kunorikiku/yet-another-overlay-generator/internal/apierr"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/compiler"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/model"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/render"
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

// TestValidateManualNodes covers the plan-2 controller-side identity guard for manual nodes: a manual
// node must carry a public key, and that key must be unique across the fleet (not duplicating another
// manual node's, nor colliding with an enrolled node's). All failures surface CodeManualNodeInvalid.
func TestValidateManualNodes(t *testing.T) {
	enrolledPub := genWGPubKey(t)
	managed := []Node{{NodeID: "node-alpha", WGPublicKey: enrolledPub, Status: NodeApproved}}

	// A valid manual node (its own unique pubkey) passes.
	if err := validateManualNodes(manualMixedTopo(genWGPubKey(t)), managed); err != nil {
		t.Errorf("a valid manual node should pass validateManualNodes, got %v", err)
	}

	// A manual node with NO public key is rejected loudly (vs the silent exclusion enrolledSubgraph applies).
	if err := validateManualNodes(manualMixedTopo(""), managed); !apierr.HasCode(err, apierr.CodeManualNodeInvalid) {
		t.Errorf("a pubkey-less manual node must be rejected with CodeManualNodeInvalid, got %v", err)
	}

	// A manual node with a MALFORMED pubkey (not valid base64/32-byte Curve25519) is rejected — the
	// operator-asserted key is rendered verbatim into peers' root-parsed wg configs, so an
	// injection-shaped value must never be admitted (plan-4).
	if err := validateManualNodes(manualMixedTopo("not-a-valid-curve25519-key"), managed); !apierr.HasCode(err, apierr.CodeManualNodeInvalid) {
		t.Errorf("a manual node with a malformed pubkey must be rejected with CodeManualNodeInvalid, got %v", err)
	}

	// A manual node whose pubkey COLLIDES with an enrolled node's is rejected (no identity confusion).
	if err := validateManualNodes(manualMixedTopo(enrolledPub), managed); !apierr.HasCode(err, apierr.CodeManualNodeInvalid) {
		t.Errorf("a manual node colliding with an enrolled pubkey must be rejected, got %v", err)
	}

	// Two manual nodes SHARING a pubkey are rejected (dedupe across manual nodes).
	dupPub := genWGPubKey(t)
	topo := manualMixedTopo(dupPub)
	topo.Nodes = append(topo.Nodes, model.Node{
		ID: "node-mike2", Name: "mike2", Role: "router", DomainID: "domain-1",
		DeploymentMode: model.DeploymentManual, WireGuardPublicKey: dupPub,
		Capabilities:    model.NodeCapabilities{CanAcceptInbound: true, HasPublicIP: true},
		PublicEndpoints: []model.PublicEndpoint{{ID: "m2-ep", Host: "mike2.example.com", Port: 51820}},
	})
	if err := validateManualNodes(topo, managed); !apierr.HasCode(err, apierr.CodeManualNodeInvalid) {
		t.Errorf("two manual nodes sharing a pubkey must be rejected, got %v", err)
	}
}

// peerHasPubkey reports whether a node's derived peer set carries a peer with the given public key.
func peerHasPubkey(peers []compiler.PeerInfo, pub string) bool {
	for _, p := range peers {
		if p.PublicKey == pub {
			return true
		}
	}
	return false
}

// TestCompileSubgraph_ManualNode_BidirectionalRender_ZeroKnowledge is the plan-1 hazard-gated, load-
// bearing proof on the RENDERED/STAGED OUTPUT (not just the enrolledSubgraph projection layer): a
// manual node is rendered bidirectionally as a peer (the managed node carries it, and it carries the
// managed node), AND a stray private key on the manual node is cleared all the way through to the
// rendered output — the controller renders a manual node's bundle with the private-key PLACEHOLDER, so
// no real private key ever appears in any staged file (the operator splices the real key off-host).
func TestCompileSubgraph_ManualNode_BidirectionalRender_ZeroKnowledge(t *testing.T) {
	alphaPriv, err := wgtypes.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("gen alpha key: %v", err)
	}
	mikePriv, err := wgtypes.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("gen mike key: %v", err)
	}
	alphaPub := alphaPriv.PublicKey().String()
	mikePub := mikePriv.PublicKey().String()

	full := manualMixedTopo(mikePub)
	// Stray private key on the manual node (e.g. an imported air-gap topology): it MUST be cleared all
	// the way to the rendered output, not just at the projection layer.
	for i := range full.Nodes {
		if full.Nodes[i].ID == "node-mike" {
			full.Nodes[i].WireGuardPrivateKey = mikePriv.String()
		}
	}
	// Only the managed node enrolls; the manual node never does.
	nodes := []Node{{NodeID: "node-alpha", WGPublicKey: alphaPub, Status: NodeApproved}}

	result, _, _, err := CompileSubgraph(context.Background(), full, nodes, render.FetchSettings{})
	if err != nil {
		t.Fatalf("CompileSubgraph: %v", err)
	}
	if result == nil {
		t.Fatalf("CompileSubgraph returned no result (the manual node was not admitted)")
	}

	// Bidirectional [Peer] render: the managed node carries the manual node as a peer (its pubkey), and
	// the manual node carries the managed node — so the overlay link is configured on BOTH ends.
	if !peerHasPubkey(result.PeerMap["node-alpha"], mikePub) {
		t.Errorf("managed node alpha must render the manual node mike as a peer (mike's pubkey), but it does not")
	}
	if !peerHasPubkey(result.PeerMap["node-mike"], alphaPub) {
		t.Errorf("manual node mike must render managed node alpha as a peer (alpha's pubkey), but it does not")
	}

	// Zero-knowledge on the rendered OUTPUT: gather every rendered file and assert the manual node's
	// private key is NOWHERE in it, while the placeholder IS (so the manual node's own [Interface]
	// PrivateKey is the splice placeholder, never a real key).
	var all strings.Builder
	for _, m := range []map[string]string{
		result.WireGuardConfigs, result.InstallScripts, result.BabelConfigs,
		result.SysctlConfigs, result.DeployScripts, result.ArtifactsJSON,
	} {
		for _, v := range m {
			all.WriteString(v)
			all.WriteByte('\n')
		}
	}
	out := all.String()
	if strings.Contains(out, mikePriv.String()) {
		t.Errorf("ZERO-KNOWLEDGE VIOLATION: the manual node's private key appears in the rendered/staged output")
	}
	if strings.Contains(out, alphaPriv.String()) {
		t.Errorf("ZERO-KNOWLEDGE VIOLATION: a node's private key appears in the rendered/staged output")
	}
	if !strings.Contains(out, render.PrivateKeyPlaceholder) {
		t.Errorf("rendered output must carry the private-key placeholder %q (AgentHeld custody), but it is missing", render.PrivateKeyPlaceholder)
	}
}

// TestEnrolledSubgraph_ManualWithoutPubkeyExcludedNotSkipped: a manual node missing its topology
// pubkey (a design error the controller-registration validator will reject in plan-2; until then this
// branch defensively excludes it) is excluded, but is NEVER listed as a transient "skipped/unenrolled"
// node — that status is for managed nodes awaiting enrollment.
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
