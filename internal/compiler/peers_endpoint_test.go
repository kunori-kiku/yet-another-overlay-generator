package compiler

import (
	"testing"

	"github.com/kunorikiku/yet-another-overlay-generator/internal/model"
)

// publicRouterNode 构建一个公网可达的 router 节点（带一个 public endpoint）。
// HasPublicIP 直接置位，使 DerivePeers 的反向回退逻辑生效（DerivePeers 不会自行推导 capabilities）。
func publicRouterNode(id, name, host string) model.Node {
	return model.Node{
		ID:       id,
		Name:     name,
		Hostname: host,
		Role:     "router",
		DomainID: "domain-1",
		Capabilities: model.NodeCapabilities{
			CanAcceptInbound: true,
			CanForward:       true,
			HasPublicIP:      true,
		},
		PublicEndpoints: []model.PublicEndpoint{
			// Port 是节点可达提示，而非链路监听端口；反向回退绝不应使用它。
			{ID: id + "-ep", Host: host, Port: 51820},
		},
	}
}

// findPeer 在 peers 中找到指向 remoteID 的 PeerInfo。
func findPeer(peers []PeerInfo, remoteID string) *PeerInfo {
	for i := range peers {
		if peers[i].NodeID == remoteID {
			return &peers[i]
		}
	}
	return nil
}

// findEdge 在 edges 中按 id 找到 Edge。
func findEdge(edges []model.Edge, id string) *model.Edge {
	for i := range edges {
		if edges[i].ID == id {
			return &edges[i]
		}
	}
	return nil
}

// TestEndpointResolution_Forward 覆盖正向 endpoint 解析矩阵（Spec A）。
// 表驱动：每个用例描述一条 from->to 的 edge，断言 from 侧拨号的 endpoint。
func TestEndpointResolution_Forward(t *testing.T) {
	tests := []struct {
		name         string
		endpointHost string
		endpointPort int
		// wantEndpoint 为空字符串表示不应生成 Endpoint 行
		wantEndpoint string
		// wantPort 为 0 表示不校验端口（仅当 wantEndpoint 非空时校验）
		wantPort int
	}{
		{
			// (a) 仅 endpoint_host：from 侧拨对端已分配的监听端口（51820）
			name:         "endpoint_host only dials allocated port",
			endpointHost: "b.example",
			endpointPort: 0,
			wantEndpoint: "b.example:51820",
			wantPort:     51820,
		},
		{
			// (b) 显式 endpoint_port 覆盖：逐字拨号
			name:         "explicit endpoint_port override dialed verbatim",
			endpointHost: "b.example",
			endpointPort: 51900,
			wantEndpoint: "b.example:51900",
			wantPort:     51900,
		},
		{
			// (d) endpoint_host 为空：不生成 Endpoint 行
			name:         "empty endpoint_host produces no Endpoint",
			endpointHost: "",
			endpointPort: 0,
			wantEndpoint: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			topo := &model.Topology{
				Project: model.Project{ID: "ep-fwd", Name: "Endpoint Forward"},
				Domains: []model.Domain{{
					ID: "domain-1", Name: "fwd-net", CIDR: "10.40.0.0/24",
					AllocationMode: "auto", RoutingMode: "babel",
				}},
				Nodes: []model.Node{
					publicRouterNode("node-a", "alpha", "a.example"),
					publicRouterNode("node-b", "beta", "b.example"),
				},
				Edges: []model.Edge{
					{ID: "e1", FromNodeID: "node-a", ToNodeID: "node-b", Type: "public-endpoint",
						EndpointHost: tt.endpointHost, EndpointPort: tt.endpointPort, Transport: "udp", IsEnabled: true},
				},
			}
			topo.Nodes[0].OverlayIP = "10.40.0.1"
			topo.Nodes[1].OverlayIP = "10.40.0.2"

			peerMap, _, err := DerivePeers(topo, testKeys2())
			if err != nil {
				t.Fatalf("DerivePeers 失败: %v", err)
			}

			fwd := findPeer(peerMap["node-a"], "node-b")
			if fwd == nil {
				t.Fatalf("node-a 应有指向 node-b 的 peer")
			}
			if fwd.Endpoint != tt.wantEndpoint {
				t.Errorf("正向 endpoint = %q, 期望 %q", fwd.Endpoint, tt.wantEndpoint)
			}
			if tt.wantEndpoint != "" && tt.wantPort != 0 {
				if got := extractPortFromEndpoint(fwd.Endpoint); got != tt.wantPort {
					t.Errorf("正向拨号端口 = %d, 期望 %d", got, tt.wantPort)
				}
			}
		})
	}
}

// TestEndpointResolution_ReverseFallback 覆盖反向 peer 的 endpoint 回退矩阵（Spec A）。
// (a) 单条 A->B edge + 两端公网可达：B 的反向 peer 应回退拨 A 的 public host + A 侧已分配端口。
// (e) 显式反向 edge（自带 endpoint_host）优先于回退。
func TestEndpointResolution_ReverseFallback(t *testing.T) {
	tests := []struct {
		name string
		// reverseEdge 为 true 时额外添加一条 B->A 的显式反向 edge
		reverseEdge     bool
		reverseHost     string
		fromHasPublicIP bool
		// wantReverseEndpoint 为 B 反向拨 A 时期望的 endpoint
		wantReverseEndpoint string
	}{
		{
			// (a) 无反向 edge，A 公网可达 → 回退拨 A 的 public host + A 侧端口（51820），不使用 public endpoint 的 Port
			name:                "fallback dials from-node public host at allocated port",
			reverseEdge:         false,
			fromHasPublicIP:     true,
			wantReverseEndpoint: "a.example:51820",
		},
		{
			// (e) 显式反向 edge 自带 host，优先于回退；A 仍声明了 public endpoint 以证明显式优先
			name:                "explicit reverse edge wins over fallback",
			reverseEdge:         true,
			reverseHost:         "a-nat.example",
			fromHasPublicIP:     true,
			wantReverseEndpoint: "a-nat.example:51820",
		},
		{
			// 无反向 edge 且 A 不公网可达 → 反向 peer 无 endpoint
			name:                "no reverse edge and no public IP produces no Endpoint",
			reverseEdge:         false,
			fromHasPublicIP:     false,
			wantReverseEndpoint: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			nodeA := publicRouterNode("node-a", "alpha", "a.example")
			if !tt.fromHasPublicIP {
				nodeA.Capabilities.HasPublicIP = false
				nodeA.PublicEndpoints = nil
			}
			nodeB := publicRouterNode("node-b", "beta", "b.example")

			edges := []model.Edge{
				{ID: "e1", FromNodeID: "node-a", ToNodeID: "node-b", Type: "public-endpoint",
					EndpointHost: "b.example", EndpointPort: 0, Transport: "udp", IsEnabled: true},
			}
			if tt.reverseEdge {
				edges = append(edges, model.Edge{
					ID: "e2", FromNodeID: "node-b", ToNodeID: "node-a", Type: "public-endpoint",
					EndpointHost: tt.reverseHost, EndpointPort: 0, Transport: "udp", IsEnabled: true,
				})
			}

			topo := &model.Topology{
				Project: model.Project{ID: "ep-rev", Name: "Endpoint Reverse"},
				Domains: []model.Domain{{
					ID: "domain-1", Name: "rev-net", CIDR: "10.41.0.0/24",
					AllocationMode: "auto", RoutingMode: "babel",
				}},
				Nodes: []model.Node{nodeA, nodeB},
				Edges: edges,
			}
			topo.Nodes[0].OverlayIP = "10.41.0.1"
			topo.Nodes[1].OverlayIP = "10.41.0.2"

			peerMap, _, err := DerivePeers(topo, testKeys2())
			if err != nil {
				t.Fatalf("DerivePeers 失败: %v", err)
			}

			// B 反向拨 A 的 peer：在 peerMap["node-b"] 中 NodeID == node-a
			rev := findPeer(peerMap["node-b"], "node-a")
			if rev == nil {
				t.Fatalf("node-b 应有指向 node-a 的反向 peer")
			}
			if rev.Endpoint != tt.wantReverseEndpoint {
				t.Errorf("反向 endpoint = %q, 期望 %q", rev.Endpoint, tt.wantReverseEndpoint)
			}

			// 关键不变量：回退绝不能使用 public_endpoints[0].Port（此处也是 51820，但应来自 A 侧已分配端口）。
			// 用正向端口对称性间接验证：A 反向被拨的端口 == A 自身接口的 ListenPort。
			if tt.wantReverseEndpoint != "" {
				aPeer := findPeer(peerMap["node-a"], "node-b")
				if aPeer == nil {
					t.Fatalf("node-a 应有指向 node-b 的 peer")
				}
				dialedPort := extractPortFromEndpoint(rev.Endpoint)
				if dialedPort != aPeer.ListenPort {
					t.Errorf("反向拨 A 的端口 = %d, 应等于 A 接口的 ListenPort = %d", dialedPort, aPeer.ListenPort)
				}
			}
		})
	}
}

// TestEndpointResolution_SymmetricSingleEdge 验证 (a) 的端到端对称性：
// 一条 A->B edge 即产生双向可拨的隧道——A 拨 B 的已分配端口，B 回退拨 A 的已分配端口。
func TestEndpointResolution_SymmetricSingleEdge(t *testing.T) {
	topo := &model.Topology{
		Project: model.Project{ID: "ep-sym", Name: "Endpoint Symmetric"},
		Domains: []model.Domain{{
			ID: "domain-1", Name: "sym-net", CIDR: "10.42.0.0/24",
			AllocationMode: "auto", RoutingMode: "babel",
		}},
		Nodes: []model.Node{
			publicRouterNode("node-a", "alpha", "a.example"),
			publicRouterNode("node-b", "beta", "b.example"),
		},
		Edges: []model.Edge{
			{ID: "e1", FromNodeID: "node-a", ToNodeID: "node-b", Type: "public-endpoint",
				EndpointHost: "b.example", EndpointPort: 0, Transport: "udp", IsEnabled: true},
		},
	}
	topo.Nodes[0].OverlayIP = "10.42.0.1"
	topo.Nodes[1].OverlayIP = "10.42.0.2"

	peerMap, _, err := DerivePeers(topo, testKeys2())
	if err != nil {
		t.Fatalf("DerivePeers 失败: %v", err)
	}

	aToB := findPeer(peerMap["node-a"], "node-b")
	bToA := findPeer(peerMap["node-b"], "node-a")
	if aToB == nil || bToA == nil {
		t.Fatalf("应有双向 peer: aToB=%v bToA=%v", aToB, bToA)
	}

	// A 拨 B 的 endpoint 端口 == B 接口的 ListenPort
	if got := extractPortFromEndpoint(aToB.Endpoint); got != bToA.ListenPort {
		t.Errorf("A 拨 B 的端口 = %d, 应等于 B 接口 ListenPort = %d", got, bToA.ListenPort)
	}
	// B 回退拨 A 的 endpoint 端口 == A 接口的 ListenPort
	if got := extractPortFromEndpoint(bToA.Endpoint); got != aToB.ListenPort {
		t.Errorf("B 拨 A 的端口 = %d, 应等于 A 接口 ListenPort = %d", got, aToB.ListenPort)
	}
	// 双向都应拨各自的 public host
	if aToB.Endpoint != "b.example:"+itoa(bToA.ListenPort) {
		t.Errorf("A->B endpoint = %q, 期望 b.example:%d", aToB.Endpoint, bToA.ListenPort)
	}
	if bToA.Endpoint != "a.example:"+itoa(aToB.ListenPort) {
		t.Errorf("B->A endpoint = %q, 期望 a.example:%d", bToA.Endpoint, aToB.ListenPort)
	}
}

// TestEndpointResolution_HubDistinctPorts 覆盖 (c)：三条 edge 汇入同一 hub，
// 每条各拨 hub 上一个独立的已分配端口（base, base+1, base+2）。
func TestEndpointResolution_HubDistinctPorts(t *testing.T) {
	topo := &model.Topology{
		Project: model.Project{ID: "ep-hub", Name: "Endpoint Hub"},
		Domains: []model.Domain{{
			ID: "domain-1", Name: "hub-net", CIDR: "10.43.0.0/24",
			AllocationMode: "auto", RoutingMode: "babel",
		}},
		Nodes: []model.Node{
			publicRouterNode("node-h", "hub", "h.example"),
			publicRouterNode("node-a", "alpha", "a.example"),
			publicRouterNode("node-b", "beta", "b.example"),
			publicRouterNode("node-c", "gamma", "c.example"),
		},
		// 处理顺序决定 hub 侧端口分配：A 链路 51820，B 链路 51821，C 链路 51822
		Edges: []model.Edge{
			{ID: "e1", FromNodeID: "node-a", ToNodeID: "node-h", Type: "public-endpoint",
				EndpointHost: "h.example", EndpointPort: 0, Transport: "udp", IsEnabled: true},
			{ID: "e2", FromNodeID: "node-b", ToNodeID: "node-h", Type: "public-endpoint",
				EndpointHost: "h.example", EndpointPort: 0, Transport: "udp", IsEnabled: true},
			{ID: "e3", FromNodeID: "node-c", ToNodeID: "node-h", Type: "public-endpoint",
				EndpointHost: "h.example", EndpointPort: 0, Transport: "udp", IsEnabled: true},
		},
	}
	topo.Nodes[0].OverlayIP = "10.43.0.1"
	topo.Nodes[1].OverlayIP = "10.43.0.2"
	topo.Nodes[2].OverlayIP = "10.43.0.3"
	topo.Nodes[3].OverlayIP = "10.43.0.4"

	peerMap, _, err := DerivePeers(topo, testKeys4())
	if err != nil {
		t.Fatalf("DerivePeers 失败: %v", err)
	}

	wantPorts := map[string]int{
		"node-a": 51820,
		"node-b": 51821,
		"node-c": 51822,
	}

	seen := make(map[int]bool)
	for spoke, want := range wantPorts {
		p := findPeer(peerMap[spoke], "node-h")
		if p == nil {
			t.Fatalf("%s 应有指向 hub 的 peer", spoke)
		}
		got := extractPortFromEndpoint(p.Endpoint)
		if got != want {
			t.Errorf("%s 拨 hub 的端口 = %d, 期望 %d", spoke, got, want)
		}
		if p.Endpoint != "h.example:"+itoa(want) {
			t.Errorf("%s endpoint = %q, 期望 h.example:%d", spoke, p.Endpoint, want)
		}
		if seen[got] {
			t.Errorf("hub 侧端口 %d 被多条链路复用，应各不相同", got)
		}
		seen[got] = true
	}

	// hub 应有 3 个独立 peer 接口，端口各不相同
	hubPeers := peerMap["node-h"]
	if len(hubPeers) != 3 {
		t.Fatalf("hub 应有 3 个 peer 接口，实际 %d", len(hubPeers))
	}
	hubPorts := make(map[int]bool)
	for _, hp := range hubPeers {
		if hubPorts[hp.ListenPort] {
			t.Errorf("hub 接口 ListenPort %d 重复", hp.ListenPort)
		}
		hubPorts[hp.ListenPort] = true
	}
}

// TestCompiledPort_OverrideAware 覆盖 (b) 与 (d) 的 CompiledPort 写回（D51）：
// (b) 显式 endpoint_port=51900 → CompiledPort 等于 51900，且等于渲染 endpoint 中的端口；
// (d) endpoint_host 为空 → 不写回 CompiledPort（保持 0）。
func TestCompiledPort_OverrideAware(t *testing.T) {
	t.Run("override reflected in CompiledPort and Endpoint", func(t *testing.T) {
		topo := &model.Topology{
			Project: model.Project{ID: "cp-ovr", Name: "CompiledPort Override"},
			Domains: []model.Domain{{
				ID: "domain-1", Name: "ovr-net", CIDR: "10.44.0.0/24",
				AllocationMode: "auto", RoutingMode: "babel",
			}},
			Nodes: []model.Node{
				publicRouterNode("node-a", "alpha", "a.example"),
				publicRouterNode("node-b", "beta", "b.example"),
			},
			Edges: []model.Edge{
				{ID: "e1", FromNodeID: "node-a", ToNodeID: "node-b", Type: "public-endpoint",
					EndpointHost: "b.example", EndpointPort: 51900, Transport: "udp", IsEnabled: true},
			},
		}

		c := NewCompiler()
		result, err := c.Compile(topo, testKeys2())
		if err != nil {
			t.Fatalf("Compile 失败: %v", err)
		}

		edge := findEdge(result.Topology.Edges, "e1")
		if edge == nil {
			t.Fatalf("结果中应有 edge e1")
		}
		if edge.CompiledPort != 51900 {
			t.Errorf("CompiledPort = %d, 期望 51900（覆盖值）", edge.CompiledPort)
		}

		// CompiledPort 必须等于渲染 endpoint 中携带的端口
		fwd := findPeer(result.PeerMap["node-a"], "node-b")
		if fwd == nil {
			t.Fatalf("node-a 应有指向 node-b 的 peer")
		}
		if got := extractPortFromEndpoint(fwd.Endpoint); got != edge.CompiledPort {
			t.Errorf("渲染 endpoint 端口 = %d, CompiledPort = %d, 两者必须一致", got, edge.CompiledPort)
		}
	})

	t.Run("empty endpoint_host leaves no CompiledPort", func(t *testing.T) {
		topo := &model.Topology{
			Project: model.Project{ID: "cp-empty", Name: "CompiledPort Empty"},
			Domains: []model.Domain{{
				ID: "domain-1", Name: "empty-net", CIDR: "10.45.0.0/24",
				AllocationMode: "auto", RoutingMode: "babel",
			}},
			Nodes: []model.Node{
				publicRouterNode("node-a", "alpha", "a.example"),
				publicRouterNode("node-b", "beta", "b.example"),
			},
			Edges: []model.Edge{
				{ID: "e1", FromNodeID: "node-a", ToNodeID: "node-b", Type: "public-endpoint",
					EndpointHost: "", EndpointPort: 0, Transport: "udp", IsEnabled: true},
			},
		}

		c := NewCompiler()
		result, err := c.Compile(topo, testKeys2())
		if err != nil {
			t.Fatalf("Compile 失败: %v", err)
		}

		edge := findEdge(result.Topology.Edges, "e1")
		if edge == nil {
			t.Fatalf("结果中应有 edge e1")
		}
		if edge.CompiledPort != 0 {
			t.Errorf("endpoint_host 为空时不应写回 CompiledPort，实际 %d", edge.CompiledPort)
		}

		// 正向 peer 也不应有 Endpoint 行
		fwd := findPeer(result.PeerMap["node-a"], "node-b")
		if fwd == nil {
			t.Fatalf("node-a 应有指向 node-b 的 peer")
		}
		if fwd.Endpoint != "" {
			t.Errorf("endpoint_host 为空时不应生成 Endpoint，实际 %q", fwd.Endpoint)
		}
	})
}

// testKeys2 提供两节点测试所需的密钥。
func testKeys2() map[string]KeyPair {
	return map[string]KeyPair{
		"node-a": {PrivateKey: "privkey-a-fake", PublicKey: "pubkey-a-fake"},
		"node-b": {PrivateKey: "privkey-b-fake", PublicKey: "pubkey-b-fake"},
	}
}

// testKeys4 提供 hub + 三 spoke 测试所需的密钥。
func testKeys4() map[string]KeyPair {
	return map[string]KeyPair{
		"node-h": {PrivateKey: "privkey-h-fake", PublicKey: "pubkey-h-fake"},
		"node-a": {PrivateKey: "privkey-a-fake", PublicKey: "pubkey-a-fake"},
		"node-b": {PrivateKey: "privkey-b-fake", PublicKey: "pubkey-b-fake"},
		"node-c": {PrivateKey: "privkey-c-fake", PublicKey: "pubkey-c-fake"},
	}
}
