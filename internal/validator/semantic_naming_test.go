package validator

import (
	"testing"

	"github.com/kunorikiku/yet-another-overlay-generator/internal/model"
)

// nameCollisionTopology 构造一个仅含两个节点的最小拓扑，
// 两节点分属同一 Domain 并互相连边，使其除节点名称外都通过语义校验，
// 从而把断言聚焦在节点名称冲突规则（Spec D 的 N1–N3）上。
func nameCollisionTopology(firstName, secondName string) *model.Topology {
	return &model.Topology{
		Project: model.Project{ID: "test-001", Name: "Test Project"},
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
				ID:         "node-1",
				Name:       firstName,
				Hostname:   "first.example.com",
				Platform:   "debian",
				Role:       "router",
				DomainID:   "domain-1",
				ListenPort: 51820,
				Capabilities: model.NodeCapabilities{
					CanAcceptInbound: true,
					CanForward:       true,
					HasPublicIP:      true,
				},
			},
			{
				ID:         "node-2",
				Name:       secondName,
				Hostname:   "second.example.com",
				Platform:   "ubuntu",
				Role:       "router",
				DomainID:   "domain-1",
				ListenPort: 51820,
				Capabilities: model.NodeCapabilities{
					CanAcceptInbound: true,
					CanForward:       true,
					HasPublicIP:      true,
				},
			},
		},
		Edges: []model.Edge{
			{
				ID:           "edge-1",
				FromNodeID:   "node-1",
				ToNodeID:     "node-2",
				Type:         "direct",
				EndpointHost: "203.0.113.2",
				EndpointPort: 51820,
				Transport:    "udp",
				IsEnabled:    true,
			},
			{
				ID:           "edge-2",
				FromNodeID:   "node-2",
				ToNodeID:     "node-1",
				Type:         "direct",
				EndpointHost: "203.0.113.1",
				EndpointPort: 51820,
				Transport:    "udp",
				IsEnabled:    true,
			},
		},
	}
}

// TestValidateSemantic_NodeNameCollisions 以表驱动方式覆盖 Spec D 的三条命名唯一性不变式：
//   - 安装脚本文件名冲突（N2）："Web 1" 与 "web-1" 都归一为 web-1.install.sh。
//   - 原始名称冲突（N1）：两个 "Alpha" 完全相同。
//   - WireGuard 接口名冲突（N3）："db.east" 与 "db-east" 都归一为 wg-db-east。
//   - 互不冲突的两个名称（"alpha" 与 "beta"）应当通过校验。
func TestValidateSemantic_NodeNameCollisions(t *testing.T) {
	cases := []struct {
		name        string
		firstName   string
		secondName  string
		expectError bool
	}{
		{
			name:        "安装脚本文件名冲突",
			firstName:   "Web 1",
			secondName:  "web-1",
			expectError: true,
		},
		{
			name:        "原始名称冲突",
			firstName:   "Alpha",
			secondName:  "Alpha",
			expectError: true,
		},
		{
			name:        "WireGuard 接口名冲突",
			firstName:   "db.east",
			secondName:  "db-east",
			expectError: true,
		},
		{
			name:        "互不冲突的名称",
			firstName:   "alpha",
			secondName:  "beta",
			expectError: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			topo := nameCollisionTopology(tc.firstName, tc.secondName)
			result := ValidateSemantic(topo)
			if tc.expectError {
				// 冲突应当在第二个节点的 name 字段上报错。
				assertHasError(t, result, "nodes[1].name")
			} else {
				// 互不冲突的名称不应触发任何 name 字段错误。
				for _, e := range result.Errors {
					if contains(e.Field, "nodes[1].name") {
						t.Errorf("名称 %q 与 %q 不应产生冲突错误，却得到：%s",
							tc.firstName, tc.secondName, e.Error())
					}
				}
			}
		})
	}
}
