//go:build linux && integration

package realtunnel

import (
	"os"
	"strings"
	"testing"
)

// scenarios_test.go — the ADDITIVE scenario tier (plan-18 Phase 7). None of these gate rc.1 (the
// simple-mesh canary is the required floor); each runs ONLY when explicitly selected via
// REALTUNNEL_SCENARIOS (a comma list of scenario keys, or "all"). They extend coverage beyond the
// full-mesh canary to: the C3 reverse-endpoint contract, relay transit reachability, and router
// hub-and-spoke forwarding — each on the real kernel.

// requireScenario skips unless this scenario key is selected in REALTUNNEL_SCENARIOS (or "all" is
// present). It first runs the capability preflight, so a selected-but-incapable host still skips
// cleanly. Returns the resolved base-rootfs path.
func requireScenario(t *testing.T, key string) string {
	t.Helper()
	rootfs := requireCapabilities(t)
	sel := os.Getenv("REALTUNNEL_SCENARIOS")
	if !scenarioSelected(sel, key) {
		t.Skipf("realtunnel: additive scenario %q not selected — set REALTUNNEL_SCENARIOS=all "+
			"(or a comma list including %q) to run it", key, key)
	}
	return rootfs
}

// scenarioSelected reports whether sel (a comma-separated list) selects key, with "all" matching any.
func scenarioSelected(sel, key string) bool {
	for _, tok := range strings.Split(sel, ",") {
		switch strings.TrimSpace(tok) {
		case "all", key:
			return true
		}
	}
	return false
}

// onFailDump registers the forensic kernel/WG/babel dump to run (LIFO, before container teardown)
// only if the test failed.
func onFailDump(t *testing.T, sc *scenario) {
	t.Helper()
	t.Cleanup(func() {
		if t.Failed() {
			sc.dumpDiagnostics(t)
		}
	})
}

// TestC3OneDirectional (scenario key "c3") is the C3 reverse-endpoint regression guard on a real
// kernel. C3 (investigation-report.md) was the bug where the reverse-Endpoint fallback keyed on the
// raw has_public_ip flag, so a node with a real public_endpoints entry but has_public_ip=false got an
// EMPTY reverse Endpoint (a wrong-but-valid one-directional link). plan-8 fixed it by normalizing
// HasPublicIP up from len(PublicEndpoints) (roles.go InferCapabilitiesFromRole, feeding the fallback
// at peers.go:855). This fixture pins the fix: two NAT-side peers dial one hub with no reverse edges —
//   - c3-endpoint sets public_endpoints WHILE has_public_ip=false → its reverse Endpoint MUST be
//     populated (the normalization fired); revert the fix and this assertion goes red on the wire.
//   - c3-natpeer is genuinely unreachable (no public_endpoints) → its reverse Endpoint MUST be empty
//     (correct one-directional — the hub can never dial it; the peer always dials in).
//
// The kernel run then proves the contrast is real: both tunnels still form (the peers dial in) and
// the overlay routes.
func TestC3OneDirectional(t *testing.T) {
	rootfs := requireScenario(t, "c3")
	sc := bringUp(t, rootfs, repoFile(t, "test/realtunnel/testdata/c3-onedir/topology.json"))
	onFailDump(t, sc)

	// The C3 contract, asserted on the rendered bundle (deterministic, race-free).
	if !sc.reverseEndpointPresent(t, "c3-hub", "c3-endpoint") {
		t.Fatalf("C3 regression: c3-hub's reverse Endpoint for the endpoint-bearing peer c3-endpoint is " +
			"EMPTY — the HasPublicIP normalization (roles.go) that fixes C3 did not fire")
	}
	if sc.reverseEndpointPresent(t, "c3-hub", "c3-natpeer") {
		t.Fatalf("C3 fixture invalid: c3-hub has a reverse Endpoint for c3-natpeer, which has no " +
			"public_endpoints — it must be empty (correct one-directional)")
	}

	// The kernel run: both peers dial the hub, so both tunnels form and the overlay routes.
	sc.requireHandshakes(t)
	sc.requireRouteConvergence(t, allPairs)
	sc.requireOverlayPing(t, allPairs)
}

// TestRelayTopology (scenario key "relay") exercises relay transit: two NAT peers with no direct edge
// reach each other ONLY through the relay (the relay forwards + babel converges /32s across the two
// tunnels). The all-pairs assertions therefore prove transitive peer<->peer reachability through the
// relay, the whole point of the relay role.
func TestRelayTopology(t *testing.T) {
	rootfs := requireScenario(t, "relay")
	sc := bringUp(t, rootfs, repoFile(t, "examples/relay-topology/topology.json"))
	onFailDump(t, sc)

	sc.requireHandshakes(t)
	sc.requireRouteConvergence(t, allPairs)
	sc.requireOverlayPing(t, allPairs)
	sc.requireSNATRewrite(t)
}

// TestNatHub (scenario key "nat-hub") exercises router hub-and-spoke forwarding: two NAT clients with
// no direct edge reach each other through the router hub (NOT a relay — its NAT clients correctly
// have empty reverse Endpoints, and per-peer AllowedIPs=0.0.0.0/0 + the hub's IP forwarding carry the
// spoke-to-spoke path). All-pairs proves the transit; this complements relay by driving the router
// role's forwarding/announce derivation on the kernel.
func TestNatHub(t *testing.T) {
	rootfs := requireScenario(t, "nat-hub")
	sc := bringUp(t, rootfs, repoFile(t, "examples/nat-hub/topology.json"))
	onFailDump(t, sc)

	sc.requireHandshakes(t)
	sc.requireRouteConvergence(t, allPairs)
	sc.requireOverlayPing(t, allPairs)
	sc.requireSNATRewrite(t)
}
