package compiler

import (
	"context"
	"testing"

	"github.com/kunorikiku/yet-another-overlay-generator/internal/model"
)

// countOccurrences counts how many times cidr appears in the slice, used to assert "appears exactly once".
func countOccurrences(slice []string, target string) int {
	n := 0
	for _, v := range slice {
		if v == target {
			n++
		}
	}
	return n
}

// TestClientAllowedIPs_MultiDomainUnion covers D30 (Decision 6):
// a client's wg0 is its sole tunnel into the entire overlay, so DomainCIDRs must be
// the union of "all domain CIDRs" and "each domain's resolved transit CIDR", with
// each prefix appearing exactly once.
//
// Topology: two domains (10.11.0.0/24 and 10.12.0.0/24), where domain B has a custom
// transit (10.20.0.0/24) and domain A leaves transit empty (resolving to the default
// 10.10.0.0/24). The client is in domain A, connecting to a router in the same domain.
func TestClientAllowedIPs_MultiDomainUnion(t *testing.T) {
	topo := &model.Topology{
		Project: model.Project{ID: "cl-multi", Name: "Client Multi-Domain"},
		Domains: []model.Domain{
			{
				ID: "domain-a", Name: "alpha-net", CIDR: "10.11.0.0/24",
				AllocationMode: "auto", RoutingMode: "babel",
				// transit left empty -> resolves to default 10.10.0.0/24
			},
			{
				ID: "domain-b", Name: "beta-net", CIDR: "10.12.0.0/24",
				AllocationMode: "auto", RoutingMode: "babel",
				TransitCIDR: "10.20.0.0/24",
			},
		},
		Nodes: []model.Node{
			{
				ID: "router-a", Name: "router-a", Role: "router", DomainID: "domain-a",
				Capabilities: model.NodeCapabilities{HasPublicIP: true},
				PublicEndpoints: []model.PublicEndpoint{
					{ID: "router-a-ep", Host: "router-a.example", Port: 51820},
				},
			},
			// Place a router in domain B to ensure that domain (including its custom
			// transit) participates in the union, even though the client does not connect to it directly.
			{
				ID: "router-b", Name: "router-b", Role: "router", DomainID: "domain-b",
				Capabilities: model.NodeCapabilities{HasPublicIP: true},
				PublicEndpoints: []model.PublicEndpoint{
					{ID: "router-b-ep", Host: "router-b.example", Port: 51820},
				},
			},
			{
				ID: "client-a", Name: "client-a", Role: "client", DomainID: "domain-a",
			},
		},
		Edges: []model.Edge{
			// router-a <-> router-b: cross-domain backbone, making both domains "in use".
			{ID: "e-backbone", FromNodeID: "router-a", ToNodeID: "router-b", Type: "public-endpoint",
				EndpointHost: "router-b.example", Transport: "udp", IsEnabled: true},
			// client-a -> router-a: the client's sole outbound edge (must carry endpoint_host).
			{ID: "e-client", FromNodeID: "client-a", ToNodeID: "router-a", Type: "public-endpoint",
				EndpointHost: "router-a.example", Transport: "udp", IsEnabled: true},
		},
	}

	keys := map[string]KeyPair{
		"router-a": {PrivateKey: "privkey-router-a-fake", PublicKey: "pubkey-router-a-fake"},
		"router-b": {PrivateKey: "privkey-router-b-fake", PublicKey: "pubkey-router-b-fake"},
		"client-a": {PrivateKey: "privkey-client-a-fake", PublicKey: "pubkey-client-a-fake"},
	}

	c := NewCompiler()
	result, err := c.Compile(context.Background(), topo, keys)
	if err != nil {
		t.Fatalf("Compile failed: %v", err)
	}

	clientCfg := result.ClientConfigs["client-a"]
	if clientCfg == nil {
		t.Fatalf("a ClientPeerInfo should be generated for client-a")
	}

	// Each of the two domain CIDRs exactly once.
	for _, cidr := range []string{"10.11.0.0/24", "10.12.0.0/24"} {
		if got := countOccurrences(clientCfg.DomainCIDRs, cidr); got != 1 {
			t.Errorf("DomainCIDRs should contain domain CIDR %s exactly once, got %d times (DomainCIDRs=%v)",
				cidr, got, clientCfg.DomainCIDRs)
		}
	}

	// Each of the two transit CIDRs exactly once: domain A resolves to default 10.10.0.0/24, domain B custom 10.20.0.0/24.
	for _, cidr := range []string{"10.10.0.0/24", "10.20.0.0/24"} {
		if got := countOccurrences(clientCfg.DomainCIDRs, cidr); got != 1 {
			t.Errorf("DomainCIDRs should contain transit CIDR %s exactly once, got %d times (DomainCIDRs=%v)",
				cidr, got, clientCfg.DomainCIDRs)
		}
	}

	// The union is exactly 4 prefixes (2 domains + 2 transit), with no extras and no duplicates.
	if len(clientCfg.DomainCIDRs) != 4 {
		t.Errorf("DomainCIDRs should be exactly 4 prefixes (2 domains + 2 transit), got %d: %v",
			len(clientCfg.DomainCIDRs), clientCfg.DomainCIDRs)
	}
}

// TestClientAllowedIPs_SingleDomain verifies the single-domain case still works:
// DomainCIDRs == [domain CIDR, default transit CIDR], each exactly once.
func TestClientAllowedIPs_SingleDomain(t *testing.T) {
	topo := &model.Topology{
		Project: model.Project{ID: "cl-single", Name: "Client Single-Domain"},
		Domains: []model.Domain{
			{
				ID: "domain-a", Name: "alpha-net", CIDR: "10.13.0.0/24",
				AllocationMode: "auto", RoutingMode: "babel",
				// transit left empty -> default 10.10.0.0/24
			},
		},
		Nodes: []model.Node{
			{
				ID: "router-a", Name: "router-a", Role: "router", DomainID: "domain-a",
				Capabilities: model.NodeCapabilities{HasPublicIP: true},
				PublicEndpoints: []model.PublicEndpoint{
					{ID: "router-a-ep", Host: "router-a.example", Port: 51820},
				},
			},
			{
				ID: "client-a", Name: "client-a", Role: "client", DomainID: "domain-a",
			},
		},
		Edges: []model.Edge{
			{ID: "e-client", FromNodeID: "client-a", ToNodeID: "router-a", Type: "public-endpoint",
				EndpointHost: "router-a.example", Transport: "udp", IsEnabled: true},
		},
	}

	keys := map[string]KeyPair{
		"router-a": {PrivateKey: "privkey-router-a-fake", PublicKey: "pubkey-router-a-fake"},
		"client-a": {PrivateKey: "privkey-client-a-fake", PublicKey: "pubkey-client-a-fake"},
	}

	c := NewCompiler()
	result, err := c.Compile(context.Background(), topo, keys)
	if err != nil {
		t.Fatalf("Compile failed: %v", err)
	}

	clientCfg := result.ClientConfigs["client-a"]
	if clientCfg == nil {
		t.Fatalf("a ClientPeerInfo should be generated for client-a")
	}

	if got := countOccurrences(clientCfg.DomainCIDRs, "10.13.0.0/24"); got != 1 {
		t.Errorf("single domain: DomainCIDRs should contain domain CIDR 10.13.0.0/24 exactly once, got %d times (%v)",
			got, clientCfg.DomainCIDRs)
	}
	if got := countOccurrences(clientCfg.DomainCIDRs, "10.10.0.0/24"); got != 1 {
		t.Errorf("single domain: DomainCIDRs should contain default transit CIDR 10.10.0.0/24 exactly once, got %d times (%v)",
			got, clientCfg.DomainCIDRs)
	}
	if len(clientCfg.DomainCIDRs) != 2 {
		t.Errorf("single domain: DomainCIDRs should be exactly 2 prefixes (domain + default transit), got %d: %v",
			len(clientCfg.DomainCIDRs), clientCfg.DomainCIDRs)
	}
}

// TestInferCapabilitiesFromRole_PublicRouterAcceptsInbound covers D49:
// a router with HasPublicIP should, after capability inference, get CanAcceptInbound=true,
// consistent with DeriveRoleSemantics' AcceptAllInbound.
// It also verifies that a router without a public IP is not inferred to accept inbound.
func TestInferCapabilitiesFromRole_PublicRouterAcceptsInbound(t *testing.T) {
	publicRouter := &model.Node{
		ID: "r1", Role: "router",
		Capabilities: model.NodeCapabilities{HasPublicIP: true},
	}
	caps := InferCapabilitiesFromRole(publicRouter)
	if !caps.CanAcceptInbound {
		t.Errorf("a router with a public IP should infer CanAcceptInbound=true, got false")
	}

	privateRouter := &model.Node{
		ID: "r2", Role: "router",
		Capabilities: model.NodeCapabilities{HasPublicIP: false},
	}
	if caps := InferCapabilitiesFromRole(privateRouter); caps.CanAcceptInbound {
		t.Errorf("a router without a public IP should not infer CanAcceptInbound=true")
	}

	// gateway has the same semantics.
	publicGateway := &model.Node{
		ID: "g1", Role: "gateway",
		Capabilities: model.NodeCapabilities{HasPublicIP: true},
	}
	if caps := InferCapabilitiesFromRole(publicGateway); !caps.CanAcceptInbound {
		t.Errorf("a gateway with a public IP should infer CanAcceptInbound=true, got false")
	}
}

// TestPeerInfo_LinkCostFromEdgePriority covers D63:
// edge.Priority (>0) should map to the LinkCost of both the forward and reverse PeerInfo.
func TestPeerInfo_LinkCostFromEdgePriority(t *testing.T) {
	const wantCost = 77

	topo := &model.Topology{
		Project: model.Project{ID: "lc-prio", Name: "LinkCost Priority"},
		Domains: []model.Domain{{
			ID: "domain-1", Name: "lc-net", CIDR: "10.46.0.0/24",
			AllocationMode: "auto", RoutingMode: "babel",
		}},
		Nodes: []model.Node{
			publicRouterNode("node-a", "alpha", "a.example"),
			publicRouterNode("node-b", "beta", "b.example"),
		},
		Edges: []model.Edge{
			{ID: "e1", FromNodeID: "node-a", ToNodeID: "node-b", Type: "public-endpoint",
				EndpointHost: "b.example", Priority: wantCost, Transport: "udp", IsEnabled: true},
		},
	}
	topo.Nodes[0].OverlayIP = "10.46.0.1"
	topo.Nodes[1].OverlayIP = "10.46.0.2"

	peerMap, _, err := DerivePeers(topo, testKeys2())
	if err != nil {
		t.Fatalf("DerivePeers failed: %v", err)
	}

	fwd := findPeer(peerMap["node-a"], "node-b")
	if fwd == nil {
		t.Fatalf("node-a should have a forward peer pointing at node-b")
	}
	if fwd.LinkCost != wantCost {
		t.Errorf("forward LinkCost = %d, want %d (from edge.Priority)", fwd.LinkCost, wantCost)
	}

	rev := findPeer(peerMap["node-b"], "node-a")
	if rev == nil {
		t.Fatalf("node-b should have a reverse peer pointing at node-a")
	}
	if rev.LinkCost != wantCost {
		t.Errorf("reverse LinkCost = %d, want %d (reverse peer shares the same edge)", rev.LinkCost, wantCost)
	}
}

// TestPeerInfo_LinkCostFallback verifies the fallback order of D63:
//   - no Priority but a Weight -> LinkCost = Weight;
//   - neither set -> LinkCost = 0 (deferred to the role preset default).
func TestPeerInfo_LinkCostFallback(t *testing.T) {
	t.Run("weight used when priority absent", func(t *testing.T) {
		const wantWeight = 42
		topo := &model.Topology{
			Project: model.Project{ID: "lc-wt", Name: "LinkCost Weight"},
			Domains: []model.Domain{{
				ID: "domain-1", Name: "lc-net", CIDR: "10.47.0.0/24",
				AllocationMode: "auto", RoutingMode: "babel",
			}},
			Nodes: []model.Node{
				publicRouterNode("node-a", "alpha", "a.example"),
				publicRouterNode("node-b", "beta", "b.example"),
			},
			Edges: []model.Edge{
				{ID: "e1", FromNodeID: "node-a", ToNodeID: "node-b", Type: "public-endpoint",
					EndpointHost: "b.example", Weight: wantWeight, Transport: "udp", IsEnabled: true},
			},
		}
		topo.Nodes[0].OverlayIP = "10.47.0.1"
		topo.Nodes[1].OverlayIP = "10.47.0.2"

		peerMap, _, err := DerivePeers(topo, testKeys2())
		if err != nil {
			t.Fatalf("DerivePeers failed: %v", err)
		}
		fwd := findPeer(peerMap["node-a"], "node-b")
		if fwd == nil {
			t.Fatalf("node-a should have a peer pointing at node-b")
		}
		if fwd.LinkCost != wantWeight {
			t.Errorf("LinkCost = %d, want %d (fell back to edge.Weight)", fwd.LinkCost, wantWeight)
		}
	})

	t.Run("zero when neither set", func(t *testing.T) {
		topo := &model.Topology{
			Project: model.Project{ID: "lc-zero", Name: "LinkCost Zero"},
			Domains: []model.Domain{{
				ID: "domain-1", Name: "lc-net", CIDR: "10.48.0.0/24",
				AllocationMode: "auto", RoutingMode: "babel",
			}},
			Nodes: []model.Node{
				publicRouterNode("node-a", "alpha", "a.example"),
				publicRouterNode("node-b", "beta", "b.example"),
			},
			Edges: []model.Edge{
				{ID: "e1", FromNodeID: "node-a", ToNodeID: "node-b", Type: "public-endpoint",
					EndpointHost: "b.example", Transport: "udp", IsEnabled: true},
			},
		}
		topo.Nodes[0].OverlayIP = "10.48.0.1"
		topo.Nodes[1].OverlayIP = "10.48.0.2"

		peerMap, _, err := DerivePeers(topo, testKeys2())
		if err != nil {
			t.Fatalf("DerivePeers failed: %v", err)
		}
		fwd := findPeer(peerMap["node-a"], "node-b")
		if fwd == nil {
			t.Fatalf("node-a should have a peer pointing at node-b")
		}
		if fwd.LinkCost != 0 {
			t.Errorf("LinkCost = %d, want 0 (no priority/weight set)", fwd.LinkCost)
		}
	})
}
