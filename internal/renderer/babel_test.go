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
		t.Fatalf(" Babel : %v", err)
	}

	if config == "" {
		t.Fatalf("Router  Babel ")
	}

	// 
	if !strings.Contains(config, "interface wg-node-2") {
		t.Errorf(" wg-node-2 ")
	}
	if !strings.Contains(config, "interface wg-node-3") {
		t.Errorf(" wg-node-3 ")
	}

	// 
	if !strings.Contains(config, "type wired") {
		t.Errorf("WireGuard  wired ")
	}

	// 
	if !strings.Contains(config, "10.10.0.1/32") {
		t.Errorf(" overlay IP /32")
	}

	//  redistribute deny
	if !strings.Contains(config, "redistribute local deny") {
		t.Errorf(" redistribute local deny")
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
		t.Fatalf(": %v", err)
	}

	// peer  Babel（）
	if config == "" {
		t.Fatalf("Peer  babel  Babel ")
	}

	// 
	if !strings.Contains(config, "interface wg-hub-1") {
		t.Errorf(" wg-hub-1 ")
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
		RoutingMode: "static", //  babel 
	}

	config, err := RenderBabelConfig(node, peers, domain)
	if err != nil {
		t.Fatalf(": %v", err)
	}

	//  babel  Babel 
	if config != "" {
		t.Errorf(" babel  Babel ")
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

	peers := []compiler.PeerInfo{} //  peer

	domain := &model.Domain{
		ID:          "domain-1",
		Name:        "test",
		CIDR:        "10.10.0.0/24",
		RoutingMode: "babel",
	}

	config, err := RenderBabelConfig(node, peers, domain)
	if err != nil {
		t.Fatalf(": %v", err)
	}

	//  peer，（）
	if config == "" {
		t.Fatalf(" peer  Babel ")
	}

	//  interface 
	if strings.Contains(config, "interface wg-") {
		t.Errorf(" peer ")
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
		t.Fatalf(" Babel : %v", err)
	}

	if len(configs) != 2 {
		t.Errorf(" 2  Babel ,  %d", len(configs))
	}

	for nodeID, config := range configs {
		if config == "" {
			t.Errorf(" %s  Babel ", nodeID)
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
		t.Fatalf(" sysctl : %v", err)
	}

	if !strings.Contains(config, "net.ipv4.ip_forward = 1") {
		t.Errorf(" ip_forward")
	}

	if !strings.Contains(config, "net.ipv4.conf.all.rp_filter = 0") {
		t.Errorf(" rp_filter")
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
		t.Fatalf(" sysctl : %v", err)
	}

	if strings.Contains(config, "net.ipv4.ip_forward = 1") {
		t.Errorf(" ip_forward")
	}

	//  rp_filter
	if !strings.Contains(config, "net.ipv4.conf.all.rp_filter = 2") {
		t.Errorf(" rp_filter (=2)")
	}
}
