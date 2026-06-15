package validator

import (
	"fmt"
	"net"
	"regexp"
	"strconv"
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

// sshKeyPathCharset constrains ssh_key_path. Like ssh_host/alias/user it is
// spliced into the operator's bash + PowerShell deploy scripts (ssh/scp -i
// <path>), so it must exclude every shell metacharacter that could break out of
// quoting. But unlike those connection fields it is a filesystem PATH, so it
// additionally permits the path characters a real key path needs: forward and
// back slashes, a leading ~, a Windows drive colon, and spaces (e.g.
// `C:\Users\John Doe\.ssh\id_ed25519`). Everything dangerous — $ ` " ' ; | & <
// > ( ) etc. — is excluded. This is the validation half of the ssh_key_path
// injection fix; the renderer's bashSingleQuote/powerShellArgQuote escaping is
// the defence-in-depth runtime half.
var sshKeyPathCharset = regexp.MustCompile(`^[A-Za-z0-9._:@/\\~ -]+$`)

// endpointHostCharset constrains edge endpoint_host and node public_endpoints[].host (plan-6).
// These hosts are rendered into the per-peer WireGuard config FILE that root's wg-quick parses
// (the `Endpoint = <host>:<port>` line), so the charset admits exactly what a WireGuard endpoint
// host can be — hostnames, IPv4, and bracketed IPv6 (letters, digits, dot, underscore, colon,
// square brackets, hyphen) — and forbids whitespace and control/metacharacters that would
// corrupt the config or confuse the parser. (It is NOT spliced onto a root shell command line —
// the host never reaches the install script's shell; this is config-integrity defense-in-depth.)
var endpointHostCharset = regexp.MustCompile(`^[A-Za-z0-9._:\[\]-]+$`)

// routerIDMAC48 约束 Babel router-id 的 MAC-48 形式（D66）：六组以冒号分隔的十六进制对，
// 如 02:11:22:33:44:55。babeld 也接受 IPv4 形式的 router-id，因此 IPv4 形式由 net.ParseIP
// 单独判定（见 validateNodesSchema），二者满足其一即合法。
var routerIDMAC48 = regexp.MustCompile(`^([0-9A-Fa-f]{2}:){5}[0-9A-Fa-f]{2}$`)

const (
	// mtuMinimum 是 WireGuard 接口 MTU 的实用下限：576 为 IPv4 数据报必须支持的最小重组缓冲，
	// 低于此值 wg-quick 会拒绝接口（生成无法部署的配置）。D64。
	mtuMinimum = 576
	// mtuMaximum 是 MTU 的理论上限（16 位无符号字段）。D64。
	mtuMaximum = 65535
)

// ValidateSchema  Schema （ Pass 1）
// 、、CIDR
// Topology size bounds (plan-6 item 6): a DoS guard DISTINCT from the HTTP body-size cap.
// They reject obviously-abusive topologies before the per-entity loops and the O(n²)
// semantic pass (IP-collision/NAT-reachability) ever run on attacker-controlled bulk. The
// ceilings are far above any realistic overlay (hundreds of nodes) — they stop "a million
// nodes", not a power user.
const (
	maxTopologyNodes = 2000
	maxTopologyEdges = 10000
)

// topologyExceedsBounds reports whether a topology must be rejected at the root before any
// further validation: it is too large to process safely (count bound) OR is stamped with an
// allocation-schema version newer than this build understands (forward-compat fail-closed,
// plan-6 item 7 — a newer YAOG may use a pin format we would misread as v1). Both
// ValidateSchema and ValidateSemantic short-circuit on it so neither the per-entity loops
// nor the O(n²) semantic checks touch abusive bulk or a future-format topology.
func topologyExceedsBounds(topo *model.Topology) bool {
	return topo.AllocSchemaVersion > model.CurrentAllocSchemaVersion ||
		len(topo.Nodes) > maxTopologyNodes ||
		len(topo.Edges) > maxTopologyEdges
}

func ValidateSchema(topo *model.Topology) *ValidationResult {
	result := &ValidationResult{}

	// Topology-root guards reported HERE (schema is the canonical reporter) and
	// short-circuiting: an oversized or future-format topology is rejected outright rather
	// than merged into a pile of misleading downstream errors, and the expensive passes
	// never run on it. ValidateSemantic guards on the same predicate without re-reporting.
	if topologyExceedsBounds(topo) {
		if topo.AllocSchemaVersion > model.CurrentAllocSchemaVersion {
			result.AddError("alloc_schema_version", CodeTopologySchemaVersionUnsupported,
				P{"version", strconv.Itoa(topo.AllocSchemaVersion)}, P{"max", strconv.Itoa(model.CurrentAllocSchemaVersion)})
		}
		if len(topo.Nodes) > maxTopologyNodes {
			result.AddError("nodes", CodeTopologyTooManyNodes,
				P{"count", strconv.Itoa(len(topo.Nodes))}, P{"max", strconv.Itoa(maxTopologyNodes)})
		}
		if len(topo.Edges) > maxTopologyEdges {
			result.AddError("edges", CodeTopologyTooManyEdges,
				P{"count", strconv.Itoa(len(topo.Edges))}, P{"max", strconv.Itoa(maxTopologyEdges)})
		}
		return result
	}

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
		result.AddError("project.id", CodeProjectIDRequired)
	}
	if topo.Project.Name == "" {
		result.AddError("project.name", CodeProjectNameRequired)
	}
}

func validateDomainsSchema(topo *model.Topology, result *ValidationResult) {
	if len(topo.Domains) == 0 {
		result.AddError("domains", CodeDomainNoneDefined)
		return
	}

	for i := range topo.Domains {
		// 通过下标取指针访问，确保对 RoutingMode 等字段的归一写回能持久化进拓扑对象
		// （range 出的副本写回不会生效，见 Spec C 的 round-trip 要求）。
		domain := &topo.Domains[i]
		prefix := fmt.Sprintf("domains[%d]", i)

		if domain.ID == "" {
			result.AddError(prefix+".id", CodeDomainIDRequired)
		}
		if domain.Name == "" {
			result.AddError(prefix+".name", CodeDomainNameRequired)
		}

		// CIDR 格式校验
		if domain.CIDR == "" {
			result.AddError(prefix+".cidr", CodeDomainCIDREmpty)
		} else {
			_, ipNet, err := net.ParseCIDR(domain.CIDR)
			if err != nil {
				result.AddError(prefix+".cidr", CodeDomainCIDRInvalid, P{"cidr", domain.CIDR})
			} else if ipNet.IP.To4() == nil {
				// IPv4-only：分配器仅支持 IPv4，IPv6/其他地址族会使分配器崩溃
				result.AddError(prefix+".cidr", CodeDomainCIDRNotIPv4, P{"cidr", domain.CIDR})
			} else {
				// CIDR 大小下限：前缀短于 /8 的网段过大，无法枚举分配
				ones, _ := ipNet.Mask.Size()
				if ones < 8 {
					result.AddError(prefix+".cidr", CodeDomainCIDRTooLarge, P{"cidr", domain.CIDR})
				}
			}
		}

		// AllocationMode
		validAllocModes := map[string]bool{"auto": true, "manual": true}
		if domain.AllocationMode != "" && !validAllocModes[domain.AllocationMode] {
			result.AddError(prefix+".allocation_mode", CodeDomainAllocationModeInvalid, P{"mode", domain.AllocationMode})
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
			result.AddError(prefix+".routing_mode", CodeDomainRoutingModeUnimplemented, P{"mode", domain.RoutingMode})
		default:
			result.AddError(prefix+".routing_mode", CodeDomainRoutingModeInvalid, P{"mode", domain.RoutingMode})
		}

		// ReservedRanges 校验：每项需为可解析的 CIDR 或 IP，且必须为 IPv4
		for j, rr := range domain.ReservedRanges {
			rrPrefix := fmt.Sprintf("%s.reserved_ranges[%d]", prefix, j)
			_, rNet, err := net.ParseCIDR(rr)
			if err == nil {
				// 解析为 CIDR：要求 IPv4 地址族
				if rNet.IP.To4() == nil {
					result.AddError(rrPrefix, CodeDomainReservedRangeNotIPv4, P{"cidr", rr})
				}
				continue
			}
			// 退化为单个 IP：要求可解析且为 IPv4
			ip := net.ParseIP(rr)
			if ip == nil {
				result.AddError(rrPrefix, CodeDomainReservedRangeInvalid, P{"value", rr})
			} else if ip.To4() == nil {
				result.AddError(rrPrefix, CodeDomainReservedAddressNotIPv4, P{"ip", rr})
			}
		}

		// transit_cidr 校验（plan-6）：可解析、IPv4-only，且大小足以容纳 per-link transit 地址对
		// （每条链路占用一对 transit IP）。镜像 domain CIDR 的 IPv4 + 大小守卫；空值由编译器回退
		// 到默认 10.10.0.0/24，无需校验。
		if domain.TransitCIDR != "" {
			_, tNet, err := net.ParseCIDR(domain.TransitCIDR)
			if err != nil {
				result.AddError(prefix+".transit_cidr", CodeDomainTransitCIDRInvalid, P{"cidr", domain.TransitCIDR})
			} else if tNet.IP.To4() == nil {
				result.AddError(prefix+".transit_cidr", CodeDomainTransitCIDRNotIPv4, P{"cidr", domain.TransitCIDR})
			} else {
				ones, _ := tNet.Mask.Size()
				if ones < 8 {
					result.AddError(prefix+".transit_cidr", CodeDomainTransitCIDRTooLarge, P{"cidr", domain.TransitCIDR})
				} else if ones > 30 {
					result.AddError(prefix+".transit_cidr", CodeDomainTransitCIDRTooSmall, P{"cidr", domain.TransitCIDR})
				}
			}
		}
	}
}

func validateNodesSchema(topo *model.Topology, result *ValidationResult) {
	for i, node := range topo.Nodes {
		prefix := fmt.Sprintf("nodes[%d]", i)

		if node.ID == "" {
			result.AddError(prefix+".id", CodeNodeIDRequired)
		}
		if node.Name == "" {
			result.AddError(prefix+".name", CodeNodeNameRequired)
		} else if !nodeNameCharset.MatchString(node.Name) {
			// 节点名称字符集校验（D15 纵深防御）：名称会派生 WireGuard 接口名，
			// 并被插值进以 root 身份执行的安装脚本，禁止引号、反引号、$、; 等 shell 元字符。
			result.AddError(prefix+".name", CodeNodeNameIllegalChars, P{"name", fmt.Sprintf("%q", node.Name)})
		}
		if node.DomainID == "" {
			result.AddError(prefix+".domain_id", CodeNodeDomainIDRequired)
		}

		// Role
		validRoles := map[string]bool{"peer": true, "router": true, "relay": true, "gateway": true, "client": true}
		if node.Role == "" {
			result.AddError(prefix+".role", CodeNodeRoleEmpty)
		} else if !validRoles[node.Role] {
			result.AddError(prefix+".role", CodeNodeRoleInvalid, P{"role", node.Role})
		}

		// Platform （，）
		if node.Platform != "" {
			validPlatforms := map[string]bool{"debian": true, "ubuntu": true}
			if !validPlatforms[strings.ToLower(node.Platform)] {
				result.AddWarning(prefix+".platform", CodeNodePlatformUnsupported, P{"platform", node.Platform})
			}
		}

		// XDPMode：mimic（transport=="tcp"）的 XDP 附着模式。仅 skb/native 合法；
		// 空等价于 skb（默认通用 XDP）。非法值会被渲染器静默回落到 skb，故在此显式拒绝，
		// 避免 "Native"/"generic" 等拼写被悄悄当成 skb（docs/spec/artifacts/mimic.md）。
		if node.XDPMode != "" {
			validXDPModes := map[string]bool{"skb": true, "native": true}
			if !validXDPModes[node.XDPMode] {
				result.AddError(prefix+".xdp_mode", CodeNodeXDPModeInvalid, P{"mode", node.XDPMode})
			}
		}

		// OverlayIP （）
		if node.OverlayIP != "" {
			if net.ParseIP(node.OverlayIP) == nil {
				result.AddError(prefix+".overlay_ip", CodeNodeOverlayIPInvalid, P{"ip", node.OverlayIP})
			}
		}

		// MTU 校验（D64）：0 表示使用系统默认值（通常 1420），跳过。
		// 非零时必须落在 [576, 65535] 内——低于 576（IPv4 数据报最小重组缓冲）
		// 或高于 65535 的 MTU 会被 wg-quick 拒绝，生成无法部署的 WireGuard 配置。
		if node.MTU != 0 && (node.MTU < mtuMinimum || node.MTU > mtuMaximum) {
			result.AddError(prefix+".mtu", CodeNodeMTUOutOfRange, P{"mtu", strconv.Itoa(node.MTU)}, P{"low", strconv.Itoa(mtuMinimum)}, P{"high", strconv.Itoa(mtuMaximum)})
		}

		// SSHPort 校验（D65）：0 表示使用默认端口 22，跳过。
		// 非零时必须落在 1–65535 内，否则会被插值进无法连接的 SSH 部署命令。
		if node.SSHPort != 0 && (node.SSHPort < 1 || node.SSHPort > 65535) {
			result.AddError(prefix+".ssh_port", CodeNodeSSHPortOutOfRange, P{"port", strconv.Itoa(node.SSHPort)})
		}

		// RouterID 校验（D66）：留空时由编译器自动生成，跳过。
		// 非空时必须为 MAC-48 形式（六组冒号分隔的十六进制对，如 02:11:22:33:44:55）
		// 或可解析为 IPv4 地址——babeld 两种形式都接受；其它取值会被 babeld 拒绝。
		if node.RouterID != "" {
			if !routerIDMAC48.MatchString(node.RouterID) && net.ParseIP(node.RouterID).To4() == nil {
				result.AddError(prefix+".router_id", CodeNodeRouterIDInvalid, P{"id", fmt.Sprintf("%q", node.RouterID)})
			}
		}

		// ExtraPrefixes 校验（D67）：每项必须可解析为 IPv4 CIDR（镜像 reserved_ranges 的 IPv4 守卫风格）。
		// 这些前缀会被宣告进 Babel 路由表；非 IPv4 或非 CIDR 的前缀会生成无法部署的 babeld 配置。
		for j, prefixCIDR := range node.ExtraPrefixes {
			epPrefix := fmt.Sprintf("%s.extra_prefixes[%d]", prefix, j)
			_, epNet, err := net.ParseCIDR(prefixCIDR)
			if err != nil {
				result.AddError(epPrefix, CodeNodeExtraPrefixInvalid, P{"prefix", prefixCIDR})
			} else if epNet.IP.To4() == nil {
				result.AddError(epPrefix, CodeNodeExtraPrefixNotIPv4, P{"prefix", prefixCIDR})
			}
		}

		// SSH 字段字符集校验（D44）：非空时各字段都会被插值进操作员本机执行的
		// bash 与 PowerShell 部署脚本，必须排除空白与一切 shell 元字符。
		if node.SSHHost != "" && !sshFieldCharset.MatchString(node.SSHHost) {
			result.AddError(prefix+".ssh_host", CodeNodeSSHHostIllegalChars, P{"host", fmt.Sprintf("%q", node.SSHHost)})
		}
		if node.SSHAlias != "" && !sshFieldCharset.MatchString(node.SSHAlias) {
			result.AddError(prefix+".ssh_alias", CodeNodeSSHAliasIllegalChars, P{"alias", fmt.Sprintf("%q", node.SSHAlias)})
		}
		if node.SSHUser != "" && !sshFieldCharset.MatchString(node.SSHUser) {
			result.AddError(prefix+".ssh_user", CodeNodeSSHUserIllegalChars, P{"user", fmt.Sprintf("%q", node.SSHUser)})
		}
		// ssh_key_path is also spliced into the operator's deploy shell command
		// (ssh/scp -i <path>); it permits path characters (/ \ ~ : space) the
		// connection fields don't, but still forbids every shell metacharacter so a
		// hostile path like `/k$(reboot).pem` or `k".pem` cannot inject. See
		// sshKeyPathCharset.
		if node.SSHKeyPath != "" && !sshKeyPathCharset.MatchString(node.SSHKeyPath) {
			result.AddError(prefix+".ssh_key_path", CodeNodeSSHKeyPathIllegalChars, P{"path", fmt.Sprintf("%q", node.SSHKeyPath)})
		}

		// public_endpoints[].host 字符集校验（plan-6）：host 会被渲染进 root 的 wg-quick 解析的
		// per-peer WireGuard 配置文件（Endpoint = 行），必须排除空白与控制/元字符以免破坏配置或
		// 混淆解析器。PublicEndpoint.Port 不在此校验：它只是一个节点可达性提示，编译器从不渲染它
		// （反向 endpoint 回退使用分配到的监听端口，见 peers.go），因此只需守 host。
		for k := range node.PublicEndpoints {
			ep := &node.PublicEndpoints[k]
			if ep.Host != "" && !endpointHostCharset.MatchString(ep.Host) {
				result.AddError(fmt.Sprintf("%s.public_endpoints[%d].host", prefix, k), CodeNodePublicEndpointHostIllegalChars, P{"host", fmt.Sprintf("%q", ep.Host)})
			}
		}
	}
}

func validateEdgesSchema(topo *model.Topology, result *ValidationResult) {
	for i := range topo.Edges {
		// 通过下标取指针访问，确保对 Transport 等字段的归一写回能持久化进拓扑对象。
		edge := &topo.Edges[i]
		prefix := fmt.Sprintf("edges[%d]", i)

		if edge.ID == "" {
			result.AddError(prefix+".id", CodeEdgeIDRequired)
		}
		if edge.FromNodeID == "" {
			result.AddError(prefix+".from_node_id", CodeEdgeFromNodeIDRequired)
		}
		if edge.ToNodeID == "" {
			result.AddError(prefix+".to_node_id", CodeEdgeToNodeIDRequired)
		}

		// Type
		validTypes := map[string]bool{"direct": true, "public-endpoint": true, "relay-path": true, "candidate": true}
		if edge.Type == "" {
			result.AddError(prefix+".type", CodeEdgeTypeEmpty)
		} else if !validTypes[edge.Type] {
			result.AddError(prefix+".type", CodeEdgeTypeInvalid, P{"type", edge.Type})
		}

		// Transport 归一化与校验（D72，Spec C）。
		// 先将空值归一为 udp 并写回拓扑对象，再做枚举校验——与 routing_mode 同样的归一模式，
		// 使枚举校验在归一之后执行。
		if edge.Transport == "" {
			edge.Transport = "udp"
		}
		validTransports := map[string]bool{"udp": true, "tcp": true}
		if !validTransports[edge.Transport] {
			result.AddError(prefix+".transport", CodeEdgeTransportInvalid, P{"transport", edge.Transport})
		}
		// tcp 现已实现（mimic eBPF UDP→伪 TCP 封装），是合法取值、不再告警。
		// 「两端必须为可部署 Linux」这一语义约束由 semantic.go 的 validateMimicTransport 负责。

		// EndpointPort
		if edge.EndpointPort < 0 || edge.EndpointPort > 65535 {
			result.AddError(prefix+".endpoint_port", CodeEdgeEndpointPortInvalid, P{"port", strconv.Itoa(edge.EndpointPort)})
		}

		// endpoint_host 字符集校验（plan-6）：非空时会被渲染进 root 的 wg-quick 解析的 per-peer
		// WireGuard 配置文件（Endpoint = 行），必须排除空白与控制/元字符以免破坏配置或混淆解析器。
		if edge.EndpointHost != "" && !endpointHostCharset.MatchString(edge.EndpointHost) {
			result.AddError(prefix+".endpoint_host", CodeEdgeEndpointHostIllegalChars, P{"host", fmt.Sprintf("%q", edge.EndpointHost)})
		}

		// Role 校验（并行链路 / 故障切换）：仅允许空值、"primary"、"backup"。
		// 空值与 "primary" 同属 primary class（同一对节点折叠为一条主链路），
		// "backup" 则每条 edge 各自成为一条独立备份链路。语义见
		// docs/spec/compiler/allocation-stability.md（Link identity with parallel edges）。
		if edge.Role != "" && edge.Role != model.EdgeRolePrimary && edge.Role != model.EdgeRoleBackup {
			result.AddError(prefix+".role", CodeEdgeRoleInvalid, P{"role", edge.Role})
		}

		//
		if edge.FromNodeID != "" && edge.FromNodeID == edge.ToNodeID {
			result.AddError(prefix, CodeEdgeSelfLoop)
		}
	}
}
