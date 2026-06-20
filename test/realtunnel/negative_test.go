//go:build linux && integration

package realtunnel

import (
	"fmt"
	"os"
	"testing"
	"time"
)

// negative_test.go — the red-proof (plan-18 Phase 9). A required gate that is never observed to FAIL
// could be vacuously green (asserting nothing). This test deliberately breaks a wire and confirms the
// SNAT assertion CATCHES it, so we know the floor has teeth. It is gated on REALTUNNEL_NEGATIVE so
// the normal all-green run never injects a fault; the realtunnel-bakein workflow runs it as the rc.1
// precondition (documented in RC1-GATE.md).
//
// It runs on the simple-mesh canary — the same topology the required gate uses. Transit IPs are
// allocated /32, so a transit-sourced overlay ping's reply is routable back ONLY via the SNAT
// rewrite; dropping the rule strands every such ping. (No multi-hop topology is needed: the rewrite
// is load-bearing on every topology, full mesh included.)

// TestNegativeProof brings up the canary, confirms a transit-sourced ping works (SNAT carrying it),
// drops the SNAT rule, and asserts the same ping now FAILS on every node — proving a broken wire is
// detected, not silently passed. GREEN when the fault is caught.
func TestNegativeProof(t *testing.T) {
	rootfs := requireCapabilities(t)
	fault := os.Getenv("REALTUNNEL_NEGATIVE")
	if fault == "" {
		t.Skip("realtunnel: REALTUNNEL_NEGATIVE unset — set it (e.g. =drop-snat) to run the red-proof")
	}

	sc := bringUp(t, rootfs, repoFile(t, "examples/simple-mesh/topology.json"))
	onFailDump(t, sc)

	// Pre-fault: every node's transit-sourced ping must WORK (poll through convergence) — so the
	// later failure is provably the fault, not a pre-existing break.
	for _, from := range sc.nodes {
		transit := sc.aTransitIP(t, from)
		if transit == "" {
			t.Fatalf("node %s: no transit IP found on a wg interface", from.name)
		}
		to := sc.otherNode(from)
		waitFor(t, 60*time.Second, fmt.Sprintf("pre-fault SNAT path %s(transit %s)->%s", from.name, transit, to.name), func() bool {
			ok, _ := sc.snatFunctionalOK(t, from, transit, to)
			return ok
		})
	}

	sc.applyFault(t, fault)

	// Post-fault: the same probe must now FAIL on every node — the rewrite is gone, so the target
	// replies to a /32 transit address it has no route to. If any node still passes, the SNAT
	// assertion would be vacuous. (Each ping is a fresh ICMP id, so no stale conntrack NAT binding
	// from the pre-fault flow survives to mask the dropped rule.)
	for _, from := range sc.nodes {
		transit := sc.aTransitIP(t, from)
		to := sc.otherNode(from)
		if ok, out := sc.snatFunctionalOK(t, from, transit, to); ok {
			t.Fatalf("negative proof FAILED: after fault %q, %s(transit %s)->%s(%s) STILL has 0%% loss — "+
				"the SNAT assertion is vacuous (the fault did not break the data plane):\n%s",
				fault, from.name, transit, to.name, to.overlayIP, out)
		}
	}
	t.Logf("negative proof OK: fault %q broke the transit->overlay SNAT path on all %d nodes; "+
		"the required SNAT assertion has teeth", fault, len(sc.nodes))
}
