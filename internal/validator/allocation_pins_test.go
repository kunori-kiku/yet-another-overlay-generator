package validator

import (
	"testing"

	"github.com/kunorikiku/yet-another-overlay-generator/internal/model"
)

// 分配 pin 校验测试（不变式 I7，规则见 docs/spec/compiler/allocation-stability.md「Pin validation」）。
//
// 这些测试都聚焦在 validateAllocationPins 经由 ValidateSemantic 暴露的校验上。pin 按边存储，
// 由该边自身的 from/to 定向：边 A->B 的 PinnedFromPort 是 A 侧端口，反向边 B->A 携带镜像后的同一对值。

// pinnedTopology 在 validTopology 之上为唯一一条链路（node-1 <-> node-2）的正反两条边
// 设置一组「干净的、成对镜像的」pin，作为合法基线供各测试在其上注入单点违例。
//
//	edge-1 (node-1 -> node-2): from=node-1 侧, to=node-2 侧
//	edge-2 (node-2 -> node-1): from=node-2 侧, to=node-1 侧（与 edge-1 镜像）
//
// 约定：node-1 端口 51820 / transit 10.10.0.1 / link-local fe80::1；
//
//	node-2 端口 51820 / transit 10.10.0.2 / link-local fe80::2。
func pinnedTopology() *model.Topology {
	topo := validTopology()

	// node-1 端口可与 node-2 相同：它们是不同节点，各自绑定自身接口。
	const (
		node1Port      = 51820
		node2Port      = 51820
		node1Transit   = "10.10.0.1"
		node2Transit   = "10.10.0.2"
		node1LinkLocal = "fe80::1"
		node2LinkLocal = "fe80::2"
	)

	// edge-1: node-1 -> node-2。from = node-1，to = node-2。
	topo.Edges[0].PinnedFromPort = node1Port
	topo.Edges[0].PinnedToPort = node2Port
	topo.Edges[0].PinnedFromTransitIP = node1Transit
	topo.Edges[0].PinnedToTransitIP = node2Transit
	topo.Edges[0].PinnedFromLinkLocal = node1LinkLocal
	topo.Edges[0].PinnedToLinkLocal = node2LinkLocal

	// edge-2: node-2 -> node-1。from = node-2，to = node-1（镜像）。
	topo.Edges[1].PinnedFromPort = node2Port
	topo.Edges[1].PinnedToPort = node1Port
	topo.Edges[1].PinnedFromTransitIP = node2Transit
	topo.Edges[1].PinnedToTransitIP = node1Transit
	topo.Edges[1].PinnedFromLinkLocal = node2LinkLocal
	topo.Edges[1].PinnedToLinkLocal = node1LinkLocal

	return topo
}

// pinErrorCount 统计落在边 pin 字段或边索引前缀上的错误数量，用于在含有其它无关错误时
// 仍能精确断言「pin 校验」是否触发。
func pinErrorCount(result *ValidationResult) int {
	n := 0
	for _, e := range result.Errors {
		if containsSubstring(e.Field, "edges[") {
			n++
		}
	}
	return n
}

// --- 干净 pin 接受 ---

// TestValidateAllocationPins_CleanPinsAccepted 一组完整、成对、镜像、落在池内的 pin 必须无任何错误。
func TestValidateAllocationPins_CleanPinsAccepted(t *testing.T) {
	topo := pinnedTopology()
	result := ValidateSemantic(topo)
	if !result.IsValid() {
		t.Errorf("干净的成对 pin 应当通过校验，却报了 %d 条错误：", len(result.Errors))
		for _, e := range result.Errors {
			t.Errorf("  %s", e.Error())
		}
	}
}

// TestValidateAllocationPins_NoPinsAccepted 完全无 pin 的拓扑（未钉住，留待 gap-fill）必须通过校验。
func TestValidateAllocationPins_NoPinsAccepted(t *testing.T) {
	topo := validTopology()
	result := ValidateSemantic(topo)
	if !result.IsValid() {
		t.Errorf("无 pin 的拓扑应当通过校验，却报了 %d 条错误：", len(result.Errors))
		for _, e := range result.Errors {
			t.Errorf("  %s", e.Error())
		}
	}
}

// --- 部分 pin（成对完整性）---

// TestValidateAllocationPins_PartialPortPair 仅钉住一端的端口应被拒绝。
func TestValidateAllocationPins_PartialPortPair(t *testing.T) {
	topo := validTopology()
	topo.Edges[0].PinnedFromPort = 51820 // 仅 from 侧，to 侧留空
	result := ValidateSemantic(topo)
	assertHasError(t, result, "edges[0]")
}

// TestValidateAllocationPins_PartialTransitPair 仅钉住一端的 transit IP 应被拒绝。
func TestValidateAllocationPins_PartialTransitPair(t *testing.T) {
	topo := validTopology()
	topo.Edges[0].PinnedFromTransitIP = "10.10.0.1" // 仅 from 侧，to 侧留空
	result := ValidateSemantic(topo)
	assertHasError(t, result, "edges[0]")
}

// TestValidateAllocationPins_PartialLinkLocalPair 仅钉住一端的 link-local 应被拒绝。
func TestValidateAllocationPins_PartialLinkLocalPair(t *testing.T) {
	topo := validTopology()
	topo.Edges[0].PinnedFromLinkLocal = "fe80::1" // 仅 from 侧，to 侧留空
	result := ValidateSemantic(topo)
	assertHasError(t, result, "edges[0]")
}

// --- 端口越界 ---

// TestValidateAllocationPins_PortAboveMax 超过 65535 的端口 pin 应被拒绝。
func TestValidateAllocationPins_PortAboveMax(t *testing.T) {
	topo := pinnedTopology()
	topo.Edges[0].PinnedFromPort = 70000
	topo.Edges[1].PinnedToPort = 70000 // 反向边镜像，保持成对完整以隔离越界这一条违例
	result := ValidateSemantic(topo)
	assertHasError(t, result, "pinned_from_port")
}

// TestValidateAllocationPins_PortBelowBase 低于节点基准 listen_port 的端口 pin 应被拒绝（陈旧基准检测）。
func TestValidateAllocationPins_PortBelowBase(t *testing.T) {
	topo := pinnedTopology()
	// node-1 基准 listen_port = 51820；把 node-1 侧端口钉到基准之下。
	topo.Edges[0].PinnedFromPort = 51000
	topo.Edges[1].PinnedToPort = 51000 // 反向边镜像
	result := ValidateSemantic(topo)
	assertHasError(t, result, "pinned_from_port")
}

// --- transit IP 越池 ---

// TestValidateAllocationPins_TransitOutOfCIDR 不在该边解析出的 transit 池内的 transit IP pin 应被拒绝。
func TestValidateAllocationPins_TransitOutOfCIDR(t *testing.T) {
	topo := pinnedTopology()
	// 域的 transit 池回退默认 10.10.0.0/24；钉一个池外地址。
	topo.Edges[0].PinnedFromTransitIP = "192.168.99.1"
	topo.Edges[1].PinnedToTransitIP = "192.168.99.1" // 反向边镜像
	result := ValidateSemantic(topo)
	assertHasError(t, result, "pinned_from_transit_ip")
}

// TestValidateAllocationPins_TransitNarrowedPoolStale 当 transit_cidr 被收窄后，原本合法的 pin 变为池外，应被拒绝。
func TestValidateAllocationPins_TransitNarrowedPoolStale(t *testing.T) {
	topo := pinnedTopology()
	// 把域的 transit 池收窄到 10.10.0.0/30（可用主机 10.10.0.1、10.10.0.2）。
	topo.Domains[0].TransitCIDR = "10.10.0.0/30"
	// 把 node-1 侧 transit 钉到收窄后池外的 10.10.0.5。
	topo.Edges[0].PinnedFromTransitIP = "10.10.0.5"
	topo.Edges[1].PinnedToTransitIP = "10.10.0.5" // 反向边镜像
	result := ValidateSemantic(topo)
	assertHasError(t, result, "pinned_from_transit_ip")
}

// --- 跨链路重复占用 ---

// threeNodeTwoLinkTopology 构造 node-1 与 node-2、node-1 与 node-3 两条独立链路，
// 共享 node-1，用于检测「同一节点端口」「同一 transit IP」「同一 link-local」被两条不同链路重复占用。
func threeNodeTwoLinkTopology() *model.Topology {
	topo := pinnedTopology() // node-1 <-> node-2 已是干净 pin 的链路

	// 追加 node-3 并把 validTopology 中冗余的反向边替换为指向 node-3 的新链路。
	topo.Nodes = append(topo.Nodes, model.Node{
		ID:       "node-3",
		Name:     "node-gamma",
		Hostname: "gamma.example.com",
		Platform: "debian",
		Role:     "router",
		DomainID: "domain-1",
		Capabilities: model.NodeCapabilities{
			CanAcceptInbound: true,
			CanForward:       true,
			HasPublicIP:      true,
		},
	})

	// node-1 -> node-3 的新链路（一条边即可，干净且与 node-1<->node-2 链路不冲突）。
	topo.Edges = append(topo.Edges, model.Edge{
		ID:                  "edge-3",
		FromNodeID:          "node-1",
		ToNodeID:            "node-3",
		Type:                "direct",
		EndpointHost:        "203.0.113.3",
		Transport:           "udp",
		IsEnabled:           true,
		PinnedFromPort:      51821, // node-1 在该链路上的另一个接口端口（与 51820 不同）
		PinnedToPort:        51820, // node-3 的端口
		PinnedFromTransitIP: "10.10.0.3",
		PinnedToTransitIP:   "10.10.0.4",
		PinnedFromLinkLocal: "fe80::3",
		PinnedToLinkLocal:   "fe80::4",
	})

	return topo
}

// TestValidateAllocationPins_ThreeNodeTwoLinkBaselineClean 两条独立链路、各自干净 pin 的基线必须通过校验。
func TestValidateAllocationPins_ThreeNodeTwoLinkBaselineClean(t *testing.T) {
	topo := threeNodeTwoLinkTopology()
	result := ValidateSemantic(topo)
	if pinErrorCount(result) != 0 {
		t.Errorf("两条独立链路的干净 pin 应当无 pin 错误，却报了 %d 条：%v", pinErrorCount(result), result.Errors)
	}
}

// TestValidateAllocationPins_DuplicatePortOnNodeAcrossLinks node-1 在两条不同链路上钉住相同端口应被拒绝。
func TestValidateAllocationPins_DuplicatePortOnNodeAcrossLinks(t *testing.T) {
	topo := threeNodeTwoLinkTopology()
	// node-1<->node-2 链路里 node-1 端口为 51820；让 node-1<->node-3 链路里 node-1 端口也为 51820。
	topo.Edges[2].PinnedFromPort = 51820
	result := ValidateSemantic(topo)
	if pinErrorCount(result) == 0 {
		t.Errorf("同一节点在两条不同链路上钉住相同端口应当报错，却无 pin 错误：%v", result.Errors)
	}
}

// TestValidateAllocationPins_DuplicateTransitIPAcrossLinks 两条不同链路钉住相同 transit IP 应被拒绝。
func TestValidateAllocationPins_DuplicateTransitIPAcrossLinks(t *testing.T) {
	topo := threeNodeTwoLinkTopology()
	// node-1<->node-2 链路占用 10.10.0.1；让 node-1<->node-3 链路也占用 10.10.0.1。
	topo.Edges[2].PinnedFromTransitIP = "10.10.0.1"
	result := ValidateSemantic(topo)
	if pinErrorCount(result) == 0 {
		t.Errorf("两条不同链路钉住相同 transit IP 应当报错，却无 pin 错误：%v", result.Errors)
	}
}

// TestValidateAllocationPins_DuplicateLinkLocalAcrossLinks 两条不同链路钉住相同 link-local 应被拒绝。
func TestValidateAllocationPins_DuplicateLinkLocalAcrossLinks(t *testing.T) {
	topo := threeNodeTwoLinkTopology()
	// node-1<->node-2 链路占用 fe80::1；让 node-1<->node-3 链路也占用 fe80::1。
	topo.Edges[2].PinnedFromLinkLocal = "fe80::1"
	result := ValidateSemantic(topo)
	if pinErrorCount(result) == 0 {
		t.Errorf("两条不同链路钉住相同 link-local 应当报错，却无 pin 错误：%v", result.Errors)
	}
}

// TestValidateAllocationPins_ReverseEdgeNotDuplicate 同一链路的正反两条边携带镜像 pin，不得被误判为重复占用。
func TestValidateAllocationPins_ReverseEdgeNotDuplicate(t *testing.T) {
	// pinnedTopology 的 edge-1/edge-2 即同一链路的正反两条边，已镜像。
	topo := pinnedTopology()
	result := ValidateSemantic(topo)
	if pinErrorCount(result) != 0 {
		t.Errorf("同一链路的正反边（镜像 pin）不应被判为重复占用，却报了 %d 条 pin 错误：%v", pinErrorCount(result), result.Errors)
	}
}

// --- client 边的 pin ---

// clientEdgeTopology 构造一个 client 节点经一条边连到 router，便于测试 client 边上的 pin 处理。
func clientEdgeTopology() *model.Topology {
	topo := validTopology()

	// 把 node-2 改为 client，并去掉以 client 为目标的反向边（client 不接受入站）。
	topo.Nodes[1].Role = "client"
	// 仅保留 node-2(client) -> node-1(router) 这一条出站边，并补上 client 所需的 endpoint_host。
	topo.Edges = []model.Edge{
		{
			ID:           "edge-1",
			FromNodeID:   "node-2",
			ToNodeID:     "node-1",
			Type:         "direct",
			EndpointHost: "203.0.113.1",
			Transport:    "udp",
			IsEnabled:    true,
		},
	}
	return topo
}

// TestValidateAllocationPins_ClientEdgePortPinRejected client 边携带端口 pin 应报错（client 用单一 wg0，无 per-peer 端口）。
func TestValidateAllocationPins_ClientEdgePortPinRejected(t *testing.T) {
	topo := clientEdgeTopology()
	topo.Edges[0].PinnedFromPort = 51820
	topo.Edges[0].PinnedToPort = 51820
	result := ValidateSemantic(topo)
	if pinErrorCount(result) == 0 {
		t.Errorf("client 边携带端口 pin 应当报错，却无 pin 错误：%v", result.Errors)
	}
}

// TestValidateAllocationPins_ClientEdgeResourcePinWarns client 边携带 transit/link-local pin 应告警（将被忽略），而非报错。
func TestValidateAllocationPins_ClientEdgeResourcePinWarns(t *testing.T) {
	topo := clientEdgeTopology()
	topo.Edges[0].PinnedFromTransitIP = "10.10.0.1"
	topo.Edges[0].PinnedToTransitIP = "10.10.0.2"
	result := ValidateSemantic(topo)

	// 不应因 transit/link-local pin 在 client 边上而报 pin 错误。
	if pinErrorCount(result) != 0 {
		t.Errorf("client 边上的 transit/link-local pin 应当只告警不报错，却报了 pin 错误：%v", result.Errors)
	}
	assertHasWarning(t, result, "edges[0]")
}
