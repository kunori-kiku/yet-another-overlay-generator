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
}

// DerivePeers 根据 Edge 拓扑推导每个节点的 WireGuard Peer 列表
// 新架构：每个 peer 一个独立接口
// 返回 map[nodeID][]PeerInfo
func DerivePeers(topo *model.Topology, keys map[string]KeyPair) map[string][]PeerInfo {
	// 构建 Domain 索引
	domainMap := make(map[string]*model.Domain)
	for i := range topo.Domains {
		domainMap[topo.Domains[i].ID] = &topo.Domains[i]
	}

	return derivePeersWithDomains(topo, keys, domainMap)
}

// derivePeersWithDomains 核心推导逻辑
func derivePeersWithDomains(topo *model.Topology, keys map[string]KeyPair, domainMap map[string]*model.Domain) map[string][]PeerInfo {
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

	// 去重：已添加的 peer（避免重复生成）
	// key: "localNodeID->remoteNodeID"
	addedPeers := make(map[string]bool)

	// 全局 transit 地址分配计数器
	transitPairIndex := 0

	// 每个节点的端口偏移计数器
	nodePortOffset := make(map[string]int)

	// 遍历每条 edge，生成对应的点对点接口配置
	for _, edge := range topo.Edges {
		if !edge.IsEnabled {
			continue
		}

		fromNode := nodeMap[edge.FromNodeID]
		toNode := nodeMap[edge.ToNodeID]
		if fromNode == nil || toNode == nil {
			continue
		}

		// 检查是否已处理过这对节点
		peerKey := fromNode.ID + "->" + toNode.ID
		if addedPeers[peerKey] {
			continue
		}

		toKey, _ := keys[toNode.ID]
		fromKey, _ := keys[fromNode.ID]

		// === 计算 endpoint ===
		endpoint := ""
		if edge.EndpointHost != "" {
			port := edge.EndpointPort
			if port == 0 {
				port = toNode.ListenPort
			}
			if port > 0 {
				endpoint = formatEndpoint(edge.EndpointHost, port)
			} else {
				endpoint = edge.EndpointHost
			}
		}

		// === 计算 PersistentKeepalive ===
		keepalive := 0
		hasReverseEdge := enabledEdgeDirections[toNode.ID+"->"+fromNode.ID]
		if !fromNode.Capabilities.CanAcceptInbound || !hasReverseEdge {
			keepalive = 25
		}

		// === 分配 transit IP 对 ===
		localTransit, remoteTransit := allocateTransitPair(transitPairIndex)
		transitPairIndex++

		// === 分配 IPv6 link-local 对 ===
		localLL, remoteLL := allocateLinkLocalPair(transitPairIndex - 1) // use same index as transit pair

		// === 分配 ListenPort ===
		fromBasePort := fromNode.ListenPort
		if fromBasePort == 0 {
			fromBasePort = 51820
		}
		fromListenPort := fromBasePort + nodePortOffset[fromNode.ID]
		nodePortOffset[fromNode.ID]++

		// === 生成接口名 ===
		ifaceName := wgInterfaceName(toNode.Name)

		// === AllowedIPs：宽松策略 ===
		allowedIPs := []string{"0.0.0.0/0", "::/0"}

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
		}

		peerMap[fromNode.ID] = append(peerMap[fromNode.ID], peer)
		addedPeers[peerKey] = true

		// === 自动生成反向 peer ===
		reversePeerKey := toNode.ID + "->" + fromNode.ID
		if !addedPeers[reversePeerKey] {
			// 反向 keepalive
			reverseKeepalive := 0
			if !toNode.Capabilities.CanAcceptInbound {
				reverseKeepalive = 25
			}

			// 反向端口
			toBasePort := toNode.ListenPort
			if toBasePort == 0 {
				toBasePort = 51820
			}
			toListenPort := toBasePort + nodePortOffset[toNode.ID]
			nodePortOffset[toNode.ID]++

			reverseIfaceName := wgInterfaceName(fromNode.Name)

			// 查找反向 edge 的 endpoint
			reverseEndpoint := ""
			if reverseEdge, ok := edgeMap[toNode.ID+"->"+fromNode.ID]; ok {
				if reverseEdge.EndpointHost != "" {
					port := reverseEdge.EndpointPort
					if port == 0 {
						port = fromNode.ListenPort
					}
					if port > 0 {
						reverseEndpoint = formatEndpoint(reverseEdge.EndpointHost, port)
					} else {
						reverseEndpoint = reverseEdge.EndpointHost
					}
				}
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
				// transit 地址互换
				ListenPort:      toListenPort,
				LocalTransitIP:  remoteTransit,
				RemoteTransitIP: localTransit,
				// link-local 也互换
				LocalLinkLocal:  remoteLL,
				RemoteLinkLocal: localLL,
			}

			peerMap[toNode.ID] = append(peerMap[toNode.ID], reversePeer)
			addedPeers[reversePeerKey] = true
		}
	}

	return peerMap
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
