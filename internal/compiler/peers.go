package compiler

import (
	"github.com/kunorikiku/yet-another-overlay-generator/internal/model"
)

// KeyPair WireGuard 密钥对
type KeyPair struct {
	PrivateKey string
	PublicKey  string
}

// PeerInfo 推导出的 Peer 信息
type PeerInfo struct {
	// 对端节点 ID
	NodeID string

	// 对端节点名称
	NodeName string

	// 对端公钥
	PublicKey string

	// 对端 Overlay IP
	OverlayIP string

	// AllowedIPs（点对点模式：只包含对端 overlay IP）
	AllowedIPs []string

	// Endpoint（若可达）
	Endpoint string

	// 是否需要 PersistentKeepalive
	PersistentKeepalive int

	// WireGuard 接口名称（用于 Babel 邻接）
	InterfaceName string
}

// DerivePeers 根据 Edge 推导每个节点的 WireGuard Peer 列表
// 返回 map[nodeID][]PeerInfo
func DerivePeers(topo *model.Topology, keys map[string]KeyPair) map[string][]PeerInfo {
	// 构建 Domain 索引
	domainMap := make(map[string]*model.Domain)
	for i := range topo.Domains {
		domainMap[topo.Domains[i].ID] = &topo.Domains[i]
	}

	return derivePeersWithDomains(topo, keys, domainMap)
}

// derivePeersWithDomains 内部实现，带 Domain 索引，使用角色语义推导 AllowedIPs
func derivePeersWithDomains(topo *model.Topology, keys map[string]KeyPair, domainMap map[string]*model.Domain) map[string][]PeerInfo {
	peerMap := make(map[string][]PeerInfo)

	// 构建节点索引
	nodeMap := make(map[string]*model.Node)
	for i := range topo.Nodes {
		nodeMap[topo.Nodes[i].ID] = &topo.Nodes[i]
	}

	// 为每个节点初始化空的 peer 列表
	for _, node := range topo.Nodes {
		peerMap[node.ID] = []PeerInfo{}
	}

	// 跟踪每个节点已添加的 peer（避免重复）
	// key: "localNodeID->remoteNodeID"
	addedPeers := make(map[string]bool)

	// 遍历所有启用的边，推导 peer 关系
	for _, edge := range topo.Edges {
		if !edge.IsEnabled {
			continue
		}

		fromNode := nodeMap[edge.FromNodeID]
		toNode := nodeMap[edge.ToNodeID]
		if fromNode == nil || toNode == nil {
			continue
		}

		// 边表示 "from 可以访问 to"
		// 因此 from 节点需要将 to 节点作为 peer，使用 edge 中的 endpoint
		peerKey := fromNode.ID + "->" + toNode.ID
		if addedPeers[peerKey] {
			// 已有此 peer（可能是多 endpoint 的情况，第一阶段取第一条）
			continue
		}

		toKey, _ := keys[toNode.ID]

		// 构建 endpoint
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

		// 判断是否需要 PersistentKeepalive
		// 如果 from 不能接受入站连接（NAT 后），需要保活
		keepalive := 0
		if !fromNode.Capabilities.CanAcceptInbound {
			keepalive = 25
		}

		// WireGuard 接口名称：wg-<peer-name 缩写>
		ifaceName := wgInterfaceName(fromNode.ID, toNode.ID)

		peer := PeerInfo{
			NodeID:              toNode.ID,
			NodeName:            toNode.Name,
			PublicKey:           toKey.PublicKey,
			OverlayIP:           toNode.OverlayIP,
			AllowedIPs:          deriveAllowedIPs(toNode),
			Endpoint:            endpoint,
			PersistentKeepalive: keepalive,
			InterfaceName:       ifaceName,
		}

		peerMap[fromNode.ID] = append(peerMap[fromNode.ID], peer)
		addedPeers[peerKey] = true
	}

	return peerMap
}

// deriveAllowedIPs 推导对端的 AllowedIPs
// 点对点模式：只包含对端 overlay IP/32
func deriveAllowedIPs(node *model.Node) []string {
	if node.OverlayIP == "" {
		return []string{}
	}
	return []string{node.OverlayIP + "/32"}
}

// wgInterfaceName 生成 WireGuard 接口名称
// 格式：wg<编号>，基于两个节点 ID 生成确定性名称
func wgInterfaceName(localID, remoteID string) string {
	// 简单实现：使用 "wg-" + remoteID 的前几个字符
	// Linux 接口名限制 15 字符
	name := "wg-" + remoteID
	if len(name) > 15 {
		name = name[:15]
	}
	return name
}

// formatEndpoint 格式化 endpoint 地址
func formatEndpoint(host string, port int) string {
	// 检查是否是 IPv6
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
