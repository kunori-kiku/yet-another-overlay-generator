package compiler

import (
	"strings"
	"testing"

	"github.com/kunorikiku/yet-another-overlay-generator/internal/model"
)

// portBoundsTopo 构建一个以 hub 为中心、向外连接 spokeCount 个 spoke 的星型拓扑，
// hub 的基础监听端口为 hubBasePort。hub 的每条 peer 接口在 base+offset 处监听，
// 因此当 spokeCount 足够大时，hub 的有效端口会越过 65535（审计项 D11）。
func portBoundsTopo(hubBasePort, spokeCount int) (*model.Topology, map[string]KeyPair) {
	nodes := []model.Node{
		{
			ID: "node-hub", Name: "hub", Hostname: "hub.example.com",
			Role: "router", DomainID: "domain-1", ListenPort: hubBasePort,
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
			Role: "router", DomainID: "domain-1", ListenPort: 51820,
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
	// 为节点分配 overlay IP（DerivePeers 直接消费 OverlayIP，不再经过 IP 分配器）。
	for i := range topo.Nodes {
		topo.Nodes[i].OverlayIP = overlayIPForIndex(i)
	}
	return topo, keys
}

func spokeName(i int) string {
	return "spoke-" + string(rune('a'+i))
}

func overlayIPForIndex(i int) string {
	// 10.50.0.(i+1)；测试用例规模远小于 /24，无需边界处理。
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

// TestDerivePeers_EffectivePortOverflowErrors 验证：基础端口 65530 且 peer 数量足以让
// 有效端口越过 65535 时，DerivePeers 返回错误，且错误中点名越界的节点（审计项 D11）。
// 直接调用编译器（而非经验证器）能确保该不变量在 API/CLI 直连路径上同样不可被绕过。
func TestDerivePeers_EffectivePortOverflowErrors(t *testing.T) {
	// base=65530：offset 0..5 -> 65530..65535（合法），offset 6 -> 65536（越界）。
	// 7 个 spoke 使第 7 条 peer 接口的有效端口达到 65536。
	topo, keys := portBoundsTopo(65530, 7)

	_, _, err := DerivePeers(topo, keys)
	if err == nil {
		t.Fatalf("有效监听端口越过 65535 时应返回错误，但得到 nil")
	}
	if !strings.Contains(err.Error(), "hub") {
		t.Errorf("错误信息应点名越界节点 \"hub\"，实际: %q", err.Error())
	}
	if !strings.Contains(err.Error(), "65535") {
		t.Errorf("错误信息应提及上限 65535，实际: %q", err.Error())
	}
}

// TestCompile_EffectivePortOverflowErrors 经完整 Compile 路径再验证一次：同一越界拓扑
// 应使 Compile 返回错误且点名节点；标准基础端口（51820）的同规模拓扑则正常编译。
func TestCompile_EffectivePortOverflowErrors(t *testing.T) {
	c := NewCompiler()

	// 越界：base=65530，7 个 spoke。
	overflowTopo, overflowKeys := portBoundsTopo(65530, 7)
	if _, err := c.Compile(overflowTopo, overflowKeys); err == nil {
		t.Fatalf("有效端口越界拓扑应使 Compile 返回错误，但得到 nil")
	} else if !strings.Contains(err.Error(), "hub") {
		t.Errorf("Compile 错误应点名越界节点 \"hub\"，实际: %q", err.Error())
	}

	// 同规模、合法基础端口：应正常编译。
	okTopo, okKeys := portBoundsTopo(51820, 7)
	if _, err := c.Compile(okTopo, okKeys); err != nil {
		t.Fatalf("基础端口 51820 的同规模拓扑应正常编译，但失败: %v", err)
	}
}
