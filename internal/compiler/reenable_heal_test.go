package compiler

import (
	"context"
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
// — so the kept claimant of a slot is the SMALLER-linkKey (reserve-first) owner; the colliding
// cross-link edge that reserve-first allocation would NOT put there is stripped.
//
// This topology is the COMMON re-enable case, where the smaller-linkKey owner is also the
// historical incumbent: alpha<->bravo has the SMALLER linkKey ("alpha|bravo" < "alpha|charlie")
// yet sits LATER in array order, while the re-enabled alpha<->charlie has the LARGER linkKey but
// sits EARLIER in the array. So the correct outcome (strip alpha<->charlie, keep alpha<->bravo)
// is reproducible ONLY by the reserve-first/linkKey discriminator — array order ("first claimant
// wins") would wrongly keep the earlier, stale alpha<->charlie and strip the live incumbent.
//
// IMPORTANT: these tests do NOT prove the heal always preserves the historical incumbent — that
// is not a guarantee (model.Edge has no timestamp). They prove it preserves the smaller-linkKey
// reserve-first owner, which HAPPENS to be the incumbent here. The divergent case where the
// incumbent has the LARGER linkKey (and is therefore NOT preserved) is covered by
// TestReenableHeal_DivergentIncumbentLargerLinkKey below, which asserts only the guaranteed
// properties (clean / deterministic / stable fixed point), not incumbent-preservation.
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

// TestReenableHeal_StripsReenabledNotIncumbent is the C2 repro for the common case (incumbent ==
// smaller-linkKey reserve-first owner). It asserts the heal strips the re-enabled edge (the one
// reserve-first allocation would NOT place in that slot) and leaves the smaller-linkKey edge's
// pins intact — NOT the array order ("first claimant"). Here that smaller-linkKey edge is the
// live incumbent, so the heal also happens to preserve the deployment; the divergent case where
// it does not is covered separately below.
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
	result, err := NewCompiler().Compile(context.Background(), topo, keys)
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

// divergentIncumbentLargerLinkKeyTopology builds the symmetric-ambiguity case the heal CANNOT
// disambiguate by age: two different-link enabled edges carry IDENTICAL pins at the same natural
// reserve-first slot (.1/.2), but here the historical INCUMBENT is the LARGER-linkKey edge.
//
//   - charlie<->delta has linkKey "charlie|delta" — the LARGER key. It sits EARLIER in the array
//     and is the "historical incumbent" by construction (it was the deployed edge).
//   - bravo<->charlie has linkKey "bravo|charlie" — the SMALLER key. It sits LATER in the array
//     and is the re-enabled edge that carries a stale, identical pin.
//
// Because model.Edge has no timestamp, the heal cannot know charlie<->delta was deployed first.
// reserve-first allocation would put .1/.2 on the SMALLER linkKey (bravo<->charlie), so the heal
// keeps bravo<->charlie and strips charlie<->delta — the OPPOSITE of incumbent-preservation. This
// is an accepted limit (see HealCollidingPins discriminator note). The test therefore asserts only
// the GUARANTEED properties: clean, deterministic, stable fixed point, smaller-linkKey kept.
func divergentIncumbentLargerLinkKeyTopology() *model.Topology {
	const (
		contestedTransitFrom = "10.10.0.1"
		contestedTransitTo   = "10.10.0.2"
	)
	return &model.Topology{
		Project: model.Project{ID: "c2-div", Name: "C2 divergent (incumbent larger linkKey)"},
		Domains: []model.Domain{{
			ID:             "domain-1",
			Name:           "net",
			CIDR:           "10.0.0.0/16",
			TransitCIDR:    "10.10.0.0/24",
			AllocationMode: "auto",
			RoutingMode:    "babel",
		}},
		Nodes: []model.Node{
			{ID: "bravo", Name: "bravo", Hostname: "bravo.example.com", Platform: "debian", Role: "router", DomainID: "domain-1",
				Capabilities: model.NodeCapabilities{CanAcceptInbound: true, CanForward: true, HasPublicIP: true}},
			{ID: "charlie", Name: "charlie", Hostname: "charlie.example.com", Platform: "debian", Role: "router", DomainID: "domain-1",
				Capabilities: model.NodeCapabilities{CanAcceptInbound: true, CanForward: true, HasPublicIP: true}},
			{ID: "delta", Name: "delta", Hostname: "delta.example.com", Platform: "debian", Role: "router", DomainID: "domain-1",
				Capabilities: model.NodeCapabilities{CanAcceptInbound: true, CanForward: true, HasPublicIP: true}},
		},
		Edges: []model.Edge{
			// Historical INCUMBENT: LARGER linkKey ("charlie|delta"), EARLIER array position.
			{
				ID: "charlie-delta", FromNodeID: "charlie", ToNodeID: "delta",
				Type: "direct", EndpointHost: "203.0.113.4", Transport: "udp", IsEnabled: true,
				PinnedFromPort: 51820, PinnedToPort: 51820,
				PinnedFromTransitIP: contestedTransitFrom, PinnedToTransitIP: contestedTransitTo,
				PinnedFromLinkLocal: "fe80::1", PinnedToLinkLocal: "fe80::2",
			},
			// Re-enabled edge: SMALLER linkKey ("bravo|charlie"), LATER array position, stale identical pin.
			{
				ID: "bravo-charlie", FromNodeID: "bravo", ToNodeID: "charlie",
				Type: "direct", EndpointHost: "203.0.113.2", Transport: "udp", IsEnabled: true,
				PinnedFromPort: 51820, PinnedToPort: 51820,
				PinnedFromTransitIP: contestedTransitFrom, PinnedToTransitIP: contestedTransitTo,
				PinnedFromLinkLocal: "fe80::1", PinnedToLinkLocal: "fe80::2",
			},
		},
	}
}

// pinSnapshot captures the six pins + CompiledPort of an edge so two heal runs can be compared
// byte-for-byte for determinism.
type pinSnapshot struct {
	compiledPort               int
	fromPort, toPort           int
	fromTransit, toTransit     string
	fromLinkLocal, toLinkLocal string
}

func snapshotPins(e *model.Edge) pinSnapshot {
	return pinSnapshot{
		compiledPort:  e.CompiledPort,
		fromPort:      e.PinnedFromPort,
		toPort:        e.PinnedToPort,
		fromTransit:   e.PinnedFromTransitIP,
		toTransit:     e.PinnedToTransitIP,
		fromLinkLocal: e.PinnedFromLinkLocal,
		toLinkLocal:   e.PinnedToLinkLocal,
	}
}

// TestReenableHeal_DivergentIncumbentLargerLinkKey is the symmetric-ambiguity case: the historical
// incumbent holds the LARGER linkKey. The heal does NOT (and cannot) preserve the incumbent here —
// it preserves the smaller-linkKey reserve-first owner. The test asserts ONLY the guaranteed
// properties: exactly one collision-free claimant of the contested slot remains, the smaller-linkKey
// edge keeps its pins while the other is stripped, the outcome is deterministic (heal twice ->
// identical), the topology compiles cleanly, and recompile is a stable fixed point.
func TestReenableHeal_DivergentIncumbentLargerLinkKey(t *testing.T) {
	topo := divergentIncumbentLargerLinkKeyTopology()

	if !normalize.HealCollidingPins(topo) {
		t.Fatalf("HealCollidingPins reported no change; expected it to strip one collider")
	}

	smaller := edgeByIDPtr(topo, "bravo-charlie")   // smaller linkKey "bravo|charlie" -> reserve-first owner
	incumbent := edgeByIDPtr(topo, "charlie-delta") // larger linkKey "charlie|delta" -> the historical incumbent

	// Guaranteed: the smaller-linkKey edge keeps the contested slot (it is what reserve-first
	// allocation reproduces), NOT the historical incumbent.
	if smaller.PinnedFromTransitIP != "10.10.0.1" || smaller.PinnedToTransitIP != "10.10.0.2" {
		t.Errorf("smaller-linkKey bravo-charlie should keep .1/.2, got from=%q to=%q",
			smaller.PinnedFromTransitIP, smaller.PinnedToTransitIP)
	}
	if smaller.PinnedFromPort != 51820 || smaller.PinnedToPort != 51820 {
		t.Errorf("smaller-linkKey bravo-charlie should keep its port pins, got from=%d to=%d",
			smaller.PinnedFromPort, smaller.PinnedToPort)
	}

	// Guaranteed: the OTHER claimant (here the historical incumbent) is stripped wholesale.
	if incumbent.PinnedFromTransitIP != "" || incumbent.PinnedToTransitIP != "" ||
		incumbent.PinnedFromPort != 0 || incumbent.PinnedToPort != 0 ||
		incumbent.PinnedFromLinkLocal != "" || incumbent.PinnedToLinkLocal != "" {
		t.Errorf("the stripped claimant charlie-delta should carry no pins, still has: %+v", *incumbent)
	}

	// Guaranteed: exactly one collision-free claimant of the contested slot remains — the two edges
	// no longer share .1/.2.
	if smaller.PinnedFromTransitIP == incumbent.PinnedFromTransitIP &&
		smaller.PinnedFromTransitIP != "" {
		t.Errorf("both edges still claim the contested slot after heal")
	}

	// Guaranteed: deterministic. Healing a fresh copy yields byte-identical pins.
	topo2 := divergentIncumbentLargerLinkKeyTopology()
	normalize.HealCollidingPins(topo2)
	for _, id := range []string{"bravo-charlie", "charlie-delta"} {
		if snapshotPins(edgeByIDPtr(topo, id)) != snapshotPins(edgeByIDPtr(topo2, id)) {
			t.Errorf("heal is not deterministic for edge %s: run1=%+v run2=%+v",
				id, snapshotPins(edgeByIDPtr(topo, id)), snapshotPins(edgeByIDPtr(topo2, id)))
		}
	}

	// Guaranteed: the healed topology compiles cleanly (no CodePin*DuplicateCrossLink).
	keys := map[string]KeyPair{
		"bravo":   {PrivateKey: "privkey-bravo-fake", PublicKey: "pubkey-bravo-fake"},
		"charlie": {PrivateKey: "privkey-charlie-fake", PublicKey: "pubkey-charlie-fake"},
		"delta":   {PrivateKey: "privkey-delta-fake", PublicKey: "pubkey-delta-fake"},
	}
	result, err := NewCompiler().Compile(context.Background(), topo, keys)
	if err != nil {
		t.Fatalf("compile after heal should succeed cleanly, got: %v", err)
	}
	out := result.Topology

	// The kept (smaller-linkKey) edge holds its slot; the stripped edge is re-allocated and no
	// longer collides.
	keptOut := edgeByIDPtr(out, "bravo-charlie")
	reallocOut := edgeByIDPtr(out, "charlie-delta")
	if keptOut.PinnedFromTransitIP != "10.10.0.1" || keptOut.PinnedToTransitIP != "10.10.0.2" {
		t.Errorf("kept bravo-charlie moved after compile: from=%q to=%q",
			keptOut.PinnedFromTransitIP, keptOut.PinnedToTransitIP)
	}
	if reallocOut.PinnedFromTransitIP == "" || reallocOut.PinnedToTransitIP == "" {
		t.Fatalf("stripped charlie-delta was not re-allocated a transit pair after compile")
	}
	if reallocOut.PinnedFromTransitIP == keptOut.PinnedFromTransitIP ||
		reallocOut.PinnedToTransitIP == keptOut.PinnedToTransitIP ||
		reallocOut.PinnedFromTransitIP == keptOut.PinnedToTransitIP ||
		reallocOut.PinnedToTransitIP == keptOut.PinnedFromTransitIP {
		t.Errorf("re-allocated charlie-delta still collides with kept bravo-charlie: charlie-delta=%s/%s vs bravo-charlie=%s/%s",
			reallocOut.PinnedFromTransitIP, reallocOut.PinnedToTransitIP,
			keptOut.PinnedFromTransitIP, keptOut.PinnedToTransitIP)
	}

	// Guaranteed: stable fixed point. Re-healing the COMPILED topology changes nothing, and the
	// pins are unchanged by the second heal.
	before := map[string]pinSnapshot{
		"bravo-charlie": snapshotPins(keptOut),
		"charlie-delta": snapshotPins(reallocOut),
	}
	if normalize.HealCollidingPins(out) {
		t.Errorf("re-healing the compiled topology reported a change; expected a stable fixed point")
	}
	for id, want := range before {
		if got := snapshotPins(edgeByIDPtr(out, id)); got != want {
			t.Errorf("fixed-point violated for %s: before=%+v after second heal=%+v", id, want, got)
		}
	}
}
