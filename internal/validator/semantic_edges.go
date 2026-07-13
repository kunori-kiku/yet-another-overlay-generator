package validator

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/kunorikiku/yet-another-overlay-generator/internal/allocconst"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/linkid"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/model"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/naming"
)

// validateRoutePoliciesReserved rejects any non-empty route_policies (D10/D37/D62, Spec E).
// route_policies is declared on both the Go and TS sides, yet no renderer consumes it and there is
// no editor entry point; the compiler merely passes it through unchanged (compiler.go). Per the
// binding decision (Decisions log #2) it is a feature "reserved for a future subject", not a usable
// capability: a topology carrying non-empty route_policies would compile into a dead config that
// does not match user intent yet shows no routing policy taking effect. Semantic validation
// therefore errors here, requiring the array to be empty.
// The LAN-bridge / route-injection use case is served by extra_prefixes and the routing layer, not
// by route_policies.
func validateRoutePoliciesReserved(topo *model.Topology, result *ValidationResult) {
	if len(topo.RoutePolicies) > 0 {
		result.AddError("route_policies", CodeRoutePolicyReserved, P{"count", strconv.Itoa(len(topo.RoutePolicies))})
	}
}

func validateEdgeNodeRefs(topo *model.Topology, nodeMap map[string]*model.Node, result *ValidationResult) {
	for i, edge := range topo.Edges {
		prefix := fmt.Sprintf("edges[%d]", i)
		if edge.FromNodeID != "" {
			if _, ok := nodeMap[edge.FromNodeID]; !ok {
				result.AddError(prefix+".from_node_id", CodeEdgeNodeRefMissing, P{"id", edge.ID}, P{"other", edge.FromNodeID})
			}
		}
		if edge.ToNodeID != "" {
			if _, ok := nodeMap[edge.ToNodeID]; !ok {
				result.AddError(prefix+".to_node_id", CodeEdgeNodeRefMissing, P{"id", edge.ID}, P{"other", edge.ToNodeID})
			}
		}
	}
}

// validateClientEdges validates the edge constraints for client nodes.
func validateClientEdges(topo *model.Topology, nodeMap map[string]*model.Node, result *ValidationResult) {
	// Collect each client's outbound and inbound edge counts.
	clientOutbound := make(map[string]int)        // nodeID -> count of enabled outbound edges
	clientOutboundEdges := make(map[string][]int) // nodeID -> edge indices

	for i, edge := range topo.Edges {
		if !edge.IsEnabled {
			continue
		}

		fromNode := nodeMap[edge.FromNodeID]
		toNode := nodeMap[edge.ToNodeID]

		// Reject inbound edges that target a client.
		if toNode != nil && toNode.Role == "client" {
			result.AddError(fmt.Sprintf("edges[%d]", i), CodeClientInboundRejected, P{"node", toNode.Name})
		}

		// Count client outbound edges.
		if fromNode != nil && fromNode.Role == "client" {
			clientOutbound[fromNode.ID]++
			clientOutboundEdges[fromNode.ID] = append(clientOutboundEdges[fromNode.ID], i)

			// A client's target must be router/relay/gateway (not peer or client).
			if toNode != nil {
				if toNode.Role == "peer" {
					result.AddError(fmt.Sprintf("edges[%d]", i), CodeClientTargetPeer, P{"node", fromNode.Name}, P{"other", toNode.Name})
				}
				if toNode.Role == "client" {
					result.AddError(fmt.Sprintf("edges[%d]", i), CodeClientTargetClient, P{"node", fromNode.Name}, P{"other", toNode.Name})
				}
			}

			// A client edge must have an endpoint_host.
			if edge.EndpointHost == "" {
				result.AddError(fmt.Sprintf("edges[%d].endpoint_host", i), CodeClientEndpointHostRequired, P{"node", fromNode.Name})
			}
		}
	}

	// A client must have exactly one enabled outbound edge.
	for _, node := range topo.Nodes {
		if node.Role != "client" {
			continue
		}
		count := clientOutbound[node.ID]
		if count == 0 {
			result.AddError("topology", CodeClientNoOutboundEdge, P{"node", node.Name})
		} else if count > 1 {
			result.AddError("topology", CodeClientMultipleOutboundEdges, P{"node", node.Name}, P{"count", strconv.Itoa(count)})
		}

		// Warning: the client set fields that are meaningless for it.
		if node.RouterID != "" {
			result.AddWarning(fmt.Sprintf("node.%s.router_id", node.ID), CodeClientRouterIDMeaningless, P{"node", node.Name})
		}
		if len(node.ExtraPrefixes) > 0 {
			result.AddWarning(fmt.Sprintf("node.%s.extra_prefixes", node.ID), CodeClientExtraPrefixesMeaningless, P{"node", node.Name})
		}
	}
}

// mimicLinuxDeployable reports whether a node's platform can deploy mimic (eBPF/kernel features).
// mimic is only available on Linux, and the Linux distributions YAOG currently supports are
// debian / ubuntu. An empty platform is treated as Linux (allowed): consistent with the "empty
// platform skips validation" handling in schema.go validateNodesSchema, avoiding false positives for
// nodes with no platform set.
func mimicLinuxDeployable(node *model.Node) bool {
	if node == nil {
		// A missing node is reported by validateEdgeNodeRefs; do not duplicate that here and allow it.
		return true
	}
	if node.Platform == "" {
		return true
	}
	switch strings.ToLower(node.Platform) {
	case "debian", "ubuntu":
		return true
	default:
		return false
	}
}

// validateMimicTransport validates the platform constraint on transport=="tcp" (mimic) edges (Spec:
// docs/spec/artifacts/mimic.md, docs/spec/data-model/edge.md §TCP transport, docs/spec/compiler/validation.md
// mimic rules).
//
// mimic is an eBPF/kernel feature and can only run on deployable Linux (debian / ubuntu); therefore
// both endpoint nodes of a tcp edge must be deployable Linux, otherwise the compiled config cannot
// be deployed on that endpoint. If either endpoint's platform is unsupported it errors, naming the
// edge and the offending node in the message.
//
// Actual kernel/eBPF availability is an install-time check (see mimic.md), not a compile-time error;
// mimic has no keys, so no key validation is needed. An empty platform is treated as Linux
// (allowed), consistent with how other platform checks handle empty values.
func validateMimicTransport(topo *model.Topology, nodeMap map[string]*model.Node, result *ValidationResult) {
	for i := range topo.Edges {
		edge := &topo.Edges[i]
		if !edge.IsEnabled {
			continue
		}
		if edge.Transport != "tcp" {
			continue
		}

		// mimic (fake-TCP) needs a DIRECT L3/L4 path: an L7 / UDP-accelerator relay (a relay-path edge)
		// terminates + re-originates the connection, so the fake-TCP can't traverse it end to end (the
		// reverse leg RSTs). Advise udp for a relayed edge — a WARNING (deploy is not blocked).
		if edge.Type == "relay-path" {
			result.AddWarning(fmt.Sprintf("edges[%d].transport", i), CodeEdgeMimicRelayPath, P{"id", edge.ID})
		}

		fromNode := nodeMap[edge.FromNodeID]
		toNode := nodeMap[edge.ToNodeID]

		if !mimicLinuxDeployable(fromNode) {
			result.AddError(fmt.Sprintf("edges[%d].transport", i), CodeEdgeMimicPlatformUnsupported, P{"id", edge.ID}, P{"node", fromNode.Name}, P{"platform", fmt.Sprintf("%q", fromNode.Platform)})
		}
		if !mimicLinuxDeployable(toNode) {
			result.AddError(fmt.Sprintf("edges[%d].transport", i), CodeEdgeMimicPlatformUnsupported, P{"id", edge.ID}, P{"node", toNode.Name}, P{"platform", fmt.Sprintf("%q", toNode.Platform)})
		}
	}
}

// edgeTransitCIDR resolves the transit address pool an edge actually uses.
// Consistent with the resolution rule in compiler/peers.go Pass 1: take the transit_cidr of the
// domain the from node belongs to, falling back to the default 10.10.0.0/24 when empty.
func edgeTransitCIDR(edge model.Edge, domainMap map[string]*model.Domain, nodeMap map[string]*model.Node) string {
	fromNode := nodeMap[edge.FromNodeID]
	if fromNode == nil {
		return allocconst.DefaultTransitCIDR
	}
	if domain := domainMap[fromNode.DomainID]; domain != nil && domain.TransitCIDR != "" {
		return domain.TransitCIDR
	}
	return allocconst.DefaultTransitCIDR
}

// validateEdgeEndpointConsistency checks whether an edge's endpoint_host is consistent with the
// target node's public endpoints.
// When an enabled edge sets endpoint_host, the target node also declares at least one public
// endpoint, but none of the target node's public_endpoints[].host equals that endpoint_host, it
// warns -- this usually means the snapshot on the edge went stale after the node endpoint was edited.
// Warning only, not an error: under NAT/port-forwarding or hairpin scenarios, the dial host can
// legitimately differ from the host the node declares for itself.
func validateEdgeEndpointConsistency(topo *model.Topology, nodeMap map[string]*model.Node, result *ValidationResult) {
	for i, edge := range topo.Edges {
		if !edge.IsEnabled || edge.EndpointHost == "" {
			continue
		}

		toNode := nodeMap[edge.ToNodeID]
		if toNode == nil || len(toNode.PublicEndpoints) == 0 {
			continue
		}

		matched := false
		for _, ep := range toNode.PublicEndpoints {
			if ep.Host == edge.EndpointHost {
				matched = true
				break
			}
		}

		if !matched {
			result.AddWarning(fmt.Sprintf("edges[%d].endpoint_host", i), CodeEdgeEndpointNoMatch, P{"id", edge.ID}, P{"other", edge.EndpointHost}, P{"node", toNode.Name})
		}
	}
}

// detectDuplicateEnabledEdges warns when the same node pair (same direction) has multiple primary
// class enabled edges (D71, parallel-links rescope).
// Same-direction redundant primary class edges (Role empty or "primary") are folded by the compiler
// into one link: only the first takes effect, and the endpoint_host/endpoint_port overrides carried
// by later edges are silently ignored, so the operator sees two edges but only one has any effect.
// backup edges (Role == "backup") each become an independent link -- intentional parallel links
// rather than accidental duplicates -- so they never trigger this warning.
// Warning only, not an error: the topology still compiles, but the operator should delete or disable
// the redundant edge -- if redundancy was intended, the redundant edge should be set to role
// "backup" to make it an independent backup link.
func detectDuplicateEnabledEdges(topo *model.Topology, result *ValidationResult) {
	firstEdgeByDirection := make(map[string]string) // "from->to" -> first primary class edge ID
	for i := range topo.Edges {
		edge := &topo.Edges[i]
		if !edge.IsEnabled {
			continue
		}
		// A backup edge is an independent link and does not participate in same-direction dedup warnings.
		if linkid.IsBackup(edge) {
			continue
		}
		direction := edge.FromNodeID + "->" + edge.ToNodeID
		if firstID, exists := firstEdgeByDirection[direction]; exists {
			result.AddWarning(fmt.Sprintf("edges[%d]", i), CodeEdgeDuplicateEnabledSameDirection, P{"id", edge.ID}, P{"other", firstID})
			continue
		}
		firstEdgeByDirection[direction] = edge.ID
	}
}

// babeldWiredDefaultCost is babeld's built-in default rxcost for wired / tunnel interfaces.
// The compiler resolves a link cost that is "not explicitly set and not backup" to 0 (omitting the
// rxcost token and deferring to babeld's default); when comparing equal cost, 0 must be treated as
// this built-in default, otherwise two links both with no cost set would be wrongly judged as
// unequal cost.
const babeldWiredDefaultCost = 96

// effectiveLinkCost exactly mirrors the compiler's link-cost resolution order (contract item 4 /
// babel.md):
//  1. explicit priority/weight mapping takes precedence (D63: priority>0 takes priority, otherwise
//     weight>0 takes weight);
//  2. otherwise a backup link → allocconst.BackupDefaultLinkCost (384);
//  3. otherwise 0 (the compiler omits rxcost and babeld uses its built-in default of 96).
//
// The return value is the raw cost the compiler writes (0 means defer to babeld's default). For
// equal-cost comparison the caller normalizes 0 to 96 via comparableCost. rep is the link's
// representative edge: a unified primary link takes the first primary class edge, a backup link takes
// the backup edge itself.
func effectiveLinkCost(rep *model.Edge) int {
	if rep == nil {
		return 0
	}
	if rep.Priority > 0 {
		return rep.Priority
	}
	if rep.Weight > 0 {
		return rep.Weight
	}
	if linkid.IsBackup(rep) {
		return allocconst.BackupDefaultLinkCost
	}
	return 0
}

// comparableCost normalizes a link cost into a comparable value: 0 (unset, deferred to babeld's
// default) is treated as the built-in default of 96.
func comparableCost(cost int) int {
	if cost == 0 {
		return babeldWiredDefaultCost
	}
	return cost
}

// validateInterfaceNameUniqueness validates invariant N4: all per-peer WireGuard interface names on
// the same node (including primary and backup, toward any peer) must be distinct.
//
// A node may hold multiple interfaces toward the same peer (a primary link + several backups); the
// interface name is derived from the peer name (primary) or the peer name plus a 4-character hash of
// the backup edge ID (backup). Two colliding interface names would let one WireGuard config and one
// Babel interface line overwrite another -- the deterministic answer to a 16-bit hash collision is
// "rename one of them", so it errors here and names both colliding links (naming spec in
// docs/spec/artifacts/naming.md §Edge-aware names).
//
// Interface names are computed the same way the compiler does:
//   - a primary link toward peer R → naming.WgInterfaceName(R.Name);
//   - a backup edge e toward peer R → naming.WgInterfaceNameForEdge(R.Name, e.ID, true).
//
// Client nodes use a single wg0 and do not participate in per-peer interface allocation, so the
// client endpoint side is skipped.
func validateInterfaceNameUniqueness(topo *model.Topology, nodeMap map[string]*model.Node, result *ValidationResult) {
	// nodeID -> (interface name -> description of the link that first occupied that interface name),
	// used to detect name collisions across links.
	ifaceByNode := make(map[string]map[string]string)

	// register records an interface name on a node; if it already exists it errors and names both links.
	register := func(nodeIndex int, nodeID, ifaceName, linkDesc string) {
		if ifaceByNode[nodeID] == nil {
			ifaceByNode[nodeID] = make(map[string]string)
		}
		if first, exists := ifaceByNode[nodeID][ifaceName]; exists {
			node := nodeMap[nodeID]
			nodeName := nodeID
			if node != nil {
				nodeName = node.Name
			}
			result.AddError(fmt.Sprintf("nodes[%d]", nodeIndex), CodeNodeInterfaceNameCollision, P{"node", nodeName}, P{"name", fmt.Sprintf("%q", ifaceName)}, P{"prefix", first}, P{"other", linkDesc})
			return
		}
		ifaceByNode[nodeID][ifaceName] = linkDesc
	}

	// Node-index lookup, used to locate the error field.
	nodeIndex := make(map[string]int)
	for i := range topo.Nodes {
		nodeIndex[topo.Nodes[i].ID] = i
	}

	// Deduplicate links by linkKey: a primary class folds into one link (the first primary class edge
	// is its representative), and each backup edge is an independent link.
	seenLinks := make(map[string]bool)
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
		lk := linkid.LinkKey(edge)
		if seenLinks[lk] {
			continue
		}
		seenLinks[lk] = true

		backup := linkid.IsBackup(edge)

		// The from endpoint's interface faces to (peer = to); the to endpoint's interface faces from
		// (peer = from). The interface name is derived from the peer name; a client endpoint is not
		// allocated a per-peer interface and is skipped.
		if fromNode.Role != "client" {
			var ifaceName string
			if backup {
				ifaceName = naming.WgInterfaceNameForEdge(toNode.Name, edge.ID, true)
			} else {
				ifaceName = naming.WgInterfaceName(toNode.Name)
			}
			register(nodeIndex[fromNode.ID], fromNode.ID, ifaceName, linkDescription(edge, toNode.Name, backup))
		}
		if toNode.Role != "client" {
			var ifaceName string
			if backup {
				ifaceName = naming.WgInterfaceNameForEdge(fromNode.Name, edge.ID, true)
			} else {
				ifaceName = naming.WgInterfaceName(fromNode.Name)
			}
			register(nodeIndex[toNode.ID], toNode.ID, ifaceName, linkDescription(edge, fromNode.Name, backup))
		}
	}
}

// linkDescription builds a readable description of a link for error messages: it names the peer it
// faces, the link class (primary/backup), and the representative edge ID, so the operator can locate
// the link to rename.
// linkDescription builds a LANGUAGE-NEUTRAL locator for a colliding link from DATA only — the
// role enum value (primary|backup), the remote node name, and the representative edge ID for a
// backup — with NO translatable prose. It is interpolated as the {prefix}/{other} params of
// CodeNodeInterfaceNameCollision, so it must render the same in every locale: prose here would
// splice English into the zh message (plan-3.5a review). Role values are kept verbatim across
// locales by the same convention as auto/manual/babel.
func linkDescription(edge *model.Edge, remoteName string, backup bool) string {
	if backup {
		return fmt.Sprintf("backup→%s (%s)", remoteName, edge.ID)
	}
	return fmt.Sprintf("primary→%s", remoteName)
}

// validateSinglePrimaryPerPair validates that a node pair has at most one edge explicitly marked
// role=="primary".
// An empty role and "primary" both belong to the primary class, but explicitly writing two
// "primary" edges is usually an operator misunderstanding (thinking it expresses dual-primary); the
// compiler only folds them into one primary link and silently ignores the rest -- so it errors
// directly and asks for clarification.
// Note: only explicit "primary" is counted, not an empty role (same-direction duplicates with an
// empty role are warned by detectDuplicateEnabledEdges).
func validateSinglePrimaryPerPair(topo *model.Topology, nodeMap map[string]*model.Node, result *ValidationResult) {
	// pinKey -> first explicit primary edge ID.
	firstPrimary := make(map[string]string)
	for i := range topo.Edges {
		edge := &topo.Edges[i]
		if !edge.IsEnabled {
			continue
		}
		if edge.Role != model.EdgeRolePrimary {
			continue
		}
		if nodeMap[edge.FromNodeID] == nil || nodeMap[edge.ToNodeID] == nil {
			continue
		}
		pk := linkid.PinKey(edge.FromNodeID, edge.ToNodeID)
		if firstID, exists := firstPrimary[pk]; exists {
			result.AddError(fmt.Sprintf("edges[%d].role", i), CodeEdgeMultipleExplicitPrimary, P{"id", edge.ID}, P{"other", firstID})
			continue
		}
		firstPrimary[pk] = edge.ID
	}
}

// validateLinkDirection validates the per-edge dial-direction policy (link_direction; semantics in
// docs/spec/data-model/edge.md §Link direction). Every rule is an ERROR, not a warning: a
// single-linked edge that cannot dial is a provably dead link, and a direction that pair-folding
// would silently ignore is the same silently-dropped-config failure class as
// CodeEdgeEndpointPortWithoutHost. Schema has already rejected unrecognized values (there is no
// "reverse" — D11, one spelling), so this function acts only on "forward":
//   - conflict: a primary-class edge folds with every other enabled primary-class edge of its node
//     pair (either direction), so a folded edge's direction would be silently ignored — a
//     direction-bearing edge must be its pair's ONLY enabled primary-class edge. Backup edges are
//     their own links and are exempt from the pair rule.
//   - forward requires endpoint_host: the forward peer only ever dials the edge's endpoint host,
//     and the reverse dial is suppressed, so without a host no side could initiate.
//   - a client-touching edge must not set a direction (a client link's dial semantics are fixed:
//     the client always dials the router).
func validateLinkDirection(topo *model.Topology, nodeMap map[string]*model.Node, result *ValidationResult) {
	// Collect each node pair's enabled primary-class edge IDs (either direction), so a
	// direction-bearing member of a multi-edge pair can name a folding sibling in its error.
	pairEdgeIDs := make(map[string][]string) // pinKey -> enabled primary-class edge IDs, in order
	for i := range topo.Edges {
		edge := &topo.Edges[i]
		if !edge.IsEnabled || linkid.IsBackup(edge) {
			continue
		}
		pk := linkid.PinKey(edge.FromNodeID, edge.ToNodeID)
		pairEdgeIDs[pk] = append(pairEdgeIDs[pk], edge.ID)
	}

	for i := range topo.Edges {
		edge := &topo.Edges[i]
		if !edge.IsEnabled {
			continue
		}
		dir := edge.LinkDirection
		if dir != model.EdgeLinkDirectionForward {
			continue // "", "both", and unrecognized (schema-rejected) values carry no dial rules
		}
		prefix := fmt.Sprintf("edges[%d].link_direction", i)

		fromNode := nodeMap[edge.FromNodeID]
		toNode := nodeMap[edge.ToNodeID]
		if fromNode == nil || toNode == nil {
			continue // dangling refs are validateEdgeNodeRefs' finding
		}

		// Client rule first: it is the root cause, so the dial rules below are skipped for it.
		if fromNode.Role == "client" || toNode.Role == "client" {
			clientNode := fromNode
			if toNode.Role == "client" {
				clientNode = toNode
			}
			result.AddError(prefix, CodeEdgeLinkDirectionClientEdge, P{"id", edge.ID}, P{"node", clientNode.Name}, P{"direction", dir})
			continue
		}

		// Pair conflict (primary class only): folding would silently ignore this edge's direction.
		if !linkid.IsBackup(edge) {
			if ids := pairEdgeIDs[linkid.PinKey(edge.FromNodeID, edge.ToNodeID)]; len(ids) > 1 {
				other := ids[0]
				if other == edge.ID {
					other = ids[1]
				}
				result.AddError(prefix, CodeEdgeLinkDirectionConflict, P{"id", edge.ID}, P{"direction", dir}, P{"other", other})
			}
		}

		// Forward requires a dialable host on the edge itself.
		if edge.EndpointHost == "" {
			result.AddError(prefix, CodeEdgeLinkDirectionForwardNoEndpoint, P{"id", edge.ID})
		}
	}
}

// validateBackupClientEdges validates that a role=="backup" edge must not touch a client node.
// A client uses a single wg0 and does not run Babel, so it has no per-peer interfaces and no
// cost-based failover semantics; a backup link is meaningless for it (consistent with the "client
// has exactly one outbound edge, single wg0" constraint in validateClientEdges).
func validateBackupClientEdges(topo *model.Topology, nodeMap map[string]*model.Node, result *ValidationResult) {
	for i := range topo.Edges {
		edge := &topo.Edges[i]
		if !edge.IsEnabled {
			continue
		}
		if !linkid.IsBackup(edge) {
			continue
		}
		fromNode := nodeMap[edge.FromNodeID]
		toNode := nodeMap[edge.ToNodeID]
		if (fromNode != nil && fromNode.Role == "client") || (toNode != nil && toNode.Role == "client") {
			result.AddError(fmt.Sprintf("edges[%d].role", i), CodeEdgeBackupTouchesClient, P{"id", edge.ID})
		}
	}
}

// pairLinkSummary summarizes all links of a node pair, used for equal-cost / no-primary warnings.
type pairLinkSummary struct {
	edgeIndex  int    // representative edge index for locating a triggered warning (the pair's first enabled edge)
	hasPrimary bool   // whether a primary class link exists
	costs      []int  // each link's comparable cost (with 0 already normalized to 96)
	fromName   string // used in error messages
	toName     string
}

// validateParallelLinkCosts emits two kinds of warnings for multi-link node pairs (parallel links /
// failover):
//   - Equal-cost warning: a node pair has >=2 links, but all links resolve to the same comparable
//     cost -- Babel cannot prefer any one of them, and the config fails to express failover intent
//     (spec: docs/spec/artifacts/babel.md).
//   - No-primary warning: all of a node pair's links are backup (e.g. a role flip that left out the
//     primary).
//
// Links are grouped by the unify rule: a primary class folds into one link (representative edge =
// first primary class edge), and each backup edge is its own link. Cost resolution exactly mirrors
// the compiler (effectiveLinkCost), and before comparison comparableCost normalizes 0 to babeld's
// built-in default of 96.
func validateParallelLinkCosts(topo *model.Topology, nodeMap map[string]*model.Node, result *ValidationResult) {
	// pinKey -> the node pair's link summary. pinKey is direction-independent, ensuring forward/reverse
	// edges fall into the same pair.
	summaries := make(map[string]*pairLinkSummary)
	order := make([]string, 0) // preserve first-appearance order so warnings are stable and testable

	// primaryCounted records whether each node pair's primary class has been counted toward cost: a
	// primary class folds into one link, and its cost is taken from the first primary class edge (the
	// representative) and counted only once.
	primaryCounted := make(map[string]bool)

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
		pk := linkid.PinKey(edge.FromNodeID, edge.ToNodeID)
		s := summaries[pk]
		if s == nil {
			s = &pairLinkSummary{
				edgeIndex: i,
				fromName:  fromNode.Name,
				toName:    toNode.Name,
			}
			summaries[pk] = s
			order = append(order, pk)
		}

		if linkid.IsBackup(edge) {
			// Each backup edge is its own link.
			s.costs = append(s.costs, comparableCost(effectiveLinkCost(edge)))
			continue
		}

		// primary class: all non-backup edges fold into one link; count the representative (first)
		// edge's cost only once.
		s.hasPrimary = true
		if !primaryCounted[pk] {
			primaryCounted[pk] = true
			s.costs = append(s.costs, comparableCost(effectiveLinkCost(edge)))
		}
	}

	for _, pk := range order {
		s := summaries[pk]

		// No-primary warning: the node pair has links but no primary class link (all are backup).
		if len(s.costs) > 0 && !s.hasPrimary {
			result.AddWarning(fmt.Sprintf("edges[%d]", s.edgeIndex), CodeLinkNoPrimary, P{"node", s.fromName}, P{"other", s.toName})
		}

		// Equal-cost warning: >=2 links and all comparable costs are identical.
		if len(s.costs) >= 2 {
			allEqual := true
			for _, c := range s.costs[1:] {
				if c != s.costs[0] {
					allEqual = false
					break
				}
			}
			if allEqual {
				result.AddWarning(fmt.Sprintf("edges[%d]", s.edgeIndex), CodeLinkEqualCost, P{"node", s.fromName}, P{"other", s.toName}, P{"count", strconv.Itoa(len(s.costs))}, P{"low", strconv.Itoa(s.costs[0])})
			}
		}
	}
}
