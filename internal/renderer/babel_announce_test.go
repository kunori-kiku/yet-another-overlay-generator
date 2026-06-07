package renderer

import (
	"strings"
	"testing"

	"github.com/kunorikiku/yet-another-overlay-generator/internal/compiler"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/model"
)

// 本文件是 Plan 6（routing-mode + Babel 正确性）中 Babel 渲染分区的「黄金门禁」。
//
// 受保护不变量（outline principle）：self-/32 宣告路径
// （dummy0 上的 overlay IP，配合 `redistribute local`）是当前唯一在已部署集群上
// 真正生效的宣告机制，它在本次改动前后必须逐字节保持一致。下方的
// TestBabelAnnounce_GoldenSelf32_ByteIdentical 用从模板逐字推导出的字面量
// 把 self-/32 行钉死；任何改动都不得让这些行发生变化。

// representativeBabelTopology 构造一个具代表性的 4 节点拓扑，覆盖本计划关心的全部宣告类别：
//   - peer    ：仅 self-/32
//   - router  ：self-/32 + domain CIDR 聚合 + extra_prefixes
//   - relay   ：self-/32 + domain CIDR 聚合
//   - gateway ：self-/32 + domain CIDR 聚合 + extra_prefixes + 默认路由 0.0.0.0/0
//
// 返回各角色节点以及一份可直接喂给 RenderBabelConfig 的 domain。
func representativeBabelTopology() (peerNode, routerNode, relayNode, gatewayNode *model.Node, domain *model.Domain) {
	domain = &model.Domain{
		ID:          "domain-1",
		Name:        "test",
		CIDR:        "10.11.0.0/24",
		RoutingMode: "babel",
	}

	peerNode = &model.Node{
		ID:        "node-peer",
		Name:      "peernode",
		Role:      "peer",
		DomainID:  "domain-1",
		OverlayIP: "10.11.0.2",
	}

	routerNode = &model.Node{
		ID:        "node-router",
		Name:      "routernode",
		Role:      "router",
		DomainID:  "domain-1",
		OverlayIP: "10.11.0.1",
		Capabilities: model.NodeCapabilities{
			CanForward: true,
		},
		ExtraPrefixes: []string{"192.168.50.0/24"},
	}

	relayNode = &model.Node{
		ID:        "node-relay",
		Name:      "relaynode",
		Role:      "relay",
		DomainID:  "domain-1",
		OverlayIP: "10.11.0.3",
		Capabilities: model.NodeCapabilities{
			CanForward:       true,
			CanRelay:         true,
			CanAcceptInbound: true,
		},
	}

	gatewayNode = &model.Node{
		ID:        "node-gateway",
		Name:      "gatewaynode",
		Role:      "gateway",
		DomainID:  "domain-1",
		OverlayIP: "10.11.0.4",
		Capabilities: model.NodeCapabilities{
			CanForward:  true,
			HasPublicIP: true,
		},
		ExtraPrefixes: []string{"172.16.0.0/16"},
	}

	return peerNode, routerNode, relayNode, gatewayNode, domain
}

// containsLine 判断 config 是否包含与 want 逐字节相等的某一行。
func containsLine(config, want string) bool {
	for _, line := range strings.Split(config, "\n") {
		if line == want {
			return true
		}
	}
	return false
}

// TestBabelAnnounce_GoldenSelf32_ByteIdentical 是受保护路径的黄金门禁。
//
// 下面的期望行是从 babelConfigTemplate 逐字推导出来的字面量：当
// RedistributePrefixes（self-/32 走 `redistribute local`）渲染 overlay IP 时，
// 模板产生的精确字符串是 `redistribute local ip <OverlayIP>/32 allow`。
// 改动后这些行必须仍然存在且逐字节相同，否则说明 self-/32 部署路径发生了回归。
func TestBabelAnnounce_GoldenSelf32_ByteIdentical(t *testing.T) {
	peerNode, routerNode, relayNode, gatewayNode, domain := representativeBabelTopology()

	cases := []struct {
		name     string
		node     *model.Node
		peers    []compiler.PeerInfo
		wantSelf string
	}{
		{
			name:     "peer",
			node:     peerNode,
			peers:    []compiler.PeerInfo{{NodeID: "node-router", NodeName: "routernode", InterfaceName: "wg-routernode"}},
			wantSelf: "redistribute local ip 10.11.0.2/32 allow",
		},
		{
			name:     "router",
			node:     routerNode,
			peers:    []compiler.PeerInfo{{NodeID: "node-peer", NodeName: "peernode", InterfaceName: "wg-peernode"}},
			wantSelf: "redistribute local ip 10.11.0.1/32 allow",
		},
		{
			name:     "relay",
			node:     relayNode,
			peers:    []compiler.PeerInfo{{NodeID: "node-router", NodeName: "routernode", InterfaceName: "wg-routernode"}},
			wantSelf: "redistribute local ip 10.11.0.3/32 allow",
		},
		{
			name:     "gateway",
			node:     gatewayNode,
			peers:    []compiler.PeerInfo{{NodeID: "node-router", NodeName: "routernode", InterfaceName: "wg-routernode"}},
			wantSelf: "redistribute local ip 10.11.0.4/32 allow",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			config, err := RenderBabelConfig(tc.node, tc.peers, domain)
			if err != nil {
				t.Fatalf("渲染 Babel 配置失败: %v", err)
			}
			if !containsLine(config, tc.wantSelf) {
				t.Errorf("self-/32 宣告行发生回归。\n期望逐字节包含: %q\n实际配置:\n%s", tc.wantSelf, config)
			}
		})
	}
}

// TestBabelAnnounce_GatewayDefaultRoute_NonLocal 验证 D40：网关默认路由必须以
// 非 local 形式渲染（`redistribute ip 0.0.0.0/0 allow`），以便 babeld 匹配
// 节点上真实存在的内核默认路由。它绝不能再以 `redistribute local` 形式出现。
func TestBabelAnnounce_GatewayDefaultRoute_NonLocal(t *testing.T) {
	_, _, _, gatewayNode, domain := representativeBabelTopology()

	peers := []compiler.PeerInfo{{NodeID: "node-router", NodeName: "routernode", InterfaceName: "wg-routernode"}}
	config, err := RenderBabelConfig(gatewayNode, peers, domain)
	if err != nil {
		t.Fatalf("渲染失败: %v", err)
	}

	want := "redistribute ip 0.0.0.0/0 allow"
	if !containsLine(config, want) {
		t.Errorf("网关默认路由应以非 local 形式渲染。\n期望逐字节包含: %q\n实际配置:\n%s", want, config)
	}

	// 不得再以 local 形式渲染默认路由（会匹配不到任何连接路由，导致出口静默失效）。
	if containsLine(config, "redistribute local ip 0.0.0.0/0 allow") {
		t.Errorf("默认路由不应再使用 `redistribute local`（D40），实际配置:\n%s", config)
	}
}

// TestBabelAnnounce_ExtraPrefixes_NonLocal 验证 D41 的 extra_prefixes 部分：
// extra_prefixes 对应真实的内核连接路由（节点真实 LAN 网段），因此必须以
// 非 local 形式 `redistribute ip <prefix> allow` 渲染。
func TestBabelAnnounce_ExtraPrefixes_NonLocal(t *testing.T) {
	_, routerNode, _, _, domain := representativeBabelTopology()

	peers := []compiler.PeerInfo{{NodeID: "node-peer", NodeName: "peernode", InterfaceName: "wg-peernode"}}
	config, err := RenderBabelConfig(routerNode, peers, domain)
	if err != nil {
		t.Fatalf("渲染失败: %v", err)
	}

	want := "redistribute ip 192.168.50.0/24 allow"
	if !containsLine(config, want) {
		t.Errorf("extra_prefixes 应以非 local 形式渲染。\n期望逐字节包含: %q\n实际配置:\n%s", want, config)
	}

	if containsLine(config, "redistribute local ip 192.168.50.0/24 allow") {
		t.Errorf("extra_prefixes 不应再使用 `redistribute local`（D41），实际配置:\n%s", config)
	}
}

// TestBabelAnnounce_DomainCIDR_DeferredNoOp 验证 plan-6.5 的延期决策：domain CIDR
// 聚合在任何节点上都没有对应内核路由，本计划暂不修复，保持其当前的 `redistribute local`
// 无操作行不变（仅加注释说明延期），不得误改成非 local 形式。
func TestBabelAnnounce_DomainCIDR_DeferredNoOp(t *testing.T) {
	_, routerNode, _, _, domain := representativeBabelTopology()

	peers := []compiler.PeerInfo{{NodeID: "node-peer", NodeName: "peernode", InterfaceName: "wg-peernode"}}
	config, err := RenderBabelConfig(routerNode, peers, domain)
	if err != nil {
		t.Fatalf("渲染失败: %v", err)
	}

	want := "redistribute local ip 10.11.0.0/24 allow"
	if !containsLine(config, want) {
		t.Errorf("domain CIDR 聚合应保持当前 `redistribute local` 无操作行不变（plan-6.5 延期）。\n期望逐字节包含: %q\n实际配置:\n%s", want, config)
	}
}

// TestBabelAnnounce_ClientPeerInterfaceAbsent 验证 D73：连接 client 的隧道
// （IsClientPeer=true）绝不能出现在 babeld 接口声明里——client 不跑 babeld，
// 否则 router 会永远向该隧道单播 hello。client 的可达性由 client-/32 重分发承载。
func TestBabelAnnounce_ClientPeerInterfaceAbsent(t *testing.T) {
	_, routerNode, _, _, domain := representativeBabelTopology()

	peers := []compiler.PeerInfo{
		{NodeID: "node-peer", NodeName: "peernode", InterfaceName: "wg-peernode"},
		{
			NodeID:          "node-client",
			NodeName:        "clientnode",
			InterfaceName:   "wg-clientnode",
			IsClientPeer:    true,
			ClientOverlayIP: "10.11.0.9",
		},
	}

	config, err := RenderBabelConfig(routerNode, peers, domain)
	if err != nil {
		t.Fatalf("渲染失败: %v", err)
	}

	// 普通 peer 接口必须存在
	if !strings.Contains(config, "interface wg-peernode") {
		t.Errorf("普通 peer 接口应被声明，实际配置:\n%s", config)
	}

	// client 隧道接口绝不能出现在接口声明里
	if strings.Contains(config, "interface wg-clientnode") {
		t.Errorf("client 隧道不应被声明为 babel 接口（D73），实际配置:\n%s", config)
	}

	// client 的可达性仍由 client-/32 重分发承载（client-/32 走 local）
	if !containsLine(config, "redistribute local ip 10.11.0.9/32 allow") {
		t.Errorf("client 可达性应由 client-/32 重分发承载，实际配置:\n%s", config)
	}
}

// TestBabelAnnounce_LinkCostOverride_Rxcost 验证 D63：边上的 LinkCost 覆盖角色预设默认
// cost，并反映在接口的 rxcost 上；未设置（0）时回退到角色预设的 DefaultCost。
func TestBabelAnnounce_LinkCostOverride_Rxcost(t *testing.T) {
	_, routerNode, _, _, domain := representativeBabelTopology()

	peers := []compiler.PeerInfo{
		{NodeID: "node-peer", NodeName: "peernode", InterfaceName: "wg-peernode", LinkCost: 250},
	}

	config, err := RenderBabelConfig(routerNode, peers, domain)
	if err != nil {
		t.Fatalf("渲染失败: %v", err)
	}

	if !strings.Contains(config, "rxcost 250") {
		t.Errorf("LinkCost 覆盖应反映在 rxcost 上（期望 rxcost 250），实际配置:\n%s", config)
	}
}

// TestBabelAnnounce_RelayPresetDefaultCost 验证当边未设置 LinkCost 时，relay 角色
// 回退到角色预设 DefaultCost（96），渲染为 rxcost 96。
func TestBabelAnnounce_RelayPresetDefaultCost(t *testing.T) {
	_, _, relayNode, _, domain := representativeBabelTopology()

	peers := []compiler.PeerInfo{
		{NodeID: "node-router", NodeName: "routernode", InterfaceName: "wg-routernode"},
	}

	config, err := RenderBabelConfig(relayNode, peers, domain)
	if err != nil {
		t.Fatalf("渲染失败: %v", err)
	}

	if !strings.Contains(config, "rxcost 96") {
		t.Errorf("relay 未设置 LinkCost 时应回退到预设 DefaultCost 96，实际配置:\n%s", config)
	}
}

// TestBabelAnnounce_ParallelLinks_TwoStanzasDistinctCost 验证并行链路的 Babel 渲染契约
// （docs/spec/artifacts/babel.md）：一个节点向同一对端同时拥有 primary 与 backup 两条链路时，
// 必须渲染出两条接口声明（interface 行），接口名互异；primary（LinkCost 0，router 预设 DefaultCost
// 也为 0）省略 rxcost token，backup（LinkCost 384）带 `rxcost 384`，两者之间形成故障切换所需的
// cost 落差。
func TestBabelAnnounce_ParallelLinks_TwoStanzasDistinctCost(t *testing.T) {
	_, routerNode, _, _, domain := representativeBabelTopology()

	const (
		primaryIface = "wg-peernode"  // primary class 接口名（== naming.WgInterfaceName(remote)）
		backupIface  = "wg-peernod1a" // backup 的 edge-aware 接口名（形态不同即可，仅需与 primary 互异）
	)

	peers := []compiler.PeerInfo{
		// 同一对端（node-peer）的两条并行链路：primary 无 cost，backup cost 384。
		{NodeID: "node-peer", NodeName: "peernode", InterfaceName: primaryIface, LinkCost: 0},
		{NodeID: "node-peer", NodeName: "peernode", InterfaceName: backupIface, LinkCost: 384},
	}

	config, err := RenderBabelConfig(routerNode, peers, domain)
	if err != nil {
		t.Fatalf("渲染失败: %v", err)
	}

	// 两条接口声明都必须出现，且接口名互异。
	if !strings.Contains(config, "interface "+primaryIface) {
		t.Errorf("应渲染 primary 接口声明 %q，实际配置:\n%s", "interface "+primaryIface, config)
	}
	if !strings.Contains(config, "interface "+backupIface) {
		t.Errorf("应渲染 backup 接口声明 %q，实际配置:\n%s", "interface "+backupIface, config)
	}

	// backup 接口必须带 `rxcost 384`。
	if !strings.Contains(config, "rxcost 384") {
		t.Errorf("backup 链路应渲染 `rxcost 384`，实际配置:\n%s", config)
	}

	// primary 接口必须在「cost 0 省略 rxcost」路径上：其接口行不得携带任何 rxcost token。
	// 逐行定位 primary 的接口行（router 预设 DefaultCost 为 0，故 LinkCost 0 时不写 rxcost）。
	var primaryLine string
	for _, line := range strings.Split(config, "\n") {
		if strings.HasPrefix(line, "interface "+primaryIface+" ") || line == "interface "+primaryIface {
			primaryLine = line
			break
		}
	}
	if primaryLine == "" {
		t.Fatalf("未能定位 primary 接口行，实际配置:\n%s", config)
	}
	if strings.Contains(primaryLine, "rxcost") {
		t.Errorf("primary 链路（cost 0）的接口行不应带 rxcost token，实际行: %q", primaryLine)
	}
}

// TestBabelAnnounce_PresetTimersPresent 验证 D78：hello-interval / update-interval
// 来自角色预设（默认 4 / 16），且 local-port 来自命名常量（默认 33123）。
func TestBabelAnnounce_PresetTimersPresent(t *testing.T) {
	_, routerNode, _, _, domain := representativeBabelTopology()

	peers := []compiler.PeerInfo{
		{NodeID: "node-peer", NodeName: "peernode", InterfaceName: "wg-peernode"},
	}

	config, err := RenderBabelConfig(routerNode, peers, domain)
	if err != nil {
		t.Fatalf("渲染失败: %v", err)
	}

	if !strings.Contains(config, "hello-interval 4") {
		t.Errorf("应包含来自预设的 hello-interval 4，实际配置:\n%s", config)
	}
	if !strings.Contains(config, "update-interval 16") {
		t.Errorf("应包含来自预设的 update-interval 16，实际配置:\n%s", config)
	}
	if !strings.Contains(config, "local-port 33123") {
		t.Errorf("应包含来自命名常量的 local-port 33123，实际配置:\n%s", config)
	}
}
