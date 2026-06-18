package compiler

import (
	"context"
	"testing"

	"github.com/kunorikiku/yet-another-overlay-generator/internal/model"
)

// portBoundsTopo builds a star topology centered on a hub, connecting out to spokeCount spokes.
// The base listen port is uniformly 51820 (per-node listen_port has been removed), and each of
// the hub's peer interfaces listens at 51820+offset, to cover the compilation path for multi-interface allocation.
func portBoundsTopo(spokeCount int) (*model.Topology, map[string]KeyPair) {
	nodes := []model.Node{
		{
			ID: "node-hub", Name: "hub", Hostname: "hub.example.com",
			Role: "router", DomainID: "domain-1",
			Capabilities: model.NodeCapabilities{CanAcceptInbound: true, CanForward: true, HasPublicIP: true},
		},
	}
	keys := map[string]KeyPair{
		"node-hub": {PrivateKey: "privkey-hub-fake", PublicKey: "pubkey-hub-fake"},
	}
	var edges []model.Edge
	for i := 0; i < spokeCount; i++ {
		spokeID := spokeName(i)
		nodes = append(nodes, model.Node{
			ID: spokeID, Name: spokeID, Hostname: spokeID + ".example.com",
			Role: "router", DomainID: "domain-1",
			Capabilities: model.NodeCapabilities{CanAcceptInbound: true, CanForward: true, HasPublicIP: true},
		})
		keys[spokeID] = KeyPair{PrivateKey: "privkey-" + spokeID + "-fake", PublicKey: "pubkey-" + spokeID + "-fake"}
		edges = append(edges, model.Edge{
			ID: "e-" + spokeID, FromNodeID: "node-hub", ToNodeID: spokeID,
			Type: "direct", Transport: "udp", IsEnabled: true,
		})
	}

	topo := &model.Topology{
		Project: model.Project{ID: "portbounds-001", Name: "Port Bounds"},
		Domains: []model.Domain{{
			ID: "domain-1", Name: "pb-net", CIDR: "10.50.0.0/24",
			AllocationMode: "auto", RoutingMode: "babel",
		}},
		Nodes: nodes,
		Edges: edges,
	}
	// Assign overlay IPs to the nodes (DerivePeers consumes OverlayIP directly, no longer going through the IP allocator).
	for i := range topo.Nodes {
		topo.Nodes[i].OverlayIP = overlayIPForIndex(i)
	}
	return topo, keys
}

func spokeName(i int) string {
	return "spoke-" + string(rune('a'+i))
}

func overlayIPForIndex(i int) string {
	// 10.50.0.(i+1); test cases are far smaller than a /24, so no boundary handling is needed.
	return "10.50.0." + itoaTest(i+1)
}

func itoaTest(n int) string {
	if n == 0 {
		return "0"
	}
	s := ""
	for n > 0 {
		s = string(rune('0'+n%10)) + s
		n /= 10
	}
	return s
}

// TestCompile_ManyInterfacesCompileClean verifies that, with the base port uniformly
// 51820, a hub connecting to 7 spokes (the hub gets 7 per-peer interfaces, listening on
// 51820..51826) compiles cleanly. The out-of-range rule (lowestFreePort returns
// CodeListenPortExhausted when port>65535) is still kept, but under the uniform base it
// would take tens of thousands of interfaces to trigger and can no longer be artificially
// constructed via a high base, so the out-of-range error path is no longer unit-tested
// (per-node listen_port has been removed).
func TestCompile_ManyInterfacesCompileClean(t *testing.T) {
	c := NewCompiler()
	topo, keys := portBoundsTopo(7)
	if _, err := c.Compile(context.Background(), topo, keys); err != nil {
		t.Fatalf("with base port 51820, a hub connecting to 7 spokes should compile cleanly, but failed: %v", err)
	}
}
