package allocator

import (
	"testing"

	"github.com/kunorikiku/yet-another-overlay-generator/internal/model"
)

func TestAllocateIPs_AutoAssignment(t *testing.T) {
	topo := &model.Topology{
		Project: model.Project{ID: "test", Name: "Test"},
		Domains: []model.Domain{{
			ID:             "domain-1",
			Name:           "test",
			CIDR:           "10.10.0.0/24",
			AllocationMode: "auto",
			RoutingMode:    "babel",
		}},
		Nodes: []model.Node{
			{ID: "n1", Name: "node-1", Role: "router", DomainID: "domain-1"},
			{ID: "n2", Name: "node-2", Role: "router", DomainID: "domain-1"},
			{ID: "n3", Name: "node-3", Role: "peer", DomainID: "domain-1"},
		},
		Edges: []model.Edge{},
	}

	alloc := NewIPAllocator()
	nodes, err := alloc.AllocateIPs(topo)
	if err != nil {
		t.Fatalf("分配 IP 失败: %v", err)
	}

	// 检查所有节点都有 IP
	for _, node := range nodes {
		if node.OverlayIP == "" {
			t.Errorf("节点 %s 没有分配到 IP", node.Name)
		}
	}

	// 检查 IP 顺序：应从 10.10.0.1 开始
	expectedIPs := []string{"10.10.0.1", "10.10.0.2", "10.10.0.3"}
	for i, node := range nodes {
		if node.OverlayIP != expectedIPs[i] {
			t.Errorf("节点 %s 的 IP 期望 %s, 得到 %s", node.Name, expectedIPs[i], node.OverlayIP)
		}
	}
}

func TestAllocateIPs_ManualIPPreserved(t *testing.T) {
	topo := &model.Topology{
		Project: model.Project{ID: "test", Name: "Test"},
		Domains: []model.Domain{{
			ID:             "domain-1",
			Name:           "test",
			CIDR:           "10.10.0.0/24",
			AllocationMode: "auto",
			RoutingMode:    "babel",
		}},
		Nodes: []model.Node{
			{ID: "n1", Name: "node-1", Role: "router", DomainID: "domain-1", OverlayIP: "10.10.0.50"},
			{ID: "n2", Name: "node-2", Role: "router", DomainID: "domain-1"},
		},
		Edges: []model.Edge{},
	}

	alloc := NewIPAllocator()
	nodes, err := alloc.AllocateIPs(topo)
	if err != nil {
		t.Fatalf("分配 IP 失败: %v", err)
	}

	// 手动 IP 不被覆盖
	if nodes[0].OverlayIP != "10.10.0.50" {
		t.Errorf("手动 IP 被覆盖: 期望 10.10.0.50, 得到 %s", nodes[0].OverlayIP)
	}

	// 自动分配的不应与手动 IP 冲突
	if nodes[1].OverlayIP == "10.10.0.50" {
		t.Errorf("自动分配的 IP 与手动 IP 冲突")
	}

	// 自动分配应从 10.10.0.1 开始
	if nodes[1].OverlayIP != "10.10.0.1" {
		t.Errorf("自动分配 IP 期望 10.10.0.1, 得到 %s", nodes[1].OverlayIP)
	}
}

func TestAllocateIPs_SkipManualIPInSequence(t *testing.T) {
	topo := &model.Topology{
		Project: model.Project{ID: "test", Name: "Test"},
		Domains: []model.Domain{{
			ID:             "domain-1",
			Name:           "test",
			CIDR:           "10.10.0.0/24",
			AllocationMode: "auto",
			RoutingMode:    "babel",
		}},
		Nodes: []model.Node{
			{ID: "n1", Name: "node-1", Role: "router", DomainID: "domain-1", OverlayIP: "10.10.0.1"},
			{ID: "n2", Name: "node-2", Role: "router", DomainID: "domain-1"},
			{ID: "n3", Name: "node-3", Role: "peer", DomainID: "domain-1"},
		},
		Edges: []model.Edge{},
	}

	alloc := NewIPAllocator()
	nodes, err := alloc.AllocateIPs(topo)
	if err != nil {
		t.Fatalf("分配 IP 失败: %v", err)
	}

	// 10.10.0.1 已被手动占用，自动分配应从 10.10.0.2 开始
	if nodes[1].OverlayIP != "10.10.0.2" {
		t.Errorf("期望 10.10.0.2, 得到 %s", nodes[1].OverlayIP)
	}
	if nodes[2].OverlayIP != "10.10.0.3" {
		t.Errorf("期望 10.10.0.3, 得到 %s", nodes[2].OverlayIP)
	}
}

func TestAllocateIPs_ReservedRangeSkipped(t *testing.T) {
	topo := &model.Topology{
		Project: model.Project{ID: "test", Name: "Test"},
		Domains: []model.Domain{{
			ID:             "domain-1",
			Name:           "test",
			CIDR:           "10.10.0.0/24",
			AllocationMode: "auto",
			RoutingMode:    "babel",
			ReservedRanges: []string{"10.10.0.1/30"}, // 保留 10.10.0.0-10.10.0.3
		}},
		Nodes: []model.Node{
			{ID: "n1", Name: "node-1", Role: "router", DomainID: "domain-1"},
		},
		Edges: []model.Edge{},
	}

	alloc := NewIPAllocator()
	nodes, err := alloc.AllocateIPs(topo)
	if err != nil {
		t.Fatalf("分配 IP 失败: %v", err)
	}

	// 10.10.0.1-10.10.0.3 被保留，应分配 10.10.0.4
	if nodes[0].OverlayIP != "10.10.0.4" {
		t.Errorf("期望 10.10.0.4（跳过保留区间）, 得到 %s", nodes[0].OverlayIP)
	}
}

func TestAllocateIPs_ReservedSingleIP(t *testing.T) {
	topo := &model.Topology{
		Project: model.Project{ID: "test", Name: "Test"},
		Domains: []model.Domain{{
			ID:             "domain-1",
			Name:           "test",
			CIDR:           "10.10.0.0/24",
			AllocationMode: "auto",
			RoutingMode:    "babel",
			ReservedRanges: []string{"10.10.0.1"},
		}},
		Nodes: []model.Node{
			{ID: "n1", Name: "node-1", Role: "router", DomainID: "domain-1"},
		},
		Edges: []model.Edge{},
	}

	alloc := NewIPAllocator()
	nodes, err := alloc.AllocateIPs(topo)
	if err != nil {
		t.Fatalf("分配 IP 失败: %v", err)
	}

	if nodes[0].OverlayIP != "10.10.0.2" {
		t.Errorf("期望 10.10.0.2（跳过保留 IP）, 得到 %s", nodes[0].OverlayIP)
	}
}

func TestAllocateIPs_CIDRExhausted(t *testing.T) {
	topo := &model.Topology{
		Project: model.Project{ID: "test", Name: "Test"},
		Domains: []model.Domain{{
			ID:             "domain-1",
			Name:           "test",
			CIDR:           "10.10.0.0/30", // 只有 2 个可用地址 (.1 和 .2)
			AllocationMode: "auto",
			RoutingMode:    "babel",
		}},
		Nodes: []model.Node{
			{ID: "n1", Name: "node-1", Role: "router", DomainID: "domain-1"},
			{ID: "n2", Name: "node-2", Role: "router", DomainID: "domain-1"},
			{ID: "n3", Name: "node-3", Role: "peer", DomainID: "domain-1"},
		},
		Edges: []model.Edge{},
	}

	alloc := NewIPAllocator()
	_, err := alloc.AllocateIPs(topo)
	if err == nil {
		t.Errorf("CIDR 耗尽时应返回错误")
	}
}

func TestAllocateIPs_NonExistentDomain(t *testing.T) {
	topo := &model.Topology{
		Project: model.Project{ID: "test", Name: "Test"},
		Domains: []model.Domain{{
			ID:             "domain-1",
			Name:           "test",
			CIDR:           "10.10.0.0/24",
			AllocationMode: "auto",
			RoutingMode:    "babel",
		}},
		Nodes: []model.Node{
			{ID: "n1", Name: "node-1", Role: "router", DomainID: "domain-nonexistent"},
		},
		Edges: []model.Edge{},
	}

	alloc := NewIPAllocator()
	_, err := alloc.AllocateIPs(topo)
	if err == nil {
		t.Errorf("引用不存在的 Domain 应返回错误")
	}
}

func TestAllocateIPs_OriginalNotModified(t *testing.T) {
	topo := &model.Topology{
		Project: model.Project{ID: "test", Name: "Test"},
		Domains: []model.Domain{{
			ID:             "domain-1",
			Name:           "test",
			CIDR:           "10.10.0.0/24",
			AllocationMode: "auto",
			RoutingMode:    "babel",
		}},
		Nodes: []model.Node{
			{ID: "n1", Name: "node-1", Role: "router", DomainID: "domain-1"},
		},
		Edges: []model.Edge{},
	}

	alloc := NewIPAllocator()
	_, err := alloc.AllocateIPs(topo)
	if err != nil {
		t.Fatalf("分配 IP 失败: %v", err)
	}

	// 原始拓扑不应被修改
	if topo.Nodes[0].OverlayIP != "" {
		t.Errorf("原始拓扑的节点 IP 应保持为空, 得到 %s", topo.Nodes[0].OverlayIP)
	}
}

func TestAllocateIPs_NoIPDuplication(t *testing.T) {
	topo := &model.Topology{
		Project: model.Project{ID: "test", Name: "Test"},
		Domains: []model.Domain{{
			ID:             "domain-1",
			Name:           "test",
			CIDR:           "10.10.0.0/24",
			AllocationMode: "auto",
			RoutingMode:    "babel",
		}},
		Nodes: []model.Node{
			{ID: "n1", Name: "node-1", Role: "router", DomainID: "domain-1"},
			{ID: "n2", Name: "node-2", Role: "router", DomainID: "domain-1"},
			{ID: "n3", Name: "node-3", Role: "peer", DomainID: "domain-1"},
			{ID: "n4", Name: "node-4", Role: "peer", DomainID: "domain-1"},
			{ID: "n5", Name: "node-5", Role: "peer", DomainID: "domain-1"},
		},
		Edges: []model.Edge{},
	}

	alloc := NewIPAllocator()
	nodes, err := alloc.AllocateIPs(topo)
	if err != nil {
		t.Fatalf("分配 IP 失败: %v", err)
	}

	seen := make(map[string]bool)
	for _, node := range nodes {
		if seen[node.OverlayIP] {
			t.Errorf("发现重复 IP: %s", node.OverlayIP)
		}
		seen[node.OverlayIP] = true
	}
}
