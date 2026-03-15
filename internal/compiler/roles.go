package compiler

import (
	"github.com/kunorikiku/yet-another-overlay-generator/internal/model"
)

// RoleSemantics 角色语义推导结果
type RoleSemantics struct {
	// 是否应启用 IP 转发
	EnableForwarding bool

	// 是否接受所有入站连接
	AcceptAllInbound bool

	// 是否运行 Babel
	RunBabel bool

	// Babel 通告策略
	BabelAnnounce BabelAnnouncePolicy

	// AllowedIPs 策略
	AllowedIPsMode string // "point-to-point" | "relay-all" | "gateway"
}

// BabelAnnouncePolicy Babel 通告策略
type BabelAnnouncePolicy struct {
	// 通告自身 /32
	AnnounceSelf bool

	// 通告所属 Domain CIDR
	AnnounceDomainCIDR bool

	// 通告额外前缀
	AnnounceExtraPrefixes bool

	// 通告默认路由 0.0.0.0/0
	AnnounceDefault bool
}

// DeriveRoleSemantics 根据节点角色推导语义
func DeriveRoleSemantics(node *model.Node) RoleSemantics {
	switch node.Role {
	case "router":
		return RoleSemantics{
			EnableForwarding: true,
			AcceptAllInbound: node.Capabilities.HasPublicIP,
			RunBabel:         true,
			BabelAnnounce: BabelAnnouncePolicy{
				AnnounceSelf:          true,
				AnnounceDomainCIDR:    true,
				AnnounceExtraPrefixes: len(node.ExtraPrefixes) > 0,
			},
			AllowedIPsMode: "point-to-point",
		}

	case "relay":
		return RoleSemantics{
			EnableForwarding: true,
			AcceptAllInbound: true,
			RunBabel:         true,
			BabelAnnounce: BabelAnnouncePolicy{
				AnnounceSelf:          true,
				AnnounceDomainCIDR:    true,
				AnnounceExtraPrefixes: len(node.ExtraPrefixes) > 0,
			},
			AllowedIPsMode: "relay-all",
		}

	case "gateway":
		return RoleSemantics{
			EnableForwarding: true,
			AcceptAllInbound: node.Capabilities.HasPublicIP,
			RunBabel:         true,
			BabelAnnounce: BabelAnnouncePolicy{
				AnnounceSelf:          true,
				AnnounceDomainCIDR:    true,
				AnnounceExtraPrefixes: true,
				AnnounceDefault:       true,
			},
			AllowedIPsMode: "gateway",
		}

	default: // "peer"
		return RoleSemantics{
			EnableForwarding: false,
			AcceptAllInbound: false,
			RunBabel:         true,
			BabelAnnounce: BabelAnnouncePolicy{
				AnnounceSelf: true,
			},
			AllowedIPsMode: "point-to-point",
		}
	}
}

// InferCapabilitiesFromRole 根据角色推导/覆盖节点能力
// 返回更新后的 capabilities（不修改原始节点）
func InferCapabilitiesFromRole(node *model.Node) model.NodeCapabilities {
	caps := node.Capabilities

	switch node.Role {
	case "router":
		caps.CanForward = true
	case "relay":
		caps.CanForward = true
		caps.CanRelay = true
		caps.CanAcceptInbound = true
	case "gateway":
		caps.CanForward = true
	case "peer":
		// peer 不覆盖，保持用户设置
	}

	return caps
}

// DeriveAllowedIPsForPeer 根据对端角色推导 AllowedIPs
func DeriveAllowedIPsForPeer(remoteNode *model.Node, domain *model.Domain) []string {
	semantics := DeriveRoleSemantics(remoteNode)
	ips := []string{}

	switch semantics.AllowedIPsMode {
	case "relay-all":
		// Relay 需要放宽 AllowedIPs 以转发所有流量
		if domain != nil && domain.CIDR != "" {
			ips = append(ips, domain.CIDR)
		}
		// 加上额外前缀
		ips = append(ips, remoteNode.ExtraPrefixes...)
		if len(ips) == 0 && remoteNode.OverlayIP != "" {
			ips = append(ips, remoteNode.OverlayIP+"/32")
		}

	case "gateway":
		// Gateway 放宽到 Domain CIDR + 额外前缀 + 可能的默认路由
		if domain != nil && domain.CIDR != "" {
			ips = append(ips, domain.CIDR)
		}
		ips = append(ips, remoteNode.ExtraPrefixes...)
		// 如果有默认路由意图
		if semantics.BabelAnnounce.AnnounceDefault {
			ips = append(ips, "0.0.0.0/0")
		}

	default: // "point-to-point"
		if remoteNode.OverlayIP != "" {
			ips = append(ips, remoteNode.OverlayIP+"/32")
		}
	}

	return ips
}
