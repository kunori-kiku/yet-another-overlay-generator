package compiler

import (
	"testing"

	"github.com/kunorikiku/yet-another-overlay-generator/internal/model"
)

// 本文件是 Plan 7（粘性 pin 分配，增量扩展特性）的 I1/I2 属性门禁，覆盖
// docs/spec/compiler/allocation-stability.md 的核心不变量：
//   - I1 超集稳定：超集拓扑重编译，对每条既有 edge 重现逐字节相同的分配值。
//   - I2 顺序无关：分配值不依赖节点/edge 的数组位置。
//   - I9 删除回收 + G1 gap-fill 幂等：删除再以全新 edge 重加同一对节点，
//     按 pinKey 排序 + 取最低空闲槽位的 gap-fill 重现同一 transit 对。
//   - I7 pin 逐字遵循：运营商手钉的（合法、在池内、不冲突）pin 编译为完全相同的值。
//   - I10 + 向后兼容：v1.2.0 形态（无 pin、无 alloc_schema_version）的拓扑能正常编译，
//     结果带上 pin 与 AllocSchemaVersion=1。

// stableRouterNode 构造一个公网可达的 router 节点，已填好 overlay IP 与 base 端口，
// 供本文件的属性测试直接喂给 Compile（IP 分配器会保留已设的 overlay IP）。
func stableRouterNode(id, name, overlayIP string) model.Node {
	return model.Node{
		ID:         id,
		Name:       name,
		Hostname:   name + ".example.com",
		Role:       "router",
		DomainID:   "domain-1",
		ListenPort: 51820,
		OverlayIP:  overlayIP,
		Capabilities: model.NodeCapabilities{
			CanAcceptInbound: true,
			CanForward:       true,
			HasPublicIP:      true,
		},
		PublicEndpoints: []model.PublicEndpoint{
			{ID: id + "-ep", Host: name + ".example.com", Port: 51820},
		},
	}
}

// stableDomain 是这些测试共用的单域定义（overlay 10.50.0.0/24，transit 留空→默认 10.10.0.0/24）。
func stableDomain() model.Domain {
	return model.Domain{
		ID: "domain-1", Name: "stable", CIDR: "10.50.0.0/24",
		AllocationMode: "auto", RoutingMode: "babel",
	}
}

// stableKeys 为 a/b/c 三个节点提供固定密钥，避免触发任何密钥生成路径
// （密钥持久化在另一分区，本文件只关心端口/transit/link-local 的稳定性）。
func stableKeys() map[string]KeyPair {
	return map[string]KeyPair{
		"node-a": {PrivateKey: "priv-a-fake", PublicKey: "pub-a-fake"},
		"node-b": {PrivateKey: "priv-b-fake", PublicKey: "pub-b-fake"},
		"node-c": {PrivateKey: "priv-c-fake", PublicKey: "pub-c-fake"},
	}
}

// abPins 抓取一条 edge 的全部分配输出，用于跨编译做逐字节比较。
type abPins struct {
	fromPort      int
	toPort        int
	fromTransitIP string
	toTransitIP   string
	fromLinkLocal string
	toLinkLocal   string
	compiledPort  int
}

// capturePins 从编译后的拓扑里按 edge id 取出其全部分配值。
func capturePins(t *testing.T, topo *model.Topology, edgeID string) abPins {
	t.Helper()
	edge := findEdge(topo.Edges, edgeID)
	if edge == nil {
		t.Fatalf("编译后拓扑中找不到 edge %q", edgeID)
	}
	return abPins{
		fromPort:      edge.PinnedFromPort,
		toPort:        edge.PinnedToPort,
		fromTransitIP: edge.PinnedFromTransitIP,
		toTransitIP:   edge.PinnedToTransitIP,
		fromLinkLocal: edge.PinnedFromLinkLocal,
		toLinkLocal:   edge.PinnedToLinkLocal,
		compiledPort:  edge.CompiledPort,
	}
}

// assertPinsEqual 断言两次抓取的分配值逐字段相同（即「逐字节相同的 bundle」的代理判据）。
func assertPinsEqual(t *testing.T, label string, want, got abPins) {
	t.Helper()
	if want.fromPort != got.fromPort {
		t.Errorf("%s：from 端口应不变（%d），实际 %d", label, want.fromPort, got.fromPort)
	}
	if want.toPort != got.toPort {
		t.Errorf("%s：to 端口应不变（%d），实际 %d", label, want.toPort, got.toPort)
	}
	if want.fromTransitIP != got.fromTransitIP {
		t.Errorf("%s：from transit IP 应不变（%s），实际 %s", label, want.fromTransitIP, got.fromTransitIP)
	}
	if want.toTransitIP != got.toTransitIP {
		t.Errorf("%s：to transit IP 应不变（%s），实际 %s", label, want.toTransitIP, got.toTransitIP)
	}
	if want.fromLinkLocal != got.fromLinkLocal {
		t.Errorf("%s：from link-local 应不变（%s），实际 %s", label, want.fromLinkLocal, got.fromLinkLocal)
	}
	if want.toLinkLocal != got.toLinkLocal {
		t.Errorf("%s：to link-local 应不变（%s），实际 %s", label, want.toLinkLocal, got.toLinkLocal)
	}
	if want.compiledPort != got.compiledPort {
		t.Errorf("%s：CompiledPort 应不变（%d），实际 %d", label, want.compiledPort, got.compiledPort)
	}
}

// applyPins 把抓取到的 pin 写回到一条 edge 上，模拟前端持久化往返后再次提交编译。
func applyPins(edge *model.Edge, p abPins) {
	edge.PinnedFromPort = p.fromPort
	edge.PinnedToPort = p.toPort
	edge.PinnedFromTransitIP = p.fromTransitIP
	edge.PinnedToTransitIP = p.toTransitIP
	edge.PinnedFromLinkLocal = p.fromLinkLocal
	edge.PinnedToLinkLocal = p.toLinkLocal
}

// abEdge 构造一条 A->B 的 direct edge（带 endpoint_host，使 CompiledPort 也参与比较）。
func abEdge(id, from, to, endpointHost string) model.Edge {
	return model.Edge{
		ID: id, FromNodeID: from, ToNodeID: to,
		Type: "direct", EndpointHost: endpointHost, EndpointPort: 0,
		Transport: "udp", IsEnabled: true,
	}
}

// TestSupersetCompileReproducesAllocations 是 I1（超集稳定）+ I2（顺序无关）的主门禁。
//
//	topo1 = [A,B] + A-B            →  抓取 A-B 的全部分配值
//	topo2 = [A,B,C] + A-B(带 pin) + A-C(追加)  →  A-B 必须逐字节相同
//	topo3 = [A,B,C] + A-C(前插) + A-B(带 pin)  →  A-B 仍逐字节相同（顺序无关）
//
// topo3 的「前插」是关键：在旧的位置计数器实现下，A-C 排在 A-B 之前会改变 A-B 的端口/transit
// 取值，从而违反 I2。reserve-then-gap-fill 由构造让它成立。
func TestSupersetCompileReproducesAllocations(t *testing.T) {
	c := NewCompiler()
	keys := stableKeys()

	// ---- compile 1：[A,B] + A-B ----
	topo1 := &model.Topology{
		Project: model.Project{ID: "stable-001", Name: "Superset Stability"},
		Domains: []model.Domain{stableDomain()},
		Nodes: []model.Node{
			stableRouterNode("node-a", "alpha", "10.50.0.1"),
			stableRouterNode("node-b", "beta", "10.50.0.2"),
		},
		Edges: []model.Edge{
			abEdge("e-ab", "node-a", "node-b", "beta.example.com"),
		},
	}
	res1, err := c.Compile(topo1, keys)
	if err != nil {
		t.Fatalf("compile 1 失败: %v", err)
	}
	base := capturePins(t, res1.Topology, "e-ab")
	// sanity：A-B 的分配值应当都已写回（端口、transit、link-local 非空）。
	if base.fromPort == 0 || base.toPort == 0 || base.fromTransitIP == "" ||
		base.toTransitIP == "" || base.fromLinkLocal == "" || base.toLinkLocal == "" {
		t.Fatalf("compile 1 应把 A-B 的全部分配写回 pin，实际: %+v", base)
	}

	// 把 compile 1 写回的 pin 取出来，作为 A-B 在后续拓扑里携带的 pin。
	pinnedAB := abEdge("e-ab", "node-a", "node-b", "beta.example.com")
	applyPins(&pinnedAB, base)

	// ---- compile 2：[A,B,C] + A-B(带 pin) + A-C(追加，无 pin) ----
	topo2 := &model.Topology{
		Project: model.Project{ID: "stable-001", Name: "Superset Stability"},
		Domains: []model.Domain{stableDomain()},
		Nodes: []model.Node{
			stableRouterNode("node-a", "alpha", "10.50.0.1"),
			stableRouterNode("node-b", "beta", "10.50.0.2"),
			stableRouterNode("node-c", "gamma", "10.50.0.3"),
		},
		Edges: []model.Edge{
			pinnedAB,
			abEdge("e-ac", "node-a", "node-c", "gamma.example.com"),
		},
	}
	res2, err := c.Compile(topo2, keys)
	if err != nil {
		t.Fatalf("compile 2 失败: %v", err)
	}
	got2 := capturePins(t, res2.Topology, "e-ab")
	assertPinsEqual(t, "I1 追加 C 后 A-B", base, got2)

	// ---- compile 3：[A,B,C] + A-C(前插，无 pin) + A-B(带 pin) ----
	// 新 edge 排在 A-B 之前：在旧位置计数器下会移动 A-B 的取值，此处必须仍然不变。
	topo3 := &model.Topology{
		Project: model.Project{ID: "stable-001", Name: "Superset Stability"},
		Domains: []model.Domain{stableDomain()},
		Nodes: []model.Node{
			stableRouterNode("node-a", "alpha", "10.50.0.1"),
			stableRouterNode("node-b", "beta", "10.50.0.2"),
			stableRouterNode("node-c", "gamma", "10.50.0.3"),
		},
		Edges: []model.Edge{
			abEdge("e-ac", "node-a", "node-c", "gamma.example.com"),
			pinnedAB,
		},
	}
	res3, err := c.Compile(topo3, keys)
	if err != nil {
		t.Fatalf("compile 3 失败: %v", err)
	}
	got3 := capturePins(t, res3.Topology, "e-ab")
	assertPinsEqual(t, "I2 前插 A-C 后 A-B", base, got3)
}

// TestDeleteReAddReclaimsValues 是 I9（删除回收）+ G1（gap-fill 幂等）的门禁。
//
//	compile 1：[A,B,C] + A-B + A-C       →  抓取 A-C 的 transit 对
//	compile 2：[A,B,C] + A-B(带 pin)      →  删除 A-C，其槽位被释放
//	compile 3：[A,B,C] + A-B(带 pin) + A-C(全新 id，无 pin)  →  A-C 应重现同一 transit 对
//
// 重现成立的机制：gap-fill 按 pinKey 排序遍历、池内取最低空闲槽位，与 A-C 的删除/重加历史、
// 数组位置无关；既有的 A-B 始终被先预留（pin 逐字遵循），故 A-C 在两次编译里看到相同的预留集合，
// 因而取到同一最低空闲 pair。
func TestDeleteReAddReclaimsValues(t *testing.T) {
	c := NewCompiler()
	keys := stableKeys()

	nodes := func() []model.Node {
		return []model.Node{
			stableRouterNode("node-a", "alpha", "10.50.0.1"),
			stableRouterNode("node-b", "beta", "10.50.0.2"),
			stableRouterNode("node-c", "gamma", "10.50.0.3"),
		}
	}

	// ---- compile 1：A-B + A-C 都无 pin（首次 gap-fill） ----
	topo1 := &model.Topology{
		Project: model.Project{ID: "stable-002", Name: "Delete ReAdd"},
		Domains: []model.Domain{stableDomain()},
		Nodes:   nodes(),
		Edges: []model.Edge{
			abEdge("e-ab", "node-a", "node-b", "beta.example.com"),
			abEdge("e-ac", "node-a", "node-c", "gamma.example.com"),
		},
	}
	res1, err := c.Compile(topo1, keys)
	if err != nil {
		t.Fatalf("compile 1 失败: %v", err)
	}
	abBase := capturePins(t, res1.Topology, "e-ab")
	acBase := capturePins(t, res1.Topology, "e-ac")
	if acBase.fromTransitIP == "" || acBase.toTransitIP == "" {
		t.Fatalf("compile 1 应为 A-C 分配 transit 对，实际: %+v", acBase)
	}

	// A-B 携带 compile 1 的 pin 进入后续编译（既有链路被先预留）。
	pinnedAB := abEdge("e-ab", "node-a", "node-b", "beta.example.com")
	applyPins(&pinnedAB, abBase)

	// ---- compile 2：删除 A-C，仅留带 pin 的 A-B（A-C 槽位释放） ----
	topo2 := &model.Topology{
		Project: model.Project{ID: "stable-002", Name: "Delete ReAdd"},
		Domains: []model.Domain{stableDomain()},
		Nodes:   nodes(),
		Edges: []model.Edge{
			pinnedAB,
		},
	}
	if _, err := c.Compile(topo2, keys); err != nil {
		t.Fatalf("compile 2（删除 A-C）失败: %v", err)
	}

	// ---- compile 3：以全新 id、无 pin 的 A-C 重加 ----
	topo3 := &model.Topology{
		Project: model.Project{ID: "stable-002", Name: "Delete ReAdd"},
		Domains: []model.Domain{stableDomain()},
		Nodes:   nodes(),
		Edges: []model.Edge{
			pinnedAB,
			abEdge("e-ac-readded", "node-a", "node-c", "gamma.example.com"),
		},
	}
	res3, err := c.Compile(topo3, keys)
	if err != nil {
		t.Fatalf("compile 3（重加 A-C）失败: %v", err)
	}
	acReadded := capturePins(t, res3.Topology, "e-ac-readded")

	if acReadded.fromTransitIP != acBase.fromTransitIP || acReadded.toTransitIP != acBase.toTransitIP {
		t.Errorf("删除再重加 A-C 应重现同一 transit 对：原 {%s, %s}，重加后 {%s, %s}",
			acBase.fromTransitIP, acBase.toTransitIP, acReadded.fromTransitIP, acReadded.toTransitIP)
	}
	// link-local 同样应被哈希播种重现。
	if acReadded.fromLinkLocal != acBase.fromLinkLocal || acReadded.toLinkLocal != acBase.toLinkLocal {
		t.Errorf("删除再重加 A-C 应重现同一 link-local 对：原 {%s, %s}，重加后 {%s, %s}",
			acBase.fromLinkLocal, acBase.toLinkLocal, acReadded.fromLinkLocal, acReadded.toLinkLocal)
	}
}

// TestPinnedValuesHonoredVerbatim 是 I7 的门禁：运营商手钉的（合法、在池内、不冲突）pin
// 编译后被逐字遵循，编译器绝不重新编号。
func TestPinnedValuesHonoredVerbatim(t *testing.T) {
	c := NewCompiler()
	keys := stableKeys()

	// 手钉一组合法 pin：端口 >= base(51820)，transit 在默认池 10.10.0.0/24 内且非网络/广播，
	// link-local 为合法十六进制。
	edge := abEdge("e-ab", "node-a", "node-b", "beta.example.com")
	edge.PinnedFromPort = 51830
	edge.PinnedToPort = 51831
	edge.PinnedFromTransitIP = "10.10.0.51"
	edge.PinnedToTransitIP = "10.10.0.52"
	edge.PinnedFromLinkLocal = "fe80::aa"
	edge.PinnedToLinkLocal = "fe80::ab"

	topo := &model.Topology{
		Project: model.Project{ID: "stable-003", Name: "Pins Verbatim"},
		Domains: []model.Domain{stableDomain()},
		Nodes: []model.Node{
			stableRouterNode("node-a", "alpha", "10.50.0.1"),
			stableRouterNode("node-b", "beta", "10.50.0.2"),
		},
		Edges: []model.Edge{edge},
	}

	res, err := c.Compile(topo, keys)
	if err != nil {
		t.Fatalf("带合法 pin 的拓扑应能编译，实际报错: %v", err)
	}
	got := capturePins(t, res.Topology, "e-ab")

	if got.fromPort != 51830 || got.toPort != 51831 {
		t.Errorf("端口 pin 应逐字遵循 {51830, 51831}，实际 {%d, %d}", got.fromPort, got.toPort)
	}
	if got.fromTransitIP != "10.10.0.51" || got.toTransitIP != "10.10.0.52" {
		t.Errorf("transit pin 应逐字遵循 {10.10.0.51, 10.10.0.52}，实际 {%s, %s}", got.fromTransitIP, got.toTransitIP)
	}
	if got.fromLinkLocal != "fe80::aa" || got.toLinkLocal != "fe80::ab" {
		t.Errorf("link-local pin 应逐字遵循 {fe80::aa, fe80::ab}，实际 {%s, %s}", got.fromLinkLocal, got.toLinkLocal)
	}
	// CompiledPort 应等于对端（toNode）接口的已分配监听端口 = PinnedToPort。
	if got.compiledPort != 51831 {
		t.Errorf("CompiledPort 应等于对端接口端口 51831，实际 %d", got.compiledPort)
	}
}

// TestPrePinTopologyCompiles 是 I10 + 向后兼容的门禁：一个 v1.2.0 形态的拓扑
// （无任何 pin 字段、无 alloc_schema_version）应能正常编译，且结果带上
// 写回的 pin 与 AllocSchemaVersion=1。
func TestPrePinTopologyCompiles(t *testing.T) {
	c := NewCompiler()
	keys := stableKeys()

	topo := &model.Topology{
		Project: model.Project{ID: "stable-004", Name: "Pre-Pin BackCompat"},
		Domains: []model.Domain{stableDomain()},
		Nodes: []model.Node{
			stableRouterNode("node-a", "alpha", "10.50.0.1"),
			stableRouterNode("node-b", "beta", "10.50.0.2"),
		},
		// 注意：完全不设置任何 pinned_* 字段，也不设置 AllocSchemaVersion。
		Edges: []model.Edge{
			abEdge("e-ab", "node-a", "node-b", "beta.example.com"),
		},
	}
	// 显式确认输入是 pre-pin 形态。
	if topo.AllocSchemaVersion != 0 {
		t.Fatalf("前置条件：输入拓扑的 AllocSchemaVersion 应为 0（pre-pin 形态）")
	}

	res, err := c.Compile(topo, keys)
	if err != nil {
		t.Fatalf("pre-pin 拓扑应能正常编译，实际报错: %v", err)
	}

	// 结果应被标记为当前 schema 版本（I10）。
	if res.Topology.AllocSchemaVersion != AllocationSchemaVersion {
		t.Errorf("编译结果的 AllocSchemaVersion 应为 %d，实际 %d",
			AllocationSchemaVersion, res.Topology.AllocSchemaVersion)
	}
	if AllocationSchemaVersion != 1 {
		t.Errorf("AllocationSchemaVersion 常量应为 1，实际 %d", AllocationSchemaVersion)
	}

	// 结果应把分配值写回成 pin（供下次编译沿用）。
	got := capturePins(t, res.Topology, "e-ab")
	if got.fromPort == 0 || got.toPort == 0 {
		t.Errorf("编译后 A-B 应写回端口 pin，实际 {%d, %d}", got.fromPort, got.toPort)
	}
	if got.fromTransitIP == "" || got.toTransitIP == "" {
		t.Errorf("编译后 A-B 应写回 transit pin，实际 {%q, %q}", got.fromTransitIP, got.toTransitIP)
	}
	if got.fromLinkLocal == "" || got.toLinkLocal == "" {
		t.Errorf("编译后 A-B 应写回 link-local pin，实际 {%q, %q}", got.fromLinkLocal, got.toLinkLocal)
	}
}
