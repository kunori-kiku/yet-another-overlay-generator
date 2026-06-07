package validator

import (
	"testing"

	"github.com/kunorikiku/yet-another-overlay-generator/internal/model"
)

// mimicTransportTopology 构造一个两节点单边拓扑，边 transport 与两端节点 platform
// 由参数指定，用于覆盖 mimic（tcp 传输）的平台约束校验
// （docs/spec/artifacts/mimic.md、compiler/validation.md、契约 item 4）。
//
// 与 field_safety_test.go 的 transportTopology 类似，但额外参数化两端平台，
// 以便构造「tcp 边连向非 Linux 平台」的报错用例。
func mimicTransportTopology(transport, fromPlatform, toPlatform string) *model.Topology {
	return &model.Topology{
		Project: model.Project{ID: "mimic-validate", Name: "Mimic Validate"},
		Domains: []model.Domain{
			{ID: "domain-1", Name: "net", CIDR: "10.10.0.0/24", AllocationMode: "auto", RoutingMode: "babel"},
		},
		Nodes: []model.Node{
			{ID: "a", Name: "a", Role: "router", DomainID: "domain-1", ListenPort: 51820, Platform: fromPlatform,
				Capabilities: model.NodeCapabilities{CanAcceptInbound: true, CanForward: true, HasPublicIP: true}},
			{ID: "b", Name: "b", Role: "router", DomainID: "domain-1", ListenPort: 51820, Platform: toPlatform,
				Capabilities: model.NodeCapabilities{CanAcceptInbound: true, CanForward: true, HasPublicIP: true}},
		},
		Edges: []model.Edge{
			{ID: "edge-1", FromNodeID: "a", ToNodeID: "b", Type: "direct", Transport: transport, IsEnabled: true},
		},
	}
}

// TestValidate_MimicTcpBetweenLinux_NoErrorNoWarning 覆盖契约 item 4 的正路径：
// 两个 debian/ubuntu 节点之间一条 tcp 边 → schema 与 semantic 都不应报 transport 错误，
// 且 v1.3.0 的「tcp 保留/未实现」告警必须已被移除（不再出现任何 transport 相关告警）。
func TestValidate_MimicTcpBetweenLinux_NoErrorNoWarning(t *testing.T) {
	cases := []struct {
		name         string
		fromPF, toPF string
	}{
		{name: "debian <-> ubuntu", fromPF: "debian", toPF: "ubuntu"},
		{name: "ubuntu <-> debian", fromPF: "ubuntu", toPF: "debian"},
		// 空 platform 视为 Linux（放行），与其它平台校验对空值的处理一致。
		{name: "empty <-> debian (empty=Linux)", fromPF: "", toPF: "debian"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			topo := mimicTransportTopology("tcp", tc.fromPF, tc.toPF)

			// Schema 阶段：tcp 是合法取值，不应报错；且不应再有 v1.3.0 的 transport 告警。
			schemaResult := ValidateSchema(topo)
			for _, e := range schemaResult.Errors {
				if containsSubstring(e.Field, "transport") {
					t.Errorf("Linux↔Linux 的 tcp 边不应产生 schema transport 错误，实际: %v", schemaResult.Errors)
				}
			}
			for _, w := range schemaResult.Warnings {
				if containsSubstring(w.Field, "transport") {
					t.Errorf("v1.3.0 的 tcp 保留告警应已移除，却仍产生 schema 告警: %v", schemaResult.Warnings)
				}
			}

			// Semantic 阶段：两端均为可部署 Linux，mimic 平台约束应放行。
			semResult := ValidateSemantic(topo)
			for _, e := range semResult.Errors {
				if containsSubstring(e.Field, "transport") {
					t.Errorf("Linux↔Linux 的 tcp 边不应产生 semantic transport 错误，实际: %v", semResult.Errors)
				}
			}
			for _, w := range semResult.Warnings {
				if containsSubstring(w.Field, "transport") {
					t.Errorf("Linux↔Linux 的 tcp 边不应产生 semantic transport 告警，实际: %v", semResult.Warnings)
				}
			}
		})
	}
}

// TestValidate_MimicTcpToNonLinux_Errors 覆盖契约 item 4 的报错路径：
// tcp 边的任一端点平台不是可部署 Linux（debian / ubuntu）时，semantic 校验必须报错，
// 错误字段定位到该边的 transport，且错误消息点名该边 ID。
func TestValidate_MimicTcpToNonLinux_Errors(t *testing.T) {
	cases := []struct {
		name         string
		fromPF, toPF string
	}{
		{name: "to non-Linux (windows)", fromPF: "debian", toPF: "windows"},
		{name: "from non-Linux (macos)", fromPF: "macos", toPF: "ubuntu"},
		{name: "both non-Linux", fromPF: "windows", toPF: "darwin"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			topo := mimicTransportTopology("tcp", tc.fromPF, tc.toPF)
			result := ValidateSemantic(topo)

			// 错误必须定位到 edge 的 transport 字段。
			assertHasError(t, result, "edges[0].transport")

			// 错误消息应点名该边（edge.ID="edge-1"），便于运营商定位。
			found := false
			for _, e := range result.Errors {
				if containsSubstring(e.Field, "edges[0].transport") && containsSubstring(e.Message, "edge-1") {
					found = true
				}
			}
			if !found {
				t.Errorf("非 Linux 平台的 tcp 边报错消息应点名该边 ID（edge-1），实际错误: %v", result.Errors)
			}
		})
	}
}

// TestValidate_UdpEdge_UnaffectedByMimic 覆盖契约 item 4 的不变量：
// udp 边完全不受 mimic 平台约束影响——即便端点是非 Linux 平台，udp 边也不应因 mimic
// 规则报 transport 错误，且不产生任何 transport 告警。
func TestValidate_UdpEdge_UnaffectedByMimic(t *testing.T) {
	// 故意把一端设为非 Linux 平台：udp 边不应触发 mimic 平台约束。
	topo := mimicTransportTopology("udp", "debian", "windows")

	semResult := ValidateSemantic(topo)
	for _, e := range semResult.Errors {
		if containsSubstring(e.Field, "transport") {
			t.Errorf("udp 边不应触发 mimic 平台约束（transport 错误），实际: %v", semResult.Errors)
		}
	}

	schemaResult := ValidateSchema(topo)
	for _, w := range schemaResult.Warnings {
		if containsSubstring(w.Field, "transport") {
			t.Errorf("udp 边不应产生任何 transport 告警，实际: %v", schemaResult.Warnings)
		}
	}
	for _, e := range schemaResult.Errors {
		if containsSubstring(e.Field, "transport") {
			t.Errorf("udp 边不应产生 schema transport 错误，实际: %v", schemaResult.Errors)
		}
	}
}

// TestValidate_XDPModeEnum 覆盖 per-node xdp_mode 枚举校验：
// 空 / "skb" / "native" 合法（无 xdp_mode 错误）；其它值（含大小写错误）应在 schema 阶段报错。
func TestValidate_XDPModeEnum(t *testing.T) {
	hasXDPErr := func(r *ValidationResult) bool {
		for _, e := range r.Errors {
			if containsSubstring(e.Field, "xdp_mode") {
				return true
			}
		}
		return false
	}

	for _, mode := range []string{"", "skb", "native"} {
		topo := mimicTransportTopology("tcp", "debian", "debian")
		topo.Nodes[0].XDPMode = mode
		if hasXDPErr(ValidateSchema(topo)) {
			t.Errorf("xdp_mode=%q 合法，不应报错", mode)
		}
	}

	for _, mode := range []string{"Native", "generic", "xdp", "SKB"} {
		topo := mimicTransportTopology("tcp", "debian", "debian")
		topo.Nodes[0].XDPMode = mode
		if !hasXDPErr(ValidateSchema(topo)) {
			t.Errorf("xdp_mode=%q 非法，应在 schema 报 xdp_mode 错误", mode)
		}
	}
}
