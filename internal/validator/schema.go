package validator

import (
	"fmt"
	"net"
	"regexp"
	"strings"

	"github.com/kunorikiku/yet-another-overlay-generator/internal/model"
)

// nodeNameCharset 约束节点名称的合法字符集（D15 的纵深防御）。
// 节点名称会被派生为 WireGuard 接口名，并被插值进以 root 身份执行的安装脚本，
// 因此必须排除引号、反引号、美元符、分号等 shell 元字符以杜绝命令注入。
// 仅允许：字母、数字、空格、点、下划线、连字符。
var nodeNameCharset = regexp.MustCompile(`^[A-Za-z0-9 ._-]+$`)

// sshFieldCharset 约束 SSH 连接字段（ssh_host / ssh_alias / ssh_user）的合法字符集（D44）。
// 这些字段会被插值进操作员本机执行的 bash 与 PowerShell 部署脚本，
// 因此必须排除空白字符与一切 shell 元字符。仅允许：字母、数字、点、下划线、冒号、@、连字符。
var sshFieldCharset = regexp.MustCompile(`^[A-Za-z0-9._:@-]+$`)

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

	for i := range topo.Domains {
		// 通过下标取指针访问，确保对 RoutingMode 等字段的归一写回能持久化进拓扑对象
		// （range 出的副本写回不会生效，见 Spec C 的 round-trip 要求）。
		domain := &topo.Domains[i]
		prefix := fmt.Sprintf("domains[%d]", i)

		if domain.ID == "" {
			result.AddError(prefix+".id", "Domain ID ")
		}
		if domain.Name == "" {
			result.AddError(prefix+".name", "Domain ")
		}

		// CIDR 格式校验
		if domain.CIDR == "" {
			result.AddError(prefix+".cidr", "CIDR 不能为空")
		} else {
			_, ipNet, err := net.ParseCIDR(domain.CIDR)
			if err != nil {
				result.AddError(prefix+".cidr", fmt.Sprintf("CIDR 格式无效: %s", domain.CIDR))
			} else if ipNet.IP.To4() == nil {
				// IPv4-only：分配器仅支持 IPv4，IPv6/其他地址族会使分配器崩溃
				result.AddError(prefix+".cidr",
					fmt.Sprintf("CIDR 必须为 IPv4 网段: %s（暂不支持 IPv6 及其他地址族）", domain.CIDR))
			} else {
				// CIDR 大小下限：前缀短于 /8 的网段过大，无法枚举分配
				ones, _ := ipNet.Mask.Size()
				if ones < 8 {
					result.AddError(prefix+".cidr",
						fmt.Sprintf("CIDR %s 过大，前缀长度不能小于 /8（无法枚举分配）", domain.CIDR))
				}
			}
		}

		// AllocationMode 
		validAllocModes := map[string]bool{"auto": true, "manual": true}
		if domain.AllocationMode != "" && !validAllocModes[domain.AllocationMode] {
			result.AddError(prefix+".allocation_mode",
				fmt.Sprintf(": %s, : auto, manual", domain.AllocationMode))
		}

		// RoutingMode 归一化与校验（D2/D72，Spec C：docs/spec/compiler/routing-modes.md）。
		// 先将空值归一为 babel 并写回拓扑对象，使其能 round-trip（编译结果与持久化拓扑都
		// 显式携带 babel），消除「空 routing_mode 静默关闭路由守护进程却编译成功」的失败模式。
		// 枚举校验必须在归一之后执行，空值才无法绕过它。
		if domain.RoutingMode == "" {
			domain.RoutingMode = "babel"
		}
		// babel 是当前唯一实现的路由模式；static 与 none 为保留值，尚未实现路由安装器，
		// 直接拒绝而非渲染出零路由的死 overlay。
		switch domain.RoutingMode {
		case "babel":
			// 唯一实现的模式，放行。
		case "static", "none":
			result.AddError(prefix+".routing_mode",
				fmt.Sprintf("路由模式 %s 尚未实现：当前仅支持 babel（唯一已实现的路由模式）", domain.RoutingMode))
		default:
			result.AddError(prefix+".routing_mode",
				fmt.Sprintf("路由模式无效: %s，当前仅支持 babel（唯一已实现的路由模式）", domain.RoutingMode))
		}

		// ReservedRanges 校验：每项需为可解析的 CIDR 或 IP，且必须为 IPv4
		for j, rr := range domain.ReservedRanges {
			rrPrefix := fmt.Sprintf("%s.reserved_ranges[%d]", prefix, j)
			_, rNet, err := net.ParseCIDR(rr)
			if err == nil {
				// 解析为 CIDR：要求 IPv4 地址族
				if rNet.IP.To4() == nil {
					result.AddError(rrPrefix,
						fmt.Sprintf("保留网段必须为 IPv4: %s（暂不支持 IPv6 及其他地址族）", rr))
				}
				continue
			}
			// 退化为单个 IP：要求可解析且为 IPv4
			ip := net.ParseIP(rr)
			if ip == nil {
				result.AddError(rrPrefix,
					fmt.Sprintf("保留范围格式无效: %s", rr))
			} else if ip.To4() == nil {
				result.AddError(rrPrefix,
					fmt.Sprintf("保留地址必须为 IPv4: %s（暂不支持 IPv6 及其他地址族）", rr))
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
		} else if !nodeNameCharset.MatchString(node.Name) {
			// 节点名称字符集校验（D15 纵深防御）：名称会派生 WireGuard 接口名，
			// 并被插值进以 root 身份执行的安装脚本，禁止引号、反引号、$、; 等 shell 元字符。
			result.AddError(prefix+".name",
				fmt.Sprintf("节点名称 %q 含有非法字符：仅允许字母、数字、空格、点(.)、下划线(_)、连字符(-)，禁止引号、反引号、$、; 等 shell 元字符", node.Name))
		}
		if node.DomainID == "" {
			result.AddError(prefix+".domain_id", " Domain")
		}

		// Role 
		validRoles := map[string]bool{"peer": true, "router": true, "relay": true, "gateway": true, "client": true}
		if node.Role == "" {
			result.AddError(prefix+".role", "角色不能为空")
		} else if !validRoles[node.Role] {
			result.AddError(prefix+".role",
				fmt.Sprintf("角色无效: %s，可选值: peer, router, relay, gateway, client", node.Role))
		}

		// Platform （，）
		if node.Platform != "" {
			validPlatforms := map[string]bool{"debian": true, "ubuntu": true}
			if !validPlatforms[strings.ToLower(node.Platform)] {
				result.AddWarning(prefix+".platform",
					fmt.Sprintf("平台不受支持: %s，可选值: debian, ubuntu", node.Platform))
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

		// SSH 字段字符集校验（D44）：非空时各字段都会被插值进操作员本机执行的
		// bash 与 PowerShell 部署脚本，必须排除空白与一切 shell 元字符。
		if node.SSHHost != "" && !sshFieldCharset.MatchString(node.SSHHost) {
			result.AddError(prefix+".ssh_host",
				fmt.Sprintf("ssh_host %q 含有非法字符：仅允许字母、数字、点(.)、下划线(_)、冒号(:)、@、连字符(-)，禁止空白与 shell 元字符", node.SSHHost))
		}
		if node.SSHAlias != "" && !sshFieldCharset.MatchString(node.SSHAlias) {
			result.AddError(prefix+".ssh_alias",
				fmt.Sprintf("ssh_alias %q 含有非法字符：仅允许字母、数字、点(.)、下划线(_)、冒号(:)、@、连字符(-)，禁止空白与 shell 元字符", node.SSHAlias))
		}
		if node.SSHUser != "" && !sshFieldCharset.MatchString(node.SSHUser) {
			result.AddError(prefix+".ssh_user",
				fmt.Sprintf("ssh_user %q 含有非法字符：仅允许字母、数字、点(.)、下划线(_)、冒号(:)、@、连字符(-)，禁止空白与 shell 元字符", node.SSHUser))
		}
	}
}

func validateEdgesSchema(topo *model.Topology, result *ValidationResult) {
	for i := range topo.Edges {
		// 通过下标取指针访问，确保对 Transport 等字段的归一写回能持久化进拓扑对象。
		edge := &topo.Edges[i]
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

		// Transport 归一化与校验（D72，Spec C）。
		// 先将空值归一为 udp 并写回拓扑对象，再做枚举校验——与 routing_mode 同样的归一模式，
		// 使枚举校验在归一之后执行。
		if edge.Transport == "" {
			edge.Transport = "udp"
		}
		validTransports := map[string]bool{"udp": true, "tcp": true}
		if !validTransports[edge.Transport] {
			result.AddError(prefix+".transport",
				fmt.Sprintf("传输协议无效: %s，可选值: udp, tcp", edge.Transport))
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
