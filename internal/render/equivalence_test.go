package render

import (
	"strings"
	"testing"

	"github.com/kunorikiku/yet-another-overlay-generator/internal/compiler"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/model"
	"golang.zx2c4.com/wireguard/wgctrl/wgtypes"
)

// TestEntrypointParity 是「共享渲染入口」的等价性闸门。
//
// 在此 PR 之前，API（internal/api/handler.go）与 CLI（cmd/compiler）各自维护一份渲染逻辑，
// CLI 那份是退化实现：塞入字面量 FAKE_PRIVKEY_*、从不渲染 client 的 wg0.conf、不生成
// client 安装脚本、也不生成 deploy-all 脚本（审计主题 T6：D6 / D27–29 / D59）。本 PR 把
// GenerateKeys + All 抽到本共享包，两个入口现在都走 render.GenerateKeys → compiler.Compile →
// render.All 这一条完全相同的路径。本测试就锁定该路径必须产出的关键产物，任何回归到分叉
// 行为（漏渲 client、漏渲 deploy、再次出现 FAKE_）都会让它失败。
//
// 拓扑刻意同时含 router、peer、client 三种角色，确保覆盖到 render.All 内三条分支：
// per-peer WireGuard、client 单一 wg0、以及 client 与 per-peer 两种安装脚本模板。
//
// 三枚私钥在测试中一次性用 wgtypes.GeneratePrivateKey 生成并写到节点的 WireGuardPrivateKey
// 上（落入 GenerateKeys 情形 (a)：私钥在场则复用），从而让本次运行内的密钥确定且为真实
// WireGuard 私钥，渲染出的配置不含任何占位串。
func TestEntrypointParity(t *testing.T) {
	// 一次性生成三枚真实 WireGuard 私钥，分别钉到三个节点上，使 GenerateKeys 走「私钥在场
	// 则复用」分支（情形 a），渲染结果在本次运行内确定。
	routerKey := mustGenerateKey(t)
	peerKey := mustGenerateKey(t)
	clientKey := mustGenerateKey(t)

	topo := &model.Topology{
		Project: model.Project{ID: "parity-001", Name: "Entrypoint Parity", Version: "1"},
		Domains: []model.Domain{{
			ID: "domain-1", Name: "parity-net", CIDR: "10.40.0.0/24",
			AllocationMode: "auto", RoutingMode: "babel",
		}},
		Nodes: []model.Node{
			{
				ID: "router-1", Name: "router-1", Hostname: "router-1.example",
				Role: "router", DomainID: "domain-1",
				Capabilities: model.NodeCapabilities{
					CanAcceptInbound: true, CanForward: true, HasPublicIP: true,
				},
				PublicEndpoints: []model.PublicEndpoint{
					{ID: "router-1-ep", Host: "router-1.example", Port: 51820},
				},
				WireGuardPrivateKey: routerKey.String(),
			},
			{
				ID: "peer-1", Name: "peer-1",
				Role: "peer", DomainID: "domain-1",
				Capabilities: model.NodeCapabilities{
					CanAcceptInbound: false, CanForward: false, HasPublicIP: false,
				},
				WireGuardPrivateKey: peerKey.String(),
			},
			{
				ID: "client-1", Name: "client-1",
				Role: "client", DomainID: "domain-1",
				WireGuardPrivateKey: clientKey.String(),
			},
		},
		Edges: []model.Edge{
			// peer-1 -> router-1：peer 主动连公网 router（必须带 endpoint_host）。
			{ID: "e-peer", FromNodeID: "peer-1", ToNodeID: "router-1", Type: "public-endpoint",
				EndpointHost: "router-1.example", Transport: "udp", IsEnabled: true},
			// client-1 -> router-1：client 的唯一出站边（必须带 endpoint_host）。
			{ID: "e-client", FromNodeID: "client-1", ToNodeID: "router-1", Type: "public-endpoint",
				EndpointHost: "router-1.example", Transport: "udp", IsEnabled: true},
		},
	}

	// 走与 API/CLI 完全相同的共享路径。
	keys, err := GenerateKeys(topo)
	if err != nil {
		t.Fatalf("GenerateKeys 失败: %v", err)
	}

	// GenerateKeys 应复用钉好的私钥（情形 a），且由私钥派生公钥写回。
	if got := keys["router-1"].PrivateKey; got != routerKey.String() {
		t.Errorf("router-1 私钥应被原样复用，期望 %q，实际 %q", routerKey.String(), got)
	}
	if got := keys["router-1"].PublicKey; got != routerKey.PublicKey().String() {
		t.Errorf("router-1 公钥应由私钥派生，期望 %q，实际 %q", routerKey.PublicKey().String(), got)
	}

	c := compiler.NewCompiler()
	result, err := c.Compile(topo, keys)
	if err != nil {
		t.Fatalf("Compile 失败: %v", err)
	}

	if err := All(result, keys); err != nil {
		t.Fatalf("render.All 失败: %v", err)
	}

	// 断言 1：client 节点有 "client-1:wg0" 的 WireGuard 配置（client 模板，D27）。
	clientWG, ok := result.WireGuardConfigs["client-1:wg0"]
	if !ok {
		t.Fatalf("client 节点应有 %q 的 WireGuard 配置（client wg0 模板）；现有键：%v",
			"client-1:wg0", keysOf(result.WireGuardConfigs))
	}
	if !strings.Contains(clientWG, "wg0") {
		t.Errorf("client wg0 配置应提及接口名 wg0，实际内容：\n%s", clientWG)
	}

	// 断言 2：client 节点有安装脚本，且为 client 模板（含 wg0，D28/D29）。
	clientInstall, ok := result.InstallScripts["client-1"]
	if !ok {
		t.Fatalf("client 节点应有安装脚本；现有键：%v", keysOf(result.InstallScripts))
	}
	if !strings.Contains(clientInstall, "wg0") {
		t.Errorf("client 安装脚本应使用 client 模板（含 wg0），实际未出现 wg0")
	}

	// 断言 3：deploy 脚本出现在 deploy-all.sh / deploy-all.ps1 键下（D59）。
	if _, ok := result.DeployScripts["deploy-all.sh"]; !ok {
		t.Errorf("应生成 deploy-all.sh；现有键：%v", keysOf(result.DeployScripts))
	}
	if _, ok := result.DeployScripts["deploy-all.ps1"]; !ok {
		t.Errorf("应生成 deploy-all.ps1；现有键：%v", keysOf(result.DeployScripts))
	}

	// 断言 4：任何渲染产物都不得包含 FAKE_（CLI 旧的占位密钥彻底消失，D6）。
	assertNoFake(t, "WireGuardConfigs", result.WireGuardConfigs)
	assertNoFake(t, "BabelConfigs", result.BabelConfigs)
	assertNoFake(t, "SysctlConfigs", result.SysctlConfigs)
	assertNoFake(t, "InstallScripts", result.InstallScripts)
	assertNoFake(t, "DeployScripts", result.DeployScripts)
}

// mustGenerateKey 生成一枚真实 WireGuard 私钥，失败即终止测试。
func mustGenerateKey(t *testing.T) wgtypes.Key {
	t.Helper()
	key, err := wgtypes.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("生成 WireGuard 私钥失败: %v", err)
	}
	return key
}

// assertNoFake 断言 map 中没有任何值包含字面量 FAKE_（旧 CLI 占位密钥的标志，D6）。
func assertNoFake(t *testing.T, label string, m map[string]string) {
	t.Helper()
	for key, value := range m {
		if strings.Contains(value, "FAKE_") {
			t.Errorf("%s[%q] 不应包含占位串 FAKE_（D6 回归）", label, key)
		}
	}
}

// keysOf 返回 map 的键集合，仅用于断言失败时的诊断输出。
func keysOf(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
