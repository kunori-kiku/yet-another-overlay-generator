package compiler

import (
	"testing"

	"github.com/kunorikiku/yet-another-overlay-generator/internal/model"
)

func testKeys() map[string]KeyPair {
	return map[string]KeyPair{
		"node-1": {PrivateKey: "privkey-node1-fake", PublicKey: "pubkey-node1-fake"},
		"node-2": {PrivateKey: "privkey-node2-fake", PublicKey: "pubkey-node2-fake"},
		"node-3": {PrivateKey: "privkey-node3-fake", PublicKey: "pubkey-node3-fake"},
	}
}

func simpleMeshTopo() *model.Topology {
	return &model.Topology{
		Project: model.Project{ID: "test-001", Name: "Test Mesh"},
		Domains: []model.Domain{{
			ID: "domain-1", Name: "mesh", CIDR: "10.11.0.0/24",
			AllocationMode: "auto", RoutingMode: "babel",
		}},
		Nodes: []model.Node{
			{
				ID: "node-1", Name: "alpha", Hostname: "alpha.example.com",
				Role: "router", DomainID: "domain-1",
				Capabilities: model.NodeCapabilities{CanAcceptInbound: true, CanForward: true, HasPublicIP: true},
			},
			{
				ID: "node-2", Name: "beta", Hostname: "beta.example.com",
				Role: "router", DomainID: "domain-1",
				Capabilities: model.NodeCapabilities{CanAcceptInbound: true, CanForward: true, HasPublicIP: true},
			},
			{
				ID: "node-3", Name: "gamma", Hostname: "gamma.example.com",
				Role: "router", DomainID: "domain-1",
				Capabilities: model.NodeCapabilities{CanAcceptInbound: true, CanForward: true, HasPublicIP: true},
			},
		},
		Edges: []model.Edge{
			{ID: "e1", FromNodeID: "node-1", ToNodeID: "node-2", Type: "direct", EndpointHost: "203.0.113.2", EndpointPort: 0, Transport: "udp", IsEnabled: true},
			{ID: "e2", FromNodeID: "node-2", ToNodeID: "node-1", Type: "direct", EndpointHost: "203.0.113.1", EndpointPort: 0, Transport: "udp", IsEnabled: true},
			{ID: "e3", FromNodeID: "node-1", ToNodeID: "node-3", Type: "direct", EndpointHost: "203.0.113.3", EndpointPort: 0, Transport: "udp", IsEnabled: true},
			{ID: "e4", FromNodeID: "node-3", ToNodeID: "node-1", Type: "direct", EndpointHost: "203.0.113.1", EndpointPort: 0, Transport: "udp", IsEnabled: true},
			{ID: "e5", FromNodeID: "node-2", ToNodeID: "node-3", Type: "direct", EndpointHost: "203.0.113.3", EndpointPort: 0, Transport: "udp", IsEnabled: true},
			{ID: "e6", FromNodeID: "node-3", ToNodeID: "node-2", Type: "direct", EndpointHost: "203.0.113.2", EndpointPort: 0, Transport: "udp", IsEnabled: true},
		},
	}
}

func natHubTopo() *model.Topology {
	return &model.Topology{
		Project: model.Project{ID: "test-002", Name: "NAT Hub"},
		Domains: []model.Domain{{
			ID: "domain-1", Name: "hub-net", CIDR: "10.20.0.0/24",
			AllocationMode: "auto", RoutingMode: "babel",
		}},
		Nodes: []model.Node{
			{
				ID: "node-1", Name: "hub", Hostname: "hub.example.com",
				Role: "router", DomainID: "domain-1",
				Capabilities: model.NodeCapabilities{CanAcceptInbound: true, CanForward: true, CanRelay: true, HasPublicIP: true},
			},
			{
				ID: "node-2", Name: "client-a",
				Role: "peer", DomainID: "domain-1",
				Capabilities: model.NodeCapabilities{CanAcceptInbound: false, CanForward: false, HasPublicIP: false},
			},
			{
				ID: "node-3", Name: "client-b",
				Role: "peer", DomainID: "domain-1",
				Capabilities: model.NodeCapabilities{CanAcceptInbound: false, CanForward: false, HasPublicIP: false},
			},
		},
		Edges: []model.Edge{
			{ID: "e1", FromNodeID: "node-2", ToNodeID: "node-1", Type: "public-endpoint", EndpointHost: "198.51.100.1", EndpointPort: 0, Transport: "udp", IsEnabled: true},
			{ID: "e2", FromNodeID: "node-3", ToNodeID: "node-1", Type: "public-endpoint", EndpointHost: "198.51.100.1", EndpointPort: 0, Transport: "udp", IsEnabled: true},
		},
	}
}

func TestDerivePeers_SimpleMesh(t *testing.T) {
	topo := simpleMeshTopo()
	// Assign overlay IPs.
	topo.Nodes[0].OverlayIP = "10.11.0.1"
	topo.Nodes[1].OverlayIP = "10.11.0.2"
	topo.Nodes[2].OverlayIP = "10.11.0.3"

	keys := testKeys()
	peerMap, _, _ := DerivePeers(topo, keys)

	// Full mesh: each node should have 2 peers.
	for _, node := range topo.Nodes {
		peers := peerMap[node.ID]
		if len(peers) != 2 {
			t.Errorf("node %s should have 2 peers, got %d", node.Name, len(peers))
		}
	}
}

func TestDerivePeers_EdgeConsistency(t *testing.T) {
	topo := simpleMeshTopo()
	topo.Nodes[0].OverlayIP = "10.11.0.1"
	topo.Nodes[1].OverlayIP = "10.11.0.2"
	topo.Nodes[2].OverlayIP = "10.11.0.3"

	keys := testKeys()
	peerMap, _, _ := DerivePeers(topo, keys)

	// node-1's peers should be node-2 and node-3.
	node1Peers := peerMap["node-1"]
	peerIDs := make(map[string]bool)
	for _, p := range node1Peers {
		peerIDs[p.NodeID] = true
	}
	if !peerIDs["node-2"] {
		t.Errorf("node-1 is missing peer node-2")
	}
	if !peerIDs["node-3"] {
		t.Errorf("node-1 is missing peer node-3")
	}
}

func TestDerivePeers_EndpointCorrect(t *testing.T) {
	topo := simpleMeshTopo()
	topo.Nodes[0].OverlayIP = "10.11.0.1"
	topo.Nodes[1].OverlayIP = "10.11.0.2"
	topo.Nodes[2].OverlayIP = "10.11.0.3"

	keys := testKeys()
	peerMap, _, _ := DerivePeers(topo, keys)

	// node-1 -> node-2 endpoint should be 203.0.113.2:51820.
	for _, p := range peerMap["node-1"] {
		if p.NodeID == "node-2" {
			if p.Endpoint != "203.0.113.2:51820" {
				t.Errorf("node-1->node-2 endpoint should be 203.0.113.2:51820, got %s", p.Endpoint)
			}
		}
	}
}

func TestDerivePeers_AllowedIPs(t *testing.T) {
	topo := simpleMeshTopo()
	topo.Nodes[0].OverlayIP = "10.11.0.1"
	topo.Nodes[1].OverlayIP = "10.11.0.2"
	topo.Nodes[2].OverlayIP = "10.11.0.3"

	keys := testKeys()
	peerMap, _, _ := DerivePeers(topo, keys)

	// per-peer architecture: AllowedIPs uses a permissive policy.
	for _, p := range peerMap["node-1"] {
		if p.NodeID == "node-2" {
			if len(p.AllowedIPs) != 2 || p.AllowedIPs[0] != "0.0.0.0/0" || p.AllowedIPs[1] != "::/0" {
				t.Errorf("AllowedIPs should be [0.0.0.0/0, ::/0], got %v", p.AllowedIPs)
			}
		}
	}
}

func TestDerivePeers_NATKeepalive(t *testing.T) {
	topo := natHubTopo()
	topo.Nodes[0].OverlayIP = "10.20.0.1"
	topo.Nodes[1].OverlayIP = "10.20.0.2"
	topo.Nodes[2].OverlayIP = "10.20.0.3"

	keys := testKeys()
	peerMap, _, _ := DerivePeers(topo, keys)

	// NAT nodes (node-2, node-3) toward the hub should have PersistentKeepalive.
	for _, p := range peerMap["node-2"] {
		if p.NodeID == "node-1" && p.PersistentKeepalive == 0 {
			t.Errorf("NAT node toward hub should have PersistentKeepalive")
		}
	}

	// Hub (node-1) should have node-2 and node-3 as peers.
	if len(peerMap["node-1"]) != 2 {
		t.Errorf("Hub should have 2 peers, got %d", len(peerMap["node-1"]))
	}
}

func TestDerivePeers_DisabledEdgeIgnored(t *testing.T) {
	topo := simpleMeshTopo()
	topo.Nodes[0].OverlayIP = "10.11.0.1"
	topo.Nodes[1].OverlayIP = "10.11.0.2"
	topo.Nodes[2].OverlayIP = "10.11.0.3"

	// Disable the node-1 <-> node-2 link.
	topo.Edges[0].IsEnabled = false
	topo.Edges[1].IsEnabled = false

	keys := testKeys()
	peerMap, _, _ := DerivePeers(topo, keys)

	// node-1 should have 1 peer (node-3), node-2 dropped.
	if len(peerMap["node-1"]) != 1 {
		t.Errorf("node-1 should have 1 peer after dropping node-2, got %d", len(peerMap["node-1"]))
	}
}

// unidirectionalPublicEndpointTopo models two nodes that both have a public IP
// but with only a single unidirectional edge drawn (A->B). In this case A must
// have PersistentKeepalive, because B has no reverse edge to actively connect to A.
func unidirectionalPublicEndpointTopo() *model.Topology {
	return &model.Topology{
		Project: model.Project{ID: "test-003", Name: "Unidirectional Public Endpoint"},
		Domains: []model.Domain{{
			ID: "domain-1", Name: "uni-net", CIDR: "10.30.0.0/24",
			AllocationMode: "auto", RoutingMode: "babel",
		}},
		Nodes: []model.Node{
			{
				ID: "node-1", Name: "server-a", Hostname: "a.example.com",
				Role: "router", DomainID: "domain-1",
				Capabilities: model.NodeCapabilities{CanAcceptInbound: true, CanForward: true, HasPublicIP: true},
			},
			{
				ID: "node-2", Name: "server-b", Hostname: "b.example.com",
				Role: "router", DomainID: "domain-1",
				Capabilities: model.NodeCapabilities{CanAcceptInbound: true, CanForward: true, HasPublicIP: true},
			},
		},
		Edges: []model.Edge{
			// Only the single unidirectional edge A->B, no B->A.
			{ID: "e1", FromNodeID: "node-1", ToNodeID: "node-2", Type: "public-endpoint", EndpointHost: "203.0.113.2", EndpointPort: 0, Transport: "udp", IsEnabled: true},
		},
	}
}

func TestDerivePeers_UnidirectionalKeepalive(t *testing.T) {
	topo := unidirectionalPublicEndpointTopo()
	topo.Nodes[0].OverlayIP = "10.30.0.1"
	topo.Nodes[1].OverlayIP = "10.30.0.2"

	keys := testKeys()
	peerMap, _, _ := DerivePeers(topo, keys)

	// node-1 (initiator) should have node-2 as a peer.
	node1Peers := peerMap["node-1"]
	if len(node1Peers) != 1 {
		t.Fatalf("node-1 should have 1 peer, got %d", len(node1Peers))
	}

	// node-1->node-2: because there is no reverse edge (node-2->node-1), it must have keepalive.
	if node1Peers[0].PersistentKeepalive == 0 {
		t.Errorf("unidirectional edge case: node-1->node-2 should have PersistentKeepalive (want 25, got 0)")
	}

	// node-2 should have an auto-generated reverse peer (node-1).
	node2Peers := peerMap["node-2"]
	if len(node2Peers) != 1 {
		t.Fatalf("node-2 should have 1 auto-generated peer, got %d", len(node2Peers))
	}

	// node-2's reverse peer has no endpoint, but node-2 can accept inbound so it needs no keepalive.
	if node2Peers[0].Endpoint != "" {
		t.Errorf("auto-generated reverse peer should have no endpoint, got %s", node2Peers[0].Endpoint)
	}
}

func TestDerivePeers_BidirectionalNoExtraKeepalive(t *testing.T) {
	topo := simpleMeshTopo()
	topo.Nodes[0].OverlayIP = "10.11.0.1"
	topo.Nodes[1].OverlayIP = "10.11.0.2"
	topo.Nodes[2].OverlayIP = "10.11.0.3"

	keys := testKeys()
	peerMap, _, _ := DerivePeers(topo, keys)

	// With a bidirectional edge and both having a public IP, no keepalive is needed.
	for _, p := range peerMap["node-1"] {
		if p.PersistentKeepalive != 0 {
			t.Errorf("bidirectional edge case: node-1->%s should not have PersistentKeepalive (want 0, got %d)",
				p.NodeID, p.PersistentKeepalive)
		}
	}
}

func TestDerivePeers_PerPeerFields(t *testing.T) {
	topo := simpleMeshTopo()
	topo.Nodes[0].OverlayIP = "10.11.0.1"
	topo.Nodes[1].OverlayIP = "10.11.0.2"
	topo.Nodes[2].OverlayIP = "10.11.0.3"

	keys := testKeys()
	peerMap, _, _ := DerivePeers(topo, keys)

	// Verify the per-peer fields of node-1's first peer.
	node1Peers := peerMap["node-1"]
	if len(node1Peers) != 2 {
		t.Fatalf("node-1 should have 2 peers, got %d", len(node1Peers))
	}

	for _, p := range node1Peers {
		// InterfaceName format: wg-<peername>.
		if p.InterfaceName == "" {
			t.Errorf("peer %s InterfaceName should not be empty", p.NodeID)
		}
		if len(p.InterfaceName) > 15 {
			t.Errorf("peer %s InterfaceName exceeds 15 chars: %s", p.NodeID, p.InterfaceName)
		}

		// ListenPort should be set and increment from basePort.
		if p.ListenPort == 0 {
			t.Errorf("peer %s ListenPort should not be 0", p.NodeID)
		}

		// Transit IP should be set.
		if p.LocalTransitIP == "" {
			t.Errorf("peer %s LocalTransitIP should not be empty", p.NodeID)
		}
		if p.RemoteTransitIP == "" {
			t.Errorf("peer %s RemoteTransitIP should not be empty", p.NodeID)
		}

		// Link-local should be set.
		if p.LocalLinkLocal == "" {
			t.Errorf("peer %s LocalLinkLocal should not be empty", p.NodeID)
		}
		if p.RemoteLinkLocal == "" {
			t.Errorf("peer %s RemoteLinkLocal should not be empty", p.NodeID)
		}
	}

	// Verify the two peers have different ListenPorts (incremental allocation).
	if node1Peers[0].ListenPort == node1Peers[1].ListenPort {
		t.Errorf("the two peer interfaces of the same node should have different ListenPorts, both are %d", node1Peers[0].ListenPort)
	}

	// Verify transit IP complementarity: node-1->node-2 local should equal node-2->node-1 remote.
	var n1ToN2, n2ToN1 *PeerInfo
	for i, p := range peerMap["node-1"] {
		if p.NodeID == "node-2" {
			n1ToN2 = &peerMap["node-1"][i]
		}
	}
	for i, p := range peerMap["node-2"] {
		if p.NodeID == "node-1" {
			n2ToN1 = &peerMap["node-2"][i]
		}
	}
	if n1ToN2 != nil && n2ToN1 != nil {
		if n1ToN2.LocalTransitIP != n2ToN1.RemoteTransitIP {
			t.Errorf("transit IP not complementary: n1->n2 local=%s, n2->n1 remote=%s",
				n1ToN2.LocalTransitIP, n2ToN1.RemoteTransitIP)
		}
		if n1ToN2.RemoteTransitIP != n2ToN1.LocalTransitIP {
			t.Errorf("transit IP not complementary: n1->n2 remote=%s, n2->n1 local=%s",
				n1ToN2.RemoteTransitIP, n2ToN1.LocalTransitIP)
		}
	}
}

func TestGenerateRouterID(t *testing.T) {
	// Verify the MAC-48 format.
	rid := GenerateRouterID("node-1")
	if len(rid) != 17 { // xx:xx:xx:xx:xx:xx = 17 chars
		t.Errorf("RouterID length should be 17, got %d: %s", len(rid), rid)
	}

	// Verify the format contains 5 colons.
	colonCount := 0
	for _, c := range rid {
		if c == ':' {
			colonCount++
		}
	}
	if colonCount != 5 {
		t.Errorf("RouterID should contain 5 colons, got %d: %s", colonCount, rid)
	}

	// Verify stability (same input -> same output).
	rid2 := GenerateRouterID("node-1")
	if rid != rid2 {
		t.Errorf("RouterID not stable: first=%s, second=%s", rid, rid2)
	}

	// Verify different input -> different output.
	rid3 := GenerateRouterID("node-2")
	if rid == rid3 {
		t.Errorf("RouterID should differ for different nodes: node-1=%s, node-2=%s", rid, rid3)
	}
}

func TestWgInterfaceName(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"beta", "wg-beta"},
		{"Alpha", "wg-alpha"},                   // uppercase to lowercase
		{"my_server", "wg-my-server"},           // underscore to hyphen
		{"a.b.c", "wg-a-b-c"},                   // dot to hyphen
		{"abcdefghijklmnop", "wg-abcdefghf39d"}, // over 15 chars: use a hash suffix to avoid truncation collisions
	}

	for _, tt := range tests {
		got := wgInterfaceName(tt.input)
		if got != tt.expected {
			t.Errorf("wgInterfaceName(%q) = %q, want %q", tt.input, got, tt.expected)
		}
		if len(got) > 15 {
			t.Errorf("wgInterfaceName(%q) = %q exceeds 15 chars", tt.input, got)
		}
	}
}

func TestCompile_SimpleMesh(t *testing.T) {
	topo := simpleMeshTopo()
	keys := testKeys()

	c := NewCompiler()
	result, err := c.Compile(topo, keys)
	if err != nil {
		t.Fatalf("compile failed: %v", err)
	}

	// Every node should have an overlay IP.
	for _, node := range result.Topology.Nodes {
		if node.OverlayIP == "" {
			t.Errorf("node %s is missing its overlay IP", node.Name)
		}
	}

	// Verify the PeerMap.
	if len(result.PeerMap) != 3 {
		t.Errorf("PeerMap should have 3 entries, got %d", len(result.PeerMap))
	}

	// Verify the Manifest.
	if result.Manifest.NodeCount != 3 {
		t.Errorf("Manifest NodeCount should be 3, got %d", result.Manifest.NodeCount)
	}
}

// extractPortFromEndpoint extracts the port number from "host:port" or "[ipv6]:port".
func extractPortFromEndpoint(endpoint string) int {
	if endpoint == "" {
		return 0
	}
	// Scan from the end for the last colon.
	lastColon := -1
	for i := len(endpoint) - 1; i >= 0; i-- {
		if endpoint[i] == ':' {
			lastColon = i
			break
		}
	}
	if lastColon < 0 {
		return 0
	}
	port := 0
	for _, c := range endpoint[lastColon+1:] {
		if c >= '0' && c <= '9' {
			port = port*10 + int(c-'0')
		}
	}
	return port
}

// TestDerivePeers_PortEndpointSymmetry verifies the core invariant:
// for each pair (A, B), the endpoint port A uses to connect to B == the interface
// ListenPort B allocated for A.
func TestDerivePeers_PortEndpointSymmetry(t *testing.T) {
	topo := simpleMeshTopo()
	topo.Nodes[0].OverlayIP = "10.11.0.1"
	topo.Nodes[1].OverlayIP = "10.11.0.2"
	topo.Nodes[2].OverlayIP = "10.11.0.3"

	keys := testKeys()
	peerMap, _, _ := DerivePeers(topo, keys)

	// For each peer of each node, verify port symmetry.
	for nodeID, peers := range peerMap {
		for _, p := range peers {
			if p.Endpoint == "" {
				continue // skip peers without an endpoint (e.g. auto-generated reverse peers with no endpoint)
			}

			endpointPort := extractPortFromEndpoint(p.Endpoint)

			// Find the entry pointing back at the current node in the remote node's peer list.
			remotePeers := peerMap[p.NodeID]
			found := false
			for _, rp := range remotePeers {
				if rp.NodeID == nodeID {
					found = true
					if endpointPort != rp.ListenPort {
						t.Errorf("port symmetry violated: %s->%s endpoint port=%d, but the ListenPort %s allocated for %s=%d",
							nodeID, p.NodeID, endpointPort, p.NodeID, nodeID, rp.ListenPort)
					}
					break
				}
			}
			if !found {
				t.Errorf("symmetry check failed: %s has peer %s, but %s has no reverse peer %s",
					nodeID, p.NodeID, p.NodeID, nodeID)
			}
		}
	}
}

// TestDerivePeers_MultiPeerPortIncrement verifies that ports increment correctly when a hub node has multiple peers.
func TestDerivePeers_MultiPeerPortIncrement(t *testing.T) {
	topo := natHubTopo()
	topo.Nodes[0].OverlayIP = "10.20.0.1"
	topo.Nodes[1].OverlayIP = "10.20.0.2"
	topo.Nodes[2].OverlayIP = "10.20.0.3"

	keys := testKeys()
	peerMap, _, _ := DerivePeers(topo, keys)

	// Hub (node-1) should have 2 peer interfaces with incrementing ports.
	hubPeers := peerMap["node-1"]
	if len(hubPeers) != 2 {
		t.Fatalf("Hub should have 2 peers, got %d", len(hubPeers))
	}

	// The hub's two interface ports should differ and increment from base.
	if hubPeers[0].ListenPort == hubPeers[1].ListenPort {
		t.Errorf("the hub's two interface ports should not be the same, both are %d", hubPeers[0].ListenPort)
	}

	// Verify each client's endpoint port == the ListenPort the hub allocated for that client.
	for _, clientID := range []string{"node-2", "node-3"} {
		clientPeers := peerMap[clientID]
		if len(clientPeers) != 1 {
			t.Fatalf("Client %s should have 1 peer, got %d", clientID, len(clientPeers))
		}

		cp := clientPeers[0]
		if cp.Endpoint == "" {
			t.Errorf("Client %s endpoint should not be empty", clientID)
			continue
		}

		endpointPort := extractPortFromEndpoint(cp.Endpoint)

		// Find the hub's peer corresponding to this client.
		for _, hp := range hubPeers {
			if hp.NodeID == clientID {
				if endpointPort != hp.ListenPort {
					t.Errorf("Client %s endpoint port=%d, but the ListenPort the Hub allocated for %s=%d",
						clientID, endpointPort, clientID, hp.ListenPort)
				}
				break
			}
		}
	}
}
