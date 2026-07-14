package renderer

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kunorikiku/yet-another-overlay-generator/internal/compiler"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/model"
)

// updateDeployGolden regenerates the deploy-script golden files instead of asserting against them.
// Mirrors the -update pattern in internal/localcompile/manifest_golden_test.go. The renderer package
// has no other -update flag, so registering "update" here does not collide (each package's tests build
// into a separate binary).
var updateDeployGolden = flag.Bool("update", false, "regenerate the deploy-script golden files from the current renderer output")

// deployGoldenDir holds the frozen deploy-all.sh / deploy-all.ps1 characterization goldens.
const deployGoldenDir = "testdata/deploy"

// deployGoldenTopology builds a representative multi-node topology + peerMap + babelConfigs for the
// deploy-script golden. It deliberately spans every teardown surface deploy.go emits so a drift in the
// per-node uninstall (SNAT / mimic) turns the golden red:
//
//   - alpha: a forwarding ROUTER with per-peer WireGuard interfaces (wg-beta, wg-gamma) and a mimic
//     (transport=tcp) peer -> HasMimic with the AUTO-DETECT egress (no override); exercises the
//     per-node SNAT teardown + dummy0 removal + the mimic teardown's default-route egress detection.
//   - beta:  a ROUTER on a NON-DEFAULT transit CIDR (domain-b transit_cidr 10.99.0.0/24) with a mimic
//     peer AND an egress OVERRIDE (wan0) -> proves the SNAT teardown is CIDR-agnostic and the mimic
//     teardown honors the operator override (bashSingleQuote'd).
//   - gamma: a CLIENT (wg0, no Babel). Its peerMap entry is empty (the compiler never adds PeerInfo for
//     a client), so HasMimic is false and it renders neither SNAT/dummy0 nor mimic teardown.
//   - delta: a node with NO SSH details -> the SKIPPED branch (no teardown), covering the skip path.
func deployGoldenTopology() (*model.Topology, map[string][]compiler.PeerInfo, map[string]string) {
	topo := &model.Topology{
		Project: model.Project{ID: "deploy-golden", Name: "Deploy Golden"},
		Domains: []model.Domain{
			// domain-a: default transit pool (empty TransitCIDR -> 10.10.0.0/24).
			{ID: "domain-a", Name: "domain-a", CIDR: "10.50.0.0/24", RoutingMode: "babel"},
			// domain-b: a NON-default transit pool, to cover the CIDR-agnostic SNAT teardown.
			{ID: "domain-b", Name: "domain-b", CIDR: "10.60.0.0/24", RoutingMode: "babel", TransitCIDR: "10.99.0.0/24"},
		},
		Nodes: []model.Node{
			{
				ID: "alpha", Name: "alpha", Role: "router", Platform: "debian",
				DomainID: "domain-a", OverlayIP: "10.50.0.1",
				SSHHost: "alpha.example.com", SSHUser: "root",
				Capabilities: model.NodeCapabilities{CanForward: true},
			},
			{
				ID: "beta", Name: "beta", Role: "router", Platform: "debian",
				DomainID: "domain-b", OverlayIP: "10.60.0.1",
				SSHAlias:             "beta-alias",
				MimicEgressInterface: "wan0", // egress override
				Capabilities:         model.NodeCapabilities{CanForward: true},
			},
			{
				ID: "gamma", Name: "gamma", Role: "client", Platform: "debian",
				DomainID: "domain-a", OverlayIP: "10.50.0.9",
				SSHHost: "10.0.0.9", SSHUser: "deploy", SSHPort: 2222, SSHKeyPath: "/home/op/keys/id_ed25519",
			},
			{
				ID: "delta", Name: "delta", Role: "peer", Platform: "debian",
				DomainID: "domain-a", OverlayIP: "10.50.0.4",
				// no SSH details -> SKIPPED
			},
		},
	}

	peerMap := map[string][]compiler.PeerInfo{
		"alpha": {
			{NodeID: "beta", NodeName: "beta", InterfaceName: "wg-beta", ListenPort: 51820, Mimic: true},
			{NodeID: "gamma", NodeName: "gamma", InterfaceName: "wg-gamma", ListenPort: 51821, IsClientPeer: true},
		},
		"beta": {
			{NodeID: "alpha", NodeName: "alpha", InterfaceName: "wg-alpha", ListenPort: 51820, Mimic: true},
		},
		// gamma is a client: the compiler adds no PeerInfo here (wg0 lives in ClientConfigs).
		"gamma": {},
		"delta": {
			{NodeID: "alpha", NodeName: "alpha", InterfaceName: "wg-alpha", ListenPort: 51822},
		},
	}

	// Routers run Babel; the client (gamma) and the skipped node (delta) do not.
	babelConfigs := map[string]string{
		"alpha": "# babeld.conf for alpha\n",
		"beta":  "# babeld.conf for beta\n",
	}

	return topo, peerMap, babelConfigs
}

// TestRenderDeployScripts_Golden is the characterization gate for the operator deploy scripts. It
// freezes deploy-all.sh and deploy-all.ps1 byte-for-byte so any future drift in the per-node teardown
// (the SNAT + mimic lines this plan corrects, and everything else) is caught. Run with -update to
// (re)freeze after an intentional deploy-script change.
func TestRenderDeployScripts_Golden(t *testing.T) {
	topo, peerMap, babelConfigs := deployGoldenTopology()
	bash, ps1, err := RenderDeployScripts(topo, peerMap, babelConfigs)
	if err != nil {
		t.Fatalf("RenderDeployScripts: %v", err)
	}
	checkDeployGolden(t, "deploy-all.sh.golden", bash)
	checkDeployGolden(t, "deploy-all.ps1.golden", ps1)
}

// checkDeployGolden compares one rendered script against its committed golden, or rewrites it under
// -update. On mismatch it reports the first differing line for a legible failure.
func checkDeployGolden(t *testing.T, name, got string) {
	t.Helper()
	path := filepath.Join(deployGoldenDir, name)
	if *updateDeployGolden {
		if err := os.MkdirAll(deployGoldenDir, 0o755); err != nil {
			t.Fatalf("ensure golden dir: %v", err)
		}
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatalf("write golden %s: %v", path, err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read golden %s (run with -update to generate): %v", path, err)
	}
	if string(want) != got {
		t.Errorf("%s diverges from golden %s\n%s", name, path, firstLineDivergence(string(want), got))
	}
}

// firstLineDivergence returns a human-readable first-differing-line report for a golden mismatch.
func firstLineDivergence(want, got string) string {
	wl := strings.Split(want, "\n")
	gl := strings.Split(got, "\n")
	n := len(wl)
	if len(gl) < n {
		n = len(gl)
	}
	for i := 0; i < n; i++ {
		if wl[i] != gl[i] {
			return fmt.Sprintf("first differing line %d:\n  want: %q\n  got:  %q", i+1, wl[i], gl[i])
		}
	}
	if len(wl) != len(gl) {
		return fmt.Sprintf("line count differs: want %d lines, got %d", len(wl), len(gl))
	}
	return "(identical by line; differs only in trailing bytes)"
}
