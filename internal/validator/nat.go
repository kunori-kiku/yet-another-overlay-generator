package validator

import (
	"fmt"

	"github.com/kunorikiku/yet-another-overlay-generator/internal/model"
)

// canBeDialed 判断一个节点在没有 endpoint 的情况下能否被对端主动拨入。
//
// 该函数在语义校验阶段调用，此时角色能力尚未被 InferCapabilitiesFromRole 推导
// （能力推导发生在 compiler.Compile 的能力推导 Pass，晚于本校验），因此除了已显式声明的
// HasPublicIP / CanAcceptInbound 之外，还必须把 relay 角色当作可接受入站——
// relay 在能力推导后必然得到 CanAcceptInbound=true。这与本文件下方
// hasOutboundToPublic 判定以及目标可达性告警中的 relay 例外保持一致。
func canBeDialed(node *model.Node) bool {
	return node.Capabilities.HasPublicIP || node.Capabilities.CanAcceptInbound || node.Role == "relay"
}

// hasEnabledEndpointEdge 判断 from->to 方向上是否存在一条启用且携带 endpoint_host 的边。
// 只要某个方向带有 endpoint_host，WireGuard 对端即可主动拨号建立隧道；
// 反之，没有任何 Endpoint 的 WireGuard 对端永远只能被动等待、不会主动发起握手。
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
			result.AddWarning(prefix, CodeNATTargetUnreachable, P{"edge", edge.ID}, P{"node", toNode.Name})
		}

		// 双端 NAT：两端均无公网 IP 的 direct 链路，且未指定 endpoint。
		if !fromNode.Capabilities.HasPublicIP && !toNode.Capabilities.HasPublicIP {
			if edge.Type == "direct" && edge.EndpointHost == "" {
				// D50：判定这条边是否「确凿死链」。死链需同时满足两个条件，二者皆成立时
				// 没有任何一方能发起握手，隧道在编译期即可证明无法建立——升级为 error。
				//   1. 没有任何方向带 endpoint_host：本方向（from->to）无 endpoint_host
				//      （进入此分支已保证），且反向（to->from）也不存在带 endpoint_host 的启用边；
				//   2. 两端都无法被拨入：双方均不接受入站（含 relay 角色例外）。
				// 任一条件不成立（例如反向边带 endpoint，或某一端是 relay / 可接受入站），
				// 则仍可能建链，保持原有 warning。
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
			result.AddWarning("nat_reachability", CodeNATNoOutboundToPublic, P{"name", node.Name}, P{"id", node.ID})
		}
	}
}
