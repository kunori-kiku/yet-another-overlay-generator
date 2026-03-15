package renderer

import (
	"strings"
	"testing"

	"github.com/kunorikiku/yet-another-overlay-generator/internal/compiler"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/model"
)

func TestRenderBabelConfig_Router(t *testing.T) {
	node := &model.Node{
		ID:        "node-1",
		Name:      "alpha",
		Role:      "router",
		DomainID:  "domain-1",
		OverlayIP: "10.10.0.1",
		Capabilities: model.NodeCapabilities{
			CanForward: true,
		},
	}

	peers := []compiler.PeerInfo{
		{NodeID: "node-2", NodeName: "beta", InterfaceName: "wg-node-2"},
		{NodeID: "node-3", NodeName: "gamma", InterfaceName: "wg-node-3"},
	}

	domain := &model.Domain{
		ID:          "domain-1",
		Name:        "test",
		CIDR:        "10.10.0.0/24",
		RoutingMode: "babel",
	}

	config, err := RenderBabelConfig(node, peers, domain)
	if err != nil {
		t.Fatalf("渲染 Babel 配置失败: %v", err)
	}

	if config == "" {
		t.Fatalf("Router 节点应生成 Babel 配置")
	}

	// 检查接口声明
	if !strings.Contains(config, "interface wg-node-2") {
		t.Errorf("配置应包含 wg-node-2 接口")
	}
	if !strings.Contains(config, "interface wg-node-3") {
		t.Errorf("配置应包含 wg-node-3 接口")
	}

	// 检查接口类型
	if !strings.Contains(config, "type wired") {
		t.Errorf("WireGuard 接口应标记为 wired 类型")
	}

	// 检查通告前缀
	if !strings.Contains(config, "10.10.0.1/32") {
		t.Errorf("配置应通告自身 overlay IP /32")
	}

	// 检查 redistribute deny
	if !strings.Contains(config, "redistribute local deny") {
		t.Errorf("配置应包含 redistribute local deny")
	}
}

func TestRenderBabelConfig_Peer(t *testing.T) {
	node := &model.Node{
		ID:        "client-1",
		Name:      "nat-client",
		Role:      "peer",
		DomainID:  "domain-1",
		OverlayIP: "10.10.0.2",
		Capabilities: model.NodeCapabilities{
			CanForward: false,
		},
	}

	peers := []compiler.PeerInfo{
		{NodeID: "hub-1", NodeName: "hub", InterfaceName: "wg-hub-1"},
	}

	domain := &model.Domain{
		ID:          "domain-1",
		Name:        "test",
		CIDR:        "10.10.0.0/24",
		RoutingMode: "babel",
	}

	config, err := RenderBabelConfig(node, peers, domain)
	if err != nil {
		t.Fatalf("渲染失败: %v", err)
	}

	// peer 也应运行 Babel（需要学习路由）
	if config == "" {
		t.Fatalf("Peer 节点在 babel 模式下也应生成 Babel 配置")
	}

	// 应有接口
	if !strings.Contains(config, "interface wg-hub-1") {
		t.Errorf("配置应包含 wg-hub-1 接口")
	}
}

func TestRenderBabelConfig_NonBabelDomain(t *testing.T) {
	node := &model.Node{
		ID:        "node-1",
		Name:      "alpha",
		Role:      "router",
		DomainID:  "domain-1",
		OverlayIP: "10.10.0.1",
	}

	peers := []compiler.PeerInfo{
		{NodeID: "node-2", NodeName: "beta", InterfaceName: "wg-node-2"},
	}

	domain := &model.Domain{
		ID:          "domain-1",
		Name:        "test",
		CIDR:        "10.10.0.0/24",
		RoutingMode: "static", // 非 babel 模式
	}

	config, err := RenderBabelConfig(node, peers, domain)
	if err != nil {
		t.Fatalf("渲染失败: %v", err)
	}

	// 非 babel 模式不应生成 Babel 配置
	if config != "" {
		t.Errorf("非 babel 模式不应生成 Babel 配置")
	}
}

func TestRenderBabelConfig_NoPeers(t *testing.T) {
	node := &model.Node{
		ID:        "node-1",
		Name:      "alpha",
		Role:      "router",
		DomainID:  "domain-1",
		OverlayIP: "10.10.0.1",
	}

	peers := []compiler.PeerInfo{} // 无 peer

	domain := &model.Domain{
		ID:          "domain-1",
		Name:        "test",
		CIDR:        "10.10.0.0/24",
		RoutingMode: "babel",
	}

	config, err := RenderBabelConfig(node, peers, domain)
	if err != nil {
		t.Fatalf("渲染失败: %v", err)
	}

	// 即使没有 peer，也应生成基础配置（节点可能后续连接）
	if config == "" {
		t.Fatalf("即使没有 peer 也应生成基础 Babel 配置")
	}

	// 不应包含 interface 行
	if strings.Contains(config, "interface wg-") {
		t.Errorf("没有 peer 时不应有接口配置")
	}
}

func TestRenderAllBabelConfigs(t *testing.T) {
	topo := &model.Topology{
		Domains: []model.Domain{{
			ID: "domain-1", Name: "test", CIDR: "10.10.0.0/24",
			RoutingMode: "babel",
		}},
		Nodes: []model.Node{
			{ID: "n1", Name: "alpha", Role: "router", DomainID: "domain-1", OverlayIP: "10.10.0.1",
				Capabilities: model.NodeCapabilities{CanForward: true}},
			{ID: "n2", Name: "beta", Role: "peer", DomainID: "domain-1", OverlayIP: "10.10.0.2"},
		},
	}

	peerMap := map[string][]compiler.PeerInfo{
		"n1": {{NodeID: "n2", NodeName: "beta", InterfaceName: "wg-n2"}},
		"n2": {{NodeID: "n1", NodeName: "alpha", InterfaceName: "wg-n1"}},
	}

	configs, err := RenderAllBabelConfigs(topo, peerMap)
	if err != nil {
		t.Fatalf("渲染所有 Babel 配置失败: %v", err)
	}

	if len(configs) != 2 {
		t.Errorf("期望 2 个 Babel 配置, 得到 %d", len(configs))
	}

	for nodeID, config := range configs {
		if config == "" {
			t.Errorf("节点 %s 的 Babel 配置为空", nodeID)
		}
	}
}

func TestRenderSysctlConfig_Forwarding(t *testing.T) {
	node := &model.Node{
		ID:   "node-1",
		Name: "alpha",
		Capabilities: model.NodeCapabilities{
			CanForward: true,
		},
	}

	config, err := RenderSysctlConfig(node)
	if err != nil {
		t.Fatalf("渲染 sysctl 配置失败: %v", err)
	}

	if !strings.Contains(config, "net.ipv4.ip_forward = 1") {
		t.Errorf("转发节点应启用 ip_forward")
	}

	if !strings.Contains(config, "net.ipv4.conf.all.rp_filter = 0") {
		t.Errorf("转发节点应禁用 rp_filter")
	}
}

func TestRenderSysctlConfig_NoForwarding(t *testing.T) {
	node := &model.Node{
		ID:   "client-1",
		Name: "client",
		Capabilities: model.NodeCapabilities{
			CanForward: false,
		},
	}

	config, err := RenderSysctlConfig(node)
	if err != nil {
		t.Fatalf("渲染 sysctl 配置失败: %v", err)
	}

	if strings.Contains(config, "net.ipv4.ip_forward = 1") {
		t.Errorf("非转发节点不应启用 ip_forward")
	}

	// 应使用松散模式 rp_filter
	if !strings.Contains(config, "net.ipv4.conf.all.rp_filter = 2") {
		t.Errorf("非转发节点应使用松散模式 rp_filter (=2)")
	}
}
