package compiler

import (
	"testing"

	"github.com/kunorikiku/yet-another-overlay-generator/internal/model"
)

func TestDeriveRoleSemantics_Peer(t *testing.T) {
	node := &model.Node{ID: "n1", Name: "peer1", Role: "peer"}
	sem := DeriveRoleSemantics(node)

	if sem.EnableForwarding {
		t.Errorf("peer ")
	}
	if sem.AcceptAllInbound {
		t.Errorf("peer ")
	}
	if !sem.RunBabel {
		t.Errorf("peer  Babel")
	}
	if !sem.BabelAnnounce.AnnounceSelf {
		t.Errorf("peer  /32")
	}
	if sem.BabelAnnounce.AnnounceDomainCIDR {
		t.Errorf("peer  Domain CIDR")
	}
	if sem.AllowedIPsMode != "point-to-point" {
		t.Errorf("peer AllowedIPs  point-to-point,  %s", sem.AllowedIPsMode)
	}
}

func TestDeriveRoleSemantics_Router(t *testing.T) {
	node := &model.Node{ID: "n1", Name: "router1", Role: "router"}
	sem := DeriveRoleSemantics(node)

	if !sem.EnableForwarding {
		t.Errorf("router ")
	}
	if !sem.BabelAnnounce.AnnounceSelf {
		t.Errorf("router ")
	}
	if !sem.BabelAnnounce.AnnounceDomainCIDR {
		t.Errorf("router  Domain CIDR")
	}
	if sem.AllowedIPsMode != "point-to-point" {
		t.Errorf("router AllowedIPs  point-to-point,  %s", sem.AllowedIPsMode)
	}
}

func TestDeriveRoleSemantics_Relay(t *testing.T) {
	node := &model.Node{ID: "n1", Name: "relay1", Role: "relay"}
	sem := DeriveRoleSemantics(node)

	if !sem.EnableForwarding {
		t.Errorf("relay ")
	}
	if !sem.AcceptAllInbound {
		t.Errorf("relay ")
	}
	if sem.AllowedIPsMode != "relay-all" {
		t.Errorf("relay AllowedIPs  relay-all,  %s", sem.AllowedIPsMode)
	}
	if !sem.BabelAnnounce.AnnounceDomainCIDR {
		t.Errorf("relay  Domain CIDR")
	}
}

func TestDeriveRoleSemantics_Gateway(t *testing.T) {
	node := &model.Node{ID: "n1", Name: "gw1", Role: "gateway", ExtraPrefixes: []string{"192.168.0.0/24"}}
	sem := DeriveRoleSemantics(node)

	if !sem.EnableForwarding {
		t.Errorf("gateway ")
	}
	if !sem.BabelAnnounce.AnnounceDefault {
		t.Errorf("gateway ")
	}
	if !sem.BabelAnnounce.AnnounceExtraPrefixes {
		t.Errorf("gateway ")
	}
	if sem.AllowedIPsMode != "gateway" {
		t.Errorf("gateway AllowedIPs  gateway,  %s", sem.AllowedIPsMode)
	}
}

func TestInferCapabilitiesFromRole_Router(t *testing.T) {
	node := &model.Node{ID: "n1", Role: "router", Capabilities: model.NodeCapabilities{}}
	caps := InferCapabilitiesFromRole(node)

	if !caps.CanForward {
		t.Errorf("router  CanForward=true")
	}
}

func TestInferCapabilitiesFromRole_Relay(t *testing.T) {
	node := &model.Node{ID: "n1", Role: "relay", Capabilities: model.NodeCapabilities{}}
	caps := InferCapabilitiesFromRole(node)

	if !caps.CanForward {
		t.Errorf("relay  CanForward=true")
	}
	if !caps.CanRelay {
		t.Errorf("relay  CanRelay=true")
	}
	if !caps.CanAcceptInbound {
		t.Errorf("relay  CanAcceptInbound=true")
	}
}

// TestInferCapabilitiesFromRole_EndpointImpliesPublicIP pins the C3 derive-up cascade: a
// router/gateway node that carries a configured public endpoint but has has_public_ip=false
// must have HasPublicIP normalized UP to true, which in turn flips CanAcceptInbound to true
// (D49: a node with a real public endpoint SHOULD accept inbound). The pre-C3 code read
// node.Capabilities.HasPublicIP directly (false here), so CanAcceptInbound stayed false.
func TestInferCapabilitiesFromRole_EndpointImpliesPublicIP(t *testing.T) {
	for _, role := range []string{"router", "gateway"} {
		t.Run(role, func(t *testing.T) {
			node := &model.Node{
				ID:   "n1",
				Role: role,
				Capabilities: model.NodeCapabilities{
					HasPublicIP:      false,
					CanAcceptInbound: false,
				},
				PublicEndpoints: []model.PublicEndpoint{
					{ID: "ep1", Host: "node.example", Port: 51820},
				},
			}
			caps := InferCapabilitiesFromRole(node)

			if !caps.HasPublicIP {
				t.Errorf("%s with a public endpoint: HasPublicIP should normalize UP to true, got false", role)
			}
			if !caps.CanAcceptInbound {
				t.Errorf("%s with a public endpoint: CanAcceptInbound should flip to true (D49), got false", role)
			}
		})
	}
}

// TestInferCapabilitiesFromRole_NoEndpointNoPublicIP is the negative control for C3: with
// neither has_public_ip nor any public endpoint, a router/gateway must NOT gain HasPublicIP
// or CanAcceptInbound — the derive-up cascade only fires when there is real public reach.
func TestInferCapabilitiesFromRole_NoEndpointNoPublicIP(t *testing.T) {
	for _, role := range []string{"router", "gateway"} {
		t.Run(role, func(t *testing.T) {
			node := &model.Node{ID: "n1", Role: role, Capabilities: model.NodeCapabilities{}}
			caps := InferCapabilitiesFromRole(node)

			if caps.HasPublicIP {
				t.Errorf("%s with no endpoint: HasPublicIP should stay false, got true", role)
			}
			if caps.CanAcceptInbound {
				t.Errorf("%s with no endpoint: CanAcceptInbound should stay false, got true", role)
			}
		})
	}
}

// TestInferCapabilitiesFromRole_ExplicitPublicIPPreserved guards the "normalize UP only"
// invariant: an explicitly-set HasPublicIP=true is never stripped, even with no endpoint.
func TestInferCapabilitiesFromRole_ExplicitPublicIPPreserved(t *testing.T) {
	for _, role := range []string{"router", "gateway"} {
		t.Run(role, func(t *testing.T) {
			node := &model.Node{ID: "n1", Role: role, Capabilities: model.NodeCapabilities{
				HasPublicIP: true,
			}}
			caps := InferCapabilitiesFromRole(node)

			if !caps.HasPublicIP {
				t.Errorf("%s: an explicit HasPublicIP=true must never be stripped", role)
			}
			if !caps.CanAcceptInbound {
				t.Errorf("%s: explicit HasPublicIP=true should yield CanAcceptInbound=true", role)
			}
		})
	}
}

func TestInferCapabilitiesFromRole_Peer(t *testing.T) {
	// peer
	node := &model.Node{ID: "n1", Role: "peer", Capabilities: model.NodeCapabilities{
		CanForward: false,
	}}
	caps := InferCapabilitiesFromRole(node)

	if caps.CanForward {
		t.Errorf("peer  CanForward")
	}
}

func TestDeriveAllowedIPsForPeer_PointToPoint(t *testing.T) {
	node := &model.Node{ID: "n1", Role: "peer", OverlayIP: "10.10.0.1"}
	domain := &model.Domain{ID: "d1", CIDR: "10.10.0.0/24"}

	ips := DeriveAllowedIPsForPeer(node, domain)
	if len(ips) != 1 || ips[0] != "10.10.0.1/32" {
		t.Errorf("peer AllowedIPs  [10.10.0.1/32],  %v", ips)
	}
}

func TestDeriveAllowedIPsForPeer_Relay(t *testing.T) {
	node := &model.Node{ID: "n1", Role: "relay", OverlayIP: "10.10.0.1"}
	domain := &model.Domain{ID: "d1", CIDR: "10.10.0.0/24"}

	ips := DeriveAllowedIPsForPeer(node, domain)
	if len(ips) != 1 || ips[0] != "10.10.0.0/24" {
		t.Errorf("relay AllowedIPs  Domain CIDR,  %v", ips)
	}
}

func TestDeriveAllowedIPsForPeer_Gateway(t *testing.T) {
	node := &model.Node{
		ID: "n1", Role: "gateway", OverlayIP: "10.10.0.1",
		ExtraPrefixes: []string{"192.168.0.0/24"},
	}
	domain := &model.Domain{ID: "d1", CIDR: "10.10.0.0/24"}

	ips := DeriveAllowedIPsForPeer(node, domain)

	//  Domain CIDR +  +
	hasDefault := false
	hasExtra := false
	hasDomain := false
	for _, ip := range ips {
		if ip == "0.0.0.0/0" {
			hasDefault = true
		}
		if ip == "192.168.0.0/24" {
			hasExtra = true
		}
		if ip == "10.10.0.0/24" {
			hasDomain = true
		}
	}

	if !hasDefault {
		t.Errorf("gateway AllowedIPs  0.0.0.0/0,  %v", ips)
	}
	if !hasExtra {
		t.Errorf("gateway AllowedIPs ,  %v", ips)
	}
	if !hasDomain {
		t.Errorf("gateway AllowedIPs  Domain CIDR,  %v", ips)
	}
}
