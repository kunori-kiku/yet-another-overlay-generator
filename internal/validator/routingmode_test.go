package validator

import (
	"testing"

	"github.com/kunorikiku/yet-another-overlay-generator/internal/model"
)

// 本文件覆盖 Plan 6（Spec C：docs/spec/compiler/routing-modes.md）中归属于
// 「校验 + 归一化」分区的契约：
//   - routing_mode 空值归一为 babel 并 round-trip（拓扑对象事后显式携带 babel）；
//   - static / none 被拒绝（尚未实现）；
//   - transport 空值归一为 udp；
//   - D50：双端 NAT、两个方向都无 endpoint_host 的确凿死链报 error，
//     而同一链路只要有一个方向带 endpoint_host 则仅告警。
//
// 复用 validator_test.go 中的 validTopology / assertHasError / assertHasWarning 等辅助函数。

// --- routing_mode 归一化与 round-trip ---

// TestRoutingMode_EmptyNormalizesToBabelAndRoundTrips 验证空 routing_mode 被归一为 babel，
// 且该归一以写回的方式持久化进拓扑对象（round-trip）：校验后拓扑对象自身显式携带 babel。
func TestRoutingMode_EmptyNormalizesToBabelAndRoundTrips(t *testing.T) {
	topo := validTopology()
	topo.Domains[0].RoutingMode = ""

	result := ValidateSchema(topo)

	if !result.IsValid() {
		t.Fatalf("空 routing_mode 归一为 babel 后不应产生校验错误，实际错误: %v", result.Errors)
	}
	// round-trip 断言：归一必须写回拓扑对象，使其后续编译/持久化都显式携带 babel。
	if got := topo.Domains[0].RoutingMode; got != "babel" {
		t.Errorf("空 routing_mode 应被归一并写回为 babel，实际仍为 %q", got)
	}
}

// TestRoutingMode_StaticRejected 验证 static 模式被拒绝（尚未实现）。
func TestRoutingMode_StaticRejected(t *testing.T) {
	topo := validTopology()
	topo.Domains[0].RoutingMode = "static"

	result := ValidateSchema(topo)

	assertHasError(t, result, "domains[0].routing_mode")
	assertErrorMessageContains(t, result, "domains[0].routing_mode", "static")
	assertErrorMessageContains(t, result, "domains[0].routing_mode", "babel")
}

// TestRoutingMode_NoneRejected 验证 none 模式被拒绝（尚未实现）。
func TestRoutingMode_NoneRejected(t *testing.T) {
	topo := validTopology()
	topo.Domains[0].RoutingMode = "none"

	result := ValidateSchema(topo)

	assertHasError(t, result, "domains[0].routing_mode")
	assertErrorMessageContains(t, result, "domains[0].routing_mode", "none")
	assertErrorMessageContains(t, result, "domains[0].routing_mode", "babel")
}

// TestRoutingMode_BabelAccepted 验证显式 babel 通过校验且保持不变。
func TestRoutingMode_BabelAccepted(t *testing.T) {
	topo := validTopology()
	topo.Domains[0].RoutingMode = "babel"

	result := ValidateSchema(topo)

	if !result.IsValid() {
		t.Fatalf("显式 babel 不应产生校验错误，实际错误: %v", result.Errors)
	}
	if got := topo.Domains[0].RoutingMode; got != "babel" {
		t.Errorf("显式 babel 应保持为 babel，实际为 %q", got)
	}
}

// --- transport 归一化 ---

// TestTransport_EmptyNormalizesToUDP 验证空 transport 被归一为 udp 并写回拓扑对象。
func TestTransport_EmptyNormalizesToUDP(t *testing.T) {
	topo := validTopology()
	topo.Edges[0].Transport = ""
	topo.Edges[1].Transport = ""

	result := ValidateSchema(topo)

	if !result.IsValid() {
		t.Fatalf("空 transport 归一为 udp 后不应产生校验错误，实际错误: %v", result.Errors)
	}
	if got := topo.Edges[0].Transport; got != "udp" {
		t.Errorf("空 transport 应被归一并写回为 udp，实际仍为 %q", got)
	}
	if got := topo.Edges[1].Transport; got != "udp" {
		t.Errorf("空 transport 应被归一并写回为 udp，实际仍为 %q", got)
	}
}

// TestTransport_InvalidRejected 验证归一后的无效 transport 仍被枚举校验拒绝。
func TestTransport_InvalidRejected(t *testing.T) {
	topo := validTopology()
	topo.Edges[0].Transport = "sctp"

	result := ValidateSchema(topo)

	assertHasError(t, result, "edges[0].transport")
}

// --- D50：双端 NAT、无 endpoint 死链 ---

// natBothEndsTopology 构造一个两端均位于 NAT 之后、彼此 direct 直连的最小拓扑。
// 两个节点都没有公网 IP、不接受入站、也不是 relay；两条边（双向）默认都不带 endpoint_host。
// 这正是 D50 关注的「确凿死链」基底；各测试可在其上调整某一方向的 endpoint_host。
func natBothEndsTopology() *model.Topology {
	return &model.Topology{
		Project: model.Project{ID: "test", Name: "Test"},
		Domains: []model.Domain{{
			ID:             "domain-1",
			Name:           "test",
			CIDR:           "10.10.0.0/24",
			AllocationMode: "auto",
			RoutingMode:    "babel",
		}},
		Nodes: []model.Node{
			{
				ID: "nat-a", Name: "nat-a", Role: "peer", DomainID: "domain-1",
				Capabilities: model.NodeCapabilities{HasPublicIP: false, CanAcceptInbound: false},
			},
			{
				ID: "nat-b", Name: "nat-b", Role: "peer", DomainID: "domain-1",
				Capabilities: model.NodeCapabilities{HasPublicIP: false, CanAcceptInbound: false},
			},
		},
		Edges: []model.Edge{
			{ID: "e-ab", FromNodeID: "nat-a", ToNodeID: "nat-b", Type: "direct", Transport: "udp", IsEnabled: true},
			{ID: "e-ba", FromNodeID: "nat-b", ToNodeID: "nat-a", Type: "direct", Transport: "udp", IsEnabled: true},
		},
	}
}

// TestNATDeadLink_BothDirectionsEndpointless_Errors 验证：双端 NAT、两个方向都无 endpoint_host、
// 且两端都不接受入站时，链路被判定为确凿死链并报 error（而非仅告警）。
func TestNATDeadLink_BothDirectionsEndpointless_Errors(t *testing.T) {
	topo := natBothEndsTopology()
	// 两条边都不带 endpoint_host —— 确凿死链。

	result := ValidateSemantic(topo)

	// 死链应报 error。
	if !hasErrorMentioning(result, "nat-a", "nat-b") {
		t.Errorf("双端 NAT、两个方向均无 endpoint_host 的死链应报 error，实际错误: %v", result.Errors)
	}
}

// TestNATLink_OneDirectionHasEndpoint_OnlyWarns 验证：同一双端 NAT 链路，
// 只要有一个方向（反向边）带 endpoint_host，就仍可能建链，应降级为仅告警、不报死链 error。
func TestNATLink_OneDirectionHasEndpoint_OnlyWarns(t *testing.T) {
	topo := natBothEndsTopology()
	// 反向边 nat-b -> nat-a 带 endpoint_host：nat-b 可主动拨向 nat-a。
	topo.Edges[1].EndpointHost = "198.51.100.10"

	result := ValidateSemantic(topo)

	// 不应报死链 error。
	if hasErrorMentioning(result, "nat-a", "nat-b") {
		t.Errorf("某一方向已带 endpoint_host 时不应报死链 error，实际错误: %v", result.Errors)
	}
	// 仍应保留 NAT 告警（无 endpoint 的那条方向）。
	if !hasWarningMentioning(result, "nat-a", "nat-b") {
		t.Errorf("无 endpoint 的方向应保留 NAT 告警，实际告警: %v", result.Warnings)
	}
}

// TestNATLink_RelayEndpoint_OnlyWarns 验证：当一端为 relay（可接受入站）时，
// 即便两个方向都无 endpoint_host，链路也并非确凿死链，应仅告警、不报 error。
func TestNATLink_RelayEndpoint_OnlyWarns(t *testing.T) {
	topo := natBothEndsTopology()
	// 把 nat-b 改为 relay：relay 在能力推导后必然可接受入站，故可被拨入。
	topo.Nodes[1].Role = "relay"

	result := ValidateSemantic(topo)

	if hasErrorMentioning(result, "nat-a", "nat-b") {
		t.Errorf("一端为 relay 时不应报死链 error，实际错误: %v", result.Errors)
	}
}

// --- 局部断言辅助 ---

// assertErrorMessageContains 断言存在一条字段命中 fieldSubstring 且消息包含 msgSubstring 的 error。
func assertErrorMessageContains(t *testing.T, result *ValidationResult, fieldSubstring, msgSubstring string) {
	t.Helper()
	for _, e := range result.Errors {
		if contains(e.Field, fieldSubstring) && contains(e.Message, msgSubstring) {
			return
		}
	}
	t.Errorf("未找到字段含 %q 且消息含 %q 的 error，实际错误: %v", fieldSubstring, msgSubstring, result.Errors)
}

// hasErrorMentioning 判断是否存在一条消息同时提及 a 与 b 的 error。
func hasErrorMentioning(result *ValidationResult, a, b string) bool {
	for _, e := range result.Errors {
		if contains(e.Message, a) && contains(e.Message, b) {
			return true
		}
	}
	return false
}

// hasWarningMentioning 判断是否存在一条消息同时提及 a 与 b 的 warning。
func hasWarningMentioning(result *ValidationResult, a, b string) bool {
	for _, w := range result.Warnings {
		if contains(w.Message, a) && contains(w.Message, b) {
			return true
		}
	}
	return false
}
