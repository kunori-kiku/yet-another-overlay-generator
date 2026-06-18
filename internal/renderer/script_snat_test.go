package renderer

import (
	"strings"
	"testing"

	"github.com/kunorikiku/yet-another-overlay-generator/internal/compiler"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/model"
)

// snatTestPeers is the minimal per-peer interface list shared by the SNAT tests. The SNAT rule is
// independent of the specific peer — it only cares about the transit pool of the node's domain — so a
// single simplest peer suffices.
func snatTestPeers() []compiler.PeerInfo {
	return []compiler.PeerInfo{
		{NodeID: "n2", NodeName: "beta", InterfaceName: "wg-beta",
			ListenPort: 51820, LocalTransitIP: "10.20.0.1", LocalLinkLocal: "fe80::1"},
	}
}

// TestRenderInstallScript_SNAT_CustomTransitCIDR verifies D38/D39: for a node with a custom
// transit_cidr, every SNAT-related location in its install script (nft/iptables add, cleanup,
// uninstall, and the persistent overlay-snat.service ExecStart/ExecStop) must carry that custom pool
// and must never contain the hard-coded default pool 10.10.0.0/24 — otherwise the source-address fix
// would silently fail for the custom pool.
func TestRenderInstallScript_SNAT_CustomTransitCIDR(t *testing.T) {
	const customCIDR = "10.20.0.0/24"

	topo := &model.Topology{
		Domains: []model.Domain{
			{ID: "d-custom", Name: "custom", CIDR: "10.21.0.0/24", TransitCIDR: customCIDR},
		},
		Nodes: []model.Node{
			{
				ID:        "node-1",
				Name:      "alpha",
				Role:      "router",
				Platform:  "debian",
				DomainID:  "d-custom",
				OverlayIP: "10.21.0.1",
				Capabilities: model.NodeCapabilities{
					CanForward: true,
				},
			},
		},
	}
	node := &topo.Nodes[0]

	transitCIDRs := NodeTransitCIDRs(topo, node)
	if len(transitCIDRs) != 1 || transitCIDRs[0] != customCIDR {
		t.Fatalf("NodeTransitCIDRs should resolve to the custom pool [%s], got %v", customCIDR, transitCIDRs)
	}

	script, err := RenderInstallScript(node, snatTestPeers(), true, transitCIDRs...)
	if err != nil {
		t.Fatalf("render failed: %v", err)
	}

	// The custom pool must appear in every SNAT path: the nft rule, the nft echo, iptables -C/-A, the
	// iptables cleanup, the uninstall section, and the systemd unit's ExecStart/ExecStop. Assert each
	// specific syntax fragment to ensure it is not just "appearing somewhere by coincidence".
	requiredFragments := []string{
		// install-phase nft rule and echo
		`oifname "wg-*" ip saddr ` + customCIDR + ` snat to 10.21.0.1`,
		`SNAT (nftables): transit ` + customCIDR + ` → 10.21.0.1`,
		// install-phase iptables check/add and echo
		`iptables -t nat -C POSTROUTING -o "wg-+" -s ` + customCIDR + ` -j SNAT --to-source 10.21.0.1`,
		`iptables -t nat -A POSTROUTING -o "wg-+" -s ` + customCIDR + ` -j SNAT --to-source 10.21.0.1`,
		`SNAT (iptables): transit ` + customCIDR + ` → 10.21.0.1`,
		// pre-install cleanup (_overlay_snat_cleanup): D52 switched to a chained grep -F loop-delete by
		// pool (independent of --to-source, so rules for a stale overlay IP are also cleared); assert
		// the by-pool filter fragment.
		`grep -F -- '-s ` + customCIDR + `'`,
		// persistent systemd unit (D39)
		`nft add rule inet overlay-snat postrouting oifname "wg-*" ip saddr ` + customCIDR + ` snat to 10.21.0.1`,
		`iptables -t nat -A POSTROUTING -o wg-+ -s ` + customCIDR + ` -j SNAT --to-source 10.21.0.1`,
		`iptables -t nat -D POSTROUTING -o wg-+ -s ` + customCIDR + ` -j SNAT --to-source 10.21.0.1`,
	}
	for _, frag := range requiredFragments {
		if !strings.Contains(script, frag) {
			t.Errorf("custom transit pool script missing SNAT fragment:\n  %q", frag)
		}
	}

	// Key negative assertion: the hard-coded default pool must never appear anywhere. Comments mention
	// transit IPs of the form 10.10.0.x, but the appearance of the full pool string "10.10.0.0/24"
	// means something is still hard-coded.
	if strings.Contains(script, "10.10.0.0/24") {
		t.Errorf("custom transit pool script should not contain the hard-coded default pool 10.10.0.0/24 (D38/D39 regression)")
	}

	// The uninstall section must also delete rules by the custom pool.
	uninstallIdx := strings.Index(script, "Uninstall All")
	cleanupIdx := strings.Index(script, "Remove overlay SNAT rule and service")
	if uninstallIdx < 0 || cleanupIdx < 0 || cleanupIdx < uninstallIdx {
		t.Fatalf("uninstall section missing the SNAT cleanup block")
	}
}

// TestRenderInstallScript_SNAT_DefaultTransitCIDR verifies that a default-domain node still renders
// the default pool 10.10.0.0/24 (both the compatibility path that passes no transitCIDRs and the
// fallback when the domain transit_cidr is left empty).
func TestRenderInstallScript_SNAT_DefaultTransitCIDR(t *testing.T) {
	const defaultCIDR = "10.10.0.0/24"

	topo := &model.Topology{
		Domains: []model.Domain{
			// transit_cidr left empty -> falls back to the default pool
			{ID: "d-default", Name: "default", CIDR: "10.11.0.0/24"},
		},
		Nodes: []model.Node{
			{
				ID:        "node-1",
				Name:      "alpha",
				Role:      "router",
				Platform:  "debian",
				DomainID:  "d-default",
				OverlayIP: "10.11.0.1",
				Capabilities: model.NodeCapabilities{
					CanForward: true,
				},
			},
		},
	}
	node := &topo.Nodes[0]

	transitCIDRs := NodeTransitCIDRs(topo, node)
	if len(transitCIDRs) != 1 || transitCIDRs[0] != defaultCIDR {
		t.Fatalf("NodeTransitCIDRs should fall back to the default pool [%s] for an empty transit_cidr, got %v", defaultCIDR, transitCIDRs)
	}

	script, err := RenderInstallScript(node, snatTestPeers(), true, transitCIDRs...)
	if err != nil {
		t.Fatalf("render failed: %v", err)
	}

	requiredFragments := []string{
		`oifname "wg-*" ip saddr ` + defaultCIDR + ` snat to 10.11.0.1`,
		`iptables -t nat -A POSTROUTING -o "wg-+" -s ` + defaultCIDR + ` -j SNAT --to-source 10.11.0.1`,
		`nft add rule inet overlay-snat postrouting oifname "wg-*" ip saddr ` + defaultCIDR + ` snat to 10.11.0.1`,
		`iptables -t nat -D POSTROUTING -o wg-+ -s ` + defaultCIDR + ` -j SNAT --to-source 10.11.0.1`,
	}
	for _, frag := range requiredFragments {
		if !strings.Contains(script, frag) {
			t.Errorf("default transit pool script missing SNAT fragment:\n  %q", frag)
		}
	}

	// The custom pool should not leak into the default-domain script.
	if strings.Contains(script, "10.20.0.0/24") {
		t.Errorf("default transit pool script should not contain the custom pool 10.20.0.0/24")
	}
}

// TestRenderInstallScript_SNAT_DefaultWhenNoCIDRPassed verifies the variadic compatibility path:
// existing three-argument callers (passing no transitCIDRs) still render the default pool, behaving
// consistently with history.
func TestRenderInstallScript_SNAT_DefaultWhenNoCIDRPassed(t *testing.T) {
	node := &model.Node{
		ID:        "node-1",
		Name:      "alpha",
		Role:      "router",
		Platform:  "debian",
		OverlayIP: "10.11.0.1",
		Capabilities: model.NodeCapabilities{
			CanForward: true,
		},
	}

	// Pass no transitCIDRs (using the historical three-argument call form).
	script, err := RenderInstallScript(node, snatTestPeers(), true)
	if err != nil {
		t.Fatalf("render failed: %v", err)
	}

	if !strings.Contains(script, `oifname "wg-*" ip saddr 10.10.0.0/24 snat to 10.11.0.1`) {
		t.Errorf("with no transitCIDRs passed, the SNAT rule should default to the pool 10.10.0.0/24")
	}
}

// TestNodeTransitCIDRs_UnknownDomain verifies that when a node's DomainID does not resolve to any
// domain, the lookup function safely falls back to the default pool instead of returning an empty
// slice (avoiding a script rendered with no SNAT rule at all).
func TestNodeTransitCIDRs_UnknownDomain(t *testing.T) {
	topo := &model.Topology{
		Domains: []model.Domain{
			{ID: "d-other", TransitCIDR: "10.30.0.0/24"},
		},
		Nodes: []model.Node{
			{ID: "node-1", DomainID: "d-missing"},
		},
	}
	got := NodeTransitCIDRs(topo, &topo.Nodes[0])
	if len(got) != 1 || got[0] != "10.10.0.0/24" {
		t.Errorf("an unknown domain should fall back to the default pool [10.10.0.0/24], got %v", got)
	}
}
