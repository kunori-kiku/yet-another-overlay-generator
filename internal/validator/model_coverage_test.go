package validator

import (
	"testing"

	"github.com/kunorikiku/yet-another-overlay-generator/internal/model"
)

// model_coverage_test.go 覆盖「字段覆盖」分区新增的若干结构性校验规则
// （Spec E 字段平价表 + Spec docs/spec/compiler/validation.md 覆盖表）：
//   - route_policies 保留特性拒绝（D10/D37/D62，semantic）
//   - MTU 范围（D64，schema）
//   - ssh_port 范围（D65，schema）
//   - router_id MAC-48 / IPv4 格式（D66，schema）
//   - extra_prefixes IPv4 CIDR（D67，schema）
//
// 每张表都成对覆盖「应通过」与「应拒绝」两类取值，沿用既有 validator 测试的
// validTopology()/assertHasError()/contains() 辅助函数。

// assertNoErrorOnField 断言结果中不存在任何字段名包含 fieldSubstring 的错误。
// 用于「应通过」分支：合法取值不得在目标字段上触发任何校验错误。
func assertNoErrorOnField(t *testing.T, result *ValidationResult, fieldSubstring, value string) {
	t.Helper()
	for _, e := range result.Errors {
		if contains(e.Field, fieldSubstring) {
			t.Errorf("取值 %q 不应在字段 %s 上触发错误，却得到：%s", value, fieldSubstring, e.Error())
		}
	}
}

// TestValidateSemantic_RoutePoliciesReserved 覆盖 route_policies 保留特性拒绝（D10/D37/D62）。
// route_policies 没有任何渲染器消费，编译器仅原样透传，因此非空数组必须在语义校验阶段被拒绝；
// 空数组（或 nil）则应当通过。
func TestValidateSemantic_RoutePoliciesReserved(t *testing.T) {
	cases := []struct {
		name        string
		policies    []model.RoutePolicy
		expectError bool
	}{
		{
			name:        "空 route_policies 通过",
			policies:    nil,
			expectError: false,
		},
		{
			name:        "零长 route_policies 通过",
			policies:    []model.RoutePolicy{},
			expectError: false,
		},
		{
			name: "单条 route_policy 被拒绝",
			policies: []model.RoutePolicy{
				{ID: "rp-1", DomainID: "domain-1", DestinationCIDR: "192.168.0.0/24"},
			},
			expectError: true,
		},
		{
			name: "多条 route_policy 被拒绝",
			policies: []model.RoutePolicy{
				{ID: "rp-1", DomainID: "domain-1", DestinationCIDR: "192.168.0.0/24"},
				{ID: "rp-2", DomainID: "domain-1", DestinationCIDR: "10.0.0.0/8"},
			},
			expectError: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			topo := validTopology()
			topo.RoutePolicies = tc.policies
			result := ValidateSemantic(topo)
			if tc.expectError {
				assertHasError(t, result, "route_policies")
			} else {
				assertNoErrorOnField(t, result, "route_policies", "")
			}
		})
	}
}

// TestValidateSchema_MTURange 覆盖 MTU 范围校验（D64）。
// 0 表示使用系统默认值，应通过；非零时必须落在 [576, 65535] 内，越界（含 575、65536）应拒绝。
func TestValidateSchema_MTURange(t *testing.T) {
	cases := []struct {
		name        string
		mtu         int
		expectError bool
	}{
		{name: "0 使用默认值", mtu: 0, expectError: false},
		{name: "下限 576", mtu: 576, expectError: false},
		{name: "常用 1420", mtu: 1420, expectError: false},
		{name: "上限 65535", mtu: 65535, expectError: false},
		{name: "低于下限 575", mtu: 575, expectError: true},
		{name: "负值", mtu: -1, expectError: true},
		{name: "超过上限 65536", mtu: 65536, expectError: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			topo := validTopology()
			topo.Nodes[0].MTU = tc.mtu
			result := ValidateSchema(topo)
			if tc.expectError {
				assertHasError(t, result, "nodes[0].mtu")
			} else {
				assertNoErrorOnField(t, result, "nodes[0].mtu", itoaTest(tc.mtu))
			}
		})
	}
}

// TestValidateSchema_SSHPortRange 覆盖 ssh_port 范围校验（D65）。
// 0 表示使用默认端口 22，应通过；非零时必须落在 1–65535 内，越界应拒绝。
func TestValidateSchema_SSHPortRange(t *testing.T) {
	cases := []struct {
		name        string
		sshPort     int
		expectError bool
	}{
		{name: "0 使用默认端口", sshPort: 0, expectError: false},
		{name: "下限 1", sshPort: 1, expectError: false},
		{name: "常用 22", sshPort: 22, expectError: false},
		{name: "上限 65535", sshPort: 65535, expectError: false},
		{name: "负值", sshPort: -1, expectError: true},
		{name: "超过上限 65536", sshPort: 65536, expectError: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			topo := validTopology()
			topo.Nodes[0].SSHPort = tc.sshPort
			result := ValidateSchema(topo)
			if tc.expectError {
				assertHasError(t, result, "nodes[0].ssh_port")
			} else {
				assertNoErrorOnField(t, result, "nodes[0].ssh_port", itoaTest(tc.sshPort))
			}
		})
	}
}

// TestValidateSchema_RouterIDFormat 覆盖 router_id 格式校验（D66）。
// 留空由编译器自动生成，应通过；非空时必须为 MAC-48 形式或可解析为 IPv4 地址，二者皆非则拒绝。
func TestValidateSchema_RouterIDFormat(t *testing.T) {
	cases := []struct {
		name        string
		routerID    string
		expectError bool
	}{
		{name: "留空自动生成", routerID: "", expectError: false},
		{name: "合法 MAC-48 小写", routerID: "02:11:22:33:44:55", expectError: false},
		{name: "合法 MAC-48 大写", routerID: "AA:BB:CC:DD:EE:FF", expectError: false},
		{name: "合法 IPv4", routerID: "10.0.0.1", expectError: false},
		{name: "MAC 段数不足", routerID: "02:11:22:33:44", expectError: true},
		{name: "MAC 含非十六进制", routerID: "02:11:22:33:44:GG", expectError: true},
		{name: "MAC 段位过长", routerID: "002:11:22:33:44:55", expectError: true},
		{name: "IPv6 不接受", routerID: "fe80::1", expectError: true},
		{name: "纯文本", routerID: "router-one", expectError: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			topo := validTopology()
			topo.Nodes[0].RouterID = tc.routerID
			result := ValidateSchema(topo)
			if tc.expectError {
				assertHasError(t, result, "nodes[0].router_id")
			} else {
				assertNoErrorOnField(t, result, "nodes[0].router_id", tc.routerID)
			}
		})
	}
}

// TestValidateSchema_ExtraPrefixesIPv4CIDR 覆盖 extra_prefixes IPv4 CIDR 校验（D67）。
// 空数组应通过；每项必须可解析为 IPv4 CIDR，非 CIDR、IPv6 CIDR、裸 IP 均应拒绝。
func TestValidateSchema_ExtraPrefixesIPv4CIDR(t *testing.T) {
	cases := []struct {
		name        string
		prefixes    []string
		expectError bool
	}{
		{name: "空数组通过", prefixes: nil, expectError: false},
		{name: "单个合法 IPv4 CIDR", prefixes: []string{"192.168.0.0/24"}, expectError: false},
		{name: "多个合法 IPv4 CIDR", prefixes: []string{"192.168.0.0/24", "10.0.0.0/8"}, expectError: false},
		{name: "非 CIDR 文本", prefixes: []string{"not-a-cidr"}, expectError: true},
		{name: "裸 IP 无前缀", prefixes: []string{"192.168.0.1"}, expectError: true},
		{name: "IPv6 CIDR 被拒绝", prefixes: []string{"fd00::/8"}, expectError: true},
		{name: "首项合法次项非法", prefixes: []string{"192.168.0.0/24", "bad"}, expectError: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			topo := validTopology()
			topo.Nodes[0].ExtraPrefixes = tc.prefixes
			result := ValidateSchema(topo)
			if tc.expectError {
				assertHasError(t, result, "nodes[0].extra_prefixes")
			} else {
				assertNoErrorOnField(t, result, "nodes[0].extra_prefixes", "")
			}
		})
	}
}
