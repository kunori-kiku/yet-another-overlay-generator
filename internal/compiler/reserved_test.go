package compiler

import (
	"testing"

	"github.com/kunorikiku/yet-another-overlay-generator/internal/model"
)

// TestBuildReservedFromExcludedEdges_SkipRules locks the reservation builder's exclusion rules:
// an INCLUDED edge (in the subgraph), a DISABLED edge, and a CLIENT-touched edge each contribute
// NO reservation; only the pins of an enabled, non-client, complete-pin EXCLUDED edge are reserved,
// keyed by the resolved transit CIDR / node / value. (Same package so it reads the unexported sets
// directly — no test-only accessors leak onto the production type.)
func TestBuildReservedFromExcludedEdges_SkipRules(t *testing.T) {
	topo := &model.Topology{
		Domains: []model.Domain{{ID: "domain-1", Name: "net", CIDR: "10.60.0.0/24"}},
		Nodes: []model.Node{
			{ID: "n1", Role: "router", DomainID: "domain-1"},
			{ID: "n2", Role: "router", DomainID: "domain-1"},
			{ID: "n3", Role: "client", DomainID: "domain-1"},
		},
		Edges: []model.Edge{
			// included -> not reserved
			{ID: "inc", FromNodeID: "n1", ToNodeID: "n2", IsEnabled: true,
				PinnedFromTransitIP: "10.10.0.1", PinnedToTransitIP: "10.10.0.2"},
			// excluded + disabled -> not reserved
			{ID: "dis", FromNodeID: "n1", ToNodeID: "n2", IsEnabled: false,
				PinnedFromTransitIP: "10.10.0.3", PinnedToTransitIP: "10.10.0.4"},
			// excluded + client-touched -> not reserved
			{ID: "cli", FromNodeID: "n3", ToNodeID: "n2", IsEnabled: true,
				PinnedFromTransitIP: "10.10.0.5", PinnedToTransitIP: "10.10.0.6"},
			// excluded + enabled + non-client + complete -> RESERVED
			{ID: "res", FromNodeID: "n1", ToNodeID: "n2", IsEnabled: true,
				PinnedFromPort: 51830, PinnedToPort: 51831,
				PinnedFromTransitIP: "10.10.0.7", PinnedToTransitIP: "10.10.0.8",
				PinnedFromLinkLocal: "fe80::7", PinnedToLinkLocal: "fe80::8"},
		},
	}
	r := BuildReservedFromExcludedEdges(topo, map[string]bool{"inc": true})

	const cidr = "10.10.0.0/24" // domain-1 has no transit_cidr -> default pool
	transitReserved := func(ip string) bool { return r.transitIPs[cidr] != nil && r.transitIPs[cidr][ip] }

	for _, ip := range []string{"10.10.0.7", "10.10.0.8"} {
		if !transitReserved(ip) {
			t.Errorf("transit %s should be reserved (from excluded edge 'res')", ip)
		}
	}
	for _, ip := range []string{"10.10.0.1", "10.10.0.2", "10.10.0.3", "10.10.0.4", "10.10.0.5", "10.10.0.6"} {
		if transitReserved(ip) {
			t.Errorf("transit %s should NOT be reserved (included / disabled / client edge)", ip)
		}
	}
	if !r.ports["n1"][51830] || !r.ports["n2"][51831] {
		t.Errorf("ports 51830@n1 / 51831@n2 should be reserved (from excluded edge 'res')")
	}
	if !r.linkLocals["fe80::7"] || !r.linkLocals["fe80::8"] {
		t.Errorf("link-locals fe80::7/::8 should be reserved (from excluded edge 'res')")
	}
}

// TestBuildReservedFromExcludedEdges_PartialPinIgnored confirms a single-ended (partial) pin is
// NOT reserved — it is treated as "unpinned" exactly as the pre-allocation pass does, so a
// half-written pin never reserves a resource and forces spurious gap-fill churn.
func TestBuildReservedFromExcludedEdges_PartialPinIgnored(t *testing.T) {
	topo := &model.Topology{
		Domains: []model.Domain{{ID: "domain-1", Name: "net", CIDR: "10.60.0.0/24"}},
		Nodes: []model.Node{
			{ID: "n1", Role: "router", DomainID: "domain-1"},
			{ID: "n2", Role: "router", DomainID: "domain-1"},
		},
		Edges: []model.Edge{
			{ID: "partial", FromNodeID: "n1", ToNodeID: "n2", IsEnabled: true,
				PinnedFromTransitIP: "10.10.0.9", // to-end empty -> incomplete
				PinnedFromPort:      51840},      // to-port 0 -> incomplete
		},
	}
	r := BuildReservedFromExcludedEdges(topo, map[string]bool{})
	if r.transitIPs["10.10.0.0/24"]["10.10.0.9"] {
		t.Errorf("partial transit pin 10.10.0.9 must not be reserved")
	}
	if r.ports["n1"][51840] {
		t.Errorf("partial port pin 51840 must not be reserved")
	}
}
