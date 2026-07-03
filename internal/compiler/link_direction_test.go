package compiler

import (
	"context"
	"testing"

	"github.com/kunorikiku/yet-another-overlay-generator/internal/model"
)

// linkDirectionTopology builds the canonical race topology: two PUBLIC routers (both dialable, so
// without a direction the auto-reverse fallback fires) joined by one A->B edge carrying an
// explicit endpoint_host ("the accelerator") and the given link_direction.
func linkDirectionTopology(direction string) *model.Topology {
	topo := &model.Topology{
		Project: model.Project{ID: "link-dir", Name: "Link Direction"},
		Domains: []model.Domain{{
			ID: "domain-1", Name: "dir-net", CIDR: "10.47.0.0/24",
			AllocationMode: "auto", RoutingMode: "babel",
		}},
		Nodes: []model.Node{
			publicRouterNode("node-a", "alpha", "a.example"),
			publicRouterNode("node-b", "beta", "b.example"),
		},
		Edges: []model.Edge{
			{ID: "e1", FromNodeID: "node-a", ToNodeID: "node-b", Type: "public-endpoint",
				EndpointHost: "accel.example", EndpointPort: 0, Transport: "udp",
				LinkDirection: direction, IsEnabled: true},
		},
	}
	topo.Nodes[0].OverlayIP = "10.47.0.1"
	topo.Nodes[1].OverlayIP = "10.47.0.2"
	return topo
}

// TestLinkDirection_ForwardSuppressesReverseEndpoint pins the feature's core contract: with
// link_direction=forward, the reverse peer keeps its full stanza (AllowedIPs, transit, listen
// port) but carries NO Endpoint — even though the from-node's public endpoint would otherwise
// make the fallback fire (the exact reverse-peer race that bypasses a relay/accelerator path).
// "both" and the "" default stay byte-identical to today; an unrecognized value floors to both
// (defensive — the validator rejects it, the compiler never re-errors).
func TestLinkDirection_ForwardSuppressesReverseEndpoint(t *testing.T) {
	tests := []struct {
		name      string
		direction string
		// wantReverseEndpoint "" means the reverse peer must carry no Endpoint.
		wantReverseEndpoint string
	}{
		{name: "forward suppresses the public-endpoint fallback", direction: model.EdgeLinkDirectionForward, wantReverseEndpoint: ""},
		{name: "explicit both keeps the fallback", direction: model.EdgeLinkDirectionBoth, wantReverseEndpoint: "a.example:51820"},
		{name: "empty default keeps the fallback", direction: "", wantReverseEndpoint: "a.example:51820"},
		{name: "unrecognized value floors to both", direction: "one-way", wantReverseEndpoint: "a.example:51820"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			peerMap, _, err := DerivePeers(linkDirectionTopology(tt.direction), testKeys2())
			if err != nil {
				t.Fatalf("DerivePeers failed: %v", err)
			}

			// The forward dial is never affected by the direction.
			fwd := findPeer(peerMap["node-a"], "node-b")
			if fwd == nil {
				t.Fatalf("node-a should have a peer pointing at node-b")
			}
			if fwd.Endpoint != "accel.example:51820" {
				t.Errorf("forward endpoint = %q, want accel.example:51820", fwd.Endpoint)
			}

			// The reverse peer must exist in full either way — only its Endpoint is gated.
			rev := findPeer(peerMap["node-b"], "node-a")
			if rev == nil {
				t.Fatalf("node-b should have a reverse peer pointing at node-a (the stanza itself is never suppressed)")
			}
			if rev.Endpoint != tt.wantReverseEndpoint {
				t.Errorf("reverse endpoint = %q, want %q", rev.Endpoint, tt.wantReverseEndpoint)
			}
			if len(rev.AllowedIPs) == 0 || rev.ListenPort == 0 || rev.LocalTransitIP == "" || rev.LocalLinkLocal == "" {
				t.Errorf("reverse peer must keep its full stanza (AllowedIPs/ListenPort/transit/link-local), got %+v", rev)
			}
		})
	}
}

// TestLinkDirection_ForwardSuppressesExplicitReverseEdge is belt-and-braces for the compiler's
// determinism: an explicit reverse edge on a direction-bearing pair is a validator ERROR
// (CodeEdgeLinkDirectionConflict), but DerivePeers is validator-independent and must still
// suppress BOTH reverse-endpoint branches (the explicit-reverse-edge branch, not just the
// public-endpoint fallback) rather than half-apply the direction.
func TestLinkDirection_ForwardSuppressesExplicitReverseEdge(t *testing.T) {
	topo := linkDirectionTopology(model.EdgeLinkDirectionForward)
	topo.Edges = append(topo.Edges, model.Edge{
		ID: "e2", FromNodeID: "node-b", ToNodeID: "node-a", Type: "public-endpoint",
		EndpointHost: "a-nat.example", EndpointPort: 0, Transport: "udp", IsEnabled: true,
	})

	peerMap, _, err := DerivePeers(topo, testKeys2())
	if err != nil {
		t.Fatalf("DerivePeers failed: %v", err)
	}

	rev := findPeer(peerMap["node-b"], "node-a")
	if rev == nil {
		t.Fatalf("node-b should have a reverse peer pointing at node-a")
	}
	if rev.Endpoint != "" {
		t.Errorf("reverse endpoint = %q, want empty: forward must suppress the explicit-reverse-edge branch too", rev.Endpoint)
	}
}

// TestLinkDirection_AllocationInvariance pins the HIGH invariant that link_direction is
// allocation-blind: toggling forward on and off moves ZERO allocated values — every pin,
// CompiledPort, listen port, transit IP, and link-local is byte-identical; only the reverse
// peer's Endpoint differs. Runs the full Compile path so the CompiledPort write-back and the
// pin write-back are both covered.
func TestLinkDirection_AllocationInvariance(t *testing.T) {
	compileOf := func(direction string) *CompileResult {
		t.Helper()
		c := NewCompiler()
		result, err := c.Compile(context.Background(), linkDirectionTopology(direction), testKeys2())
		if err != nil {
			t.Fatalf("Compile(direction=%q) failed: %v", direction, err)
		}
		return result
	}

	base := compileOf("")
	directed := compileOf(model.EdgeLinkDirectionForward)

	// Edge write-back: all six pins + CompiledPort identical.
	be := findEdge(base.Topology.Edges, "e1")
	de := findEdge(directed.Topology.Edges, "e1")
	if be == nil || de == nil {
		t.Fatalf("both compiles should contain edge e1")
	}
	if be.CompiledPort != de.CompiledPort ||
		be.PinnedFromPort != de.PinnedFromPort || be.PinnedToPort != de.PinnedToPort ||
		be.PinnedFromTransitIP != de.PinnedFromTransitIP || be.PinnedToTransitIP != de.PinnedToTransitIP ||
		be.PinnedFromLinkLocal != de.PinnedFromLinkLocal || be.PinnedToLinkLocal != de.PinnedToLinkLocal {
		t.Errorf("allocation write-back must be direction-invariant:\n base=%+v\n directed=%+v", be, de)
	}

	// Peer resources: identical on every field except the reverse Endpoint.
	for _, nodeID := range []string{"node-a", "node-b"} {
		bp := base.PeerMap[nodeID]
		dp := directed.PeerMap[nodeID]
		if len(bp) != len(dp) {
			t.Fatalf("%s: peer count changed %d -> %d", nodeID, len(bp), len(dp))
		}
		for i := range bp {
			b, d := bp[i], dp[i]
			if b.ListenPort != d.ListenPort || b.LocalTransitIP != d.LocalTransitIP ||
				b.RemoteTransitIP != d.RemoteTransitIP || b.LocalLinkLocal != d.LocalLinkLocal ||
				b.RemoteLinkLocal != d.RemoteLinkLocal || b.PersistentKeepalive != d.PersistentKeepalive ||
				b.InterfaceName != d.InterfaceName {
				t.Errorf("%s peer[%d]: non-Endpoint field changed:\n base=%+v\n directed=%+v", nodeID, i, b, d)
			}
		}
	}

	// The ONLY difference: the reverse peer's Endpoint.
	baseRev := findPeer(base.PeerMap["node-b"], "node-a")
	dirRev := findPeer(directed.PeerMap["node-b"], "node-a")
	if baseRev == nil || dirRev == nil {
		t.Fatalf("both compiles should carry the reverse peer")
	}
	if baseRev.Endpoint == "" || dirRev.Endpoint != "" {
		t.Errorf("expected exactly the reverse Endpoint to differ: base=%q directed=%q", baseRev.Endpoint, dirRev.Endpoint)
	}
}
