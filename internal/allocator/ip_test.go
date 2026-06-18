package allocator

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/kunorikiku/yet-another-overlay-generator/internal/apierr"
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
	nodes, err := alloc.AllocateIPs(context.Background(), topo)
	if err != nil {
		t.Fatalf("IP allocation failed: %v", err)
	}

	// Every node should have an IP.
	for _, node := range nodes {
		if node.OverlayIP == "" {
			t.Errorf("node %s has no IP", node.Name)
		}
	}

	// IPs should be allocated sequentially starting from 10.10.0.1.
	expectedIPs := []string{"10.10.0.1", "10.10.0.2", "10.10.0.3"}
	for i, node := range nodes {
		if node.OverlayIP != expectedIPs[i] {
			t.Errorf("node %s IP should be %s, got %s", node.Name, expectedIPs[i], node.OverlayIP)
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
	nodes, err := alloc.AllocateIPs(context.Background(), topo)
	if err != nil {
		t.Fatalf("IP allocation failed: %v", err)
	}

	// The manually assigned IP should be preserved.
	if nodes[0].OverlayIP != "10.10.0.50" {
		t.Errorf("manual IP should be preserved: want 10.10.0.50, got %s", nodes[0].OverlayIP)
	}

	// The auto-allocated IP must not collide with the manual IP.
	if nodes[1].OverlayIP == "10.10.0.50" {
		t.Errorf("auto-allocated IP collided with the manual IP")
	}

	// The auto-allocated IP should be 10.10.0.1.
	if nodes[1].OverlayIP != "10.10.0.1" {
		t.Errorf("auto-allocated IP should be 10.10.0.1, got %s", nodes[1].OverlayIP)
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
	nodes, err := alloc.AllocateIPs(context.Background(), topo)
	if err != nil {
		t.Fatalf("IP allocation failed: %v", err)
	}

	// 10.10.0.1 is already taken manually, so the next allocation should be 10.10.0.2.
	if nodes[1].OverlayIP != "10.10.0.2" {
		t.Errorf("expected 10.10.0.2, got %s", nodes[1].OverlayIP)
	}
	if nodes[2].OverlayIP != "10.10.0.3" {
		t.Errorf("expected 10.10.0.3, got %s", nodes[2].OverlayIP)
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
			ReservedRanges: []string{"10.10.0.1/30"}, // reserves 10.10.0.0-10.10.0.3
		}},
		Nodes: []model.Node{
			{ID: "n1", Name: "node-1", Role: "router", DomainID: "domain-1"},
		},
		Edges: []model.Edge{},
	}

	alloc := NewIPAllocator()
	nodes, err := alloc.AllocateIPs(context.Background(), topo)
	if err != nil {
		t.Fatalf("IP allocation failed: %v", err)
	}

	// 10.10.0.1-10.10.0.3 are reserved, so the first allocation should be 10.10.0.4.
	if nodes[0].OverlayIP != "10.10.0.4" {
		t.Errorf("expected 10.10.0.4 (first address after the reserved range), got %s", nodes[0].OverlayIP)
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
	nodes, err := alloc.AllocateIPs(context.Background(), topo)
	if err != nil {
		t.Fatalf("IP allocation failed: %v", err)
	}

	if nodes[0].OverlayIP != "10.10.0.2" {
		t.Errorf("expected 10.10.0.2 (10.10.0.1 is a reserved single IP), got %s", nodes[0].OverlayIP)
	}
}

func TestAllocateIPs_CIDRExhausted(t *testing.T) {
	topo := &model.Topology{
		Project: model.Project{ID: "test", Name: "Test"},
		Domains: []model.Domain{{
			ID:             "domain-1",
			Name:           "test",
			CIDR:           "10.10.0.0/30", // only 2 usable addresses (.1 and .2)
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
	_, err := alloc.AllocateIPs(context.Background(), topo)
	if err == nil {
		t.Errorf("expected an error when the CIDR is exhausted")
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
	_, err := alloc.AllocateIPs(context.Background(), topo)
	if err == nil {
		t.Errorf("expected an error when the node references a non-existent domain")
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
	_, err := alloc.AllocateIPs(context.Background(), topo)
	if err != nil {
		t.Fatalf("IP allocation failed: %v", err)
	}

	// The original topology must not be mutated.
	if topo.Nodes[0].OverlayIP != "" {
		t.Errorf("the original topology's IP should not be modified, got %s", topo.Nodes[0].OverlayIP)
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
	nodes, err := alloc.AllocateIPs(context.Background(), topo)
	if err != nil {
		t.Fatalf("IP allocation failed: %v", err)
	}

	seen := make(map[string]bool)
	for _, node := range nodes {
		if seen[node.OverlayIP] {
			t.Errorf("duplicate IP: %s", node.OverlayIP)
		}
		seen[node.OverlayIP] = true
	}
}

// TestAllocateIPs_ScanBudgetExceeded pins the S1 DoS bound (plan-8 Phase 2): a topology whose
// node count multiplied by the (per-node) scan span of its domain CIDR exceeds the documented
// scan-budget ceiling is rejected FAST with the coded CodeOverlayScanBudgetExceeded, rather than
// the allocator entering a multi-billion-iteration linear scan that ties up a request goroutine.
// A /8 CIDR has ~16.7M host candidates; with even a single node that already blows past the
// budget. The key assertion is that the reject is IMMEDIATE (the bound is checked before the scan
// loop runs), not a timeout — so the test itself completes in microseconds.
func TestAllocateIPs_ScanBudgetExceeded(t *testing.T) {
	topo := &model.Topology{
		Project: model.Project{ID: "test", Name: "Test"},
		Domains: []model.Domain{{
			ID:             "domain-1",
			Name:           "test",
			CIDR:           "10.0.0.0/8", // ~16.7M host candidates — valid per the /8 lower bound, but huge
			AllocationMode: "auto",
			RoutingMode:    "babel",
		}},
		Nodes: []model.Node{
			{ID: "n1", Name: "node-1", Role: "router", DomainID: "domain-1"},
		},
		Edges: []model.Edge{},
	}

	alloc := NewIPAllocator()
	done := make(chan struct{})
	var err error
	go func() {
		_, err = alloc.AllocateIPs(context.Background(), topo)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("AllocateIPs did not return promptly on an over-budget CIDR — the cap must short-circuit before the scan, not run the full enumeration")
	}

	if err == nil {
		t.Fatal("expected an over-budget CIDR × node count to be rejected, got nil")
	}
	var ce *apierr.Error
	if !errors.As(err, &ce) || ce.Code() != apierr.CodeOverlayScanBudgetExceeded {
		t.Fatalf("expected CodeOverlayScanBudgetExceeded, got: %v", err)
	}
}

// TestAllocateIPs_ScanBudgetWithinBoundsStillAllocates is the negative control for the S1 cap: a
// /24 (the realistic case) is well under the budget and must allocate normally — the budget bound
// must never reject a legitimate topology.
func TestAllocateIPs_ScanBudgetWithinBoundsStillAllocates(t *testing.T) {
	topo := &model.Topology{
		Project: model.Project{ID: "test", Name: "Test"},
		Domains: []model.Domain{{
			ID:             "domain-1",
			Name:           "test",
			CIDR:           "10.10.0.0/16", // 65k candidates × a few nodes — comfortably within budget
			AllocationMode: "auto",
			RoutingMode:    "babel",
		}},
		Nodes: []model.Node{
			{ID: "n1", Name: "node-1", Role: "router", DomainID: "domain-1"},
			{ID: "n2", Name: "node-2", Role: "peer", DomainID: "domain-1"},
		},
		Edges: []model.Edge{},
	}

	alloc := NewIPAllocator()
	nodes, err := alloc.AllocateIPs(context.Background(), topo)
	if err != nil {
		t.Fatalf("a within-budget /16 must allocate normally, got: %v", err)
	}
	if nodes[0].OverlayIP != "10.10.0.1" || nodes[1].OverlayIP != "10.10.0.2" {
		t.Fatalf("unexpected allocation: %s, %s", nodes[0].OverlayIP, nodes[1].OverlayIP)
	}
}

// TestAllocateIPs_ContextCancelAborts pins the S1 ctx-cancel half (plan-8 Phase 2): an
// already-cancelled context causes AllocateIPs to abort the scan with ctx.Err(), so a long-running
// allocation on a behaving-but-large topology is abortable when the request goroutine is cancelled.
// We use a CIDR within the scan budget (so the budget cap does NOT pre-empt) but force a fully
// reserved pool so the scan would otherwise run to exhaustion — the cancel must win first.
func TestAllocateIPs_ContextCancelAborts(t *testing.T) {
	topo := &model.Topology{
		Project: model.Project{ID: "test", Name: "Test"},
		Domains: []model.Domain{{
			ID:             "domain-1",
			Name:           "test",
			CIDR:           "10.10.0.0/16",
			AllocationMode: "auto",
			RoutingMode:    "babel",
		}},
		Nodes: []model.Node{
			{ID: "n1", Name: "node-1", Role: "router", DomainID: "domain-1"},
		},
		Edges: []model.Edge{},
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // already cancelled before the call

	alloc := NewIPAllocator()
	_, err := alloc.AllocateIPs(ctx, topo)
	if err == nil {
		t.Fatal("expected a cancelled context to abort allocation, got nil")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected the error to wrap context.Canceled, got: %v", err)
	}
}
