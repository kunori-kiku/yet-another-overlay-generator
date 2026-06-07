package renderer

import (
	"strings"
	"testing"

	"github.com/kunorikiku/yet-another-overlay-generator/internal/compiler"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/model"
)

// snatTestPeers 是 SNAT 测试通用的最小 per-peer 接口列表。SNAT 规则与具体 peer
// 无关——它只关心节点所属域的 transit 池——所以一条最简单的 peer 足矣。
func snatTestPeers() []compiler.PeerInfo {
	return []compiler.PeerInfo{
		{NodeID: "n2", NodeName: "beta", InterfaceName: "wg-beta",
			ListenPort: 51820, LocalTransitIP: "10.20.0.1", LocalLinkLocal: "fe80::1"},
	}
}

// TestRenderInstallScript_SNAT_CustomTransitCIDR 验证 D38/D39：自定义 transit_cidr
// 的节点，其安装脚本里所有 SNAT 相关位置（nft/iptables 添加、清理、卸载，以及持久化的
// overlay-snat.service 的 ExecStart/ExecStop）都必须携带该自定义池，且绝不出现硬编码的
// 默认池 10.10.0.0/24——否则源地址修复会对自定义池静默失效。
func TestRenderInstallScript_SNAT_CustomTransitCIDR(t *testing.T) {
	const customCIDR = "10.20.0.0/24"

	topo := &model.Topology{
		Domains: []model.Domain{
			{ID: "d-custom", Name: "custom", CIDR: "10.21.0.0/24", TransitCIDR: customCIDR},
		},
		Nodes: []model.Node{
			{
				ID:        "node-1",
				Name:      "alpha",
				Role:      "router",
				Platform:  "debian",
				DomainID:  "d-custom",
				OverlayIP: "10.21.0.1",
				Capabilities: model.NodeCapabilities{
					CanForward: true,
				},
			},
		},
	}
	node := &topo.Nodes[0]

	transitCIDRs := NodeTransitCIDRs(topo, node)
	if len(transitCIDRs) != 1 || transitCIDRs[0] != customCIDR {
		t.Fatalf("NodeTransitCIDRs 应解析出自定义池 [%s]，实际 %v", customCIDR, transitCIDRs)
	}

	script, err := RenderInstallScript(node, snatTestPeers(), true, transitCIDRs...)
	if err != nil {
		t.Fatalf("渲染失败: %v", err)
	}

	// 自定义池必须出现在所有 SNAT 路径里：nft 规则、nft echo、iptables -C/-A、
	// iptables 清理、卸载段、以及 systemd 单元的 ExecStart/ExecStop。逐一断言其
	// 具体语法片段，确保不是「碰巧在某处出现」。
	requiredFragments := []string{
		// 安装阶段 nft 规则与 echo
		`oifname "wg-*" ip saddr ` + customCIDR + ` snat to 10.21.0.1`,
		`SNAT (nftables): transit ` + customCIDR + ` → 10.21.0.1`,
		// 安装阶段 iptables 检查/添加与 echo
		`iptables -t nat -C POSTROUTING -o "wg-+" -s ` + customCIDR + ` -j SNAT --to-source 10.21.0.1`,
		`iptables -t nat -A POSTROUTING -o "wg-+" -s ` + customCIDR + ` -j SNAT --to-source 10.21.0.1`,
		`SNAT (iptables): transit ` + customCIDR + ` → 10.21.0.1`,
		// 安装前清理（_overlay_snat_cleanup）：D52 改为链式 grep -F 按池循环删除
		// （与 --to-source 无关，陈旧 overlay IP 的规则同样会被清掉），断言按池过滤的片段。
		`grep -F -- '-s ` + customCIDR + `'`,
		// systemd 持久化单元（D39）
		`nft add rule inet overlay-snat postrouting oifname "wg-*" ip saddr ` + customCIDR + ` snat to 10.21.0.1`,
		`iptables -t nat -A POSTROUTING -o wg-+ -s ` + customCIDR + ` -j SNAT --to-source 10.21.0.1`,
		`iptables -t nat -D POSTROUTING -o wg-+ -s ` + customCIDR + ` -j SNAT --to-source 10.21.0.1`,
	}
	for _, frag := range requiredFragments {
		if !strings.Contains(script, frag) {
			t.Errorf("自定义 transit 池脚本缺少 SNAT 片段:\n  %q", frag)
		}
	}

	// 关键否定断言：硬编码默认池绝不能出现在任何位置。注释里提到 transit IP 形如
	// 10.10.0.x，但完整的池字符串 "10.10.0.0/24" 一旦出现就说明某处仍在硬编码。
	if strings.Contains(script, "10.10.0.0/24") {
		t.Errorf("自定义 transit 池脚本不应出现硬编码默认池 10.10.0.0/24（D38/D39 回归）")
	}

	// 卸载段同样必须按自定义池删除规则。
	uninstallIdx := strings.Index(script, "Uninstall All")
	cleanupIdx := strings.Index(script, "Remove overlay SNAT rule and service")
	if uninstallIdx < 0 || cleanupIdx < 0 || cleanupIdx < uninstallIdx {
		t.Fatalf("卸载段缺少 SNAT 清理块")
	}
}

// TestRenderInstallScript_SNAT_DefaultTransitCIDR 验证默认域节点仍渲染默认池
// 10.10.0.0/24（不传 transitCIDRs 的兼容路径，以及域 transit_cidr 留空回退）。
func TestRenderInstallScript_SNAT_DefaultTransitCIDR(t *testing.T) {
	const defaultCIDR = "10.10.0.0/24"

	topo := &model.Topology{
		Domains: []model.Domain{
			// transit_cidr 留空 → 回退默认池
			{ID: "d-default", Name: "default", CIDR: "10.11.0.0/24"},
		},
		Nodes: []model.Node{
			{
				ID:        "node-1",
				Name:      "alpha",
				Role:      "router",
				Platform:  "debian",
				DomainID:  "d-default",
				OverlayIP: "10.11.0.1",
				Capabilities: model.NodeCapabilities{
					CanForward: true,
				},
			},
		},
	}
	node := &topo.Nodes[0]

	transitCIDRs := NodeTransitCIDRs(topo, node)
	if len(transitCIDRs) != 1 || transitCIDRs[0] != defaultCIDR {
		t.Fatalf("NodeTransitCIDRs 对空 transit_cidr 应回退默认池 [%s]，实际 %v", defaultCIDR, transitCIDRs)
	}

	script, err := RenderInstallScript(node, snatTestPeers(), true, transitCIDRs...)
	if err != nil {
		t.Fatalf("渲染失败: %v", err)
	}

	requiredFragments := []string{
		`oifname "wg-*" ip saddr ` + defaultCIDR + ` snat to 10.11.0.1`,
		`iptables -t nat -A POSTROUTING -o "wg-+" -s ` + defaultCIDR + ` -j SNAT --to-source 10.11.0.1`,
		`nft add rule inet overlay-snat postrouting oifname "wg-*" ip saddr ` + defaultCIDR + ` snat to 10.11.0.1`,
		`iptables -t nat -D POSTROUTING -o wg-+ -s ` + defaultCIDR + ` -j SNAT --to-source 10.11.0.1`,
	}
	for _, frag := range requiredFragments {
		if !strings.Contains(script, frag) {
			t.Errorf("默认 transit 池脚本缺少 SNAT 片段:\n  %q", frag)
		}
	}

	// 自定义池不应泄漏进默认域脚本。
	if strings.Contains(script, "10.20.0.0/24") {
		t.Errorf("默认 transit 池脚本不应出现自定义池 10.20.0.0/24")
	}
}

// TestRenderInstallScript_SNAT_DefaultWhenNoCIDRPassed 验证可变参兼容路径：
// 既有三参调用方（不传 transitCIDRs）仍渲染默认池，行为与历史一致。
func TestRenderInstallScript_SNAT_DefaultWhenNoCIDRPassed(t *testing.T) {
	node := &model.Node{
		ID:        "node-1",
		Name:      "alpha",
		Role:      "router",
		Platform:  "debian",
		OverlayIP: "10.11.0.1",
		Capabilities: model.NodeCapabilities{
			CanForward: true,
		},
	}

	// 不传 transitCIDRs（沿用历史三参调用形式）。
	script, err := RenderInstallScript(node, snatTestPeers(), true)
	if err != nil {
		t.Fatalf("渲染失败: %v", err)
	}

	if !strings.Contains(script, `oifname "wg-*" ip saddr 10.10.0.0/24 snat to 10.11.0.1`) {
		t.Errorf("不传 transitCIDRs 时应兜底为默认池 10.10.0.0/24 的 SNAT 规则")
	}
}

// TestNodeTransitCIDRs_UnknownDomain 验证当节点的 DomainID 找不到对应域时，
// 解析函数安全回退到默认池而非返回空切片（避免脚本渲染出没有任何 SNAT 规则）。
func TestNodeTransitCIDRs_UnknownDomain(t *testing.T) {
	topo := &model.Topology{
		Domains: []model.Domain{
			{ID: "d-other", TransitCIDR: "10.30.0.0/24"},
		},
		Nodes: []model.Node{
			{ID: "node-1", DomainID: "d-missing"},
		},
	}
	got := NodeTransitCIDRs(topo, &topo.Nodes[0])
	if len(got) != 1 || got[0] != "10.10.0.0/24" {
		t.Errorf("未知域应回退默认池 [10.10.0.0/24]，实际 %v", got)
	}
}
