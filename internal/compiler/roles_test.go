package compiler

import (
	"testing"

	"github.com/kunorikiku/yet-another-overlay-generator/internal/model"
)

func TestDeriveRoleSemantics_Peer(t *testing.T) {
	node := &model.Node{ID: "n1", Name: "peer1", Role: "peer"}
	sem := DeriveRoleSemantics(node)

	if sem.EnableForwarding {
		t.Errorf("peer 不应启用转发")
	}
	if sem.AcceptAllInbound {
		t.Errorf("peer 不应接受所有入站")
	}
	if !sem.RunBabel {
		t.Errorf("peer 应运行 Babel")
	}
	if !sem.BabelAnnounce.AnnounceSelf {
		t.Errorf("peer 应通告自身 /32")
	}
	if sem.BabelAnnounce.AnnounceDomainCIDR {
		t.Errorf("peer 不应通告 Domain CIDR")
	}
	if sem.AllowedIPsMode != "point-to-point" {
		t.Errorf("peer AllowedIPs 模式应为 point-to-point, 得到 %s", sem.AllowedIPsMode)
	}
}

func TestDeriveRoleSemantics_Router(t *testing.T) {
	node := &model.Node{ID: "n1", Name: "router1", Role: "router"}
	sem := DeriveRoleSemantics(node)

	if !sem.EnableForwarding {
		t.Errorf("router 应启用转发")
	}
	if !sem.BabelAnnounce.AnnounceSelf {
		t.Errorf("router 应通告自身")
	}
	if !sem.BabelAnnounce.AnnounceDomainCIDR {
		t.Errorf("router 应通告 Domain CIDR")
	}
	if sem.AllowedIPsMode != "point-to-point" {
		t.Errorf("router AllowedIPs 模式应为 point-to-point, 得到 %s", sem.AllowedIPsMode)
	}
}

func TestDeriveRoleSemantics_Relay(t *testing.T) {
	node := &model.Node{ID: "n1", Name: "relay1", Role: "relay"}
	sem := DeriveRoleSemantics(node)

	if !sem.EnableForwarding {
		t.Errorf("relay 应启用转发")
	}
	if !sem.AcceptAllInbound {
		t.Errorf("relay 应接受所有入站")
	}
	if sem.AllowedIPsMode != "relay-all" {
		t.Errorf("relay AllowedIPs 模式应为 relay-all, 得到 %s", sem.AllowedIPsMode)
	}
	if !sem.BabelAnnounce.AnnounceDomainCIDR {
		t.Errorf("relay 应通告 Domain CIDR")
	}
}

func TestDeriveRoleSemantics_Gateway(t *testing.T) {
	node := &model.Node{ID: "n1", Name: "gw1", Role: "gateway", ExtraPrefixes: []string{"192.168.0.0/24"}}
	sem := DeriveRoleSemantics(node)

	if !sem.EnableForwarding {
		t.Errorf("gateway 应启用转发")
	}
	if !sem.BabelAnnounce.AnnounceDefault {
		t.Errorf("gateway 应通告默认路由")
	}
	if !sem.BabelAnnounce.AnnounceExtraPrefixes {
		t.Errorf("gateway 应通告额外前缀")
	}
	if sem.AllowedIPsMode != "gateway" {
		t.Errorf("gateway AllowedIPs 模式应为 gateway, 得到 %s", sem.AllowedIPsMode)
	}
}

func TestInferCapabilitiesFromRole_Router(t *testing.T) {
	node := &model.Node{ID: "n1", Role: "router", Capabilities: model.NodeCapabilities{}}
	caps := InferCapabilitiesFromRole(node)

	if !caps.CanForward {
		t.Errorf("router 角色推导后应 CanForward=true")
	}
}

func TestInferCapabilitiesFromRole_Relay(t *testing.T) {
	node := &model.Node{ID: "n1", Role: "relay", Capabilities: model.NodeCapabilities{}}
	caps := InferCapabilitiesFromRole(node)

	if !caps.CanForward {
		t.Errorf("relay 角色推导后应 CanForward=true")
	}
	if !caps.CanRelay {
		t.Errorf("relay 角色推导后应 CanRelay=true")
	}
	if !caps.CanAcceptInbound {
		t.Errorf("relay 角色推导后应 CanAcceptInbound=true")
	}
}

func TestInferCapabilitiesFromRole_Peer(t *testing.T) {
	// peer 不覆盖用户设置
	node := &model.Node{ID: "n1", Role: "peer", Capabilities: model.NodeCapabilities{
		CanForward: false,
	}}
	caps := InferCapabilitiesFromRole(node)

	if caps.CanForward {
		t.Errorf("peer 角色推导不应覆盖 CanForward")
	}
}

func TestDeriveAllowedIPsForPeer_PointToPoint(t *testing.T) {
	node := &model.Node{ID: "n1", Role: "peer", OverlayIP: "10.10.0.1"}
	domain := &model.Domain{ID: "d1", CIDR: "10.10.0.0/24"}

	ips := DeriveAllowedIPsForPeer(node, domain)
	if len(ips) != 1 || ips[0] != "10.10.0.1/32" {
		t.Errorf("peer AllowedIPs 期望 [10.10.0.1/32], 得到 %v", ips)
	}
}

func TestDeriveAllowedIPsForPeer_Relay(t *testing.T) {
	node := &model.Node{ID: "n1", Role: "relay", OverlayIP: "10.10.0.1"}
	domain := &model.Domain{ID: "d1", CIDR: "10.10.0.0/24"}

	ips := DeriveAllowedIPsForPeer(node, domain)
	if len(ips) != 1 || ips[0] != "10.10.0.0/24" {
		t.Errorf("relay AllowedIPs 期望包含 Domain CIDR, 得到 %v", ips)
	}
}

func TestDeriveAllowedIPsForPeer_Gateway(t *testing.T) {
	node := &model.Node{
		ID: "n1", Role: "gateway", OverlayIP: "10.10.0.1",
		ExtraPrefixes: []string{"192.168.0.0/24"},
	}
	domain := &model.Domain{ID: "d1", CIDR: "10.10.0.0/24"}

	ips := DeriveAllowedIPsForPeer(node, domain)

	// 应包含 Domain CIDR + 额外前缀 + 默认路由
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
		t.Errorf("gateway AllowedIPs 应包含 0.0.0.0/0, 得到 %v", ips)
	}
	if !hasExtra {
		t.Errorf("gateway AllowedIPs 应包含额外前缀, 得到 %v", ips)
	}
	if !hasDomain {
		t.Errorf("gateway AllowedIPs 应包含 Domain CIDR, 得到 %v", ips)
	}
}
