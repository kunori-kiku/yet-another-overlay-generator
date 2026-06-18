package renderer

import (
	"strings"
	"testing"

	"github.com/kunorikiku/yet-another-overlay-generator/internal/compiler"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/model"
)

// This file is the "golden gate" for the Babel rendering partition of Plan 6
// (routing-mode + Babel correctness).
//
// Protected invariant (outline principle): the self-/32 announcement path
// (the overlay IP on dummy0, together with `redistribute local`) is currently
// the only announcement mechanism that actually takes effect on the deployed
// cluster, and it must stay byte-identical across this change. Below,
// TestBabelAnnounce_GoldenSelf32_ByteIdentical pins the self-/32 lines using
// literals derived character-by-character from the template; no change may
// alter these lines.

// representativeBabelTopology constructs a representative 4-node topology that
// covers every announcement category this plan cares about:
//   - peer    : self-/32 only
//   - router  : self-/32 + domain CIDR aggregate + extra_prefixes
//   - relay   : self-/32 + domain CIDR aggregate
//   - gateway : self-/32 + domain CIDR aggregate + extra_prefixes + default route 0.0.0.0/0
//
// It returns the node for each role plus a domain ready to feed directly into RenderBabelConfig.
func representativeBabelTopology() (peerNode, routerNode, relayNode, gatewayNode *model.Node, domain *model.Domain) {
	domain = &model.Domain{
		ID:          "domain-1",
		Name:        "test",
		CIDR:        "10.11.0.0/24",
		RoutingMode: "babel",
	}

	peerNode = &model.Node{
		ID:        "node-peer",
		Name:      "peernode",
		Role:      "peer",
		DomainID:  "domain-1",
		OverlayIP: "10.11.0.2",
	}

	routerNode = &model.Node{
		ID:        "node-router",
		Name:      "routernode",
		Role:      "router",
		DomainID:  "domain-1",
		OverlayIP: "10.11.0.1",
		Capabilities: model.NodeCapabilities{
			CanForward: true,
		},
		ExtraPrefixes: []string{"192.168.50.0/24"},
	}

	relayNode = &model.Node{
		ID:        "node-relay",
		Name:      "relaynode",
		Role:      "relay",
		DomainID:  "domain-1",
		OverlayIP: "10.11.0.3",
		Capabilities: model.NodeCapabilities{
			CanForward:       true,
			CanRelay:         true,
			CanAcceptInbound: true,
		},
	}

	gatewayNode = &model.Node{
		ID:        "node-gateway",
		Name:      "gatewaynode",
		Role:      "gateway",
		DomainID:  "domain-1",
		OverlayIP: "10.11.0.4",
		Capabilities: model.NodeCapabilities{
			CanForward:  true,
			HasPublicIP: true,
		},
		ExtraPrefixes: []string{"172.16.0.0/16"},
	}

	return peerNode, routerNode, relayNode, gatewayNode, domain
}

// containsLine reports whether config contains a line that is byte-identical to want.
func containsLine(config, want string) bool {
	for _, line := range strings.Split(config, "\n") {
		if line == want {
			return true
		}
	}
	return false
}

// TestBabelAnnounce_GoldenSelf32_ByteIdentical is the golden gate for the protected path.
//
// The expected lines below are literals derived character-by-character from
// babelConfigTemplate: when RedistributePrefixes (self-/32 goes through
// `redistribute local`) renders the overlay IP, the exact string the template
// produces is `redistribute local ip <OverlayIP>/32 allow`. After any change
// these lines must still exist and be byte-identical, otherwise the self-/32
// deployment path has regressed.
func TestBabelAnnounce_GoldenSelf32_ByteIdentical(t *testing.T) {
	peerNode, routerNode, relayNode, gatewayNode, domain := representativeBabelTopology()

	cases := []struct {
		name     string
		node     *model.Node
		peers    []compiler.PeerInfo
		wantSelf string
	}{
		{
			name:     "peer",
			node:     peerNode,
			peers:    []compiler.PeerInfo{{NodeID: "node-router", NodeName: "routernode", InterfaceName: "wg-routernode"}},
			wantSelf: "redistribute local ip 10.11.0.2/32 allow",
		},
		{
			name:     "router",
			node:     routerNode,
			peers:    []compiler.PeerInfo{{NodeID: "node-peer", NodeName: "peernode", InterfaceName: "wg-peernode"}},
			wantSelf: "redistribute local ip 10.11.0.1/32 allow",
		},
		{
			name:     "relay",
			node:     relayNode,
			peers:    []compiler.PeerInfo{{NodeID: "node-router", NodeName: "routernode", InterfaceName: "wg-routernode"}},
			wantSelf: "redistribute local ip 10.11.0.3/32 allow",
		},
		{
			name:     "gateway",
			node:     gatewayNode,
			peers:    []compiler.PeerInfo{{NodeID: "node-router", NodeName: "routernode", InterfaceName: "wg-routernode"}},
			wantSelf: "redistribute local ip 10.11.0.4/32 allow",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			config, err := RenderBabelConfig(tc.node, tc.peers, domain)
			if err != nil {
				t.Fatalf("failed to render Babel config: %v", err)
			}
			if !containsLine(config, tc.wantSelf) {
				t.Errorf("self-/32 announcement line regressed.\nexpected byte-identical line: %q\nactual config:\n%s", tc.wantSelf, config)
			}
		})
	}
}

// TestBabelAnnounce_GatewayDefaultRoute_NonLocal verifies D40: the gateway
// default route must be rendered in non-local form (`redistribute ip 0.0.0.0/0 allow`)
// so babeld matches the real kernel default route present on the node. It must
// never again appear in `redistribute local` form.
func TestBabelAnnounce_GatewayDefaultRoute_NonLocal(t *testing.T) {
	_, _, _, gatewayNode, domain := representativeBabelTopology()

	peers := []compiler.PeerInfo{{NodeID: "node-router", NodeName: "routernode", InterfaceName: "wg-routernode"}}
	config, err := RenderBabelConfig(gatewayNode, peers, domain)
	if err != nil {
		t.Fatalf("render failed: %v", err)
	}

	want := "redistribute ip 0.0.0.0/0 allow"
	if !containsLine(config, want) {
		t.Errorf("gateway default route should be rendered in non-local form.\nexpected byte-identical line: %q\nactual config:\n%s", want, config)
	}

	// The default route must no longer be rendered in local form (it would match no
	// connected route, silently breaking egress).
	if containsLine(config, "redistribute local ip 0.0.0.0/0 allow") {
		t.Errorf("default route should not use `redistribute local` (D40), actual config:\n%s", config)
	}
}

// TestBabelAnnounce_ExtraPrefixes_NonLocal verifies the extra_prefixes part of
// D41: extra_prefixes correspond to real connected kernel routes (the node's
// real LAN segments), so they must be rendered in non-local form,
// `redistribute ip <prefix> allow`.
func TestBabelAnnounce_ExtraPrefixes_NonLocal(t *testing.T) {
	_, routerNode, _, _, domain := representativeBabelTopology()

	peers := []compiler.PeerInfo{{NodeID: "node-peer", NodeName: "peernode", InterfaceName: "wg-peernode"}}
	config, err := RenderBabelConfig(routerNode, peers, domain)
	if err != nil {
		t.Fatalf("render failed: %v", err)
	}

	want := "redistribute ip 192.168.50.0/24 allow"
	if !containsLine(config, want) {
		t.Errorf("extra_prefixes should be rendered in non-local form.\nexpected byte-identical line: %q\nactual config:\n%s", want, config)
	}

	if containsLine(config, "redistribute local ip 192.168.50.0/24 allow") {
		t.Errorf("extra_prefixes should not use `redistribute local` (D41), actual config:\n%s", config)
	}
}

// TestBabelAnnounce_DomainCIDR_DeferredNoOp verifies the plan-6.5 deferral
// decision: the domain CIDR aggregate has no corresponding kernel route on any
// node; this plan does not fix it for now and keeps its current `redistribute local`
// no-op line unchanged (only adding a comment noting the deferral), and must not
// mistakenly change it to non-local form.
func TestBabelAnnounce_DomainCIDR_DeferredNoOp(t *testing.T) {
	_, routerNode, _, _, domain := representativeBabelTopology()

	peers := []compiler.PeerInfo{{NodeID: "node-peer", NodeName: "peernode", InterfaceName: "wg-peernode"}}
	config, err := RenderBabelConfig(routerNode, peers, domain)
	if err != nil {
		t.Fatalf("render failed: %v", err)
	}

	want := "redistribute local ip 10.11.0.0/24 allow"
	if !containsLine(config, want) {
		t.Errorf("domain CIDR aggregate should keep its current `redistribute local` no-op line unchanged (plan-6.5 deferred).\nexpected byte-identical line: %q\nactual config:\n%s", want, config)
	}
}

// TestBabelAnnounce_ClientPeerInterfaceAbsent verifies D73: a tunnel connecting
// to a client (IsClientPeer=true) must never appear in the babeld interface
// declarations -- the client does not run babeld, otherwise the router would
// forever unicast hellos to that tunnel. Client reachability is carried by the
// client-/32 redistribution.
func TestBabelAnnounce_ClientPeerInterfaceAbsent(t *testing.T) {
	_, routerNode, _, _, domain := representativeBabelTopology()

	peers := []compiler.PeerInfo{
		{NodeID: "node-peer", NodeName: "peernode", InterfaceName: "wg-peernode"},
		{
			NodeID:          "node-client",
			NodeName:        "clientnode",
			InterfaceName:   "wg-clientnode",
			IsClientPeer:    true,
			ClientOverlayIP: "10.11.0.9",
		},
	}

	config, err := RenderBabelConfig(routerNode, peers, domain)
	if err != nil {
		t.Fatalf("render failed: %v", err)
	}

	// The ordinary peer interface must be present.
	if !strings.Contains(config, "interface wg-peernode") {
		t.Errorf("ordinary peer interface should be declared, actual config:\n%s", config)
	}

	// The client tunnel interface must never appear in the interface declarations.
	if strings.Contains(config, "interface wg-clientnode") {
		t.Errorf("client tunnel should not be declared as a babel interface (D73), actual config:\n%s", config)
	}

	// Client reachability is still carried by the client-/32 redistribution (client-/32 goes through local).
	if !containsLine(config, "redistribute local ip 10.11.0.9/32 allow") {
		t.Errorf("client reachability should be carried by the client-/32 redistribution, actual config:\n%s", config)
	}
}

// TestBabelAnnounce_LinkCostOverride_Rxcost verifies D63: an edge's LinkCost
// overrides the role-preset default cost and is reflected in the interface's
// rxcost; when unset (0) it falls back to the role preset's DefaultCost.
func TestBabelAnnounce_LinkCostOverride_Rxcost(t *testing.T) {
	_, routerNode, _, _, domain := representativeBabelTopology()

	peers := []compiler.PeerInfo{
		{NodeID: "node-peer", NodeName: "peernode", InterfaceName: "wg-peernode", LinkCost: 250},
	}

	config, err := RenderBabelConfig(routerNode, peers, domain)
	if err != nil {
		t.Fatalf("render failed: %v", err)
	}

	if !strings.Contains(config, "rxcost 250") {
		t.Errorf("LinkCost override should be reflected in rxcost (expected rxcost 250), actual config:\n%s", config)
	}
}

// TestBabelAnnounce_RelayPresetDefaultCost verifies that when an edge does not
// set LinkCost, the relay role falls back to the role-preset DefaultCost (96),
// rendered as rxcost 96.
func TestBabelAnnounce_RelayPresetDefaultCost(t *testing.T) {
	_, _, relayNode, _, domain := representativeBabelTopology()

	peers := []compiler.PeerInfo{
		{NodeID: "node-router", NodeName: "routernode", InterfaceName: "wg-routernode"},
	}

	config, err := RenderBabelConfig(relayNode, peers, domain)
	if err != nil {
		t.Fatalf("render failed: %v", err)
	}

	if !strings.Contains(config, "rxcost 96") {
		t.Errorf("relay should fall back to preset DefaultCost 96 when LinkCost is unset, actual config:\n%s", config)
	}
}

// TestBabelAnnounce_ParallelLinks_TwoStanzasDistinctCost verifies the Babel
// rendering contract for parallel links (docs/spec/artifacts/babel.md): when a
// node has both a primary and a backup link to the same remote at once, two
// interface declarations (interface lines) must be rendered, with distinct
// interface names; the primary (LinkCost 0, and the router preset DefaultCost is
// also 0) omits the rxcost token, while the backup (LinkCost 384) carries
// `rxcost 384`, forming the cost gap needed for failover.
func TestBabelAnnounce_ParallelLinks_TwoStanzasDistinctCost(t *testing.T) {
	_, routerNode, _, _, domain := representativeBabelTopology()

	const (
		primaryIface = "wg-peernode"  // primary-class interface name (== naming.WgInterfaceName(remote))
		backupIface  = "wg-peernod1a" // backup's edge-aware interface name (any distinct shape; only needs to differ from primary)
	)

	peers := []compiler.PeerInfo{
		// Two parallel links to the same remote (node-peer): primary has no cost, backup cost 384.
		{NodeID: "node-peer", NodeName: "peernode", InterfaceName: primaryIface, LinkCost: 0},
		{NodeID: "node-peer", NodeName: "peernode", InterfaceName: backupIface, LinkCost: 384},
	}

	config, err := RenderBabelConfig(routerNode, peers, domain)
	if err != nil {
		t.Fatalf("render failed: %v", err)
	}

	// Both interface declarations must appear, with distinct interface names.
	if !strings.Contains(config, "interface "+primaryIface) {
		t.Errorf("should render primary interface declaration %q, actual config:\n%s", "interface "+primaryIface, config)
	}
	if !strings.Contains(config, "interface "+backupIface) {
		t.Errorf("should render backup interface declaration %q, actual config:\n%s", "interface "+backupIface, config)
	}

	// The backup interface must carry `rxcost 384`.
	if !strings.Contains(config, "rxcost 384") {
		t.Errorf("backup link should render `rxcost 384`, actual config:\n%s", config)
	}

	// The primary interface must be on the "cost 0 omits rxcost" path: its interface line must carry no rxcost token.
	// Locate the primary's interface line line-by-line (router preset DefaultCost is 0, so LinkCost 0 writes no rxcost).
	var primaryLine string
	for _, line := range strings.Split(config, "\n") {
		if strings.HasPrefix(line, "interface "+primaryIface+" ") || line == "interface "+primaryIface {
			primaryLine = line
			break
		}
	}
	if primaryLine == "" {
		t.Fatalf("failed to locate primary interface line, actual config:\n%s", config)
	}
	if strings.Contains(primaryLine, "rxcost") {
		t.Errorf("primary link (cost 0) interface line should not carry an rxcost token, actual line: %q", primaryLine)
	}
}

// TestBabelAnnounce_PresetTimersPresent verifies D78: hello-interval /
// update-interval come from the role preset (default 4 / 16), and local-port
// comes from a named constant (default 33123).
func TestBabelAnnounce_PresetTimersPresent(t *testing.T) {
	_, routerNode, _, _, domain := representativeBabelTopology()

	peers := []compiler.PeerInfo{
		{NodeID: "node-peer", NodeName: "peernode", InterfaceName: "wg-peernode"},
	}

	config, err := RenderBabelConfig(routerNode, peers, domain)
	if err != nil {
		t.Fatalf("render failed: %v", err)
	}

	if !strings.Contains(config, "hello-interval 4") {
		t.Errorf("should contain hello-interval 4 from the preset, actual config:\n%s", config)
	}
	if !strings.Contains(config, "update-interval 16") {
		t.Errorf("should contain update-interval 16 from the preset, actual config:\n%s", config)
	}
	if !strings.Contains(config, "local-port 33123") {
		t.Errorf("should contain local-port 33123 from the named constant, actual config:\n%s", config)
	}
}
