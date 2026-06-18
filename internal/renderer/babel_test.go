package renderer

import (
	"strings"
	"testing"

	"github.com/kunorikiku/yet-another-overlay-generator/internal/compiler"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/model"
)

// TestRenderBabelConfig_StableUnderPeerReorder pins C1: a node's babeld.conf must depend
// only on link identity (InterfaceName), not on peer-slice / edge-array order. Rendering the
// same peers in a different order produces byte-identical output, so a benign edge reorder
// does not churn the config hash / bundle digest (the incremental-deploy byte-stability the
// pin/reserve/heal apparatus protects), and the interfaces appear in InterfaceName order.
func TestRenderBabelConfig_StableUnderPeerReorder(t *testing.T) {
	node := &model.Node{
		ID: "node-1", Name: "alpha", Role: "router", DomainID: "domain-1",
		OverlayIP:    "10.11.0.1",
		Capabilities: model.NodeCapabilities{CanForward: true},
	}
	domain := &model.Domain{ID: "domain-1", Name: "test", CIDR: "10.11.0.0/24", RoutingMode: "babel"}

	beta := compiler.PeerInfo{NodeID: "node-2", NodeName: "beta", InterfaceName: "wg-beta",
		LocalTransitIP: "10.10.0.1", LocalLinkLocal: "fe80::1"}
	gamma := compiler.PeerInfo{NodeID: "node-3", NodeName: "gamma", InterfaceName: "wg-gamma",
		LocalTransitIP: "10.10.0.3", LocalLinkLocal: "fe80::3"}
	delta := compiler.PeerInfo{NodeID: "node-4", NodeName: "delta", InterfaceName: "wg-delta",
		LocalTransitIP: "10.10.0.5", LocalLinkLocal: "fe80::5"}

	forward, err := RenderBabelConfig(node, []compiler.PeerInfo{beta, gamma, delta}, domain)
	if err != nil {
		t.Fatalf("render (forward order): %v", err)
	}
	reordered, err := RenderBabelConfig(node, []compiler.PeerInfo{delta, beta, gamma}, domain)
	if err != nil {
		t.Fatalf("render (reordered): %v", err)
	}
	if forward != reordered {
		t.Errorf("babeld.conf differs under peer reorder (C1 regression):\n--- forward ---\n%s\n--- reordered ---\n%s", forward, reordered)
	}
	// Interfaces appear in InterfaceName order: wg-beta < wg-delta < wg-gamma.
	ib := strings.Index(forward, "interface wg-beta")
	id := strings.Index(forward, "interface wg-delta")
	ig := strings.Index(forward, "interface wg-gamma")
	if ib < 0 || id < 0 || ig < 0 || !(ib < id && id < ig) {
		t.Errorf("interfaces not in InterfaceName order: beta=%d delta=%d gamma=%d", ib, id, ig)
	}
}

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
		t.Fatalf("failed to render Babel config: %v", err)
	}

	if config == "" {
		t.Fatalf("router should generate a Babel config")
	}

	// Should contain router-id (MAC-48 format).
	if !strings.Contains(config, "router-id") {
		t.Errorf("should contain router-id")
	}

	// router-id should contain a colon (MAC-48 format).
	lines := strings.Split(config, "\n")
	for _, line := range lines {
		if strings.HasPrefix(line, "router-id ") {
			rid := strings.TrimPrefix(line, "router-id ")
			if !strings.Contains(rid, ":") {
				t.Errorf("router-id should be in MAC-48 format, actual: %s", rid)
			}
		}
	}

	// Should contain per-peer interface declarations.
	if !strings.Contains(config, "interface wg-beta") {
		t.Errorf("should contain wg-beta interface declaration")
	}
	if !strings.Contains(config, "interface wg-gamma") {
		t.Errorf("should contain wg-gamma interface declaration")
	}

	// Interface type should be tunnel (not wired).
	if !strings.Contains(config, "type tunnel") {
		t.Errorf("WireGuard interface type should be tunnel")
	}
	if strings.Contains(config, "type wired") {
		t.Errorf("should not use type wired, should use type tunnel")
	}

	// Should contain hello-interval and update-interval.
	if !strings.Contains(config, "hello-interval 4") {
		t.Errorf("should contain hello-interval 4")
	}
	if !strings.Contains(config, "update-interval 16") {
		t.Errorf("should contain update-interval 16")
	}

	// Should contain local-port.
	if !strings.Contains(config, "local-port 33123") {
		t.Errorf("should contain local-port 33123")
	}

	// Should contain skip-kernel-setup.
	if !strings.Contains(config, "skip-kernel-setup false") {
		t.Errorf("should contain skip-kernel-setup false")
	}

	// Should contain the overlay IP /32 redistribution.
	if !strings.Contains(config, "10.11.0.1/32") {
		t.Errorf("should contain the overlay IP /32 redistribution")
	}

	// Should contain redistribute deny.
	if !strings.Contains(config, "redistribute local deny") {
		t.Errorf("should contain redistribute local deny")
	}

	// Should not contain default-metric.
	if strings.Contains(config, "default-metric") {
		t.Errorf("should not contain default-metric")
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
		t.Fatalf("render failed: %v", err)
	}

	if !strings.Contains(config, "router-id 02:aa:bb:cc:dd:ee") {
		t.Errorf("should use the user-supplied router-id")
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
		t.Fatalf("render failed: %v", err)
	}

	if config == "" {
		t.Fatalf("peer should generate a Babel config in a babel domain")
	}

	if !strings.Contains(config, "interface wg-hub") {
		t.Errorf("should contain wg-hub interface declaration")
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
		t.Fatalf("render failed: %v", err)
	}

	if config != "" {
		t.Errorf("a non-babel domain should not generate a Babel config")
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
		t.Fatalf("render failed: %v", err)
	}

	if config == "" {
		t.Fatalf("a Babel config should be generated even with no peers (but with no interfaces)")
	}

	// Should not contain any interface lines.
	if strings.Contains(config, "interface wg-") {
		t.Errorf("there should be no interface declarations when there are no peers")
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
		t.Fatalf("render failed: %v", err)
	}

	if len(configs) != 2 {
		t.Errorf("should have 2 Babel configs, actual %d", len(configs))
	}

	for nodeID, config := range configs {
		if config == "" {
			t.Errorf("Babel config for node %s is empty", nodeID)
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
		t.Fatalf("failed to render sysctl config: %v", err)
	}

	if !strings.Contains(config, "net.ipv4.ip_forward = 1") {
		t.Errorf("should contain ip_forward")
	}

	if !strings.Contains(config, "net.ipv4.conf.all.rp_filter = 0") {
		t.Errorf("should contain rp_filter")
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
		t.Fatalf("failed to render sysctl config: %v", err)
	}

	if strings.Contains(config, "net.ipv4.ip_forward = 1") {
		t.Errorf("should not contain ip_forward")
	}

	if !strings.Contains(config, "net.ipv4.conf.all.rp_filter = 2") {
		t.Errorf("should contain rp_filter (=2)")
	}
}
