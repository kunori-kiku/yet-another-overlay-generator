package validator

import (
	"fmt"
	"net"
	"strconv"
	"strings"

	"github.com/kunorikiku/yet-another-overlay-generator/internal/linkid"
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

	// 生效监听端口范围：每节点 base..base+(对端接口数-1) 是否越界，以及同主机节点范围是否重叠
	validateEffectivePortRanges(topo, result)

	//
	detectIsolatedNodes(topo, result)

	// NAT
	validateNATReachability(topo, nodeMap, result)

	// Client 边验证
	validateClientEdges(topo, nodeMap, result)

	// mimic（tcp 传输）：tcp 边两端必须均为可部署 Linux（eBPF/内核特性）
	validateMimicTransport(topo, nodeMap, result)

	// Edge endpoint 与目标节点 public endpoints 一致性检查
	validateEdgeEndpointConsistency(topo, nodeMap, result)

	// 同一节点对的重复启用边检测（编译器只取首条，后续边的 endpoint 覆盖会被静默丢弃）
	detectDuplicateEnabledEdges(topo, result)

	// 并行链路：每节点 WireGuard 接口名唯一性（不变式 N4）
	validateInterfaceNameUniqueness(topo, nodeMap, result)

	// 并行链路：同一对节点至多一条显式 primary 边
	validateSinglePrimaryPerPair(topo, nodeMap, result)

	// 并行链路：client 边不得为 backup（client 用单一 wg0，不参与并行链路）
	validateBackupClientEdges(topo, nodeMap, result)

	// 并行链路：多链路节点对的等代价告警、无 primary 告警
	validateParallelLinkCosts(topo, nodeMap, result)

	// 分配 pin 校验：pin 在被预留之前必须先校验（不变式 I7）
	validateAllocationPins(topo, domainMap, nodeMap, result)

	// route_policies 为保留特性：非空即拒绝（Decisions log #2，Spec E）
	validateRoutePoliciesReserved(topo, result)

	return result
}

// validateRoutePoliciesReserved 拒绝任何非空的 route_policies（D10/D37/D62，Spec E）。
// route_policies 在 Go 与 TS 两侧均有声明，却没有任何 renderer 消费、也没有编辑器入口，
// 编译器仅原样透传（compiler.go）。按绑定决策（Decisions log #2）它是「为未来主题保留」的
// 特性，而非可用功能：携带非空 route_policies 的拓扑会编译出一份与用户意图不符、却看不出
// 任何路由策略生效的死配置。因此语义校验在此直接报错，要求该数组必须为空。
// LAN 桥接 / 路由注入这一用例由 extra_prefixes 与路由层承载，而非 route_policies。
func validateRoutePoliciesReserved(topo *model.Topology, result *ValidationResult) {
	if len(topo.RoutePolicies) > 0 {
		result.AddError("route_policies", CodeRoutePolicyReserved, P{"count", strconv.Itoa(len(topo.RoutePolicies))})
	}
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
				result.AddError(fmt.Sprintf("nodes[%d].domain_id", i), CodeNodeDomainRefMissing, P{"node", node.Name}, P{"id", node.DomainID})
			}
		}
	}
}

func validateEdgeNodeRefs(topo *model.Topology, nodeMap map[string]*model.Node, result *ValidationResult) {
	for i, edge := range topo.Edges {
		prefix := fmt.Sprintf("edges[%d]", i)
		if edge.FromNodeID != "" {
			if _, ok := nodeMap[edge.FromNodeID]; !ok {
				result.AddError(prefix+".from_node_id", CodeEdgeNodeRefMissing, P{"id", edge.ID}, P{"other", edge.FromNodeID})
			}
		}
		if edge.ToNodeID != "" {
			if _, ok := nodeMap[edge.ToNodeID]; !ok {
				result.AddError(prefix+".to_node_id", CodeEdgeNodeRefMissing, P{"id", edge.ID}, P{"other", edge.ToNodeID})
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
				result.AddError(prefix, CodeNodeOverlayIPOutOfCIDR, P{"node", node.Name}, P{"cidr", node.OverlayIP}, P{"name", domain.Name}, P{"prefix", domain.CIDR})
			}
		}

		//  IP
		if existingNode, exists := ipUsage[node.OverlayIP]; exists {
			result.AddError(prefix, CodeNodeOverlayIPConflict, P{"cidr", node.OverlayIP}, P{"other", existingNode}, P{"node", node.Name})
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
			result.AddError(fmt.Sprintf("domains[%d].id", i), CodeDomainIDDuplicate, P{"id", d.ID})
		}
		domainIDs[d.ID] = true
	}

	// Node ID
	nodeIDs := make(map[string]bool)
	for i, n := range topo.Nodes {
		if nodeIDs[n.ID] {
			result.AddError(fmt.Sprintf("nodes[%d].id", i), CodeNodeIDDuplicate, P{"id", n.ID})
		}
		nodeIDs[n.ID] = true
	}

	// Edge ID
	edgeIDs := make(map[string]bool)
	for i, e := range topo.Edges {
		if edgeIDs[e.ID] {
			result.AddError(fmt.Sprintf("edges[%d].id", i), CodeEdgeIDDuplicate, P{"id", e.ID})
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
			result.AddError(prefix, CodeNodeNameDuplicate, P{"other", firstNode}, P{"node", node.Name}, P{"name", fmt.Sprintf("%q", node.Name)})
		} else {
			rawNames[node.Name] = node.Name
		}

		// N2：安装脚本文件名冲突（例如 "Web 1" 与 "web-1" 都归一为 web-1.install.sh）。
		installerName := naming.SafeInstallerFileName(node.Name)
		if firstNode, exists := installerNames[installerName]; exists {
			if firstNode != node.Name {
				result.AddError(prefix, CodeNodeNameInstallerCollision, P{"other", firstNode}, P{"node", node.Name}, P{"name", fmt.Sprintf("%q", installerName)})
			}
		} else {
			installerNames[installerName] = node.Name
		}

		// N3：WireGuard 接口名冲突（例如 "db.east" 与 "db-east" 都归一为 wg-db-east）。
		interfaceName := naming.WgInterfaceName(node.Name)
		if firstNode, exists := interfaceNames[interfaceName]; exists {
			if firstNode != node.Name {
				result.AddError(prefix, CodeNodeNameInterfaceCollision, P{"other", firstNode}, P{"node", node.Name}, P{"name", fmt.Sprintf("%q", interfaceName)})
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
			result.AddWarning(fmt.Sprintf("nodes[%d].listen_port", i), CodeNodeListenPortHostConflict, P{"node", node.Name}, P{"other", existingNode}, P{"name", node.Hostname}, P{"port", strconv.Itoa(node.ListenPort)})
		} else {
			seen[hp] = node.Name
		}
	}
}

// defaultListenPort 是节点未显式设置 listen_port 时编译器采用的基准端口，
// 必须与 peers.go Pass 1 中的默认值（51820）保持一致。
const defaultListenPort = 51820

// effectivePortRange 描述一个节点在 per-peer 接口模型下实际占用的监听端口范围。
//
//	[base, base+count-1]
//
// 其中 base 为节点的基准 listen_port（未设置时取 defaultListenPort），
// count 为该节点作为「非 client 端点」参与的去重链路数量（并行链路下，同一对节点的
// primary class 折叠为一条链路、每条 backup 各为一条链路）——
// 这正是编译器为它分配的 WireGuard 接口个数。
type effectivePortRange struct {
	nodeIndex int    // 节点在 topo.Nodes 中的下标，用于定位错误字段
	nodeName  string // 节点名称，用于错误消息
	hostname  string // 节点 hostname（可能为空）
	base      int    // 基准监听端口
	count     int    // 该节点占用的接口数（= 去重链路数）
}

// high 返回该节点占用的最高监听端口（base + count - 1）。
func (r effectivePortRange) high() int {
	return r.base + r.count - 1
}

// validateEffectivePortRanges 校验 per-peer 接口模型下每个节点的「生效监听端口范围」
// （D47 + D11 的一部分）。
//
// 编译器为每条链路的每个非 client 端点分配一个独立 WireGuard 接口，监听端口从
// 节点基准端口起按 base+offset 递增（见 peers.go Pass 1 中 nodePortOffset 的逻辑）。
// 接口数按「链路」而非「节点对」统计，与编译器的 unify 规则一致（并行链路）：
//   - 仅统计启用且两端节点均存在的边；
//   - 以 linkid.LinkKey 去重——同一对节点的 primary class（Role != backup）折叠为一条链路，
//     每条 backup 边各自成为一条独立链路；
//   - 因此一对节点若有 1 条 primary 链路 + 2 条 backup，会为其两端各贡献 3 个接口；
//   - 每条链路为其两端中的「非 client」端点各 +1。
//
// 计算出每个节点的占用区间 [base, base+count-1] 后：
//  1. 当区间最高端口超过 65535 时报错（D11：base+offset 越界会被原样渲染进 WireGuard 配置）。
//  2. 当两个共享同一非空 hostname 的节点区间发生重叠时报错（D47：今日仅对完全相同的 base 端口告警，
//     无法发现共置节点的范围交叠）。
func validateEffectivePortRanges(topo *model.Topology, result *ValidationResult) {
	// 节点索引（与 peers.go 一致：以 ID 查找）。
	nodeMap := make(map[string]*model.Node)
	nodeIndex := make(map[string]int)
	for i := range topo.Nodes {
		nodeMap[topo.Nodes[i].ID] = &topo.Nodes[i]
		nodeIndex[topo.Nodes[i].ID] = i
	}

	// 镜像 peers.go Pass 1 的 unify 分组：以 linkKey 去重，为每个非 client 端点累计接口数。
	seenLinks := make(map[string]bool)
	interfaceCount := make(map[string]int) // nodeID -> 接口数（去重链路数）

	for i := range topo.Edges {
		edge := &topo.Edges[i]
		if !edge.IsEnabled {
			continue
		}

		fromNode := nodeMap[edge.FromNodeID]
		toNode := nodeMap[edge.ToNodeID]
		if fromNode == nil || toNode == nil {
			continue
		}

		// 链路键：primary class 的正反边与同向多余 primary 边共享同一 linkKey（折叠为一条链路）；
		// 每条 backup 边携带自身 edge.ID，各自成为一条独立链路。
		lk := linkid.LinkKey(edge)
		if seenLinks[lk] {
			continue
		}
		seenLinks[lk] = true

		// client 节点使用单一 wg0，不参与 per-peer 端口分配（与 peers.go 的
		// isFromClient / isToClient 守卫一致）。
		if fromNode.Role != "client" {
			interfaceCount[fromNode.ID]++
		}
		if toNode.Role != "client" {
			interfaceCount[toNode.ID]++
		}
	}

	// 为占用了至少一个接口的节点构建生效端口范围。
	var ranges []effectivePortRange
	for _, node := range topo.Nodes {
		count := interfaceCount[node.ID]
		if count == 0 {
			// 没有 per-peer 接口（无启用边，或为 client 节点）：无生效范围可校验。
			continue
		}
		base := node.ListenPort
		if base == 0 {
			base = defaultListenPort
		}
		r := effectivePortRange{
			nodeIndex: nodeIndex[node.ID],
			nodeName:  node.Name,
			hostname:  node.Hostname,
			base:      base,
			count:     count,
		}
		ranges = append(ranges, r)

		// 规则 1：生效范围最高端口越界。
		if r.high() > 65535 {
			result.AddError(fmt.Sprintf("nodes[%d].listen_port", r.nodeIndex), CodeNodeEffectivePortRangeOverflow, P{"node", r.nodeName}, P{"low", strconv.Itoa(r.base)}, P{"high", strconv.Itoa(r.high())}, P{"base", strconv.Itoa(r.base)}, P{"count", strconv.Itoa(r.count)})
		}
	}

	// 规则 2：共享同一非空 hostname 的节点之间，生效范围不得重叠。
	// 两两比较（节点数量很小），仅对 hostname 非空且相同的节点对生效。
	for a := 0; a < len(ranges); a++ {
		for b := a + 1; b < len(ranges); b++ {
			ra := ranges[a]
			rb := ranges[b]
			if ra.hostname == "" || ra.hostname != rb.hostname {
				continue
			}
			// 区间重叠判定：max(low) <= min(high)。
			if ra.base <= rb.high() && rb.base <= ra.high() {
				// 在下标较大的节点上报错，便于测试与定位。
				later := rb
				earlier := ra
				if ra.nodeIndex > rb.nodeIndex {
					later = ra
					earlier = rb
				}
				result.AddError(fmt.Sprintf("nodes[%d].listen_port", later.nodeIndex), CodeNodeEffectivePortRangeOverlap, P{"node", earlier.nodeName}, P{"low", strconv.Itoa(earlier.base)}, P{"high", strconv.Itoa(earlier.high())}, P{"other", later.nodeName}, P{"other_low", strconv.Itoa(later.base)}, P{"other_high", strconv.Itoa(later.high())}, P{"name", later.hostname})
			}
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
			result.AddWarning("topology", CodeNodeIsolated, P{"node", node.Name}, P{"id", node.ID})
		}
	}
}

// validateClientEdges 验证 client 节点的边约束
func validateClientEdges(topo *model.Topology, nodeMap map[string]*model.Node, result *ValidationResult) {
	// 收集每个 client 的出站和入站 edge 数量
	clientOutbound := make(map[string]int)        // nodeID -> count of enabled outbound edges
	clientOutboundEdges := make(map[string][]int) // nodeID -> edge indices

	for i, edge := range topo.Edges {
		if !edge.IsEnabled {
			continue
		}

		fromNode := nodeMap[edge.FromNodeID]
		toNode := nodeMap[edge.ToNodeID]

		// 拒绝以 client 为目标的入站边
		if toNode != nil && toNode.Role == "client" {
			result.AddError(fmt.Sprintf("edges[%d]", i), CodeClientInboundRejected, P{"node", toNode.Name})
		}

		// 统计 client 出站边
		if fromNode != nil && fromNode.Role == "client" {
			clientOutbound[fromNode.ID]++
			clientOutboundEdges[fromNode.ID] = append(clientOutboundEdges[fromNode.ID], i)

			// Client 的目标必须是 router/relay/gateway（不能是 peer 或 client）
			if toNode != nil {
				if toNode.Role == "peer" {
					result.AddError(fmt.Sprintf("edges[%d]", i), CodeClientTargetPeer, P{"node", fromNode.Name}, P{"other", toNode.Name})
				}
				if toNode.Role == "client" {
					result.AddError(fmt.Sprintf("edges[%d]", i), CodeClientTargetClient, P{"node", fromNode.Name}, P{"other", toNode.Name})
				}
			}

			// Client 边必须有 endpoint_host
			if edge.EndpointHost == "" {
				result.AddError(fmt.Sprintf("edges[%d].endpoint_host", i), CodeClientEndpointHostRequired, P{"node", fromNode.Name})
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
			result.AddError("topology", CodeClientNoOutboundEdge, P{"node", node.Name})
		} else if count > 1 {
			result.AddError("topology", CodeClientMultipleOutboundEdges, P{"node", node.Name}, P{"count", strconv.Itoa(count)})
		}

		// 警告：client 设置了无意义的字段
		if node.RouterID != "" {
			result.AddWarning(fmt.Sprintf("node.%s.router_id", node.ID), CodeClientRouterIDMeaningless, P{"node", node.Name})
		}
		if len(node.ExtraPrefixes) > 0 {
			result.AddWarning(fmt.Sprintf("node.%s.extra_prefixes", node.ID), CodeClientExtraPrefixesMeaningless, P{"node", node.Name})
		}
	}
}

// mimicLinuxDeployable 判定某节点的平台是否可部署 mimic（eBPF/内核特性）。
// mimic 仅在 Linux 上可用，YAOG 当前支持的 Linux 发行版为 debian / ubuntu。
// 空 platform 视为 Linux（放行）：与 schema.go validateNodesSchema 中「空 platform 跳过校验」
// 的处理一致，避免对未设置 platform 的节点产生误报。
func mimicLinuxDeployable(node *model.Node) bool {
	if node == nil {
		// 节点缺失由 validateEdgeNodeRefs 报错，这里不重复，按放行处理。
		return true
	}
	if node.Platform == "" {
		return true
	}
	switch strings.ToLower(node.Platform) {
	case "debian", "ubuntu":
		return true
	default:
		return false
	}
}

// validateMimicTransport 校验 transport=="tcp"（mimic）边的平台约束（Spec：docs/spec/artifacts/mimic.md、
// docs/spec/data-model/edge.md §TCP transport、docs/spec/compiler/validation.md mimic 规则）。
//
// mimic 是 eBPF/内核特性，仅能在可部署的 Linux（debian / ubuntu）上运行；因此一条 tcp 边的
// 两个端点节点都必须是可部署 Linux，否则编译出的配置在该端点上无法部署。任一端点平台不被支持
// 即报错，并在错误消息中点名该边与违规节点。
//
// 内核/eBPF 的实际可用性是安装期检查（见 mimic.md），不在编译期报错；mimic 无密钥，无需做任何
// 密钥校验。空 platform 视为 Linux（放行），与其它平台校验对空值的处理一致。
func validateMimicTransport(topo *model.Topology, nodeMap map[string]*model.Node, result *ValidationResult) {
	for i := range topo.Edges {
		edge := &topo.Edges[i]
		if !edge.IsEnabled {
			continue
		}
		if edge.Transport != "tcp" {
			continue
		}

		fromNode := nodeMap[edge.FromNodeID]
		toNode := nodeMap[edge.ToNodeID]

		if !mimicLinuxDeployable(fromNode) {
			result.AddError(fmt.Sprintf("edges[%d].transport", i), CodeEdgeMimicPlatformUnsupported, P{"id", edge.ID}, P{"node", fromNode.Name}, P{"platform", fmt.Sprintf("%q", fromNode.Platform)})
		}
		if !mimicLinuxDeployable(toNode) {
			result.AddError(fmt.Sprintf("edges[%d].transport", i), CodeEdgeMimicPlatformUnsupported, P{"id", edge.ID}, P{"node", toNode.Name}, P{"platform", fmt.Sprintf("%q", toNode.Platform)})
		}
	}
}

// defaultTransitCIDR 是域未显式配置 transit_cidr 时回退使用的默认 transit 地址池，
// 必须与 compiler/peers.go 的同名常量保持一致——pin 的 out-of-CIDR 校验要用编译器实际
// 解析出的池来判定，二者若不一致会让校验放行编译器随后拒绝（或反之）的 pin。
const defaultTransitCIDR = "10.10.0.0/24"

// edgeTransitCIDR 解析一条边实际使用的 transit 地址池。
// 与 compiler/peers.go Pass 1 的解析规则一致：取 from 节点所属 domain 的 transit_cidr，
// 留空时回退默认 10.10.0.0/24。
func edgeTransitCIDR(edge model.Edge, domainMap map[string]*model.Domain, nodeMap map[string]*model.Node) string {
	fromNode := nodeMap[edge.FromNodeID]
	if fromNode == nil {
		return defaultTransitCIDR
	}
	if domain := domainMap[fromNode.DomainID]; domain != nil && domain.TransitCIDR != "" {
		return domain.TransitCIDR
	}
	return defaultTransitCIDR
}

// 链路规范键与链路键由 internal/linkid 提供（linkid.PinKey / linkid.LinkKey），
// 编译器与验证器共用同一份语义，避免重复字面量。
//   - linkid.PinKey(a,b)：两端节点 ID 的无序对（字符串 min|max），方向无关；
//     一条边与其反向边、同一对节点的所有 primary class 边都落在同一个 PinKey 上。
//   - linkid.LinkKey(edge)：primary class 边等于 PinKey（正反边、同向多余 primary 边共享一个键）；
//     backup 边为 PinKey + "#" + edge.ID，每条 backup 各自成为一条独立链路。

// nodePortPin 描述某个节点在某条链路上被钉住的监听端口，用于跨链路去重时定位冲突。
type nodePortPin struct {
	port   int
	linkID string // 首个声明该 (节点, 端口) 的链路键 linkKey
	edge   string // 首个声明该 (节点, 端口) 的边 ID，用于错误消息
}

// pinOwner 记录某个被钉住的地址（transit IP 或 link-local）的首个占用者：
// linkID 用于让同一链路的正反边互不冲突，edge 用于在错误消息中点名首个占用边。
type pinOwner struct {
	linkID string
	edge   string
}

// validateAllocationPins 校验边上的分配 pin（不变式 I7，pin 校验规则见
// docs/spec/compiler/allocation-stability.md「Pin validation」表）。
// 每条规则的违例都是阻断编译的错误（而非告警），且必须在任何资源被预留之前完成。
//
// pin 按边存储，并由「该边自身的 from/to」定向：边 A->B 的 PinnedFromPort 是 A 侧端口，
// PinnedToPort 是 B 侧端口；反向边 B->A 携带同一对值的镜像（其 PinnedFromPort 即 B 侧端口）。
// 因此本函数：
//   - 结构性规则（部分 pin、端口越界、transit 越池、client 边端口 pin）逐边校验，作用于该边自身；
//   - 去重规则（同一节点端口、同一 transit IP、同一 link-local 被两条不同链路占用）按 linkKey
//     归并后跨链路比较：primary class 的正反边共享同一 linkKey 不算冲突，
//     而 backup 边各有独立 linkKey，与 primary 链路的 pin 碰撞会被如实标记。
func validateAllocationPins(topo *model.Topology, domainMap map[string]*model.Domain, nodeMap map[string]*model.Node, result *ValidationResult) {
	// 去重表：跨「不同链路」检测同一资源被重复钉住。
	//   - 节点端口：键为 nodeID，值记录首个占用该端口的 (端口, 链路, 边)。
	//   - transit IP / link-local：键为规范化后的地址字符串，值记录首个占用者 (链路, 边)。
	portsByNode := make(map[string][]nodePortPin)
	transitByValue := make(map[string]pinOwner)   // 规范化 transit IP -> 首个占用者
	linkLocalByValue := make(map[string]pinOwner) // 规范化 link-local -> 首个占用者

	for i := range topo.Edges {
		edge := topo.Edges[i]
		if !edge.IsEnabled {
			continue
		}

		prefix := fmt.Sprintf("edges[%d]", i)
		fromNode := nodeMap[edge.FromNodeID]
		toNode := nodeMap[edge.ToNodeID]
		// 两端节点缺失的边由 validateEdgeNodeRefs 报错，这里不重复，跳过 pin 校验。
		if fromNode == nil || toNode == nil {
			continue
		}

		// 链路键用于跨链路去重：primary class 的正反边与同向多余 primary 边共享同一 linkKey
		// （它们合并为同一条物理链路，pin 值合法地相同）；backup 边的 linkKey 携带自身 edge.ID，
		// 因此与 primary 链路的 pin 碰撞会被如实标记为跨链路冲突。
		link := linkid.LinkKey(&edge)

		// --- 规则：client 边携带 pin（client 用单一 wg0，无 per-peer 资源）。 ---
		// 先于其它规则处理：client 边的所有 per-peer pin 都会被忽略，因此端口 pin 报错、
		// 其余 pin（transit / link-local）告警「将被忽略」，并跳过其余只对 per-peer 链路
		// 有意义的检查（成对完整性、范围、越池、去重）。
		clientTouched := fromNode.Role == "client" || toNode.Role == "client"
		if clientTouched {
			if edge.PinnedFromPort != 0 || edge.PinnedToPort != 0 {
				result.AddError(prefix, CodePinClientPortPin, P{"id", edge.ID})
			}
			if edge.PinnedFromTransitIP != "" || edge.PinnedToTransitIP != "" ||
				edge.PinnedFromLinkLocal != "" || edge.PinnedToLinkLocal != "" {
				result.AddWarning(prefix, CodePinClientAllocationIgnored, P{"id", edge.ID})
			}
			continue
		}

		// --- 规则：部分 pin（一端钉住、另一端为空）。逐资源检查。 ---
		validatePinPairCompleteness(prefix, edge, result)

		// --- 规则：端口越界（< 1、> 65535，或低于节点基准 listen_port）。 ---
		validatePinnedPortRange(prefix, "pinned_from_port", edge.PinnedFromPort, fromNode, result)
		validatePinnedPortRange(prefix, "pinned_to_port", edge.PinnedToPort, toNode, result)

		// --- 规则：transit IP 越池（不在该边解析出的 domain transit CIDR 内）。 ---
		transitCIDR := edgeTransitCIDR(edge, domainMap, nodeMap)
		validatePinnedTransitInCIDR(prefix, "pinned_from_transit_ip", edge.PinnedFromTransitIP, transitCIDR, result)
		validatePinnedTransitInCIDR(prefix, "pinned_to_transit_ip", edge.PinnedToTransitIP, transitCIDR, result)

		// --- 规则：跨链路去重。 ---
		// 节点端口：from 侧端口归 from 节点，to 侧端口归 to 节点。
		checkDuplicatePortOnNode(prefix, edge.FromNodeID, edge.PinnedFromPort, link, edge.ID, portsByNode, result)
		checkDuplicatePortOnNode(prefix, edge.ToNodeID, edge.PinnedToPort, link, edge.ID, portsByNode, result)

		// transit IP 与 link-local：按规范化后的地址值跨链路去重。
		checkDuplicateTransitIP(prefix, edge.PinnedFromTransitIP, link, edge.ID, transitByValue, result)
		checkDuplicateTransitIP(prefix, edge.PinnedToTransitIP, link, edge.ID, transitByValue, result)
		checkDuplicateLinkLocal(prefix, edge.PinnedFromLinkLocal, link, edge.ID, linkLocalByValue, result)
		checkDuplicateLinkLocal(prefix, edge.PinnedToLinkLocal, link, edge.ID, linkLocalByValue, result)
	}
}

// validatePinPairCompleteness 校验「成对 pin」：对每一种资源，一端钉住而另一端为空都非法。
// pin 必须以完整成对的形式出现，否则编译器无法构造一条链路的双端配置。
func validatePinPairCompleteness(prefix string, edge model.Edge, result *ValidationResult) {
	if (edge.PinnedFromPort != 0) != (edge.PinnedToPort != 0) {
		result.AddError(prefix, CodePinPortIncomplete, P{"id", edge.ID})
	}
	if (edge.PinnedFromTransitIP != "") != (edge.PinnedToTransitIP != "") {
		result.AddError(prefix, CodePinTransitIPIncomplete, P{"id", edge.ID})
	}
	if (edge.PinnedFromLinkLocal != "") != (edge.PinnedToLinkLocal != "") {
		result.AddError(prefix, CodePinLinkLocalIncomplete, P{"id", edge.ID})
	}
}

// validatePinnedPortRange 校验单个被钉住的端口是否落在合法区间内：
// 必须 >= 节点基准 listen_port（未设置时取 defaultListenPort），且 <= 65535。
// 端口为 0 表示未钉住，跳过（成对完整性由 validatePinPairCompleteness 负责）。
func validatePinnedPortRange(prefix, field string, port int, node *model.Node, result *ValidationResult) {
	if port == 0 {
		return
	}
	base := node.ListenPort
	if base == 0 {
		base = defaultListenPort
	}
	if port < base || port > 65535 {
		result.AddError(prefix+"."+field, CodePinPortOutOfRange, P{"node", node.Name}, P{"port", strconv.Itoa(port)}, P{"base", strconv.Itoa(base)})
	}
}

// validatePinnedTransitInCIDR 校验单个被钉住的 transit IP 是否落在该边解析出的 transit 池内。
// 空字符串表示未钉住，跳过。无法解析的地址同样报错（陈旧或手误的 pin）。
func validatePinnedTransitInCIDR(prefix, field, value, transitCIDR string, result *ValidationResult) {
	if value == "" {
		return
	}
	ip := net.ParseIP(value)
	if ip == nil {
		result.AddError(prefix+"."+field, CodePinTransitIPInvalid, P{"cidr", fmt.Sprintf("%q", value)})
		return
	}
	_, cidrNet, err := net.ParseCIDR(transitCIDR)
	if err != nil {
		// transit CIDR 本身非法由 schema/编译器报错，这里不重复判定越池。
		return
	}
	if !cidrNet.Contains(ip) {
		result.AddError(prefix+"."+field, CodePinTransitIPOutOfCIDR, P{"cidr", value}, P{"prefix", transitCIDR})
	}
}

// checkDuplicatePortOnNode 跨「不同链路」检测同一节点上被重复钉住的监听端口。
// 同一链路（同一 linkKey）的正反两条边携带镜像后的同一端口，不视为冲突。
func checkDuplicatePortOnNode(prefix, nodeID string, port int, link, edgeID string, portsByNode map[string][]nodePortPin, result *ValidationResult) {
	if port == 0 {
		return
	}
	for _, existing := range portsByNode[nodeID] {
		if existing.port != port {
			continue
		}
		if existing.linkID == link {
			// 同一链路（正反边），不是跨链路冲突。
			return
		}
		result.AddError(prefix, CodePinPortDuplicateCrossLink, P{"port", strconv.Itoa(port)}, P{"other", existing.edge}, P{"id", edgeID})
		return
	}
	portsByNode[nodeID] = append(portsByNode[nodeID], nodePortPin{port: port, linkID: link, edge: edgeID})
}

// checkDuplicateTransitIP 跨「不同链路」检测被重复钉住的 transit IP。
// 地址按解析后的规范形式比较，避免 "10.10.0.1" 与等价写法逃过去重。
// 同一链路（同一 linkKey）的正反两条边携带镜像后的同一地址，不视为冲突。
func checkDuplicateTransitIP(prefix, value, link, edgeID string, transitByValue map[string]pinOwner, result *ValidationResult) {
	if value == "" {
		return
	}
	key := canonicalIP(value)
	if owner, exists := transitByValue[key]; exists {
		if owner.linkID == link {
			return
		}
		result.AddError(prefix, CodePinTransitIPDuplicateCrossLink, P{"cidr", value}, P{"other", owner.edge}, P{"id", edgeID})
		return
	}
	transitByValue[key] = pinOwner{linkID: link, edge: edgeID}
}

// checkDuplicateLinkLocal 跨「不同链路」检测被重复钉住的 IPv6 link-local 地址。
// 同一链路（同一 linkKey）的正反两条边携带镜像后的同一地址，不视为冲突。
func checkDuplicateLinkLocal(prefix, value, link, edgeID string, linkLocalByValue map[string]pinOwner, result *ValidationResult) {
	if value == "" {
		return
	}
	key := canonicalIP(value)
	if owner, exists := linkLocalByValue[key]; exists {
		if owner.linkID == link {
			return
		}
		result.AddError(prefix, CodePinLinkLocalDuplicateCrossLink, P{"cidr", value}, P{"other", owner.edge}, P{"id", edgeID})
		return
	}
	linkLocalByValue[key] = pinOwner{linkID: link, edge: edgeID}
}

// canonicalIP 把地址字符串归一为可比较的规范形式；不可解析时原样返回，
// 让去重退化为字符串相等（不可解析地址的合法性由其它规则报错）。
func canonicalIP(value string) string {
	if ip := net.ParseIP(value); ip != nil {
		return ip.String()
	}
	return value
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
			result.AddWarning(fmt.Sprintf("edges[%d].endpoint_host", i), CodeEdgeEndpointNoMatch, P{"id", edge.ID}, P{"other", edge.EndpointHost}, P{"node", toNode.Name})
		}
	}
}

// detectDuplicateEnabledEdges 对同一对节点（同方向）存在多条 primary class 启用边的情况
// 发出警告（D71，并行链路重定范围）。
// 同向多余的 primary class 边（Role 为空或 "primary"）会被编译器折叠进同一条链路：
// 只有首条生效，后续边携带的 endpoint_host/endpoint_port 覆盖会被静默忽略，
// 操作员看见两条边却只有一条起作用。
// backup 边（Role == "backup"）各自成为一条独立链路，是有意的并行链路而非意外重复，
// 因此从不触发本告警。
// 仅警告而非报错：拓扑仍可编译，但操作员应删除或禁用多余的边——
// 若本意是冗余备份，应将多余的边设为 role "backup" 使其成为独立的备份链路。
func detectDuplicateEnabledEdges(topo *model.Topology, result *ValidationResult) {
	firstEdgeByDirection := make(map[string]string) // "from->to" -> 首条 primary class 边 ID
	for i := range topo.Edges {
		edge := &topo.Edges[i]
		if !edge.IsEnabled {
			continue
		}
		// backup 边是独立链路，不参与同向去重告警。
		if linkid.IsBackup(edge) {
			continue
		}
		direction := edge.FromNodeID + "->" + edge.ToNodeID
		if firstID, exists := firstEdgeByDirection[direction]; exists {
			result.AddWarning(fmt.Sprintf("edges[%d]", i), CodeEdgeDuplicateEnabledSameDirection, P{"id", edge.ID}, P{"other", firstID})
			continue
		}
		firstEdgeByDirection[direction] = edge.ID
	}
}

// backupDefaultLinkCost 是 backup 链路的默认 Babel rxcost（4× babeld 有线默认 96），
// 必须与 compiler/peers.go 的同名常量保持一致——等代价告警要用编译器实际解析出的代价
// 来比较，二者若不一致会让校验放行编译器随后视为有故障切换偏好（或反之）的配置。
// 规范见 docs/spec/artifacts/babel.md（Link cost resolution）。
const backupDefaultLinkCost = 384

// babeldWiredDefaultCost 是 babeld 对有线 / tunnel 接口的内建默认 rxcost。
// 编译器把「未显式设置且非 backup」的链路代价解析为 0（省略 rxcost token，交由 babeld 默认），
// 比较等代价时必须把 0 视为该内建默认值，否则两条都未设代价的链路会被误判为不等代价。
const babeldWiredDefaultCost = 96

// effectiveLinkCost 完全镜像编译器的链路代价解析顺序（contract item 4 / babel.md）：
//  1. 显式 priority/weight 映射（D63：priority>0 取 priority，否则 weight>0 取 weight）优先；
//  2. 否则 backup 链路 → backupDefaultLinkCost（384）；
//  3. 否则 0（编译器省略 rxcost，babeld 采用内建默认 96）。
//
// 返回值为编译器写入的原始代价（0 表示交由 babeld 默认）。等代价比较时由调用方
// 通过 comparableCost 把 0 归一为 96。rep 为该链路的代表边：unify 后的 primary 链路取首条
// primary class 边，backup 链路取该 backup 边自身。
func effectiveLinkCost(rep *model.Edge) int {
	if rep == nil {
		return 0
	}
	if rep.Priority > 0 {
		return rep.Priority
	}
	if rep.Weight > 0 {
		return rep.Weight
	}
	if linkid.IsBackup(rep) {
		return backupDefaultLinkCost
	}
	return 0
}

// comparableCost 把链路代价归一为可比较值：0（未设、交由 babeld 默认）视为内建默认 96。
func comparableCost(cost int) int {
	if cost == 0 {
		return babeldWiredDefaultCost
	}
	return cost
}

// validateInterfaceNameUniqueness 校验不变式 N4：同一节点上所有 per-peer WireGuard 接口名
// （含 primary 与 backup、面向任意对端）必须互不相同。
//
// 一个节点可能朝同一对端持有多条接口（primary 链路 + 若干 backup），接口名由对端名称
// （primary）或对端名称叠加 backup 边 ID 的 4 位 hash（backup）派生。两条接口名相撞会让
// 一份 WireGuard 配置与一条 Babel 接口行覆盖另一条——16 位 hash 碰撞的确定性答案是「重命名其一」，
// 因此这里报错并点名两条相撞的链路（命名规范见 docs/spec/artifacts/naming.md §Edge-aware names）。
//
// 接口名按编译器的口径计算：
//   - primary 链路朝对端 R → naming.WgInterfaceName(R.Name)；
//   - backup 边 e 朝对端 R → naming.WgInterfaceNameForEdge(R.Name, e.ID, true)。
//
// client 节点使用单一 wg0、不参与 per-peer 接口分配，故跳过 client 端点侧。
func validateInterfaceNameUniqueness(topo *model.Topology, nodeMap map[string]*model.Node, result *ValidationResult) {
	// nodeID -> (接口名 -> 首个占用该接口名的链路描述)，用于跨链路检测同名。
	ifaceByNode := make(map[string]map[string]string)

	// register 在某节点上登记一个接口名；若已存在则报错并点名两条链路。
	register := func(nodeIndex int, nodeID, ifaceName, linkDesc string) {
		if ifaceByNode[nodeID] == nil {
			ifaceByNode[nodeID] = make(map[string]string)
		}
		if first, exists := ifaceByNode[nodeID][ifaceName]; exists {
			node := nodeMap[nodeID]
			nodeName := nodeID
			if node != nil {
				nodeName = node.Name
			}
			result.AddError(fmt.Sprintf("nodes[%d]", nodeIndex), CodeNodeInterfaceNameCollision, P{"node", nodeName}, P{"name", fmt.Sprintf("%q", ifaceName)}, P{"prefix", first}, P{"other", linkDesc})
			return
		}
		ifaceByNode[nodeID][ifaceName] = linkDesc
	}

	// 节点下标查找，用于错误字段定位。
	nodeIndex := make(map[string]int)
	for i := range topo.Nodes {
		nodeIndex[topo.Nodes[i].ID] = i
	}

	// 以 linkKey 去重链路：primary class 折叠为一条链路（用首条 primary class 边作代表），
	// 每条 backup 边各为一条独立链路。
	seenLinks := make(map[string]bool)
	for i := range topo.Edges {
		edge := &topo.Edges[i]
		if !edge.IsEnabled {
			continue
		}
		fromNode := nodeMap[edge.FromNodeID]
		toNode := nodeMap[edge.ToNodeID]
		if fromNode == nil || toNode == nil {
			continue
		}
		lk := linkid.LinkKey(edge)
		if seenLinks[lk] {
			continue
		}
		seenLinks[lk] = true

		backup := linkid.IsBackup(edge)

		// from 端点的接口朝 to（对端 = to），to 端点的接口朝 from（对端 = from）。
		// 接口名以对端名称派生；client 端点不分配 per-peer 接口，跳过。
		if fromNode.Role != "client" {
			var ifaceName string
			if backup {
				ifaceName = naming.WgInterfaceNameForEdge(toNode.Name, edge.ID, true)
			} else {
				ifaceName = naming.WgInterfaceName(toNode.Name)
			}
			register(nodeIndex[fromNode.ID], fromNode.ID, ifaceName, linkDescription(edge, toNode.Name, backup))
		}
		if toNode.Role != "client" {
			var ifaceName string
			if backup {
				ifaceName = naming.WgInterfaceNameForEdge(fromNode.Name, edge.ID, true)
			} else {
				ifaceName = naming.WgInterfaceName(fromNode.Name)
			}
			register(nodeIndex[toNode.ID], toNode.ID, ifaceName, linkDescription(edge, fromNode.Name, backup))
		}
	}
}

// linkDescription 为错误消息构造一条链路的可读描述：标明朝向的对端、链路类别（primary/backup）
// 与代表边 ID，便于操作员定位需重命名的链路。
// linkDescription builds a LANGUAGE-NEUTRAL locator for a colliding link from DATA only — the
// role enum value (primary|backup), the remote node name, and the representative edge ID for a
// backup — with NO translatable prose. It is interpolated as the {prefix}/{other} params of
// CodeNodeInterfaceNameCollision, so it must render the same in every locale: prose here would
// splice English into the zh message (plan-3.5a review). Role values are kept verbatim across
// locales by the same convention as auto/manual/babel.
func linkDescription(edge *model.Edge, remoteName string, backup bool) string {
	if backup {
		return fmt.Sprintf("backup→%s (%s)", remoteName, edge.ID)
	}
	return fmt.Sprintf("primary→%s", remoteName)
}

// validateSinglePrimaryPerPair 校验：同一对节点至多有一条显式标记 role=="primary" 的边。
// 空 role 与 "primary" 同属 primary class，但显式写两条 "primary" 通常是操作员误解
// （以为可借此表达双主），编译器只会折叠为一条主链路并静默忽略其余——故直接报错要求澄清。
// 注意：只统计显式 "primary"，不含空 role（空 role 的同向重复由 detectDuplicateEnabledEdges 告警）。
func validateSinglePrimaryPerPair(topo *model.Topology, nodeMap map[string]*model.Node, result *ValidationResult) {
	// pinKey -> 首条显式 primary 边 ID。
	firstPrimary := make(map[string]string)
	for i := range topo.Edges {
		edge := &topo.Edges[i]
		if !edge.IsEnabled {
			continue
		}
		if edge.Role != model.EdgeRolePrimary {
			continue
		}
		if nodeMap[edge.FromNodeID] == nil || nodeMap[edge.ToNodeID] == nil {
			continue
		}
		pk := linkid.PinKey(edge.FromNodeID, edge.ToNodeID)
		if firstID, exists := firstPrimary[pk]; exists {
			result.AddError(fmt.Sprintf("edges[%d].role", i), CodeEdgeMultipleExplicitPrimary, P{"id", edge.ID}, P{"other", firstID})
			continue
		}
		firstPrimary[pk] = edge.ID
	}
}

// validateBackupClientEdges 校验：role=="backup" 的边不得触及 client 节点。
// client 使用单一 wg0、不运行 Babel，也就没有 per-peer 接口与基于代价的故障切换语义，
// 备份链路对其毫无意义（与 validateClientEdges 中「client 恰好一条出站边、单一 wg0」的约束一致）。
func validateBackupClientEdges(topo *model.Topology, nodeMap map[string]*model.Node, result *ValidationResult) {
	for i := range topo.Edges {
		edge := &topo.Edges[i]
		if !edge.IsEnabled {
			continue
		}
		if !linkid.IsBackup(edge) {
			continue
		}
		fromNode := nodeMap[edge.FromNodeID]
		toNode := nodeMap[edge.ToNodeID]
		if (fromNode != nil && fromNode.Role == "client") || (toNode != nil && toNode.Role == "client") {
			result.AddError(fmt.Sprintf("edges[%d].role", i), CodeEdgeBackupTouchesClient, P{"id", edge.ID})
		}
	}
}

// pairLinkSummary 汇总一对节点的全部链路，用于等代价 / 无 primary 告警。
type pairLinkSummary struct {
	edgeIndex  int    // 触发告警时定位用的代表边下标（取该对节点首条启用边）
	hasPrimary bool   // 是否存在 primary class 链路
	costs      []int  // 各链路的可比较代价（已把 0 归一为 96）
	fromName   string // 用于错误消息
	toName     string
}

// validateParallelLinkCosts 对多链路节点对发出两类告警（并行链路 / 故障切换）：
//   - 等代价告警：一对节点有 >=2 条链路、但所有链路解析出的可比较代价都相同——
//     Babel 无从偏好任何一条，配置表达不出故障切换意图（规范见 docs/spec/artifacts/babel.md）。
//   - 无 primary 告警：一对节点的所有链路都是 backup（例如某次 role 翻转后遗漏了 primary）。
//
// 链路按 unify 规则分组：primary class 折叠为一条链路（代表边 = 首条 primary class 边），
// 每条 backup 边各为一条链路。代价解析完全镜像编译器（effectiveLinkCost），
// 比较前由 comparableCost 把 0 归一为 babeld 内建默认 96。
func validateParallelLinkCosts(topo *model.Topology, nodeMap map[string]*model.Node, result *ValidationResult) {
	// pinKey -> 该对节点的链路汇总。pinKey 方向无关，保证正反边落在同一对。
	summaries := make(map[string]*pairLinkSummary)
	order := make([]string, 0) // 保持首次出现顺序，使告警稳定可测

	// primaryCounted 记录每对节点的 primary class 是否已计入代价：primary class 折叠为一条链路，
	// 其代价取首条 primary class 边（代表边），只计一次。
	primaryCounted := make(map[string]bool)

	for i := range topo.Edges {
		edge := &topo.Edges[i]
		if !edge.IsEnabled {
			continue
		}
		fromNode := nodeMap[edge.FromNodeID]
		toNode := nodeMap[edge.ToNodeID]
		if fromNode == nil || toNode == nil {
			continue
		}
		pk := linkid.PinKey(edge.FromNodeID, edge.ToNodeID)
		s := summaries[pk]
		if s == nil {
			s = &pairLinkSummary{
				edgeIndex: i,
				fromName:  fromNode.Name,
				toName:    toNode.Name,
			}
			summaries[pk] = s
			order = append(order, pk)
		}

		if linkid.IsBackup(edge) {
			// 每条 backup 边各为一条链路。
			s.costs = append(s.costs, comparableCost(effectiveLinkCost(edge)))
			continue
		}

		// primary class：所有非 backup 边折叠为一条链路，仅计代表边（首条）的代价一次。
		s.hasPrimary = true
		if !primaryCounted[pk] {
			primaryCounted[pk] = true
			s.costs = append(s.costs, comparableCost(effectiveLinkCost(edge)))
		}
	}

	for _, pk := range order {
		s := summaries[pk]

		// 无 primary 告警：该对节点存在链路但无 primary class 链路（全是 backup）。
		if len(s.costs) > 0 && !s.hasPrimary {
			result.AddWarning(fmt.Sprintf("edges[%d]", s.edgeIndex), CodeLinkNoPrimary, P{"node", s.fromName}, P{"other", s.toName})
		}

		// 等代价告警：>=2 条链路且可比较代价全相同。
		if len(s.costs) >= 2 {
			allEqual := true
			for _, c := range s.costs[1:] {
				if c != s.costs[0] {
					allEqual = false
					break
				}
			}
			if allEqual {
				result.AddWarning(fmt.Sprintf("edges[%d]", s.edgeIndex), CodeLinkEqualCost, P{"node", s.fromName}, P{"other", s.toName}, P{"count", strconv.Itoa(len(s.costs))}, P{"low", strconv.Itoa(s.costs[0])})
			}
		}
	}
}
