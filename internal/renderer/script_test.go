package renderer

import (
	"strings"
	"testing"

	"github.com/kunorikiku/yet-another-overlay-generator/internal/compiler"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/model"
)

func TestRenderInstallScript_RouterWithBabel_PerPeer(t *testing.T) {
	node := &model.Node{
		ID:        "node-1",
		Name:      "alpha",
		Role:      "router",
		Platform:  "debian",
		OverlayIP: "10.11.0.1",
		Capabilities: model.NodeCapabilities{
			CanForward: true,
		},
	}

	peers := []compiler.PeerInfo{
		{NodeID: "node-2", NodeName: "beta", InterfaceName: "wg-beta",
			ListenPort: 51820, LocalTransitIP: "10.10.0.1", LocalLinkLocal: "fe80::1"},
		{NodeID: "node-3", NodeName: "gamma", InterfaceName: "wg-gamma",
			ListenPort: 51821, LocalTransitIP: "10.10.0.3", LocalLinkLocal: "fe80::3"},
	}

	script, err := RenderInstallScript(node, peers, true)
	if err != nil {
		t.Fatalf("渲染失败: %v", err)
	}

	// shebang
	if !strings.HasPrefix(script, "#!/usr/bin/env bash") {
		t.Errorf("缺少 shebang")
	}

	// Phase 0 清理
	if !strings.Contains(script, "Phase 0") {
		t.Errorf("缺少 Phase 0 清理阶段")
	}

	// 应包含所有阶段
	for _, phase := range []string{"Phase 0", "Phase 1", "Phase 2", "Phase 3"} {
		if !strings.Contains(script, phase) {
			t.Errorf("缺少 %s", phase)
		}
	}

	// 应包含 per-peer 接口名
	if !strings.Contains(script, "wg-beta") {
		t.Errorf("应包含 wg-beta 接口")
	}
	if !strings.Contains(script, "wg-gamma") {
		t.Errorf("应包含 wg-gamma 接口")
	}

	// 应包含 dummy0 创建（overlay 地址）
	if !strings.Contains(script, "dummy0") {
		t.Errorf("应包含 dummy0 接口创建")
	}
	if !strings.Contains(script, "10.11.0.1/32") {
		t.Errorf("应包含 overlay 地址分配到 dummy0")
	}

	// 应包含 babeld systemd override
	if !strings.Contains(script, "babeld.service.d/override.conf") {
		t.Errorf("应包含 babeld systemd override")
	}

	// 应清理遗留的单接口 wg0
	if !strings.Contains(script, "wg0") {
		t.Errorf("应清理遗留的 wg0 配置")
	}

	// 应包含 ip_forward
	if !strings.Contains(script, "ip_forward") {
		t.Errorf("应包含 ip_forward")
	}
}

func TestRenderInstallScript_PeerWithoutBabel(t *testing.T) {
	node := &model.Node{
		ID:        "client-1",
		Name:      "nat-client",
		Role:      "peer",
		Platform:  "ubuntu",
		OverlayIP: "10.11.0.2",
	}

	peers := []compiler.PeerInfo{
		{NodeID: "hub-1", NodeName: "hub", InterfaceName: "wg-hub",
			ListenPort: 51820, LocalTransitIP: "10.10.0.2", LocalLinkLocal: "fe80::2"},
	}

	script, err := RenderInstallScript(node, peers, false)
	if err != nil {
		t.Fatalf("渲染失败: %v", err)
	}

	// 无 babel 时不应安装 babeld
	if strings.Contains(script, "ensure_pkg babeld") {
		t.Errorf("无 Babel 时不应安装 babeld")
	}

	// 无 babel 时不应创建 babel 目录
	if strings.Contains(script, "mkdir -p /etc/babel") {
		t.Errorf("无 Babel 时不应创建 babel 目录")
	}
}

func TestRenderInstallScript_DefaultPlatform(t *testing.T) {
	node := &model.Node{
		ID:        "node-1",
		Name:      "test",
		Role:      "peer",
		OverlayIP: "10.11.0.1",
	}

	peers := []compiler.PeerInfo{
		{NodeID: "n2", NodeName: "peer2", InterfaceName: "wg-peer2",
			ListenPort: 51820, LocalTransitIP: "10.10.0.1", LocalLinkLocal: "fe80::1"},
	}

	script, err := RenderInstallScript(node, peers, false)
	if err != nil {
		t.Fatalf("渲染失败: %v", err)
	}

	if !strings.Contains(script, "Platform: debian") {
		t.Errorf("默认平台应为 debian")
	}
}

func TestRenderInstallScript_PerPeerCleanupOrder(t *testing.T) {
	node := &model.Node{
		ID:        "node-1",
		Name:      "order-test",
		Role:      "router",
		Platform:  "debian",
		OverlayIP: "10.11.0.1",
		Capabilities: model.NodeCapabilities{
			CanForward: true,
		},
	}

	peers := []compiler.PeerInfo{
		{NodeID: "n2", NodeName: "beta", InterfaceName: "wg-beta",
			ListenPort: 51820, LocalTransitIP: "10.10.0.1", LocalLinkLocal: "fe80::1"},
	}

	script, err := RenderInstallScript(node, peers, true)
	if err != nil {
		t.Fatalf("渲染失败: %v", err)
	}

	// Phase 顺序检查
	phase0Idx := strings.Index(script, "Phase 0")
	phase1Idx := strings.Index(script, "Phase 1")
	phase2Idx := strings.Index(script, "Phase 2")
	phase3Idx := strings.Index(script, "Phase 3")

	if phase0Idx >= phase1Idx {
		t.Errorf("Phase 0 应在 Phase 1 之前")
	}
	if phase1Idx >= phase2Idx {
		t.Errorf("Phase 1 应在 Phase 2 之前")
	}
	if phase2Idx >= phase3Idx {
		t.Errorf("Phase 2 应在 Phase 3 之前")
	}
}
