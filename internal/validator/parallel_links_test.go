package validator

import (
	"testing"

	"github.com/kunorikiku/yet-another-overlay-generator/internal/model"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/naming"
)

// 本文件是并行链路（primary / backup 故障切换）在语义/结构校验分区的门禁，覆盖
// docs/spec/compiler/validation.md「Parallel links」与 docs/spec/artifacts/{naming.md,babel.md}：
//   - role 枚举：仅允许空 / "primary" / "backup"，其余拒绝（schema 阶段）。
//   - 同一对节点至多一条显式 "primary" 边（多于一条报错）。
//   - client 边不得为 backup（client 用单一 wg0，不参与并行链路）。
//   - 等代价告警：一对多链路若全部解析为相同 cost 则表达不出故障切换偏好（告警）；
//     默认 primary(96/babeld 默认) + backup(384) 因 cost 有落差，不应触发。
//   - 无 primary 告警：一对节点的链路全为 backup（例如角色翻转后）。
//   - D71 重定域：无 role 的同方向重复边仍告警，消息建议改用 role: "backup"；backup 边不触发。
//   - 接口名唯一性（不变式 N4）：同节点上所有 primary/backup 接口名不得冲突。
//
// 断言风格沿用相邻验证器测试：按稳定的字段前缀（edges[ / nodes[…].name）与稳定的中文/英文
// 片段做子串匹配，并同时断言「应触发」与「不应触发」，避免空洞通过。

// --- 跨字段 + 消息的稳定片段匹配辅助 ---

// errMatching 报告是否存在某条错误，其 Field 含 fieldFrag 且 Message 含 msgFrag。
// 任一片段为空表示该维度不约束。
func errMatching(result *ValidationResult, fieldFrag, msgFrag string) bool {
	for _, e := range result.Errors {
		if (fieldFrag == "" || containsSubstring(e.Field, fieldFrag)) &&
			(msgFrag == "" || containsSubstring(e.Message, msgFrag)) {
			return true
		}
	}
	return false
}

// warnMatching 报告是否存在某条告警，其 Field 含 fieldFrag 且 Message 含 msgFrag。
func warnMatching(result *ValidationResult, fieldFrag, msgFrag string) bool {
	for _, w := range result.Warnings {
		if (fieldFrag == "" || containsSubstring(w.Field, fieldFrag)) &&
			(msgFrag == "" || containsSubstring(w.Message, msgFrag)) {
			return true
		}
	}
	return false
}

// parallelBaseTopology 是并行链路测试的合法基线：两台公网可达的 router（避免 NAT 告警），
// 一对 primary + backup 边（A->B）。两端都 HasPublicIP，edge 带 endpoint_host 但目标节点未声明
// public_endpoints，故不会触发端点一致性告警。
//
// backupID 暴露给调用方以便重建 backup 的接口名；mutate 允许在基线上注入单点违例。
func parallelBaseTopology() *model.Topology {
	topo := &model.Topology{
		Project: model.Project{ID: "pl-001", Name: "Parallel Links"},
		Domains: []model.Domain{{
			ID: "domain-1", Name: "test", CIDR: "10.10.0.0/24",
			AllocationMode: "auto", RoutingMode: "babel",
		}},
		Nodes: []model.Node{
			{
				ID: "node-a", Name: "alpha", Hostname: "alpha.example.com",
				Platform: "debian", Role: "router", DomainID: "domain-1",
				OverlayIP: "10.10.0.1",
				Capabilities: model.NodeCapabilities{
					CanAcceptInbound: true, CanForward: true, HasPublicIP: true,
				},
			},
			{
				ID: "node-b", Name: "beta", Hostname: "beta.example.com",
				Platform: "debian", Role: "router", DomainID: "domain-1",
				OverlayIP: "10.10.0.2",
				Capabilities: model.NodeCapabilities{
					CanAcceptInbound: true, CanForward: true, HasPublicIP: true,
				},
			},
		},
		Edges: []model.Edge{
			{
				ID: "e-ab", FromNodeID: "node-a", ToNodeID: "node-b",
				Type: "direct", EndpointHost: "beta.example.com",
				Transport: "udp", IsEnabled: true,
				// 空 role == primary class。
			},
			{
				ID: "e-ab-backup", FromNodeID: "node-a", ToNodeID: "node-b",
				Type: "direct", EndpointHost: "beta.example.com",
				Transport: "udp", IsEnabled: true,
				Role: model.EdgeRoleBackup,
			},
		},
	}
	return topo
}

// --- 1. role 枚举拒绝（schema 阶段） ---

// TestParallelLinks_RoleEnumRejected 验证非法 role 值被 schema 校验拒绝，而合法值放行。
func TestParallelLinks_RoleEnumRejected(t *testing.T) {
	// 非法 role：报错于 edges[0].role。
	topo := parallelBaseTopology()
	topo.Edges[0].Role = "tertiary"
	result := ValidateSchema(topo)
	assertHasError(t, result, "edges[0].role")

	// 合法 role（primary / backup / 空）：role 字段不得报错。
	for _, role := range []string{"", model.EdgeRolePrimary, model.EdgeRoleBackup} {
		ok := parallelBaseTopology()
		ok.Edges[0].Role = role
		res := ValidateSchema(ok)
		for _, e := range res.Errors {
			if containsSubstring(e.Field, "edges[0].role") {
				t.Errorf("合法 role %q 不应在 schema 阶段报错，却收到: %s", role, e.Error())
			}
		}
	}
}

// --- 2. 同一对节点至多一条显式 primary ---

// TestParallelLinks_MultipleExplicitPrimaryRejected 验证同一对节点出现两条显式 role:"primary"
// 边时报错；而「一条显式 primary + 一条 backup」是合法的，不应触发该错误。
func TestParallelLinks_MultipleExplicitPrimaryRejected(t *testing.T) {
	// 两条显式 primary（同一对节点）→ 报错。
	topo := parallelBaseTopology()
	topo.Edges[0].Role = model.EdgeRolePrimary
	topo.Edges[1].Role = model.EdgeRolePrimary
	topo.Edges[1].FromNodeID = "node-b" // 反向，仍是同一对节点（同一 pinKey）
	topo.Edges[1].ToNodeID = "node-a"
	topo.Edges[1].EndpointHost = "alpha.example.com"
	result := ValidateSemantic(topo)
	if !errMatching(result, "edges[", "primary") {
		t.Errorf("同一对节点出现两条显式 primary 边应报错（消息含 primary），实际错误: %v", result.Errors)
	}

	// 一条显式 primary + 一条 backup → 不应触发「多 primary」错误。
	ok := parallelBaseTopology()
	ok.Edges[0].Role = model.EdgeRolePrimary // primary
	// Edges[1] 已是 backup。
	res := ValidateSemantic(ok)
	if errMatching(res, "edges[", "primary") {
		t.Errorf("一条 primary + 一条 backup 不应触发多 primary 错误，实际错误: %v", res.Errors)
	}
}

// --- 3. client 边不得为 backup ---

// TestParallelLinks_BackupOnClientRejected 验证触及 client 节点的 backup 边被拒绝，
// 而普通（非 backup）client 边不触发该错误。
func TestParallelLinks_BackupOnClientRejected(t *testing.T) {
	// 构造一个 client -> router 的拓扑，把出站边标成 backup。
	clientTopo := func() *model.Topology {
		return &model.Topology{
			Project: model.Project{ID: "pl-client", Name: "Client Backup"},
			Domains: []model.Domain{{
				ID: "domain-1", Name: "test", CIDR: "10.10.0.0/24",
				AllocationMode: "auto", RoutingMode: "babel",
			}},
			Nodes: []model.Node{
				{
					ID: "router-1", Name: "router", Role: "router", DomainID: "domain-1",
					OverlayIP:    "10.10.0.1",
					Capabilities: model.NodeCapabilities{CanAcceptInbound: true, CanForward: true, HasPublicIP: true},
				},
				{
					ID: "client-1", Name: "laptop", Role: "client", DomainID: "domain-1",
					OverlayIP: "10.10.0.9",
				},
			},
			Edges: []model.Edge{
				{
					ID: "e-cli", FromNodeID: "client-1", ToNodeID: "router-1",
					Type: "public-endpoint", EndpointHost: "router.example.com",
					Transport: "udp", IsEnabled: true,
				},
			},
		}
	}

	// backup client 边 → 报错（消息点名 client 与 backup）。
	bad := clientTopo()
	bad.Edges[0].Role = model.EdgeRoleBackup
	badRes := ValidateSemantic(bad)
	if !errMatching(badRes, "edges[", "backup") && !errMatching(badRes, "edges[", "client") {
		t.Errorf("client 边设为 backup 应报错（消息含 backup/client），实际错误: %v", badRes.Errors)
	}

	// 普通 client 边（无 role）→ 不触发 backup-on-client 错误。
	good := clientTopo()
	goodRes := ValidateSemantic(good)
	if errMatching(goodRes, "edges[", "backup") {
		t.Errorf("普通 client 边不应触发 backup-on-client 错误，实际错误: %v", goodRes.Errors)
	}
}

// --- 4. 等代价告警 vs. 默认 primary+backup 不告警 ---

// TestParallelLinks_EqualCostWarning 验证一对多链路若全部解析为相同 cost 则告警；
// 而默认 primary(无显式 cost) + backup(384) 因有 cost 落差，绝不触发等代价告警。
func TestParallelLinks_EqualCostWarning(t *testing.T) {
	// 两条链路显式设置相同 cost（同一对节点：一条 primary、一条 backup，但 priority 相等）→ 告警。
	equal := parallelBaseTopology()
	equal.Edges[0].Priority = 200 // primary
	equal.Edges[1].Priority = 200 // backup，但显式 cost 与 primary 相同 → 无故障切换偏好
	equalRes := ValidateSemantic(equal)
	if !warnMatching(equalRes, "", "") {
		t.Fatalf("等代价拓扑应至少产生一条告警，实际无任何告警")
	}
	// 必须存在一条与「代价/cost」相关的等代价告警。匹配稳定片段：cost 或 代价。
	if !warnMatching(equalRes, "", "cost") && !warnMatching(equalRes, "", "代价") {
		t.Errorf("两条等代价链路应触发等代价告警（消息含 cost/代价），实际告警: %v", equalRes.Warnings)
	}

	// 默认 primary + backup（primary 无 cost → 0/babeld 默认；backup → 384）：cost 有落差，
	// 不应触发等代价告警。
	gap := parallelBaseTopology() // Edges[0] 空 role、Edges[1] backup，均无显式 priority
	gapRes := ValidateSemantic(gap)
	if warnMatching(gapRes, "", "cost") || warnMatching(gapRes, "", "代价") {
		t.Errorf("默认 primary(96/默认) + backup(384) 有 cost 落差，不应触发等代价告警，实际告警: %v", gapRes.Warnings)
	}
}

// --- 5. 无 primary 告警 ---

// TestParallelLinks_NoPrimaryWarning 验证一对节点的链路全为 backup（无任何 primary class 边）
// 时告警；而存在 primary class 边的对不触发该告警。
func TestParallelLinks_NoPrimaryWarning(t *testing.T) {
	// 一对节点全 backup（把基线里本属 primary class 的 e-ab 也翻成 backup，赋新 id 使其各自成链）。
	noPrimary := parallelBaseTopology()
	noPrimary.Edges[0].Role = model.EdgeRoleBackup
	noPrimary.Edges[0].ID = "e-ab-bk0"
	// Edges[1] 已是 backup。
	noPrimaryRes := ValidateSemantic(noPrimary)
	if !warnMatching(noPrimaryRes, "", "primary") {
		t.Errorf("一对节点全为 backup（无 primary）应告警（消息含 primary），实际告警: %v", noPrimaryRes.Warnings)
	}

	// 含 primary class 边的对（基线本身）→ 不触发无 primary 告警。
	hasPrimary := parallelBaseTopology() // Edges[0] 空 role == primary class
	hasPrimaryRes := ValidateSemantic(hasPrimary)
	for _, w := range hasPrimaryRes.Warnings {
		// 仅当告警同时点名「无 primary」语义时才算违例；用较强的片段「无」+「primary」近似。
		if containsSubstring(w.Message, "primary") && containsSubstring(w.Message, "无") {
			t.Errorf("存在 primary class 边的对不应触发无 primary 告警，实际告警: %s", w.Message)
		}
	}
}

// --- 6. D71 重定域：无 role 同向重复边告警（建议 backup）；backup 边不触发 ---

// TestParallelLinks_D71DuplicateStillWarnsSuggestsBackup 验证 D71 重定域：
//   - 无 role 的同方向重复边仍触发重复边告警，且消息建议改用 role: "backup"；
//   - 一条 primary + 一条 backup（同方向）不再触发重复边告警（backup 是被支持的并行链路用法）。
func TestParallelLinks_D71DuplicateStillWarnsSuggestsBackup(t *testing.T) {
	// 两条无 role 的同方向边（A->B）→ 重复边告警，消息建议 backup。
	dup := parallelBaseTopology()
	dup.Edges[1].Role = "" // 去掉 backup，使两条都是无 role 的同向边
	dupRes := ValidateSemantic(dup)
	if !warnMatching(dupRes, "edges[", "backup") {
		t.Errorf("无 role 的同方向重复边应告警且建议 role: backup（消息含 backup），实际告警: %v", dupRes.Warnings)
	}

	// 一条无 role primary + 一条 backup（同方向）→ 不再触发「重复边」告警。
	// 用 D71 历史消息里的稳定片段「只有首条生效」判定该告警是否被错误触发。
	mixed := parallelBaseTopology() // Edges[0] 空 role、Edges[1] backup
	mixedRes := ValidateSemantic(mixed)
	if warnMatching(mixedRes, "edges[", "只有首条生效") {
		t.Errorf("一条 primary + 一条 backup 不应再触发 D71 重复边告警，实际告警: %v", mixedRes.Warnings)
	}
}

// --- 7. 接口名唯一性（不变式 N4） ---

// TestParallelLinks_BackupInterfaceNamesDistinct 验证同一对节点之间的两条 backup 边
// 生成互异的 WireGuard 接口名（edge-aware 命名按 edge.ID 折入哈希），从而满足 N4 的前提；
// 并断言 N4 校验路径在合法拓扑上不误报。
//
// 16 位哈希后缀使构造一个真实冲突不切实际，故本测试改为断言「两条 backup 的接口名 DISTINCT」
// （命名权威层面的不冲突），并验证 N4 校验在合法的 primary+backup 拓扑上不产生接口名唯一性错误。
func TestParallelLinks_BackupInterfaceNamesDistinct(t *testing.T) {
	const (
		backupID1 = "e-ab-bk1"
		backupID2 = "e-ab-bk2"
	)
	// 命名权威：同一远端、不同 edge.ID 的两条 backup 接口名必须不同。
	name1 := naming.WgInterfaceNameForEdge("beta", backupID1, true)
	name2 := naming.WgInterfaceNameForEdge("beta", backupID2, true)
	if name1 == name2 {
		t.Fatalf("不同 edge.ID 的两条 backup 接口名应互异，实际都为 %q", name1)
	}
	// backup 接口名也必须区别于 primary 接口名。
	if name1 == naming.WgInterfaceName("beta") {
		t.Errorf("backup 接口名不应与 primary 接口名相同：%q", name1)
	}

	// N4 校验路径：一个含 primary + 两条 backup（接口名互异）的合法拓扑不得产生接口名唯一性错误。
	topo := parallelBaseTopology()
	topo.Edges[1].ID = backupID1 // 第一条 backup
	topo.Edges = append(topo.Edges, model.Edge{
		ID: backupID2, FromNodeID: "node-a", ToNodeID: "node-b",
		Type: "direct", EndpointHost: "beta.example.com",
		Transport: "udp", IsEnabled: true, Role: model.EdgeRoleBackup,
	})
	res := ValidateSemantic(topo)
	// 不得出现「接口名」冲突类错误（N4 的稳定片段）。
	if errMatching(res, "", "接口名") {
		t.Errorf("接口名互异的 primary + 两条 backup 拓扑不应触发 N4 接口名唯一性错误，实际错误: %v", res.Errors)
	}
}
