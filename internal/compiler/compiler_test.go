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
	// 先分配 IP
	topo.Nodes[0].OverlayIP = "10.10.0.1"
	topo.Nodes[1].OverlayIP = "10.10.0.2"
	topo.Nodes[2].OverlayIP = "10.10.0.3"

	keys := testKeys()
	peerMap := DerivePeers(topo, keys)

	// 全互联：每个节点应有 2 个 peer
	for _, node := range topo.Nodes {
		peers := peerMap[node.ID]
		if len(peers) != 2 {
			t.Errorf("节点 %s 期望 2 个 peer, 得到 %d", node.Name, len(peers))
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

	// node-1 的 peer 列表应包含 node-2 和 node-3
	node1Peers := peerMap["node-1"]
	peerIDs := make(map[string]bool)
	for _, p := range node1Peers {
		peerIDs[p.NodeID] = true
	}
	if !peerIDs["node-2"] {
		t.Errorf("node-1 的 peer 列表应包含 node-2")
	}
	if !peerIDs["node-3"] {
		t.Errorf("node-1 的 peer 列表应包含 node-3")
	}
}

func TestDerivePeers_EndpointCorrect(t *testing.T) {
	topo := simpleMeshTopo()
	topo.Nodes[0].OverlayIP = "10.10.0.1"
	topo.Nodes[1].OverlayIP = "10.10.0.2"
	topo.Nodes[2].OverlayIP = "10.10.0.3"

	keys := testKeys()
	peerMap := DerivePeers(topo, keys)

	// node-1 -> node-2 的 endpoint 应该是 203.0.113.2:51820
	for _, p := range peerMap["node-1"] {
		if p.NodeID == "node-2" {
			if p.Endpoint != "203.0.113.2:51820" {
				t.Errorf("node-1->node-2 endpoint 期望 203.0.113.2:51820, 得到 %s", p.Endpoint)
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

	// 点对点模式：AllowedIPs 应只包含对端 /32
	for _, p := range peerMap["node-1"] {
		if p.NodeID == "node-2" {
			if len(p.AllowedIPs) != 1 || p.AllowedIPs[0] != "10.10.0.2/32" {
				t.Errorf("AllowedIPs 期望 [10.10.0.2/32], 得到 %v", p.AllowedIPs)
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

	// NAT 客户端 (node-2, node-3) 连接 hub 时应有 PersistentKeepalive
	for _, p := range peerMap["node-2"] {
		if p.NodeID == "node-1" && p.PersistentKeepalive == 0 {
			t.Errorf("NAT 客户端连接 hub 应有 PersistentKeepalive")
		}
	}

	// Hub (node-1) 没有出站边，所以 peer 列表为空
	if len(peerMap["node-1"]) != 0 {
		t.Errorf("Hub 没有出站边，peer 列表应为空, 得到 %d", len(peerMap["node-1"]))
	}
}

func TestDerivePeers_DisabledEdgeIgnored(t *testing.T) {
	topo := simpleMeshTopo()
	topo.Nodes[0].OverlayIP = "10.10.0.1"
	topo.Nodes[1].OverlayIP = "10.10.0.2"
	topo.Nodes[2].OverlayIP = "10.10.0.3"

	// 禁用一条边
	topo.Edges[0].IsEnabled = false

	keys := testKeys()
	peerMap := DerivePeers(topo, keys)

	// node-1 应只有 1 个 peer（node-3），因为到 node-2 的边被禁用
	if len(peerMap["node-1"]) != 1 {
		t.Errorf("node-1 禁用一条边后期望 1 个 peer, 得到 %d", len(peerMap["node-1"]))
	}
}

func TestCompile_SimpleMesh(t *testing.T) {
	topo := simpleMeshTopo()
	keys := testKeys()

	c := NewCompiler()
	result, err := c.Compile(topo, keys)
	if err != nil {
		t.Fatalf("编译失败: %v", err)
	}

	// 检查 IP 已分配
	for _, node := range result.Topology.Nodes {
		if node.OverlayIP == "" {
			t.Errorf("节点 %s 没有分配 IP", node.Name)
		}
	}

	// 检查 PeerMap 已生成
	if len(result.PeerMap) != 3 {
		t.Errorf("PeerMap 期望 3 个节点, 得到 %d", len(result.PeerMap))
	}

	// 检查 Manifest
	if result.Manifest.NodeCount != 3 {
		t.Errorf("Manifest NodeCount 期望 3, 得到 %d", result.Manifest.NodeCount)
	}
}
