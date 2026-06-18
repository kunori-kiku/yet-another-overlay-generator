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
		t.Fatalf("render failed: %v", err)
	}

	// shebang
	if !strings.HasPrefix(script, "#!/usr/bin/env bash") {
		t.Errorf("missing shebang")
	}

	// Phase 0 cleanup
	if !strings.Contains(script, "Phase 0") {
		t.Errorf("missing Phase 0 cleanup stage")
	}

	// should contain all phases
	for _, phase := range []string{"Phase 0", "Phase 1", "Phase 2", "Phase 3"} {
		if !strings.Contains(script, phase) {
			t.Errorf("missing %s", phase)
		}
	}

	// should contain the per-peer interface names
	if !strings.Contains(script, "wg-beta") {
		t.Errorf("should contain the wg-beta interface")
	}
	if !strings.Contains(script, "wg-gamma") {
		t.Errorf("should contain the wg-gamma interface")
	}

	// should contain dummy0 creation (overlay address)
	if !strings.Contains(script, "dummy0") {
		t.Errorf("should contain dummy0 interface creation")
	}
	if !strings.Contains(script, "10.11.0.1/32") {
		t.Errorf("should contain the overlay address assigned to dummy0")
	}

	// should contain the babeld systemd override
	if !strings.Contains(script, "babeld.service.d/override.conf") {
		t.Errorf("should contain the babeld systemd override")
	}

	// should clean up the legacy single-interface wg0
	if !strings.Contains(script, "wg0") {
		t.Errorf("should clean up the legacy wg0 config")
	}

	// should contain ip_forward
	if !strings.Contains(script, "ip_forward") {
		t.Errorf("should contain ip_forward")
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
		t.Fatalf("render failed: %v", err)
	}

	// without babel, babeld should not be installed
	if strings.Contains(script, "ensure_cmd babeld babeld") {
		t.Errorf("without Babel, babeld should not be installed")
	}

	// without babel, the babel directory should not be created
	if strings.Contains(script, "mkdir -p /etc/babel") {
		t.Errorf("without Babel, the babel directory should not be created")
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
		t.Fatalf("render failed: %v", err)
	}

	if !strings.Contains(script, "Platform: debian") {
		t.Errorf("the default platform should be debian")
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
		t.Fatalf("render failed: %v", err)
	}

	// Phase ordering check — anchor on the unambiguous phase-title markers (a bare "Phase N"
	// substring would be matched first by an explanatory comment in the template, e.g. the
	// uninstall section's comment referencing the Phase 1 cleanup function).
	phase0Idx := strings.Index(script, "=== Phase 0:")
	phase1Idx := strings.Index(script, "=== Phase 1:")
	phase2Idx := strings.Index(script, "=== Phase 2:")
	phase3Idx := strings.Index(script, "=== Phase 3:")

	if phase0Idx >= phase1Idx {
		t.Errorf("Phase 0 should come before Phase 1")
	}
	if phase1Idx >= phase2Idx {
		t.Errorf("Phase 1 should come before Phase 2")
	}
	if phase2Idx >= phase3Idx {
		t.Errorf("Phase 2 should come before Phase 3")
	}
}
