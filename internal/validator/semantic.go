package validator

import (
	"fmt"
	"net"

	"github.com/kunorikiku/yet-another-overlay-generator/internal/model"
)

// ValidateSemantic 执行语义校验（编译器 Pass 2）
// 检查跨实体引用、IP 语义、拓扑连通性等
func ValidateSemantic(topo *model.Topology) *ValidationResult {
	result := &ValidationResult{}

	// 构建索引
	domainMap := buildDomainMap(topo)
	nodeMap := buildNodeMap(topo)

	// 校验节点引用的 Domain 是否存在
	validateNodeDomainRefs(topo, domainMap, result)

	// 校验 Edge 引用的节点是否存在
	validateEdgeNodeRefs(topo, nodeMap, result)

	// 校验 IP 语义
	validateIPSemantics(topo, domainMap, result)

	// 校验 ID 唯一性
	validateIDUniqueness(topo, result)

	// 校验 listen port 冲突
	validateListenPortConflicts(topo, result)

	// 检测孤立节点
	detectIsolatedNodes(topo, result)

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
				result.AddError(fmt.Sprintf("nodes[%d].domain_id", i),
					fmt.Sprintf("节点 %s 引用的 Domain %s 不存在", node.Name, node.DomainID))
			}
		}
	}
}

func validateEdgeNodeRefs(topo *model.Topology, nodeMap map[string]*model.Node, result *ValidationResult) {
	for i, edge := range topo.Edges {
		prefix := fmt.Sprintf("edges[%d]", i)
		if edge.FromNodeID != "" {
			if _, ok := nodeMap[edge.FromNodeID]; !ok {
				result.AddError(prefix+".from_node_id",
					fmt.Sprintf("Edge %s 引用的起始节点 %s 不存在", edge.ID, edge.FromNodeID))
			}
		}
		if edge.ToNodeID != "" {
			if _, ok := nodeMap[edge.ToNodeID]; !ok {
				result.AddError(prefix+".to_node_id",
					fmt.Sprintf("Edge %s 引用的目标节点 %s 不存在", edge.ID, edge.ToNodeID))
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
			// 已在 schema 校验中检查，此处跳过
			continue
		}

		// 检查 IP 是否属于对应 Domain CIDR
		domain, ok := domainMap[node.DomainID]
		if ok && domain.CIDR != "" {
			_, cidrNet, err := net.ParseCIDR(domain.CIDR)
			if err == nil && !cidrNet.Contains(ip) {
				result.AddError(prefix,
					fmt.Sprintf("节点 %s 的 IP %s 不属于 Domain %s 的 CIDR %s",
						node.Name, node.OverlayIP, domain.Name, domain.CIDR))
			}
		}

		// 检查 IP 是否重复
		if existingNode, exists := ipUsage[node.OverlayIP]; exists {
			result.AddError(prefix,
				fmt.Sprintf("IP %s 重复分配: 已被节点 %s 使用, 又被节点 %s 使用",
					node.OverlayIP, existingNode, node.Name))
		} else {
			ipUsage[node.OverlayIP] = node.Name
		}
	}
}

func validateIDUniqueness(topo *model.Topology, result *ValidationResult) {
	// Domain ID 唯一性
	domainIDs := make(map[string]bool)
	for i, d := range topo.Domains {
		if domainIDs[d.ID] {
			result.AddError(fmt.Sprintf("domains[%d].id", i),
				fmt.Sprintf("Domain ID 重复: %s", d.ID))
		}
		domainIDs[d.ID] = true
	}

	// Node ID 唯一性
	nodeIDs := make(map[string]bool)
	for i, n := range topo.Nodes {
		if nodeIDs[n.ID] {
			result.AddError(fmt.Sprintf("nodes[%d].id", i),
				fmt.Sprintf("Node ID 重复: %s", n.ID))
		}
		nodeIDs[n.ID] = true
	}

	// Edge ID 唯一性
	edgeIDs := make(map[string]bool)
	for i, e := range topo.Edges {
		if edgeIDs[e.ID] {
			result.AddError(fmt.Sprintf("edges[%d].id", i),
				fmt.Sprintf("Edge ID 重复: %s", e.ID))
		}
		edgeIDs[e.ID] = true
	}
}

func validateListenPortConflicts(topo *model.Topology, result *ValidationResult) {
	// 同一主机上不应有相同的 listen port（通过 hostname 判断）
	// 由于不同主机可以用相同端口，因此按 hostname 分组检查
	type hostPort struct {
		hostname string
		port     int
	}

	seen := make(map[hostPort]string) // hostPort -> node name
	for i, node := range topo.Nodes {
		if node.ListenPort == 0 || node.Hostname == "" {
			continue
		}
		hp := hostPort{hostname: node.Hostname, port: node.ListenPort}
		if existingNode, exists := seen[hp]; exists {
			result.AddWarning(fmt.Sprintf("nodes[%d].listen_port", i),
				fmt.Sprintf("节点 %s 与节点 %s 在主机 %s 上使用了相同的监听端口 %d",
					node.Name, existingNode, node.Hostname, node.ListenPort))
		} else {
			seen[hp] = node.Name
		}
	}
}

func detectIsolatedNodes(topo *model.Topology, result *ValidationResult) {
	if len(topo.Nodes) <= 1 {
		return
	}

	// 收集所有在边中出现的节点
	connectedNodes := make(map[string]bool)
	for _, edge := range topo.Edges {
		if edge.IsEnabled {
			connectedNodes[edge.FromNodeID] = true
			connectedNodes[edge.ToNodeID] = true
		}
	}

	// 检查未出现在任何边中的节点
	for _, node := range topo.Nodes {
		if !connectedNodes[node.ID] {
			result.AddWarning("topology",
				fmt.Sprintf("节点 %s (%s) 是孤立节点，没有任何可达连接", node.Name, node.ID))
		}
	}
}
