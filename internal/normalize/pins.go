// Package normalize cleans up a topology's persisted allocation pins so it validates and compiles
// cleanly. HealCollidingPins repairs both historical allocation corruptions that can be identified
// without operator intent:
//   - a client endpoint carrying a per-link listen-port pin (the non-client endpoint port and the
//     full transit/link-local pairs remain valid sticky allocations); and
//   - different enabled links claiming the same port, transit IP, or link-local value.
//
// The allocator prevents new cross-link collisions. This package is the migration boundary that
// cleans records created by older versions or by role changes over already-allocated edges.
package normalize

import (
	"net"
	"sort"

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

// HealCollidingPins first clears only a port pin attached to a client endpoint, including on a
// disabled edge that may later be enabled. A client uses one shared wg0, but its non-client endpoint
// still owns a real per-link interface/listen port and the complete transit/link-local pair is
// rendered there, so those values remain sticky. It then strips all six pins plus CompiledPort from
// any edge whose valid pinned port / transit IP / link-local collides with another ENABLED edge of a
// DIFFERENT link identity — exactly the
// conflict the semantic validator reports as "occupied by two different links". A colliding edge
// re-allocates fresh on the next compile. The function returns whether anything changed and mutates
// topo.Edges in place.
//
// Which colliding edge keeps its pin — the discriminator (C2, plan-8 Phase 6.3).
// model.Edge has no age/timestamp field (topology.go carries only IsEnabled + slice order), so
// "keep the longer-lived (historical incumbent) edge" is UNCODEABLE in the general case, and array
// order MUST NOT decide either — a re-enabled edge that predates the incumbent sits EARLIER in the
// slice, so a naive "first claimant in slice order wins" would wrongly keep the stale re-enabled
// pin and strip the live incumbent (the re-enable corruption: disable A-B, add A-C into the freed
// slot, re-enable A-B; A-B's stale pin now collides with A-C's legitimate one).
//
// So instead of (uncodeable) age, the discriminator is purely structural: of the edges contesting a
// slot, keep the one the compiler's reserve-first allocator would put there from scratch. Pass-1
// (peers.go) assigns pins by reserve-first + linkKey-SORTED gap-fill — within a pool the lowest free
// slot goes to the SMALLEST linkKey first — so we claim in linkKey-sorted order (NOT slice order):
// the first edge (in that order) to claim a slot keeps it, every later different-link edge that
// needs an already-claimed value is stripped as a whole. The kept edge is therefore the
// SMALLER-linkKey (reserve-first) owner of each contested slot. Pass-1's reserve-first gap-fill then
// re-allocates the stripped edge into the next free slot, byte-stable for everything else.
//
// Relationship to the historical incumbent — and its limit.
// In the common re-enable case (the one C2 was reported for) the smaller-linkKey owner IS the
// historical incumbent: the incumbent was the only enabled link when it was first compiled, so
// reserve-first put it into that slot precisely because it was the smaller linkKey of whatever
// contests the slot today. There, "keep smaller-linkKey" == "keep the incumbent", and the heal
// preserves the live deployment. But this equivalence is NOT guaranteed: when two different-link
// edges carry IDENTICAL pins at the same natural reserve-first slot, the historical owner is
// genuinely unrecoverable (no timestamp on model.Edge — plan-8 ruled out adding one as uncodeable),
// and if the incumbent happens to hold the LARGER linkKey the heal keeps the smaller-linkKey edge
// and strips the incumbent (which then re-allocates a fresh slot on the next compile). This
// symmetric ambiguity is an accepted limit, not a bug: the heal does NOT promise to always preserve
// the incumbent. What it DOES guarantee is independent of which edge was deployed first — for any
// colliding set the result is ALWAYS clean (collision-free), DETERMINISTIC (the linkKey sort has no
// ties across distinct links, so a given topology always heals to the same outcome), and a stable
// FIXED POINT (recompiling the healed topology does not churn the kept edges, because the kept edge
// is exactly the one reserve-first allocation reproduces).
//
// It mirrors the validator's dedup precisely:
//   - disabled edges are skipped (neither checked nor claimed);
//   - an ordinary link claims a complete port pair, while a client link claims its valid
//     non-client-side port individually;
//   - link identity is linkid.LinkKey (primary class folds A->B / B->A and same-pair primaries into
//     one link, so their legitimately-mirrored equal values never count as a collision; each backup
//     is its own link);
//   - claims are processed in linkKey-sorted order (mirroring the allocator's gap-fill priority);
//     the first edge (in that order) to claim a value keeps it; a later, different-link edge that
//     needs any already-claimed value is the colliding one and is stripped as a whole (an allocation
//     is a unit — a half-kept pin set would still fail pair-completeness).
//
// Two-phase per edge: first test ALL of the edge's resources against the claim tables, then either
// claim them all (no collision) or strip the edge and claim nothing (collision) — so a partially
// colliding edge never leaves a stale half-claim behind.
//
// Scope: transit and link-local claims, and ordinary-link port claims, participate only as COMPLETE
// pairs. The non-client-side port on a client link is the deliberate one-sided exception. Any other
// incomplete pin remains a distinct corruption for the validator to report.
func HealCollidingPins(topo *model.Topology) bool {
	if topo == nil {
		return false
	}
	roleByNode := make(map[string]string, len(topo.Nodes))
	for i := range topo.Nodes {
		roleByNode[topo.Nodes[i].ID] = topo.Nodes[i].Role
	}

	// Clear only the endpoint-local port that became meaningless when its node became a client. The
	// valid non-client-side port, complete address pairs, and CompiledPort are preserved.
	changed := false
	for i := range topo.Edges {
		e := &topo.Edges[i]
		if roleByNode[e.FromNodeID] == "client" && e.PinnedFromPort != 0 {
			e.PinnedFromPort = 0
			changed = true
		}
		if roleByNode[e.ToNodeID] == "client" && e.PinnedToPort != 0 {
			e.PinnedToPort = 0
			changed = true
		}
	}

	type portKey struct {
		node string
		port int
	}
	portOwner := make(map[portKey]string)   // (node,port) -> linkKey
	transitOwner := make(map[string]string) // canonical IP -> linkKey
	llOwner := make(map[string]string)      // canonical IP -> linkKey

	// Process edges in reserve-first (linkKey-SORTED) order, the same priority the allocator's
	// Pass-1 gap-fill uses, so the kept claimant of a slot is exactly the edge whose pin reserve-
	// first allocation reproduces (the smaller-linkKey owner — the historical incumbent in the common
	// re-enable case, see the discriminator note above), not whichever edge happens to come first in
	// slice order. Ties (the forward/reverse edges of one link share a linkKey, and slice order among
	// them is irrelevant because same-link claims never collide) are broken by original slice index to
	// keep the walk deterministic. We sort indices, not edges, so the in-place mutation still targets
	// the original topo.Edges[i].
	order := make([]int, 0, len(topo.Edges))
	for i := range topo.Edges {
		e := &topo.Edges[i]
		if !e.IsEnabled {
			continue
		}
		order = append(order, i)
	}
	sort.SliceStable(order, func(a, b int) bool {
		la := linkid.LinkKey(&topo.Edges[order[a]])
		lb := linkid.LinkKey(&topo.Edges[order[b]])
		if la != lb {
			return la < lb
		}
		return order[a] < order[b]
	})

	for _, i := range order {
		e := &topo.Edges[i]
		link := linkid.LinkKey(e)

		// claim describes one resource this edge would occupy.
		type claim struct {
			kind  int // 0=port, 1=transit, 2=link-local
			pk    portKey
			value string
		}
		var claims []claim
		fromClient := roleByNode[e.FromNodeID] == "client"
		toClient := roleByNode[e.ToNodeID] == "client"
		switch {
		case fromClient && !toClient && e.PinnedToPort > 0:
			claims = append(claims, claim{kind: 0, pk: portKey{e.ToNodeID, e.PinnedToPort}})
		case toClient && !fromClient && e.PinnedFromPort > 0:
			claims = append(claims, claim{kind: 0, pk: portKey{e.FromNodeID, e.PinnedFromPort}})
		case !fromClient && !toClient && e.PinnedFromPort > 0 && e.PinnedToPort > 0:
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

// stripAllocationPins clears only the six persisted allocation pins.
func stripAllocationPins(e *model.Edge) {
	e.PinnedFromPort = 0
	e.PinnedToPort = 0
	e.PinnedFromTransitIP = ""
	e.PinnedToTransitIP = ""
	e.PinnedFromLinkLocal = ""
	e.PinnedToLinkLocal = ""
}

// stripPins clears the six allocation pins plus the read-only CompiledPort, returning the edge to an
// unpinned state so the next compile re-derives its allocation.
func stripPins(e *model.Edge) {
	e.CompiledPort = 0
	stripAllocationPins(e)
}
