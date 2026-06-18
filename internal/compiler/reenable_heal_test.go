package compiler

import (
	"testing"

	"github.com/kunorikiku/yet-another-overlay-generator/internal/model"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/normalize"
)

// C2 (plan-8 Phase 6.3) — re-enable collision auto-heal.
//
// Real fleet sequence that produces the corruption:
//  1. edge alpha<->charlie compiles enabled, gets transit pair .1/.2 (pinned, persisted).
//  2. alpha<->charlie is DISABLED. Its pins persist dormant; a disabled edge is excluded
//     from gap-fill AND from reservation (peers.go skips !IsEnabled edges), so its .1/.2 slot
//     is free again.
//  3. edge alpha<->bravo is ADDED while alpha<->charlie is disabled. It is the only enabled
//     link, so reserve-first gap-fill assigns it the lowest free pair .1/.2 (pinned, persisted).
//     alpha<->bravo is now the deployed INCUMBENT that legitimately owns .1/.2.
//  4. alpha<->charlie is RE-ENABLED. It still carries its stale .1/.2 pin. Now two ENABLED
//     edges of DIFFERENT links both pin .1/.2 — the "occupied by two different links" corruption.
//
// The discriminator (plan-8 Phase 6.3): model.Edge has no age/timestamp, so "keep the
// longer-lived edge" is uncodeable and array order MUST NOT decide. The heal claims resources
// in reserve-first (linkKey-sorted) order — the SAME order the allocator's Pass-1 gap-fill uses
// — so the edge whose pin reserve-first allocation reproduces is the incumbent and keeps its
// pin; the colliding cross-link edge with no reserved backing (the re-enabled one) is stripped.
//
// Here the incumbent alpha<->bravo has the SMALLER linkKey ("alpha|bravo" < "alpha|charlie")
// yet sits LATER in array order, while the re-enabled alpha<->charlie has the LARGER linkKey but
// sits EARLIER in the array. So the correct outcome (strip alpha<->charlie, keep alpha<->bravo)
// is reproducible ONLY by the reserve-first/linkKey discriminator — array order ("first claimant
// wins") would wrongly keep the earlier, stale alpha<->charlie and strip the live incumbent.
func reenableCollisionTopology() *model.Topology {
	const (
		incumbentTransitFrom = "10.10.0.1" // alpha side
		incumbentTransitTo   = "10.10.0.2" // bravo side
	)
	return &model.Topology{
		Project: model.Project{ID: "c2", Name: "C2 re-enable heal"},
		Domains: []model.Domain{{
			ID:             "domain-1",
			Name:           "net",
			CIDR:           "10.0.0.0/16",
			TransitCIDR:    "10.10.0.0/24",
			AllocationMode: "auto",
			RoutingMode:    "babel",
		}},
		Nodes: []model.Node{
			{ID: "alpha", Name: "alpha", Hostname: "alpha.example.com", Platform: "debian", Role: "router", DomainID: "domain-1",
				Capabilities: model.NodeCapabilities{CanAcceptInbound: true, CanForward: true, HasPublicIP: true}},
			{ID: "bravo", Name: "bravo", Hostname: "bravo.example.com", Platform: "debian", Role: "router", DomainID: "domain-1",
				Capabilities: model.NodeCapabilities{CanAcceptInbound: true, CanForward: true, HasPublicIP: true}},
			{ID: "charlie", Name: "charlie", Hostname: "charlie.example.com", Platform: "debian", Role: "router", DomainID: "domain-1",
				Capabilities: model.NodeCapabilities{CanAcceptInbound: true, CanForward: true, HasPublicIP: true}},
		},
		Edges: []model.Edge{
			// Re-enabled edge: LARGER linkKey ("alpha|charlie"), EARLIER array position, stale .1/.2 pin.
			{
				ID: "alpha-charlie", FromNodeID: "alpha", ToNodeID: "charlie",
				Type: "direct", EndpointHost: "203.0.113.3", Transport: "udp", IsEnabled: true,
				PinnedFromPort: 51820, PinnedToPort: 51820,
				PinnedFromTransitIP: incumbentTransitFrom, PinnedToTransitIP: incumbentTransitTo,
				PinnedFromLinkLocal: "fe80::1", PinnedToLinkLocal: "fe80::2",
			},
			// Incumbent edge: SMALLER linkKey ("alpha|bravo"), LATER array position, legitimately owns .1/.2.
			{
				ID: "alpha-bravo", FromNodeID: "alpha", ToNodeID: "bravo",
				Type: "direct", EndpointHost: "203.0.113.2", Transport: "udp", IsEnabled: true,
				PinnedFromPort: 51820, PinnedToPort: 51820,
				PinnedFromTransitIP: incumbentTransitFrom, PinnedToTransitIP: incumbentTransitTo,
				PinnedFromLinkLocal: "fe80::1", PinnedToLinkLocal: "fe80::2",
			},
		},
	}
}

func edgeByIDPtr(topo *model.Topology, id string) *model.Edge {
	for i := range topo.Edges {
		if topo.Edges[i].ID == id {
			return &topo.Edges[i]
		}
	}
	return nil
}

// TestReenableHeal_StripsReenabledNotIncumbent is the C2 repro. It asserts the heal strips the
// re-enabled edge (no reserved backing) and leaves the incumbent's pins intact — NOT the array
// order ("first claimant").
func TestReenableHeal_StripsReenabledNotIncumbent(t *testing.T) {
	topo := reenableCollisionTopology()

	if !normalize.HealCollidingPins(topo) {
		t.Fatalf("HealCollidingPins reported no change; expected it to strip the re-enabled collider")
	}

	incumbent := edgeByIDPtr(topo, "alpha-bravo")
	reenabled := edgeByIDPtr(topo, "alpha-charlie")

	// Incumbent (smaller linkKey, reproduced by reserve-first allocation) keeps its pins.
	if incumbent.PinnedFromTransitIP != "10.10.0.1" || incumbent.PinnedToTransitIP != "10.10.0.2" {
		t.Errorf("incumbent alpha-bravo lost its pins: from=%q to=%q (expected 10.10.0.1/10.10.0.2)",
			incumbent.PinnedFromTransitIP, incumbent.PinnedToTransitIP)
	}
	if incumbent.PinnedFromPort != 51820 || incumbent.PinnedToPort != 51820 {
		t.Errorf("incumbent alpha-bravo lost its port pins: from=%d to=%d", incumbent.PinnedFromPort, incumbent.PinnedToPort)
	}

	// Re-enabled (larger linkKey, earlier in array) is stripped wholesale.
	if reenabled.PinnedFromTransitIP != "" || reenabled.PinnedToTransitIP != "" ||
		reenabled.PinnedFromPort != 0 || reenabled.PinnedToPort != 0 ||
		reenabled.PinnedFromLinkLocal != "" || reenabled.PinnedToLinkLocal != "" {
		t.Errorf("re-enabled alpha-charlie should have been stripped, but still carries pins: %+v", *reenabled)
	}
}

// TestReenableHeal_CleanCompileFreshPin drives the FULL pipeline: heal then Compile. After the
// heal the topology must validate + compile cleanly (no CodePin*DuplicateCrossLink), the
// incumbent's allocation is unchanged, and the re-enabled edge is re-allocated into a FRESH
// non-colliding transit pair (.3/.4, the next free slot under reserve-first gap-fill).
func TestReenableHeal_CleanCompileFreshPin(t *testing.T) {
	topo := reenableCollisionTopology()

	normalize.HealCollidingPins(topo)

	keys := map[string]KeyPair{
		"alpha":   {PrivateKey: "privkey-alpha-fake", PublicKey: "pubkey-alpha-fake"},
		"bravo":   {PrivateKey: "privkey-bravo-fake", PublicKey: "pubkey-bravo-fake"},
		"charlie": {PrivateKey: "privkey-charlie-fake", PublicKey: "pubkey-charlie-fake"},
	}
	result, err := NewCompiler().Compile(topo, keys)
	if err != nil {
		t.Fatalf("compile after heal should succeed cleanly, got: %v", err)
	}

	out := result.Topology
	incumbent := edgeByIDPtr(out, "alpha-bravo")
	reenabled := edgeByIDPtr(out, "alpha-charlie")

	// Incumbent keeps its slot (.1/.2): the heal never touched it.
	if incumbent.PinnedFromTransitIP != "10.10.0.1" || incumbent.PinnedToTransitIP != "10.10.0.2" {
		t.Errorf("incumbent alpha-bravo moved: from=%q to=%q (expected 10.10.0.1/10.10.0.2)",
			incumbent.PinnedFromTransitIP, incumbent.PinnedToTransitIP)
	}

	// Re-enabled edge gets a FRESH, non-colliding pair — the next free slot is .3/.4.
	if reenabled.PinnedFromTransitIP == "" || reenabled.PinnedToTransitIP == "" {
		t.Fatalf("re-enabled alpha-charlie was not re-allocated a transit pair after compile")
	}
	if reenabled.PinnedFromTransitIP == incumbent.PinnedFromTransitIP ||
		reenabled.PinnedToTransitIP == incumbent.PinnedToTransitIP ||
		reenabled.PinnedFromTransitIP == incumbent.PinnedToTransitIP ||
		reenabled.PinnedToTransitIP == incumbent.PinnedFromTransitIP {
		t.Errorf("re-enabled alpha-charlie still collides with the incumbent: charlie=%s/%s vs bravo=%s/%s",
			reenabled.PinnedFromTransitIP, reenabled.PinnedToTransitIP,
			incumbent.PinnedFromTransitIP, incumbent.PinnedToTransitIP)
	}
	if reenabled.PinnedFromTransitIP != "10.10.0.3" || reenabled.PinnedToTransitIP != "10.10.0.4" {
		t.Errorf("re-enabled alpha-charlie expected fresh pair 10.10.0.3/10.10.0.4, got %s/%s",
			reenabled.PinnedFromTransitIP, reenabled.PinnedToTransitIP)
	}
}
