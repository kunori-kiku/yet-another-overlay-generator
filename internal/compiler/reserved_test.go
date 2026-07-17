package compiler

import (
	"testing"

	"github.com/kunorikiku/yet-another-overlay-generator/internal/model"
)

// TestBuildReservedFromExcludedEdges_SkipRules locks the reservation builder's exclusion rules:
// included and disabled edges contribute no reservation, while enabled excluded edges reserve
// every live allocation. A client link contributes its non-client-side port plus its complete
// transit/link-local pairs. (Same package so the test reads the unexported sets directly.)
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
			// excluded + client link -> valid router-side port and address pairs are RESERVED
			{ID: "cli", FromNodeID: "n3", ToNodeID: "n2", IsEnabled: true,
				PinnedToPort:        51832,
				PinnedFromTransitIP: "10.10.0.5", PinnedToTransitIP: "10.10.0.6",
				PinnedFromLinkLocal: "fe80::5", PinnedToLinkLocal: "fe80::6"},
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

	for _, ip := range []string{"10.10.0.5", "10.10.0.6", "10.10.0.7", "10.10.0.8"} {
		if !transitReserved(ip) {
			t.Errorf("transit %s should be reserved (from an enabled excluded edge)", ip)
		}
	}
	for _, ip := range []string{"10.10.0.1", "10.10.0.2", "10.10.0.3", "10.10.0.4"} {
		if transitReserved(ip) {
			t.Errorf("transit %s should NOT be reserved (included / disabled edge)", ip)
		}
	}
	if !r.ports["n1"][51830] || !r.ports["n2"][51831] {
		t.Errorf("ports 51830@n1 / 51831@n2 should be reserved (from excluded edge 'res')")
	}
	if !r.ports["n2"][51832] {
		t.Errorf("router-side client-link port 51832@n2 should be reserved")
	}
	for _, ll := range []string{"fe80::5", "fe80::6", "fe80::7", "fe80::8"} {
		if !r.linkLocals[ll] {
			t.Errorf("link-local %s should be reserved (from an enabled excluded edge)", ll)
		}
	}
}

// TestBuildReservedFromExcludedEdges_CrossDomainBothPools confirms the superset reservation: an
// excluded cross-domain edge's transit IPs are reserved into BOTH endpoint domains' pools, so a
// reverse-direction edge (whose own from-node domain differs from where the pair's IPs physically
// live) still reserves into the pool the allocation actually occupies.
func TestBuildReservedFromExcludedEdges_CrossDomainBothPools(t *testing.T) {
	topo := &model.Topology{
		Domains: []model.Domain{
			{ID: "d1", Name: "net1", CIDR: "10.60.0.0/24", TransitCIDR: "10.10.0.0/24"},
			{ID: "d2", Name: "net2", CIDR: "10.61.0.0/24", TransitCIDR: "10.20.0.0/24"},
		},
		Nodes: []model.Node{
			{ID: "a", Role: "router", DomainID: "d1"},
			{ID: "b", Role: "router", DomainID: "d2"},
		},
		// Excluded reverse-direction edge b(d2)->a(d1) pinning IPs that physically live in d1's pool
		// (10.10.x). Resolving from the from-node (b => d2 => 10.20.0.0/24) alone would key them to the
		// WRONG pool; the superset reservation must also reserve them under d1's 10.10.0.0/24.
		Edges: []model.Edge{
			{ID: "rev", FromNodeID: "b", ToNodeID: "a", IsEnabled: true,
				PinnedFromTransitIP: "10.10.0.2", PinnedToTransitIP: "10.10.0.1"},
		},
	}
	r := BuildReservedFromExcludedEdges(topo, map[string]bool{})
	for _, ip := range []string{"10.10.0.1", "10.10.0.2"} {
		if !(r.transitIPs["10.10.0.0/24"] != nil && r.transitIPs["10.10.0.0/24"][ip]) {
			t.Errorf("transit %s must be reserved under d1's pool 10.10.0.0/24 (where the IP lives)", ip)
		}
		if !(r.transitIPs["10.20.0.0/24"] != nil && r.transitIPs["10.20.0.0/24"][ip]) {
			t.Errorf("transit %s must also be reserved under d2's pool 10.20.0.0/24 (superset, harmless)", ip)
		}
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
