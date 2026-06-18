package validator

import (
	"fmt"

	"github.com/kunorikiku/yet-another-overlay-generator/internal/model"
)

// canBeDialed reports whether a node can be dialed into by a peer without having an endpoint.
//
// It is called during the semantic-validation phase, at which point role capabilities have not yet
// been derived by InferCapabilitiesFromRole (capability inference happens in compiler.Compile's
// capability-inference pass, which is later than this validation). So in addition to the explicitly
// declared HasPublicIP / CanAcceptInbound, it must also treat the relay role as accepting inbound —
// a relay is guaranteed to get CanAcceptInbound=true after capability inference. This is consistent
// with the hasOutboundToPublic check below and with the relay exception in the target-reachability
// warning.
func canBeDialed(node *model.Node) bool {
	return node.Capabilities.HasPublicIP || node.Capabilities.CanAcceptInbound || node.Role == "relay"
}

// hasEnabledEndpointEdge reports whether an enabled edge carrying an endpoint_host exists in the
// from->to direction. As long as some direction carries an endpoint_host, the WireGuard peer can
// actively dial out to establish the tunnel; conversely, a WireGuard peer with no Endpoint at all
// can only ever wait passively and will never initiate a handshake.
func hasEnabledEndpointEdge(topo *model.Topology, fromID, toID string) bool {
	for _, edge := range topo.Edges {
		if !edge.IsEnabled {
			continue
		}
		if edge.FromNodeID == fromID && edge.ToNodeID == toID && edge.EndpointHost != "" {
			return true
		}
	}
	return false
}

// validateNATReachability checks NAT reachability across the topology: it warns when an edge's
// target cannot be reached, escalates a provably-undeliverable double-NAT direct link with no
// endpoint to an error (D50 dead link), and warns when a node with no public IP and no inbound
// acceptance has no outbound edge toward a publicly reachable peer.
func validateNATReachability(topo *model.Topology, nodeMap map[string]*model.Node, result *ValidationResult) {
	for i, edge := range topo.Edges {
		if !edge.IsEnabled {
			continue
		}

		fromNode := nodeMap[edge.FromNodeID]
		toNode := nodeMap[edge.ToNodeID]
		if fromNode == nil || toNode == nil {
			continue
		}

		prefix := fmt.Sprintf("edges[%d]", i)

		// Target unreachable: the target node has no public IP, does not accept inbound connections, and is not a relay
		if !toNode.Capabilities.HasPublicIP && !toNode.Capabilities.CanAcceptInbound && toNode.Role != "relay" {
			result.AddWarning(prefix, CodeNATTargetUnreachable, P{"edge", edge.ID}, P{"node", toNode.Name})
		}

		// Double-ended NAT: a direct link where both ends lack a public IP and no endpoint is specified.
		if !fromNode.Capabilities.HasPublicIP && !toNode.Capabilities.HasPublicIP {
			if edge.Type == "direct" && edge.EndpointHost == "" {
				// D50: decide whether this edge is a "definite dead link". A dead link requires both
				// conditions to hold simultaneously; when both hold, neither side can initiate a
				// handshake and the tunnel is provably unbuildable at compile time — escalate to an error.
				//   1. No direction carries an endpoint_host: this direction (from->to) has no
				//      endpoint_host (already guaranteed by entering this branch), and the reverse
				//      direction (to->from) also has no enabled edge carrying an endpoint_host;
				//   2. Neither end can be dialed: both sides reject inbound (including the relay-role exception).
				// If either condition fails (e.g. a reverse edge carries an endpoint, or one end is a
				// relay / accepts inbound), a link may still be established, so keep the original warning.
				reverseHasEndpoint := hasEnabledEndpointEdge(topo, toNode.ID, fromNode.ID)
				neitherCanBeDialed := !canBeDialed(fromNode) && !canBeDialed(toNode)

				if !reverseHasEndpoint && neitherCanBeDialed {
					result.AddError(prefix, CodeNATDeadLink, P{"edge", edge.ID}, P{"from", fromNode.Name}, P{"to", toNode.Name})
				} else {
					result.AddWarning(prefix, CodeNATDoubleNATNoEndpoint, P{"edge", edge.ID}, P{"from", fromNode.Name}, P{"to", toNode.Name})
				}
			}
		}
	}

	// Per-node check: a node behind NAT (no public IP, no inbound acceptance) must have at least one
	// outbound edge toward a publicly reachable peer (public IP / accepts inbound / relay), otherwise
	// it has no way to reach the overlay.
	for _, node := range topo.Nodes {
		if node.Capabilities.HasPublicIP || node.Capabilities.CanAcceptInbound {
			continue // The node is itself reachable; nothing to check.
		}

		hasOutboundToPublic := false
		for _, edge := range topo.Edges {
			if !edge.IsEnabled || edge.FromNodeID != node.ID {
				continue
			}
			target := nodeMap[edge.ToNodeID]
			if target != nil && (target.Capabilities.HasPublicIP || target.Capabilities.CanAcceptInbound || target.Role == "relay") {
				hasOutboundToPublic = true
				break
			}
		}

		if !hasOutboundToPublic && len(topo.Edges) > 0 {
			result.AddWarning("nat_reachability", CodeNATNoOutboundToPublic, P{"name", node.Name}, P{"id", node.ID})
		}
	}
}
