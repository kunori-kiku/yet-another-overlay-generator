package model

// Topology ，
type Topology struct {
	// 
	Project Project `json:"project"`

	// 
	Domains []Domain `json:"domains"`

	// 
	Nodes []Node `json:"nodes"`

	// 
	Edges []Edge `json:"edges"`

	// （）
	RoutePolicies []RoutePolicy `json:"route_policies,omitempty"`
}

// Project 
type Project struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Version     string `json:"version,omitempty"`
}

// Domain ， overlay 
type Domain struct {
	ID             string   `json:"id"`
	Name           string   `json:"name"`
	CIDR           string   `json:"cidr"`
	Description    string   `json:"description,omitempty"`
	AllocationMode string   `json:"allocation_mode"` // "auto" | "manual"
	RoutingMode    string   `json:"routing_mode"`    // "static" | "babel" | "none"
	ReservedRanges []string `json:"reserved_ranges,omitempty"`
}

// Node ，
type Node struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Hostname string `json:"hostname,omitempty"`
	Platform string `json:"platform,omitempty"` // "debian" | "ubuntu"

	// ：peer, router, relay, gateway
	Role string `json:"role"`

	//  Domain ID（： Domain）
	DomainID string `json:"domain_id"`

	// Overlay IP，
	OverlayIP string `json:"overlay_ip,omitempty"`

	// WireGuard 
	ListenPort int `json:"listen_port,omitempty"`

	// WireGuard 接口 MTU（0 = 使用系统默认值，通常 1420）
	MTU int `json:"mtu,omitempty"`

	// 
	Capabilities NodeCapabilities `json:"capabilities"`

	// ： WireGuard 
	FixedPrivateKey bool `json:"fixed_private_key,omitempty"`

	// WireGuard （ fixed_private_key=true ）
	WireGuardPrivateKey string `json:"wireguard_private_key,omitempty"`
	WireGuardPublicKey  string `json:"wireguard_public_key,omitempty"`

	// （gateway ：）
	ExtraPrefixes []string `json:"extra_prefixes,omitempty"`
}

// NodeCapabilities 
type NodeCapabilities struct {
	CanAcceptInbound bool `json:"can_accept_inbound"`
	CanForward       bool `json:"can_forward"`
	CanRelay         bool `json:"can_relay"`
	HasPublicIP      bool `json:"has_public_ip"`
}

// Edge ，
//  "from  to "
type Edge struct {
	ID         string `json:"id"`
	FromNodeID string `json:"from_node_id"`
	ToNodeID   string `json:"to_node_id"`

	// ：direct, public-endpoint, relay-path, candidate
	Type string `json:"type"`

	//  endpoint
	EndpointHost string `json:"endpoint_host,omitempty"`
	EndpointPort int    `json:"endpoint_port,omitempty"`

	// 
	Priority int `json:"priority,omitempty"`
	Weight   int `json:"weight,omitempty"`

	// ：udp, tcp
	Transport string `json:"transport,omitempty"`

	// 
	IsEnabled bool `json:"is_enabled"`

	// 
	Notes string `json:"notes,omitempty"`
}

// RoutePolicy （Phase 2 ）
type RoutePolicy struct {
	ID              string `json:"id"`
	DomainID        string `json:"domain_id"`
	DestinationCIDR string `json:"destination_cidr"`
	NextHopNodeID   string `json:"next_hop_node_id,omitempty"`
	Metric          int    `json:"metric,omitempty"`
	Notes           string `json:"notes,omitempty"`

	// Phase 2 
	SourceSelector string `json:"source_selector,omitempty"`  //  ID 
	Action         string `json:"action,omitempty"`           // "allow" | "deny" | "metric-override"
	ApplyToNodeID  string `json:"apply_to_node_id,omitempty"` // （=）
}
