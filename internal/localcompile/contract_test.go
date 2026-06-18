package localcompile

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/kunorikiku/yet-another-overlay-generator/internal/bundlesig"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/compiler"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/model"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/render"
)

// simpleMeshTopo is a three-router full mesh — a representative fixture exercising
// per-peer WireGuard interfaces, per-node babeld.conf, sysctl, install.sh, and the
// deploy scripts. It mirrors internal/compiler's simpleMeshTopo (that helper is
// unexported, so it is reconstructed here).
func simpleMeshTopo() model.Topology {
	return model.Topology{
		Project: model.Project{ID: "test-001", Name: "Test Mesh"},
		Domains: []model.Domain{{
			ID: "domain-1", Name: "mesh", CIDR: "10.11.0.0/24",
			AllocationMode: "auto", RoutingMode: "babel",
		}},
		Nodes: []model.Node{
			{
				ID: "node-1", Name: "alpha", Hostname: "alpha.example.com",
				Role: "router", DomainID: "domain-1",
				Capabilities: model.NodeCapabilities{CanAcceptInbound: true, CanForward: true, HasPublicIP: true},
			},
			{
				ID: "node-2", Name: "beta", Hostname: "beta.example.com",
				Role: "router", DomainID: "domain-1",
				Capabilities: model.NodeCapabilities{CanAcceptInbound: true, CanForward: true, HasPublicIP: true},
			},
			{
				ID: "node-3", Name: "gamma", Hostname: "gamma.example.com",
				Role: "router", DomainID: "domain-1",
				Capabilities: model.NodeCapabilities{CanAcceptInbound: true, CanForward: true, HasPublicIP: true},
			},
		},
		Edges: []model.Edge{
			{ID: "e1", FromNodeID: "node-1", ToNodeID: "node-2", Type: "direct", EndpointHost: "203.0.113.2", Transport: "udp", IsEnabled: true},
			{ID: "e2", FromNodeID: "node-2", ToNodeID: "node-1", Type: "direct", EndpointHost: "203.0.113.1", Transport: "udp", IsEnabled: true},
			{ID: "e3", FromNodeID: "node-1", ToNodeID: "node-3", Type: "direct", EndpointHost: "203.0.113.3", Transport: "udp", IsEnabled: true},
			{ID: "e4", FromNodeID: "node-3", ToNodeID: "node-1", Type: "direct", EndpointHost: "203.0.113.1", Transport: "udp", IsEnabled: true},
			{ID: "e5", FromNodeID: "node-2", ToNodeID: "node-3", Type: "direct", EndpointHost: "203.0.113.3", Transport: "udp", IsEnabled: true},
			{ID: "e6", FromNodeID: "node-3", ToNodeID: "node-2", Type: "direct", EndpointHost: "203.0.113.2", Transport: "udp", IsEnabled: true},
		},
	}
}

// TestCompile_LosslessWrapper proves the façade's re-shaped CompileArtifacts equals the
// field-by-field content of a directly-run compiler.CompileResult (+ render.All) — the
// lossless-wrapper anchor (plan-3 Phase 1). It builds the direct path first to obtain a
// stable key set, then pins those keys onto the request's topology so the façade's
// GenerateKeys re-derives the same pair (case-a) instead of generating fresh random keys.
func TestCompile_LosslessWrapper(t *testing.T) {
	// 1. Direct path: GenerateKeys (case-c, fresh keys) -> Compile -> render.All.
	directTopo := simpleMeshTopo()
	keys, err := render.GenerateKeys(&directTopo, render.AirGap)
	if err != nil {
		t.Fatalf("GenerateKeys (direct): %v", err)
	}
	directResult, err := compiler.NewCompiler().Compile(context.Background(), &directTopo, keys)
	if err != nil {
		t.Fatalf("Compile (direct): %v", err)
	}
	if err := render.All(directResult, keys, render.FetchSettings{}); err != nil {
		t.Fatalf("render.All (direct): %v", err)
	}

	// 2. Façade path: pin the generated private keys onto a fresh topology so
	// GenerateKeys reuses them (case-a) and the two runs allocate identically.
	reqTopo := simpleMeshTopo()
	for i := range reqTopo.Nodes {
		reqTopo.Nodes[i].WireGuardPrivateKey = directTopo.Nodes[i].WireGuardPrivateKey
	}
	got, err := Compile(CompileRequest{Topology: reqTopo, Custody: render.AirGap})
	if err != nil {
		t.Fatalf("Compile (façade): %v", err)
	}

	// 3. Rebuild the expected per-node Files / Checksums / Deploy from the direct
	// result, mirroring artifacts.Export's bundleFiles build, and compare field by
	// field. Signing is off in this test, so Signatures stays empty.
	wantFiles := make(map[string]map[string]string)
	wantChecksums := make(map[string]string)
	for _, node := range directResult.Topology.Nodes {
		bundleFiles := make(map[string]string)
		for configKey, wgConf := range directResult.WireGuardConfigs {
			parts := strings.SplitN(configKey, ":", 2)
			if len(parts) != 2 || parts[0] != node.ID {
				continue
			}
			bundleFiles["wireguard/"+parts[1]+".conf"] = wgConf
		}
		if babelConf, ok := directResult.BabelConfigs[node.ID]; ok {
			bundleFiles["babel/babeld.conf"] = babelConf
		}
		if sysctlConf, ok := directResult.SysctlConfigs[node.ID]; ok {
			bundleFiles["sysctl/99-overlay.conf"] = sysctlConf
		}
		if script, ok := directResult.InstallScripts[node.ID]; ok {
			bundleFiles["install.sh"] = script
		}
		if aj, ok := directResult.ArtifactsJSON[node.ID]; ok && aj != "" {
			bundleFiles["artifacts.json"] = aj
		}
		wantFiles[node.ID] = bundleFiles
		wantChecksums[node.ID] = string(bundlesig.Canonicalize(bundleFiles))
	}

	if !reflect.DeepEqual(got.Files, wantFiles) {
		t.Errorf("Files mismatch:\n got %#v\nwant %#v", got.Files, wantFiles)
	}
	if !reflect.DeepEqual(got.Checksums, wantChecksums) {
		t.Errorf("Checksums mismatch:\n got %#v\nwant %#v", got.Checksums, wantChecksums)
	}
	if !reflect.DeepEqual(got.Deploy, directResult.DeployScripts) {
		t.Errorf("Deploy mismatch:\n got %#v\nwant %#v", got.Deploy, directResult.DeployScripts)
	}
	if !reflect.DeepEqual(got.Warnings, directResult.Warnings) {
		t.Errorf("Warnings mismatch:\n got %#v\nwant %#v", got.Warnings, directResult.Warnings)
	}
	// Compare the manifest with CompiledAt masked: it is a time.Now() impurity, OUT of
	// the conformance byte set (the two runs compile at different wall-clock instants).
	// Everything else — including the display-only Checksum — must match.
	gotManifest, wantManifest := got.Manifest, directResult.Manifest
	gotManifest.CompiledAt, wantManifest.CompiledAt = time.Time{}, time.Time{}
	if !reflect.DeepEqual(gotManifest, wantManifest) {
		t.Errorf("Manifest mismatch (CompiledAt masked):\n got %#v\nwant %#v", gotManifest, wantManifest)
	}
	// Signing off: no detached signatures, no pubkey.
	if len(got.Signatures) != 0 {
		t.Errorf("expected no signatures with signing off, got %d", len(got.Signatures))
	}
	if got.SigningPubPEM != nil {
		t.Errorf("expected nil SigningPubPEM with signing off, got %q", got.SigningPubPEM)
	}

	// The compiled topology carries the allocator write-backs (overlay IPs, pins).
	if got.Topology == nil || len(got.Topology.Nodes) != 3 {
		t.Fatalf("expected a compiled topology with 3 nodes, got %#v", got.Topology)
	}
	for _, node := range got.Topology.Nodes {
		if node.OverlayIP == "" {
			t.Errorf("node %s has no overlay IP written back", node.ID)
		}
	}

	// Guard the contract's documented per-node relpath shape: alpha (router) must carry
	// per-peer wireguard confs, a babeld.conf, sysctl, and install.sh.
	alpha := got.Files["node-1"]
	for _, want := range []string{"babel/babeld.conf", "sysctl/99-overlay.conf", "install.sh"} {
		if _, ok := alpha[want]; !ok {
			t.Errorf("node-1 bundle missing %q (keys: %v)", want, keysOf(alpha))
		}
	}
	hasWG := false
	for k := range alpha {
		if strings.HasPrefix(k, "wireguard/") && strings.HasSuffix(k, ".conf") {
			hasWG = true
		}
	}
	if !hasWG {
		t.Errorf("node-1 bundle missing a wireguard/<iface>.conf (keys: %v)", keysOf(alpha))
	}
}

func keysOf(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
