package renderer

import (
	"strings"
	"testing"

	"github.com/kunorikiku/yet-another-overlay-generator/internal/model"
)

func TestRenderInstallScript_RouterWithBabel(t *testing.T) {
	node := &model.Node{
		ID:        "node-1",
		Name:      "alpha",
		Role:      "router",
		Platform:  "debian",
		OverlayIP: "10.10.0.1",
		Capabilities: model.NodeCapabilities{
			CanForward: true,
		},
	}

	script, err := RenderInstallScript(node, true)
	if err != nil {
		t.Fatalf("渲染失败: %v", err)
	}

	//  shebang
	if !strings.HasPrefix(script, "#!/usr/bin/env bash") {
		t.Errorf("缺少 shebang 行")
	}

	//  set -euo pipefail
	if !strings.Contains(script, "set -euo pipefail") {
		t.Errorf("缺少 set -euo pipefail")
	}

	//  wireguard
	if !strings.Contains(script, "wireguard") {
		t.Errorf("缺少 wireguard")
	}

	//  babeld
	if !strings.Contains(script, "babeld") {
		t.Errorf("有 Babel 时应包含 babeld")
	}

	// 验证 Phase 0 清理阶段
	if !strings.Contains(script, "Phase 0") {
		t.Errorf("缺少 Phase 0 清理阶段")
	}

	// 验证包含所有阶段
	if !strings.Contains(script, "Phase 1") {
		t.Errorf("缺少 Phase 1")
	}
	if !strings.Contains(script, "Phase 2") {
		t.Errorf("缺少 Phase 2")
	}
	if !strings.Contains(script, "Phase 3") {
		t.Errorf("缺少 Phase 3")
	}

	//  root 
	if !strings.Contains(script, "id -u") {
		t.Errorf("缺少 root 检查")
	}

	// 
	if !strings.Contains(script, "ip_forward") {
		t.Errorf("缺少 ip_forward 相关")
	}

	// Phase 0 应包含停止 WireGuard
	if !strings.Contains(script, "wg-quick down") {
		t.Errorf("Phase 0 应包含 wg-quick down 清理命令")
	}

	// Phase 0 应包含停止 Babel（因为 hasBabel=true）
	if !strings.Contains(script, "systemctl stop babeld") {
		t.Errorf("Phase 0 应包含停止 babeld 的清理命令")
	}

	// Phase 0 应该删除旧配置文件
	if !strings.Contains(script, "rm -f") {
		t.Errorf("Phase 0 应包含删除旧配置文件的命令")
	}

	// Phase 2 不应包含备份逻辑（Phase 0 已清理）
	if strings.Contains(script, ".bak.") {
		t.Errorf("不应包含备份逻辑，Phase 0 已清理旧文件")
	}

	// 应包含 babeld systemd override（指定配置文件路径）
	if !strings.Contains(script, "babeld.service.d/override.conf") {
		t.Errorf("有 Babel 时应创建 systemd override 指定配置文件路径")
	}
	if !strings.Contains(script, "/etc/babel/babeld.conf") {
		t.Errorf("babeld 应使用 /etc/babel/babeld.conf 配置路径")
	}
}

func TestRenderInstallScript_PeerWithoutBabel(t *testing.T) {
	node := &model.Node{
		ID:        "client-1",
		Name:      "nat-client",
		Role:      "peer",
		Platform:  "ubuntu",
		OverlayIP: "10.10.0.2",
		Capabilities: model.NodeCapabilities{
			CanForward: false,
		},
	}

	script, err := RenderInstallScript(node, false)
	if err != nil {
		t.Fatalf("渲染失败: %v", err)
	}

	//  babel  babeld
	if strings.Contains(script, "ensure_pkg babeld") {
		t.Errorf("无 Babel 时不应安装 babeld")
	}

	//  babel  babel 
	if strings.Contains(script, "mkdir -p /etc/babel") {
		t.Errorf("无 Babel 时不应创建 babel 目录")
	}

	// Phase 0 不应包含 babeld 清理
	if strings.Contains(script, "systemctl stop babeld") {
		t.Errorf("无 Babel 时 Phase 0 不应清理 babeld")
	}
}

func TestRenderInstallScript_DefaultPlatform(t *testing.T) {
	node := &model.Node{
		ID:        "node-1",
		Name:      "test",
		Role:      "peer",
		OverlayIP: "10.10.0.1",
		// Platform 为空
	}

	script, err := RenderInstallScript(node, false)
	if err != nil {
		t.Fatalf("渲染失败: %v", err)
	}

	if !strings.Contains(script, "Platform: debian") {
		t.Errorf("默认平台应为 debian")
	}
}

func TestRenderInstallScript_WithMTU(t *testing.T) {
	node := &model.Node{
		ID:        "node-1",
		Name:      "mtu-node",
		Role:      "router",
		Platform:  "debian",
		OverlayIP: "10.10.0.1",
		MTU:       1280,
		Capabilities: model.NodeCapabilities{
			CanForward: true,
		},
	}

	script, err := RenderInstallScript(node, false)
	if err != nil {
		t.Fatalf("渲染失败: %v", err)
	}

	// 状态输出中应显示 MTU
	if !strings.Contains(script, "MTU: 1280") {
		t.Errorf("设置了 MTU 时状态输出应显示 MTU 值")
	}
}

func TestRenderInstallScript_WithoutMTU(t *testing.T) {
	node := &model.Node{
		ID:        "node-1",
		Name:      "default-mtu",
		Role:      "peer",
		Platform:  "debian",
		OverlayIP: "10.10.0.1",
		// MTU 默认为 0
	}

	script, err := RenderInstallScript(node, false)
	if err != nil {
		t.Fatalf("渲染失败: %v", err)
	}

	// 没有设置 MTU 时不应显示 MTU 行
	if strings.Contains(script, "MTU:") {
		t.Errorf("未设置 MTU 时不应在状态中显示 MTU")
	}
}

func TestRenderInstallScript_CleanupPhaseOrder(t *testing.T) {
	node := &model.Node{
		ID:        "node-1",
		Name:      "order-test",
		Role:      "router",
		Platform:  "debian",
		OverlayIP: "10.10.0.1",
		Capabilities: model.NodeCapabilities{
			CanForward: true,
		},
	}

	script, err := RenderInstallScript(node, true)
	if err != nil {
		t.Fatalf("渲染失败: %v", err)
	}

	// Phase 0 should come before Phase 1
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
