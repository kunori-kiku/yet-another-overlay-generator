package renderer

import (
	"strings"
	"testing"

	"github.com/kunorikiku/yet-another-overlay-generator/internal/compiler"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/model"
)

// robustnessTestNode 返回一个带转发能力的 router 节点，用于安装脚本健壮性测试。
func robustnessTestNode() *model.Node {
	return &model.Node{
		ID:        "node-1",
		Name:      "alpha",
		Role:      "router",
		Platform:  "debian",
		OverlayIP: "10.11.0.1",
		Capabilities: model.NodeCapabilities{
			CanForward: true,
		},
	}
}

// robustnessTestPeers 返回两个 per-peer 接口，确保 wg-quick 启动块与 SNAT 块
// 都按多接口/多 CIDR 的形态被渲染。
func robustnessTestPeers() []compiler.PeerInfo {
	return []compiler.PeerInfo{
		{NodeID: "n2", NodeName: "beta", InterfaceName: "wg-beta",
			ListenPort: 51820, LocalTransitIP: "10.10.0.1", LocalLinkLocal: "fe80::1"},
		{NodeID: "n3", NodeName: "gamma", InterfaceName: "wg-gamma",
			ListenPort: 51821, LocalTransitIP: "10.10.0.3", LocalLinkLocal: "fe80::3"},
	}
}

// TestRenderInstallScript_D52_IptablesLoopDelete 验证 D52：iptables 的 SNAT 清理
// 不再按精确规则（含 --to-source <当前 overlay IP>）删除，而是解析 iptables-save，
// 把每条匹配 wg 接口 + transit 源池的 POSTROUTING SNAT 规则整条删除，无论 --to-source
// 是什么。这样 overlay IP 变更后重装/卸载都能清掉留下的旧规则，避免错误的源改写。
func TestRenderInstallScript_D52_IptablesLoopDelete(t *testing.T) {
	script, err := RenderInstallScript(robustnessTestNode(), robustnessTestPeers(), true)
	if err != nil {
		t.Fatalf("渲染失败: %v", err)
	}

	// 默认池下，loop-delete 应基于 iptables-save 解析 + 整条删除。断言稳定的模板子串：
	// 管道头（iptables-save），顺序无关的链式 grep -F 过滤（POSTROUTING / SNAT / 出接口
	// wg-+ / 源池），把 -A 改写成 -D 的替换，以及在删除分支里调用 iptables -t nat 删除整条规则。
	loopDeleteFragments := []string{
		`iptables-save -t nat 2>/dev/null \`,
		`| grep -E '^-A POSTROUTING '`,
		`| grep -F -- '-j SNAT'`,
		`| grep -F -- '-o wg-+'`,
		`| grep -F -- '-s 10.10.0.0/24'`,
		`_snat_del="${_snat_rule/#-A/-D}"`,
		`iptables -t nat $_snat_del 2>/dev/null || true`,
	}
	for _, frag := range loopDeleteFragments {
		if !strings.Contains(script, frag) {
			t.Errorf("D52: 缺少 iptables-save loop-delete 片段:\n  %q", frag)
		}
	}

	// loop-delete 必须在两个清理上下文里各出现：安装前的 _overlay_snat_cleanup 函数，
	// 以及卸载段的 "Remove overlay SNAT rule and service" 块。统计管道头出现次数。
	pipeHead := `iptables-save -t nat 2>/dev/null \`
	if got := strings.Count(script, pipeHead); got < 2 {
		t.Errorf("D52: loop-delete 应同时出现在安装清理与卸载清理两处，实际出现 %d 次", got)
	}

	// 关键否定断言：旧的「精确匹配删除」形式（带引号的 -o "wg-+" 且含 --to-source）
	// 不应再出现在任何 *清理* 路径里。systemd 持久化单元用的是不带引号的 -o wg-+，
	// 不会与此子串冲突，所以这条断言专门盯住被 D52 移除的清理写法。
	staleExactDelete := `iptables -t nat -D POSTROUTING -o "wg-+"`
	if strings.Contains(script, staleExactDelete) {
		t.Errorf("D52 回归: 清理路径仍使用精确匹配删除（含 --to-source），应改为 loop-delete:\n  %q", staleExactDelete)
	}
}

// TestRenderInstallScript_D53_WgQuickFailureTolerant 验证 D53：Phase 3 启动每个
// WireGuard 接口时失败可容忍——用 `if ! wg-quick up ...; then` 收集失败（不被 set -e
// 直接中止），向 stderr 告警并继续，让 babeld 等后续步骤照常执行；脚本末尾打印失败
// 汇总并在有失败时以非零码退出（部署工具仍能感知失败），但退出在其余步骤之后。
func TestRenderInstallScript_D53_WgQuickFailureTolerant(t *testing.T) {
	script, err := RenderInstallScript(robustnessTestNode(), robustnessTestPeers(), true)
	if err != nil {
		t.Fatalf("渲染失败: %v", err)
	}

	// 失败累加器初始化。
	if !strings.Contains(script, `FAILED_INTERFACES=""`) {
		t.Errorf("D53: 缺少 FAILED_INTERFACES 累加器初始化")
	}

	// 每个接口必须用 if ! wg-quick up 形式（set -e 安全），失败时累加并告警。
	tolerantFragments := []string{
		`if ! wg-quick up "wg-beta"; then`,
		`if ! wg-quick up "wg-gamma"; then`,
		`FAILED_INTERFACES="$FAILED_INTERFACES wg-beta"`,
		`FAILED_INTERFACES="$FAILED_INTERFACES wg-gamma"`,
		`continuing with remaining setup" >&2`,
	}
	for _, frag := range tolerantFragments {
		if !strings.Contains(script, frag) {
			t.Errorf("D53: 缺少失败容忍片段:\n  %q", frag)
		}
	}

	// 绝不能再出现「裸」的 wg-quick up（无 if 守护）——那会在 set -e 下中止脚本。
	// 渲染后每个接口对应一行启动；裸形式形如 `\nwg-quick up "wg-beta"\n`。
	for _, iface := range []string{"wg-beta", "wg-gamma"} {
		bare := "\nwg-quick up \"" + iface + "\""
		if strings.Contains(script, bare) {
			t.Errorf("D53 回归: 接口 %s 仍以裸 wg-quick up 启动（无 set -e 守护）", iface)
		}
	}

	// 末尾汇总块：有失败时打印清单并以非零码退出。
	summaryFragments := []string{
		`if [ -n "$FAILED_INTERFACES" ]; then`,
		`the following WireGuard interface(s) failed to start:$FAILED_INTERFACES" >&2`,
		`exit 1`,
	}
	for _, frag := range summaryFragments {
		if !strings.Contains(script, frag) {
			t.Errorf("D53: 缺少末尾失败汇总片段:\n  %q", frag)
		}
	}

	// 顺序：babeld 配置必须在 wg-quick 启动块之后、汇总退出之前，证明「半启动」不会发生
	// （即便接口失败，babeld 也已配置；非零退出发生在最后）。
	startIdx := strings.Index(script, `FAILED_INTERFACES=""`)
	babelIdx := strings.Index(script, "Configuring babeld systemd service")
	summaryIdx := strings.Index(script, `if [ -n "$FAILED_INTERFACES" ]; then`)
	if startIdx < 0 || babelIdx < 0 || summaryIdx < 0 {
		t.Fatalf("D53: 缺少关键锚点 (start=%d babel=%d summary=%d)", startIdx, babelIdx, summaryIdx)
	}
	if !(startIdx < babelIdx && babelIdx < summaryIdx) {
		t.Errorf("D53: 顺序应为 wg启动(%d) → babeld配置(%d) → 失败汇总退出(%d)", startIdx, babelIdx, summaryIdx)
	}
}

// parallelLinksPeers 返回指向同一对端的两条并行链路（primary + backup），接口名互异，
// 监听端口 / transit 互异——模拟节点对某一邻居同时持有 primary 与 backup 隧道。
func parallelLinksPeers() []compiler.PeerInfo {
	return []compiler.PeerInfo{
		{NodeID: "n2", NodeName: "beta", InterfaceName: "wg-beta",
			ListenPort: 51820, LocalTransitIP: "10.10.0.1", LocalLinkLocal: "fe80::1"},
		// backup：edge-aware 接口名（形态不同即可），独立端口与 transit。
		{NodeID: "n2", NodeName: "beta", InterfaceName: "wg-beta-bk1",
			ListenPort: 51821, LocalTransitIP: "10.10.0.3", LocalLinkLocal: "fe80::3", LinkCost: 384},
	}
}

// TestRenderInstallScript_ParallelLinks_BothInterfacesEveryPhase 验证并行链路（primary + backup）
// 节点的安装脚本在每一个 per-interface 阶段都同时列出两条接口：
//   - Phase 3 启动（D53 的 `if ! wg-quick up "<iface>"`）
//   - Phase 3 开机自启（systemctl enable wg-quick@"<iface>"）
//   - 卸载段的停止 / 禁用 / 删除配置（wg-quick down / systemctl disable / rm 配置文件）
//
// 安装脚本按 PeerInfo 列表逐接口展开模板的 {{ range .WgInterfaces }} 区段，因此两条链路
// （两个 InterfaceName）都必须在每个 per-interface 区段各出现一次，缺一即意味着某条隧道
// 不会被启动 / 启用 / 清理。
func TestRenderInstallScript_ParallelLinks_BothInterfacesEveryPhase(t *testing.T) {
	script, err := RenderInstallScript(robustnessTestNode(), parallelLinksPeers(), true)
	if err != nil {
		t.Fatalf("渲染失败: %v", err)
	}

	ifaces := []string{"wg-beta", "wg-beta-bk1"}

	for _, iface := range ifaces {
		// Phase 3 启动：D53 容错形式的 wg-quick up。
		if !strings.Contains(script, `if ! wg-quick up "`+iface+`"; then`) {
			t.Errorf("启动阶段缺少接口 %s 的 wg-quick up 行", iface)
		}
		// Phase 3 开机自启。
		if !strings.Contains(script, `systemctl enable wg-quick@"`+iface+`"`) {
			t.Errorf("启动阶段缺少接口 %s 的 systemctl enable 行", iface)
		}
		// 卸载段停止：wg-quick down。
		if !strings.Contains(script, `wg-quick down "`+iface+`"`) {
			t.Errorf("卸载/清理阶段缺少接口 %s 的 wg-quick down 行", iface)
		}
		// 卸载段禁用 systemd unit。
		if !strings.Contains(script, `systemctl disable "wg-quick@`+iface+`"`) {
			t.Errorf("卸载/清理阶段缺少接口 %s 的 systemctl disable 行", iface)
		}
		// 配置文件清理（卸载段与 Phase 0 各有一次删除）。
		if !strings.Contains(script, `/etc/wireguard/`+iface+`.conf`) {
			t.Errorf("脚本缺少接口 %s 的配置文件路径引用", iface)
		}
	}

	// 非空门禁：backup 接口（wg-beta-bk1）确实是新增的——它必须区别于 primary（wg-beta），
	// 否则模板可能只展开了一条链路而测试假性通过。统计两条 up 行各出现。
	if strings.Count(script, `if ! wg-quick up "wg-beta"; then`) < 1 ||
		strings.Count(script, `if ! wg-quick up "wg-beta-bk1"; then`) < 1 {
		t.Errorf("primary 与 backup 两条接口的启动行都必须出现且各异，实际脚本:\n%s", script)
	}
}

// TestRenderClientInstallScript_RobustnessUnaffected 验证 client 安装脚本不受
// D52/D53 改动影响：client 走单接口 wg0、无 Babel、无 SNAT，因此既不应出现
// per-peer 的 FAILED_INTERFACES 容错块，也不应出现 iptables-save loop-delete。
// client 仍保持其原有的单接口 wg-quick up 行为。
func TestRenderClientInstallScript_RobustnessUnaffected(t *testing.T) {
	node := &model.Node{
		ID:        "client-1",
		Name:      "laptop",
		Role:      "client",
		Platform:  "debian",
		OverlayIP: "10.11.0.9",
	}

	script, err := RenderClientInstallScript(node)
	if err != nil {
		t.Fatalf("渲染失败: %v", err)
	}

	// client 不引入 per-peer 的失败累加器。
	if strings.Contains(script, "FAILED_INTERFACES") {
		t.Errorf("client 脚本不应包含 per-peer 的 FAILED_INTERFACES 容错块")
	}

	// client 无 SNAT，因此不应出现 iptables-save loop-delete。
	if strings.Contains(script, "iptables-save -t nat") {
		t.Errorf("client 脚本不应包含 iptables-save loop-delete（client 无 SNAT）")
	}

	// client 仍以单接口 wg0 启动（原有行为不变）。
	if !strings.Contains(script, `wg-quick up "wg0"`) {
		t.Errorf("client 脚本应保持单接口 wg0 的启动")
	}
}
