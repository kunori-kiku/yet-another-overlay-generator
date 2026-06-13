package validator

import (
	"testing"

	"github.com/kunorikiku/yet-another-overlay-generator/internal/model"
)

// TestValidateSchema_NodeNameCharset 覆盖节点名称字符集校验（D15 纵深防御）。
// 节点名称会被派生为 WireGuard 接口名并被插值进以 root 身份执行的安装脚本，
// 因此含 shell 元字符（引号、反引号、$、; 等）的名称必须在 schema 阶段被拒绝，
// 而仅含字母、数字、空格、点、下划线、连字符的名称应当通过。
func TestValidateSchema_NodeNameCharset(t *testing.T) {
	cases := []struct {
		name        string
		nodeName    string
		expectError bool
	}{
		{name: "反引号命令注入", nodeName: "node`id`", expectError: true},
		{name: "美元符号命令替换", nodeName: "node$(whoami)", expectError: true},
		{name: "分号链式命令", nodeName: "node; rm -rf /", expectError: true},
		{name: "双引号闭合", nodeName: `node"evil`, expectError: true},
		{name: "单引号闭合", nodeName: "node'evil", expectError: true},
		{name: "干净的连字符名称", nodeName: "node-alpha", expectError: false},
		{name: "干净的带空格点下划线名称", nodeName: "Web 1.east_a", expectError: false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			topo := validTopology()
			topo.Nodes[0].Name = tc.nodeName
			result := ValidateSchema(topo)
			if tc.expectError {
				assertHasError(t, result, "nodes[0].name")
			} else {
				for _, e := range result.Errors {
					if contains(e.Field, "nodes[0].name") {
						t.Errorf("名称 %q 不应触发字符集错误，却得到：%s", tc.nodeName, e.Error())
					}
				}
			}
		})
	}
}

// TestValidateSchema_SSHFieldCharset 覆盖 SSH 字段字符集校验（D44）。
// ssh_host / ssh_alias / ssh_user 非空时会被插值进操作员本机执行的 bash 与
// PowerShell 部署脚本，含空白或 shell 元字符的取值必须被拒绝，干净取值应通过。
func TestValidateSchema_SSHFieldCharset(t *testing.T) {
	cases := []struct {
		name        string
		mutate      func(n *model.Node)
		field       string
		expectError bool
	}{
		{
			name:        "ssh_host 命令替换",
			mutate:      func(n *model.Node) { n.SSHHost = "host$(reboot)" },
			field:       "nodes[0].ssh_host",
			expectError: true,
		},
		{
			name:        "ssh_host 含空白",
			mutate:      func(n *model.Node) { n.SSHHost = "1.2.3.4 evil" },
			field:       "nodes[0].ssh_host",
			expectError: true,
		},
		{
			name:        "ssh_alias 反引号",
			mutate:      func(n *model.Node) { n.SSHAlias = "alias`id`" },
			field:       "nodes[0].ssh_alias",
			expectError: true,
		},
		{
			name:        "ssh_user 分号",
			mutate:      func(n *model.Node) { n.SSHUser = "root;reboot" },
			field:       "nodes[0].ssh_user",
			expectError: true,
		},
		{
			name:        "干净的 ssh_host",
			mutate:      func(n *model.Node) { n.SSHHost = "203.0.113.5" },
			field:       "nodes[0].ssh_host",
			expectError: false,
		},
		{
			name:        "干净的 ssh_host 含冒号与 at",
			mutate:      func(n *model.Node) { n.SSHHost = "user@host.example.com:2222" },
			field:       "nodes[0].ssh_host",
			expectError: false,
		},
		{
			name:        "干净的 ssh_user",
			mutate:      func(n *model.Node) { n.SSHUser = "deploy-user_1" },
			field:       "nodes[0].ssh_user",
			expectError: false,
		},
		{
			name:        "空 SSH 字段不报错",
			mutate:      func(n *model.Node) { n.SSHHost = ""; n.SSHAlias = ""; n.SSHUser = "" },
			field:       "nodes[0].ssh_host",
			expectError: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			topo := validTopology()
			tc.mutate(&topo.Nodes[0])
			result := ValidateSchema(topo)
			if tc.expectError {
				assertHasError(t, result, tc.field)
			} else {
				for _, e := range result.Errors {
					if contains(e.Field, tc.field) {
						t.Errorf("字段 %s 不应触发字符集错误，却得到：%s", tc.field, e.Error())
					}
				}
			}
		})
	}
}

// TestValidateSchema_SSHKeyPathCharset pins the ssh_key_path validation half of
// the deploy-script command-injection fix. ssh_key_path is spliced into the
// operator's bash + PowerShell deploy commands (ssh/scp -i <path>); unlike the
// connection fields it permits real path characters (/ \ ~ : space) but must
// still reject every shell metacharacter. A regression here reopens the
// injection path the renderer escaping also guards.
func TestValidateSchema_SSHKeyPathCharset(t *testing.T) {
	cases := []struct {
		name        string
		keyPath     string
		expectError bool
	}{
		// Hostile: shell metacharacters that enable injection.
		{"command substitution", `/keys/x$(reboot).pem`, true},
		{"powershell quote break", `/keys/k".pem`, true},
		{"backtick", "/keys/k`id`.pem", true},
		{"statement separator", `/keys/k.pem;reboot`, true},
		{"pipe", `/keys/k.pem|cat`, true},
		{"single quote", `/keys/k'.pem`, true},
		// Clean: realistic key paths on Linux and Windows.
		{"linux absolute path", `/home/user/.ssh/id_ed25519`, false},
		{"tilde home path", `~/.ssh/id_rsa`, false},
		{"relative path", `./keys/deploy.pem`, false},
		{"windows backslash path", `C:\Users\me\.ssh\id_rsa`, false},
		{"windows path with space", `C:/Users/John Doe/key.pem`, false},
		{"empty is allowed", ``, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			topo := validTopology()
			topo.Nodes[0].SSHKeyPath = tc.keyPath
			result := ValidateSchema(topo)
			if tc.expectError {
				assertHasError(t, result, "nodes[0].ssh_key_path")
			} else {
				for _, e := range result.Errors {
					if contains(e.Field, "nodes[0].ssh_key_path") {
						t.Errorf("ssh_key_path %q should be accepted, got: %s", tc.keyPath, e.Error())
					}
				}
			}
		})
	}
}

// portRangeTopology 构造一个最小拓扑：单个 router 节点，给定基准端口、hostname，
// 以及连向 peerCount 个对端 peer 的启用边。每条边都是一个去重节点对，
// 因此该 router 会获得 peerCount 个 per-peer 接口，生效监听端口范围为
// [base, base+peerCount-1]，用于覆盖生效端口越界校验（D11）。
func portRangeTopology(base, peerCount int, hostname string) *model.Topology {
	topo := &model.Topology{
		Project: model.Project{ID: "test-port", Name: "Port Range Test"},
		Domains: []model.Domain{
			{
				ID:             "domain-1",
				Name:           "test-network",
				CIDR:           "10.10.0.0/24",
				AllocationMode: "auto",
				RoutingMode:    "babel",
			},
		},
		Nodes: []model.Node{
			{
				ID:         "hub",
				Name:       "hub",
				Hostname:   hostname,
				Role:       "router",
				DomainID:   "domain-1",
				ListenPort: base,
				Capabilities: model.NodeCapabilities{
					CanAcceptInbound: true,
					CanForward:       true,
					HasPublicIP:      true,
				},
			},
		},
	}

	for i := 0; i < peerCount; i++ {
		peerID := "peer-" + itoaTest(i)
		topo.Nodes = append(topo.Nodes, model.Node{
			ID:       peerID,
			Name:     "peer-" + itoaTest(i),
			Role:     "router",
			DomainID: "domain-1",
			Capabilities: model.NodeCapabilities{
				CanAcceptInbound: true,
				CanForward:       true,
				HasPublicIP:      true,
			},
		})
		topo.Edges = append(topo.Edges, model.Edge{
			ID:         "edge-" + itoaTest(i),
			FromNodeID: "hub",
			ToNodeID:   peerID,
			Type:       "direct",
			Transport:  "udp",
			IsEnabled:  true,
		})
	}

	return topo
}

// transportTopology 构造一个两节点单边拓扑，边的 transport 由参数指定，用于
// 覆盖 tcp 保留值告警。
func transportTopology(transport string) *model.Topology {
	return &model.Topology{
		Project: model.Project{ID: "test-transport", Name: "Transport Test"},
		Domains: []model.Domain{
			{ID: "domain-1", Name: "net", CIDR: "10.10.0.0/24", AllocationMode: "auto", RoutingMode: "babel"},
		},
		Nodes: []model.Node{
			{ID: "a", Name: "a", Role: "router", DomainID: "domain-1", ListenPort: 51820,
				Capabilities: model.NodeCapabilities{CanAcceptInbound: true, CanForward: true, HasPublicIP: true}},
			{ID: "b", Name: "b", Role: "router", DomainID: "domain-1", ListenPort: 51820,
				Capabilities: model.NodeCapabilities{CanAcceptInbound: true, CanForward: true, HasPublicIP: true}},
		},
		Edges: []model.Edge{
			{ID: "edge-1", FromNodeID: "a", ToNodeID: "b", Type: "direct", Transport: transport, IsEnabled: true},
		},
	}
}

// TestValidateSchema_TcpTransportNoReservedWarning 覆盖 mimic-tcp-transport 落地后的新契约：
// tcp 现在是已实现的合法值（链路由 mimic 包裹），schema 阶段既不报 transport 错误，也不再
// 产生 v1.3.0 的"保留/未实现"告警（该告警已移除）。Linux 端点的语义校验由
// validateMimicTransport 负责（见 mimic_test.go），不在 schema 层。udp 同样无告警。
func TestValidateSchema_TcpTransportNoReservedWarning(t *testing.T) {
	for _, transport := range []string{"tcp", "udp"} {
		result := ValidateSchema(transportTopology(transport))
		for _, e := range result.Errors {
			if containsSubstring(e.Field, "transport") {
				t.Fatalf("%s 是合法传输值，不应产生 transport 错误，实际: %v", transport, result.Errors)
			}
		}
		for _, w := range result.Warnings {
			if containsSubstring(w.Field, "transport") {
				t.Errorf("%s 传输不应再产生 transport 告警（保留告警已移除），实际: %v", transport, result.Warnings)
			}
		}
	}
}

// itoaTest 是测试内的小整数转字符串助手，避免引入标准库 strconv 以保持测试自包含。
func itoaTest(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf []byte
	for n > 0 {
		buf = append([]byte{byte('0' + n%10)}, buf...)
		n /= 10
	}
	if neg {
		buf = append([]byte{'-'}, buf...)
	}
	return string(buf)
}

// TestValidateSemantic_EffectivePortRangeOverflow 覆盖生效监听端口越界（D11）：
// 基准端口 65530 的节点连向 8 个对端，会占用 8 个接口（端口 65530..65537），
// 最高端口 65537 超过 65535，必须报错。
func TestValidateSemantic_EffectivePortRangeOverflow(t *testing.T) {
	topo := portRangeTopology(65530, 8, "")
	result := ValidateSemantic(topo)
	assertHasError(t, result, "nodes[0].listen_port")
}

// TestValidateSemantic_EffectivePortRangeInBounds 验证基准端口 51820 的节点连向
// 8 个对端时（端口 51820..51827）不会触发越界错误。
func TestValidateSemantic_EffectivePortRangeInBounds(t *testing.T) {
	topo := portRangeTopology(51820, 8, "")
	result := ValidateSemantic(topo)
	for _, e := range result.Errors {
		if contains(e.Field, "nodes[0].listen_port") {
			t.Errorf("基准 51820 + 8 接口（51820-51827）不应越界，却得到：%s", e.Error())
		}
	}
}

// sameHostTopology 构造两个共享同一非空 hostname 的 router 节点，
// 各自的基准端口与对端接口数可独立设置，用于覆盖同主机生效范围重叠校验（D47）。
// 每个 hub 各连向独立的一组 peer，使两个 hub 的接口数互不干扰。
func sameHostTopology(hostname string, baseA, ifacesA, baseB, ifacesB int) *model.Topology {
	topo := &model.Topology{
		Project: model.Project{ID: "test-samehost", Name: "Same Host Test"},
		Domains: []model.Domain{
			{
				ID:             "domain-1",
				Name:           "test-network",
				CIDR:           "10.10.0.0/24",
				AllocationMode: "auto",
				RoutingMode:    "babel",
			},
		},
		Nodes: []model.Node{
			{
				ID:         "hub-a",
				Name:       "hub-a",
				Hostname:   hostname,
				Role:       "router",
				DomainID:   "domain-1",
				ListenPort: baseA,
				Capabilities: model.NodeCapabilities{
					CanAcceptInbound: true,
					CanForward:       true,
					HasPublicIP:      true,
				},
			},
			{
				ID:         "hub-b",
				Name:       "hub-b",
				Hostname:   hostname,
				Role:       "router",
				DomainID:   "domain-1",
				ListenPort: baseB,
				Capabilities: model.NodeCapabilities{
					CanAcceptInbound: true,
					CanForward:       true,
					HasPublicIP:      true,
				},
			},
		},
	}

	addPeers := func(hubID, tag string, count int) {
		for i := 0; i < count; i++ {
			peerID := tag + "-peer-" + itoaTest(i)
			topo.Nodes = append(topo.Nodes, model.Node{
				ID:       peerID,
				Name:     peerID,
				Role:     "router",
				DomainID: "domain-1",
				Capabilities: model.NodeCapabilities{
					CanAcceptInbound: true,
					CanForward:       true,
					HasPublicIP:      true,
				},
			})
			topo.Edges = append(topo.Edges, model.Edge{
				ID:         tag + "-edge-" + itoaTest(i),
				FromNodeID: hubID,
				ToNodeID:   peerID,
				Type:       "direct",
				Transport:  "udp",
				IsEnabled:  true,
			})
		}
	}

	addPeers("hub-a", "a", ifacesA)
	addPeers("hub-b", "b", ifacesB)

	return topo
}

// TestValidateSemantic_SameHostRangeOverlap 覆盖同主机生效范围重叠（D47）：
// hub-a 基准 51820 占 3 个接口（51820-51822），hub-b 基准 51821 占 1 个接口（51821），
// 二者共享同一 hostname 且范围重叠，必须报错。
func TestValidateSemantic_SameHostRangeOverlap(t *testing.T) {
	topo := sameHostTopology("shared.example.com", 51820, 3, 51821, 1)
	result := ValidateSemantic(topo)
	// 重叠错误报在下标较大的节点（hub-b）的 listen_port 上。
	assertHasError(t, result, "nodes[1].listen_port")
}

// TestValidateSemantic_SameHostRangeDisjoint 验证同主机但范围互不相交时不报错：
// hub-a 基准 51820 占 3 个接口（51820-51822），hub-b 基准 51900 占 3 个接口（51900-51902），
// 区间不相交，应当通过。
func TestValidateSemantic_SameHostRangeDisjoint(t *testing.T) {
	topo := sameHostTopology("shared.example.com", 51820, 3, 51900, 3)
	result := ValidateSemantic(topo)
	for _, e := range result.Errors {
		if contains(e.Field, "listen_port") {
			t.Errorf("不相交的同主机范围不应报错，却得到：%s", e.Error())
		}
	}
}
