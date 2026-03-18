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
			{ID: "e1", FromNodeID: "node-2", ToNodeID: "node-1", Type: "public-endpoint", EndpointHost: "198.51.100.1", EndpointPort: 0, Transport: "udp", IsEnabled: true},
			{ID: "e2", FromNodeID: "node-3", ToNodeID: "node-1", Type: "public-endpoint", EndpointHost: "198.51.100.1", EndpointPort: 0, Transport: "udp", IsEnabled: true},
		},
	}
}

func TestDerivePeers_SimpleMesh(t *testing.T) {
	topo := simpleMeshTopo()
	//  IP
	topo.Nodes[0].OverlayIP = "10.11.0.1"
	topo.Nodes[1].OverlayIP = "10.11.0.2"
	topo.Nodes[2].OverlayIP = "10.11.0.3"

	keys := testKeys()
	peerMap, _ := DerivePeers(topo, keys)

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
	topo.Nodes[0].OverlayIP = "10.11.0.1"
	topo.Nodes[1].OverlayIP = "10.11.0.2"
	topo.Nodes[2].OverlayIP = "10.11.0.3"

	keys := testKeys()
	peerMap, _ := DerivePeers(topo, keys)

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
	topo.Nodes[0].OverlayIP = "10.11.0.1"
	topo.Nodes[1].OverlayIP = "10.11.0.2"
	topo.Nodes[2].OverlayIP = "10.11.0.3"

	keys := testKeys()
	peerMap, _ := DerivePeers(topo, keys)

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
	topo.Nodes[0].OverlayIP = "10.11.0.1"
	topo.Nodes[1].OverlayIP = "10.11.0.2"
	topo.Nodes[2].OverlayIP = "10.11.0.3"

	keys := testKeys()
	peerMap, _ := DerivePeers(topo, keys)

	// per-peer 架构：AllowedIPs 使用宽松策略
	for _, p := range peerMap["node-1"] {
		if p.NodeID == "node-2" {
			if len(p.AllowedIPs) != 2 || p.AllowedIPs[0] != "0.0.0.0/0" || p.AllowedIPs[1] != "::/0" {
				t.Errorf("AllowedIPs 应为 [0.0.0.0/0, ::/0]，实际 %v", p.AllowedIPs)
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
	peerMap, _ := DerivePeers(topo, keys)

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
	topo.Nodes[0].OverlayIP = "10.11.0.1"
	topo.Nodes[1].OverlayIP = "10.11.0.2"
	topo.Nodes[2].OverlayIP = "10.11.0.3"

	//  node-1 <-> node-2 
	topo.Edges[0].IsEnabled = false
	topo.Edges[1].IsEnabled = false

	keys := testKeys()
	peerMap, _ := DerivePeers(topo, keys)

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
			{ID: "e1", FromNodeID: "node-1", ToNodeID: "node-2", Type: "public-endpoint", EndpointHost: "203.0.113.2", EndpointPort: 0, Transport: "udp", IsEnabled: true},
		},
	}
}

func TestDerivePeers_UnidirectionalKeepalive(t *testing.T) {
	topo := unidirectionalPublicEndpointTopo()
	topo.Nodes[0].OverlayIP = "10.30.0.1"
	topo.Nodes[1].OverlayIP = "10.30.0.2"

	keys := testKeys()
	peerMap, _ := DerivePeers(topo, keys)

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
	topo.Nodes[0].OverlayIP = "10.11.0.1"
	topo.Nodes[1].OverlayIP = "10.11.0.2"
	topo.Nodes[2].OverlayIP = "10.11.0.3"

	keys := testKeys()
	peerMap, _ := DerivePeers(topo, keys)

	// 双向 edge + 都有公网IP 的情况下，不需要 keepalive
	for _, p := range peerMap["node-1"] {
		if p.PersistentKeepalive != 0 {
			t.Errorf("双向 edge 场景: node-1→%s 不应该有 PersistentKeepalive (期望 0，实际 %d)",
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
	peerMap, _ := DerivePeers(topo, keys)

	// 验证 node-1 的第一个 peer 的 per-peer 字段
	node1Peers := peerMap["node-1"]
	if len(node1Peers) != 2 {
		t.Fatalf("node-1 应有 2 个 peer，实际 %d", len(node1Peers))
	}

	for _, p := range node1Peers {
		// InterfaceName 格式：wg-<peername>
		if p.InterfaceName == "" {
			t.Errorf("peer %s 的 InterfaceName 不应为空", p.NodeID)
		}
		if len(p.InterfaceName) > 15 {
			t.Errorf("peer %s 的 InterfaceName 超过 15 字符: %s", p.NodeID, p.InterfaceName)
		}

		// ListenPort 应有值且从 basePort 递增
		if p.ListenPort == 0 {
			t.Errorf("peer %s 的 ListenPort 不应为 0", p.NodeID)
		}

		// Transit IP 应有值
		if p.LocalTransitIP == "" {
			t.Errorf("peer %s 的 LocalTransitIP 不应为空", p.NodeID)
		}
		if p.RemoteTransitIP == "" {
			t.Errorf("peer %s 的 RemoteTransitIP 不应为空", p.NodeID)
		}

		// Link-local 应有值
		if p.LocalLinkLocal == "" {
			t.Errorf("peer %s 的 LocalLinkLocal 不应为空", p.NodeID)
		}
		if p.RemoteLinkLocal == "" {
			t.Errorf("peer %s 的 RemoteLinkLocal 不应为空", p.NodeID)
		}
	}

	// 验证两个 peer 的 ListenPort 不同（递增分配）
	if node1Peers[0].ListenPort == node1Peers[1].ListenPort {
		t.Errorf("同一节点的两个 peer 接口 ListenPort 应不同，实际都为 %d", node1Peers[0].ListenPort)
	}

	// 验证 transit IP 互补：node-1 到 node-2 的 local 应等于 node-2 到 node-1 的 remote
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
			t.Errorf("transit IP 不互补: n1→n2 local=%s, n2→n1 remote=%s",
				n1ToN2.LocalTransitIP, n2ToN1.RemoteTransitIP)
		}
		if n1ToN2.RemoteTransitIP != n2ToN1.LocalTransitIP {
			t.Errorf("transit IP 不互补: n1→n2 remote=%s, n2→n1 local=%s",
				n1ToN2.RemoteTransitIP, n2ToN1.LocalTransitIP)
		}
	}
}

func TestGenerateRouterID(t *testing.T) {
	// 验证 MAC-48 格式
	rid := GenerateRouterID("node-1")
	if len(rid) != 17 { // xx:xx:xx:xx:xx:xx = 17 chars
		t.Errorf("RouterID 长度应为 17，实际 %d: %s", len(rid), rid)
	}

	// 验证格式包含 5 个冒号
	colonCount := 0
	for _, c := range rid {
		if c == ':' {
			colonCount++
		}
	}
	if colonCount != 5 {
		t.Errorf("RouterID 应包含 5 个冒号，实际 %d: %s", colonCount, rid)
	}

	// 验证稳定性（同输入 → 同输出）
	rid2 := GenerateRouterID("node-1")
	if rid != rid2 {
		t.Errorf("RouterID 不稳定: 第一次=%s, 第二次=%s", rid, rid2)
	}

	// 验证不同输入 → 不同输出
	rid3 := GenerateRouterID("node-2")
	if rid == rid3 {
		t.Errorf("不同节点的 RouterID 应不同: node-1=%s, node-2=%s", rid, rid3)
	}
}

func TestWgInterfaceName(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"beta", "wg-beta"},
		{"Alpha", "wg-alpha"},          // 大写转小写
		{"my_server", "wg-my-server"},   // 下划线转连字符
		{"a.b.c", "wg-a-b-c"},          // 点转连字符
		{"abcdefghijklmnop", "wg-abcdefghijkl"}, // 超过15字符截断
	}

	for _, tt := range tests {
		got := wgInterfaceName(tt.input)
		if got != tt.expected {
			t.Errorf("wgInterfaceName(%q) = %q, 期望 %q", tt.input, got, tt.expected)
		}
		if len(got) > 15 {
			t.Errorf("wgInterfaceName(%q) = %q 超过 15 字符", tt.input, got)
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

// extractPortFromEndpoint 从 "host:port" 或 "[ipv6]:port" 中提取端口号
func extractPortFromEndpoint(endpoint string) int {
	if endpoint == "" {
		return 0
	}
	// 从末尾找最后一个冒号
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

// TestDerivePeers_PortEndpointSymmetry 验证核心不变量：
// 对于每对 (A, B)，A 用来连 B 的 endpoint 端口 == B 为 A 分配的接口 ListenPort
func TestDerivePeers_PortEndpointSymmetry(t *testing.T) {
	topo := simpleMeshTopo()
	topo.Nodes[0].OverlayIP = "10.11.0.1"
	topo.Nodes[1].OverlayIP = "10.11.0.2"
	topo.Nodes[2].OverlayIP = "10.11.0.3"

	keys := testKeys()
	peerMap, _ := DerivePeers(topo, keys)

	// 对每个节点的每个 peer，验证端口对称性
	for nodeID, peers := range peerMap {
		for _, p := range peers {
			if p.Endpoint == "" {
				continue // 没有 endpoint 的 peer（如反向自动生成的无 endpoint peer）跳过
			}

			endpointPort := extractPortFromEndpoint(p.Endpoint)

			// 在对端节点的 peer 列表中找到指向当前节点的条目
			remotePeers := peerMap[p.NodeID]
			found := false
			for _, rp := range remotePeers {
				if rp.NodeID == nodeID {
					found = true
					if endpointPort != rp.ListenPort {
						t.Errorf("端口对称性违反: %s->%s endpoint 端口=%d, 但 %s 为 %s 分配的 ListenPort=%d",
							nodeID, p.NodeID, endpointPort, p.NodeID, nodeID, rp.ListenPort)
					}
					break
				}
			}
			if !found {
				t.Errorf("对称性检查失败: %s 有 peer %s, 但 %s 没有反向 peer %s",
					nodeID, p.NodeID, p.NodeID, nodeID)
			}
		}
	}
}

// TestDerivePeers_MultiPeerPortIncrement 验证 hub 节点有多个 peer 时端口正确递增
func TestDerivePeers_MultiPeerPortIncrement(t *testing.T) {
	topo := natHubTopo()
	topo.Nodes[0].OverlayIP = "10.20.0.1"
	topo.Nodes[1].OverlayIP = "10.20.0.2"
	topo.Nodes[2].OverlayIP = "10.20.0.3"

	keys := testKeys()
	peerMap, _ := DerivePeers(topo, keys)

	// Hub (node-1) 应该有 2 个 peer 接口，端口递增
	hubPeers := peerMap["node-1"]
	if len(hubPeers) != 2 {
		t.Fatalf("Hub 应有 2 个 peer，实际 %d", len(hubPeers))
	}

	// Hub 的两个接口端口应该不同且从 base 递增
	if hubPeers[0].ListenPort == hubPeers[1].ListenPort {
		t.Errorf("Hub 的两个接口端口不应相同，都为 %d", hubPeers[0].ListenPort)
	}

	// 验证每个 client 的 endpoint 端口 == hub 为该 client 分配的 ListenPort
	for _, clientID := range []string{"node-2", "node-3"} {
		clientPeers := peerMap[clientID]
		if len(clientPeers) != 1 {
			t.Fatalf("Client %s 应有 1 个 peer，实际 %d", clientID, len(clientPeers))
		}

		cp := clientPeers[0]
		if cp.Endpoint == "" {
			t.Errorf("Client %s 的 endpoint 不应为空", clientID)
			continue
		}

		endpointPort := extractPortFromEndpoint(cp.Endpoint)

		// 找到 hub 中对应该 client 的 peer
		for _, hp := range hubPeers {
			if hp.NodeID == clientID {
				if endpointPort != hp.ListenPort {
					t.Errorf("Client %s endpoint 端口=%d, 但 Hub 为 %s 分配的 ListenPort=%d",
						clientID, endpointPort, clientID, hp.ListenPort)
				}
				break
			}
		}
	}
}
