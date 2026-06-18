package renderer

import (
	"strings"
	"testing"

	"github.com/kunorikiku/yet-another-overlay-generator/internal/compiler"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/model"
)

func TestRenderPerPeerWireGuardConfig_Basic(t *testing.T) {
	node := &model.Node{
		ID:        "node-1",
		Name:      "alpha",
		OverlayIP: "10.11.0.1",
	}

	peer := compiler.PeerInfo{
		NodeID:              "node-2",
		NodeName:            "beta",
		PublicKey:           "peer-pubkey-fake",
		OverlayIP:           "10.11.0.2",
		AllowedIPs:          []string{"0.0.0.0/0", "::/0"},
		Endpoint:            "203.0.113.2:51820",
		PersistentKeepalive: 25,
		InterfaceName:       "wg-beta",
		ListenPort:          51820,
		LocalTransitIP:      "10.10.0.1",
		RemoteTransitIP:     "10.10.0.2",
		LocalLinkLocal:      "fe80::1",
		RemoteLinkLocal:     "fe80::2",
	}

	keys := compiler.KeyPair{PrivateKey: "test-privkey", PublicKey: "test-pubkey"}

	config, err := RenderPerPeerWireGuardConfig(node, peer, keys)
	if err != nil {
		t.Fatalf("render failed: %v", err)
	}

	// should contain the interface name
	if !strings.Contains(config, "wg-beta") {
		t.Errorf("should contain interface name wg-beta")
	}

	// should use the transit IP rather than the overlay IP
	if !strings.Contains(config, "Address = 10.10.0.1/32") {
		t.Errorf("Address should use transit IP 10.10.0.1/32")
	}

	// should contain Table = off (prevents wg-quick from adding a default route)
	if !strings.Contains(config, "Table = off") {
		t.Errorf("should contain Table = off")
	}

	// should contain ListenPort
	if !strings.Contains(config, "ListenPort = 51820") {
		t.Errorf("should contain ListenPort")
	}

	// should contain the IPv6 link-local PostUp
	if !strings.Contains(config, "fe80::1") {
		t.Errorf("should contain the IPv6 link-local address")
	}

	// should contain PostUp/PostDown
	if !strings.Contains(config, "PostUp") || !strings.Contains(config, "PostDown") {
		t.Errorf("should contain PostUp/PostDown configuring link-local")
	}

	// AllowedIPs should be the permissive policy
	if !strings.Contains(config, "0.0.0.0/0") {
		t.Errorf("AllowedIPs should contain the permissive policy 0.0.0.0/0")
	}

	// should contain Endpoint
	if !strings.Contains(config, "Endpoint = 203.0.113.2:51820") {
		t.Errorf("should contain Endpoint")
	}

	// should contain PersistentKeepalive
	if !strings.Contains(config, "PersistentKeepalive = 25") {
		t.Errorf("should contain PersistentKeepalive")
	}

	// there must be exactly one [Peer] section
	peerCount := strings.Count(config, "[Peer]")
	if peerCount != 1 {
		t.Errorf("per-peer mode should have only 1 [Peer] section, got %d", peerCount)
	}
}

func TestRenderPerPeerWireGuardConfig_NoEndpoint(t *testing.T) {
	node := &model.Node{
		ID:        "node-2",
		Name:      "beta",
		OverlayIP: "10.11.0.2",
	}

	peer := compiler.PeerInfo{
		NodeID:          "node-1",
		NodeName:        "alpha",
		PublicKey:       "peer-pubkey-fake",
		OverlayIP:       "10.11.0.1",
		AllowedIPs:      []string{"0.0.0.0/0", "::/0"},
		InterfaceName:   "wg-alpha",
		ListenPort:      51821,
		LocalTransitIP:  "10.10.0.2",
		RemoteTransitIP: "10.10.0.1",
		LocalLinkLocal:  "fe80::2",
		RemoteLinkLocal: "fe80::1",
	}

	keys := compiler.KeyPair{PrivateKey: "test-privkey-2", PublicKey: "test-pubkey-2"}

	config, err := RenderPerPeerWireGuardConfig(node, peer, keys)
	if err != nil {
		t.Fatalf("render failed: %v", err)
	}

	// should not contain Endpoint
	if strings.Contains(config, "Endpoint") {
		t.Errorf("with no endpoint, no Endpoint line should appear")
	}
}

func TestRenderPerPeerWireGuardConfig_WithMTU(t *testing.T) {
	node := &model.Node{
		ID:        "node-1",
		Name:      "alpha",
		OverlayIP: "10.11.0.1",
		MTU:       1280,
	}

	peer := compiler.PeerInfo{
		NodeID:        "node-2",
		NodeName:      "beta",
		PublicKey:     "peer-pubkey",
		AllowedIPs:    []string{"0.0.0.0/0", "::/0"},
		InterfaceName: "wg-beta",
		ListenPort:    51820,
		// MTU is now determined per-interface by PeerInfo.MTU (mimic links are -12); the renderer reads peer.MTU, not node.MTU.
		MTU:            1280,
		LocalTransitIP: "10.10.0.1",
		LocalLinkLocal: "fe80::1",
	}

	keys := compiler.KeyPair{PrivateKey: "privkey", PublicKey: "pubkey"}

	config, err := RenderPerPeerWireGuardConfig(node, peer, keys)
	if err != nil {
		t.Fatalf("render failed: %v", err)
	}

	if !strings.Contains(config, "MTU = 1280") {
		t.Errorf("should contain MTU = 1280")
	}
}

func TestRenderAllWireGuardConfigs_PerPeer(t *testing.T) {
	topo := &model.Topology{
		Domains: []model.Domain{{
			ID: "domain-1", Name: "test", CIDR: "10.11.0.0/24",
			RoutingMode: "babel",
		}},
		Nodes: []model.Node{
			{ID: "n1", Name: "alpha", Role: "router", DomainID: "domain-1", OverlayIP: "10.11.0.1"},
			{ID: "n2", Name: "beta", Role: "router", DomainID: "domain-1", OverlayIP: "10.11.0.2"},
		},
	}

	peerMap := map[string][]compiler.PeerInfo{
		"n1": {{
			NodeID: "n2", NodeName: "beta", PublicKey: "pub-n2",
			InterfaceName: "wg-beta", ListenPort: 51820,
			AllowedIPs:     []string{"0.0.0.0/0", "::/0"},
			LocalTransitIP: "10.10.0.1", RemoteTransitIP: "10.10.0.2",
			LocalLinkLocal: "fe80::1", RemoteLinkLocal: "fe80::2",
		}},
		"n2": {{
			NodeID: "n1", NodeName: "alpha", PublicKey: "pub-n1",
			InterfaceName: "wg-alpha", ListenPort: 51820,
			AllowedIPs:     []string{"0.0.0.0/0", "::/0"},
			LocalTransitIP: "10.10.0.2", RemoteTransitIP: "10.10.0.1",
			LocalLinkLocal: "fe80::2", RemoteLinkLocal: "fe80::1",
		}},
	}

	keys := map[string]compiler.KeyPair{
		"n1": {PrivateKey: "priv-n1", PublicKey: "pub-n1"},
		"n2": {PrivateKey: "priv-n2", PublicKey: "pub-n2"},
	}

	configs, err := RenderAllWireGuardConfigs(topo, peerMap, keys)
	if err != nil {
		t.Fatalf("render failed: %v", err)
	}

	// there should be 2 config files (one per-peer interface per node)
	if len(configs) != 2 {
		t.Errorf("there should be 2 configs, got %d", len(configs))
	}

	// the key format should be "nodeID:interfaceName"
	if _, ok := configs["n1:wg-beta"]; !ok {
		t.Errorf("there should be an n1:wg-beta config")
	}
	if _, ok := configs["n2:wg-alpha"]; !ok {
		t.Errorf("there should be an n2:wg-alpha config")
	}
}
