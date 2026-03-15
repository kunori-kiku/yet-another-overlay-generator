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
		t.Fatalf(": %v", err)
	}

	//  shebang
	if !strings.HasPrefix(script, "#!/usr/bin/env bash") {
		t.Errorf(" shebang ")
	}

	//  set -euo pipefail
	if !strings.Contains(script, "set -euo pipefail") {
		t.Errorf(" set -euo pipefail")
	}

	//  wireguard
	if !strings.Contains(script, "wireguard") {
		t.Errorf(" wireguard")
	}

	//  babeld
	if !strings.Contains(script, "babeld") {
		t.Errorf(" Babel  babeld")
	}

	// 
	if !strings.Contains(script, "Phase 1") {
		t.Errorf(" Phase 1")
	}
	if !strings.Contains(script, "Phase 2") {
		t.Errorf(" Phase 2")
	}
	if !strings.Contains(script, "Phase 3") {
		t.Errorf(" Phase 3")
	}

	//  root 
	if !strings.Contains(script, "id -u") {
		t.Errorf(" root ")
	}

	// 
	if !strings.Contains(script, "ip_forward") {
		t.Errorf(" ip_forward ")
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
		t.Fatalf(": %v", err)
	}

	//  babel  babeld
	if strings.Contains(script, "apt-get install -y -qq babeld") {
		t.Errorf(" Babel  babeld")
	}

	//  babel  babel 
	if strings.Contains(script, "mkdir -p /etc/babel") {
		t.Errorf(" Babel  babel ")
	}
}

func TestRenderInstallScript_DefaultPlatform(t *testing.T) {
	node := &model.Node{
		ID:        "node-1",
		Name:      "test",
		Role:      "peer",
		OverlayIP: "10.10.0.1",
		// Platform 
	}

	script, err := RenderInstallScript(node, false)
	if err != nil {
		t.Fatalf(": %v", err)
	}

	if !strings.Contains(script, "Platform: debian") {
		t.Errorf(" debian")
	}
}
