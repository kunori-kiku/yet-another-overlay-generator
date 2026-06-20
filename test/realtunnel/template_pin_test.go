//go:build linux && integration

package realtunnel

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// template_pin_test.go — the anti-drift guard (plan-18 Phase 5, DoD #7). Option B runs the UNMODIFIED
// install.sh, so the harness does not extract command lines — but its activation + data-plane
// assertions still ASSUME the rendered script brings up dummy0, each wg-quick interface, the overlay
// SNAT rule, and babeld. This test greps a freshly-rendered install.sh for those load-bearing command
// shapes and FAILS LOUD if internal/renderer/script.go changes them, forcing the script and this
// harness to be reconciled in the same PR (the same drift class the rc.1 program is fighting). It
// needs no root — it only compiles + renders the shipped fixture.

func TestTemplateShapePin(t *testing.T) {
	topo := loadTopology(t, repoFile(t, "examples/simple-mesh/topology.json"))
	out := t.TempDir()
	b := produceBundle(t, topo, out)
	dirs := b.requireBundleFiles(t)

	data, err := os.ReadFile(filepath.Join(out, dirs[0], "install.sh"))
	if err != nil {
		t.Fatalf("read rendered install.sh: %v", err)
	}
	script := string(data)

	// Each required command shape (substring) the harness's assertions depend on.
	for _, shape := range []string{
		"dummy0 type dummy",     // the stable overlay address interface (requireOverlayPing target source)
		"wg-quick up",           // per-interface tunnel bring-up (requireHandshakes / wgInterfaces)
		"babeld -c /etc/babel/", // the routing daemon (requireRouteConvergence)
	} {
		if !strings.Contains(script, shape) {
			t.Errorf("install.sh no longer contains %q — script.go changed the command shape the realtunnel "+
				"harness relies on; reconcile internal/renderer/script.go and this harness in the same PR", shape)
		}
	}

	// The overlay SNAT rule renders via nft OR iptables; require one of the two shapes (requireSNATRewrite).
	if !strings.Contains(script, "snat to") && !strings.Contains(script, "-j SNAT --to-source") {
		t.Errorf("install.sh no longer installs an overlay SNAT rule (expected `snat to` or `-j SNAT --to-source`) " +
			"— reconcile script.go and the realtunnel SNAT assertion in the same PR")
	}
}
