// Package normalize cleans up a topology's persisted allocation pins so it validates and compiles
// cleanly. Its one job today is HealCollidingPins: the inverse of the semantic validator's
// cross-link pin dedup, used to repair the "pin occupied by two different links" corruption that
// older incremental-enrollment compiles could persist (a fresh subgraph re-allocating a transit IP
// / port / link-local that an out-of-subgraph edge still pinned). The allocator's reservation pass
// (compiler.BuildReservedFromExcludedEdges) PREVENTS new instances; this CLEANS existing ones.
package normalize

import (
	"net"

	"github.com/kunorikiku/yet-another-overlay-generator/internal/linkid"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/model"
)

// canonicalIP mirrors the semantic validator's canonicalIP so the heal's notion of "same address"
// matches the validator's exactly — what the heal strips is precisely what the validator would flag.
func canonicalIP(value string) string {
	if ip := net.ParseIP(value); ip != nil {
		return ip.String()
	}
	return value
}

// isClientTouched reports whether either endpoint of the edge is a client node. Mirrors the
// validator: client edges use a single wg0 with no per-peer resources, so their pins are not part
// of cross-link dedup and must never be stripped (or claimed) by the heal.
func isClientTouched(e *model.Edge, roleByNode map[string]string) bool {
	return roleByNode[e.FromNodeID] == "client" || roleByNode[e.ToNodeID] == "client"
}

// HealCollidingPins strips the allocation pins (six pinned_* fields + CompiledPort) from any edge
// whose pinned port / transit IP / link-local collides with an EARLIER edge of a DIFFERENT link
// identity — exactly the conflict the semantic validator reports as "occupied by two different
// links". The stripped edge re-allocates fresh on the next compile (the allocator's reserve-then-
// gap-fill keeps every other edge's value stable, so only the colliding edge moves). It returns
// whether anything changed, and mutates topo.Edges in place.
//
// It mirrors the validator's dedup precisely:
//   - disabled edges and client-touched edges are skipped (neither checked nor claimed);
//   - link identity is linkid.LinkKey (primary class folds A->B / B->A and same-pair primaries into
//     one link, so their legitimately-mirrored equal values never count as a collision; each backup
//     is its own link);
//   - the FIRST edge (in slice order) to claim a value keeps it; a later, different-link edge that
//     needs any already-claimed value is the colliding one and is stripped as a whole (an allocation
//     is a unit — a half-kept pin set would still fail pair-completeness).
//
// Two-phase per edge: first test ALL of the edge's resources against the claim tables, then either
// claim them all (no collision) or strip the edge and claim nothing (collision) — so a partially
// colliding edge never leaves a stale half-claim behind.
func HealCollidingPins(topo *model.Topology) bool {
	if topo == nil {
		return false
	}
	roleByNode := make(map[string]string, len(topo.Nodes))
	for i := range topo.Nodes {
		roleByNode[topo.Nodes[i].ID] = topo.Nodes[i].Role
	}

	type portKey struct {
		node string
		port int
	}
	portOwner := make(map[portKey]string)   // (node,port) -> linkKey
	transitOwner := make(map[string]string) // canonical IP -> linkKey
	llOwner := make(map[string]string)      // canonical IP -> linkKey

	changed := false
	for i := range topo.Edges {
		e := &topo.Edges[i]
		if !e.IsEnabled || isClientTouched(e, roleByNode) {
			continue
		}
		link := linkid.LinkKey(e)

		// claim describes one resource this edge would occupy.
		type claim struct {
			kind  int // 0=port, 1=transit, 2=link-local
			pk    portKey
			value string
		}
		var claims []claim
		if e.PinnedFromPort > 0 && e.PinnedToPort > 0 {
			claims = append(claims,
				claim{kind: 0, pk: portKey{e.FromNodeID, e.PinnedFromPort}},
				claim{kind: 0, pk: portKey{e.ToNodeID, e.PinnedToPort}})
		}
		if e.PinnedFromTransitIP != "" && e.PinnedToTransitIP != "" {
			claims = append(claims,
				claim{kind: 1, value: canonicalIP(e.PinnedFromTransitIP)},
				claim{kind: 1, value: canonicalIP(e.PinnedToTransitIP)})
		}
		if e.PinnedFromLinkLocal != "" && e.PinnedToLinkLocal != "" {
			claims = append(claims,
				claim{kind: 2, value: canonicalIP(e.PinnedFromLinkLocal)},
				claim{kind: 2, value: canonicalIP(e.PinnedToLinkLocal)})
		}

		// Phase 1: does any resource already belong to a DIFFERENT link?
		collides := false
		for _, c := range claims {
			var owner string
			var seen bool
			switch c.kind {
			case 0:
				owner, seen = portOwner[c.pk]
			case 1:
				owner, seen = transitOwner[c.value]
			case 2:
				owner, seen = llOwner[c.value]
			}
			if seen && owner != link {
				collides = true
				break
			}
		}

		if collides {
			stripPins(e)
			changed = true
			continue // claim nothing — the stripped edge holds no resources
		}

		// Phase 2: no collision — claim every resource for this link.
		for _, c := range claims {
			switch c.kind {
			case 0:
				portOwner[c.pk] = link
			case 1:
				transitOwner[c.value] = link
			case 2:
				llOwner[c.value] = link
			}
		}
	}
	return changed
}

// stripPins clears the six allocation pins plus the read-only CompiledPort, returning the edge to an
// unpinned state so the next compile re-derives its allocation.
func stripPins(e *model.Edge) {
	e.CompiledPort = 0
	e.PinnedFromPort = 0
	e.PinnedToPort = 0
	e.PinnedFromTransitIP = ""
	e.PinnedToTransitIP = ""
	e.PinnedFromLinkLocal = ""
	e.PinnedToLinkLocal = ""
}
