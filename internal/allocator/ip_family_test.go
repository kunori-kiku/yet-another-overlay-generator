package allocator

import (
	"net"
	"testing"

	"github.com/kunorikiku/yet-another-overlay-generator/internal/model"
)

// TestAllocateIPs_AddressFamilyAndSizeBounds 验证分配器的地址族与 CIDR 大小的深度防御行为。
//
// schema 校验是拒绝 IPv6 的第一道防线（在 validator 包中），本测试直接驱动分配器，
// 证明即便非 IPv4 的 CIDR 绕过 schema 到达分配器，也只会得到干净的错误而非 panic
// ——这是针对历史上 ip[12:16] 越界切片 panic 的回归门禁。
func TestAllocateIPs_AddressFamilyAndSizeBounds(t *testing.T) {
	tests := []struct {
		name    string
		cidr    string
		wantErr bool
		wantIP  string // 仅当 wantErr 为 false 时校验
	}{
		{
			// 回归门禁：IPv6 域 CIDR 必须返回干净错误，绝不 panic。
			// fd00::/64 的主机位为 64（>=32），会在主机位溢出防御处被拦截。
			name:    "IPv6 域 CIDR 返回干净错误且不 panic",
			cidr:    "fd00::/64",
			wantErr: true,
		},
		{
			// 即使 IPv6 前缀较长（主机位 < 32）绕过溢出防御，
			// ipToUint32 的 To4() 守卫仍会返回错误而非 panic。
			name:    "长前缀 IPv6 CIDR 也返回干净错误",
			cidr:    "fd00::/120",
			wantErr: true,
		},
		{
			// /8 IPv4 CIDR 是允许的最大网段，分配器数学不应溢出，可正常分配。
			name:    "/8 IPv4 CIDR 被接受并分配出一个地址",
			cidr:    "10.0.0.0/8",
			wantErr: false,
			wantIP:  "10.0.0.1",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			topo := &model.Topology{
				Project: model.Project{ID: "test", Name: "Test"},
				Domains: []model.Domain{{
					ID:             "domain-1",
					Name:           "test",
					CIDR:           tt.cidr,
					AllocationMode: "auto",
					RoutingMode:    "babel",
				}},
				Nodes: []model.Node{
					{ID: "n1", Name: "node-1", Role: "router", DomainID: "domain-1"},
				},
				Edges: []model.Edge{},
			}

			alloc := NewIPAllocator()
			nodes, err := alloc.AllocateIPs(topo)

			if tt.wantErr {
				if err == nil {
					t.Fatalf("CIDR %s 应返回错误，但分配成功", tt.cidr)
				}
				return
			}

			if err != nil {
				t.Fatalf("CIDR %s 应分配成功，但返回错误: %v", tt.cidr, err)
			}
			if len(nodes) != 1 {
				t.Fatalf("应返回 1 个节点，实际返回 %d", len(nodes))
			}
			if nodes[0].OverlayIP != tt.wantIP {
				t.Errorf("CIDR %s 分配结果应为 %s，实际为 %s", tt.cidr, tt.wantIP, nodes[0].OverlayIP)
			}
		})
	}
}

// TestIPToUint32_Errors 验证 ipToUint32 对 nil 及 16 字节非 v4-mappable 输入返回错误。
func TestIPToUint32_Errors(t *testing.T) {
	tests := []struct {
		name    string
		ip      net.IP
		wantErr bool
	}{
		{
			name:    "nil 地址返回错误",
			ip:      nil,
			wantErr: true,
		},
		{
			// 16 字节的纯 IPv6 地址（非 v4-mappable），To4() 返回 nil。
			name:    "16 字节 IPv6 地址返回错误",
			ip:      net.ParseIP("fd00::1"),
			wantErr: true,
		},
		{
			// 正常 IPv4 用例作为对照：必须成功并转换正确。
			name:    "IPv4 地址正常转换",
			ip:      net.ParseIP("10.0.0.1"),
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ipToUint32(tt.ip)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("地址 %v 应返回错误，但成功转换为 %d", tt.ip, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("地址 %v 应转换成功，但返回错误: %v", tt.ip, err)
			}
			// 10.0.0.1 = 0x0A000001
			const want = uint32(0x0A000001)
			if got != want {
				t.Errorf("地址 %v 转换结果应为 %d，实际为 %d", tt.ip, want, got)
			}
		})
	}
}
