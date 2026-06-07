package compiler

import (
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"net"
	"sort"

	"github.com/kunorikiku/yet-another-overlay-generator/internal/model"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/naming"
)

// defaultTransitCIDR 是域未显式配置 transit_cidr 时回退使用的默认 transit 地址池。
// 在分配点把空值解析到这个常量，可让 per-CIDR 计数器（审计项 D12）以「解析后的 CIDR」
// 为键，从而与 allocateTransitPair 内部的默认解析、以及 DeriveClientConfigs 的 AllowedIPs
// 解析保持一致——同一池绝不会被记成两份。
const defaultTransitCIDR = "10.10.0.0/24"

// KeyPair WireGuard 密钥对
type KeyPair struct {
	PrivateKey string
	PublicKey  string
}

// PeerInfo 描述一个点对点 WireGuard 接口的完整配置
// 新架构：每个 peer 一个 WireGuard 接口
type PeerInfo struct {
	// 远端节点 ID
	NodeID string

	// 远端节点名称
	NodeName string

	// 远端节点公钥
	PublicKey string

	// 远端节点 Overlay IP
	OverlayIP string

	// AllowedIPs（per-peer 模型中使用宽松策略：0.0.0.0/0, ::/0）
	AllowedIPs []string

	// Endpoint（远端公网地址）
	Endpoint string

	// PersistentKeepalive
	PersistentKeepalive int

	// WireGuard 接口名（如 wg-dmit，Linux 限 15 字符）
	InterfaceName string

	// === 以下为 per-peer-interface 架构新增字段 ===

	// 该接口的独立监听端口
	ListenPort int

	// 本端 transit IP（点对点链路地址）
	LocalTransitIP string

	// 对端 transit IP
	RemoteTransitIP string

	// 本端 IPv6 link-local 地址（Babel 需要）
	LocalLinkLocal string

	// 对端 IPv6 link-local 地址
	RemoteLinkLocal string

	// 是否为连接 client 的 router 侧接口
	IsClientPeer bool

	// Client 的 overlay IP（仅当 IsClientPeer=true 时有值，用于 PostUp 路由注入）
	ClientOverlayIP string

	// 该链路的 Babel rxcost 覆盖值，由对应 edge 推导（D63）。
	// 0 表示采用角色 preset 的默认 cost（由 Babel 渲染器决定）。
	LinkCost int
}

// DerivePeers 根据 Edge 拓扑推导每个节点的 WireGuard Peer 列表
// 新架构：每个 peer 一个独立接口
// 返回 map[nodeID][]PeerInfo
func DerivePeers(topo *model.Topology, keys map[string]KeyPair) (map[string][]PeerInfo, map[string]*pairAllocation, error) {
	// 构建 Domain 索引
	domainMap := make(map[string]*model.Domain)
	for i := range topo.Domains {
		domainMap[topo.Domains[i].ID] = &topo.Domains[i]
	}

	return derivePeersWithDomains(topo, keys, domainMap)
}

// pairAllocation 预分配的节点对资源（端口、transit IP、link-local）
type pairAllocation struct {
	fromNodeID    string
	toNodeID      string
	fromPort      int // fromNode 接口的已分配监听端口
	toPort        int // toNode 接口的已分配监听端口
	localTransit  string
	remoteTransit string
	localLL       string
	remoteLL      string
}

// derivePeersWithDomains 核心推导逻辑（两阶段算法）
// Pass 1: 预分配所有节点对的端口和地址资源
// Pass 2: 使用预分配的端口构建 PeerInfo（确保 endpoint 端口 = 对端接口监听端口）
func derivePeersWithDomains(topo *model.Topology, keys map[string]KeyPair, domainMap map[string]*model.Domain) (map[string][]PeerInfo, map[string]*pairAllocation, error) {
	peerMap := make(map[string][]PeerInfo)

	// 节点索引
	nodeMap := make(map[string]*model.Node)
	for i := range topo.Nodes {
		nodeMap[topo.Nodes[i].ID] = &topo.Nodes[i]
	}

	// 初始化每个节点的 peer 列表
	for _, node := range topo.Nodes {
		peerMap[node.ID] = []PeerInfo{}
	}

	// 预扫描所有启用的 edge 方向，用于 keepalive 判断
	enabledEdgeDirections := make(map[string]bool)
	for _, edge := range topo.Edges {
		if edge.IsEnabled {
			enabledEdgeDirections[edge.FromNodeID+"->"+edge.ToNodeID] = true
		}
	}

	// 构建 edge 反向查找索引：key="fromNodeID->toNodeID" -> Edge
	edgeMap := make(map[string]*model.Edge)
	for i := range topo.Edges {
		e := &topo.Edges[i]
		if e.IsEnabled {
			edgeMap[e.FromNodeID+"->"+e.ToNodeID] = e
		}
	}

	// ======== Pass 1: 预分配资源（reserve-then-gap-fill，Spec B） ========
	//
	// 顺序无关性（I2）由构造保证：先把全拓扑所有 pin 预留进各资源池，再为未 pin 的链路
	// gap-fill。因此新增链路绝不会拿到既有链路已占用的值，既有链路的值也永不移动（I1/I3/I4）。
	// gap-fill 按 pinKey 排序遍历、池内取最低空闲槽位（Spec B 规范要求的 pinKey-deterministic 顺序）：
	// 一条链路看到的预留集合只取决于全拓扑当前的 pin 与 pinKey 更小的未 pin 链路，与数组位置、
	// 以及该链路自身的删除/重加历史无关，从而保证 delete/re-add 幂等（I9/G1）。
	allocations := make(map[string]*pairAllocation) // key: "fromNodeID->toNodeID"（双向都指向同一 struct）

	// 把每个 enabled edge 折叠到其规范 pinKey；同一对节点（任一方向）只处理一次。
	// primaryEdge 记录该 pinKey 首次出现的 edge，用以确定 pairAllocation 的 from/to 定向。
	type linkEntity struct {
		pinKey      string
		primaryEdge *model.Edge // 决定 from/to 定向
		fromNode    *model.Node
		toNode      *model.Node
		transitCIDR string // 解析后的 transit CIDR（per-pool 键）
	}
	links := make([]*linkEntity, 0, len(topo.Edges))
	seenPinKey := make(map[string]bool)

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
		pk := pinKey(fromNode.ID, toNode.ID)
		if seenPinKey[pk] {
			continue
		}
		seenPinKey[pk] = true

		// 解析该链路所属域的 transit CIDR（空值回退默认池）。必须与 allocateTransitPair
		// 内部的默认解析、以及 DeriveClientConfigs 的 AllowedIPs 解析保持一致（审计项 D12）。
		transitCIDR := defaultTransitCIDR
		if domain := domainMap[fromNode.DomainID]; domain != nil && domain.TransitCIDR != "" {
			transitCIDR = domain.TransitCIDR
		}

		links = append(links, &linkEntity{
			pinKey:      pk,
			primaryEdge: edge,
			fromNode:    fromNode,
			toNode:      toNode,
			transitCIDR: transitCIDR,
		})
	}

	// ---- 预留集合 ----
	// 端口按节点定向；transit IP 按 CIDR 池逐字存 IP 字符串（不做 index 反查——见 Spec B 的稳健选择）；
	// link-local 全局唯一。
	usedPorts := make(map[string]map[int]bool)         // nodeID -> 端口集合
	usedTransitIPs := make(map[string]map[string]bool) // cidr -> IP 字符串集合
	usedLinkLocals := make(map[string]bool)            // link-local 字符串集合

	markPort := func(nodeID string, port int) {
		if usedPorts[nodeID] == nil {
			usedPorts[nodeID] = make(map[int]bool)
		}
		usedPorts[nodeID][port] = true
	}
	markTransit := func(cidr, ip string) {
		if usedTransitIPs[cidr] == nil {
			usedTransitIPs[cidr] = make(map[string]bool)
		}
		usedTransitIPs[cidr][ip] = true
	}
	transitUsed := func(cidr, ip string) bool {
		return usedTransitIPs[cidr] != nil && usedTransitIPs[cidr][ip]
	}

	// ======== Pass 1 阶段 3：预留所有 pin ========
	// 在任何 gap-fill 之前，把每条链路携带的（成对完整的）pin 逐资源预留。partial pin
	// （单端有值）在此一律按「该资源未 pin」处理并跳过——成对校验由验证器分区负责。
	pinnedAllocations := make(map[string]*pairAllocation) // pinKey -> 直接由 pin 构造的分配
	for _, link := range links {
		edge := link.primaryEdge
		isFromClient := link.fromNode.Role == "client"
		isToClient := link.toNode.Role == "client"

		alloc := &pairAllocation{
			fromNodeID: link.fromNode.ID,
			toNodeID:   link.toNode.ID,
		}
		hasAnyPin := false

		// 端口 pin（成对完整且非 client 侧才视为已 pin）。
		if !isFromClient && !isToClient && edge.PinnedFromPort > 0 && edge.PinnedToPort > 0 {
			alloc.fromPort = edge.PinnedFromPort
			alloc.toPort = edge.PinnedToPort
			markPort(link.fromNode.ID, edge.PinnedFromPort)
			markPort(link.toNode.ID, edge.PinnedToPort)
			hasAnyPin = true
		}

		// transit IP pin（成对完整才视为已 pin）。
		if edge.PinnedFromTransitIP != "" && edge.PinnedToTransitIP != "" {
			alloc.localTransit = edge.PinnedFromTransitIP
			alloc.remoteTransit = edge.PinnedToTransitIP
			markTransit(link.transitCIDR, edge.PinnedFromTransitIP)
			markTransit(link.transitCIDR, edge.PinnedToTransitIP)
			hasAnyPin = true
		}

		// link-local pin（成对完整才视为已 pin）。
		if edge.PinnedFromLinkLocal != "" && edge.PinnedToLinkLocal != "" {
			alloc.localLL = edge.PinnedFromLinkLocal
			alloc.remoteLL = edge.PinnedToLinkLocal
			usedLinkLocals[edge.PinnedFromLinkLocal] = true
			usedLinkLocals[edge.PinnedToLinkLocal] = true
			hasAnyPin = true
		}

		if hasAnyPin {
			pinnedAllocations[link.pinKey] = alloc
		}
	}

	// ======== Pass 1 阶段 4：gap-fill 未 pin 的资源 ========
	// 按 pinKey 排序遍历，保证候选顺序与数组位置无关（Spec B 规范要求的 pinKey-deterministic 顺序）。
	// 每个资源在其池内取最低空闲槽位；因预留在前、遍历顺序仅由 pinKey 决定，删除再重加同一对节点
	// 会看到相同的预留集合从而重现同一值（I2/I9）。
	sort.Slice(links, func(i, j int) bool { return links[i].pinKey < links[j].pinKey })

	for _, link := range links {
		fromNode := link.fromNode
		toNode := link.toNode
		isFromClient := fromNode.Role == "client"
		isToClient := toNode.Role == "client"

		// 取该 pinKey 的（部分）pin 分配作为起点，未 pin 的资源在其上补齐。
		alloc := pinnedAllocations[link.pinKey]
		if alloc == nil {
			alloc = &pairAllocation{fromNodeID: fromNode.ID, toNodeID: toNode.ID}
		}

		// ---- 端口：未 pin 则逐侧取「不低于节点 base 的最低空闲端口」 ----
		// client 侧不参与 per-peer 端口分配（使用单一 wg0），端口保持 0、不预留；
		// 但触及 client 的边其「非 client 侧」（router/relay/gateway）仍需分配监听端口，
		// 否则 DeriveClientConfigs 无法得知 client 该拨哪个端口。因此逐侧独立判断。
		// 端口 pin 是成对的（验证器保证），故只要任一侧已 pin 即视为整对已 pin、跳过分配。
		portsPinned := alloc.fromPort > 0 || alloc.toPort > 0
		if !portsPinned {
			if !isFromClient {
				fromPort, err := lowestFreePort(fromNode, usedPorts)
				if err != nil {
					return nil, nil, err
				}
				markPort(fromNode.ID, fromPort)
				alloc.fromPort = fromPort
			}
			if !isToClient {
				toPort, err := lowestFreePort(toNode, usedPorts)
				if err != nil {
					return nil, nil, err
				}
				markPort(toNode.ID, toPort)
				alloc.toPort = toPort
			}
		}

		// ---- transit IP 对：未 pin 则在 per-CIDR 池里取最低空闲 pair ----
		transitPinned := alloc.localTransit != "" && alloc.remoteTransit != ""
		if !transitPinned {
			localTransit, remoteTransit, err := gapFillTransitPair(link.transitCIDR, transitUsed)
			if err != nil {
				return nil, nil, fmt.Errorf("节点 %s<->%s 的 transit 地址分配失败: %w", fromNode.Name, toNode.Name, err)
			}
			markTransit(link.transitCIDR, localTransit)
			markTransit(link.transitCIDR, remoteTransit)
			alloc.localTransit = localTransit
			alloc.remoteTransit = remoteTransit
		}

		// ---- link-local 对：未 pin 则取最低空闲 pair ----
		llPinned := alloc.localLL != "" && alloc.remoteLL != ""
		if !llPinned {
			localLL, remoteLL := gapFillLinkLocalPair(usedLinkLocals)
			usedLinkLocals[localLL] = true
			usedLinkLocals[remoteLL] = true
			alloc.localLL = localLL
			alloc.remoteLL = remoteLL
		}

		peerKey := fromNode.ID + "->" + toNode.ID
		reversePeerKey := toNode.ID + "->" + fromNode.ID
		allocations[peerKey] = alloc
		allocations[reversePeerKey] = alloc
	}

	// ======== Pass 2: 使用预分配的端口构建 PeerInfo ========
	addedPeers := make(map[string]bool)

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
		if addedPeers[peerKey] {
			continue
		}

		alloc := allocations[peerKey]
		if alloc == nil {
			continue
		}

		// Client 节点不在 peerMap 中创建 PeerInfo（client 使用单一 wg0，由 DeriveClientConfigs 处理）
		if fromNode.Role == "client" {
			// 只创建 router 侧的 PeerInfo（router -> client 方向）
			reversePeerKey := toNode.ID + "->" + fromNode.ID
			if !addedPeers[reversePeerKey] {
				fromKey, _ := keys[fromNode.ID]
				isForward := alloc.fromNodeID == fromNode.ID

				var routerListenPort int
				var routerLocalTransit, routerRemoteTransit, routerLocalLL, routerRemoteLL string
				if isForward {
					routerListenPort = alloc.toPort
					routerLocalTransit = alloc.remoteTransit
					routerRemoteTransit = alloc.localTransit
					routerLocalLL = alloc.remoteLL
					routerRemoteLL = alloc.localLL
				} else {
					routerListenPort = alloc.fromPort
					routerLocalTransit = alloc.localTransit
					routerRemoteTransit = alloc.remoteTransit
					routerLocalLL = alloc.localLL
					routerRemoteLL = alloc.remoteLL
				}

				routerPeer := PeerInfo{
					NodeID:              fromNode.ID,
					NodeName:            fromNode.Name,
					PublicKey:           fromKey.PublicKey,
					OverlayIP:           fromNode.OverlayIP,
					AllowedIPs:          []string{fromNode.OverlayIP + "/32"},
					Endpoint:            "",
					PersistentKeepalive: 0,
					InterfaceName:       wgInterfaceName(fromNode.Name),
					ListenPort:          routerListenPort,
					LocalTransitIP:      routerLocalTransit,
					RemoteTransitIP:     routerRemoteTransit,
					LocalLinkLocal:      routerLocalLL,
					RemoteLinkLocal:     routerRemoteLL,
					IsClientPeer:        true,
					ClientOverlayIP:     fromNode.OverlayIP,
				}

				peerMap[toNode.ID] = append(peerMap[toNode.ID], routerPeer)
				addedPeers[reversePeerKey] = true
			}
			addedPeers[peerKey] = true
			continue
		}

		// 判断当前 edge 的方向与 alloc 的方向是否一致
		isForward := alloc.fromNodeID == fromNode.ID

		toKey, _ := keys[toNode.ID]
		fromKey, _ := keys[fromNode.ID]

		// === 计算 endpoint（用户指定端口优先，否则使用预分配的端口） ===
		endpoint := ""
		if edge.EndpointHost != "" {
			var portToUse int
			if edge.EndpointPort > 0 {
				// 用户指定了 NAT/端口转发覆盖端口
				portToUse = edge.EndpointPort
			} else {
				// 自动分配：使用对端接口的已分配监听端口
				if isForward {
					portToUse = alloc.toPort
				} else {
					portToUse = alloc.fromPort
				}
			}
			endpoint = formatEndpoint(edge.EndpointHost, portToUse)
		}

		// === 计算 PersistentKeepalive ===
		keepalive := 0
		hasReverseEdge := enabledEdgeDirections[toNode.ID+"->"+fromNode.ID]
		if !fromNode.Capabilities.CanAcceptInbound || !hasReverseEdge {
			keepalive = 25
		}

		// === 确定本端资源 ===
		var fromListenPort int
		var localTransit, remoteTransit, localLL, remoteLL string
		if isForward {
			fromListenPort = alloc.fromPort
			localTransit = alloc.localTransit
			remoteTransit = alloc.remoteTransit
			localLL = alloc.localLL
			remoteLL = alloc.remoteLL
		} else {
			fromListenPort = alloc.toPort
			localTransit = alloc.remoteTransit
			remoteTransit = alloc.localTransit
			localLL = alloc.remoteLL
			remoteLL = alloc.localLL
		}

		ifaceName := wgInterfaceName(toNode.Name)
		allowedIPs := []string{"0.0.0.0/0", "::/0"}

		// 该链路的 rxcost 覆盖值（D63）：正向与反向 peer 共用同一条 edge，因此取同一值。
		linkCost := deriveLinkCost(&edge)

		// 如果 toNode 是 client，创建 router 侧的带 IsClientPeer 标记的 PeerInfo
		isToClient := toNode.Role == "client"

		peer := PeerInfo{
			NodeID:              toNode.ID,
			NodeName:            toNode.Name,
			PublicKey:           toKey.PublicKey,
			OverlayIP:           toNode.OverlayIP,
			AllowedIPs:          allowedIPs,
			Endpoint:            endpoint,
			PersistentKeepalive: keepalive,
			InterfaceName:       ifaceName,
			ListenPort:          fromListenPort,
			LocalTransitIP:      localTransit,
			RemoteTransitIP:     remoteTransit,
			LocalLinkLocal:      localLL,
			RemoteLinkLocal:     remoteLL,
			IsClientPeer:        isToClient,
			ClientOverlayIP:     "",
			LinkCost:            linkCost,
		}
		if isToClient {
			peer.AllowedIPs = []string{toNode.OverlayIP + "/32"}
			peer.ClientOverlayIP = toNode.OverlayIP
		}

		peerMap[fromNode.ID] = append(peerMap[fromNode.ID], peer)
		addedPeers[peerKey] = true

		// === 自动生成反向 peer（跳过 client 的反向——client 侧使用 wg0） ===
		if isToClient {
			addedPeers[toNode.ID+"->"+fromNode.ID] = true
			continue
		}

		reversePeerKey := toNode.ID + "->" + fromNode.ID
		if !addedPeers[reversePeerKey] {
			reverseKeepalive := 0
			if !toNode.Capabilities.CanAcceptInbound {
				reverseKeepalive = 25
			}

			reverseIfaceName := wgInterfaceName(fromNode.Name)

			// fromNode 接口的已分配监听端口（反向 peer 回连 fromNode 时使用）
			fromSideListenPort := alloc.fromPort
			if !isForward {
				fromSideListenPort = alloc.toPort
			}

			// 解析反向 peer 的 endpoint：
			//  1. 存在显式反向 edge 且带 host 时，按正向规则解析（用户指定端口优先，否则用 fromNode 已分配端口）；
			//  2. 否则若 fromNode 具备公网可达能力且配置了 public endpoint，回退到 fromNode 的公网 host
			//     + fromNode 已分配的监听端口（绝不使用 public_endpoints[0].Port——那是节点可达提示，
			//     而非本链路的监听端口，误用会在服务端重现端口归属 bug）。
			reverseEndpoint := ""
			if reverseEdge, ok := edgeMap[toNode.ID+"->"+fromNode.ID]; ok && reverseEdge.EndpointHost != "" {
				if reverseEdge.EndpointPort > 0 {
					// 用户指定了 NAT/端口转发覆盖端口
					reverseEndpoint = formatEndpoint(reverseEdge.EndpointHost, reverseEdge.EndpointPort)
				} else {
					// 自动分配：使用 fromNode 接口的已分配监听端口
					reverseEndpoint = formatEndpoint(reverseEdge.EndpointHost, fromSideListenPort)
				}
			} else if fromNode.Capabilities.HasPublicIP && len(fromNode.PublicEndpoints) > 0 {
				// 回退：无反向 edge（或其 host 为空）且 fromNode 公网可达
				reverseEndpoint = formatEndpoint(fromNode.PublicEndpoints[0].Host, fromSideListenPort)
			}

			// 反向 peer 的资源与正向互换
			var toListenPort int
			var revLocalTransit, revRemoteTransit, revLocalLL, revRemoteLL string
			if isForward {
				toListenPort = alloc.toPort
				revLocalTransit = alloc.remoteTransit
				revRemoteTransit = alloc.localTransit
				revLocalLL = alloc.remoteLL
				revRemoteLL = alloc.localLL
			} else {
				toListenPort = alloc.fromPort
				revLocalTransit = alloc.localTransit
				revRemoteTransit = alloc.remoteTransit
				revLocalLL = alloc.localLL
				revRemoteLL = alloc.remoteLL
			}

			reversePeer := PeerInfo{
				NodeID:              fromNode.ID,
				NodeName:            fromNode.Name,
				PublicKey:           fromKey.PublicKey,
				OverlayIP:           fromNode.OverlayIP,
				AllowedIPs:          allowedIPs,
				Endpoint:            reverseEndpoint,
				PersistentKeepalive: reverseKeepalive,
				InterfaceName:       reverseIfaceName,
				ListenPort:          toListenPort,
				LocalTransitIP:      revLocalTransit,
				RemoteTransitIP:     revRemoteTransit,
				LocalLinkLocal:      revLocalLL,
				RemoteLinkLocal:     revRemoteLL,
				// 反向 peer 与正向共用同一条 edge，沿用同一 rxcost 覆盖值（D63）。
				LinkCost: linkCost,
			}

			peerMap[toNode.ID] = append(peerMap[toNode.ID], reversePeer)
			addedPeers[reversePeerKey] = true
		}
	}

	return peerMap, allocations, nil
}

// allocateTransitPair 根据序号和 transitCIDR 分配一对 transit IPv4 地址
// 如果 transitCIDR 为空，使用默认 defaultTransitCIDR（10.10.0.0/24）
// 每对占 2 个地址：pair N → (network+2N+1, network+2N+2)
// 地址池只跨可用主机区间 [network+1, broadcast-1]：网络地址与广播地址绝不分配（审计项 D48）。
// 当任一地址需要落到网络地址、广播地址或子网范围之外时，返回地址池耗尽错误。
//
// 签名稳定：后续阶段会在此函数之上重写 pair 分配主循环以支持 pin，
// 因此保持 (index, transitCIDR) -> (ip1, ip2, error) 的形态不变。
func allocateTransitPair(index int, transitCIDR string) (string, string, error) {
	if transitCIDR == "" {
		transitCIDR = defaultTransitCIDR
	}

	_, ipNet, err := net.ParseCIDR(transitCIDR)
	if err != nil {
		return "", "", fmt.Errorf("无效的 transit CIDR %q: %w", transitCIDR, err)
	}

	baseIP := ipNet.IP.To4()
	if baseIP == nil {
		return "", "", fmt.Errorf("transit CIDR 必须为 IPv4: %q", transitCIDR)
	}

	// 从掩码通用地推导网络地址与广播地址（不针对 /24 硬编码）。
	networkAddr := binary.BigEndian.Uint32(baseIP)
	maskBits, _ := ipNet.Mask.Size()
	// hostBits = 32 - maskBits；广播地址 = 网络地址 | (2^hostBits - 1)。
	// 对 /31、/32 这类没有可用广播位的掩码做保守处理：直接判定地址池容不下任何一对。
	hostBits := 32 - maskBits
	if hostBits < 2 {
		return "", "", fmt.Errorf("transit 地址池已耗尽（CIDR: %s，index: %d）", transitCIDR, index)
	}
	hostMask := uint32(1)<<uint(hostBits) - 1
	broadcastAddr := networkAddr | hostMask

	offset := uint32(2*index + 1)
	addr1 := networkAddr + offset
	addr2 := networkAddr + offset + 1

	// 越界（包含整数回绕导致的 addr2 < addr1）、命中网络地址或广播地址，一律视为地址池耗尽。
	// 可用主机区间是开区间 (networkAddr, broadcastAddr)，即 [networkAddr+1, broadcastAddr-1]。
	if addr2 < addr1 ||
		addr1 <= networkAddr || addr1 >= broadcastAddr ||
		addr2 <= networkAddr || addr2 >= broadcastAddr {
		return "", "", fmt.Errorf("transit 地址池已耗尽（CIDR: %s，index: %d）", transitCIDR, index)
	}

	ip1 := make(net.IP, 4)
	ip2 := make(net.IP, 4)
	binary.BigEndian.PutUint32(ip1, addr1)
	binary.BigEndian.PutUint32(ip2, addr2)

	return ip1.String(), ip2.String(), nil
}

// pinKey 计算一条链路的规范标识：两个节点 ID 排序后用竖线拼接。
// 方向无关——pinKey(A, B) == pinKey(B, A)——因此反向画 edge 不改变其分配身份（I3）。
// 详见 docs/spec/compiler/allocation-stability.md（Canonical link key）。
func pinKey(nodeA, nodeB string) string {
	if nodeA <= nodeB {
		return nodeA + "|" + nodeB
	}
	return nodeB + "|" + nodeA
}

// transitPoolPairCount 返回某个 transit CIDR 池可用的 pair 数量（pair index 上界）。
// 与 allocateTransitPair 同一套掩码推导：可用主机区间为 (network, broadcast)，
// 即 2^hostBits - 2 个主机地址，每对占两个 → (2^hostBits - 2) / 2 对。
// /24 → 127 对，/29 → 3 对，/30 → 1 对；hostBits < 2（/31、/32）→ 0 对。
func transitPoolPairCount(transitCIDR string) (int, error) {
	if transitCIDR == "" {
		transitCIDR = defaultTransitCIDR
	}
	_, ipNet, err := net.ParseCIDR(transitCIDR)
	if err != nil {
		return 0, fmt.Errorf("无效的 transit CIDR %q: %w", transitCIDR, err)
	}
	if ipNet.IP.To4() == nil {
		return 0, fmt.Errorf("transit CIDR 必须为 IPv4: %q", transitCIDR)
	}
	maskBits, _ := ipNet.Mask.Size()
	hostBits := 32 - maskBits
	if hostBits < 2 {
		return 0, nil
	}
	usableHosts := (uint64(1) << uint(hostBits)) - 2
	return int(usableHosts / 2), nil
}

// gapFillTransitPair 为一条未 pin 的链路在 per-CIDR 池里分配一对 transit IP。
//
// 取值策略：在 per-CIDR 池里从 index 0 起向上扫描，跳过任一地址已被预留（usedTransitIPs）的
// pair，命中首个两端都空闲的 pair 即返回；整池都满则返回干净的耗尽错误。
//
// 该函数本身是「池 + 预留集合」的纯函数；其 delete/re-add 幂等（Spec B G1）由调用侧保证：
// Pass 1 阶段 4 先预留所有 pin、再按 pinKey 排序遍历未 pin 链路。因此一条链路看到的预留集合
// 只取决于「全拓扑当前的 pin」与「pinKey 更小的未 pin 链路」，而与该链路自身的删除/重加历史、
// 以及数组位置无关——删除再重加同一对节点会重现同一最低空闲 pair（满足 I2/I9）。
//
// 这正是 docs/spec/compiler/allocation-stability.md「Hash-seeded gap-fill」一节的规范要求：
// 「the order in which candidate links are assigned MUST be deterministic in pinKey
// （iterate unpinned links sorted by pinKey, and within a pool pick the lowest free slot）」。
func gapFillTransitPair(transitCIDR string, transitUsed func(cidr, ip string) bool) (string, string, error) {
	poolPairs, err := transitPoolPairCount(transitCIDR)
	if err != nil {
		return "", "", err
	}
	if poolPairs <= 0 {
		return "", "", fmt.Errorf("transit 地址池已耗尽（CIDR: %s）", transitCIDR)
	}
	for index := 0; index < poolPairs; index++ {
		ip1, ip2, err := allocateTransitPair(index, transitCIDR)
		if err != nil {
			// 池内 index 理应都可用；防御性跳过任何意外的越界 index。
			continue
		}
		if transitUsed(transitCIDR, ip1) || transitUsed(transitCIDR, ip2) {
			continue
		}
		return ip1, ip2, nil
	}
	return "", "", fmt.Errorf("transit 地址池已耗尽（CIDR: %s，共 %d 对均已占用）", transitCIDR, poolPairs)
}

// gapFillLinkLocalPair 为一条未 pin 的链路分配一对 IPv6 link-local。
// 与 transit 同构：从 index 0 起向上扫描，跳过任一端已被预留（usedLinkLocals）的 pair，
// 命中首个两端都空闲的 pair 即返回。fe80::/10 对任何实际机群规模都「事实上无限」（I6），
// 故扫描必然在有限步内成功。delete/re-add 幂等同样由调用侧的「先预留、再按 pinKey 遍历」保证。
func gapFillLinkLocalPair(usedLinkLocals map[string]bool) (string, string) {
	for index := 0; ; index++ {
		local, remote := allocateLinkLocalPair(index)
		if usedLinkLocals[local] || usedLinkLocals[remote] {
			continue
		}
		return local, remote
	}
}

// lowestFreePort 返回某节点不低于其 base listen_port 的最低空闲端口（在 usedPorts 中跳过已用值）。
// base 默认 51820。有效端口不得超过 65535（审计项 D11）：超过即返回干净的编译期错误，
// 避免渲染出 wg-quick 在部署期才会拒绝的非法端口。
func lowestFreePort(node *model.Node, usedPorts map[string]map[int]bool) (int, error) {
	base := node.ListenPort
	if base == 0 {
		base = 51820
	}
	used := usedPorts[node.ID]
	for port := base; port <= 65535; port++ {
		if used == nil || !used[port] {
			return port, nil
		}
	}
	return 0, fmt.Errorf("节点 %s 的有效监听端口已无法在 [%d, 65535] 区间内分配：请降低该节点的 listen_port 或减少其连接数",
		node.Name, base)
}

// deriveLinkCost 从 edge 推导该链路的 Babel rxcost 覆盖值（D63）。
// 优先使用 Priority（>0），否则退回 Weight（>0），两者皆未设置时返回 0
// （0 表示交由角色 preset 的默认 cost 处理，渲染器据此决定是否省略 rxcost token）。
func deriveLinkCost(edge *model.Edge) int {
	if edge == nil {
		return 0
	}
	if edge.Priority > 0 {
		return edge.Priority
	}
	if edge.Weight > 0 {
		return edge.Weight
	}
	return 0
}

// allocateLinkLocalPair 根据序号分配一对 IPv6 link-local 地址。
// IPv6 文本是十六进制（审计项 D70）：必须用 %x 而非 %d，否则 fe80::11 会被解析成十进制 17——
// 与文档承诺的「连续十六进制编号」相矛盾。link-local 序号沿用同一池的 pair index。
// pair 0: fe80::1, fe80::2
// pair 1: fe80::3, fe80::4
// pair 5: fe80::b, fe80::c
func allocateLinkLocalPair(index int) (string, string) {
	base := 2*index + 1
	return fmt.Sprintf("fe80::%x", base), fmt.Sprintf("fe80::%x", base+1)
}

// deriveAllowedIPs 计算 AllowedIPs（保留兼容函数）
func deriveAllowedIPs(node *model.Node) []string {
	if node.OverlayIP == "" {
		return []string{}
	}
	return []string{node.OverlayIP + "/32"}
}

// wgInterfaceName 生成 WireGuard 接口名（薄封装）。
// 规范实现已上移至 internal/naming（Spec D，docs/spec/artifacts/naming.md），
// 由 renderer、compiler、validator 三层共用，消除此前的重复实现并打破导入环。
// 此处保留未导出名称仅为继续供包内调用方与既有测试使用，行为与
// naming.WgInterfaceName 完全一致。
func wgInterfaceName(remoteName string) string {
	return naming.WgInterfaceName(remoteName)
}

// formatEndpoint 格式化 endpoint 地址
func formatEndpoint(host string, port int) string {
	if isIPv6(host) {
		return "[" + host + "]:" + itoa(port)
	}
	return host + ":" + itoa(port)
}

func isIPv6(host string) bool {
	for _, c := range host {
		if c == ':' {
			return true
		}
	}
	return false
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	result := ""
	for n > 0 {
		result = string(rune('0'+n%10)) + result
		n /= 10
	}
	return result
}

// GenerateRouterID 生成一个稳定的 Babel router-id（MAC-48 格式）
// 基于节点 ID 的 SHA-256 hash 生成，确保稳定且唯一
func GenerateRouterID(nodeID string) string {
	h := sha256.Sum256([]byte(nodeID))

	// 取前 6 字节作为 MAC-48
	b0 := h[0]
	b0 = (b0 | 0x02) & 0xFE // 设置 locally administered bit, 清除 multicast bit
	b1 := h[1]
	b2 := h[2]
	b3 := h[3]
	b4 := h[4]
	b5 := h[5]

	return fmt.Sprintf("%02x:%02x:%02x:%02x:%02x:%02x", b0, b1, b2, b3, b4, b5)
}

// ClientPeerInfo 描述 client 节点的 wg0 配置所需信息
type ClientPeerInfo struct {
	// Client 节点信息
	NodeID    string
	NodeName  string
	OverlayIP string
	MTU       int

	// Client 的 WireGuard 私钥
	PrivateKey string

	// Router 侧信息
	RouterPublicKey string
	RouterEndpoint  string // host:port

	// 域 CIDR 列表（用作 AllowedIPs）
	DomainCIDRs []string

	// Client 的监听端口
	ListenPort int
}

// DeriveClientConfigs 为所有 client 节点生成 wg0 配置信息
func DeriveClientConfigs(topo *model.Topology, keys map[string]KeyPair, allocations map[string]*pairAllocation) map[string]*ClientPeerInfo {
	configs := make(map[string]*ClientPeerInfo)

	nodeMap := make(map[string]*model.Node)
	for i := range topo.Nodes {
		nodeMap[topo.Nodes[i].ID] = &topo.Nodes[i]
	}

	for _, node := range topo.Nodes {
		if node.Role != "client" {
			continue
		}

		// 找到 client 的唯一出站 edge
		var clientEdge *model.Edge
		for i := range topo.Edges {
			e := &topo.Edges[i]
			if e.IsEnabled && e.FromNodeID == node.ID {
				clientEdge = e
				break
			}
		}
		if clientEdge == nil {
			continue
		}

		routerNode := nodeMap[clientEdge.ToNodeID]
		if routerNode == nil {
			continue
		}

		routerKey, _ := keys[routerNode.ID]
		clientKey, _ := keys[node.ID]

		// 获取 router 侧的监听端口
		peerKey := node.ID + "->" + routerNode.ID
		alloc := allocations[peerKey]
		var routerPort int
		if alloc != nil {
			if alloc.fromNodeID == node.ID {
				routerPort = alloc.toPort
			} else {
				routerPort = alloc.fromPort
			}
		}

		// 构建 endpoint（用户指定端口优先，否则使用自动分配的 router 端口）
		routerEndpoint := ""
		if clientEdge.EndpointHost != "" {
			var portToUse int
			if clientEdge.EndpointPort > 0 {
				portToUse = clientEdge.EndpointPort
			} else if routerPort > 0 {
				portToUse = routerPort
			}
			if portToUse > 0 {
				routerEndpoint = formatEndpoint(clientEdge.EndpointHost, portToUse)
			}
		}

		// AllowedIPs 前缀集合（D30，Decision 6）：
		// client 的 wg0 是它通往整个 overlay 的唯一隧道，因此 AllowedIPs 不能只覆盖
		// 自身所在域，否则跨域 overlay、router 的域外 /32、以及 transit 网段都会在 client
		// 侧黑洞。这里取「所有域的 CIDR」并集「每个域解析后的 transit CIDR」（domain.TransitCIDR
		// 为空时回退默认 10.10.0.0/24，与 allocateTransitPair 的解析规则一致）。
		// 按 topo.Domains 的切片顺序遍历以保证确定性，并去重。
		var domainCIDRs []string
		seenCIDR := make(map[string]bool)
		appendCIDR := func(cidr string) {
			if cidr == "" || seenCIDR[cidr] {
				return
			}
			seenCIDR[cidr] = true
			domainCIDRs = append(domainCIDRs, cidr)
		}
		for i := range topo.Domains {
			appendCIDR(topo.Domains[i].CIDR)
		}
		for i := range topo.Domains {
			transitCIDR := topo.Domains[i].TransitCIDR
			if transitCIDR == "" {
				transitCIDR = defaultTransitCIDR
			}
			appendCIDR(transitCIDR)
		}

		// Client 监听端口
		listenPort := node.ListenPort
		if listenPort == 0 {
			listenPort = 51820
		}

		configs[node.ID] = &ClientPeerInfo{
			NodeID:          node.ID,
			NodeName:        node.Name,
			OverlayIP:       node.OverlayIP,
			MTU:             node.MTU,
			PrivateKey:      clientKey.PrivateKey,
			RouterPublicKey: routerKey.PublicKey,
			RouterEndpoint:  routerEndpoint,
			DomainCIDRs:     domainCIDRs,
			ListenPort:      listenPort,
		}
	}

	return configs
}
