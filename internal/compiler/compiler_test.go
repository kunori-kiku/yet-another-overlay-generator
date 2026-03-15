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
			ID: "domain-1", Name: "mesh", CIDR: "10.10.0.0/24",
			AllocationMode: "auto", RoutingMode: "babel",
		}},
		Nodes: []model.Node{
			{
				ID: "node-1", Name: "alpha", Hostname: "alpha.example.com",
				Role: "router", DomainID: "domain-1", ListenPort: 51820,
				Capabilities: model.NodeCapabilities{CanAcceptInbound: true, CanForward: true, HasPublicIP: true},
			},
			{
				ID: "node-2", Name: "beta", Hostname: "beta.example.com",
				Role: "router", DomainID: "domain-1", ListenPort: 51820,
				Capabilities: model.NodeCapabilities{CanAcceptInbound: true, CanForward: true, HasPublicIP: true},
			},
			{
				ID: "node-3", Name: "gamma", Hostname: "gamma.example.com",
				Role: "router", DomainID: "domain-1", ListenPort: 51820,
				Capabilities: model.NodeCapabilities{CanAcceptInbound: true, CanForward: true, HasPublicIP: true},
			},
		},
		Edges: []model.Edge{
			{ID: "e1", FromNodeID: "node-1", ToNodeID: "node-2", Type: "direct", EndpointHost: "203.0.113.2", EndpointPort: 51820, Transport: "udp", IsEnabled: true},
			{ID: "e2", FromNodeID: "node-2", ToNodeID: "node-1", Type: "direct", EndpointHost: "203.0.113.1", EndpointPort: 51820, Transport: "udp", IsEnabled: true},
			{ID: "e3", FromNodeID: "node-1", ToNodeID: "node-3", Type: "direct", EndpointHost: "203.0.113.3", EndpointPort: 51820, Transport: "udp", IsEnabled: true},
			{ID: "e4", FromNodeID: "node-3", ToNodeID: "node-1", Type: "direct", EndpointHost: "203.0.113.1", EndpointPort: 51820, Transport: "udp", IsEnabled: true},
			{ID: "e5", FromNodeID: "node-2", ToNodeID: "node-3", Type: "direct", EndpointHost: "203.0.113.3", EndpointPort: 51820, Transport: "udp", IsEnabled: true},
			{ID: "e6", FromNodeID: "node-3", ToNodeID: "node-2", Type: "direct", EndpointHost: "203.0.113.2", EndpointPort: 51820, Transport: "udp", IsEnabled: true},
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
				Role: "router", DomainID: "domain-1", ListenPort: 51820,
				Capabilities: model.NodeCapabilities{CanAcceptInbound: true, CanForward: true, CanRelay: true, HasPublicIP: true},
			},
			{
				ID: "node-2", Name: "client-a",
				Role: "peer", DomainID: "domain-1", ListenPort: 51820,
				Capabilities: model.NodeCapabilities{CanAcceptInbound: false, CanForward: false, HasPublicIP: false},
			},
			{
				ID: "node-3", Name: "client-b",
				Role: "peer", DomainID: "domain-1", ListenPort: 51820,
				Capabilities: model.NodeCapabilities{CanAcceptInbound: false, CanForward: false, HasPublicIP: false},
			},
		},
		Edges: []model.Edge{
			{ID: "e1", FromNodeID: "node-2", ToNodeID: "node-1", Type: "public-endpoint", EndpointHost: "198.51.100.1", EndpointPort: 51820, Transport: "udp", IsEnabled: true},
			{ID: "e2", FromNodeID: "node-3", ToNodeID: "node-1", Type: "public-endpoint", EndpointHost: "198.51.100.1", EndpointPort: 51820, Transport: "udp", IsEnabled: true},
		},
	}
}

func TestDerivePeers_SimpleMesh(t *testing.T) {
	topo := simpleMeshTopo()
	//  IP
	topo.Nodes[0].OverlayIP = "10.10.0.1"
	topo.Nodes[1].OverlayIP = "10.10.0.2"
	topo.Nodes[2].OverlayIP = "10.10.0.3"

	keys := testKeys()
	peerMap := DerivePeers(topo, keys)

	// ： 2  peer
	for _, node := range topo.Nodes {
		peers := peerMap[node.ID]
		if len(peers) != 2 {
			t.Errorf(" %s  2  peer,  %d", node.Name, len(peers))
		}
	}
}

func TestDerivePeers_EdgeConsistency(t *testing.T) {
	topo := simpleMeshTopo()
	topo.Nodes[0].OverlayIP = "10.10.0.1"
	topo.Nodes[1].OverlayIP = "10.10.0.2"
	topo.Nodes[2].OverlayIP = "10.10.0.3"

	keys := testKeys()
	peerMap := DerivePeers(topo, keys)

	// node-1  peer  node-2  node-3
	node1Peers := peerMap["node-1"]
	peerIDs := make(map[string]bool)
	for _, p := range node1Peers {
		peerIDs[p.NodeID] = true
	}
	if !peerIDs["node-2"] {
		t.Errorf("node-1  peer  node-2")
	}
	if !peerIDs["node-3"] {
		t.Errorf("node-1  peer  node-3")
	}
}

func TestDerivePeers_EndpointCorrect(t *testing.T) {
	topo := simpleMeshTopo()
	topo.Nodes[0].OverlayIP = "10.10.0.1"
	topo.Nodes[1].OverlayIP = "10.10.0.2"
	topo.Nodes[2].OverlayIP = "10.10.0.3"

	keys := testKeys()
	peerMap := DerivePeers(topo, keys)

	// node-1 -> node-2  endpoint  203.0.113.2:51820
	for _, p := range peerMap["node-1"] {
		if p.NodeID == "node-2" {
			if p.Endpoint != "203.0.113.2:51820" {
				t.Errorf("node-1->node-2 endpoint  203.0.113.2:51820,  %s", p.Endpoint)
			}
		}
	}
}

func TestDerivePeers_AllowedIPs(t *testing.T) {
	topo := simpleMeshTopo()
	topo.Nodes[0].OverlayIP = "10.10.0.1"
	topo.Nodes[1].OverlayIP = "10.10.0.2"
	topo.Nodes[2].OverlayIP = "10.10.0.3"

	keys := testKeys()
	peerMap := DerivePeers(topo, keys)

	// ：AllowedIPs  /32
	for _, p := range peerMap["node-1"] {
		if p.NodeID == "node-2" {
			if len(p.AllowedIPs) != 1 || p.AllowedIPs[0] != "10.10.0.2/32" {
				t.Errorf("AllowedIPs  [10.10.0.2/32],  %v", p.AllowedIPs)
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
	peerMap := DerivePeers(topo, keys)

	// NAT  (node-2, node-3)  hub  PersistentKeepalive
	for _, p := range peerMap["node-2"] {
		if p.NodeID == "node-1" && p.PersistentKeepalive == 0 {
			t.Errorf("NAT  hub  PersistentKeepalive")
		}
	}

	// Hub (node-1)  node-2  node-3， Peer 
	if len(peerMap["node-1"]) != 2 {
		t.Errorf("Hub  2  Peer， %d", len(peerMap["node-1"]))
	}
}

func TestDerivePeers_DisabledEdgeIgnored(t *testing.T) {
	topo := simpleMeshTopo()
	topo.Nodes[0].OverlayIP = "10.10.0.1"
	topo.Nodes[1].OverlayIP = "10.10.0.2"
	topo.Nodes[2].OverlayIP = "10.10.0.3"

	//  node-1 <-> node-2 
	topo.Edges[0].IsEnabled = false
	topo.Edges[1].IsEnabled = false

	keys := testKeys()
	peerMap := DerivePeers(topo, keys)

	// node-1  1  peer（node-3）， node-2 
	if len(peerMap["node-1"]) != 1 {
		t.Errorf("node-1  node-2  1  peer,  %d", len(peerMap["node-1"]))
	}
}

// unidirectionalPublicEndpointTopo 模拟两个都有公网IP的节点，但只画了一条单向edge (A→B)
// 这种情况下 A 应该有 PersistentKeepalive，因为 B 没有反向 edge 去主动连 A
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
				Role: "router", DomainID: "domain-1", ListenPort: 51820,
				Capabilities: model.NodeCapabilities{CanAcceptInbound: true, CanForward: true, HasPublicIP: true},
			},
			{
				ID: "node-2", Name: "server-b", Hostname: "b.example.com",
				Role: "router", DomainID: "domain-1", ListenPort: 51820,
				Capabilities: model.NodeCapabilities{CanAcceptInbound: true, CanForward: true, HasPublicIP: true},
			},
		},
		Edges: []model.Edge{
			// 只有 A→B 这一条单向 edge，没有 B→A
			{ID: "e1", FromNodeID: "node-1", ToNodeID: "node-2", Type: "public-endpoint", EndpointHost: "203.0.113.2", EndpointPort: 51820, Transport: "udp", IsEnabled: true},
		},
	}
}

func TestDerivePeers_UnidirectionalKeepalive(t *testing.T) {
	topo := unidirectionalPublicEndpointTopo()
	topo.Nodes[0].OverlayIP = "10.30.0.1"
	topo.Nodes[1].OverlayIP = "10.30.0.2"

	keys := testKeys()
	peerMap := DerivePeers(topo, keys)

	// node-1 (发起方) 应该有 node-2 作为 peer
	node1Peers := peerMap["node-1"]
	if len(node1Peers) != 1 {
		t.Fatalf("node-1 应该有 1 个 peer，实际 %d", len(node1Peers))
	}

	// node-1→node-2: 因为没有反向 edge (node-2→node-1)，所以必须有 keepalive
	if node1Peers[0].PersistentKeepalive == 0 {
		t.Errorf("单向 edge 场景: node-1→node-2 应该有 PersistentKeepalive (期望 25，实际 0)")
	}

	// node-2 应该有自动生成的反向 peer (node-1)
	node2Peers := peerMap["node-2"]
	if len(node2Peers) != 1 {
		t.Fatalf("node-2 应该有 1 个自动生成的 peer，实际 %d", len(node2Peers))
	}

	// node-2 的反向 peer 没有 endpoint，但 node-2 可以接受入站，所以不需要 keepalive
	if node2Peers[0].Endpoint != "" {
		t.Errorf("自动生成的反向 peer 不应该有 endpoint，实际 %s", node2Peers[0].Endpoint)
	}
}

func TestDerivePeers_BidirectionalNoExtraKeepalive(t *testing.T) {
	topo := simpleMeshTopo()
	topo.Nodes[0].OverlayIP = "10.10.0.1"
	topo.Nodes[1].OverlayIP = "10.10.0.2"
	topo.Nodes[2].OverlayIP = "10.10.0.3"

	keys := testKeys()
	peerMap := DerivePeers(topo, keys)

	// 双向 edge + 都有公网IP 的情况下，不需要 keepalive
	for _, p := range peerMap["node-1"] {
		if p.PersistentKeepalive != 0 {
			t.Errorf("双向 edge 场景: node-1→%s 不应该有 PersistentKeepalive (期望 0，实际 %d)",
				p.NodeID, p.PersistentKeepalive)
		}
	}
}

func TestCompile_SimpleMesh(t *testing.T) {
	topo := simpleMeshTopo()
	keys := testKeys()

	c := NewCompiler()
	result, err := c.Compile(topo, keys)
	if err != nil {
		t.Fatalf(": %v", err)
	}

	//  IP 
	for _, node := range result.Topology.Nodes {
		if node.OverlayIP == "" {
			t.Errorf(" %s  IP", node.Name)
		}
	}

	//  PeerMap 
	if len(result.PeerMap) != 3 {
		t.Errorf("PeerMap  3 ,  %d", len(result.PeerMap))
	}

	//  Manifest
	if result.Manifest.NodeCount != 3 {
		t.Errorf("Manifest NodeCount  3,  %d", result.Manifest.NodeCount)
	}
}
