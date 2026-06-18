package compiler

import (
	"context"
	"testing"

	"github.com/kunorikiku/yet-another-overlay-generator/internal/model"
)

// mimicTwoRouterTopo constructs a minimal two-router topology with a single
// unidirectional edge between the two nodes; the transport and each end's node MTU
// are given as parameters. It covers the Mimic / MTU derivation of PeerInfo under
// mimic (tcp transport) (docs/spec/artifacts/mimic.md "MTU −12", contract item 1).
//
// Both ends are declared as deployable Linux (debian / ubuntu) and publicly
// reachable, so the tcp edge passes the validator's mimic platform constraints and
// produces bidirectional peers (forward + auto reverse).
func mimicTwoRouterTopo(transport string, fromMTU, toMTU int) *model.Topology {
	return &model.Topology{
		Project: model.Project{ID: "mimic-2r", Name: "Mimic Two Router"},
		Domains: []model.Domain{{
			ID: "domain-1", Name: "mimic-net", CIDR: "10.50.0.0/24",
			AllocationMode: "auto", RoutingMode: "babel",
		}},
		Nodes: []model.Node{
			{
				ID: "node-a", Name: "alpha", Hostname: "a.example", Platform: "debian",
				Role: "router", DomainID: "domain-1", MTU: fromMTU,
				Capabilities: model.NodeCapabilities{CanAcceptInbound: true, CanForward: true, HasPublicIP: true},
				PublicEndpoints: []model.PublicEndpoint{
					{ID: "node-a-ep", Host: "a.example", Port: 51820},
				},
			},
			{
				ID: "node-b", Name: "beta", Hostname: "b.example", Platform: "ubuntu",
				Role: "router", DomainID: "domain-1", MTU: toMTU,
				Capabilities: model.NodeCapabilities{CanAcceptInbound: true, CanForward: true, HasPublicIP: true},
				PublicEndpoints: []model.PublicEndpoint{
					{ID: "node-b-ep", Host: "b.example", Port: 51820},
				},
			},
		},
		Edges: []model.Edge{
			{ID: "e1", FromNodeID: "node-a", ToNodeID: "node-b", Type: "public-endpoint",
				EndpointHost: "b.example", EndpointPort: 0, Transport: transport, IsEnabled: true},
		},
	}
}

// TestDerivePeers_MimicTcpEdge_FlagsAndMTU covers the core of contract item 1:
// a tcp edge => the PeerInfo at both ends of the link both have Mimic==true, and
// MTU == (effective)−12, where effective = node.MTU>0 ? node.MTU : 1420. Each end
// is derived from its own node's MTU.
//
//	node.MTU==0    ⇒ 1420 − 12 = 1408
//	node.MTU==1500 ⇒ 1500 − 12 = 1488
func TestDerivePeers_MimicTcpEdge_FlagsAndMTU(t *testing.T) {
	cases := []struct {
		name    string
		fromMTU int
		toMTU   int
		wantA   int // expected MTU for the node-a interface
		wantB   int // expected MTU for the node-b interface
	}{
		{name: "both default MTU (0 ⇒ 1408)", fromMTU: 0, toMTU: 0, wantA: 1408, wantB: 1408},
		{name: "both explicit 1500 (⇒ 1488)", fromMTU: 1500, toMTU: 1500, wantA: 1488, wantB: 1488},
		{name: "asymmetric: a=0 ⇒ 1408, b=1500 ⇒ 1488", fromMTU: 0, toMTU: 1500, wantA: 1408, wantB: 1488},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			topo := mimicTwoRouterTopo("tcp", tc.fromMTU, tc.toMTU)
			topo.Nodes[0].OverlayIP = "10.50.0.1"
			topo.Nodes[1].OverlayIP = "10.50.0.2"

			peerMap, _, err := DerivePeers(topo, testKeys2())
			if err != nil {
				t.Fatalf("DerivePeers failed: %v", err)
			}

			// The interface on node-a pointing at node-b (local end = node-a).
			aToB := findPeer(peerMap["node-a"], "node-b")
			if aToB == nil {
				t.Fatalf("node-a should have a peer pointing at node-b")
			}
			if !aToB.Mimic {
				t.Errorf("tcp edge: node-a->node-b should have Mimic==true, got false")
			}
			if aToB.MTU != tc.wantA {
				t.Errorf("node-a->node-b MTU = %d, want %d (local node.MTU=%d − 12)", aToB.MTU, tc.wantA, tc.fromMTU)
			}

			// The (auto reverse) interface on node-b pointing at node-a (local end = node-b).
			bToA := findPeer(peerMap["node-b"], "node-a")
			if bToA == nil {
				t.Fatalf("node-b should have a reverse peer pointing at node-a")
			}
			if !bToA.Mimic {
				t.Errorf("tcp edge: node-b->node-a should have Mimic==true, got false")
			}
			if bToA.MTU != tc.wantB {
				t.Errorf("node-b->node-a MTU = %d, want %d (local node.MTU=%d − 12)", bToA.MTU, tc.wantB, tc.toMTU)
			}
		})
	}
}

// TestDerivePeers_UdpEdge_NoMimicNoMTUChange covers the opposite of contract item 1:
// a udp edge => Mimic==false, and MTU == node.MTU verbatim (here node.MTU==0 ⇒ 0,
// so the renderer omits the MTU line). This is the byte-exact behavior from before
// the mimic change.
func TestDerivePeers_UdpEdge_NoMimicNoMTUChange(t *testing.T) {
	topo := mimicTwoRouterTopo("udp", 0, 0)
	topo.Nodes[0].OverlayIP = "10.50.0.1"
	topo.Nodes[1].OverlayIP = "10.50.0.2"

	peerMap, _, err := DerivePeers(topo, testKeys2())
	if err != nil {
		t.Fatalf("DerivePeers failed: %v", err)
	}

	for _, dir := range []struct {
		node, remote string
	}{
		{"node-a", "node-b"},
		{"node-b", "node-a"},
	} {
		p := findPeer(peerMap[dir.node], dir.remote)
		if p == nil {
			t.Fatalf("%s should have a peer pointing at %s", dir.node, dir.remote)
		}
		if p.Mimic {
			t.Errorf("udp edge: %s->%s should have Mimic==false, got true", dir.node, dir.remote)
		}
		if p.MTU != 0 {
			t.Errorf("udp edge with node.MTU==0: %s->%s MTU should stay 0 (no reduction), got %d", dir.node, dir.remote, p.MTU)
		}
	}
}

// TestDerivePeers_UdpEdge_ExplicitMTUUnchanged verifies that a udp edge does not
// touch node.MTU: for a udp edge with explicit MTU 1500, peer.MTU must still be 1500
// (never subtract 12). This contrasts with the −12 behavior of mimic links, ensuring
// the MTU of non-mimic topologies is completely unaffected by the mimic logic.
func TestDerivePeers_UdpEdge_ExplicitMTUUnchanged(t *testing.T) {
	topo := mimicTwoRouterTopo("udp", 1500, 1500)
	topo.Nodes[0].OverlayIP = "10.50.0.1"
	topo.Nodes[1].OverlayIP = "10.50.0.2"

	peerMap, _, err := DerivePeers(topo, testKeys2())
	if err != nil {
		t.Fatalf("DerivePeers failed: %v", err)
	}

	for _, dir := range []struct {
		node, remote string
	}{
		{"node-a", "node-b"},
		{"node-b", "node-a"},
	} {
		p := findPeer(peerMap[dir.node], dir.remote)
		if p == nil {
			t.Fatalf("%s should have a peer pointing at %s", dir.node, dir.remote)
		}
		if p.Mimic {
			t.Errorf("udp edge: %s->%s should have Mimic==false, got true", dir.node, dir.remote)
		}
		if p.MTU != 1500 {
			t.Errorf("udp edge: %s->%s MTU should stay 1500 (never subtract 12), got %d", dir.node, dir.remote, p.MTU)
		}
	}
}

// TestDerivePeers_EmptyTransportTreatedAsUdp verifies that an empty transport
// (default) is not treated as mimic: DerivePeers does no normalization, so an empty
// transport is judged non-tcp ⇒ Mimic==false, MTU not reduced. This ensures existing
// topologies with no transport set never accidentally enable mimic.
func TestDerivePeers_EmptyTransportTreatedAsUdp(t *testing.T) {
	topo := mimicTwoRouterTopo("", 1500, 1500)
	topo.Nodes[0].OverlayIP = "10.50.0.1"
	topo.Nodes[1].OverlayIP = "10.50.0.2"

	peerMap, _, err := DerivePeers(topo, testKeys2())
	if err != nil {
		t.Fatalf("DerivePeers failed: %v", err)
	}

	p := findPeer(peerMap["node-a"], "node-b")
	if p == nil {
		t.Fatalf("node-a should have a peer pointing at node-b")
	}
	if p.Mimic {
		t.Errorf("empty transport should not be treated as mimic, got Mimic==true")
	}
	if p.MTU != 1500 {
		t.Errorf("empty transport: MTU should stay 1500 (node.MTU verbatim), got %d", p.MTU)
	}
}

// mimicClientTopo constructs a client -> router topology where the transport of the
// client's outbound edge is given as a parameter. Both nodes are deployable Linux to
// satisfy the platform constraints of a tcp edge. It covers ClientPeerInfo.Mimic / MTU
// (last sentence of contract item 1).
func mimicClientTopo(transport string, clientMTU int) *model.Topology {
	return &model.Topology{
		Project: model.Project{ID: "mimic-cl", Name: "Mimic Client"},
		Domains: []model.Domain{{
			ID: "domain-1", Name: "mimic-cl-net", CIDR: "10.51.0.0/24",
			AllocationMode: "auto", RoutingMode: "babel",
		}},
		Nodes: []model.Node{
			{
				ID: "router-a", Name: "router-a", Platform: "debian",
				Role: "router", DomainID: "domain-1",
				Capabilities: model.NodeCapabilities{CanAcceptInbound: true, CanForward: true, HasPublicIP: true},
				PublicEndpoints: []model.PublicEndpoint{
					{ID: "router-a-ep", Host: "router-a.example", Port: 51820},
				},
			},
			{
				ID: "client-a", Name: "client-a", Platform: "ubuntu",
				Role: "client", DomainID: "domain-1", MTU: clientMTU,
			},
		},
		Edges: []model.Edge{
			{ID: "e-client", FromNodeID: "client-a", ToNodeID: "router-a", Type: "public-endpoint",
				EndpointHost: "router-a.example", EndpointPort: 0, Transport: transport, IsEnabled: true},
		},
	}
}

// TestDeriveClientConfigs_MimicTcpEdge covers the derivation of ClientPeerInfo under a
// client tcp edge: if the client's single wg0 link is tcp, then ClientPeerInfo.Mimic==true
// and MTU == effective−12 (node.MTU==0 ⇒ 1408, node.MTU==1500 ⇒ 1488). Contrast udp:
// Mimic==false, MTU verbatim.
func TestDeriveClientConfigs_MimicTcpEdge(t *testing.T) {
	clientKeys := func() map[string]KeyPair {
		return map[string]KeyPair{
			"router-a": {PrivateKey: "privkey-router-a-fake", PublicKey: "pubkey-router-a-fake"},
			"client-a": {PrivateKey: "privkey-client-a-fake", PublicKey: "pubkey-client-a-fake"},
		}
	}

	cases := []struct {
		name      string
		transport string
		clientMTU int
		wantMimic bool
		wantMTU   int
	}{
		{name: "tcp + MTU 0 ⇒ mimic, 1408", transport: "tcp", clientMTU: 0, wantMimic: true, wantMTU: 1408},
		{name: "tcp + MTU 1500 ⇒ mimic, 1488", transport: "tcp", clientMTU: 1500, wantMimic: true, wantMTU: 1488},
		{name: "udp + MTU 0 ⇒ no mimic, 0", transport: "udp", clientMTU: 0, wantMimic: false, wantMTU: 0},
		{name: "udp + MTU 1500 ⇒ no mimic, 1500 unchanged", transport: "udp", clientMTU: 1500, wantMimic: false, wantMTU: 1500},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			topo := mimicClientTopo(tc.transport, tc.clientMTU)

			c := NewCompiler()
			result, err := c.Compile(context.Background(), topo, clientKeys())
			if err != nil {
				t.Fatalf("Compile failed: %v", err)
			}

			cfg := result.ClientConfigs["client-a"]
			if cfg == nil {
				t.Fatalf("a ClientPeerInfo should be generated for client-a")
			}
			if cfg.Mimic != tc.wantMimic {
				t.Errorf("ClientPeerInfo.Mimic = %v, want %v", cfg.Mimic, tc.wantMimic)
			}
			if cfg.MTU != tc.wantMTU {
				t.Errorf("ClientPeerInfo.MTU = %d, want %d", cfg.MTU, tc.wantMTU)
			}
		})
	}
}

// TestEffectiveMTU_PureFunction directly covers the MTU formula (the arithmetic core
// of contract item 1), complementing the topology-level assertions above: non-mimic
// passes through verbatim (including 0), mimic gives (base?:1420)−12.
func TestEffectiveMTU_PureFunction(t *testing.T) {
	cases := []struct {
		nodeMTU int
		mimic   bool
		want    int
	}{
		{nodeMTU: 0, mimic: false, want: 0},
		{nodeMTU: 1500, mimic: false, want: 1500},
		{nodeMTU: 1420, mimic: false, want: 1420},
		{nodeMTU: 0, mimic: true, want: 1408},
		{nodeMTU: 1420, mimic: true, want: 1408},
		{nodeMTU: 1500, mimic: true, want: 1488},
	}
	for _, tc := range cases {
		if got := effectiveMTU(tc.nodeMTU, tc.mimic); got != tc.want {
			t.Errorf("effectiveMTU(%d, %v) = %d, want %d", tc.nodeMTU, tc.mimic, got, tc.want)
		}
	}
}
