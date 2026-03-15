package validator

import (
	"fmt"
	"net"
	"strings"

	"github.com/kunorikiku/yet-another-overlay-generator/internal/model"
)

// ValidationError 校验错误
type ValidationError struct {
	Field   string `json:"field"`
	Message string `json:"message"`
	Level   string `json:"level"` // "error" | "warning"
}

func (e ValidationError) Error() string {
	return fmt.Sprintf("[%s] %s: %s", e.Level, e.Field, e.Message)
}

// ValidationResult 校验结果
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

// ValidateSchema 执行 Schema 校验（编译器 Pass 1）
// 检查必填字段、字段格式、CIDR 合法性
func ValidateSchema(topo *model.Topology) *ValidationResult {
	result := &ValidationResult{}

	// 校验 Project
	validateProjectSchema(topo, result)

	// 校验 Domains
	validateDomainsSchema(topo, result)

	// 校验 Nodes
	validateNodesSchema(topo, result)

	// 校验 Edges
	validateEdgesSchema(topo, result)

	return result
}

func validateProjectSchema(topo *model.Topology, result *ValidationResult) {
	if topo.Project.ID == "" {
		result.AddError("project.id", "项目 ID 不能为空")
	}
	if topo.Project.Name == "" {
		result.AddError("project.name", "项目名称不能为空")
	}
}

func validateDomainsSchema(topo *model.Topology, result *ValidationResult) {
	if len(topo.Domains) == 0 {
		result.AddError("domains", "至少需要定义一个网络域")
		return
	}

	for i, domain := range topo.Domains {
		prefix := fmt.Sprintf("domains[%d]", i)

		if domain.ID == "" {
			result.AddError(prefix+".id", "Domain ID 不能为空")
		}
		if domain.Name == "" {
			result.AddError(prefix+".name", "Domain 名称不能为空")
		}

		// CIDR 合法性验证
		if domain.CIDR == "" {
			result.AddError(prefix+".cidr", "CIDR 不能为空")
		} else {
			_, _, err := net.ParseCIDR(domain.CIDR)
			if err != nil {
				result.AddError(prefix+".cidr", fmt.Sprintf("CIDR 格式非法: %s", domain.CIDR))
			}
		}

		// AllocationMode 校验
		validAllocModes := map[string]bool{"auto": true, "manual": true}
		if domain.AllocationMode != "" && !validAllocModes[domain.AllocationMode] {
			result.AddError(prefix+".allocation_mode",
				fmt.Sprintf("无效的分配模式: %s, 允许值: auto, manual", domain.AllocationMode))
		}

		// RoutingMode 校验
		validRoutingModes := map[string]bool{"static": true, "babel": true, "none": true}
		if domain.RoutingMode != "" && !validRoutingModes[domain.RoutingMode] {
			result.AddError(prefix+".routing_mode",
				fmt.Sprintf("无效的路由模式: %s, 允许值: static, babel, none", domain.RoutingMode))
		}

		// ReservedRanges CIDR 校验
		for j, rr := range domain.ReservedRanges {
			_, _, err := net.ParseCIDR(rr)
			if err != nil {
				// 也尝试解析为单 IP
				if net.ParseIP(rr) == nil {
					result.AddError(fmt.Sprintf("%s.reserved_ranges[%d]", prefix, j),
						fmt.Sprintf("保留区间格式非法: %s", rr))
				}
			}
		}
	}
}

func validateNodesSchema(topo *model.Topology, result *ValidationResult) {
	for i, node := range topo.Nodes {
		prefix := fmt.Sprintf("nodes[%d]", i)

		if node.ID == "" {
			result.AddError(prefix+".id", "节点 ID 不能为空")
		}
		if node.Name == "" {
			result.AddError(prefix+".name", "节点名称不能为空")
		}
		if node.DomainID == "" {
			result.AddError(prefix+".domain_id", "节点必须归属一个 Domain")
		}

		// Role 校验
		validRoles := map[string]bool{"peer": true, "router": true, "relay": true, "gateway": true}
		if node.Role == "" {
			result.AddError(prefix+".role", "节点角色不能为空")
		} else if !validRoles[node.Role] {
			result.AddError(prefix+".role",
				fmt.Sprintf("无效的节点角色: %s, 允许值: peer, router, relay, gateway", node.Role))
		}

		// Platform 校验（可选，但若填则校验）
		if node.Platform != "" {
			validPlatforms := map[string]bool{"debian": true, "ubuntu": true}
			if !validPlatforms[strings.ToLower(node.Platform)] {
				result.AddWarning(prefix+".platform",
					fmt.Sprintf("不支持的平台: %s, 建议: debian, ubuntu", node.Platform))
			}
		}

		// OverlayIP 校验（若手动填写）
		if node.OverlayIP != "" {
			if net.ParseIP(node.OverlayIP) == nil {
				result.AddError(prefix+".overlay_ip",
					fmt.Sprintf("无效的 IP 地址: %s", node.OverlayIP))
			}
		}

		// ListenPort 校验
		if node.ListenPort < 0 || node.ListenPort > 65535 {
			result.AddError(prefix+".listen_port",
				fmt.Sprintf("端口号超出范围: %d", node.ListenPort))
		}
	}
}

func validateEdgesSchema(topo *model.Topology, result *ValidationResult) {
	for i, edge := range topo.Edges {
		prefix := fmt.Sprintf("edges[%d]", i)

		if edge.ID == "" {
			result.AddError(prefix+".id", "Edge ID 不能为空")
		}
		if edge.FromNodeID == "" {
			result.AddError(prefix+".from_node_id", "起始节点 ID 不能为空")
		}
		if edge.ToNodeID == "" {
			result.AddError(prefix+".to_node_id", "目标节点 ID 不能为空")
		}

		// Type 校验
		validTypes := map[string]bool{"direct": true, "public-endpoint": true, "relay-path": true, "candidate": true}
		if edge.Type == "" {
			result.AddError(prefix+".type", "连接类型不能为空")
		} else if !validTypes[edge.Type] {
			result.AddError(prefix+".type",
				fmt.Sprintf("无效的连接类型: %s", edge.Type))
		}

		// Transport 校验
		if edge.Transport != "" {
			validTransports := map[string]bool{"udp": true, "tcp": true}
			if !validTransports[edge.Transport] {
				result.AddError(prefix+".transport",
					fmt.Sprintf("无效的传输协议: %s, 允许值: udp, tcp", edge.Transport))
			}
		}

		// EndpointPort 校验
		if edge.EndpointPort < 0 || edge.EndpointPort > 65535 {
			result.AddError(prefix+".endpoint_port",
				fmt.Sprintf("端口号超出范围: %d", edge.EndpointPort))
		}

		// 自引用检查
		if edge.FromNodeID != "" && edge.FromNodeID == edge.ToNodeID {
			result.AddError(prefix, "边不能自引用（起始和目标节点相同）")
		}
	}
}
