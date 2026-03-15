package model

// Topology 是顶层容器，包含完整的组网拓扑定义
type Topology struct {
	// 项目元信息
	Project Project `json:"project"`

	// 网络域列表
	Domains []Domain `json:"domains"`

	// 节点列表
	Nodes []Node `json:"nodes"`

	// 可达箭头列表
	Edges []Edge `json:"edges"`

	// 路由策略列表（简化版）
	RoutePolicies []RoutePolicy `json:"route_policies,omitempty"`
}

// Project 项目元信息
type Project struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Version     string `json:"version,omitempty"`
}

// Domain 网络域，表示一个逻辑 overlay 地址空间
type Domain struct {
	ID             string   `json:"id"`
	Name           string   `json:"name"`
	CIDR           string   `json:"cidr"`
	Description    string   `json:"description,omitempty"`
	AllocationMode string   `json:"allocation_mode"` // "auto" | "manual"
	RoutingMode    string   `json:"routing_mode"`    // "static" | "babel" | "none"
	ReservedRanges []string `json:"reserved_ranges,omitempty"`
}

// Node 节点，表示一台主机
type Node struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Hostname string `json:"hostname,omitempty"`
	Platform string `json:"platform,omitempty"` // "debian" | "ubuntu"

	// 角色：peer, router, relay, gateway
	Role string `json:"role"`

	// 归属的 Domain ID（第一阶段：单主 Domain）
	DomainID string `json:"domain_id"`

	// Overlay IP，可手动指定或自动分配
	OverlayIP string `json:"overlay_ip,omitempty"`

	// WireGuard 监听端口
	ListenPort int `json:"listen_port,omitempty"`

	// 节点能力
	Capabilities NodeCapabilities `json:"capabilities"`
}

// NodeCapabilities 节点能力描述
type NodeCapabilities struct {
	CanAcceptInbound bool `json:"can_accept_inbound"`
	CanForward       bool `json:"can_forward"`
	CanRelay         bool `json:"can_relay"`
	HasPublicIP      bool `json:"has_public_ip"`
}

// Edge 可达箭头，表示节点间的连接候选关系
// 箭头表示 "from 节点可以主动访问 to 节点的某个暴露入口"
type Edge struct {
	ID         string `json:"id"`
	FromNodeID string `json:"from_node_id"`
	ToNodeID   string `json:"to_node_id"`

	// 连接类型：direct, public-endpoint, relay-path, candidate
	Type string `json:"type"`

	// 目标 endpoint
	EndpointHost string `json:"endpoint_host,omitempty"`
	EndpointPort int    `json:"endpoint_port,omitempty"`

	// 优先级与权重
	Priority int `json:"priority,omitempty"`
	Weight   int `json:"weight,omitempty"`

	// 传输协议：udp, tcp
	Transport string `json:"transport,omitempty"`

	// 是否启用
	IsEnabled bool `json:"is_enabled"`

	// 备注
	Notes string `json:"notes,omitempty"`
}

// RoutePolicy 路由策略（第一阶段简化版）
type RoutePolicy struct {
	ID              string `json:"id"`
	DomainID        string `json:"domain_id"`
	DestinationCIDR string `json:"destination_cidr"`
	NextHopNodeID   string `json:"next_hop_node_id,omitempty"`
	Metric          int    `json:"metric,omitempty"`
	Notes           string `json:"notes,omitempty"`
}
