package validator

import (
	"fmt"

	"github.com/kunorikiku/yet-another-overlay-generator/internal/model"
)

// validateNATReachability NAT 可达性约束验证
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

		// 警告：NAT 后节点被作为入站目标（且不是 relay）
		if !toNode.Capabilities.HasPublicIP && !toNode.Capabilities.CanAcceptInbound && toNode.Role != "relay" {
			result.AddWarning(prefix,
				fmt.Sprintf("Edge %s: 目标节点 %s 没有公网 IP 且不接受入站连接，连接可能无法建立",
					edge.ID, toNode.Name))
		}

		// 警告：两个 NAT 后节点直连（需中继）
		if !fromNode.Capabilities.HasPublicIP && !toNode.Capabilities.HasPublicIP {
			if edge.Type == "direct" {
				result.AddWarning(prefix,
					fmt.Sprintf("Edge %s: 节点 %s 和 %s 都在 NAT 后面，直连可能无法建立，建议通过中继",
						edge.ID, fromNode.Name, toNode.Name))
			}
		}
	}

	// 检查：非公网节点至少有一条到公网/relay 节点的出站 Edge
	for _, node := range topo.Nodes {
		if node.Capabilities.HasPublicIP || node.Capabilities.CanAcceptInbound {
			continue // 公网节点不需要检查
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
				fmt.Sprintf("NAT 后节点 %s (%s) 没有到任何公网/中继节点的出站连接，可能无法加入网络",
					node.Name, node.ID))
		}
	}
}
