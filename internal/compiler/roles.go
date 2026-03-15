package compiler

import (
	"github.com/kunorikiku/yet-another-overlay-generator/internal/model"
)

// RoleSemantics 
type RoleSemantics struct {
	//  IP 
	EnableForwarding bool

	// 
	AcceptAllInbound bool

	//  Babel
	RunBabel bool

	// Babel 
	BabelAnnounce BabelAnnouncePolicy

	// AllowedIPs 
	AllowedIPsMode string // "point-to-point" | "relay-all" | "gateway"
}

// BabelAnnouncePolicy Babel 
type BabelAnnouncePolicy struct {
	//  /32
	AnnounceSelf bool

	//  Domain CIDR
	AnnounceDomainCIDR bool

	// 
	AnnounceExtraPrefixes bool

	//  0.0.0.0/0
	AnnounceDefault bool
}

// DeriveRoleSemantics 
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

// InferCapabilitiesFromRole /
//  capabilities（）
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
		// peer ，
	}

	return caps
}

// DeriveAllowedIPsForPeer  AllowedIPs
func DeriveAllowedIPsForPeer(remoteNode *model.Node, domain *model.Domain) []string {
	semantics := DeriveRoleSemantics(remoteNode)
	ips := []string{}

	switch semantics.AllowedIPsMode {
	case "relay-all":
		// Relay  AllowedIPs 
		if domain != nil && domain.CIDR != "" {
			ips = append(ips, domain.CIDR)
		}
		// 
		ips = append(ips, remoteNode.ExtraPrefixes...)
		if len(ips) == 0 && remoteNode.OverlayIP != "" {
			ips = append(ips, remoteNode.OverlayIP+"/32")
		}

	case "gateway":
		// Gateway  Domain CIDR +  + 
		if domain != nil && domain.CIDR != "" {
			ips = append(ips, domain.CIDR)
		}
		ips = append(ips, remoteNode.ExtraPrefixes...)
		// 
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
