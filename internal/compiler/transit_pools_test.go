package compiler

import (
	"testing"

	"github.com/kunorikiku/yet-another-overlay-generator/internal/apierr"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/model"
)

// 本文件覆盖 Phase A（per-CIDR transit pools）的三处修复：
//   - D12：每个 transit 地址池按「解析后的 CIDR」独立计数，互不串号。
//   - D48：分配绝不命中网络地址或广播地址，且越界时返回干净的耗尽错误。
//   - D70：link-local 以十六进制渲染（fe80::b，而非把十进制 11 当地址）。

// transitPoolNode 是构造测试节点的小助手，统一填好 router 能力与 overlay IP。
func transitPoolNode(id, domainID, overlayIP string) model.Node {
	return model.Node{
		ID: id, Name: id, Hostname: id + ".example.com",
		Role: "router", DomainID: domainID,
		OverlayIP:    overlayIP,
		Capabilities: model.NodeCapabilities{CanAcceptInbound: true, CanForward: true, HasPublicIP: true},
	}
}

// TestPerCIDRTransitPools_IndependentAllocation 验证 D12：两个域使用不同的 transit_cidr 时，
// 各自的地址池独立从 index 0 开始消费。即便域 A 已消耗 10 个 pair index，域 B 的 /30
// 自定义池仍能容下它唯一的一对（10.20.0.1 / 10.20.0.2）。
//
// 在修复前的单一全局计数器下，域 B 的边会落到 >=10 的全局 index，对 /30 而言远超池容量
// 而报「地址池已耗尽」——这正是本用例要防止的回归。
func TestPerCIDRTransitPools_IndependentAllocation(t *testing.T) {
	// 域 A：1 个 hub + 10 个 spoke 的星型，产生 10 条边 → 域 A 池消耗 index 0..9。
	const spokeCount = 10
	nodes := []model.Node{
		transitPoolNode("a-hub", "domain-a", "10.11.0.1"),
	}
	keys := map[string]KeyPair{
		"a-hub": {PrivateKey: "privkey-a-hub-fake", PublicKey: "pubkey-a-hub-fake"},
	}
	var edges []model.Edge
	for i := 0; i < spokeCount; i++ {
		spokeID := "a-spoke-" + string(rune('a'+i))
		nodes = append(nodes, transitPoolNode(spokeID, "domain-a", "10.11.0."+itoaTest(i+2)))
		keys[spokeID] = KeyPair{PrivateKey: "privkey-" + spokeID + "-fake", PublicKey: "pubkey-" + spokeID + "-fake"}
		edges = append(edges, model.Edge{
			ID: "e-a-" + spokeID, FromNodeID: "a-hub", ToNodeID: spokeID,
			Type: "direct", Transport: "udp", IsEnabled: true,
		})
	}

	// 域 B：单条边，from 节点在域 B，域 B 的 transit_cidr 是 /30（恰好一对可用主机）。
	nodes = append(nodes,
		transitPoolNode("b-one", "domain-b", "10.12.0.1"),
		transitPoolNode("b-two", "domain-b", "10.12.0.2"),
	)
	keys["b-one"] = KeyPair{PrivateKey: "privkey-b-one-fake", PublicKey: "pubkey-b-one-fake"}
	keys["b-two"] = KeyPair{PrivateKey: "privkey-b-two-fake", PublicKey: "pubkey-b-two-fake"}
	// 把域 B 的边排在所有域 A 边之后，确保「若仍是全局计数器」时它会拿到高位 index。
	edges = append(edges, model.Edge{
		ID: "e-b", FromNodeID: "b-one", ToNodeID: "b-two",
		Type: "direct", Transport: "udp", IsEnabled: true,
	})

	topo := &model.Topology{
		Project: model.Project{ID: "transit-pools-001", Name: "Transit Pools"},
		Domains: []model.Domain{
			{ID: "domain-a", Name: "alpha", CIDR: "10.11.0.0/24", AllocationMode: "auto", RoutingMode: "babel"},
			// transit 留空 → 解析为默认 10.10.0.0/24（与域 B 的 /30 互不串号）。
			{ID: "domain-b", Name: "beta", CIDR: "10.12.0.0/24", AllocationMode: "auto", RoutingMode: "babel",
				TransitCIDR: "10.20.0.0/30"},
		},
		Nodes: nodes,
		Edges: edges,
	}

	_, allocations, err := DerivePeers(topo, keys)
	if err != nil {
		t.Fatalf("域 B 的 /30 池独立计数应能容下其唯一一对，但 DerivePeers 报错: %v", err)
	}

	bAlloc := allocations["b-one->b-two"]
	if bAlloc == nil {
		t.Fatalf("应为 b-one->b-two 生成 pairAllocation")
	}
	// 域 B 池从 index 0 开始：/30 的可用主机恰是 .1 与 .2。
	wantPair := map[string]bool{"10.20.0.1": true, "10.20.0.2": true}
	if !wantPair[bAlloc.localTransit] || !wantPair[bAlloc.remoteTransit] || bAlloc.localTransit == bAlloc.remoteTransit {
		t.Errorf("域 B（10.20.0.0/30）应分配 {10.20.0.1, 10.20.0.2}，实际 local=%s remote=%s",
			bAlloc.localTransit, bAlloc.remoteTransit)
	}

	// 域 A 池也应从 index 0 开始（默认 10.10.0.0/24）：第一条边即 .1/.2，不受域 B 影响。
	aAlloc := allocations["a-hub->a-spoke-a"]
	if aAlloc == nil {
		t.Fatalf("应为 a-hub->a-spoke-a 生成 pairAllocation")
	}
	wantAPair := map[string]bool{"10.10.0.1": true, "10.10.0.2": true}
	if !wantAPair[aAlloc.localTransit] || !wantAPair[aAlloc.remoteTransit] {
		t.Errorf("域 A（默认 10.10.0.0/24）首条边应分配 {10.10.0.1, 10.10.0.2}，实际 local=%s remote=%s",
			aAlloc.localTransit, aAlloc.remoteTransit)
	}
}

// TestPerCIDRTransitPools_SmallPoolExhausts 验证 D48/D12：一个 /30 池只能容下一对，
// 第二条同池链路必须以干净的耗尽错误失败（而非静默吐出广播地址 .3）。
func TestPerCIDRTransitPools_SmallPoolExhausts(t *testing.T) {
	nodes := []model.Node{
		transitPoolNode("n1", "domain-x", "10.40.0.1"),
		transitPoolNode("n2", "domain-x", "10.40.0.2"),
		transitPoolNode("n3", "domain-x", "10.40.0.3"),
	}
	keys := map[string]KeyPair{
		"n1": {PrivateKey: "pk-n1", PublicKey: "pub-n1"},
		"n2": {PrivateKey: "pk-n2", PublicKey: "pub-n2"},
		"n3": {PrivateKey: "pk-n3", PublicKey: "pub-n3"},
	}
	topo := &model.Topology{
		Project: model.Project{ID: "transit-pools-002", Name: "Small Pool"},
		Domains: []model.Domain{
			{ID: "domain-x", Name: "x", CIDR: "10.40.0.0/24", AllocationMode: "auto", RoutingMode: "babel",
				TransitCIDR: "10.20.0.0/30"},
		},
		Nodes: nodes,
		Edges: []model.Edge{
			// 两条不同链路都从 /30 池取地址：第一条占 index 0（.1/.2），第二条需 index 1 → 广播 .3。
			{ID: "e1", FromNodeID: "n1", ToNodeID: "n2", Type: "direct", Transport: "udp", IsEnabled: true},
			{ID: "e2", FromNodeID: "n1", ToNodeID: "n3", Type: "direct", Transport: "udp", IsEnabled: true},
		},
	}

	_, _, err := DerivePeers(topo, keys)
	if err == nil {
		t.Fatalf("a /30 pool holds only one pair; the second link should exhaust it, but got nil")
	}
	if !apierr.HasCode(err, apierr.CodeTransitPoolExhausted) {
		t.Errorf("expected a transit-pool-exhausted coded error, got: %q", err.Error())
	}
}

// TestAllocateTransitPair_NeverNetworkOrBroadcast 验证 D48：对 /29 池逐一走完所有 index，
// 任何成功分配的地址都不得是网络地址（.0）或广播地址（.7）；越界后返回耗尽错误。
// /29（10.30.0.0/29）的可用主机是 .1..6，故应得到 3 对（index 0/1/2），index 3 起耗尽。
func TestAllocateTransitPair_NeverNetworkOrBroadcast(t *testing.T) {
	const cidr = "10.30.0.0/29"
	const networkAddr = "10.30.0.0"
	const broadcastAddr = "10.30.0.7"

	successCount := 0
	exhausted := false
	// 走比池容量更多的 index，确保覆盖到耗尽边界与（可能的）越界回绕。
	for index := 0; index < 16; index++ {
		ip1, ip2, err := allocateTransitPair(index, cidr)
		if err != nil {
			exhausted = true
			continue
		}
		successCount++
		for _, ip := range []string{ip1, ip2} {
			if ip == networkAddr {
				t.Errorf("index %d 分配出网络地址 %s，绝不允许", index, networkAddr)
			}
			if ip == broadcastAddr {
				t.Errorf("index %d 分配出广播地址 %s，绝不允许", index, broadcastAddr)
			}
		}
		if ip1 == ip2 {
			t.Errorf("index %d 的一对地址不应相同（%s）", index, ip1)
		}
	}

	if successCount != 3 {
		t.Errorf("/29 池可用主机 .1..6，应恰好分配 3 对，实际 %d 对", successCount)
	}
	if !exhausted {
		t.Errorf("超过池容量的 index 应返回耗尽错误，但从未观察到")
	}

	// 显式断言 index 0 的具体地址，钉住「从 .1 起、跳过网络地址」的语义。
	ip1, ip2, err := allocateTransitPair(0, cidr)
	if err != nil {
		t.Fatalf("index 0 应成功，实际报错: %v", err)
	}
	if ip1 != "10.30.0.1" || ip2 != "10.30.0.2" {
		t.Errorf("index 0 应为 {10.30.0.1, 10.30.0.2}，实际 {%s, %s}", ip1, ip2)
	}
}

// TestAllocateLinkLocalPair_RendersHex 验证 D70：IPv6 link-local 以十六进制渲染。
// index 5 → base = 2*5+1 = 11 → fe80::b / fe80::c（而非把十进制 11 写成 fe80::11，
// 后者会被解析成 0x11 = 17，破坏文档承诺的连续编号）。
func TestAllocateLinkLocalPair_RendersHex(t *testing.T) {
	local, remote := allocateLinkLocalPair(5)
	if local != "fe80::b" {
		t.Errorf("index 5 的本端 link-local 应为 fe80::b（十六进制），实际 %q", local)
	}
	if remote != "fe80::c" {
		t.Errorf("index 5 的对端 link-local 应为 fe80::c（十六进制），实际 %q", remote)
	}
	// 反向保险：绝不能再出现十进制写法 fe80::11。
	if local == "fe80::11" {
		t.Errorf("index 5 不应渲染成十进制 fe80::11（会被解析成 fe80::17）")
	}

	// 抽查低位 index，确认连续十六进制：index 0 → ::1/::2，index 7 → ::f/::10。
	if l0, r0 := allocateLinkLocalPair(0); l0 != "fe80::1" || r0 != "fe80::2" {
		t.Errorf("index 0 应为 {fe80::1, fe80::2}，实际 {%s, %s}", l0, r0)
	}
	if l7, r7 := allocateLinkLocalPair(7); l7 != "fe80::f" || r7 != "fe80::10" {
		t.Errorf("index 7 应为 {fe80::f, fe80::10}（base=15 → 0xf, 0x10），实际 {%s, %s}", l7, r7)
	}
}
