package model

// Topology 完整的网络拓扑定义
type Topology struct {
	// 项目信息
	Project Project `json:"project"`

	// 网络域列表
	Domains []Domain `json:"domains"`

	// 节点列表
	Nodes []Node `json:"nodes"`

	// 边（连接）列表
	Edges []Edge `json:"edges"`

	// 路由策略（可选）
	RoutePolicies []RoutePolicy `json:"route_policies,omitempty"`
}

// Project 项目元信息
type Project struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Version     string `json:"version,omitempty"`
}

// Domain 网络域定义，代表一个 overlay 地址空间
type Domain struct {
	ID             string   `json:"id"`
	Name           string   `json:"name"`
	CIDR           string   `json:"cidr"`
	Description    string   `json:"description,omitempty"`
	AllocationMode string   `json:"allocation_mode"` // "auto" | "manual"
	RoutingMode    string   `json:"routing_mode"`    // "static" | "babel" | "none"
	ReservedRanges []string `json:"reserved_ranges,omitempty"`

	// Transit（点对点链路）地址池，用于 per-peer WireGuard 接口地址分配
	// 为空时自动使用 10.10.0.0/24
	TransitCIDR string `json:"transit_cidr,omitempty"`
}

// Node 网络节点定义
type Node struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Hostname string `json:"hostname,omitempty"`
	Platform string `json:"platform,omitempty"` // "debian" | "ubuntu"

	// 角色：peer, router, relay, gateway, client
	Role string `json:"role"`

	// 所属 Domain ID（必须引用已有 Domain）
	DomainID string `json:"domain_id"`

	// Overlay IP，编译时自动分配
	OverlayIP string `json:"overlay_ip,omitempty"`

	// WireGuard 基础监听端口（每个 peer 接口会从此端口递增）
	ListenPort int `json:"listen_port,omitempty"`

	// WireGuard 接口 MTU（0 = 使用系统默认值，通常 1420）
	MTU int `json:"mtu,omitempty"`

	// Babel router-id（MAC-48 格式，如 02:11:22:33:44:55）
	// 留空时由编译器自动生成
	RouterID string `json:"router_id,omitempty"`

	// 节点能力
	Capabilities NodeCapabilities `json:"capabilities"`

	// 是否固定 WireGuard 密钥
	FixedPrivateKey bool `json:"fixed_private_key,omitempty"`

	// WireGuard 密钥（仅当 fixed_private_key=true 时有效）
	WireGuardPrivateKey string `json:"wireguard_private_key,omitempty"`
	WireGuardPublicKey  string `json:"wireguard_public_key,omitempty"`

	// 公网可达地址映射（一个节点可以有多组 endpoint）
	PublicEndpoints []PublicEndpoint `json:"public_endpoints,omitempty"`

	// 额外路由前缀（gateway 用：向外宣告的网段）
	ExtraPrefixes []string `json:"extra_prefixes,omitempty"`

	// SSH 连接信息（用于自动部署）
	SSHAlias   string `json:"ssh_alias,omitempty"`   // ssh_config 中的 Host 别名
	SSHHost    string `json:"ssh_host,omitempty"`     // SSH 主机地址
	SSHPort    int    `json:"ssh_port,omitempty"`     // SSH 端口（默认 22）
	SSHUser    string `json:"ssh_user,omitempty"`     // SSH 用户名
	SSHKeyPath string `json:"ssh_key_path,omitempty"` // SSH 私钥路径
}

// PublicEndpoint 公网可达地址映射
type PublicEndpoint struct {
	ID   string `json:"id"`
	Host string `json:"host"`
	Port int    `json:"port"`
	Note string `json:"note,omitempty"`
}

// NodeCapabilities 节点能力声明
type NodeCapabilities struct {
	CanAcceptInbound bool `json:"can_accept_inbound"`
	CanForward       bool `json:"can_forward"`
	CanRelay         bool `json:"can_relay"`
	HasPublicIP      bool `json:"has_public_ip"`
}

// Edge 边定义，表示两个节点之间的连接意图
// 语义为 "from 主动连 to "
type Edge struct {
	ID         string `json:"id"`
	FromNodeID string `json:"from_node_id"`
	ToNodeID   string `json:"to_node_id"`

	// 连接类型：direct, public-endpoint, relay-path, candidate
	Type string `json:"type"`

	// 对端 endpoint（用户输入：0 = 自动分配，非零 = NAT/端口转发覆盖）
	EndpointHost string `json:"endpoint_host,omitempty"`
	EndpointPort int    `json:"endpoint_port,omitempty"`

	// 编译器分配的实际端口（只读，由编译器填充）
	CompiledPort int `json:"compiled_port,omitempty"`

	// 优先级
	Priority int `json:"priority,omitempty"`
	Weight   int `json:"weight,omitempty"`

	// 传输协议：udp, tcp
	Transport string `json:"transport,omitempty"`

	// 是否启用
	IsEnabled bool `json:"is_enabled"`

	// 备注
	Notes string `json:"notes,omitempty"`
}

// RoutePolicy 路由策略定义
type RoutePolicy struct {
	ID              string `json:"id"`
	DomainID        string `json:"domain_id"`
	DestinationCIDR string `json:"destination_cidr"`
	NextHopNodeID   string `json:"next_hop_node_id,omitempty"`
	Metric          int    `json:"metric,omitempty"`
	Notes           string `json:"notes,omitempty"`

	SourceSelector string `json:"source_selector,omitempty"`
	Action         string `json:"action,omitempty"`
	ApplyToNodeID  string `json:"apply_to_node_id,omitempty"`
}
