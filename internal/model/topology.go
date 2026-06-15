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

	// 分配方案版本号（粘性 pin 分配的 schema 版本，见 I10）。
	// 让未来对 pin 格式的改动能够检测并迁移旧拓扑，而不是把旧格式当成新格式静默误读。
	// 编译器在写回时统一标记为 compiler.AllocationSchemaVersion（当前为 1）。
	// 详见 docs/spec/compiler/allocation-stability.md（不变量 I10）。
	AllocSchemaVersion int `json:"alloc_schema_version,omitempty"`
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

	// WireGuard 接口 MTU（0 = 使用系统默认值，通常 1420）
	MTU int `json:"mtu,omitempty"`

	// mimic（transport=="tcp"）的 XDP 附着模式：空 / "skb" / "native"。
	// 空与 "skb" 等价：通用（generic）XDP，兼容几乎所有网卡（含不支持 native XDP 的
	// VPS virtio 网卡），是默认值。"native" 为驱动级 XDP，更快但需网卡/驱动支持；
	// 仅当操作员确认该节点网卡支持时才设置。详见 docs/spec/artifacts/mimic.md。
	XDPMode string `json:"xdp_mode,omitempty"`

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
	SSHAlias   string `json:"ssh_alias,omitempty"`    // ssh_config 中的 Host 别名
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

// Edge.Role 的取值常量。空值与 EdgeRolePrimary 等价（同归 primary class）。
const (
	// EdgeRolePrimary 主链路：与其它非 backup edge 一起折叠为一对节点的唯一一条链路。
	EdgeRolePrimary = "primary"
	// EdgeRoleBackup 备份链路：每条 backup edge 各自成为一条独立链路（独立分配 + 独立接口名）。
	EdgeRoleBackup = "backup"
)

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

	// 链路角色：区分一对节点之间的主链路（primary class）与备份链路。
	// 空值 / "primary" 归入 primary class——同一对节点的所有非 backup edge 折叠为一条链路；
	// "backup" 则每条 edge 各自成为一条独立链路，用于 Babel 基于 cost 的故障切换。
	// 详见 docs/spec/compiler/allocation-stability.md（Link identity with parallel edges）。
	Role string `json:"role,omitempty"`

	// 传输协议：udp, tcp
	Transport string `json:"transport,omitempty"`

	// 是否启用
	IsEnabled bool `json:"is_enabled"`

	// 备注
	Notes string `json:"notes,omitempty"`

	// === 分配 pin（读写）：编译器写回、前端持久化并原样回传 ===
	// 这六个字段把本链路分配到的资源（监听端口、transit IP 对、IPv6 link-local 对）
	// 绑定到具体的 edge 上，复用 overlay_ip / compiled_port 已有的「写回 + localStorage」
	// 往返路径（无新增传输）。下次编译时编译器优先沿用已存在的 pin，而非按数组位置重算，
	// 从而让 superset 拓扑对每条既有 edge 重现逐字节相同的分配值（增量扩展不打扰未变动的节点）。
	//
	// pin 按所属 edge 的 FromNodeID/ToNodeID 定向：from_* 是本 edge from 侧的值，
	// to_* 是 to 侧的值；同一对节点的反向 edge 携带的是镜像后的同一对取值。
	// 每个资源要么成对完整 pin、要么完全不 pin；单端 pin（partial）由验证器拒绝。
	// 详见 docs/spec/compiler/allocation-stability.md 与 docs/spec/data-model/edge.md（Allocation pins）。

	// from / to 侧接口的监听端口 pin（client 角色不使用 per-peer 端口，故不 pin）。
	PinnedFromPort int `json:"pinned_from_port,omitempty"`
	PinnedToPort   int `json:"pinned_to_port,omitempty"`

	// from / to 侧的 transit IP pin（取自所属域的 transit_cidr 地址池）。
	PinnedFromTransitIP string `json:"pinned_from_transit_ip,omitempty"`
	PinnedToTransitIP   string `json:"pinned_to_transit_ip,omitempty"`

	// from / to 侧的 IPv6 link-local pin（取自 fe80::/10）。
	PinnedFromLinkLocal string `json:"pinned_from_link_local,omitempty"`
	PinnedToLinkLocal   string `json:"pinned_to_link_local,omitempty"`
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
