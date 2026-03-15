package renderer

import (
	"strings"
	"testing"

	"github.com/kunorikiku/yet-another-overlay-generator/internal/compiler"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/model"
)

func TestRenderWireGuardConfig_Basic(t *testing.T) {
	node := &model.Node{
		ID:         "node-1",
		Name:       "alpha",
		OverlayIP:  "10.10.0.1",
		ListenPort: 51820,
	}

	peers := []compiler.PeerInfo{
		{
			NodeID:    "node-2",
			NodeName:  "beta",
			PublicKey: "pubkey-beta-fake",
			OverlayIP: "10.10.0.2",
			AllowedIPs: []string{"10.10.0.2/32"},
			Endpoint:  "203.0.113.2:51820",
		},
	}

	keys := compiler.KeyPair{
		PrivateKey: "privkey-alpha-fake",
		PublicKey:  "pubkey-alpha-fake",
	}

	config, err := RenderWireGuardConfig(node, peers, keys)
	if err != nil {
		t.Fatalf(" WireGuard : %v", err)
	}

	//  Interface 
	if !strings.Contains(config, "PrivateKey = privkey-alpha-fake") {
		t.Errorf(" PrivateKey")
	}
	if !strings.Contains(config, "Address = 10.10.0.1/32") {
		t.Errorf(" Address")
	}
	if !strings.Contains(config, "ListenPort = 51820") {
		t.Errorf(" ListenPort")
	}

	//  Peer 
	if !strings.Contains(config, "[Peer]") {
		t.Errorf(" [Peer] ")
	}
	if !strings.Contains(config, "PublicKey = pubkey-beta-fake") {
		t.Errorf(" PublicKey")
	}
	if !strings.Contains(config, "AllowedIPs = 10.10.0.2/32") {
		t.Errorf(" AllowedIPs")
	}
	if !strings.Contains(config, "Endpoint = 203.0.113.2:51820") {
		t.Errorf(" Endpoint")
	}
}

func TestRenderWireGuardConfig_WithKeepalive(t *testing.T) {
	node := &model.Node{
		ID:         "client-1",
		Name:       "nat-client",
		OverlayIP:  "10.10.0.2",
		ListenPort: 51820,
	}

	peers := []compiler.PeerInfo{
		{
			NodeID:              "hub-1",
			NodeName:            "hub",
			PublicKey:           "pubkey-hub-fake",
			OverlayIP:           "10.10.0.1",
			AllowedIPs:          []string{"10.10.0.1/32"},
			Endpoint:            "198.51.100.1:51820",
			PersistentKeepalive: 25,
		},
	}

	keys := compiler.KeyPair{
		PrivateKey: "privkey-client-fake",
		PublicKey:  "pubkey-client-fake",
	}

	config, err := RenderWireGuardConfig(node, peers, keys)
	if err != nil {
		t.Fatalf(": %v", err)
	}

	if !strings.Contains(config, "PersistentKeepalive = 25") {
		t.Errorf("NAT  PersistentKeepalive")
	}
}

func TestRenderWireGuardConfig_NoListenPort(t *testing.T) {
	node := &model.Node{
		ID:        "node-1",
		Name:      "alpha",
		OverlayIP: "10.10.0.1",
		// ListenPort  0
	}

	peers := []compiler.PeerInfo{}
	keys := compiler.KeyPair{PrivateKey: "privkey-fake", PublicKey: "pubkey-fake"}

	config, err := RenderWireGuardConfig(node, peers, keys)
	if err != nil {
		t.Fatalf(": %v", err)
	}

	if strings.Contains(config, "ListenPort") {
		t.Errorf("ListenPort  0 ")
	}
}

func TestRenderWireGuardConfig_MultiplePeers(t *testing.T) {
	node := &model.Node{
		ID:         "node-1",
		Name:       "alpha",
		OverlayIP:  "10.10.0.1",
		ListenPort: 51820,
	}

	peers := []compiler.PeerInfo{
		{NodeID: "node-2", NodeName: "beta", PublicKey: "pubkey-2", OverlayIP: "10.10.0.2", AllowedIPs: []string{"10.10.0.2/32"}, Endpoint: "203.0.113.2:51820"},
		{NodeID: "node-3", NodeName: "gamma", PublicKey: "pubkey-3", OverlayIP: "10.10.0.3", AllowedIPs: []string{"10.10.0.3/32"}, Endpoint: "203.0.113.3:51820"},
	}

	keys := compiler.KeyPair{PrivateKey: "privkey-1", PublicKey: "pubkey-1"}

	config, err := RenderWireGuardConfig(node, peers, keys)
	if err != nil {
		t.Fatalf(": %v", err)
	}

	//  2  [Peer] 
	peerCount := strings.Count(config, "[Peer]")
	if peerCount != 2 {
		t.Errorf(" 2  [Peer] ,  %d", peerCount)
	}

	if !strings.Contains(config, "# Peer: beta") {
		t.Errorf(" peer beta ")
	}
	if !strings.Contains(config, "# Peer: gamma") {
		t.Errorf(" peer gamma ")
	}
}

func TestRenderWireGuardConfig_NoEndpoint(t *testing.T) {
	node := &model.Node{
		ID:         "hub-1",
		Name:       "hub",
		OverlayIP:  "10.10.0.1",
		ListenPort: 51820,
	}

	peers := []compiler.PeerInfo{
		{
			NodeID:     "client-1",
			NodeName:   "client",
			PublicKey:  "pubkey-client",
			OverlayIP:  "10.10.0.2",
			AllowedIPs: []string{"10.10.0.2/32"},
			//  Endpoint（NAT  hub，hub  endpoint）
		},
	}

	keys := compiler.KeyPair{PrivateKey: "privkey-hub", PublicKey: "pubkey-hub"}

	config, err := RenderWireGuardConfig(node, peers, keys)
	if err != nil {
		t.Fatalf(": %v", err)
	}

	if strings.Contains(config, "Endpoint") {
		t.Errorf(" endpoint  Endpoint ")
	}
}

func TestRenderAllWireGuardConfigs(t *testing.T) {
	topo := &model.Topology{
		Nodes: []model.Node{
			{ID: "n1", Name: "alpha", OverlayIP: "10.10.0.1", ListenPort: 51820},
			{ID: "n2", Name: "beta", OverlayIP: "10.10.0.2", ListenPort: 51820},
		},
	}

	peerMap := map[string][]compiler.PeerInfo{
		"n1": {{NodeID: "n2", NodeName: "beta", PublicKey: "pub-2", OverlayIP: "10.10.0.2", AllowedIPs: []string{"10.10.0.2/32"}, Endpoint: "1.2.3.4:51820"}},
		"n2": {{NodeID: "n1", NodeName: "alpha", PublicKey: "pub-1", OverlayIP: "10.10.0.1", AllowedIPs: []string{"10.10.0.1/32"}, Endpoint: "5.6.7.8:51820"}},
	}

	keys := map[string]compiler.KeyPair{
		"n1": {PrivateKey: "priv-1", PublicKey: "pub-1"},
		"n2": {PrivateKey: "priv-2", PublicKey: "pub-2"},
	}

	configs, err := RenderAllWireGuardConfigs(topo, peerMap, keys)
	if err != nil {
		t.Fatalf(": %v", err)
	}

	if len(configs) != 2 {
		t.Errorf(" 2 ,  %d", len(configs))
	}

	for nodeID, config := range configs {
		if config == "" {
			t.Errorf(" %s ", nodeID)
		}
	}
}
