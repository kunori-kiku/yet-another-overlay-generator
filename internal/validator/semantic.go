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

	// 生效监听端口范围：每节点 base..base+(对端接口数-1) 是否越界，以及同主机节点范围是否重叠
	validateEffectivePortRanges(topo, result)

	// 
	detectIsolatedNodes(topo, result)

	// NAT
	validateNATReachability(topo, nodeMap, result)

	// Client 边验证
	validateClientEdges(topo, nodeMap, result)

	// Edge endpoint 与目标节点 public endpoints 一致性检查
	validateEdgeEndpointConsistency(topo, nodeMap, result)

	// 同一节点对的重复启用边检测（编译器只取首条，后续边的 endpoint 覆盖会被静默丢弃）
	detectDuplicateEnabledEdges(topo, result)

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
		result.AddError("route_policies",
			fmt.Sprintf("route_policies 为保留特性，尚未实现：当前没有任何渲染器消费它，编译器仅原样透传，必须为空（检测到 %d 条策略，请清空 route_policies；LAN 桥接 / 路由注入请改用 extra_prefixes）", len(topo.RoutePolicies)))
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

// defaultListenPort 是节点未显式设置 listen_port 时编译器采用的基准端口，
// 必须与 peers.go Pass 1 中的默认值（51820）保持一致。
const defaultListenPort = 51820

// effectivePortRange 描述一个节点在 per-peer 接口模型下实际占用的监听端口范围。
//
//	[base, base+count-1]
//
// 其中 base 为节点的基准 listen_port（未设置时取 defaultListenPort），
// count 为该节点作为「非 client 端点」参与的去重节点对数量——
// 这正是编译器为它分配的 WireGuard 接口个数。
type effectivePortRange struct {
	nodeIndex int    // 节点在 topo.Nodes 中的下标，用于定位错误字段
	nodeName  string // 节点名称，用于错误消息
	hostname  string // 节点 hostname（可能为空）
	base      int    // 基准监听端口
	count     int    // 该节点占用的接口数（= 去重对端数）
}

// high 返回该节点占用的最高监听端口（base + count - 1）。
func (r effectivePortRange) high() int {
	return r.base + r.count - 1
}

// validateEffectivePortRanges 校验 per-peer 接口模型下每个节点的「生效监听端口范围」
// （D47 + D11 的一部分）。
//
// 编译器为每条启用边的每个非 client 端点分配一个独立 WireGuard 接口，监听端口从
// 节点基准端口起按 base+offset 递增（见 peers.go Pass 1 中 nodePortOffset 的逻辑）。
// 这里完全镜像该计数方式：
//   - 仅统计启用且两端节点均存在的边；
//   - 以无序节点对去重（任一方向已计入则跳过），与 addedPairs 一致；
//   - 每个去重节点对，为其两端中的「非 client」端点各 +1。
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

	// 镜像 peers.go Pass 1：以无序节点对去重，为每个非 client 端点累计接口数。
	addedPairs := make(map[string]bool)
	interfaceCount := make(map[string]int) // nodeID -> 接口数（去重对端数）

	for _, edge := range topo.Edges {
		if !edge.IsEnabled {
			continue
		}

		fromNode := nodeMap[edge.FromNodeID]
		toNode := nodeMap[edge.ToNodeID]
		if fromNode == nil || toNode == nil {
			continue
		}

		peerKey := fromNode.ID + "->" + toNode.ID
		reversePeerKey := toNode.ID + "->" + fromNode.ID

		// 任一方向已计入则跳过，确保无序对只计一次。
		if addedPairs[peerKey] || addedPairs[reversePeerKey] {
			continue
		}

		// client 节点使用单一 wg0，不参与 per-peer 端口分配（与 peers.go 的
		// isFromClient / isToClient 守卫一致）。
		if fromNode.Role != "client" {
			interfaceCount[fromNode.ID]++
		}
		if toNode.Role != "client" {
			interfaceCount[toNode.ID]++
		}

		addedPairs[peerKey] = true
		addedPairs[reversePeerKey] = true
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
			result.AddError(fmt.Sprintf("nodes[%d].listen_port", r.nodeIndex),
				fmt.Sprintf("节点 %s 的生效监听端口范围为 %d-%d（基准端口 %d + %d 个对端接口），最高端口 %d 超过 65535，将生成无法部署的 WireGuard 配置",
					r.nodeName, r.base, r.high(), r.base, r.count, r.high()))
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
				result.AddError(fmt.Sprintf("nodes[%d].listen_port", later.nodeIndex),
					fmt.Sprintf("节点 %s（端口 %d-%d）与节点 %s（端口 %d-%d）共享主机 %s 且生效监听端口范围重叠，同一主机上的 WireGuard 接口会争用相同端口",
						earlier.nodeName, earlier.base, earlier.high(),
						later.nodeName, later.base, later.high(),
						later.hostname))
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

// pinKey 返回一条链路的规范键：两端节点 ID 的无序对（字符串 min|max）。
// pinKey(a,b) == pinKey(b,a)，因此一条边与其反向边落在同一个键上，
// 跨链路去重时不会把同一物理链路的正反两条边误判为两条不同链路。
func pinKey(a, b string) string {
	if a <= b {
		return a + "|" + b
	}
	return b + "|" + a
}

// nodePortPin 描述某个节点在某条链路上被钉住的监听端口，用于跨链路去重时定位冲突。
type nodePortPin struct {
	port   int
	linkID string // 首个声明该 (节点, 端口) 的链路 pinKey
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
//   - 去重规则（同一节点端口、同一 transit IP、同一 link-local 被两条不同链路占用）按 pinKey
//     归并后跨链路比较，避免把同一链路的正反边当作两条链路。
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

		link := pinKey(edge.FromNodeID, edge.ToNodeID)

		// --- 规则：client 边携带 pin（client 用单一 wg0，无 per-peer 资源）。 ---
		// 先于其它规则处理：client 边的所有 per-peer pin 都会被忽略，因此端口 pin 报错、
		// 其余 pin（transit / link-local）告警「将被忽略」，并跳过其余只对 per-peer 链路
		// 有意义的检查（成对完整性、范围、越池、去重）。
		clientTouched := fromNode.Role == "client" || toNode.Role == "client"
		if clientTouched {
			if edge.PinnedFromPort != 0 || edge.PinnedToPort != 0 {
				result.AddError(prefix,
					fmt.Sprintf("边 %s 触及 client 节点却设置了端口 pin：client 使用单一 wg0，没有 per-peer 监听端口，请清除该边的 pinned_from_port / pinned_to_port", edge.ID))
			}
			if edge.PinnedFromTransitIP != "" || edge.PinnedToTransitIP != "" ||
				edge.PinnedFromLinkLocal != "" || edge.PinnedToLinkLocal != "" {
				result.AddWarning(prefix,
					fmt.Sprintf("边 %s 触及 client 节点，其分配 pin 将被忽略：client 使用单一 wg0，不参与 per-peer transit/link-local 分配", edge.ID))
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
		result.AddError(prefix,
			fmt.Sprintf("边 %s 的监听端口 pin 不完整（仅一端被钉住）：pin 必须成对设置，请补全 pinned_from_port 与 pinned_to_port，或同时清空两者", edge.ID))
	}
	if (edge.PinnedFromTransitIP != "") != (edge.PinnedToTransitIP != "") {
		result.AddError(prefix,
			fmt.Sprintf("边 %s 的 transit IP pin 不完整（仅一端被钉住）：pin 必须成对设置，请补全 pinned_from_transit_ip 与 pinned_to_transit_ip，或同时清空两者", edge.ID))
	}
	if (edge.PinnedFromLinkLocal != "") != (edge.PinnedToLinkLocal != "") {
		result.AddError(prefix,
			fmt.Sprintf("边 %s 的 link-local pin 不完整（仅一端被钉住）：pin 必须成对设置，请补全 pinned_from_link_local 与 pinned_to_link_local，或同时清空两者", edge.ID))
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
		result.AddError(prefix+"."+field,
			fmt.Sprintf("节点 %s 的端口 pin %d 越界：必须不低于节点基准监听端口 %d 且不超过 65535（若需重新编号请清除该 pin）",
				node.Name, port, base))
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
		result.AddError(prefix+"."+field,
			fmt.Sprintf("transit IP pin %q 不是合法的 IP 地址", value))
		return
	}
	_, cidrNet, err := net.ParseCIDR(transitCIDR)
	if err != nil {
		// transit CIDR 本身非法由 schema/编译器报错，这里不重复判定越池。
		return
	}
	if !cidrNet.Contains(ip) {
		result.AddError(prefix+"."+field,
			fmt.Sprintf("transit IP pin %s 不在该边的 transit 地址池 %s 内（地址池可能被收窄，请清除该 pin 以重新编号）",
				value, transitCIDR))
	}
}

// checkDuplicatePortOnNode 跨「不同链路」检测同一节点上被重复钉住的监听端口。
// 同一链路（同一 pinKey）的正反两条边携带镜像后的同一端口，不视为冲突。
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
		result.AddError(prefix,
			fmt.Sprintf("端口 pin %d 在节点上被两条不同链路重复占用：边 %s 与边 %s 钉住了同一节点的同一监听端口", port, existing.edge, edgeID))
		return
	}
	portsByNode[nodeID] = append(portsByNode[nodeID], nodePortPin{port: port, linkID: link, edge: edgeID})
}

// checkDuplicateTransitIP 跨「不同链路」检测被重复钉住的 transit IP。
// 地址按解析后的规范形式比较，避免 "10.10.0.1" 与等价写法逃过去重。
// 同一链路（同一 pinKey）的正反两条边携带镜像后的同一地址，不视为冲突。
func checkDuplicateTransitIP(prefix, value, link, edgeID string, transitByValue map[string]pinOwner, result *ValidationResult) {
	if value == "" {
		return
	}
	key := canonicalIP(value)
	if owner, exists := transitByValue[key]; exists {
		if owner.linkID == link {
			return
		}
		result.AddError(prefix,
			fmt.Sprintf("transit IP pin %s 被两条不同链路重复占用：边 %s 与边 %s 钉住了同一 transit 地址", value, owner.edge, edgeID))
		return
	}
	transitByValue[key] = pinOwner{linkID: link, edge: edgeID}
}

// checkDuplicateLinkLocal 跨「不同链路」检测被重复钉住的 IPv6 link-local 地址。
// 同一链路（同一 pinKey）的正反两条边携带镜像后的同一地址，不视为冲突。
func checkDuplicateLinkLocal(prefix, value, link, edgeID string, linkLocalByValue map[string]pinOwner, result *ValidationResult) {
	if value == "" {
		return
	}
	key := canonicalIP(value)
	if owner, exists := linkLocalByValue[key]; exists {
		if owner.linkID == link {
			return
		}
		result.AddError(prefix,
			fmt.Sprintf("link-local pin %s 被两条不同链路重复占用：边 %s 与边 %s 钉住了同一 link-local 地址", value, owner.edge, edgeID))
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
			result.AddWarning(fmt.Sprintf("edges[%d].endpoint_host", i),
				fmt.Sprintf("Edge %s dials %s but target %s has no matching public endpoint (the endpoint snapshot may be stale after a node edit)",
					edge.ID, edge.EndpointHost, toNode.Name))
		}
	}
}

// detectDuplicateEnabledEdges 对同一对节点（同方向）存在多条启用边的情况发出警告（D71）。
// 编译器在 Pass 1/Pass 2 中按节点对去重，只有首条边生效——后续边携带的
// endpoint_host/endpoint_port 覆盖会被静默忽略，操作员看见两条边却只有一条起作用。
// 仅警告而非报错：拓扑仍可编译，但操作员应删除或禁用多余的边。
func detectDuplicateEnabledEdges(topo *model.Topology, result *ValidationResult) {
	firstEdgeByDirection := make(map[string]string) // "from->to" -> 首条边 ID
	for i, edge := range topo.Edges {
		if !edge.IsEnabled {
			continue
		}
		direction := edge.FromNodeID + "->" + edge.ToNodeID
		if firstID, exists := firstEdgeByDirection[direction]; exists {
			result.AddWarning(fmt.Sprintf("edges[%d]", i),
				fmt.Sprintf("边 %s 与边 %s 连接同一对节点（同方向），编译时只有首条生效，本条边的 endpoint 设置会被忽略；请删除或禁用多余的边", edge.ID, firstID))
			continue
		}
		firstEdgeByDirection[direction] = edge.ID
	}
}
