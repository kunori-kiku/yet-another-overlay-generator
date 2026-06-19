//go:build linux && integration

package realtunnel

import "testing"

// canary_test.go — the MANDATORY rc.1-gating floor (plan-18 / 3.6). Brings up the shipped simple-mesh
// (3 routers, full mesh) from a real cmd/compiler bundle inside per-node systemd-nspawn containers
// running the UNMODIFIED install.sh, then asserts the full MVV floor on the kernel:
//   (a) per-interface WireGuard handshake
//   (b) babel-converged kernel routes to every node's OverlayIP/32
//   (c) end-to-end overlay ping, 0% loss
//   (d) overlay-SNAT transit->overlay source rewrite (rule installed + functionally rewriting)
// A red here blocks the v2.0.0-rc.1 tag.

func TestSimpleMeshCanary(t *testing.T) {
	rootfs := requireCapabilities(t)
	sc := bringUp(t, rootfs, repoFile(t, "examples/simple-mesh/topology.json"))
	// Dump kernel/WG/babel state on failure (registered AFTER bringUp so it runs BEFORE the
	// container teardowns — LIFO — while the containers are still alive).
	t.Cleanup(func() {
		if t.Failed() {
			sc.dumpDiagnostics(t)
		}
	})

	sc.requireHandshakes(t)       // (a)
	sc.requireRouteConvergence(t) // (b)
	sc.requireOverlayPing(t)      // (c)
	sc.requireSNATRewrite(t)      // (d)
}
