package compiler

import (
	"testing"

	"github.com/kunorikiku/yet-another-overlay-generator/internal/model"
)

// portBoundsTopo 构建一个以 hub 为中心、向外连接 spokeCount 个 spoke 的星型拓扑。
// 基准监听端口统一为 51820（per-node listen_port 已移除），hub 的每条 peer 接口在
// 51820+offset 处监听，用于覆盖多接口分配的编译路径。
func portBoundsTopo(spokeCount int) (*model.Topology, map[string]KeyPair) {
	nodes := []model.Node{
		{
			ID: "node-hub", Name: "hub", Hostname: "hub.example.com",
			Role: "router", DomainID: "domain-1",
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
			Role: "router", DomainID: "domain-1",
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

// TestCompile_ManyInterfacesCompileClean 验证：基准端口统一为 51820 后，一个 hub 连向 7 个
// spoke（hub 获得 7 个 per-peer 接口，监听 51820..51826）能正常编译。越界规则（lowestFreePort
// 在 port>65535 时返回 CodeListenPortExhausted）仍然保留，但在统一基准下需上万个接口才会触发，
// 无法再经一个高基准人为构造，故不再单测越界报错路径（per-node listen_port 已移除）。
func TestCompile_ManyInterfacesCompileClean(t *testing.T) {
	c := NewCompiler()
	topo, keys := portBoundsTopo(7)
	if _, err := c.Compile(topo, keys); err != nil {
		t.Fatalf("基准端口 51820 下 hub 连 7 个 spoke 应正常编译，但失败: %v", err)
	}
}
