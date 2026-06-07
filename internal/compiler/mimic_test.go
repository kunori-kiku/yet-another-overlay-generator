package compiler

import (
	"testing"

	"github.com/kunorikiku/yet-another-overlay-generator/internal/model"
)

// mimicTwoRouterTopo 构造一个最小的双 router 拓扑，两节点之间一条单向 edge，
// transport 与两端节点 MTU 由参数指定。用于覆盖 mimic（tcp 传输）下 PeerInfo 的
// Mimic / MTU 推导（docs/spec/artifacts/mimic.md「MTU −12」、契约 item 1）。
//
// 两端都声明为可部署 Linux（debian / ubuntu）并公网可达，使 tcp 边能通过验证器的
// mimic 平台约束，并产生双向 peer（正向 + 自动反向）。
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
				Role: "router", DomainID: "domain-1", ListenPort: 51820, MTU: fromMTU,
				Capabilities: model.NodeCapabilities{CanAcceptInbound: true, CanForward: true, HasPublicIP: true},
				PublicEndpoints: []model.PublicEndpoint{
					{ID: "node-a-ep", Host: "a.example", Port: 51820},
				},
			},
			{
				ID: "node-b", Name: "beta", Hostname: "b.example", Platform: "ubuntu",
				Role: "router", DomainID: "domain-1", ListenPort: 51820, MTU: toMTU,
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

// TestDerivePeers_MimicTcpEdge_FlagsAndMTU 覆盖契约 item 1 的核心：
// 一条 tcp 边 → 该链路两端的 PeerInfo 都有 Mimic==true，且 MTU == (effective)−12，
// 其中 effective = node.MTU>0 ? node.MTU : 1420。两端各按本端节点 MTU 推导。
//
//	node.MTU==0    ⇒ 1420 − 12 = 1408
//	node.MTU==1500 ⇒ 1500 − 12 = 1488
func TestDerivePeers_MimicTcpEdge_FlagsAndMTU(t *testing.T) {
	cases := []struct {
		name    string
		fromMTU int
		toMTU   int
		wantA   int // node-a 接口的期望 MTU
		wantB   int // node-b 接口的期望 MTU
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
				t.Fatalf("DerivePeers 失败: %v", err)
			}

			// node-a 上指向 node-b 的接口（本端 = node-a）。
			aToB := findPeer(peerMap["node-a"], "node-b")
			if aToB == nil {
				t.Fatalf("node-a 应有指向 node-b 的 peer")
			}
			if !aToB.Mimic {
				t.Errorf("tcp 边：node-a→node-b 应 Mimic==true，实际 false")
			}
			if aToB.MTU != tc.wantA {
				t.Errorf("node-a→node-b MTU = %d，期望 %d（本端 node.MTU=%d − 12）", aToB.MTU, tc.wantA, tc.fromMTU)
			}

			// node-b 上指向 node-a 的（自动反向）接口（本端 = node-b）。
			bToA := findPeer(peerMap["node-b"], "node-a")
			if bToA == nil {
				t.Fatalf("node-b 应有指向 node-a 的反向 peer")
			}
			if !bToA.Mimic {
				t.Errorf("tcp 边：node-b→node-a 应 Mimic==true，实际 false")
			}
			if bToA.MTU != tc.wantB {
				t.Errorf("node-b→node-a MTU = %d，期望 %d（本端 node.MTU=%d − 12）", bToA.MTU, tc.wantB, tc.toMTU)
			}
		})
	}
}

// TestDerivePeers_UdpEdge_NoMimicNoMTUChange 覆盖契约 item 1 的反面：
// 一条 udp 边 → Mimic==false，且 MTU == node.MTU 原样（此处 node.MTU==0 ⇒ 0，
// 渲染器据此省略 MTU 行）。这是 mimic 改造前的逐字节行为。
func TestDerivePeers_UdpEdge_NoMimicNoMTUChange(t *testing.T) {
	topo := mimicTwoRouterTopo("udp", 0, 0)
	topo.Nodes[0].OverlayIP = "10.50.0.1"
	topo.Nodes[1].OverlayIP = "10.50.0.2"

	peerMap, _, err := DerivePeers(topo, testKeys2())
	if err != nil {
		t.Fatalf("DerivePeers 失败: %v", err)
	}

	for _, dir := range []struct {
		node, remote string
	}{
		{"node-a", "node-b"},
		{"node-b", "node-a"},
	} {
		p := findPeer(peerMap[dir.node], dir.remote)
		if p == nil {
			t.Fatalf("%s 应有指向 %s 的 peer", dir.node, dir.remote)
		}
		if p.Mimic {
			t.Errorf("udp 边：%s→%s 应 Mimic==false，实际 true", dir.node, dir.remote)
		}
		if p.MTU != 0 {
			t.Errorf("udp 边且 node.MTU==0：%s→%s MTU 应保持 0（不降），实际 %d", dir.node, dir.remote, p.MTU)
		}
	}
}

// TestDerivePeers_UdpEdge_ExplicitMTUUnchanged 验证 udp 边不动 node.MTU：
// 显式 MTU 1500 的 udp 边，peer.MTU 必须仍是 1500（绝不扣 12）。这与 mimic 链路的
// −12 行为形成对照，确保非 mimic 拓扑的 MTU 完全不受 mimic 逻辑影响。
func TestDerivePeers_UdpEdge_ExplicitMTUUnchanged(t *testing.T) {
	topo := mimicTwoRouterTopo("udp", 1500, 1500)
	topo.Nodes[0].OverlayIP = "10.50.0.1"
	topo.Nodes[1].OverlayIP = "10.50.0.2"

	peerMap, _, err := DerivePeers(topo, testKeys2())
	if err != nil {
		t.Fatalf("DerivePeers 失败: %v", err)
	}

	for _, dir := range []struct {
		node, remote string
	}{
		{"node-a", "node-b"},
		{"node-b", "node-a"},
	} {
		p := findPeer(peerMap[dir.node], dir.remote)
		if p == nil {
			t.Fatalf("%s 应有指向 %s 的 peer", dir.node, dir.remote)
		}
		if p.Mimic {
			t.Errorf("udp 边：%s→%s 应 Mimic==false，实际 true", dir.node, dir.remote)
		}
		if p.MTU != 1500 {
			t.Errorf("udp 边：%s→%s MTU 应保持 1500（绝不扣 12），实际 %d", dir.node, dir.remote, p.MTU)
		}
	}
}

// TestDerivePeers_EmptyTransportTreatedAsUdp 验证空 transport（缺省）不被当作 mimic：
// DerivePeers 不做归一，空 transport 直接判定为非 tcp ⇒ Mimic==false、MTU 不降。
// 这保证既有未设置 transport 的拓扑不会意外启用 mimic。
func TestDerivePeers_EmptyTransportTreatedAsUdp(t *testing.T) {
	topo := mimicTwoRouterTopo("", 1500, 1500)
	topo.Nodes[0].OverlayIP = "10.50.0.1"
	topo.Nodes[1].OverlayIP = "10.50.0.2"

	peerMap, _, err := DerivePeers(topo, testKeys2())
	if err != nil {
		t.Fatalf("DerivePeers 失败: %v", err)
	}

	p := findPeer(peerMap["node-a"], "node-b")
	if p == nil {
		t.Fatalf("node-a 应有指向 node-b 的 peer")
	}
	if p.Mimic {
		t.Errorf("空 transport 不应被当作 mimic，实际 Mimic==true")
	}
	if p.MTU != 1500 {
		t.Errorf("空 transport：MTU 应保持 1500（node.MTU 原样），实际 %d", p.MTU)
	}
}

// mimicClientTopo 构造一个 client → router 的拓扑，client 出站 edge 的 transport
// 由参数指定。两节点均为可部署 Linux 以满足 tcp 边的平台约束。用于覆盖
// ClientPeerInfo.Mimic / MTU（契约 item 1 末句）。
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
				Role: "router", DomainID: "domain-1", ListenPort: 51820,
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

// TestDeriveClientConfigs_MimicTcpEdge 覆盖 ClientPeerInfo 在 client tcp 边下的推导：
// client 的单 wg0 链路若为 tcp，则 ClientPeerInfo.Mimic==true 且 MTU == effective−12
// （node.MTU==0 ⇒ 1408，node.MTU==1500 ⇒ 1488）。对照 udp：Mimic==false、MTU 原样。
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
			result, err := c.Compile(topo, clientKeys())
			if err != nil {
				t.Fatalf("Compile 失败: %v", err)
			}

			cfg := result.ClientConfigs["client-a"]
			if cfg == nil {
				t.Fatalf("应为 client-a 生成 ClientPeerInfo")
			}
			if cfg.Mimic != tc.wantMimic {
				t.Errorf("ClientPeerInfo.Mimic = %v，期望 %v", cfg.Mimic, tc.wantMimic)
			}
			if cfg.MTU != tc.wantMTU {
				t.Errorf("ClientPeerInfo.MTU = %d，期望 %d", cfg.MTU, tc.wantMTU)
			}
		})
	}
}

// TestEffectiveMTU_PureFunction 直接覆盖 MTU 公式（契约 item 1 的算术核心），
// 与上面的拓扑级断言互补：非 mimic 时原样透传（含 0），mimic 时 (base?:1420)−12。
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
			t.Errorf("effectiveMTU(%d, %v) = %d，期望 %d", tc.nodeMTU, tc.mimic, got, tc.want)
		}
	}
}
