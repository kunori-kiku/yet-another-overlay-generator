package validator

import (
	"fmt"

	"github.com/kunorikiku/yet-another-overlay-generator/internal/model"
)

// validateNATReachability NAT 
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

		// ：NAT （ relay）
		if !toNode.Capabilities.HasPublicIP && !toNode.Capabilities.CanAcceptInbound && toNode.Role != "relay" {
			result.AddWarning(prefix,
				fmt.Sprintf("Edge %s:  %s  IP ，",
					edge.ID, toNode.Name))
		}

		// ： NAT （）
		if !fromNode.Capabilities.HasPublicIP && !toNode.Capabilities.HasPublicIP {
			if edge.Type == "direct" {
				result.AddWarning(prefix,
					fmt.Sprintf("Edge %s:  %s  %s  NAT ，，",
						edge.ID, fromNode.Name, toNode.Name))
			}
		}
	}

	// ：/relay  Edge
	for _, node := range topo.Nodes {
		if node.Capabilities.HasPublicIP || node.Capabilities.CanAcceptInbound {
			continue // 
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
			result.AddWarning("nat_reachability",
				fmt.Sprintf("NAT  %s (%s) /，",
					node.Name, node.ID))
		}
	}
}
