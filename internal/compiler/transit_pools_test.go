package compiler

import (
	"testing"

	"github.com/kunorikiku/yet-another-overlay-generator/internal/apierr"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/model"
)

// This file covers three fixes in Phase A (per-CIDR transit pools):
//   - D12: each transit address pool counts independently by its "resolved CIDR", never sharing numbering.
//   - D48: allocation never hits a network or broadcast address, and returns a clean exhaustion error when out of range.
//   - D70: link-local is rendered in hex (fe80::b, rather than treating decimal 11 as the address).

// transitPoolNode is a small helper for constructing test nodes, uniformly filling in router capabilities and overlay IP.
func transitPoolNode(id, domainID, overlayIP string) model.Node {
	return model.Node{
		ID: id, Name: id, Hostname: id + ".example.com",
		Role: "router", DomainID: domainID,
		OverlayIP:    overlayIP,
		Capabilities: model.NodeCapabilities{CanAcceptInbound: true, CanForward: true, HasPublicIP: true},
	}
}

// TestPerCIDRTransitPools_IndependentAllocation verifies D12: when two domains use
// different transit_cidr values, each address pool consumes independently from index 0.
// Even though domain A has already consumed 10 pair indices, domain B's /30 custom pool
// can still fit its sole pair (10.20.0.1 / 10.20.0.2).
//
// Under the single global counter before the fix, domain B's edge would land at a global
// index >=10, which for a /30 far exceeds the pool capacity and reports "address pool
// exhausted" -- exactly the regression this case is meant to prevent.
func TestPerCIDRTransitPools_IndependentAllocation(t *testing.T) {
	// Domain A: a star of 1 hub + 10 spokes, producing 10 edges -> domain A's pool consumes index 0..9.
	const spokeCount = 10
	nodes := []model.Node{
		transitPoolNode("a-hub", "domain-a", "10.11.0.1"),
	}
	keys := map[string]KeyPair{
		"a-hub": {PrivateKey: "privkey-a-hub-fake", PublicKey: "pubkey-a-hub-fake"},
	}
	var edges []model.Edge
	for i := 0; i < spokeCount; i++ {
		spokeID := "a-spoke-" + string(rune('a'+i))
		nodes = append(nodes, transitPoolNode(spokeID, "domain-a", "10.11.0."+itoaTest(i+2)))
		keys[spokeID] = KeyPair{PrivateKey: "privkey-" + spokeID + "-fake", PublicKey: "pubkey-" + spokeID + "-fake"}
		edges = append(edges, model.Edge{
			ID: "e-a-" + spokeID, FromNodeID: "a-hub", ToNodeID: spokeID,
			Type: "direct", Transport: "udp", IsEnabled: true,
		})
	}

	// Domain B: a single edge, the from node is in domain B, and domain B's transit_cidr is a /30 (exactly one pair of usable hosts).
	nodes = append(nodes,
		transitPoolNode("b-one", "domain-b", "10.12.0.1"),
		transitPoolNode("b-two", "domain-b", "10.12.0.2"),
	)
	keys["b-one"] = KeyPair{PrivateKey: "privkey-b-one-fake", PublicKey: "pubkey-b-one-fake"}
	keys["b-two"] = KeyPair{PrivateKey: "privkey-b-two-fake", PublicKey: "pubkey-b-two-fake"}
	// Order domain B's edge after all of domain A's edges to ensure that, "if it were still a global counter", it would get a high index.
	edges = append(edges, model.Edge{
		ID: "e-b", FromNodeID: "b-one", ToNodeID: "b-two",
		Type: "direct", Transport: "udp", IsEnabled: true,
	})

	topo := &model.Topology{
		Project: model.Project{ID: "transit-pools-001", Name: "Transit Pools"},
		Domains: []model.Domain{
			{ID: "domain-a", Name: "alpha", CIDR: "10.11.0.0/24", AllocationMode: "auto", RoutingMode: "babel"},
			// transit left empty -> resolves to default 10.10.0.0/24 (never shares numbering with domain B's /30).
			{ID: "domain-b", Name: "beta", CIDR: "10.12.0.0/24", AllocationMode: "auto", RoutingMode: "babel",
				TransitCIDR: "10.20.0.0/30"},
		},
		Nodes: nodes,
		Edges: edges,
	}

	_, allocations, err := DerivePeers(topo, keys)
	if err != nil {
		t.Fatalf("domain B's /30 pool counting independently should fit its sole pair, but DerivePeers errored: %v", err)
	}

	bAlloc := allocations["b-one->b-two"]
	if bAlloc == nil {
		t.Fatalf("a pairAllocation should be generated for b-one->b-two")
	}
	// Domain B's pool starts at index 0: the usable hosts of a /30 are exactly .1 and .2.
	wantPair := map[string]bool{"10.20.0.1": true, "10.20.0.2": true}
	if !wantPair[bAlloc.localTransit] || !wantPair[bAlloc.remoteTransit] || bAlloc.localTransit == bAlloc.remoteTransit {
		t.Errorf("domain B (10.20.0.0/30) should allocate {10.20.0.1, 10.20.0.2}, got local=%s remote=%s",
			bAlloc.localTransit, bAlloc.remoteTransit)
	}

	// Domain A's pool should also start at index 0 (default 10.10.0.0/24): the first edge is .1/.2, unaffected by domain B.
	aAlloc := allocations["a-hub->a-spoke-a"]
	if aAlloc == nil {
		t.Fatalf("a pairAllocation should be generated for a-hub->a-spoke-a")
	}
	wantAPair := map[string]bool{"10.10.0.1": true, "10.10.0.2": true}
	if !wantAPair[aAlloc.localTransit] || !wantAPair[aAlloc.remoteTransit] {
		t.Errorf("domain A (default 10.10.0.0/24) first edge should allocate {10.10.0.1, 10.10.0.2}, got local=%s remote=%s",
			aAlloc.localTransit, aAlloc.remoteTransit)
	}
}

// TestPerCIDRTransitPools_SmallPoolExhausts verifies D48/D12: a /30 pool can hold only
// one pair, so a second link in the same pool must fail with a clean exhaustion error
// (rather than silently emitting the broadcast address .3).
func TestPerCIDRTransitPools_SmallPoolExhausts(t *testing.T) {
	nodes := []model.Node{
		transitPoolNode("n1", "domain-x", "10.40.0.1"),
		transitPoolNode("n2", "domain-x", "10.40.0.2"),
		transitPoolNode("n3", "domain-x", "10.40.0.3"),
	}
	keys := map[string]KeyPair{
		"n1": {PrivateKey: "pk-n1", PublicKey: "pub-n1"},
		"n2": {PrivateKey: "pk-n2", PublicKey: "pub-n2"},
		"n3": {PrivateKey: "pk-n3", PublicKey: "pub-n3"},
	}
	topo := &model.Topology{
		Project: model.Project{ID: "transit-pools-002", Name: "Small Pool"},
		Domains: []model.Domain{
			{ID: "domain-x", Name: "x", CIDR: "10.40.0.0/24", AllocationMode: "auto", RoutingMode: "babel",
				TransitCIDR: "10.20.0.0/30"},
		},
		Nodes: nodes,
		Edges: []model.Edge{
			// Two distinct links both draw from the /30 pool: the first takes index 0 (.1/.2), the second needs index 1 -> broadcast .3.
			{ID: "e1", FromNodeID: "n1", ToNodeID: "n2", Type: "direct", Transport: "udp", IsEnabled: true},
			{ID: "e2", FromNodeID: "n1", ToNodeID: "n3", Type: "direct", Transport: "udp", IsEnabled: true},
		},
	}

	_, _, err := DerivePeers(topo, keys)
	if err == nil {
		t.Fatalf("a /30 pool holds only one pair; the second link should exhaust it, but got nil")
	}
	if !apierr.HasCode(err, apierr.CodeTransitPoolExhausted) {
		t.Errorf("expected a transit-pool-exhausted coded error, got: %q", err.Error())
	}
}

// TestAllocateTransitPair_NeverNetworkOrBroadcast verifies D48: walking through every
// index of a /29 pool, no successfully allocated address may be the network address
// (.0) or the broadcast address (.7); out of range returns an exhaustion error.
// A /29 (10.30.0.0/29) has usable hosts .1..6, so it should yield 3 pairs (index 0/1/2)
// and be exhausted from index 3 onward.
func TestAllocateTransitPair_NeverNetworkOrBroadcast(t *testing.T) {
	const cidr = "10.30.0.0/29"
	const networkAddr = "10.30.0.0"
	const broadcastAddr = "10.30.0.7"

	successCount := 0
	exhausted := false
	// Walk more indices than the pool capacity to ensure coverage of the exhaustion boundary and (possible) out-of-range wraparound.
	for index := 0; index < 16; index++ {
		ip1, ip2, err := allocateTransitPair(index, cidr)
		if err != nil {
			exhausted = true
			continue
		}
		successCount++
		for _, ip := range []string{ip1, ip2} {
			if ip == networkAddr {
				t.Errorf("index %d allocated the network address %s, never allowed", index, networkAddr)
			}
			if ip == broadcastAddr {
				t.Errorf("index %d allocated the broadcast address %s, never allowed", index, broadcastAddr)
			}
		}
		if ip1 == ip2 {
			t.Errorf("index %d pair of addresses should not be identical (%s)", index, ip1)
		}
	}

	if successCount != 3 {
		t.Errorf("/29 pool usable hosts .1..6, should allocate exactly 3 pairs, got %d pairs", successCount)
	}
	if !exhausted {
		t.Errorf("indices beyond pool capacity should return an exhaustion error, but it was never observed")
	}

	// Explicitly assert the concrete addresses of index 0, pinning the "start at .1, skip the network address" semantics.
	ip1, ip2, err := allocateTransitPair(0, cidr)
	if err != nil {
		t.Fatalf("index 0 should succeed, got error: %v", err)
	}
	if ip1 != "10.30.0.1" || ip2 != "10.30.0.2" {
		t.Errorf("index 0 should be {10.30.0.1, 10.30.0.2}, got {%s, %s}", ip1, ip2)
	}
}

// TestAllocateLinkLocalPair_RendersHex verifies D70: IPv6 link-local is rendered in hex.
// index 5 -> base = 2*5+1 = 11 -> fe80::b / fe80::c (rather than writing decimal 11 as
// fe80::11, which would be parsed as 0x11 = 17, breaking the contiguous numbering the docs promise).
func TestAllocateLinkLocalPair_RendersHex(t *testing.T) {
	local, remote := allocateLinkLocalPair(5)
	if local != "fe80::b" {
		t.Errorf("index 5 local link-local should be fe80::b (hex), got %q", local)
	}
	if remote != "fe80::c" {
		t.Errorf("index 5 remote link-local should be fe80::c (hex), got %q", remote)
	}
	// Reverse safeguard: the decimal form fe80::11 must never appear again.
	if local == "fe80::11" {
		t.Errorf("index 5 should not render as decimal fe80::11 (would be parsed as fe80::17)")
	}

	// Spot-check low indices to confirm contiguous hex: index 0 -> ::1/::2, index 7 -> ::f/::10.
	if l0, r0 := allocateLinkLocalPair(0); l0 != "fe80::1" || r0 != "fe80::2" {
		t.Errorf("index 0 should be {fe80::1, fe80::2}, got {%s, %s}", l0, r0)
	}
	if l7, r7 := allocateLinkLocalPair(7); l7 != "fe80::f" || r7 != "fe80::10" {
		t.Errorf("index 7 should be {fe80::f, fe80::10} (base=15 -> 0xf, 0x10), got {%s, %s}", l7, r7)
	}
}
