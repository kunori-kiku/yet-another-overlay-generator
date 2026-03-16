package renderer

import (
	"strings"
	"testing"

	"github.com/kunorikiku/yet-another-overlay-generator/internal/compiler"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/model"
)

func TestRenderPerPeerWireGuardConfig_Basic(t *testing.T) {
	node := &model.Node{
		ID:         "node-1",
		Name:       "alpha",
		OverlayIP:  "10.11.0.1",
		ListenPort: 51820,
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
		t.Fatalf("渲染失败: %v", err)
	}

	// 应包含接口名
	if !strings.Contains(config, "wg-beta") {
		t.Errorf("应包含接口名 wg-beta")
	}

	// 应使用 transit IP 而非 overlay IP
	if !strings.Contains(config, "Address = 10.10.0.1/32") {
		t.Errorf("Address 应使用 transit IP 10.10.0.1/32")
	}

	// 应包含 Table = off（防止 wg-quick 添加默认路由）
	if !strings.Contains(config, "Table = off") {
		t.Errorf("应包含 Table = off")
	}

	// 应包含 ListenPort
	if !strings.Contains(config, "ListenPort = 51820") {
		t.Errorf("应包含 ListenPort")
	}

	// 应包含 IPv6 link-local PostUp
	if !strings.Contains(config, "fe80::1") {
		t.Errorf("应包含 IPv6 link-local 地址")
	}

	// 应包含 PostUp/PostDown
	if !strings.Contains(config, "PostUp") || !strings.Contains(config, "PostDown") {
		t.Errorf("应包含 PostUp/PostDown 配置 link-local")
	}

	// AllowedIPs 应为宽松策略
	if !strings.Contains(config, "0.0.0.0/0") {
		t.Errorf("AllowedIPs 应包含 0.0.0.0/0 宽松策略")
	}

	// 应包含 Endpoint
	if !strings.Contains(config, "Endpoint = 203.0.113.2:51820") {
		t.Errorf("应包含 Endpoint")
	}

	// 应包含 PersistentKeepalive
	if !strings.Contains(config, "PersistentKeepalive = 25") {
		t.Errorf("应包含 PersistentKeepalive")
	}

	// 只能有一个 [Peer] 段
	peerCount := strings.Count(config, "[Peer]")
	if peerCount != 1 {
		t.Errorf("per-peer 模式应只有 1 个 [Peer] 段，实际 %d", peerCount)
	}
}

func TestRenderPerPeerWireGuardConfig_NoEndpoint(t *testing.T) {
	node := &model.Node{
		ID:         "node-2",
		Name:       "beta",
		OverlayIP:  "10.11.0.2",
		ListenPort: 51821,
	}

	peer := compiler.PeerInfo{
		NodeID:          "node-1",
		NodeName:        "alpha",
		PublicKey:        "peer-pubkey-fake",
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
		t.Fatalf("渲染失败: %v", err)
	}

	// 不应包含 Endpoint
	if strings.Contains(config, "Endpoint") {
		t.Errorf("无 endpoint 时不应出现 Endpoint 行")
	}
}

func TestRenderPerPeerWireGuardConfig_WithMTU(t *testing.T) {
	node := &model.Node{
		ID:         "node-1",
		Name:       "alpha",
		OverlayIP:  "10.11.0.1",
		ListenPort: 51820,
		MTU:        1280,
	}

	peer := compiler.PeerInfo{
		NodeID:         "node-2",
		NodeName:       "beta",
		PublicKey:       "peer-pubkey",
		AllowedIPs:     []string{"0.0.0.0/0", "::/0"},
		InterfaceName:  "wg-beta",
		ListenPort:     51820,
		LocalTransitIP: "10.10.0.1",
		LocalLinkLocal: "fe80::1",
	}

	keys := compiler.KeyPair{PrivateKey: "privkey", PublicKey: "pubkey"}

	config, err := RenderPerPeerWireGuardConfig(node, peer, keys)
	if err != nil {
		t.Fatalf("渲染失败: %v", err)
	}

	if !strings.Contains(config, "MTU = 1280") {
		t.Errorf("应包含 MTU = 1280")
	}
}

func TestRenderAllWireGuardConfigs_PerPeer(t *testing.T) {
	topo := &model.Topology{
		Domains: []model.Domain{{
			ID: "domain-1", Name: "test", CIDR: "10.11.0.0/24",
			RoutingMode: "babel",
		}},
		Nodes: []model.Node{
			{ID: "n1", Name: "alpha", Role: "router", DomainID: "domain-1", OverlayIP: "10.11.0.1", ListenPort: 51820},
			{ID: "n2", Name: "beta", Role: "router", DomainID: "domain-1", OverlayIP: "10.11.0.2", ListenPort: 51820},
		},
	}

	peerMap := map[string][]compiler.PeerInfo{
		"n1": {{
			NodeID: "n2", NodeName: "beta", PublicKey: "pub-n2",
			InterfaceName: "wg-beta", ListenPort: 51820,
			AllowedIPs: []string{"0.0.0.0/0", "::/0"},
			LocalTransitIP: "10.10.0.1", RemoteTransitIP: "10.10.0.2",
			LocalLinkLocal: "fe80::1", RemoteLinkLocal: "fe80::2",
		}},
		"n2": {{
			NodeID: "n1", NodeName: "alpha", PublicKey: "pub-n1",
			InterfaceName: "wg-alpha", ListenPort: 51820,
			AllowedIPs: []string{"0.0.0.0/0", "::/0"},
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
		t.Fatalf("渲染失败: %v", err)
	}

	// 应有 2 个配置文件（每个节点各一个 per-peer 接口）
	if len(configs) != 2 {
		t.Errorf("应有 2 个配置，实际 %d", len(configs))
	}

	// key 格式应为 "nodeID:interfaceName"
	if _, ok := configs["n1:wg-beta"]; !ok {
		t.Errorf("应有 n1:wg-beta 配置")
	}
	if _, ok := configs["n2:wg-alpha"]; !ok {
		t.Errorf("应有 n2:wg-alpha 配置")
	}
}
