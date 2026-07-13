package compiler

import (
	"github.com/kunorikiku/yet-another-overlay-generator/internal/model"
)

// ReservedAllocations holds the allocation resources occupied by "edges outside
// the subgraph" that a "subgraph compile" must avoid.
//
// Background (cross-subgraph collision root cause): when the controller enrolls
// incrementally it only compiles the "already-enrolled subgraph"; edges whose
// remote end is not yet enrolled are dropped by enrolledSubgraph. The compiler
// cannot see them, so gap-fill re-allocates from .1 and collides with the pins
// those dropped edges still hold (persisted in the full topology). Reserving the
// pins of the edges in the full topology that are "not in this subgraph" makes the
// subgraph's gap-fill skip them, so cross-subgraph collisions never recur (zero
// drift for existing healthy allocations, because the reserved values are exactly
// what other edges already occupy and should be avoided anyway).
//
// Note: it only "reserves" (affecting gap-fill's avoidance set); it does not alter
// the sticky pins of the subgraph's own edges — cleanup of existing colliding data
// is the job of the normalize layer's heal, with separated responsibilities
// (reserve = prevent new, heal = clean existing).
type ReservedAllocations struct {
	ports      map[string]map[int]bool    // nodeID -> set of ports
	transitIPs map[string]map[string]bool // resolved CIDR -> set of IP strings
	linkLocals map[string]bool            // set of link-local strings
}

// BuildReservedFromExcludedEdges scans the full topology and, for every enabled,
// non-client-touching edge with complete paired pins whose "ID is not in
// includedEdgeIDs", reserves its port / transit IP / link-local. These are exactly
// the edges dropped during subgraph compilation that still hold pins in storage —
// reserving them keeps the subgraph's new allocations from colliding with them. The
// CIDR is resolved via transitCIDRForNode (same source as link construction).
//
//   - Skip disabled edges: both the validator and the allocator ignore them, their
//     pins do not form a collision, and reserving them only adds needless avoidance.
//   - Skip client-touching edges: a client uses a single wg0 with no per-peer
//     resources, so its port pin is already wrong and its transit/LL are ignored
//     (consistent with client handling in the pre-allocation phase).
//   - Only reserve "complete paired" pins (a single-sided value is treated as
//     unpinned, consistent with the pre-allocation phase).
func BuildReservedFromExcludedEdges(full *model.Topology, includedEdgeIDs map[string]bool) *ReservedAllocations {
	r := &ReservedAllocations{
		ports:      make(map[string]map[int]bool),
		transitIPs: make(map[string]map[string]bool),
		linkLocals: make(map[string]bool),
	}
	if full == nil {
		return r
	}
	nodeMap := make(map[string]*model.Node, len(full.Nodes))
	for i := range full.Nodes {
		nodeMap[full.Nodes[i].ID] = &full.Nodes[i]
	}
	domainMap := make(map[string]*model.Domain, len(full.Domains))
	for i := range full.Domains {
		domainMap[full.Domains[i].ID] = &full.Domains[i]
	}
	for i := range full.Edges {
		e := &full.Edges[i]
		if includedEdgeIDs[e.ID] || !e.IsEnabled {
			continue
		}
		from := nodeMap[e.FromNodeID]
		to := nodeMap[e.ToNodeID]
		if from == nil || to == nil {
			continue
		}
		if from.Role == "client" || to.Role == "client" {
			continue
		}
		if e.PinnedFromPort > 0 && e.PinnedToPort > 0 {
			r.reservePort(e.FromNodeID, e.PinnedFromPort)
			r.reservePort(e.ToNodeID, e.PinnedToPort)
		}
		if e.PinnedFromTransitIP != "" && e.PinnedToTransitIP != "" {
			// Reserve into BOTH endpoint domains' pools. The transit pair physically lives in the
			// LINK-DRIVING (first enabled primary) edge's from-node pool, which — for a cross-domain
			// pair whose excluded representative is the REVERSE-direction edge — is the to-node's
			// domain, not this edge's from-node domain. Resolving from a single side could key the
			// reservation to the wrong pool and miss the collision. A transit IP only belongs to one
			// pool's range, so reserving it in the other endpoint's pool is a harmless no-op for that
			// pool's gap-fill; reserving in both guarantees it is avoided wherever it actually lives.
			cidrFrom := transitCIDRForNode(from, domainMap)
			cidrTo := transitCIDRForNode(to, domainMap)
			r.reserveTransit(cidrFrom, e.PinnedFromTransitIP)
			r.reserveTransit(cidrFrom, e.PinnedToTransitIP)
			if cidrTo != cidrFrom {
				r.reserveTransit(cidrTo, e.PinnedFromTransitIP)
				r.reserveTransit(cidrTo, e.PinnedToTransitIP)
			}
		}
		if e.PinnedFromLinkLocal != "" && e.PinnedToLinkLocal != "" {
			r.linkLocals[e.PinnedFromLinkLocal] = true
			r.linkLocals[e.PinnedToLinkLocal] = true
		}
	}
	return r
}

func (r *ReservedAllocations) reservePort(nodeID string, port int) {
	if r.ports[nodeID] == nil {
		r.ports[nodeID] = make(map[int]bool)
	}
	r.ports[nodeID][port] = true
}

func (r *ReservedAllocations) reserveTransit(cidr, ip string) {
	if r.transitIPs[cidr] == nil {
		r.transitIPs[cidr] = make(map[string]bool)
	}
	r.transitIPs[cidr][ip] = true
}
