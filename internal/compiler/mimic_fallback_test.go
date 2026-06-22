package compiler

import (
	"context"
	"testing"
	"time"

	"github.com/kunorikiku/yet-another-overlay-generator/internal/model"
)

// TestResolveMimicFallback is the policy-resolution CONTRACT table (perpetual): the fail-closed
// flooring + inherit semantics that plan-5's mechanism and the TS parity both depend on.
func TestResolveMimicFallback(t *testing.T) {
	cases := []struct {
		edge, def, want string
	}{
		// explicit edge choice wins regardless of the default
		{"udp", "", "udp"}, {"udp", "none", "udp"}, {"udp", "udp", "udp"},
		{"none", "", "none"}, {"none", "udp", "none"}, {"none", "none", "none"},
		// edge inherits: take the default, flooring anything non-"udp" to "none"
		{"", "", "none"}, {"", "none", "none"}, {"", "udp", "udp"},
		// defensive: an unrecognized EDGE is treated as inherit (so it follows the default), and an
		// unrecognized DEFAULT floors to "none". (The schema validator rejects a non-enum edge value
		// before the compiler ever sees it; this is belt-and-suspenders.)
		{"garbage", "", "none"}, {"", "garbage", "none"}, {"garbage", "udp", "udp"},
	}
	for _, tc := range cases {
		if got := resolveMimicFallback(tc.edge, tc.def); got != tc.want {
			t.Errorf("resolveMimicFallback(%q,%q) = %q, want %q", tc.edge, tc.def, got, tc.want)
		}
	}
}

// TestPeerInfoCarriesResolvedFallback proves the compiler stamps the RESOLVED policy at every
// PeerInfo build site: an explicit edge policy wins, and an empty edge inherits the fleet default
// threaded through the compiler.
func TestPeerInfoCarriesResolvedFallback(t *testing.T) {
	mustTCP := func(edgePolicy string) *model.Topology {
		topo := mimicTwoRouterTopo("tcp", 0, 0)
		topo.Nodes[0].OverlayIP = "10.50.0.1"
		topo.Nodes[1].OverlayIP = "10.50.0.2"
		topo.Edges[0].MimicFallback = edgePolicy
		return topo
	}

	// Explicit edge policy "udp" via the public DerivePeers shim (default ""→"none"): edge wins.
	peerMap, _, err := DerivePeers(mustTCP("udp"), testKeys2())
	if err != nil {
		t.Fatalf("DerivePeers(edge=udp): %v", err)
	}
	assertAllMimicFallback(t, peerMap, "udp")

	// Empty edge + the public shim (no fleet default) ⇒ resolves to "none".
	peerMap, _, err = DerivePeers(mustTCP(""), testKeys2())
	if err != nil {
		t.Fatalf("DerivePeers(edge=empty): %v", err)
	}
	assertAllMimicFallback(t, peerMap, "none")

	// Empty edge + a fleet default of "udp" threaded through the compiler ⇒ resolves to "udp"
	// (proves the WithMimicFallbackDefault → derivePeers thread, not just the resolver).
	res, err := NewCompiler().WithMimicFallbackDefault("udp").
		CompileAt(context.Background(), mustTCP(""), testKeys2(), time.Unix(0, 0).UTC())
	if err != nil {
		t.Fatalf("CompileAt(default=udp): %v", err)
	}
	assertAllMimicFallback(t, res.PeerMap, "udp")
}

// assertAllMimicFallback asserts every derived PeerInfo carries the expected resolved policy
// (never "").
func assertAllMimicFallback(t *testing.T, peerMap map[string][]PeerInfo, want string) {
	t.Helper()
	n := 0
	for node, peers := range peerMap {
		for _, p := range peers {
			n++
			if p.MimicFallback != want {
				t.Fatalf("node %s peer %s: MimicFallback = %q, want %q", node, p.InterfaceName, p.MimicFallback, want)
			}
		}
	}
	if n == 0 {
		t.Fatalf("no peers derived; fixture produced nothing to assert")
	}
}
