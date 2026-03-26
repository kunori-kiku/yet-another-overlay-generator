package validator

import (
	"fmt"
	"net"
	"strings"

	"github.com/kunorikiku/yet-another-overlay-generator/internal/model"
)

// ValidationError 
type ValidationError struct {
	Field   string `json:"field"`
	Message string `json:"message"`
	Level   string `json:"level"` // "error" | "warning"
}

func (e ValidationError) Error() string {
	return fmt.Sprintf("[%s] %s: %s", e.Level, e.Field, e.Message)
}

// ValidationResult 
type ValidationResult struct {
	Errors   []ValidationError `json:"errors"`
	Warnings []ValidationError `json:"warnings"`
}

func (r *ValidationResult) AddError(field, message string) {
	r.Errors = append(r.Errors, ValidationError{Field: field, Message: message, Level: "error"})
}

func (r *ValidationResult) AddWarning(field, message string) {
	r.Warnings = append(r.Warnings, ValidationError{Field: field, Message: message, Level: "warning"})
}

func (r *ValidationResult) IsValid() bool {
	return len(r.Errors) == 0
}

// ValidateSchema  Schema （ Pass 1）
// 、、CIDR 
func ValidateSchema(topo *model.Topology) *ValidationResult {
	result := &ValidationResult{}

	//  Project
	validateProjectSchema(topo, result)

	//  Domains
	validateDomainsSchema(topo, result)

	//  Nodes
	validateNodesSchema(topo, result)

	//  Edges
	validateEdgesSchema(topo, result)

	return result
}

func validateProjectSchema(topo *model.Topology, result *ValidationResult) {
	if topo.Project.ID == "" {
		result.AddError("project.id", " ID ")
	}
	if topo.Project.Name == "" {
		result.AddError("project.name", "")
	}
}

func validateDomainsSchema(topo *model.Topology, result *ValidationResult) {
	if len(topo.Domains) == 0 {
		result.AddError("domains", "")
		return
	}

	for i, domain := range topo.Domains {
		prefix := fmt.Sprintf("domains[%d]", i)

		if domain.ID == "" {
			result.AddError(prefix+".id", "Domain ID ")
		}
		if domain.Name == "" {
			result.AddError(prefix+".name", "Domain ")
		}

		// CIDR 
		if domain.CIDR == "" {
			result.AddError(prefix+".cidr", "CIDR ")
		} else {
			_, _, err := net.ParseCIDR(domain.CIDR)
			if err != nil {
				result.AddError(prefix+".cidr", fmt.Sprintf("CIDR : %s", domain.CIDR))
			}
		}

		// AllocationMode 
		validAllocModes := map[string]bool{"auto": true, "manual": true}
		if domain.AllocationMode != "" && !validAllocModes[domain.AllocationMode] {
			result.AddError(prefix+".allocation_mode",
				fmt.Sprintf(": %s, : auto, manual", domain.AllocationMode))
		}

		// RoutingMode 
		validRoutingModes := map[string]bool{"static": true, "babel": true, "none": true}
		if domain.RoutingMode != "" && !validRoutingModes[domain.RoutingMode] {
			result.AddError(prefix+".routing_mode",
				fmt.Sprintf(": %s, : static, babel, none", domain.RoutingMode))
		}

		// ReservedRanges CIDR 
		for j, rr := range domain.ReservedRanges {
			_, _, err := net.ParseCIDR(rr)
			if err != nil {
				//  IP
				if net.ParseIP(rr) == nil {
					result.AddError(fmt.Sprintf("%s.reserved_ranges[%d]", prefix, j),
						fmt.Sprintf(": %s", rr))
				}
			}
		}
	}
}

func validateNodesSchema(topo *model.Topology, result *ValidationResult) {
	for i, node := range topo.Nodes {
		prefix := fmt.Sprintf("nodes[%d]", i)

		if node.ID == "" {
			result.AddError(prefix+".id", " ID ")
		}
		if node.Name == "" {
			result.AddError(prefix+".name", "")
		}
		if node.DomainID == "" {
			result.AddError(prefix+".domain_id", " Domain")
		}

		// Role 
		validRoles := map[string]bool{"peer": true, "router": true, "relay": true, "gateway": true, "client": true}
		if node.Role == "" {
			result.AddError(prefix+".role", "role is required")
		} else if !validRoles[node.Role] {
			result.AddError(prefix+".role",
				fmt.Sprintf("invalid role %q, must be one of: peer, router, relay, gateway, client", node.Role))
		}

		// Platform （，）
		if node.Platform != "" {
			validPlatforms := map[string]bool{"debian": true, "ubuntu": true}
			if !validPlatforms[strings.ToLower(node.Platform)] {
				result.AddWarning(prefix+".platform",
					fmt.Sprintf("unsupported platform %q, supported values are: debian, ubuntu", node.Platform))
			}
		}

		// OverlayIP （）
		if node.OverlayIP != "" {
			if net.ParseIP(node.OverlayIP) == nil {
				result.AddError(prefix+".overlay_ip",
					fmt.Sprintf(" IP : %s", node.OverlayIP))
			}
		}

		// ListenPort 
		if node.ListenPort < 0 || node.ListenPort > 65535 {
			result.AddError(prefix+".listen_port",
				fmt.Sprintf(": %d", node.ListenPort))
		}
	}
}

func validateEdgesSchema(topo *model.Topology, result *ValidationResult) {
	for i, edge := range topo.Edges {
		prefix := fmt.Sprintf("edges[%d]", i)

		if edge.ID == "" {
			result.AddError(prefix+".id", "Edge ID ")
		}
		if edge.FromNodeID == "" {
			result.AddError(prefix+".from_node_id", " ID ")
		}
		if edge.ToNodeID == "" {
			result.AddError(prefix+".to_node_id", " ID ")
		}

		// Type 
		validTypes := map[string]bool{"direct": true, "public-endpoint": true, "relay-path": true, "candidate": true}
		if edge.Type == "" {
			result.AddError(prefix+".type", "")
		} else if !validTypes[edge.Type] {
			result.AddError(prefix+".type",
				fmt.Sprintf(": %s", edge.Type))
		}

		// Transport 
		if edge.Transport != "" {
			validTransports := map[string]bool{"udp": true, "tcp": true}
			if !validTransports[edge.Transport] {
				result.AddError(prefix+".transport",
					fmt.Sprintf(": %s, : udp, tcp", edge.Transport))
			}
		}

		// EndpointPort 
		if edge.EndpointPort < 0 || edge.EndpointPort > 65535 {
			result.AddError(prefix+".endpoint_port",
				fmt.Sprintf(": %d", edge.EndpointPort))
		}

		// 
		if edge.FromNodeID != "" && edge.FromNodeID == edge.ToNodeID {
			result.AddError(prefix, "（）")
		}
	}
}
