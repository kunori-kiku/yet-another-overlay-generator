package validator

import (
	"fmt"
	"net"
	"strconv"

	"github.com/kunorikiku/yet-another-overlay-generator/internal/allocconst"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/linkid"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/model"
)

// The link canonical key and link key are provided by internal/linkid (linkid.PinKey /
// linkid.LinkKey); the compiler and validator share the same semantics, avoiding duplicated literals.
//   - linkid.PinKey(a,b): an unordered pair of the two endpoint node IDs (string min|max),
//     direction-independent; an edge, its reverse edge, and all primary class edges of the same node
//     pair all map to the same PinKey.
//   - linkid.LinkKey(edge): a primary class edge equals PinKey (forward/reverse and same-direction
//     redundant primary edges share one key); a backup edge is PinKey + "#" + edge.ID, so each
//     backup becomes its own independent link.

// nodePortPin describes a listen port pinned on a node for a particular link, used to locate
// conflicts during cross-link deduplication.
type nodePortPin struct {
	port   int
	linkID string // linkKey of the link that first declared this (node, port)
	edge   string // edge ID that first declared this (node, port), used in error messages
}

// pinOwner records the first occupant of a pinned address (transit IP or link-local):
// linkID lets the forward/reverse edges of the same link not conflict, and edge names the first
// occupying edge in error messages.
type pinOwner struct {
	linkID string
	edge   string
}

// validateAllocationPins validates the allocation pins on edges (invariant I7; pin validation rules
// in the "Pin validation" table of docs/spec/compiler/allocation-stability.md).
// A violation of any rule is a compile-blocking error (not a warning), and must complete before any
// resource is reserved.
//
// Pins are stored per edge and oriented by "that edge's own from/to": for edge A->B, PinnedFromPort
// is the A-side port and PinnedToPort is the B-side port; the reverse edge B->A carries the mirror
// of the same pair (its PinnedFromPort is the B-side port). Therefore this function:
//   - validates structural rules (partial pin, port out of range, transit out of pool, client-edge
//     port pin) per edge, acting on the edge itself;
//   - validates deduplication rules (the same node port, the same transit IP, or the same link-local
//     occupied by two distinct links) by grouping on linkKey and comparing across links: a primary
//     class's forward/reverse edges share one linkKey and do not count as a conflict, while each
//     backup edge has its own linkKey, so a pin collision with a primary link is faithfully flagged.
func validateAllocationPins(topo *model.Topology, domainMap map[string]*model.Domain, nodeMap map[string]*model.Node, result *ValidationResult) {
	// Deduplication tables: detect the same resource pinned more than once across "distinct links".
	//   - Node port: key is nodeID, value records the first (port, link, edge) that occupied the port.
	//   - transit IP / link-local: key is the canonicalized address string, value records the first
	//     occupant (link, edge).
	portsByNode := make(map[string][]nodePortPin)
	transitByValue := make(map[string]pinOwner)   // canonicalized transit IP -> first occupant
	linkLocalByValue := make(map[string]pinOwner) // canonicalized link-local -> first occupant

	for i := range topo.Edges {
		edge := topo.Edges[i]
		if !edge.IsEnabled {
			continue
		}

		prefix := fmt.Sprintf("edges[%d]", i)
		fromNode := nodeMap[edge.FromNodeID]
		toNode := nodeMap[edge.ToNodeID]
		// Edges with a missing endpoint node are reported by validateEdgeNodeRefs; do not duplicate
		// that here, and skip pin validation.
		if fromNode == nil || toNode == nil {
			continue
		}

		// The link key is used for cross-link deduplication: a primary class's forward/reverse edges
		// and same-direction redundant primary edges share one linkKey (they merge into the same
		// physical link and their pin values are legitimately identical); a backup edge's linkKey
		// carries its own edge.ID, so a pin collision with a primary link is faithfully flagged as a
		// cross-link conflict.
		link := linkid.LinkKey(&edge)

		// --- Rule: a client edge carries pins (client uses a single wg0, no per-peer resources). ---
		// Handled before the other rules: all per-peer pins on a client edge are ignored, so a port
		// pin errors, the remaining pins (transit / link-local) warn "will be ignored", and the rest
		// of the checks that only make sense for per-peer links (pair completeness, range, out of
		// pool, deduplication) are skipped.
		clientTouched := fromNode.Role == "client" || toNode.Role == "client"
		if clientTouched {
			if edge.PinnedFromPort != 0 || edge.PinnedToPort != 0 {
				result.AddError(prefix, CodePinClientPortPin, P{"id", edge.ID})
			}
			if edge.PinnedFromTransitIP != "" || edge.PinnedToTransitIP != "" ||
				edge.PinnedFromLinkLocal != "" || edge.PinnedToLinkLocal != "" {
				result.AddWarning(prefix, CodePinClientAllocationIgnored, P{"id", edge.ID})
			}
			continue
		}

		// --- Rule: partial pin (one end pinned, the other empty). Checked per resource. ---
		validatePinPairCompleteness(prefix, edge, result)

		// --- Rule: port out of range (below allocconst.MinPinnedPort (1024, the manual lower bound after
		// the PR7 relaxation), or > 65535). ---
		validatePinnedPortRange(prefix, "pinned_from_port", edge.PinnedFromPort, fromNode, result)
		validatePinnedPortRange(prefix, "pinned_to_port", edge.PinnedToPort, toNode, result)

		// --- Rule: transit IP out of pool (not within the domain transit CIDR resolved for this edge). ---
		transitCIDR := edgeTransitCIDR(edge, domainMap, nodeMap)
		validatePinnedTransitInCIDR(prefix, "pinned_from_transit_ip", edge.PinnedFromTransitIP, transitCIDR, result)
		validatePinnedTransitInCIDR(prefix, "pinned_to_transit_ip", edge.PinnedToTransitIP, transitCIDR, result)

		// --- Rule: cross-link deduplication. ---
		// Node port: the from-side port belongs to the from node, the to-side port to the to node.
		checkDuplicatePortOnNode(prefix, edge.FromNodeID, edge.PinnedFromPort, link, edge.ID, portsByNode, result)
		checkDuplicatePortOnNode(prefix, edge.ToNodeID, edge.PinnedToPort, link, edge.ID, portsByNode, result)

		// transit IP and link-local: deduplicated across links by canonicalized address value.
		checkDuplicateTransitIP(prefix, edge.PinnedFromTransitIP, link, edge.ID, transitByValue, result)
		checkDuplicateTransitIP(prefix, edge.PinnedToTransitIP, link, edge.ID, transitByValue, result)
		checkDuplicateLinkLocal(prefix, edge.PinnedFromLinkLocal, link, edge.ID, linkLocalByValue, result)
		checkDuplicateLinkLocal(prefix, edge.PinnedToLinkLocal, link, edge.ID, linkLocalByValue, result)
	}
}

// validatePinPairCompleteness validates "paired pins": for each resource, having one end pinned and
// the other empty is illegal. Pins must appear as complete pairs, otherwise the compiler cannot
// construct both-end configs for a link.
func validatePinPairCompleteness(prefix string, edge model.Edge, result *ValidationResult) {
	if (edge.PinnedFromPort != 0) != (edge.PinnedToPort != 0) {
		result.AddError(prefix, CodePinPortIncomplete, P{"id", edge.ID})
	}
	if (edge.PinnedFromTransitIP != "") != (edge.PinnedToTransitIP != "") {
		result.AddError(prefix, CodePinTransitIPIncomplete, P{"id", edge.ID})
	}
	if (edge.PinnedFromLinkLocal != "") != (edge.PinnedToLinkLocal != "") {
		result.AddError(prefix, CodePinLinkLocalIncomplete, P{"id", edge.ID})
	}
}

// validatePinnedPortRange validates that a single pinned port falls within the legal range:
// it must be >= allocconst.MinPinnedPort (1024, the manual lower bound after the PR7 relaxation) and <= 65535.
// A port of 0 means unpinned and is skipped (pair completeness is handled by validatePinPairCompleteness).
func validatePinnedPortRange(prefix, field string, port int, node *model.Node, result *ValidationResult) {
	if port == 0 {
		return
	}
	if port < allocconst.MinPinnedPort || port > 65535 {
		result.AddError(prefix+"."+field, CodePinPortOutOfRange, P{"node", node.Name}, P{"port", strconv.Itoa(port)}, P{"base", strconv.Itoa(allocconst.MinPinnedPort)})
	}
}

// validatePinnedTransitInCIDR validates that a single pinned transit IP falls within the transit
// pool resolved for that edge. An empty string means unpinned and is skipped. An unparseable address
// also errors (a stale or mistyped pin).
func validatePinnedTransitInCIDR(prefix, field, value, transitCIDR string, result *ValidationResult) {
	if value == "" {
		return
	}
	ip := net.ParseIP(value)
	if ip == nil {
		result.AddError(prefix+"."+field, CodePinTransitIPInvalid, P{"cidr", fmt.Sprintf("%q", value)})
		return
	}
	_, cidrNet, err := net.ParseCIDR(transitCIDR)
	if err != nil {
		// An illegal transit CIDR itself is reported by schema/compiler; do not duplicate the
		// out-of-pool judgment here.
		return
	}
	if !cidrNet.Contains(ip) {
		result.AddError(prefix+"."+field, CodePinTransitIPOutOfCIDR, P{"cidr", value}, P{"prefix", transitCIDR})
	}
}

// checkDuplicatePortOnNode detects a listen port pinned more than once on the same node across
// "distinct links". The forward/reverse edges of the same link (same linkKey) carry the mirrored
// same port and are not considered a conflict.
func checkDuplicatePortOnNode(prefix, nodeID string, port int, link, edgeID string, portsByNode map[string][]nodePortPin, result *ValidationResult) {
	if port == 0 {
		return
	}
	for _, existing := range portsByNode[nodeID] {
		if existing.port != port {
			continue
		}
		if existing.linkID == link {
			// Same link (forward/reverse edges), not a cross-link conflict.
			return
		}
		result.AddError(prefix, CodePinPortDuplicateCrossLink, P{"port", strconv.Itoa(port)}, P{"other", existing.edge}, P{"id", edgeID})
		return
	}
	portsByNode[nodeID] = append(portsByNode[nodeID], nodePortPin{port: port, linkID: link, edge: edgeID})
}

// checkDuplicateTransitIP detects a transit IP pinned more than once across "distinct links".
// Addresses are compared by their parsed canonical form, so that "10.10.0.1" and equivalent spellings
// cannot escape deduplication. The forward/reverse edges of the same link (same linkKey) carry the
// mirrored same address and are not considered a conflict.
func checkDuplicateTransitIP(prefix, value, link, edgeID string, transitByValue map[string]pinOwner, result *ValidationResult) {
	if value == "" {
		return
	}
	key := canonicalIP(value)
	if owner, exists := transitByValue[key]; exists {
		if owner.linkID == link {
			return
		}
		result.AddError(prefix, CodePinTransitIPDuplicateCrossLink, P{"cidr", value}, P{"other", owner.edge}, P{"id", edgeID})
		return
	}
	transitByValue[key] = pinOwner{linkID: link, edge: edgeID}
}

// checkDuplicateLinkLocal detects an IPv6 link-local address pinned more than once across "distinct
// links". The forward/reverse edges of the same link (same linkKey) carry the mirrored same address
// and are not considered a conflict.
func checkDuplicateLinkLocal(prefix, value, link, edgeID string, linkLocalByValue map[string]pinOwner, result *ValidationResult) {
	if value == "" {
		return
	}
	key := canonicalIP(value)
	if owner, exists := linkLocalByValue[key]; exists {
		if owner.linkID == link {
			return
		}
		result.AddError(prefix, CodePinLinkLocalDuplicateCrossLink, P{"cidr", value}, P{"other", owner.edge}, P{"id", edgeID})
		return
	}
	linkLocalByValue[key] = pinOwner{linkID: link, edge: edgeID}
}

// canonicalIP normalizes an address string into a comparable canonical form; when unparseable it
// returns the value unchanged, letting deduplication degrade to string equality (the validity of an
// unparseable address is reported by other rules).
func canonicalIP(value string) string {
	if ip := net.ParseIP(value); ip != nil {
		return ip.String()
	}
	return value
}
