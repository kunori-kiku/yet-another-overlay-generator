package compiler

import (
	"testing"

	"github.com/kunorikiku/yet-another-overlay-generator/internal/model"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/naming"
)

// 本文件是并行链路（primary + backup 故障切换）在编译器分区的门禁，覆盖
// docs/spec/compiler/allocation-stability.md（Link identity with parallel edges）与
// docs/spec/artifacts/{naming.md,babel.md} 的契约：
//   - 一对节点的 primary + backup 编译成每侧两个 PeerInfo，接口名 / 监听端口 / transit IP 全部互异；
//   - 接口名由唯一命名权威 internal/naming 给出：primary == WgInterfaceName(remote)，
//     backup == WgInterfaceNameForEdge(remote, edgeID, true)；
//   - backup 链路未设 priority/weight 时 LinkCost == 384（babeld wired 默认 96 的 4 倍）；
//   - backup 上的显式 priority 覆盖 384；
//   - 传统的「无 role 的 A->B + B->A」反向对仍折叠成一条链路（每侧恰一个 PeerInfo，
//     接口名与改动前逐字节相同）。
//
// 这些断言都通过公共面（compiler.Compile、PeerInfo、model 字段、naming 包）进行，不触碰内部实现。

// findPeerByIface 在 peers 中按 InterfaceName 精确定位 PeerInfo。
// 并行链路下同一 NodeID 会出现多个 PeerInfo（primary 与各 backup），故不能再按 NodeID 查找，
// 接口名才是每条链路的唯一判据。
func findPeerByIface(peers []PeerInfo, iface string) *PeerInfo {
	for i := range peers {
		if peers[i].InterfaceName == iface {
			return &peers[i]
		}
	}
	return nil
}

// countPeersToRemote 统计 peers 中指向 remoteID 的 PeerInfo 数量（= 该节点对该对端的接口数）。
func countPeersToRemote(peers []PeerInfo, remoteID string) int {
	n := 0
	for i := range peers {
		if peers[i].NodeID == remoteID {
			n++
		}
	}
	return n
}

// parallelPairTopology 构造一对节点 A<->B、外加一条 primary class 边与一条 backup 边。
// backupID 用于让调用方拿到 backup 边的 ID 以重建其接口名；extraBackup 允许注入额外配置
// （例如在 backup 上设置 Priority）以测试 cost 覆盖。
func parallelPairTopology(backupID string, mutateBackup func(*model.Edge)) *model.Topology {
	primary := abEdge("e-ab", "node-a", "node-b", "beta.example.com")
	backup := backupEdge(backupID, "node-a", "node-b", "beta.example.com")
	if mutateBackup != nil {
		mutateBackup(&backup)
	}
	return &model.Topology{
		Project: model.Project{ID: "parallel-links", Name: "Parallel Links"},
		Domains: []model.Domain{stableDomain()},
		Nodes: []model.Node{
			stableRouterNode("node-a", "alpha", "10.50.0.1"),
			stableRouterNode("node-b", "beta", "10.50.0.2"),
		},
		Edges: []model.Edge{primary, backup},
	}
}

// TestParallelLinks_TwoDistinctInterfacesPerSide 验证 primary + backup 在每一侧都生成两个
// PeerInfo，且接口名 / 监听端口 / transit IP 三者全部互异；接口名来自唯一命名权威。
func TestParallelLinks_TwoDistinctInterfacesPerSide(t *testing.T) {
	c := NewCompiler()
	keys := stableKeys()

	const backupID = "e-ab-backup"
	topo := parallelPairTopology(backupID, nil)
	res, err := c.Compile(topo, keys)
	if err != nil {
		t.Fatalf("primary + backup 拓扑应能编译，实际报错: %v", err)
	}

	// 期望接口名（由唯一命名权威给出，验证编译器确实通过 naming 包派生）。
	primaryIfaceOnA := naming.WgInterfaceName("beta")                        // A 侧指向 beta 的 primary 接口
	backupIfaceOnA := naming.WgInterfaceNameForEdge("beta", backupID, true)  // A 侧指向 beta 的 backup 接口
	primaryIfaceOnB := naming.WgInterfaceName("alpha")                       // B 侧指向 alpha 的 primary 接口
	backupIfaceOnB := naming.WgInterfaceNameForEdge("alpha", backupID, true) // B 侧指向 alpha 的 backup 接口

	// primary 与 backup 的接口名必须不同，否则两条链路的配置会相互覆盖。
	if primaryIfaceOnA == backupIfaceOnA {
		t.Fatalf("A 侧 primary 与 backup 接口名不应相同：%q", primaryIfaceOnA)
	}

	// ---- A 侧：恰两个指向 B 的 PeerInfo ----
	aPeers := res.PeerMap["node-a"]
	if got := countPeersToRemote(aPeers, "node-b"); got != 2 {
		t.Fatalf("A 侧应有 2 个指向 B 的 PeerInfo（primary + backup），实际 %d 个", got)
	}
	aPrimary := findPeerByIface(aPeers, primaryIfaceOnA)
	aBackup := findPeerByIface(aPeers, backupIfaceOnA)
	if aPrimary == nil {
		t.Fatalf("A 侧应存在 primary 接口 %q，实际 peers: %+v", primaryIfaceOnA, aPeers)
	}
	if aBackup == nil {
		t.Fatalf("A 侧应存在 backup 接口 %q，实际 peers: %+v", backupIfaceOnA, aPeers)
	}

	// ---- B 侧：同样恰两个指向 A 的 PeerInfo ----
	bPeers := res.PeerMap["node-b"]
	if got := countPeersToRemote(bPeers, "node-a"); got != 2 {
		t.Fatalf("B 侧应有 2 个指向 A 的 PeerInfo（primary + backup），实际 %d 个", got)
	}
	bPrimary := findPeerByIface(bPeers, primaryIfaceOnB)
	bBackup := findPeerByIface(bPeers, backupIfaceOnB)
	if bPrimary == nil {
		t.Fatalf("B 侧应存在 primary 接口 %q，实际 peers: %+v", primaryIfaceOnB, bPeers)
	}
	if bBackup == nil {
		t.Fatalf("B 侧应存在 backup 接口 %q，实际 peers: %+v", backupIfaceOnB, bPeers)
	}

	// ---- 监听端口互异（同一节点上两条链路不得争用同一端口） ----
	if aPrimary.ListenPort == aBackup.ListenPort {
		t.Errorf("A 侧 primary 与 backup 监听端口应互异，实际都为 %d", aPrimary.ListenPort)
	}
	if bPrimary.ListenPort == bBackup.ListenPort {
		t.Errorf("B 侧 primary 与 backup 监听端口应互异，实际都为 %d", bPrimary.ListenPort)
	}

	// ---- transit IP 互异（两条链路是两套点对点地址） ----
	if aPrimary.LocalTransitIP == aBackup.LocalTransitIP {
		t.Errorf("A 侧 primary 与 backup 本端 transit IP 应互异，实际都为 %s", aPrimary.LocalTransitIP)
	}
	if aPrimary.RemoteTransitIP == aBackup.RemoteTransitIP {
		t.Errorf("A 侧 primary 与 backup 对端 transit IP 应互异，实际都为 %s", aPrimary.RemoteTransitIP)
	}
}

// TestParallelLinks_BackupDefaultLinkCost 验证 backup 链路未设 priority/weight 时 LinkCost == 384
// （docs/spec/artifacts/babel.md「Link cost resolution」第 2 条 backup preset），而同对的 primary
// 链路 LinkCost == 0（走 babeld 内置默认，渲染时省略 rxcost）。
func TestParallelLinks_BackupDefaultLinkCost(t *testing.T) {
	c := NewCompiler()
	keys := stableKeys()

	const backupID = "e-ab-backup"
	topo := parallelPairTopology(backupID, nil)
	res, err := c.Compile(topo, keys)
	if err != nil {
		t.Fatalf("primary + backup 拓扑应能编译，实际报错: %v", err)
	}

	aPeers := res.PeerMap["node-a"]
	aPrimary := findPeerByIface(aPeers, naming.WgInterfaceName("beta"))
	aBackup := findPeerByIface(aPeers, naming.WgInterfaceNameForEdge("beta", backupID, true))
	if aPrimary == nil || aBackup == nil {
		t.Fatalf("应同时找到 primary 与 backup PeerInfo，实际 peers: %+v", aPeers)
	}

	if aBackup.LinkCost != backupDefaultLinkCost {
		t.Errorf("backup 链路未设 priority/weight 时 LinkCost 应为 %d，实际 %d", backupDefaultLinkCost, aBackup.LinkCost)
	}
	if backupDefaultLinkCost != 384 {
		t.Errorf("backupDefaultLinkCost 常量应为 384（babeld wired 默认 96 的 4 倍），实际 %d", backupDefaultLinkCost)
	}
	// primary（无显式 cost）应为 0，与 backup 的 384 形成故障切换所需的 cost 落差。
	if aPrimary.LinkCost != 0 {
		t.Errorf("primary 链路未设 priority/weight 时 LinkCost 应为 0（交由角色预设/babeld 默认），实际 %d", aPrimary.LinkCost)
	}
}

// TestParallelLinks_ExplicitPriorityOverridesBackupDefault 验证 backup 上的显式 priority
// 覆盖 384 的 backup 预设（docs/spec/artifacts/babel.md「Link cost resolution」第 1 条
// explicit operator setting 优先级最高）。
func TestParallelLinks_ExplicitPriorityOverridesBackupDefault(t *testing.T) {
	c := NewCompiler()
	keys := stableKeys()

	const backupID = "e-ab-backup"
	const explicitCost = 250
	topo := parallelPairTopology(backupID, func(e *model.Edge) {
		e.Priority = explicitCost
	})
	res, err := c.Compile(topo, keys)
	if err != nil {
		t.Fatalf("带显式 priority 的 backup 拓扑应能编译，实际报错: %v", err)
	}

	aPeers := res.PeerMap["node-a"]
	aBackup := findPeerByIface(aPeers, naming.WgInterfaceNameForEdge("beta", backupID, true))
	if aBackup == nil {
		t.Fatalf("应找到 backup PeerInfo，实际 peers: %+v", aPeers)
	}

	if aBackup.LinkCost != explicitCost {
		t.Errorf("backup 上的显式 priority 应覆盖 384 预设，期望 LinkCost == %d，实际 %d", explicitCost, aBackup.LinkCost)
	}
	if aBackup.LinkCost == backupDefaultLinkCost {
		t.Errorf("显式 priority 存在时不应回退到 backup 预设 %d", backupDefaultLinkCost)
	}
}

// TestParallelLinks_LegacyReversePairOneLink 验证传统的「无 role 的 A->B + B->A」反向对
// 仍折叠成一条链路（unify rule 保留 legacy 语义）：每侧恰一个 PeerInfo，接口名与改动前
// 逐字节相同（== naming.WgInterfaceName(remote)，绝不走 backup 的 edge-aware 形态）。
func TestParallelLinks_LegacyReversePairOneLink(t *testing.T) {
	c := NewCompiler()
	keys := stableKeys()

	topo := &model.Topology{
		Project: model.Project{ID: "legacy-pair", Name: "Legacy Reverse Pair"},
		Domains: []model.Domain{stableDomain()},
		Nodes: []model.Node{
			stableRouterNode("node-a", "alpha", "10.50.0.1"),
			stableRouterNode("node-b", "beta", "10.50.0.2"),
		},
		// 无 role 的正反两条边——同属 primary class，折叠为一条双向隧道。
		Edges: []model.Edge{
			abEdge("e-ab", "node-a", "node-b", "beta.example.com"),
			abEdge("e-ba", "node-b", "node-a", "alpha.example.com"),
		},
	}
	res, err := c.Compile(topo, keys)
	if err != nil {
		t.Fatalf("传统反向对拓扑应能编译，实际报错: %v", err)
	}

	// A 侧恰一个指向 B 的 PeerInfo（正反边折叠为一条链路）。
	aPeers := res.PeerMap["node-a"]
	if got := countPeersToRemote(aPeers, "node-b"); got != 1 {
		t.Fatalf("传统反向对在 A 侧应恰有 1 个指向 B 的 PeerInfo，实际 %d 个: %+v", got, aPeers)
	}
	// B 侧同样恰一个指向 A 的 PeerInfo。
	bPeers := res.PeerMap["node-b"]
	if got := countPeersToRemote(bPeers, "node-a"); got != 1 {
		t.Fatalf("传统反向对在 B 侧应恰有 1 个指向 A 的 PeerInfo，实际 %d 个: %+v", got, bPeers)
	}

	// 接口名必须与改动前逐字节相同（primary class 走 WgInterfaceName，不带 edge 区分）。
	if aPeers[0].InterfaceName != naming.WgInterfaceName("beta") {
		t.Errorf("A 侧 primary class 接口名应为 %q，实际 %q", naming.WgInterfaceName("beta"), aPeers[0].InterfaceName)
	}
	if bPeers[0].InterfaceName != naming.WgInterfaceName("alpha") {
		t.Errorf("B 侧 primary class 接口名应为 %q，实际 %q", naming.WgInterfaceName("alpha"), bPeers[0].InterfaceName)
	}
}
