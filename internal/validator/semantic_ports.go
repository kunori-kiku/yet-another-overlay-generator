package validator

import (
	"fmt"
	"strconv"

	"github.com/kunorikiku/yet-another-overlay-generator/internal/allocconst"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/linkid"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/model"
)

// defaultListenPort is the single base port for per-peer interface allocation; it must match
// peers.go's lowestFreePort base (51820). per-node listen_port has been removed -- it is meaningless
// under the per-peer model.
const defaultListenPort = allocconst.WGListenPortBase

// effectivePortRange describes the listen-port range a node actually occupies under the per-peer
// interface model.
//
//	[base, base+count-1]
//
// base is uniformly defaultListenPort (51820; per-node listen_port has been removed), and count is
// the number of deduplicated links in which the node participates as a "non-client endpoint" (under
// parallel links, a node pair's primary class folds into one link while each backup is its own
// link) -- which is exactly the number of WireGuard interfaces the compiler allocates for it.
type effectivePortRange struct {
	nodeIndex int    // node's index in topo.Nodes, used to locate the error field
	nodeName  string // node name, used in error messages
	base      int    // base listen port
	count     int    // number of interfaces the node occupies (= number of deduplicated links)
}

// high returns the highest listen port the node occupies (base + count - 1).
func (r effectivePortRange) high() int {
	return r.base + r.count - 1
}

// validateEffectivePortRanges validates each node's "effective listen-port range" under the
// per-peer interface model (D11).
//
// The compiler allocates one dedicated WireGuard interface per non-client endpoint of each link,
// with listen ports incrementing as base+offset from the node base port (see the nodePortOffset
// logic in peers.go Pass 1). Interfaces are counted by "link" rather than by "node pair",
// consistent with the compiler's unify rule (parallel links):
//   - only enabled edges whose both endpoint nodes exist are counted;
//   - deduplicated by linkid.LinkKey -- a node pair's primary class (Role != backup) folds into one
//     link, while each backup edge becomes its own independent link;
//   - so a node pair with 1 primary link + 2 backups contributes 3 interfaces to each endpoint;
//   - each link adds +1 to each of its "non-client" endpoints.
//
// After computing each node's occupied range [base, base+count-1], it errors when the range's
// highest port exceeds 65535 (D11: an out-of-range base+offset would be rendered verbatim into the
// WireGuard config).
//
// Note: the base port is uniformly 51820 (per-node listen_port has been removed), so the
// "co-located node range overlap" rule has been deleted -- under a uniform base, any two co-located
// nodes each with >=1 interface necessarily overlap, and that rule would wrongly fail every
// "multiple nodes on one host" deployment.
func validateEffectivePortRanges(topo *model.Topology, result *ValidationResult) {
	// Node indices (consistent with peers.go: looked up by ID).
	nodeMap := make(map[string]*model.Node)
	nodeIndex := make(map[string]int)
	for i := range topo.Nodes {
		nodeMap[topo.Nodes[i].ID] = &topo.Nodes[i]
		nodeIndex[topo.Nodes[i].ID] = i
	}

	// Mirror peers.go Pass 1's unify grouping: deduplicate by linkKey and accumulate interface
	// counts for each non-client endpoint.
	seenLinks := make(map[string]bool)
	interfaceCount := make(map[string]int) // nodeID -> interface count (number of deduplicated links)

	for i := range topo.Edges {
		edge := &topo.Edges[i]
		if !edge.IsEnabled {
			continue
		}

		fromNode := nodeMap[edge.FromNodeID]
		toNode := nodeMap[edge.ToNodeID]
		if fromNode == nil || toNode == nil {
			continue
		}

		// Link key: a primary class's forward/reverse edges and same-direction redundant primary
		// edges share one linkKey (folded into a single link); each backup edge carries its own
		// edge.ID and becomes an independent link.
		lk := linkid.LinkKey(edge)
		if seenLinks[lk] {
			continue
		}
		seenLinks[lk] = true

		// Client nodes use a single wg0 and do not participate in per-peer port allocation
		// (consistent with peers.go's isFromClient / isToClient guards).
		if fromNode.Role != "client" {
			interfaceCount[fromNode.ID]++
		}
		if toNode.Role != "client" {
			interfaceCount[toNode.ID]++
		}
	}

	// Validate the effective port range for nodes that occupy at least one interface.
	for _, node := range topo.Nodes {
		count := interfaceCount[node.ID]
		if count == 0 {
			// No per-peer interfaces (no enabled edges, or a client node): no effective range to validate.
			continue
		}
		// The base port is uniformly 51820 (per-node listen_port has been removed).
		r := effectivePortRange{
			nodeIndex: nodeIndex[node.ID],
			nodeName:  node.Name,
			base:      defaultListenPort,
			count:     count,
		}

		// Rule: the range's highest port overflows (base+count-1 > 65535 would be rendered verbatim
		// into the WireGuard config).
		if r.high() > 65535 {
			result.AddError(fmt.Sprintf("nodes[%d]", r.nodeIndex), CodeNodeEffectivePortRangeOverflow, P{"node", r.nodeName}, P{"low", strconv.Itoa(r.base)}, P{"high", strconv.Itoa(r.high())}, P{"base", strconv.Itoa(r.base)}, P{"count", strconv.Itoa(r.count)})
		}
	}
}
