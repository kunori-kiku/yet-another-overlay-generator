package controller

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/kunorikiku/yet-another-overlay-generator/internal/compiler"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/model"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/render"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/validator"
)

// reservationTopo models the cross-subgraph collision root cause: a fleet where routerA<->routerB
// is already deployed (its edge holds transit 10.10.0.1/.2, listen port 51820 on each end, and
// link-locals fe80::1/::2), and routerB->routerD is a brand-new, unpinned edge. When only B and D
// are enrolled, the enrolled subgraph is {B,D} + e-bd and DROPS e-ab (A not enrolled) — so without
// reserving e-ab's pins the subgraph's gap-fill restarts from .1 and hands e-bd the exact transit
// IPs / port that e-ab still pins in the full topology, producing the "pin occupied by two different
// links" corruption once persistAllocations writes e-bd's pins back. node-b is shared by both edges
// so the test also exercises per-node port reservation, not just the pool-wide transit reservation.
func reservationTopo() *model.Topology {
	router := func(id, name, host string) model.Node {
		return model.Node{
			ID: id, Name: name, Hostname: host, Role: "router", DomainID: "domain-1",
			Capabilities: model.NodeCapabilities{CanAcceptInbound: true, CanForward: true, HasPublicIP: true},
		}
	}
	return &model.Topology{
		Project: model.Project{ID: "ctrl-resv-001", Name: "Reservation Test"},
		Domains: []model.Domain{{
			ID: "domain-1", Name: "net", CIDR: "10.60.0.0/24",
			AllocationMode: "auto", RoutingMode: "babel",
		}},
		Nodes: []model.Node{
			router("node-a", "rA", "a.example.com"),
			router("node-b", "rB", "b.example.com"),
			router("node-d", "rD", "d.example.com"),
		},
		Edges: []model.Edge{
			// Already-deployed link: fully pinned. Dropped from a {B,D} subgraph (A unenrolled).
			{
				ID: "e-ab", FromNodeID: "node-a", ToNodeID: "node-b",
				Type: "public-endpoint", EndpointHost: "198.51.100.2", Transport: "udp", IsEnabled: true,
				PinnedFromPort: 51820, PinnedToPort: 51820,
				PinnedFromTransitIP: "10.10.0.1", PinnedToTransitIP: "10.10.0.2",
				PinnedFromLinkLocal: "fe80::1", PinnedToLinkLocal: "fe80::2",
			},
			// New, unpinned link sharing node-b. Must NOT re-use e-ab's allocation.
			{
				ID: "e-bd", FromNodeID: "node-b", ToNodeID: "node-d",
				Type: "public-endpoint", EndpointHost: "198.51.100.4", Transport: "udp", IsEnabled: true,
			},
		},
	}
}

// findEdge returns the compiled edge with the given ID from a compile result.
func findEdge(t *testing.T, topo *model.Topology, id string) model.Edge {
	t.Helper()
	for _, e := range topo.Edges {
		if e.ID == id {
			return e
		}
	}
	t.Fatalf("edge %q not found in compiled topology", id)
	return model.Edge{}
}

// TestCompileSubgraph_ReservesOutOfSubgraphPins is the regression guard for the cross-subgraph pin
// collision: compiling the {B,D} subgraph must allocate e-bd AROUND the pins held by the dropped
// e-ab. It first establishes a NEGATIVE CONTROL — the same subgraph compiled WITHOUT reservation
// hands e-bd exactly e-ab's transit IP and port (the bug) — then proves CompileSubgraph (which
// builds and applies the reservation) avoids every one of e-ab's resources.
func TestCompileSubgraph_ReservesOutOfSubgraphPins(t *testing.T) {
	full := reservationTopo()
	// Enroll only B and D; A stays unenrolled so e-ab is dropped from the subgraph.
	nodes := []Node{
		{NodeID: "node-b", WGPublicKey: genWGPubKey(t), Status: NodeApproved},
		{NodeID: "node-d", WGPublicKey: genWGPubKey(t), Status: NodeApproved},
	}

	// --- Negative control: WITHOUT reservation, e-bd collides with e-ab. ---
	sub, skipped := enrolledSubgraph(full, nodes)
	if !containsStr(skipped, "node-a") {
		t.Fatalf("expected node-a skipped (unenrolled), got skipped=%v", skipped)
	}
	keys, err := render.GenerateKeys(&sub, render.AgentHeld)
	if err != nil {
		t.Fatalf("GenerateKeys: %v", err)
	}
	plain, err := compiler.NewCompiler().Compile(&sub, keys)
	if err != nil {
		t.Fatalf("plain Compile: %v", err)
	}
	plainBD := findEdge(t, plain.Topology, "e-bd")
	if plainBD.PinnedFromTransitIP != "10.10.0.1" {
		t.Fatalf("negative control invalid: without reservation e-bd transit = %q, expected the colliding 10.10.0.1 "+
			"(if this changed, the test no longer reproduces the bug)", plainBD.PinnedFromTransitIP)
	}

	// --- The fix: WITH reservation (via CompileSubgraph), e-bd avoids ALL of e-ab's resources. ---
	res, _, skipped2, err := CompileSubgraph(full, nodes, render.FetchSettings{})
	if err != nil {
		t.Fatalf("CompileSubgraph: %v", err)
	}
	if !containsStr(skipped2, "node-a") {
		t.Fatalf("CompileSubgraph: expected node-a skipped, got %v", skipped2)
	}
	bd := findEdge(t, res.Topology, "e-bd")

	// Transit IPs: neither end may land on e-ab's reserved .1/.2 (pool-wide reservation).
	for _, ip := range []string{bd.PinnedFromTransitIP, bd.PinnedToTransitIP} {
		if ip == "10.10.0.1" || ip == "10.10.0.2" {
			t.Errorf("e-bd transit IP %q collides with dropped e-ab (.1/.2); reservation failed", ip)
		}
	}
	// node-b's listen port (e-bd's from end) must avoid e-ab's reserved 51820 on node-b.
	if bd.PinnedFromPort == 51820 {
		t.Errorf("e-bd from-port (node-b) = 51820, collides with dropped e-ab's node-b port; port reservation failed")
	}
	// Link-locals: neither end may land on e-ab's reserved fe80::1/::2.
	for _, ll := range []string{bd.PinnedFromLinkLocal, bd.PinnedToLinkLocal} {
		if ll == "fe80::1" || ll == "fe80::2" {
			t.Errorf("e-bd link-local %q collides with dropped e-ab; reservation failed", ll)
		}
	}
}

// TestCompileAndStage_HealsPreExistingCollision proves deploy-time self-heal: a STORED topology
// already carrying a cross-link pin collision (two different links pinning the same transit pair),
// with EVERY node enrolled so the whole graph compiles, must still stage successfully — the heal at
// the top of CompileAndStage strips the colliding edge so it re-allocates cleanly — and the re-stored
// topology must validate collision-free. Without the heal, the full-graph compile fails semantic
// validation (the collision) and the stage errors, so this is load-bearing.
func TestCompileAndStage_HealsPreExistingCollision(t *testing.T) {
	router := func(id, name, host string) model.Node {
		return model.Node{
			ID: id, Name: name, Hostname: host, Role: "router", DomainID: "domain-1",
			Capabilities: model.NodeCapabilities{CanAcceptInbound: true, CanForward: true, HasPublicIP: true},
		}
	}
	// e-ab and e-bc are DIFFERENT links both pinning transit .1/.2 — the stored corruption.
	corrupt := &model.Topology{
		Project: model.Project{ID: "heal-stage-001", Name: "Heal Stage"},
		Domains: []model.Domain{{ID: "domain-1", Name: "net", CIDR: "10.60.0.0/24", AllocationMode: "auto", RoutingMode: "babel"}},
		Nodes:   []model.Node{router("node-a", "rA", "a.example.com"), router("node-b", "rB", "b.example.com"), router("node-c", "rC", "c.example.com")},
		Edges: []model.Edge{
			{ID: "e-ab", FromNodeID: "node-a", ToNodeID: "node-b", Type: "public-endpoint", EndpointHost: "198.51.100.2", Transport: "udp", IsEnabled: true,
				PinnedFromPort: 51820, PinnedToPort: 51820, PinnedFromTransitIP: "10.10.0.1", PinnedToTransitIP: "10.10.0.2", PinnedFromLinkLocal: "fe80::1", PinnedToLinkLocal: "fe80::2"},
			{ID: "e-bc", FromNodeID: "node-b", ToNodeID: "node-c", Type: "public-endpoint", EndpointHost: "198.51.100.3", Transport: "udp", IsEnabled: true,
				PinnedFromPort: 51821, PinnedToPort: 51821, PinnedFromTransitIP: "10.10.0.1", PinnedToTransitIP: "10.10.0.2", PinnedFromLinkLocal: "fe80::1", PinnedToLinkLocal: "fe80::2"},
		},
	}

	store := NewMemStore()
	tnt := TenantID("heal-stage")
	ctx := context.Background()
	raw, err := json.Marshal(corrupt)
	if err != nil {
		t.Fatalf("marshal corrupt topo: %v", err)
	}
	if _, err := store.PutTopology(ctx, tnt, raw); err != nil {
		t.Fatalf("PutTopology: %v", err)
	}
	approveNode(t, ctx, store, tnt, "node-a", genWGPubKey(t))
	approveNode(t, ctx, store, tnt, "node-b", genWGPubKey(t))
	approveNode(t, ctx, store, tnt, "node-c", genWGPubKey(t))

	// Stage must SUCCEED (the heal removes the collision the full-graph compile would otherwise reject).
	if _, err := CompileAndStage(ctx, store, tnt, time.Now()); err != nil {
		t.Fatalf("CompileAndStage on a corrupt-but-fully-enrolled topology: %v (heal-on-stage should have cleaned it)", err)
	}

	// The re-stored topology must validate collision-free.
	rec, err := store.GetTopology(ctx, tnt)
	if err != nil {
		t.Fatalf("GetTopology after stage: %v", err)
	}
	var stored model.Topology
	if err := json.Unmarshal(rec.JSON, &stored); err != nil {
		t.Fatalf("unmarshal stored: %v", err)
	}
	res := validator.ValidateSemantic(&stored)
	for _, e := range res.Errors {
		if e.Code == "validation_pin_transit_ip_duplicate_cross_link" ||
			e.Code == "validation_pin_port_duplicate_cross_link" ||
			e.Code == "validation_pin_link_local_duplicate_cross_link" {
			t.Errorf("stored topology still has a cross-link pin collision after stage: %s %s", e.Code, e.Field)
		}
	}
}
