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
		t.Fatalf(" IP : %v", err)
	}

	//  IP
	for _, node := range nodes {
		if node.OverlayIP == "" {
			t.Errorf(" %s  IP", node.Name)
		}
	}

	//  IP ： 10.10.0.1 
	expectedIPs := []string{"10.10.0.1", "10.10.0.2", "10.10.0.3"}
	for i, node := range nodes {
		if node.OverlayIP != expectedIPs[i] {
			t.Errorf(" %s  IP  %s,  %s", node.Name, expectedIPs[i], node.OverlayIP)
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
		t.Fatalf(" IP : %v", err)
	}

	//  IP 
	if nodes[0].OverlayIP != "10.10.0.50" {
		t.Errorf(" IP :  10.10.0.50,  %s", nodes[0].OverlayIP)
	}

	//  IP 
	if nodes[1].OverlayIP == "10.10.0.50" {
		t.Errorf(" IP  IP ")
	}

	//  10.10.0.1 
	if nodes[1].OverlayIP != "10.10.0.1" {
		t.Errorf(" IP  10.10.0.1,  %s", nodes[1].OverlayIP)
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
		t.Fatalf(" IP : %v", err)
	}

	// 10.10.0.1 ， 10.10.0.2 
	if nodes[1].OverlayIP != "10.10.0.2" {
		t.Errorf(" 10.10.0.2,  %s", nodes[1].OverlayIP)
	}
	if nodes[2].OverlayIP != "10.10.0.3" {
		t.Errorf(" 10.10.0.3,  %s", nodes[2].OverlayIP)
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
			ReservedRanges: []string{"10.10.0.1/30"}, //  10.10.0.0-10.10.0.3
		}},
		Nodes: []model.Node{
			{ID: "n1", Name: "node-1", Role: "router", DomainID: "domain-1"},
		},
		Edges: []model.Edge{},
	}

	alloc := NewIPAllocator()
	nodes, err := alloc.AllocateIPs(topo)
	if err != nil {
		t.Fatalf(" IP : %v", err)
	}

	// 10.10.0.1-10.10.0.3 ， 10.10.0.4
	if nodes[0].OverlayIP != "10.10.0.4" {
		t.Errorf(" 10.10.0.4（）,  %s", nodes[0].OverlayIP)
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
		t.Fatalf(" IP : %v", err)
	}

	if nodes[0].OverlayIP != "10.10.0.2" {
		t.Errorf(" 10.10.0.2（ IP）,  %s", nodes[0].OverlayIP)
	}
}

func TestAllocateIPs_CIDRExhausted(t *testing.T) {
	topo := &model.Topology{
		Project: model.Project{ID: "test", Name: "Test"},
		Domains: []model.Domain{{
			ID:             "domain-1",
			Name:           "test",
			CIDR:           "10.10.0.0/30", //  2  (.1  .2)
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
		t.Errorf("CIDR ")
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
		t.Errorf(" Domain ")
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
		t.Fatalf(" IP : %v", err)
	}

	// 
	if topo.Nodes[0].OverlayIP != "" {
		t.Errorf(" IP ,  %s", topo.Nodes[0].OverlayIP)
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
		t.Fatalf(" IP : %v", err)
	}

	seen := make(map[string]bool)
	for _, node := range nodes {
		if seen[node.OverlayIP] {
			t.Errorf(" IP: %s", node.OverlayIP)
		}
		seen[node.OverlayIP] = true
	}
}
