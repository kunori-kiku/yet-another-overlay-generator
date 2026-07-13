package validator

import (
	"fmt"
	"net"
	"strconv"
	"strings"

	"github.com/kunorikiku/yet-another-overlay-generator/internal/allocconst"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/linkid"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/model"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/naming"
)

// ValidateSemantic runs the semantic validation pass (Pass 2): cross-reference
// checks, IP collision detection, and reachability/topology rules.
func ValidateSemantic(topo *model.Topology) *ValidationResult {
	result := &ValidationResult{}

	// DoS / forward-compat guard (plan-6): short-circuit BEFORE the O(n²) collision and
	// NAT-reachability passes so they never run on an oversized or future-format topology.
	// ValidateSchema is the canonical reporter of these root errors (HandleValidate runs
	// both passes and concatenates their errors, so re-reporting here would duplicate them);
	// this guard only protects the expensive work.
	if topologyExceedsBounds(topo) {
		return result
	}

	// Build lookup maps.
	domainMap := buildDomainMap(topo)
	nodeMap := buildNodeMap(topo)

	// Node -> Domain references.
	validateNodeDomainRefs(topo, domainMap, result)

	// Edge -> Node references.
	validateEdgeNodeRefs(topo, nodeMap, result)

	// Overlay IP semantics.
	validateIPSemantics(topo, domainMap, result)

	// ID uniqueness.
	validateIDUniqueness(topo, result)

	// Node name collisions (raw name, installer script filename, WireGuard interface name).
	validateNodeNameCollisions(topo, result)

	// Effective listen-port range: whether each node's base..base+(peer-interface-count-1) overflows.
	validateEffectivePortRanges(topo, result)

	// Isolated nodes.
	detectIsolatedNodes(topo, result)

	// NAT reachability.
	validateNATReachability(topo, nodeMap, result)

	// Client edge validation.
	validateClientEdges(topo, nodeMap, result)

	// mimic (tcp transport): both endpoints of a tcp edge must be deployable Linux (eBPF/kernel features).
	validateMimicTransport(topo, nodeMap, result)

	// Edge endpoint vs target node public-endpoints consistency check.
	validateEdgeEndpointConsistency(topo, nodeMap, result)

	// Detect duplicate enabled edges on the same node pair (the compiler keeps only the first; later
	// edges' endpoint overrides are silently dropped).
	detectDuplicateEnabledEdges(topo, result)

	// Parallel links: per-node WireGuard interface-name uniqueness (invariant N4).
	validateInterfaceNameUniqueness(topo, nodeMap, result)

	// Parallel links: at most one explicit primary edge per node pair.
	validateSinglePrimaryPerPair(topo, nodeMap, result)

	// Parallel links: a client edge must not be backup (client uses a single wg0 and does not
	// participate in parallel links).
	validateBackupClientEdges(topo, nodeMap, result)

	// Link direction: a single-linked edge must be able to dial in its chosen direction, must not
	// fold with other primary-class edges of its pair, and must not touch a client.
	validateLinkDirection(topo, nodeMap, result)

	// Parallel links: equal-cost and no-primary warnings for multi-link node pairs.
	validateParallelLinkCosts(topo, nodeMap, result)

	// Allocation-pin validation: pins must be validated before any resource is reserved (invariant I7).
	validateAllocationPins(topo, domainMap, nodeMap, result)

	// route_policies is a reserved feature: reject any non-empty value (Decisions log #2, Spec E).
	validateRoutePoliciesReserved(topo, result)

	return result
}

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

func buildDomainMap(topo *model.Topology) map[string]*model.Domain {
	m := make(map[string]*model.Domain)
	for i := range topo.Domains {
		m[topo.Domains[i].ID] = &topo.Domains[i]
	}
	return m
}

func buildNodeMap(topo *model.Topology) map[string]*model.Node {
	m := make(map[string]*model.Node)
	for i := range topo.Nodes {
		m[topo.Nodes[i].ID] = &topo.Nodes[i]
	}
	return m
}

func validateNodeDomainRefs(topo *model.Topology, domainMap map[string]*model.Domain, result *ValidationResult) {
	for i, node := range topo.Nodes {
		if node.DomainID != "" {
			if _, ok := domainMap[node.DomainID]; !ok {
				result.AddError(fmt.Sprintf("nodes[%d].domain_id", i), CodeNodeDomainRefMissing, P{"node", node.Name}, P{"id", node.DomainID})
			}
		}
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

func validateIPSemantics(topo *model.Topology, domainMap map[string]*model.Domain, result *ValidationResult) {
	ipUsage := make(map[string]string) // IP -> node name

	for i, node := range topo.Nodes {
		if node.OverlayIP == "" {
			continue
		}
		prefix := fmt.Sprintf("nodes[%d].overlay_ip", i)

		ip := net.ParseIP(node.OverlayIP)
		if ip == nil {
			// Malformed IPs are reported by schema validation; skip here.
			continue
		}

		// Check the overlay IP falls within the domain CIDR.
		domain, ok := domainMap[node.DomainID]
		if ok && domain.CIDR != "" {
			_, cidrNet, err := net.ParseCIDR(domain.CIDR)
			if err == nil && !cidrNet.Contains(ip) {
				result.AddError(prefix, CodeNodeOverlayIPOutOfCIDR, P{"node", node.Name}, P{"cidr", node.OverlayIP}, P{"name", domain.Name}, P{"prefix", domain.CIDR})
			}
		}

		// Detect duplicate overlay IPs.
		if existingNode, exists := ipUsage[node.OverlayIP]; exists {
			result.AddError(prefix, CodeNodeOverlayIPConflict, P{"cidr", node.OverlayIP}, P{"other", existingNode}, P{"node", node.Name})
		} else {
			ipUsage[node.OverlayIP] = node.Name
		}
	}
}

func validateIDUniqueness(topo *model.Topology, result *ValidationResult) {
	// Domain IDs.
	domainIDs := make(map[string]bool)
	for i, d := range topo.Domains {
		if domainIDs[d.ID] {
			result.AddError(fmt.Sprintf("domains[%d].id", i), CodeDomainIDDuplicate, P{"id", d.ID})
		}
		domainIDs[d.ID] = true
	}

	// Node IDs.
	nodeIDs := make(map[string]bool)
	for i, n := range topo.Nodes {
		if nodeIDs[n.ID] {
			result.AddError(fmt.Sprintf("nodes[%d].id", i), CodeNodeIDDuplicate, P{"id", n.ID})
		}
		nodeIDs[n.ID] = true
	}

	// Edge IDs.
	edgeIDs := make(map[string]bool)
	for i, e := range topo.Edges {
		if edgeIDs[e.ID] {
			result.AddError(fmt.Sprintf("edges[%d].id", i), CodeEdgeIDDuplicate, P{"id", e.ID})
		}
		edgeIDs[e.ID] = true
	}
}

// validateNodeNameCollisions checks node-name collisions across three normalized forms (the N1-N3
// invariants of Spec D).
// If any two distinct nodes collide in any one of these forms, the name-derived artifacts will
// overwrite one another or be silently skipped:
//   - Raw name (N1): operators and every name-derived artifact cannot tell two same-named nodes apart.
//   - Installer script filename SafeInstallerFileName (N2): identical install-bundle filenames cause
//     silent skips and identity-confused deployments.
//   - WireGuard interface name WgInterfaceName (N3): identical interface names let one WireGuard config
//     and one Babel interface line overwrite another.
//
// For each normalized form it keeps a "normalized key -> first node name that used that key" map,
// errors when a second node falls into the same key, and names both conflicting nodes in the message.
func validateNodeNameCollisions(topo *model.Topology, result *ValidationResult) {
	// Each map's key is a normalized form; the value is the first node name that used that key.
	rawNames := make(map[string]string)       // raw name -> first node name
	installerNames := make(map[string]string) // installer script filename -> first node name
	interfaceNames := make(map[string]string) // WireGuard interface name -> first node name

	for i, node := range topo.Nodes {
		if node.Name == "" {
			// Schema validation already covers empty names; skip here to avoid an empty-string collision.
			continue
		}
		prefix := fmt.Sprintf("nodes[%d].name", i)

		// N1: raw-name collision.
		if firstNode, exists := rawNames[node.Name]; exists {
			result.AddError(prefix, CodeNodeNameDuplicate, P{"other", firstNode}, P{"node", node.Name}, P{"name", fmt.Sprintf("%q", node.Name)})
		} else {
			rawNames[node.Name] = node.Name
		}

		// N2: installer-filename collision (e.g. "Web 1" and "web-1" both normalize to web-1.install.sh).
		installerName := naming.SafeInstallerFileName(node.Name)
		if firstNode, exists := installerNames[installerName]; exists {
			if firstNode != node.Name {
				result.AddError(prefix, CodeNodeNameInstallerCollision, P{"other", firstNode}, P{"node", node.Name}, P{"name", fmt.Sprintf("%q", installerName)})
			}
		} else {
			installerNames[installerName] = node.Name
		}

		// N3: WireGuard interface-name collision (e.g. "db.east" and "db-east" both normalize to wg-db-east).
		interfaceName := naming.WgInterfaceName(node.Name)
		if firstNode, exists := interfaceNames[interfaceName]; exists {
			if firstNode != node.Name {
				result.AddError(prefix, CodeNodeNameInterfaceCollision, P{"other", firstNode}, P{"node", node.Name}, P{"name", fmt.Sprintf("%q", interfaceName)})
			}
		} else {
			interfaceNames[interfaceName] = node.Name
		}
	}
}

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

func detectIsolatedNodes(topo *model.Topology, result *ValidationResult) {
	if len(topo.Nodes) <= 1 {
		return
	}

	// Collect nodes that have at least one enabled edge.
	connectedNodes := make(map[string]bool)
	for _, edge := range topo.Edges {
		if edge.IsEnabled {
			connectedNodes[edge.FromNodeID] = true
			connectedNodes[edge.ToNodeID] = true
		}
	}

	// Warn about any node with no enabled edges.
	for _, node := range topo.Nodes {
		if !connectedNodes[node.ID] {
			result.AddWarning("topology", CodeNodeIsolated, P{"node", node.Name}, P{"id", node.ID})
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
