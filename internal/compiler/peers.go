package compiler

import (
	"github.com/kunorikiku/yet-another-overlay-generator/internal/model"
)

// KeyPair WireGuard 
type KeyPair struct {
	PrivateKey string
	PublicKey  string
}

// PeerInfo  Peer 
type PeerInfo struct {
	//  ID
	NodeID string

	// 
	NodeName string

	// 
	PublicKey string

	//  Overlay IP
	OverlayIP string

	// AllowedIPs（： overlay IP）
	AllowedIPs []string

	// Endpoint（）
	Endpoint string

	//  PersistentKeepalive
	PersistentKeepalive int

	// WireGuard （ Babel ）
	InterfaceName string
}

// DerivePeers  Edge  WireGuard Peer 
//  map[nodeID][]PeerInfo
func DerivePeers(topo *model.Topology, keys map[string]KeyPair) map[string][]PeerInfo {
	//  Domain 
	domainMap := make(map[string]*model.Domain)
	for i := range topo.Domains {
		domainMap[topo.Domains[i].ID] = &topo.Domains[i]
	}

	return derivePeersWithDomains(topo, keys, domainMap)
}

// derivePeersWithDomains ， Domain ， AllowedIPs
func derivePeersWithDomains(topo *model.Topology, keys map[string]KeyPair, domainMap map[string]*model.Domain) map[string][]PeerInfo {
	peerMap := make(map[string][]PeerInfo)

	// 
	nodeMap := make(map[string]*model.Node)
	for i := range topo.Nodes {
		nodeMap[topo.Nodes[i].ID] = &topo.Nodes[i]
	}

	//  peer 
	for _, node := range topo.Nodes {
		peerMap[node.ID] = []PeerInfo{}
	}

	// ， keepalive 
	enabledEdgeDirections := make(map[string]bool)
	for _, edge := range topo.Edges {
		if edge.IsEnabled {
			enabledEdgeDirections[edge.FromNodeID+"->"+edge.ToNodeID] = true
		}
	}

	//  peer（）
	// key: "localNodeID->remoteNodeID"
	addedPeers := make(map[string]bool)

	// ， peer 
	for _, edge := range topo.Edges {
		if !edge.IsEnabled {
			continue
		}

		fromNode := nodeMap[edge.FromNodeID]
		toNode := nodeMap[edge.ToNodeID]
		if fromNode == nil || toNode == nil {
			continue
		}

		//  "from  to"
		//  from  to  peer， edge  endpoint
		peerKey := fromNode.ID + "->" + toNode.ID
		if addedPeers[peerKey] {
			//  peer（ endpoint ，）
			continue
		}

		toKey, _ := keys[toNode.ID]

		//  endpoint
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

		//  PersistentKeepalive
		// 1. from （NAT ），
		// 2. （ B→A ），
		//    from  keepalive ，
		keepalive := 0
		hasReverseEdge := enabledEdgeDirections[toNode.ID+"->"+fromNode.ID]
		if !fromNode.Capabilities.CanAcceptInbound || !hasReverseEdge {
			keepalive = 25
		}

		// WireGuard ：wg-<peer-name >
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

		// B，（ Peer）
		reversePeerKey := toNode.ID + "->" + fromNode.ID
		if !addedPeers[reversePeerKey] {
			fromKey, _ := keys[fromNode.ID]

			//  PersistentKeepalive
			// ，  endpoint，
			//  keepalive  NAT 
			reverseKeepalive := 0
			if !toNode.Capabilities.CanAcceptInbound {
				reverseKeepalive = 25
			}

			reversePeer := PeerInfo{
				NodeID:              fromNode.ID,
				NodeName:            fromNode.Name,
				PublicKey:           fromKey.PublicKey,
				OverlayIP:           fromNode.OverlayIP,
				AllowedIPs:          deriveAllowedIPs(fromNode),
				Endpoint:            "", // ，
				PersistentKeepalive: reverseKeepalive,
				InterfaceName:       wgInterfaceName(toNode.ID, fromNode.ID),
			}

			peerMap[toNode.ID] = append(peerMap[toNode.ID], reversePeer)
			addedPeers[reversePeerKey] = true
		}
	}

	return peerMap
}

// deriveAllowedIPs  AllowedIPs
// ： overlay IP/32
func deriveAllowedIPs(node *model.Node) []string {
	if node.OverlayIP == "" {
		return []string{}
	}
	return []string{node.OverlayIP + "/32"}
}

// wgInterfaceName  WireGuard 
// ：wg<>， ID 
func wgInterfaceName(localID, remoteID string) string {
	// ： "wg-" + remoteID 
	// Linux  15 
	name := "wg-" + remoteID
	if len(name) > 15 {
		name = name[:15]
	}
	return name
}

// formatEndpoint  endpoint 
func formatEndpoint(host string, port int) string {
	//  IPv6
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
