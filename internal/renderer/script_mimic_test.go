package renderer

import (
	"strings"
	"testing"

	"github.com/kunorikiku/yet-another-overlay-generator/internal/compiler"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/model"
)

// mimicRenderNode 返回一个带转发能力的 debian router 节点，用于 mimic 安装脚本测试。
func mimicRenderNode() *model.Node {
	return &model.Node{
		ID:        "node-1",
		Name:      "alpha",
		Role:      "router",
		Platform:  "debian",
		OverlayIP: "10.50.0.1",
		Capabilities: model.NodeCapabilities{
			CanForward: true,
		},
	}
}

// TestRenderInstallScript_MimicPeer_ProvisionsMimic 覆盖契约 item 2：
// 当节点有 mimic peer（PeerInfo.Mimic==true）时，安装脚本必须装配 mimic——
// 包大致包含：mimic 包安装、egress NIC 运行时探测、/etc/mimic 配置写入、
// 每端口一条 filter = local= 行（带该接口的监听端口）、mimic@<egress> 启用、
// 以及卸载段的 mimic 拆除。
func TestRenderInstallScript_MimicPeer_ProvisionsMimic(t *testing.T) {
	node := mimicRenderNode()
	peers := []compiler.PeerInfo{
		// mimic 接口：监听端口 51820（应进入 filter 行）。
		{NodeID: "node-2", NodeName: "beta", InterfaceName: "wg-beta",
			ListenPort: 51820, LocalTransitIP: "10.10.0.1", LocalLinkLocal: "fe80::1",
			Mimic: true, MTU: 1408},
	}

	script, err := RenderInstallScript(node, peers, true)
	if err != nil {
		t.Fatalf("渲染失败: %v", err)
	}

	// 必须包含的 mimic 装配片段（存在性断言）。
	required := []string{
		// 1) mimic 包安装（Debian 路径）
		"ensure_pkg mimic",
		// 2) egress NIC + IP 运行时探测（mimic 附着在默认路由接口，非 wg 接口）
		"ip route show default",
		"ip route get 1.1.1.1",
		// 3) /etc/mimic 配置目录与写入
		"mkdir -p /etc/mimic",
		"/etc/mimic/",
		// 4) 每端口一条 filter = local= 行，且携带该接口监听端口 51820
		"filter = local=",
		":51820",
		// 5) mimic@<egress> 启用并启动
		`systemctl enable --now "mimic@`,
		// 6) 卸载段的 mimic 拆除（停用 + 删配置）
		`systemctl disable --now "mimic@`,
		"rm -f \"/etc/mimic/",
	}
	for _, frag := range required {
		if !strings.Contains(script, frag) {
			t.Errorf("mimic 节点的安装脚本应包含片段 %q，实际缺失", frag)
		}
	}

	// 监听端口必须出现在 filter 行里（更强的关联断言：不是孤立地出现 51820）。
	if !strings.Contains(script, "filter = local=${MIMIC_EGRESS_IP}:51820") {
		t.Errorf("mimic filter 行应携带接口监听端口 51820（local=...:51820），实际缺失")
	}
}

// TestRenderInstallScript_MimicPorts_DedupSorted 覆盖：多个 mimic 接口的监听端口
// 在脚本里各下发一条 filter 行，去重且升序。非 mimic 接口的端口不得出现在 filter 中。
func TestRenderInstallScript_MimicPorts_DedupSorted(t *testing.T) {
	node := mimicRenderNode()
	peers := []compiler.PeerInfo{
		// 乱序 + 一个重复端口，验证去重与排序。
		{NodeID: "n3", NodeName: "gamma", InterfaceName: "wg-gamma",
			ListenPort: 51822, LocalTransitIP: "10.10.0.3", LocalLinkLocal: "fe80::3", Mimic: true, MTU: 1408},
		{NodeID: "n2", NodeName: "beta", InterfaceName: "wg-beta",
			ListenPort: 51820, LocalTransitIP: "10.10.0.1", LocalLinkLocal: "fe80::1", Mimic: true, MTU: 1408},
		{NodeID: "n2b", NodeName: "beta2", InterfaceName: "wg-beta2",
			ListenPort: 51820, LocalTransitIP: "10.10.0.5", LocalLinkLocal: "fe80::5", Mimic: true, MTU: 1408},
		// 非 mimic 接口：其端口 51999 绝不应出现在 filter 行中。
		{NodeID: "n4", NodeName: "delta", InterfaceName: "wg-delta",
			ListenPort: 51999, LocalTransitIP: "10.10.0.7", LocalLinkLocal: "fe80::7", Mimic: false, MTU: 0},
	}

	script, err := RenderInstallScript(node, peers, true)
	if err != nil {
		t.Fatalf("渲染失败: %v", err)
	}

	// 两个去重后的 mimic 端口各一条 filter 行。
	for _, port := range []string{":51820", ":51822"} {
		if c := strings.Count(script, "filter = local=${MIMIC_EGRESS_IP}"+port); c != 1 {
			t.Errorf("mimic 端口 %s 应恰好出现 1 条 filter 行，实际 %d", port, c)
		}
	}

	// 升序：51820 的 filter 行应在 51822 之前。
	i20 := strings.Index(script, "filter = local=${MIMIC_EGRESS_IP}:51820")
	i22 := strings.Index(script, "filter = local=${MIMIC_EGRESS_IP}:51822")
	if i20 < 0 || i22 < 0 || i20 >= i22 {
		t.Errorf("mimic filter 行应按端口升序排列（51820 在 51822 之前），实际 idx20=%d idx22=%d", i20, i22)
	}

	// 否定断言：非 mimic 接口的端口 51999 不得进入任何 filter 行。
	if strings.Contains(script, "filter = local=${MIMIC_EGRESS_IP}:51999") {
		t.Errorf("非 mimic 接口的端口 51999 不应出现在 mimic filter 行中")
	}
}

// TestRenderInstallScript_UdpOnly_NoMimic 覆盖契约 item 2 的反面：
// 仅有 udp peer（无 PeerInfo.Mimic==true）的节点，安装脚本不得包含任何 mimic 装配——
// 既不应出现 "/etc/mimic" 也不应出现 "mimic@"，更不应安装 mimic 包或下发 filter 行。
func TestRenderInstallScript_UdpOnly_NoMimic(t *testing.T) {
	node := mimicRenderNode()
	peers := []compiler.PeerInfo{
		{NodeID: "node-2", NodeName: "beta", InterfaceName: "wg-beta",
			ListenPort: 51820, LocalTransitIP: "10.10.0.1", LocalLinkLocal: "fe80::1", Mimic: false, MTU: 0},
		{NodeID: "node-3", NodeName: "gamma", InterfaceName: "wg-gamma",
			ListenPort: 51821, LocalTransitIP: "10.10.0.3", LocalLinkLocal: "fe80::3", Mimic: false, MTU: 0},
	}

	script, err := RenderInstallScript(node, peers, true)
	if err != nil {
		t.Fatalf("渲染失败: %v", err)
	}

	absent := []string{
		"/etc/mimic",
		"mimic@",
		"ensure_pkg mimic",
		"filter = local=",
		"Provisioning mimic",
	}
	for _, frag := range absent {
		if strings.Contains(script, frag) {
			t.Errorf("纯 udp 节点的安装脚本不应包含 mimic 片段 %q，但出现了", frag)
		}
	}
}
