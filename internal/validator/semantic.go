package validator

import (
	"fmt"
	"net"

	"github.com/kunorikiku/yet-another-overlay-generator/internal/model"
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

	//  listen port 
	validateListenPortConflicts(topo, result)

	// 
	detectIsolatedNodes(topo, result)

	// NAT 
	validateNATReachability(topo, nodeMap, result)

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
