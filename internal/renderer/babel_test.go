package renderer

import (
	"strings"
	"testing"

	"github.com/kunorikiku/yet-another-overlay-generator/internal/compiler"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/model"
)

func TestRenderBabelConfig_Router_PerPeer(t *testing.T) {
	node := &model.Node{
		ID:        "node-1",
		Name:      "alpha",
		Role:      "router",
		DomainID:  "domain-1",
		OverlayIP: "10.11.0.1",
		Capabilities: model.NodeCapabilities{
			CanForward: true,
		},
	}

	peers := []compiler.PeerInfo{
		{NodeID: "node-2", NodeName: "beta", InterfaceName: "wg-beta",
			LocalTransitIP: "10.10.0.1", LocalLinkLocal: "fe80::1"},
		{NodeID: "node-3", NodeName: "gamma", InterfaceName: "wg-gamma",
			LocalTransitIP: "10.10.0.3", LocalLinkLocal: "fe80::3"},
	}

	domain := &model.Domain{
		ID:          "domain-1",
		Name:        "test",
		CIDR:        "10.11.0.0/24",
		RoutingMode: "babel",
	}

	config, err := RenderBabelConfig(node, peers, domain)
	if err != nil {
		t.Fatalf("渲染 Babel 配置失败: %v", err)
	}

	if config == "" {
		t.Fatalf("Router 应该生成 Babel 配置")
	}

	// 应包含 router-id（MAC-48 格式）
	if !strings.Contains(config, "router-id") {
		t.Errorf("应包含 router-id")
	}

	// router-id 应包含冒号（MAC-48 格式）
	lines := strings.Split(config, "\n")
	for _, line := range lines {
		if strings.HasPrefix(line, "router-id ") {
			rid := strings.TrimPrefix(line, "router-id ")
			if !strings.Contains(rid, ":") {
				t.Errorf("router-id 应为 MAC-48 格式，实际: %s", rid)
			}
		}
	}

	// 应包含 per-peer 接口声明
	if !strings.Contains(config, "interface wg-beta") {
		t.Errorf("应包含 wg-beta 接口声明")
	}
	if !strings.Contains(config, "interface wg-gamma") {
		t.Errorf("应包含 wg-gamma 接口声明")
	}

	// 接口类型应为 tunnel（不是 wired）
	if !strings.Contains(config, "type tunnel") {
		t.Errorf("WireGuard 接口类型应为 tunnel")
	}
	if strings.Contains(config, "type wired") {
		t.Errorf("不应使用 type wired，应使用 type tunnel")
	}

	// 应包含 hello-interval 和 update-interval
	if !strings.Contains(config, "hello-interval 4") {
		t.Errorf("应包含 hello-interval 4")
	}
	if !strings.Contains(config, "update-interval 16") {
		t.Errorf("应包含 update-interval 16")
	}

	// 应包含 local-port
	if !strings.Contains(config, "local-port 33123") {
		t.Errorf("应包含 local-port 33123")
	}

	// 应包含 skip-kernel-setup
	if !strings.Contains(config, "skip-kernel-setup false") {
		t.Errorf("应包含 skip-kernel-setup false")
	}

	// 应包含 overlay IP /32 重分发
	if !strings.Contains(config, "10.11.0.1/32") {
		t.Errorf("应包含 overlay IP /32 重分发")
	}

	// 应包含 redistribute deny
	if !strings.Contains(config, "redistribute local deny") {
		t.Errorf("应包含 redistribute local deny")
	}

	// 不应包含 default-metric
	if strings.Contains(config, "default-metric") {
		t.Errorf("不应包含 default-metric")
	}
}

func TestRenderBabelConfig_CustomRouterID(t *testing.T) {
	node := &model.Node{
		ID:        "node-1",
		Name:      "alpha",
		Role:      "router",
		DomainID:  "domain-1",
		OverlayIP: "10.11.0.1",
		RouterID:  "02:aa:bb:cc:dd:ee",
	}

	peers := []compiler.PeerInfo{
		{NodeID: "node-2", NodeName: "beta", InterfaceName: "wg-beta"},
	}

	domain := &model.Domain{
		ID: "domain-1", Name: "test", CIDR: "10.11.0.0/24", RoutingMode: "babel",
	}

	config, err := RenderBabelConfig(node, peers, domain)
	if err != nil {
		t.Fatalf("渲染失败: %v", err)
	}

	if !strings.Contains(config, "router-id 02:aa:bb:cc:dd:ee") {
		t.Errorf("应使用用户自定义的 router-id")
	}
}

func TestRenderBabelConfig_Peer(t *testing.T) {
	node := &model.Node{
		ID:        "client-1",
		Name:      "nat-client",
		Role:      "peer",
		DomainID:  "domain-1",
		OverlayIP: "10.11.0.2",
	}

	peers := []compiler.PeerInfo{
		{NodeID: "hub-1", NodeName: "hub", InterfaceName: "wg-hub"},
	}

	domain := &model.Domain{
		ID: "domain-1", Name: "test", CIDR: "10.11.0.0/24", RoutingMode: "babel",
	}

	config, err := RenderBabelConfig(node, peers, domain)
	if err != nil {
		t.Fatalf("渲染失败: %v", err)
	}

	if config == "" {
		t.Fatalf("Peer 在 babel 域下应生成 Babel 配置")
	}

	if !strings.Contains(config, "interface wg-hub") {
		t.Errorf("应包含 wg-hub 接口声明")
	}
}

func TestRenderBabelConfig_NonBabelDomain(t *testing.T) {
	node := &model.Node{
		ID: "node-1", Name: "alpha", Role: "router", DomainID: "domain-1", OverlayIP: "10.11.0.1",
	}

	peers := []compiler.PeerInfo{
		{NodeID: "node-2", NodeName: "beta", InterfaceName: "wg-beta"},
	}

	domain := &model.Domain{
		ID: "domain-1", Name: "test", CIDR: "10.11.0.0/24", RoutingMode: "static",
	}

	config, err := RenderBabelConfig(node, peers, domain)
	if err != nil {
		t.Fatalf("渲染失败: %v", err)
	}

	if config != "" {
		t.Errorf("非 babel 域不应生成 Babel 配置")
	}
}

func TestRenderBabelConfig_NoPeers(t *testing.T) {
	node := &model.Node{
		ID: "node-1", Name: "alpha", Role: "router", DomainID: "domain-1", OverlayIP: "10.11.0.1",
	}

	peers := []compiler.PeerInfo{}

	domain := &model.Domain{
		ID: "domain-1", Name: "test", CIDR: "10.11.0.0/24", RoutingMode: "babel",
	}

	config, err := RenderBabelConfig(node, peers, domain)
	if err != nil {
		t.Fatalf("渲染失败: %v", err)
	}

	if config == "" {
		t.Fatalf("无 peer 时也应生成 Babel 配置（但无接口）")
	}

	// 不应包含 interface 行
	if strings.Contains(config, "interface wg-") {
		t.Errorf("无 peer 时不应有接口声明")
	}
}

func TestRenderAllBabelConfigs_PerPeer(t *testing.T) {
	topo := &model.Topology{
		Domains: []model.Domain{{
			ID: "domain-1", Name: "test", CIDR: "10.11.0.0/24",
			RoutingMode: "babel",
		}},
		Nodes: []model.Node{
			{ID: "n1", Name: "alpha", Role: "router", DomainID: "domain-1", OverlayIP: "10.11.0.1",
				Capabilities: model.NodeCapabilities{CanForward: true}},
			{ID: "n2", Name: "beta", Role: "peer", DomainID: "domain-1", OverlayIP: "10.11.0.2"},
		},
	}

	peerMap := map[string][]compiler.PeerInfo{
		"n1": {{NodeID: "n2", NodeName: "beta", InterfaceName: "wg-beta"}},
		"n2": {{NodeID: "n1", NodeName: "alpha", InterfaceName: "wg-alpha"}},
	}

	configs, err := RenderAllBabelConfigs(topo, peerMap)
	if err != nil {
		t.Fatalf("渲染失败: %v", err)
	}

	if len(configs) != 2 {
		t.Errorf("应有 2 个 Babel 配置，实际 %d", len(configs))
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
		t.Errorf("应包含 ip_forward")
	}

	if !strings.Contains(config, "net.ipv4.conf.all.rp_filter = 0") {
		t.Errorf("应包含 rp_filter")
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
		t.Errorf("不应包含 ip_forward")
	}

	if !strings.Contains(config, "net.ipv4.conf.all.rp_filter = 2") {
		t.Errorf("应包含 rp_filter (=2)")
	}
}
