// Package normalize cleans up a topology's persisted allocation pins so it validates and compiles
// cleanly. Its one job today is HealCollidingPins: the inverse of the semantic validator's
// cross-link pin dedup, used to repair the "pin occupied by two different links" corruption that
// older incremental-enrollment compiles could persist (a fresh subgraph re-allocating a transit IP
// / port / link-local that an out-of-subgraph edge still pinned). The allocator's reservation pass
// (compiler.BuildReservedFromExcludedEdges) PREVENTS new instances; this CLEANS existing ones.
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

// isClientTouched reports whether either endpoint of the edge is a client node. Mirrors the
// validator: client edges use a single wg0 with no per-peer resources, so their pins are not part
// of cross-link dedup and must never be stripped (or claimed) by the heal.
func isClientTouched(e *model.Edge, roleByNode map[string]string) bool {
	return roleByNode[e.FromNodeID] == "client" || roleByNode[e.ToNodeID] == "client"
}

// HealCollidingPins strips the allocation pins (six pinned_* fields + CompiledPort) from any edge
// whose pinned port / transit IP / link-local collides with another ENABLED edge of a DIFFERENT
// link identity — exactly the conflict the semantic validator reports as "occupied by two different
// links". The stripped edge re-allocates fresh on the next compile (the allocator's reserve-then-
// gap-fill keeps every other edge's value stable, so only the colliding edge moves). It returns
// whether anything changed, and mutates topo.Edges in place.
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
//   - disabled edges and client-touched edges are skipped (neither checked nor claimed);
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
// Scope: only COMPLETE pin pairs participate (both ends present), matching the allocator's
// reservation unit. A single-ended (incomplete) pin is a distinct corruption that the validator
// flags separately (CodePin*Incomplete) and the heal deliberately does not claim to repair.
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
		if !e.IsEnabled || isClientTouched(e, roleByNode) {
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

	changed := false
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
