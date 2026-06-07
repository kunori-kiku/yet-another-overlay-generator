package render

import (
	"strings"
	"testing"

	"github.com/kunorikiku/yet-another-overlay-generator/internal/compiler"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/model"
	"golang.zx2c4.com/wireguard/wgctrl/wgtypes"
)

// custodyTopology builds a router + peer + client topology used by the custody
// tests. With publicOnly=true each node carries only its WireGuard PUBLIC key
// (the agent-registered, zero-knowledge scenario); with publicOnly=false each
// node carries its PRIVATE key (the air-gap scenario, public derived by
// GenerateKeys). The three keys are passed in so two copies can be built with
// identical key material for the diff test.
func custodyTopology(rk, pk, ck wgtypes.Key, publicOnly bool) *model.Topology {
	field := func(k wgtypes.Key) (priv, pub string) {
		if publicOnly {
			return "", k.PublicKey().String()
		}
		return k.String(), ""
	}
	rkPriv, rkPub := field(rk)
	pkPriv, pkPub := field(pk)
	ckPriv, ckPub := field(ck)
	return &model.Topology{
		Project: model.Project{ID: "custody-001", Name: "Custody", Version: "1"},
		Domains: []model.Domain{{
			ID: "domain-1", Name: "custody-net", CIDR: "10.42.0.0/24",
			AllocationMode: "auto", RoutingMode: "babel",
		}},
		Nodes: []model.Node{
			{
				ID: "router-1", Name: "router-1", Role: "router", DomainID: "domain-1",
				Capabilities:        model.NodeCapabilities{CanAcceptInbound: true, CanForward: true, HasPublicIP: true},
				PublicEndpoints:     []model.PublicEndpoint{{ID: "ep", Host: "router-1.example", Port: 51820}},
				WireGuardPrivateKey: rkPriv, WireGuardPublicKey: rkPub,
			},
			{
				ID: "peer-1", Name: "peer-1", Role: "peer", DomainID: "domain-1",
				WireGuardPrivateKey: pkPriv, WireGuardPublicKey: pkPub,
			},
			{
				ID: "client-1", Name: "client-1", Role: "client", DomainID: "domain-1",
				WireGuardPrivateKey: ckPriv, WireGuardPublicKey: ckPub,
			},
		},
		Edges: []model.Edge{
			{ID: "e-peer", FromNodeID: "peer-1", ToNodeID: "router-1", Type: "public-endpoint",
				EndpointHost: "router-1.example", Transport: "udp", IsEnabled: true},
			{ID: "e-client", FromNodeID: "client-1", ToNodeID: "router-1", Type: "public-endpoint",
				EndpointHost: "router-1.example", Transport: "udp", IsEnabled: true},
		},
	}
}

// privateKeyLineValues returns the value of every `PrivateKey = X` line across all
// rendered artifacts (per-peer WG, client wg0, babel, sysctl, install scripts,
// deploy scripts). A node's WireGuard private key appears only on these lines.
func privateKeyLineValues(result *compiler.CompileResult) []string {
	var vals []string
	groups := []map[string]string{
		result.WireGuardConfigs, result.BabelConfigs, result.SysctlConfigs,
		result.InstallScripts, result.DeployScripts,
	}
	for _, m := range groups {
		for _, content := range m {
			for _, line := range strings.Split(content, "\n") {
				t := strings.TrimSpace(line)
				if !strings.HasPrefix(t, "PrivateKey") {
					continue
				}
				eq := strings.Index(t, "=")
				if eq < 0 {
					continue
				}
				vals = append(vals, strings.TrimSpace(t[eq+1:]))
			}
		}
	}
	return vals
}

// TestGenerateKeys_AgentHeld_NoPrivateKeyEmitted is the perpetual zero-knowledge
// custody guard: rendering a public-only fleet in AgentHeld mode must never emit a
// real WireGuard private key — every [Interface] PrivateKey line must be the
// placeholder, and the placeholder must not parse as a WG key.
func TestGenerateKeys_AgentHeld_NoPrivateKeyEmitted(t *testing.T) {
	topo := custodyTopology(mustGenerateKey(t), mustGenerateKey(t), mustGenerateKey(t), true)

	keys, err := GenerateKeys(topo, AgentHeld)
	if err != nil {
		t.Fatalf("GenerateKeys(AgentHeld): %v", err)
	}
	for id, kp := range keys {
		if kp.PrivateKey != PrivateKeyPlaceholder {
			t.Errorf("node %s: keys map carries a non-placeholder private key %q", id, kp.PrivateKey)
		}
		if _, err := wgtypes.ParseKey(kp.PrivateKey); err == nil {
			t.Errorf("node %s: placeholder unexpectedly parses as a WG key", id)
		}
	}

	result, err := compiler.NewCompiler().Compile(topo, keys)
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	if err := All(result, keys); err != nil {
		t.Fatalf("render.All: %v", err)
	}

	vals := privateKeyLineValues(result)
	if len(vals) == 0 {
		t.Fatal("expected at least one PrivateKey line in the rendered fleet")
	}
	for _, v := range vals {
		if v != PrivateKeyPlaceholder {
			t.Errorf("AgentHeld render emitted a non-placeholder PrivateKey %q (zero-knowledge custody violated)", v)
		}
	}
}

// TestGenerateKeys_AgentHeld_RequiresPublicKey asserts AgentHeld errors when a
// node has no key material at all (the agent must register a public key first).
func TestGenerateKeys_AgentHeld_RequiresPublicKey(t *testing.T) {
	topo := &model.Topology{
		Project: model.Project{ID: "c", Name: "c", Version: "1"},
		Domains: []model.Domain{{ID: "d", Name: "d", CIDR: "10.42.0.0/24", AllocationMode: "auto", RoutingMode: "babel"}},
		Nodes:   []model.Node{{ID: "n", Name: "n", Role: "router", DomainID: "d"}},
	}
	if _, err := GenerateKeys(topo, AgentHeld); err == nil {
		t.Fatal("AgentHeld must error when a node has neither a public nor a private key")
	}
}

// TestGenerateKeys_AgentHeld_DiscardsStrayPrivateKey asserts that if a node is
// imported with a private key, AgentHeld derives the public half, discards the
// private key on the node, and emits only the placeholder.
func TestGenerateKeys_AgentHeld_DiscardsStrayPrivateKey(t *testing.T) {
	k := mustGenerateKey(t)
	topo := &model.Topology{
		Project: model.Project{ID: "c", Name: "c", Version: "1"},
		Domains: []model.Domain{{ID: "d", Name: "d", CIDR: "10.42.0.0/24", AllocationMode: "auto", RoutingMode: "babel"}},
		Nodes:   []model.Node{{ID: "n", Name: "n", Role: "router", DomainID: "d", WireGuardPrivateKey: k.String()}},
	}
	keys, err := GenerateKeys(topo, AgentHeld)
	if err != nil {
		t.Fatalf("GenerateKeys(AgentHeld): %v", err)
	}
	if keys["n"].PrivateKey != PrivateKeyPlaceholder {
		t.Errorf("stray private key was not replaced by the placeholder")
	}
	if keys["n"].PublicKey != k.PublicKey().String() {
		t.Errorf("public key was not derived from the stray private key")
	}
	if topo.Nodes[0].WireGuardPrivateKey != "" {
		t.Errorf("stray private key was not cleared from the node (it must not persist in the controller topology)")
	}
}
