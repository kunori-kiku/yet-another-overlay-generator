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
		t.Fatalf("渲染安装脚本失败: %v", err)
	}

	// 检查 shebang
	if !strings.HasPrefix(script, "#!/usr/bin/env bash") {
		t.Errorf("脚本应以 shebang 开头")
	}

	// 检查 set -euo pipefail
	if !strings.Contains(script, "set -euo pipefail") {
		t.Errorf("脚本应包含 set -euo pipefail")
	}

	// 检查安装 wireguard
	if !strings.Contains(script, "wireguard") {
		t.Errorf("脚本应安装 wireguard")
	}

	// 检查安装 babeld
	if !strings.Contains(script, "babeld") {
		t.Errorf("有 Babel 时脚本应安装 babeld")
	}

	// 检查三个阶段
	if !strings.Contains(script, "Phase 1") {
		t.Errorf("脚本应包含 Phase 1")
	}
	if !strings.Contains(script, "Phase 2") {
		t.Errorf("脚本应包含 Phase 2")
	}
	if !strings.Contains(script, "Phase 3") {
		t.Errorf("脚本应包含 Phase 3")
	}

	// 检查 root 检测
	if !strings.Contains(script, "id -u") {
		t.Errorf("脚本应检测 root 权限")
	}

	// 检查转发状态输出
	if !strings.Contains(script, "ip_forward") {
		t.Errorf("转发节点应输出 ip_forward 状态")
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

	// 无 babel 时不应安装 babeld
	if strings.Contains(script, "apt-get install -y -qq babeld") {
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
