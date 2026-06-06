package compiler

import (
	"testing"

	"github.com/kunorikiku/yet-another-overlay-generator/internal/model"
)

// countOccurrences 统计 cidr 在切片中出现的次数，用于断言「恰好出现一次」。
func countOccurrences(slice []string, target string) int {
	n := 0
	for _, v := range slice {
		if v == target {
			n++
		}
	}
	return n
}

// TestClientAllowedIPs_MultiDomainUnion 覆盖 D30（Decision 6）：
// client 的 wg0 是它通往整个 overlay 的唯一隧道，因此 DomainCIDRs 必须是
// 「所有域 CIDR」与「每个域解析后的 transit CIDR」的并集，且各前缀恰好出现一次。
//
// 拓扑：两个域（10.11.0.0/24 与 10.12.0.0/24），其中域 B 自定义 transit
// （10.20.0.0/24），域 A 留空 transit（解析为默认 10.10.0.0/24）。
// client 位于域 A，连向同域的一个 router。
func TestClientAllowedIPs_MultiDomainUnion(t *testing.T) {
	topo := &model.Topology{
		Project: model.Project{ID: "cl-multi", Name: "Client Multi-Domain"},
		Domains: []model.Domain{
			{
				ID: "domain-a", Name: "alpha-net", CIDR: "10.11.0.0/24",
				AllocationMode: "auto", RoutingMode: "babel",
				// transit 留空 → 解析为默认 10.10.0.0/24
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
			// 域 B 中放一个 router，确保该域（含其自定义 transit）参与并集，
			// 即便 client 不直接连它。
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
			// router-a <-> router-b：跨域骨干，使两域都「在用」。
			{ID: "e-backbone", FromNodeID: "router-a", ToNodeID: "router-b", Type: "public-endpoint",
				EndpointHost: "router-b.example", Transport: "udp", IsEnabled: true},
			// client-a -> router-a：client 的唯一出站边（必须带 endpoint_host）。
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
	result, err := c.Compile(topo, keys)
	if err != nil {
		t.Fatalf("Compile 失败: %v", err)
	}

	clientCfg := result.ClientConfigs["client-a"]
	if clientCfg == nil {
		t.Fatalf("应为 client-a 生成 ClientPeerInfo")
	}

	// 两个域 CIDR 各恰好一次。
	for _, cidr := range []string{"10.11.0.0/24", "10.12.0.0/24"} {
		if got := countOccurrences(clientCfg.DomainCIDRs, cidr); got != 1 {
			t.Errorf("DomainCIDRs 应恰好包含一次域 CIDR %s，实际 %d 次（DomainCIDRs=%v）",
				cidr, got, clientCfg.DomainCIDRs)
		}
	}

	// 两个 transit CIDR 各恰好一次：域 A 解析为默认 10.10.0.0/24，域 B 自定义 10.20.0.0/24。
	for _, cidr := range []string{"10.10.0.0/24", "10.20.0.0/24"} {
		if got := countOccurrences(clientCfg.DomainCIDRs, cidr); got != 1 {
			t.Errorf("DomainCIDRs 应恰好包含一次 transit CIDR %s，实际 %d 次（DomainCIDRs=%v）",
				cidr, got, clientCfg.DomainCIDRs)
		}
	}

	// 并集恰好是 4 个前缀（2 域 + 2 transit），无多余、无重复。
	if len(clientCfg.DomainCIDRs) != 4 {
		t.Errorf("DomainCIDRs 应恰好为 4 个前缀（2 域 + 2 transit），实际 %d 个：%v",
			len(clientCfg.DomainCIDRs), clientCfg.DomainCIDRs)
	}
}

// TestClientAllowedIPs_SingleDomain 验证单域场景仍然可用：
// DomainCIDRs == [域 CIDR, 默认 transit CIDR]，各恰好一次。
func TestClientAllowedIPs_SingleDomain(t *testing.T) {
	topo := &model.Topology{
		Project: model.Project{ID: "cl-single", Name: "Client Single-Domain"},
		Domains: []model.Domain{
			{
				ID: "domain-a", Name: "alpha-net", CIDR: "10.13.0.0/24",
				AllocationMode: "auto", RoutingMode: "babel",
				// transit 留空 → 默认 10.10.0.0/24
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
	result, err := c.Compile(topo, keys)
	if err != nil {
		t.Fatalf("Compile 失败: %v", err)
	}

	clientCfg := result.ClientConfigs["client-a"]
	if clientCfg == nil {
		t.Fatalf("应为 client-a 生成 ClientPeerInfo")
	}

	if got := countOccurrences(clientCfg.DomainCIDRs, "10.13.0.0/24"); got != 1 {
		t.Errorf("单域：DomainCIDRs 应恰好包含一次域 CIDR 10.13.0.0/24，实际 %d 次（%v）",
			got, clientCfg.DomainCIDRs)
	}
	if got := countOccurrences(clientCfg.DomainCIDRs, "10.10.0.0/24"); got != 1 {
		t.Errorf("单域：DomainCIDRs 应恰好包含一次默认 transit CIDR 10.10.0.0/24，实际 %d 次（%v）",
			got, clientCfg.DomainCIDRs)
	}
	if len(clientCfg.DomainCIDRs) != 2 {
		t.Errorf("单域：DomainCIDRs 应恰好为 2 个前缀（域 + 默认 transit），实际 %d 个：%v",
			len(clientCfg.DomainCIDRs), clientCfg.DomainCIDRs)
	}
}

// TestInferCapabilitiesFromRole_PublicRouterAcceptsInbound 覆盖 D49：
// 具备 HasPublicIP 的 router 经能力推导后应得到 CanAcceptInbound=true，
// 与 DeriveRoleSemantics 的 AcceptAllInbound 保持一致。
// 同时验证：不具备公网 IP 的 router 不会被推导为接受入站。
func TestInferCapabilitiesFromRole_PublicRouterAcceptsInbound(t *testing.T) {
	publicRouter := &model.Node{
		ID: "r1", Role: "router",
		Capabilities: model.NodeCapabilities{HasPublicIP: true},
	}
	caps := InferCapabilitiesFromRole(publicRouter)
	if !caps.CanAcceptInbound {
		t.Errorf("具备公网 IP 的 router 推导后应 CanAcceptInbound=true，实际 false")
	}

	privateRouter := &model.Node{
		ID: "r2", Role: "router",
		Capabilities: model.NodeCapabilities{HasPublicIP: false},
	}
	if caps := InferCapabilitiesFromRole(privateRouter); caps.CanAcceptInbound {
		t.Errorf("不具备公网 IP 的 router 推导后不应 CanAcceptInbound=true")
	}

	// gateway 同样的语义。
	publicGateway := &model.Node{
		ID: "g1", Role: "gateway",
		Capabilities: model.NodeCapabilities{HasPublicIP: true},
	}
	if caps := InferCapabilitiesFromRole(publicGateway); !caps.CanAcceptInbound {
		t.Errorf("具备公网 IP 的 gateway 推导后应 CanAcceptInbound=true，实际 false")
	}
}

// TestPeerInfo_LinkCostFromEdgePriority 覆盖 D63：
// edge.Priority（>0）应映射到正向与反向 PeerInfo 的 LinkCost。
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
		t.Fatalf("DerivePeers 失败: %v", err)
	}

	fwd := findPeer(peerMap["node-a"], "node-b")
	if fwd == nil {
		t.Fatalf("node-a 应有指向 node-b 的正向 peer")
	}
	if fwd.LinkCost != wantCost {
		t.Errorf("正向 LinkCost = %d, 期望 %d（来自 edge.Priority）", fwd.LinkCost, wantCost)
	}

	rev := findPeer(peerMap["node-b"], "node-a")
	if rev == nil {
		t.Fatalf("node-b 应有指向 node-a 的反向 peer")
	}
	if rev.LinkCost != wantCost {
		t.Errorf("反向 LinkCost = %d, 期望 %d（反向 peer 共用同一 edge）", rev.LinkCost, wantCost)
	}
}

// TestPeerInfo_LinkCostFallback 验证 D63 的回退顺序：
//   - 无 Priority 但有 Weight → LinkCost = Weight；
//   - 两者皆无 → LinkCost = 0（交由角色 preset 默认）。
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
			t.Fatalf("DerivePeers 失败: %v", err)
		}
		fwd := findPeer(peerMap["node-a"], "node-b")
		if fwd == nil {
			t.Fatalf("node-a 应有指向 node-b 的 peer")
		}
		if fwd.LinkCost != wantWeight {
			t.Errorf("LinkCost = %d, 期望 %d（回退到 edge.Weight）", fwd.LinkCost, wantWeight)
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
			t.Fatalf("DerivePeers 失败: %v", err)
		}
		fwd := findPeer(peerMap["node-a"], "node-b")
		if fwd == nil {
			t.Fatalf("node-a 应有指向 node-b 的 peer")
		}
		if fwd.LinkCost != 0 {
			t.Errorf("LinkCost = %d, 期望 0（未设置 priority/weight）", fwd.LinkCost)
		}
	})
}
