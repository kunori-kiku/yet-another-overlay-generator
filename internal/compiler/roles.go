package compiler

import (
	"github.com/kunorikiku/yet-another-overlay-generator/internal/model"
)

// RoleSemantics captures the behavioral semantics a node's role implies: IP forwarding, inbound
// acceptance, whether Babel runs, the Babel announce policy, and the AllowedIPs mode.
type RoleSemantics struct {
	// EnableForwarding enables IP forwarding on the node.
	EnableForwarding bool

	// AcceptAllInbound accepts inbound connections from any peer.
	AcceptAllInbound bool

	// RunBabel reports whether the node runs Babel.
	RunBabel bool

	// BabelAnnounce is the Babel announce policy for the node.
	BabelAnnounce BabelAnnouncePolicy

	// AllowedIPsMode selects how AllowedIPs are derived for peers of this node.
	AllowedIPsMode string // "point-to-point" | "relay-all" | "gateway"
}

// BabelAnnouncePolicy describes which prefixes a node announces over Babel.
type BabelAnnouncePolicy struct {
	// AnnounceSelf announces the node's own /32 overlay address.
	AnnounceSelf bool

	// AnnounceDomainCIDR announces the node's domain CIDR.
	AnnounceDomainCIDR bool

	// AnnounceExtraPrefixes announces the node's configured extra prefixes.
	AnnounceExtraPrefixes bool

	// AnnounceDefault announces the default route 0.0.0.0/0.
	AnnounceDefault bool
}

// DeriveRoleSemantics returns the RoleSemantics for the given node based on its role.
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

	case "client":
		return RoleSemantics{
			EnableForwarding: false,
			AcceptAllInbound: false,
			RunBabel:         false,
			BabelAnnounce:    BabelAnnouncePolicy{},
			AllowedIPsMode:   "client",
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

// InferCapabilitiesFromRole derives a node's capabilities from its role, starting from the node's
// existing capabilities and overlaying the role-implied defaults.
func InferCapabilitiesFromRole(node *model.Node) model.NodeCapabilities {
	caps := node.Capabilities

	// A configured public endpoint implies public reachability even when the operator did not
	// also tick has_public_ip (C3 / D49). Normalize HasPublicIP UP only — an explicitly-set true
	// is never stripped — and use hasPub (not the raw node flag) in the accept-inbound branches
	// below so endpoint-bearing nodes correctly accept inbound. This single normalization also
	// feeds the reverse-Endpoint fallback in derivePeers, which gates on
	// fromNode.Capabilities.HasPublicIP.
	hasPub := node.Capabilities.HasPublicIP || len(node.PublicEndpoints) > 0
	caps.HasPublicIP = hasPub

	switch node.Role {
	case "router":
		caps.CanForward = true
		// A router accepts inbound connections when it is publicly reachable, consistent with
		// DeriveRoleSemantics's AcceptAllInbound (D49). Preserve an already explicitly-set true.
		caps.CanAcceptInbound = caps.CanAcceptInbound || hasPub
	case "relay":
		caps.CanForward = true
		caps.CanRelay = true
		caps.CanAcceptInbound = true
	case "gateway":
		caps.CanForward = true
		// A gateway likewise accepts inbound connections when it is publicly reachable (D49),
		// consistent with DeriveRoleSemantics.
		caps.CanAcceptInbound = caps.CanAcceptInbound || hasPub
	case "peer":
		// peer: no capability overrides; keep the node's existing capabilities.
	case "client":
		caps.CanForward = false
		caps.CanRelay = false
		caps.CanAcceptInbound = false
	}

	return caps
}

// DeriveAllowedIPsForPeer derives the WireGuard AllowedIPs entries for a peer pointing at
// remoteNode, based on remoteNode's role semantics and domain.
func DeriveAllowedIPsForPeer(remoteNode *model.Node, domain *model.Domain) []string {
	semantics := DeriveRoleSemantics(remoteNode)
	ips := []string{}

	switch semantics.AllowedIPsMode {
	case "relay-all":
		// Relay: AllowedIPs cover the domain CIDR plus extra prefixes.
		if domain != nil && domain.CIDR != "" {
			ips = append(ips, domain.CIDR)
		}
		// Extra prefixes.
		ips = append(ips, remoteNode.ExtraPrefixes...)
		if len(ips) == 0 && remoteNode.OverlayIP != "" {
			ips = append(ips, remoteNode.OverlayIP+"/32")
		}

	case "gateway":
		// Gateway: domain CIDR + extra prefixes + the default route.
		if domain != nil && domain.CIDR != "" {
			ips = append(ips, domain.CIDR)
		}
		ips = append(ips, remoteNode.ExtraPrefixes...)
		// Default route.
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
