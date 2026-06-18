package validator

import (
	"testing"

	"github.com/kunorikiku/yet-another-overlay-generator/internal/model"
)

// Allocation-pin validation tests (invariant I7; rules in docs/spec/compiler/allocation-stability.md "Pin validation").
//
// These tests all focus on the validation that validateAllocationPins exposes via ValidateSemantic.
// Pins are stored per edge and oriented by that edge's own from/to: edge A->B's PinnedFromPort is the
// A-side port, and the reverse edge B->A carries the same pair mirrored.

// pinnedTopology, on top of validTopology, sets a "clean, paired, mirrored" set of pins on the forward
// and reverse edges of the single link (node-1 <-> node-2), serving as a valid baseline on which each
// test injects a single-point violation.
//
//	edge-1 (node-1 -> node-2): from=node-1 side, to=node-2 side
//	edge-2 (node-2 -> node-1): from=node-2 side, to=node-1 side (mirror of edge-1)
//
// Convention: node-1 port 51820 / transit 10.10.0.1 / link-local fe80::1;
//
//	node-2 port 51820 / transit 10.10.0.2 / link-local fe80::2.
func pinnedTopology() *model.Topology {
	topo := validTopology()

	// node-1's port may match node-2's: they are different nodes, each binding its own interface.
	const (
		node1Port      = 51820
		node2Port      = 51820
		node1Transit   = "10.10.0.1"
		node2Transit   = "10.10.0.2"
		node1LinkLocal = "fe80::1"
		node2LinkLocal = "fe80::2"
	)

	// edge-1: node-1 -> node-2. from = node-1, to = node-2.
	topo.Edges[0].PinnedFromPort = node1Port
	topo.Edges[0].PinnedToPort = node2Port
	topo.Edges[0].PinnedFromTransitIP = node1Transit
	topo.Edges[0].PinnedToTransitIP = node2Transit
	topo.Edges[0].PinnedFromLinkLocal = node1LinkLocal
	topo.Edges[0].PinnedToLinkLocal = node2LinkLocal

	// edge-2: node-2 -> node-1. from = node-2, to = node-1 (mirror).
	topo.Edges[1].PinnedFromPort = node2Port
	topo.Edges[1].PinnedToPort = node1Port
	topo.Edges[1].PinnedFromTransitIP = node2Transit
	topo.Edges[1].PinnedToTransitIP = node1Transit
	topo.Edges[1].PinnedFromLinkLocal = node2LinkLocal
	topo.Edges[1].PinnedToLinkLocal = node1LinkLocal

	return topo
}

// pinErrorCount counts the errors that fall on an edge pin field or edge-index prefix, so that the
// presence of "pin validation" errors can be asserted precisely even when other unrelated errors exist.
func pinErrorCount(result *ValidationResult) int {
	n := 0
	for _, e := range result.Errors {
		if containsSubstring(e.Field, "edges[") {
			n++
		}
	}
	return n
}

// --- clean pins accepted ---

// TestValidateAllocationPins_CleanPinsAccepted asserts that a complete, paired, mirrored, in-pool set of pins produces no errors.
func TestValidateAllocationPins_CleanPinsAccepted(t *testing.T) {
	topo := pinnedTopology()
	result := ValidateSemantic(topo)
	if !result.IsValid() {
		t.Errorf("clean paired pins should pass validation, but reported %d errors:", len(result.Errors))
		for _, e := range result.Errors {
			t.Errorf("  %s", e.Error())
		}
	}
}

// TestValidateAllocationPins_NoPinsAccepted asserts that a topology with no pins at all (unpinned, left for gap-fill) must pass validation.
func TestValidateAllocationPins_NoPinsAccepted(t *testing.T) {
	topo := validTopology()
	result := ValidateSemantic(topo)
	if !result.IsValid() {
		t.Errorf("a topology with no pins should pass validation, but reported %d errors:", len(result.Errors))
		for _, e := range result.Errors {
			t.Errorf("  %s", e.Error())
		}
	}
}

// --- partial pins (pair completeness) ---

// TestValidateAllocationPins_PartialPortPair asserts that pinning only one end's port is rejected.
func TestValidateAllocationPins_PartialPortPair(t *testing.T) {
	topo := validTopology()
	topo.Edges[0].PinnedFromPort = 51820 // only the from side, to side left empty
	result := ValidateSemantic(topo)
	assertHasError(t, result, "edges[0]")
}

// TestValidateAllocationPins_PartialTransitPair asserts that pinning only one end's transit IP is rejected.
func TestValidateAllocationPins_PartialTransitPair(t *testing.T) {
	topo := validTopology()
	topo.Edges[0].PinnedFromTransitIP = "10.10.0.1" // only the from side, to side left empty
	result := ValidateSemantic(topo)
	assertHasError(t, result, "edges[0]")
}

// TestValidateAllocationPins_PartialLinkLocalPair asserts that pinning only one end's link-local is rejected.
func TestValidateAllocationPins_PartialLinkLocalPair(t *testing.T) {
	topo := validTopology()
	topo.Edges[0].PinnedFromLinkLocal = "fe80::1" // only the from side, to side left empty
	result := ValidateSemantic(topo)
	assertHasError(t, result, "edges[0]")
}

// --- port out of range ---

// TestValidateAllocationPins_PortAboveMax asserts that a port pin above 65535 is rejected.
func TestValidateAllocationPins_PortAboveMax(t *testing.T) {
	topo := pinnedTopology()
	topo.Edges[0].PinnedFromPort = 70000
	topo.Edges[1].PinnedToPort = 70000 // mirror on the reverse edge, keeping the pair complete to isolate the out-of-range violation
	result := ValidateSemantic(topo)
	assertHasError(t, result, "pinned_from_port")
}

// TestValidateAllocationPins_PortBelowMin asserts that a port pin below the manual-pin lower bound
// minPinnedPort (1024, the privileged-port range) is rejected.
func TestValidateAllocationPins_PortBelowMin(t *testing.T) {
	topo := pinnedTopology()
	topo.Edges[0].PinnedFromPort = 500 // < 1024 (privileged-port range)
	topo.Edges[1].PinnedToPort = 500   // mirror on the reverse edge
	result := ValidateSemantic(topo)
	assertHasError(t, result, "pinned_from_port")
}

// TestValidateAllocationPins_NATRangePortAccepted is PR7's regression guard: auto-allocation starts
// at 51820, but a port-restricted NAT VPS often forwards only a band of ports below 51820 (e.g.
// 30000-30100). An operator manually pinning an internal listen port within that range (>=1024) must
// be accepted -- otherwise it cannot work with the NAT forwarding rule (which is exactly the point of
// relaxing the lower bound).
func TestValidateAllocationPins_NATRangePortAccepted(t *testing.T) {
	topo := pinnedTopology()
	topo.Edges[0].PinnedFromPort = 30050 // within the NAT range, below the old 51820 bound, but >=1024
	topo.Edges[1].PinnedToPort = 30050   // mirror on the reverse edge
	result := ValidateSemantic(topo)
	for _, e := range result.Errors {
		if contains(e.Field, "pinned_from_port") || contains(e.Field, "pinned_to_port") {
			t.Errorf("NAT-range port 30050 (>=1024) should be accepted, but errored: %s", e.Error())
		}
	}
}

// --- transit IP out of pool ---

// TestValidateAllocationPins_TransitOutOfCIDR asserts that a transit IP pin outside the transit pool resolved for that edge is rejected.
func TestValidateAllocationPins_TransitOutOfCIDR(t *testing.T) {
	topo := pinnedTopology()
	// the domain transit pool falls back to the default 10.10.0.0/24; pin an out-of-pool address.
	topo.Edges[0].PinnedFromTransitIP = "192.168.99.1"
	topo.Edges[1].PinnedToTransitIP = "192.168.99.1" // mirror on the reverse edge
	result := ValidateSemantic(topo)
	assertHasError(t, result, "pinned_from_transit_ip")
}

// TestValidateAllocationPins_TransitNarrowedPoolStale asserts that when transit_cidr is narrowed, a previously-legal pin becomes out-of-pool and is rejected.
func TestValidateAllocationPins_TransitNarrowedPoolStale(t *testing.T) {
	topo := pinnedTopology()
	// narrow the domain transit pool to 10.10.0.0/30 (usable hosts 10.10.0.1, 10.10.0.2).
	topo.Domains[0].TransitCIDR = "10.10.0.0/30"
	// pin the node-1 side transit to 10.10.0.5, now outside the narrowed pool.
	topo.Edges[0].PinnedFromTransitIP = "10.10.0.5"
	topo.Edges[1].PinnedToTransitIP = "10.10.0.5" // mirror on the reverse edge
	result := ValidateSemantic(topo)
	assertHasError(t, result, "pinned_from_transit_ip")
}

// --- cross-link duplicate occupancy ---

// threeNodeTwoLinkTopology builds two independent links, node-1 with node-2 and node-1 with node-3,
// sharing node-1, to detect the "same node port", "same transit IP", and "same link-local" being
// occupied twice by two different links.
func threeNodeTwoLinkTopology() *model.Topology {
	topo := pinnedTopology() // node-1 <-> node-2 is already a clean-pinned link

	// append node-3 and replace validTopology's redundant reverse edge with a new link toward node-3.
	topo.Nodes = append(topo.Nodes, model.Node{
		ID:       "node-3",
		Name:     "node-gamma",
		Hostname: "gamma.example.com",
		Platform: "debian",
		Role:     "router",
		DomainID: "domain-1",
		Capabilities: model.NodeCapabilities{
			CanAcceptInbound: true,
			CanForward:       true,
			HasPublicIP:      true,
		},
	})

	// new link node-1 -> node-3 (a single edge is enough, clean and not conflicting with the node-1<->node-2 link).
	topo.Edges = append(topo.Edges, model.Edge{
		ID:                  "edge-3",
		FromNodeID:          "node-1",
		ToNodeID:            "node-3",
		Type:                "direct",
		EndpointHost:        "203.0.113.3",
		Transport:           "udp",
		IsEnabled:           true,
		PinnedFromPort:      51821, // node-1's other interface port on this link (different from 51820)
		PinnedToPort:        51820, // node-3's port
		PinnedFromTransitIP: "10.10.0.3",
		PinnedToTransitIP:   "10.10.0.4",
		PinnedFromLinkLocal: "fe80::3",
		PinnedToLinkLocal:   "fe80::4",
	})

	return topo
}

// TestValidateAllocationPins_ThreeNodeTwoLinkBaselineClean asserts that the baseline of two independent links, each cleanly pinned, must pass validation.
func TestValidateAllocationPins_ThreeNodeTwoLinkBaselineClean(t *testing.T) {
	topo := threeNodeTwoLinkTopology()
	result := ValidateSemantic(topo)
	if pinErrorCount(result) != 0 {
		t.Errorf("clean pins on two independent links should have no pin errors, but reported %d: %v", pinErrorCount(result), result.Errors)
	}
}

// TestValidateAllocationPins_DuplicatePortOnNodeAcrossLinks asserts that node-1 pinning the same port on two different links is rejected.
func TestValidateAllocationPins_DuplicatePortOnNodeAcrossLinks(t *testing.T) {
	topo := threeNodeTwoLinkTopology()
	// node-1's port on the node-1<->node-2 link is 51820; make node-1's port on the node-1<->node-3 link also 51820.
	topo.Edges[2].PinnedFromPort = 51820
	result := ValidateSemantic(topo)
	if pinErrorCount(result) == 0 {
		t.Errorf("the same node pinning the same port on two different links should error, but there were no pin errors: %v", result.Errors)
	}
}

// TestValidateAllocationPins_DuplicateTransitIPAcrossLinks asserts that two different links pinning the same transit IP is rejected.
func TestValidateAllocationPins_DuplicateTransitIPAcrossLinks(t *testing.T) {
	topo := threeNodeTwoLinkTopology()
	// the node-1<->node-2 link occupies 10.10.0.1; make the node-1<->node-3 link also occupy 10.10.0.1.
	topo.Edges[2].PinnedFromTransitIP = "10.10.0.1"
	result := ValidateSemantic(topo)
	if pinErrorCount(result) == 0 {
		t.Errorf("two different links pinning the same transit IP should error, but there were no pin errors: %v", result.Errors)
	}
}

// TestValidateAllocationPins_DuplicateLinkLocalAcrossLinks asserts that two different links pinning the same link-local is rejected.
func TestValidateAllocationPins_DuplicateLinkLocalAcrossLinks(t *testing.T) {
	topo := threeNodeTwoLinkTopology()
	// the node-1<->node-2 link occupies fe80::1; make the node-1<->node-3 link also occupy fe80::1.
	topo.Edges[2].PinnedFromLinkLocal = "fe80::1"
	result := ValidateSemantic(topo)
	if pinErrorCount(result) == 0 {
		t.Errorf("two different links pinning the same link-local should error, but there were no pin errors: %v", result.Errors)
	}
}

// TestValidateAllocationPins_ReverseEdgeNotDuplicate asserts that the forward and reverse edges of the same link carrying mirrored pins must not be misjudged as duplicate occupancy.
func TestValidateAllocationPins_ReverseEdgeNotDuplicate(t *testing.T) {
	// pinnedTopology's edge-1/edge-2 are the forward and reverse edges of the same link, already mirrored.
	topo := pinnedTopology()
	result := ValidateSemantic(topo)
	if pinErrorCount(result) != 0 {
		t.Errorf("the forward/reverse edges of the same link (mirrored pins) should not be judged duplicate occupancy, but reported %d pin errors: %v", pinErrorCount(result), result.Errors)
	}
}

// --- client edge pins ---

// clientEdgeTopology builds a client node connected to a router via a single edge, to test pin handling on client edges.
func clientEdgeTopology() *model.Topology {
	topo := validTopology()

	// change node-2 to a client and drop the reverse edge targeting the client (a client accepts no inbound).
	topo.Nodes[1].Role = "client"
	// keep only the node-2(client) -> node-1(router) outbound edge and add the endpoint_host the client needs.
	topo.Edges = []model.Edge{
		{
			ID:           "edge-1",
			FromNodeID:   "node-2",
			ToNodeID:     "node-1",
			Type:         "direct",
			EndpointHost: "203.0.113.1",
			Transport:    "udp",
			IsEnabled:    true,
		},
	}
	return topo
}

// TestValidateAllocationPins_ClientEdgePortPinRejected asserts that a client edge carrying a port pin errors (a client uses a single wg0 with no per-peer port).
func TestValidateAllocationPins_ClientEdgePortPinRejected(t *testing.T) {
	topo := clientEdgeTopology()
	topo.Edges[0].PinnedFromPort = 51820
	topo.Edges[0].PinnedToPort = 51820
	result := ValidateSemantic(topo)
	if pinErrorCount(result) == 0 {
		t.Errorf("a client edge carrying a port pin should error, but there were no pin errors: %v", result.Errors)
	}
}

// TestValidateAllocationPins_ClientEdgeResourcePinWarns asserts that a client edge carrying transit/link-local pins warns (they will be ignored) rather than errors.
func TestValidateAllocationPins_ClientEdgeResourcePinWarns(t *testing.T) {
	topo := clientEdgeTopology()
	topo.Edges[0].PinnedFromTransitIP = "10.10.0.1"
	topo.Edges[0].PinnedToTransitIP = "10.10.0.2"
	result := ValidateSemantic(topo)

	// transit/link-local pins on a client edge must not produce a pin error.
	if pinErrorCount(result) != 0 {
		t.Errorf("transit/link-local pins on a client edge should only warn, not error, but reported pin errors: %v", result.Errors)
	}
	assertHasWarning(t, result, "edges[0]")
}
