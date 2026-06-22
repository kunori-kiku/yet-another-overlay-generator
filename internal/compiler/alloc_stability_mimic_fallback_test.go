package compiler

// alloc_stability_mimic_fallback_test.go guards the HIGH allocation-stability principle for plan-4:
// the per-link mimic_fallback policy + the fleet default are PURE policy that feeds the renderer-input
// PeerInfo, NEVER the allocator. Setting them MUST NOT perturb any allocated value (ports / transit
// IPs / link-locals / keys). A regression here would silently renumber a live fleet.

import (
	"context"
	"strconv"
	"testing"
	"time"

	"github.com/kunorikiku/yet-another-overlay-generator/internal/model"
)

// allocFingerprint is the allocation-relevant subset of a PeerInfo (everything EXCEPT MimicFallback,
// which is the pure-policy field under test). Two compiles that differ only in fallback policy must
// produce identical fingerprints for every peer.
type allocFingerprint struct {
	iface, listenPort, localTransit, remoteTransit, localLL, remoteLL, pub string
}

func fingerprintPeers(peerMap map[string][]PeerInfo) map[string]allocFingerprint {
	out := make(map[string]allocFingerprint)
	for node, peers := range peerMap {
		for i, p := range peers {
			key := node + "#" + p.InterfaceName + "#" + strconv.Itoa(i)
			out[key] = allocFingerprint{
				iface:         p.InterfaceName,
				listenPort:    strconv.Itoa(p.ListenPort),
				localTransit:  p.LocalTransitIP,
				remoteTransit: p.RemoteTransitIP,
				localLL:       p.LocalLinkLocal,
				remoteLL:      p.RemoteLinkLocal,
				pub:           p.PublicKey,
			}
		}
	}
	return out
}

// TestMimicFallbackDoesNotPerturbAllocation compiles the SAME tcp topology three ways — no fallback,
// edge="udp", and edge="" with a fleet default of "udp" — and asserts every peer's allocation
// fingerprint (port / transit / link-local / key) is byte-identical across all three. Only the pure
// MimicFallback policy field may differ.
func TestMimicFallbackDoesNotPerturbAllocation(t *testing.T) {
	base := func() *model.Topology {
		topo := mimicTwoRouterTopo("tcp", 0, 0)
		topo.Nodes[0].OverlayIP = "10.50.0.1"
		topo.Nodes[1].OverlayIP = "10.50.0.2"
		return topo
	}

	noFallback, _, err := DerivePeers(base(), testKeys2())
	if err != nil {
		t.Fatalf("DerivePeers(no fallback): %v", err)
	}
	want := fingerprintPeers(noFallback)

	edgeUDP := base()
	edgeUDP.Edges[0].MimicFallback = "udp"
	pmEdge, _, err := DerivePeers(edgeUDP, testKeys2())
	if err != nil {
		t.Fatalf("DerivePeers(edge=udp): %v", err)
	}
	assertSameFingerprint(t, want, fingerprintPeers(pmEdge), "edge=udp")

	resDefault, err := NewCompiler().WithMimicFallbackDefault("udp").
		CompileAt(context.Background(), base(), testKeys2(), time.Unix(0, 0).UTC())
	if err != nil {
		t.Fatalf("CompileAt(default=udp): %v", err)
	}
	assertSameFingerprint(t, want, fingerprintPeers(resDefault.PeerMap), "default=udp")
}

func assertSameFingerprint(t *testing.T, want, got map[string]allocFingerprint, label string) {
	t.Helper()
	if len(want) != len(got) {
		t.Fatalf("%s: peer count changed: %d vs %d", label, len(want), len(got))
	}
	for k, w := range want {
		g, ok := got[k]
		if !ok {
			t.Fatalf("%s: peer %q vanished", label, k)
		}
		if g != w {
			t.Fatalf("%s: peer %q allocation perturbed:\n want %+v\n got  %+v", label, k, w, g)
		}
	}
}
