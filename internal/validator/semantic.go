package validator

import (
	"fmt"
	"net"

	"github.com/kunorikiku/yet-another-overlay-generator/internal/model"
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
