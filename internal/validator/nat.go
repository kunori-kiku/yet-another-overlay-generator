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

		// 目标不可达：目标节点既无公网 IP 也不接受入站连接，且不是 relay
		if !toNode.Capabilities.HasPublicIP && !toNode.Capabilities.CanAcceptInbound && toNode.Role != "relay" {
			result.AddWarning(prefix,
				fmt.Sprintf("Edge %s: 目标节点 %s 没有公网 IP 且不接受入站连接，对端将无法主动连入",
					edge.ID, toNode.Name))
		}

		// 双端 NAT：两端均无公网 IP 的 direct 链路，且未指定 endpoint，隧道无法建立
		if !fromNode.Capabilities.HasPublicIP && !toNode.Capabilities.HasPublicIP {
			if edge.Type == "direct" && edge.EndpointHost == "" {
				result.AddWarning(prefix,
					fmt.Sprintf("Edge %s: 节点 %s 与 %s 均位于 NAT 之后且未提供 endpoint 主机地址，直连隧道无法建立（需借助 relay 或公网中转）",
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
				fmt.Sprintf("位于 NAT 之后的节点 %s (%s) 没有任何指向公网/可入站节点或 relay 的出站连接，将无法接入 overlay",
					node.Name, node.ID))
		}
	}
}
