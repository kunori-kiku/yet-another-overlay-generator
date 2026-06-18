package render

import (
	"strings"
	"testing"

	"github.com/kunorikiku/yet-another-overlay-generator/internal/compiler"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/model"
	"golang.zx2c4.com/wireguard/wgctrl/wgtypes"
)

// TestEntrypointParity is the equivalence gate for the "shared render entrypoint".
//
// Before this PR, the API (internal/api/handler.go) and the CLI (cmd/compiler)
// each maintained their own copy of the render logic; the CLI's was a degenerate
// implementation: it stuffed in literal FAKE_PRIVKEY_*, never rendered the
// client's wg0.conf, generated no client install script, and generated no
// deploy-all script (audit theme T6: D6 / D27-29 / D59). This PR extracts
// GenerateKeys + All into this shared package, so both entrypoints now follow
// the exact same path: render.GenerateKeys -> compiler.Compile -> render.All.
// This test locks in the key artifacts that path must produce; any regression
// back to forked behavior (missing client render, missing deploy render, FAKE_
// reappearing) makes it fail.
//
// The topology deliberately contains all three of the router, peer, and client
// roles to ensure coverage of the three branches inside render.All: per-peer
// WireGuard, the client's single wg0, and both the client and per-peer install
// script templates.
//
// The three private keys are generated once in the test with
// wgtypes.GeneratePrivateKey and written onto the nodes' WireGuardPrivateKey
// (falling into GenerateKeys case (a): reuse the private key when present), so
// that the keys within this run are deterministic and are real WireGuard
// private keys, and the rendered config contains no placeholder strings.
func TestEntrypointParity(t *testing.T) {
	// Generate three real WireGuard private keys once and pin them onto the
	// three nodes so GenerateKeys takes the "reuse the private key when present"
	// branch (case a), making the render result deterministic within this run.
	routerKey := mustGenerateKey(t)
	peerKey := mustGenerateKey(t)
	clientKey := mustGenerateKey(t)

	topo := &model.Topology{
		Project: model.Project{ID: "parity-001", Name: "Entrypoint Parity", Version: "1"},
		Domains: []model.Domain{{
			ID: "domain-1", Name: "parity-net", CIDR: "10.40.0.0/24",
			AllocationMode: "auto", RoutingMode: "babel",
		}},
		Nodes: []model.Node{
			{
				ID: "router-1", Name: "router-1", Hostname: "router-1.example",
				Role: "router", DomainID: "domain-1",
				Capabilities: model.NodeCapabilities{
					CanAcceptInbound: true, CanForward: true, HasPublicIP: true,
				},
				PublicEndpoints: []model.PublicEndpoint{
					{ID: "router-1-ep", Host: "router-1.example", Port: 51820},
				},
				WireGuardPrivateKey: routerKey.String(),
			},
			{
				ID: "peer-1", Name: "peer-1",
				Role: "peer", DomainID: "domain-1",
				Capabilities: model.NodeCapabilities{
					CanAcceptInbound: false, CanForward: false, HasPublicIP: false,
				},
				WireGuardPrivateKey: peerKey.String(),
			},
			{
				ID: "client-1", Name: "client-1",
				Role: "client", DomainID: "domain-1",
				WireGuardPrivateKey: clientKey.String(),
			},
		},
		Edges: []model.Edge{
			// peer-1 -> router-1: peer actively connects to the public router (must carry endpoint_host).
			{ID: "e-peer", FromNodeID: "peer-1", ToNodeID: "router-1", Type: "public-endpoint",
				EndpointHost: "router-1.example", Transport: "udp", IsEnabled: true},
			// client-1 -> router-1: the client's sole outbound edge (must carry endpoint_host).
			{ID: "e-client", FromNodeID: "client-1", ToNodeID: "router-1", Type: "public-endpoint",
				EndpointHost: "router-1.example", Transport: "udp", IsEnabled: true},
		},
	}

	// Follow the exact same shared path as the API/CLI.
	keys, err := GenerateKeys(topo, AirGap)
	if err != nil {
		t.Fatalf("GenerateKeys failed: %v", err)
	}

	// GenerateKeys should reuse the pinned private key (case a) and write back a
	// public key derived from the private key.
	if got := keys["router-1"].PrivateKey; got != routerKey.String() {
		t.Errorf("router-1 private key should be reused verbatim, want %q, got %q", routerKey.String(), got)
	}
	if got := keys["router-1"].PublicKey; got != routerKey.PublicKey().String() {
		t.Errorf("router-1 public key should be derived from the private key, want %q, got %q", routerKey.PublicKey().String(), got)
	}

	c := compiler.NewCompiler()
	result, err := c.Compile(topo, keys)
	if err != nil {
		t.Fatalf("Compile failed: %v", err)
	}

	if err := All(result, keys, FetchSettings{}); err != nil {
		t.Fatalf("render.All failed: %v", err)
	}

	// Assertion 1: the client node has a "client-1:wg0" WireGuard config (client template, D27).
	clientWG, ok := result.WireGuardConfigs["client-1:wg0"]
	if !ok {
		t.Fatalf("client node should have a %q WireGuard config (client wg0 template); existing keys: %v",
			"client-1:wg0", keysOf(result.WireGuardConfigs))
	}
	if !strings.Contains(clientWG, "wg0") {
		t.Errorf("client wg0 config should mention the interface name wg0, actual content:\n%s", clientWG)
	}

	// Assertion 2: the client node has an install script, and it is the client template (contains wg0, D28/D29).
	clientInstall, ok := result.InstallScripts["client-1"]
	if !ok {
		t.Fatalf("client node should have an install script; existing keys: %v", keysOf(result.InstallScripts))
	}
	if !strings.Contains(clientInstall, "wg0") {
		t.Errorf("client install script should use the client template (contains wg0), but wg0 did not appear")
	}

	// Assertion 3: the deploy scripts appear under the deploy-all.sh / deploy-all.ps1 keys (D59).
	if _, ok := result.DeployScripts["deploy-all.sh"]; !ok {
		t.Errorf("deploy-all.sh should be generated; existing keys: %v", keysOf(result.DeployScripts))
	}
	if _, ok := result.DeployScripts["deploy-all.ps1"]; !ok {
		t.Errorf("deploy-all.ps1 should be generated; existing keys: %v", keysOf(result.DeployScripts))
	}

	// Assertion 4: no rendered artifact may contain FAKE_ (the CLI's old placeholder keys are gone entirely, D6).
	assertNoFake(t, "WireGuardConfigs", result.WireGuardConfigs)
	assertNoFake(t, "BabelConfigs", result.BabelConfigs)
	assertNoFake(t, "SysctlConfigs", result.SysctlConfigs)
	assertNoFake(t, "InstallScripts", result.InstallScripts)
	assertNoFake(t, "DeployScripts", result.DeployScripts)
}

// mustGenerateKey generates one real WireGuard private key, terminating the test on failure.
func mustGenerateKey(t *testing.T) wgtypes.Key {
	t.Helper()
	key, err := wgtypes.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("failed to generate WireGuard private key: %v", err)
	}
	return key
}

// assertNoFake asserts that no value in the map contains the literal FAKE_ (the marker of the old CLI placeholder keys, D6).
func assertNoFake(t *testing.T, label string, m map[string]string) {
	t.Helper()
	for key, value := range m {
		if strings.Contains(value, "FAKE_") {
			t.Errorf("%s[%q] should not contain the placeholder string FAKE_ (D6 regression)", label, key)
		}
	}
}

// keysOf returns the set of keys of the map, used only for diagnostic output on assertion failure.
func keysOf(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
