package compiler

import (
	"crypto/sha256"
	"fmt"
	"strings"

	"github.com/kunorikiku/yet-another-overlay-generator/internal/model"
)

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
}

// DerivePeers 根据 Edge 拓扑推导每个节点的 WireGuard Peer 列表
// 新架构：每个 peer 一个独立接口
// 返回 map[nodeID][]PeerInfo
func DerivePeers(topo *model.Topology, keys map[string]KeyPair) (map[string][]PeerInfo, map[string]*pairAllocation) {
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
func derivePeersWithDomains(topo *model.Topology, keys map[string]KeyPair, domainMap map[string]*model.Domain) (map[string][]PeerInfo, map[string]*pairAllocation) {
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

	// ======== Pass 1: 预分配资源 ========
	allocations := make(map[string]*pairAllocation) // key: "fromNodeID->toNodeID"
	addedPairs := make(map[string]bool)
	transitPairIndex := 0
	nodePortOffset := make(map[string]int)

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

		// 如果这对节点（任一方向）已分配过，跳过
		if addedPairs[peerKey] || addedPairs[reversePeerKey] {
			continue
		}

		// 分配 transit IP 对
		localTransit, remoteTransit := allocateTransitPair(transitPairIndex)
		localLL, remoteLL := allocateLinkLocalPair(transitPairIndex)
		transitPairIndex++

		// Client 节点不参与 per-peer 端口分配（使用单一 wg0 接口）
		isFromClient := fromNode.Role == "client"
		isToClient := toNode.Role == "client"

		// 分配 fromNode 的监听端口
		var fromListenPort int
		if !isFromClient {
			fromBasePort := fromNode.ListenPort
			if fromBasePort == 0 {
				fromBasePort = 51820
			}
			fromListenPort = fromBasePort + nodePortOffset[fromNode.ID]
			nodePortOffset[fromNode.ID]++
		}

		// 分配 toNode 的监听端口
		var toListenPort int
		if !isToClient {
			toBasePort := toNode.ListenPort
			if toBasePort == 0 {
				toBasePort = 51820
			}
			toListenPort = toBasePort + nodePortOffset[toNode.ID]
			nodePortOffset[toNode.ID]++
		}

		alloc := &pairAllocation{
			fromNodeID:    fromNode.ID,
			toNodeID:      toNode.ID,
			fromPort:      fromListenPort,
			toPort:        toListenPort,
			localTransit:  localTransit,
			remoteTransit: remoteTransit,
			localLL:       localLL,
			remoteLL:      remoteLL,
		}

		allocations[peerKey] = alloc
		allocations[reversePeerKey] = alloc
		addedPairs[peerKey] = true
		addedPairs[reversePeerKey] = true
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

			// 查找反向 edge 的 endpoint host（用户指定端口优先，否则使用预分配的端口）
			reverseEndpoint := ""
			if reverseEdge, ok := edgeMap[toNode.ID+"->"+fromNode.ID]; ok {
				if reverseEdge.EndpointHost != "" {
					var portToUse int
					if reverseEdge.EndpointPort > 0 {
						// 用户指定了 NAT/端口转发覆盖端口
						portToUse = reverseEdge.EndpointPort
					} else {
						// 自动分配：使用 fromNode 接口的已分配监听端口
						if isForward {
							portToUse = alloc.fromPort
						} else {
							portToUse = alloc.toPort
						}
					}
					reverseEndpoint = formatEndpoint(reverseEdge.EndpointHost, portToUse)
				}
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
			}

			peerMap[toNode.ID] = append(peerMap[toNode.ID], reversePeer)
			addedPeers[reversePeerKey] = true
		}
	}

	return peerMap, allocations
}

// allocateTransitPair 根据序号分配一对 transit IPv4 地址
// 使用 10.10.0.0/24 地址池，每对占 2 个地址
func allocateTransitPair(index int) (string, string) {
	// pair 0: 10.10.0.1, 10.10.0.2
	// pair 1: 10.10.0.3, 10.10.0.4
	// pair N: 10.10.0.(2N+1), 10.10.0.(2N+2)
	base := 2*index + 1
	return fmt.Sprintf("10.10.0.%d", base), fmt.Sprintf("10.10.0.%d", base+1)
}

// allocateLinkLocalPair 根据序号分配一对 IPv6 link-local 地址
func allocateLinkLocalPair(index int) (string, string) {
	// pair 0: fe80::1, fe80::2
	// pair 1: fe80::3, fe80::4
	base := 2*index + 1
	return fmt.Sprintf("fe80::%d", base), fmt.Sprintf("fe80::%d", base+1)
}

// deriveAllowedIPs 计算 AllowedIPs（保留兼容函数）
func deriveAllowedIPs(node *model.Node) []string {
	if node.OverlayIP == "" {
		return []string{}
	}
	return []string{node.OverlayIP + "/32"}
}

// wgInterfaceName 生成 WireGuard 接口名
// 格式：wg-<peername>，Linux 限制 15 字符
func wgInterfaceName(remoteName string) string {
	// 清理名称：小写、替换非法字符
	clean := strings.ToLower(remoteName)
	clean = strings.Map(func(r rune) rune {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' {
			return r
		}
		return '-'
	}, clean)

	name := "wg-" + clean
	if len(name) > 15 {
		name = name[:15]
	}
	return name
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

	domainMap := make(map[string]*model.Domain)
	for i := range topo.Domains {
		domainMap[topo.Domains[i].ID] = &topo.Domains[i]
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

		// 域 CIDR
		var domainCIDRs []string
		if domain, ok := domainMap[node.DomainID]; ok && domain.CIDR != "" {
			domainCIDRs = append(domainCIDRs, domain.CIDR)
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
