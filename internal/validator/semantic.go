package validator

import (
	"fmt"
	"net"

	"github.com/kunorikiku/yet-another-overlay-generator/internal/model"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/naming"
)

// ValidateSemantic （ Pass 2）
// 、IP 、
func ValidateSemantic(topo *model.Topology) *ValidationResult {
	result := &ValidationResult{}

	// 
	domainMap := buildDomainMap(topo)
	nodeMap := buildNodeMap(topo)

	//  Domain 
	validateNodeDomainRefs(topo, domainMap, result)

	//  Edge 
	validateEdgeNodeRefs(topo, nodeMap, result)

	//  IP 
	validateIPSemantics(topo, domainMap, result)

	//  ID
	validateIDUniqueness(topo, result)

	// 节点名称冲突（原始名称、安装脚本文件名、WireGuard 接口名）
	validateNodeNameCollisions(topo, result)

	//  listen port 
	validateListenPortConflicts(topo, result)

	// 
	detectIsolatedNodes(topo, result)

	// NAT
	validateNATReachability(topo, nodeMap, result)

	// Client 边验证
	validateClientEdges(topo, nodeMap, result)

	// Edge endpoint 与目标节点 public endpoints 一致性检查
	validateEdgeEndpointConsistency(topo, nodeMap, result)

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
					fmt.Sprintf(" %s  Domain %s ", node.Name, node.DomainID))
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
					fmt.Sprintf("Edge %s  %s ", edge.ID, edge.FromNodeID))
			}
		}
		if edge.ToNodeID != "" {
			if _, ok := nodeMap[edge.ToNodeID]; !ok {
				result.AddError(prefix+".to_node_id",
					fmt.Sprintf("Edge %s  %s ", edge.ID, edge.ToNodeID))
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
			//  schema ，
			continue
		}

		//  IP  Domain CIDR
		domain, ok := domainMap[node.DomainID]
		if ok && domain.CIDR != "" {
			_, cidrNet, err := net.ParseCIDR(domain.CIDR)
			if err == nil && !cidrNet.Contains(ip) {
				result.AddError(prefix,
					fmt.Sprintf(" %s  IP %s  Domain %s  CIDR %s",
						node.Name, node.OverlayIP, domain.Name, domain.CIDR))
			}
		}

		//  IP 
		if existingNode, exists := ipUsage[node.OverlayIP]; exists {
			result.AddError(prefix,
				fmt.Sprintf("IP %s :  %s ,  %s ",
					node.OverlayIP, existingNode, node.Name))
		} else {
			ipUsage[node.OverlayIP] = node.Name
		}
	}
}

func validateIDUniqueness(topo *model.Topology, result *ValidationResult) {
	// Domain ID 
	domainIDs := make(map[string]bool)
	for i, d := range topo.Domains {
		if domainIDs[d.ID] {
			result.AddError(fmt.Sprintf("domains[%d].id", i),
				fmt.Sprintf("Domain ID : %s", d.ID))
		}
		domainIDs[d.ID] = true
	}

	// Node ID 
	nodeIDs := make(map[string]bool)
	for i, n := range topo.Nodes {
		if nodeIDs[n.ID] {
			result.AddError(fmt.Sprintf("nodes[%d].id", i),
				fmt.Sprintf("Node ID : %s", n.ID))
		}
		nodeIDs[n.ID] = true
	}

	// Edge ID 
	edgeIDs := make(map[string]bool)
	for i, e := range topo.Edges {
		if edgeIDs[e.ID] {
			result.AddError(fmt.Sprintf("edges[%d].id", i),
				fmt.Sprintf("Edge ID : %s", e.ID))
		}
		edgeIDs[e.ID] = true
	}
}

// validateNodeNameCollisions 检查节点名称在三种规范化形式下的冲突（Spec D 的 N1–N3 不变式）。
// 任意两个不同节点若在以下任一形式上相同，都会导致命名派生的产物相互覆盖或被静默跳过：
//   - 原始名称（N1）：操作员与一切基于名称派生的产物都无法区分两个同名节点。
//   - 安装脚本文件名 SafeInstallerFileName（N2）：相同的安装包文件名会造成静默跳过与身份错位的部署。
//   - WireGuard 接口名 WgInterfaceName（N3）：相同的接口名会让一个 WireGuard 配置与一条 Babel 接口行覆盖另一个。
//
// 对每种规范化形式各维护一张「规范化键 -> 首个使用该键的节点名称」映射，
// 在遇到第二个落入同一键的节点时报错，并在错误消息中同时点出两个冲突节点的名称。
func validateNodeNameCollisions(topo *model.Topology, result *ValidationResult) {
	// 各映射的键是一种规范化形式，值是首个使用该键的节点名称。
	rawNames := make(map[string]string)       // 原始名称 -> 首个节点名称
	installerNames := make(map[string]string) // 安装脚本文件名 -> 首个节点名称
	interfaceNames := make(map[string]string) // WireGuard 接口名 -> 首个节点名称

	for i, node := range topo.Nodes {
		if node.Name == "" {
			//  schema 校验已覆盖空名称，这里跳过以免与空字符串归一冲突。
			continue
		}
		prefix := fmt.Sprintf("nodes[%d].name", i)

		// N1：原始名称冲突。
		if firstNode, exists := rawNames[node.Name]; exists {
			result.AddError(prefix,
				fmt.Sprintf("节点名称重复：节点 %s 与节点 %s 使用了相同的名称 %q",
					firstNode, node.Name, node.Name))
		} else {
			rawNames[node.Name] = node.Name
		}

		// N2：安装脚本文件名冲突（例如 "Web 1" 与 "web-1" 都归一为 web-1.install.sh）。
		installerName := naming.SafeInstallerFileName(node.Name)
		if firstNode, exists := installerNames[installerName]; exists {
			if firstNode != node.Name {
				result.AddError(prefix,
					fmt.Sprintf("节点名称会生成相同的安装脚本文件名：节点 %s 与节点 %s 都归一为 %q，将造成部署时静默跳过或身份错位",
						firstNode, node.Name, installerName))
			}
		} else {
			installerNames[installerName] = node.Name
		}

		// N3：WireGuard 接口名冲突（例如 "db.east" 与 "db-east" 都归一为 wg-db-east）。
		interfaceName := naming.WgInterfaceName(node.Name)
		if firstNode, exists := interfaceNames[interfaceName]; exists {
			if firstNode != node.Name {
				result.AddError(prefix,
					fmt.Sprintf("节点名称会生成相同的 WireGuard 接口名：节点 %s 与节点 %s 都归一为 %q，将造成一个接口配置覆盖另一个",
						firstNode, node.Name, interfaceName))
			}
		} else {
			interfaceNames[interfaceName] = node.Name
		}
	}
}

func validateListenPortConflicts(topo *model.Topology, result *ValidationResult) {
	//  listen port（ hostname ）
	// ， hostname 
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
				fmt.Sprintf(" %s  %s  %s  %d",
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

	// 
	connectedNodes := make(map[string]bool)
	for _, edge := range topo.Edges {
		if edge.IsEnabled {
			connectedNodes[edge.FromNodeID] = true
			connectedNodes[edge.ToNodeID] = true
		}
	}

	// 
	for _, node := range topo.Nodes {
		if !connectedNodes[node.ID] {
			result.AddWarning("topology",
				fmt.Sprintf(" %s (%s) ，", node.Name, node.ID))
		}
	}
}

// validateClientEdges 验证 client 节点的边约束
func validateClientEdges(topo *model.Topology, nodeMap map[string]*model.Node, result *ValidationResult) {
	// 收集每个 client 的出站和入站 edge 数量
	clientOutbound := make(map[string]int)    // nodeID -> count of enabled outbound edges
	clientOutboundEdges := make(map[string][]int) // nodeID -> edge indices

	for i, edge := range topo.Edges {
		if !edge.IsEnabled {
			continue
		}

		fromNode := nodeMap[edge.FromNodeID]
		toNode := nodeMap[edge.ToNodeID]

		// 拒绝以 client 为目标的入站边
		if toNode != nil && toNode.Role == "client" {
			result.AddError(fmt.Sprintf("edges[%d]", i),
				fmt.Sprintf("Client node %s cannot accept inbound connections", toNode.Name))
		}

		// 统计 client 出站边
		if fromNode != nil && fromNode.Role == "client" {
			clientOutbound[fromNode.ID]++
			clientOutboundEdges[fromNode.ID] = append(clientOutboundEdges[fromNode.ID], i)

			// Client 的目标必须是 router/relay/gateway（不能是 peer 或 client）
			if toNode != nil {
				if toNode.Role == "peer" {
					result.AddError(fmt.Sprintf("edges[%d]", i),
						fmt.Sprintf("Client %s cannot connect to peer %s (peers don't forward traffic)", fromNode.Name, toNode.Name))
				}
				if toNode.Role == "client" {
					result.AddError(fmt.Sprintf("edges[%d]", i),
						fmt.Sprintf("Client %s cannot connect to another client %s", fromNode.Name, toNode.Name))
				}
			}

			// Client 边必须有 endpoint_host
			if edge.EndpointHost == "" {
				result.AddError(fmt.Sprintf("edges[%d].endpoint_host", i),
					fmt.Sprintf("Client %s requires endpoint_host to reach router", fromNode.Name))
			}
		}
	}

	// Client 必须恰好有一条启用的出站边
	for _, node := range topo.Nodes {
		if node.Role != "client" {
			continue
		}
		count := clientOutbound[node.ID]
		if count == 0 {
			result.AddError("topology",
				fmt.Sprintf("Client %s must have exactly one enabled outbound edge", node.Name))
		} else if count > 1 {
			result.AddError("topology",
				fmt.Sprintf("Client %s has %d outbound edges but must have exactly one (single wg0 interface)", node.Name, count))
		}

		// 警告：client 设置了无意义的字段
		if node.RouterID != "" {
			result.AddWarning(fmt.Sprintf("node.%s.router_id", node.ID),
				fmt.Sprintf("Client %s has router_id set but clients don't run Babel", node.Name))
		}
		if len(node.ExtraPrefixes) > 0 {
			result.AddWarning(fmt.Sprintf("node.%s.extra_prefixes", node.ID),
				fmt.Sprintf("Client %s has extra_prefixes set but clients don't announce routes", node.Name))
		}
	}
}

// validateEdgeEndpointConsistency 检查 edge 的 endpoint_host 是否与目标节点的 public endpoints 一致。
// 当一个启用的 edge 设置了 endpoint_host，目标节点也声明了至少一个 public endpoint，
// 但目标节点的所有 public_endpoints[].host 都不等于该 endpoint_host 时，发出警告——
// 这通常意味着 edge 上的快照在节点 endpoint 被编辑后变得陈旧。
// 仅警告而非报错：在 NAT/端口转发或 hairpin 场景下，dial 的 host 与节点自身声明的 host 可以合法地不同。
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
			result.AddWarning(fmt.Sprintf("edges[%d].endpoint_host", i),
				fmt.Sprintf("Edge %s dials %s but target %s has no matching public endpoint (the endpoint snapshot may be stale after a node edit)",
					edge.ID, edge.EndpointHost, toNode.Name))
		}
	}
}
