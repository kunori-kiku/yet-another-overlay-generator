package compiler

import (
	"testing"

	"github.com/kunorikiku/yet-another-overlay-generator/internal/model"
)

// publicRouterNode builds a publicly reachable router node (with one public endpoint).
// HasPublicIP is set directly so DerivePeers' reverse-fallback logic kicks in (DerivePeers
// does not infer capabilities on its own).
func publicRouterNode(id, name, host string) model.Node {
	return model.Node{
		ID:       id,
		Name:     name,
		Hostname: host,
		Role:     "router",
		DomainID: "domain-1",
		Capabilities: model.NodeCapabilities{
			CanAcceptInbound: true,
			CanForward:       true,
			HasPublicIP:      true,
		},
		PublicEndpoints: []model.PublicEndpoint{
			// Port is a node-reachability hint, not a link listen port; reverse fallback must never use it.
			{ID: id + "-ep", Host: host, Port: 51820},
		},
	}
}

// findPeer finds the PeerInfo pointing at remoteID within peers.
func findPeer(peers []PeerInfo, remoteID string) *PeerInfo {
	for i := range peers {
		if peers[i].NodeID == remoteID {
			return &peers[i]
		}
	}
	return nil
}

// findEdge finds an Edge by id within edges.
func findEdge(edges []model.Edge, id string) *model.Edge {
	for i := range edges {
		if edges[i].ID == id {
			return &edges[i]
		}
	}
	return nil
}

// TestEndpointResolution_Forward covers the forward endpoint-resolution matrix (Spec A).
// Table-driven: each case describes one from->to edge and asserts the endpoint dialed from
// the from side.
func TestEndpointResolution_Forward(t *testing.T) {
	tests := []struct {
		name         string
		endpointHost string
		endpointPort int
		// wantEndpoint empty string means no Endpoint line should be generated
		wantEndpoint string
		// wantPort 0 means do not check the port (only checked when wantEndpoint is non-empty)
		wantPort int
	}{
		{
			// (a) endpoint_host only: the from side dials the remote's allocated listen port (51820)
			name:         "endpoint_host only dials allocated port",
			endpointHost: "b.example",
			endpointPort: 0,
			wantEndpoint: "b.example:51820",
			wantPort:     51820,
		},
		{
			// (b) explicit endpoint_port override: dialed verbatim
			name:         "explicit endpoint_port override dialed verbatim",
			endpointHost: "b.example",
			endpointPort: 51900,
			wantEndpoint: "b.example:51900",
			wantPort:     51900,
		},
		{
			// (d) endpoint_host empty: no Endpoint line generated
			name:         "empty endpoint_host produces no Endpoint",
			endpointHost: "",
			endpointPort: 0,
			wantEndpoint: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			topo := &model.Topology{
				Project: model.Project{ID: "ep-fwd", Name: "Endpoint Forward"},
				Domains: []model.Domain{{
					ID: "domain-1", Name: "fwd-net", CIDR: "10.40.0.0/24",
					AllocationMode: "auto", RoutingMode: "babel",
				}},
				Nodes: []model.Node{
					publicRouterNode("node-a", "alpha", "a.example"),
					publicRouterNode("node-b", "beta", "b.example"),
				},
				Edges: []model.Edge{
					{ID: "e1", FromNodeID: "node-a", ToNodeID: "node-b", Type: "public-endpoint",
						EndpointHost: tt.endpointHost, EndpointPort: tt.endpointPort, Transport: "udp", IsEnabled: true},
				},
			}
			topo.Nodes[0].OverlayIP = "10.40.0.1"
			topo.Nodes[1].OverlayIP = "10.40.0.2"

			peerMap, _, err := DerivePeers(topo, testKeys2())
			if err != nil {
				t.Fatalf("DerivePeers failed: %v", err)
			}

			fwd := findPeer(peerMap["node-a"], "node-b")
			if fwd == nil {
				t.Fatalf("node-a should have a peer pointing at node-b")
			}
			if fwd.Endpoint != tt.wantEndpoint {
				t.Errorf("forward endpoint = %q, want %q", fwd.Endpoint, tt.wantEndpoint)
			}
			if tt.wantEndpoint != "" && tt.wantPort != 0 {
				if got := extractPortFromEndpoint(fwd.Endpoint); got != tt.wantPort {
					t.Errorf("forward dialed port = %d, want %d", got, tt.wantPort)
				}
			}
		})
	}
}

// TestEndpointResolution_ReverseFallback covers the reverse-peer endpoint fallback matrix
// (Spec A).
// (a) a single A->B edge + both ends publicly reachable: B's reverse peer should fall back
// to dialing A's public host + A's allocated port.
// (e) an explicit reverse edge (carrying its own endpoint_host) wins over the fallback.
func TestEndpointResolution_ReverseFallback(t *testing.T) {
	tests := []struct {
		name string
		// reverseEdge true adds an extra explicit B->A reverse edge
		reverseEdge     bool
		reverseHost     string
		fromHasPublicIP bool
		// wantReverseEndpoint is the expected endpoint when B dials A in reverse
		wantReverseEndpoint string
	}{
		{
			// (a) no reverse edge, A publicly reachable -> fall back to dialing A's public host + A's port (51820), not the public endpoint's Port
			name:                "fallback dials from-node public host at allocated port",
			reverseEdge:         false,
			fromHasPublicIP:     true,
			wantReverseEndpoint: "a.example:51820",
		},
		{
			// (e) explicit reverse edge carries its own host, wins over fallback; A still declares a public endpoint to prove explicit takes priority
			name:                "explicit reverse edge wins over fallback",
			reverseEdge:         true,
			reverseHost:         "a-nat.example",
			fromHasPublicIP:     true,
			wantReverseEndpoint: "a-nat.example:51820",
		},
		{
			// no reverse edge and A not publicly reachable -> reverse peer has no endpoint
			name:                "no reverse edge and no public IP produces no Endpoint",
			reverseEdge:         false,
			fromHasPublicIP:     false,
			wantReverseEndpoint: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			nodeA := publicRouterNode("node-a", "alpha", "a.example")
			if !tt.fromHasPublicIP {
				nodeA.Capabilities.HasPublicIP = false
				nodeA.PublicEndpoints = nil
			}
			nodeB := publicRouterNode("node-b", "beta", "b.example")

			edges := []model.Edge{
				{ID: "e1", FromNodeID: "node-a", ToNodeID: "node-b", Type: "public-endpoint",
					EndpointHost: "b.example", EndpointPort: 0, Transport: "udp", IsEnabled: true},
			}
			if tt.reverseEdge {
				edges = append(edges, model.Edge{
					ID: "e2", FromNodeID: "node-b", ToNodeID: "node-a", Type: "public-endpoint",
					EndpointHost: tt.reverseHost, EndpointPort: 0, Transport: "udp", IsEnabled: true,
				})
			}

			topo := &model.Topology{
				Project: model.Project{ID: "ep-rev", Name: "Endpoint Reverse"},
				Domains: []model.Domain{{
					ID: "domain-1", Name: "rev-net", CIDR: "10.41.0.0/24",
					AllocationMode: "auto", RoutingMode: "babel",
				}},
				Nodes: []model.Node{nodeA, nodeB},
				Edges: edges,
			}
			topo.Nodes[0].OverlayIP = "10.41.0.1"
			topo.Nodes[1].OverlayIP = "10.41.0.2"

			peerMap, _, err := DerivePeers(topo, testKeys2())
			if err != nil {
				t.Fatalf("DerivePeers failed: %v", err)
			}

			// B's reverse peer dialing A: in peerMap["node-b"] where NodeID == node-a
			rev := findPeer(peerMap["node-b"], "node-a")
			if rev == nil {
				t.Fatalf("node-b should have a reverse peer pointing at node-a")
			}
			if rev.Endpoint != tt.wantReverseEndpoint {
				t.Errorf("reverse endpoint = %q, want %q", rev.Endpoint, tt.wantReverseEndpoint)
			}

			// Key invariant: the fallback must never use public_endpoints[0].Port (which is also 51820 here, but should come from A's allocated port).
			// Verify indirectly via forward-port symmetry: the port at which A is dialed in reverse == A's own interface ListenPort.
			if tt.wantReverseEndpoint != "" {
				aPeer := findPeer(peerMap["node-a"], "node-b")
				if aPeer == nil {
					t.Fatalf("node-a should have a peer pointing at node-b")
				}
				dialedPort := extractPortFromEndpoint(rev.Endpoint)
				if dialedPort != aPeer.ListenPort {
					t.Errorf("port for reverse-dialing A = %d, should equal A interface's ListenPort = %d", dialedPort, aPeer.ListenPort)
				}
			}
		})
	}
}

// TestEndpointResolution_SymmetricSingleEdge verifies the end-to-end symmetry of (a):
// a single A->B edge yields a bidirectionally dialable tunnel — A dials B's allocated port,
// B falls back to dialing A's allocated port.
func TestEndpointResolution_SymmetricSingleEdge(t *testing.T) {
	topo := &model.Topology{
		Project: model.Project{ID: "ep-sym", Name: "Endpoint Symmetric"},
		Domains: []model.Domain{{
			ID: "domain-1", Name: "sym-net", CIDR: "10.42.0.0/24",
			AllocationMode: "auto", RoutingMode: "babel",
		}},
		Nodes: []model.Node{
			publicRouterNode("node-a", "alpha", "a.example"),
			publicRouterNode("node-b", "beta", "b.example"),
		},
		Edges: []model.Edge{
			{ID: "e1", FromNodeID: "node-a", ToNodeID: "node-b", Type: "public-endpoint",
				EndpointHost: "b.example", EndpointPort: 0, Transport: "udp", IsEnabled: true},
		},
	}
	topo.Nodes[0].OverlayIP = "10.42.0.1"
	topo.Nodes[1].OverlayIP = "10.42.0.2"

	peerMap, _, err := DerivePeers(topo, testKeys2())
	if err != nil {
		t.Fatalf("DerivePeers failed: %v", err)
	}

	aToB := findPeer(peerMap["node-a"], "node-b")
	bToA := findPeer(peerMap["node-b"], "node-a")
	if aToB == nil || bToA == nil {
		t.Fatalf("should have bidirectional peers: aToB=%v bToA=%v", aToB, bToA)
	}

	// A->B endpoint port == B interface's ListenPort
	if got := extractPortFromEndpoint(aToB.Endpoint); got != bToA.ListenPort {
		t.Errorf("port for A dialing B = %d, should equal B interface's ListenPort = %d", got, bToA.ListenPort)
	}
	// B->A fallback endpoint port == A interface's ListenPort
	if got := extractPortFromEndpoint(bToA.Endpoint); got != aToB.ListenPort {
		t.Errorf("port for B dialing A = %d, should equal A interface's ListenPort = %d", got, aToB.ListenPort)
	}
	// Both directions should dial their respective public host
	if aToB.Endpoint != "b.example:"+itoa(bToA.ListenPort) {
		t.Errorf("A->B endpoint = %q, want b.example:%d", aToB.Endpoint, bToA.ListenPort)
	}
	if bToA.Endpoint != "a.example:"+itoa(aToB.ListenPort) {
		t.Errorf("B->A endpoint = %q, want a.example:%d", bToA.Endpoint, aToB.ListenPort)
	}
}

// TestEndpointResolution_HubDistinctPorts covers (c): three edges converge on the same hub,
// each dialing a distinct allocated port on the hub (base, base+1, base+2).
func TestEndpointResolution_HubDistinctPorts(t *testing.T) {
	topo := &model.Topology{
		Project: model.Project{ID: "ep-hub", Name: "Endpoint Hub"},
		Domains: []model.Domain{{
			ID: "domain-1", Name: "hub-net", CIDR: "10.43.0.0/24",
			AllocationMode: "auto", RoutingMode: "babel",
		}},
		Nodes: []model.Node{
			publicRouterNode("node-h", "hub", "h.example"),
			publicRouterNode("node-a", "alpha", "a.example"),
			publicRouterNode("node-b", "beta", "b.example"),
			publicRouterNode("node-c", "gamma", "c.example"),
		},
		// Processing order determines hub-side port allocation: A link 51820, B link 51821, C link 51822
		Edges: []model.Edge{
			{ID: "e1", FromNodeID: "node-a", ToNodeID: "node-h", Type: "public-endpoint",
				EndpointHost: "h.example", EndpointPort: 0, Transport: "udp", IsEnabled: true},
			{ID: "e2", FromNodeID: "node-b", ToNodeID: "node-h", Type: "public-endpoint",
				EndpointHost: "h.example", EndpointPort: 0, Transport: "udp", IsEnabled: true},
			{ID: "e3", FromNodeID: "node-c", ToNodeID: "node-h", Type: "public-endpoint",
				EndpointHost: "h.example", EndpointPort: 0, Transport: "udp", IsEnabled: true},
		},
	}
	topo.Nodes[0].OverlayIP = "10.43.0.1"
	topo.Nodes[1].OverlayIP = "10.43.0.2"
	topo.Nodes[2].OverlayIP = "10.43.0.3"
	topo.Nodes[3].OverlayIP = "10.43.0.4"

	peerMap, _, err := DerivePeers(topo, testKeys4())
	if err != nil {
		t.Fatalf("DerivePeers failed: %v", err)
	}

	wantPorts := map[string]int{
		"node-a": 51820,
		"node-b": 51821,
		"node-c": 51822,
	}

	seen := make(map[int]bool)
	for spoke, want := range wantPorts {
		p := findPeer(peerMap[spoke], "node-h")
		if p == nil {
			t.Fatalf("%s should have a peer pointing at the hub", spoke)
		}
		got := extractPortFromEndpoint(p.Endpoint)
		if got != want {
			t.Errorf("port for %s dialing hub = %d, want %d", spoke, got, want)
		}
		if p.Endpoint != "h.example:"+itoa(want) {
			t.Errorf("%s endpoint = %q, want h.example:%d", spoke, p.Endpoint, want)
		}
		if seen[got] {
			t.Errorf("hub-side port %d reused by multiple links, should all be distinct", got)
		}
		seen[got] = true
	}

	// The hub should have 3 distinct peer interfaces, each with a different port
	hubPeers := peerMap["node-h"]
	if len(hubPeers) != 3 {
		t.Fatalf("hub should have 3 peer interfaces, got %d", len(hubPeers))
	}
	hubPorts := make(map[int]bool)
	for _, hp := range hubPeers {
		if hubPorts[hp.ListenPort] {
			t.Errorf("hub interface ListenPort %d duplicated", hp.ListenPort)
		}
		hubPorts[hp.ListenPort] = true
	}
}

// TestCompiledPort_OverrideAware covers CompiledPort write-back for (b) and (d) (D51):
// (b) explicit endpoint_port=51900 -> CompiledPort equals 51900 and equals the port in the
// rendered endpoint;
// (d) endpoint_host empty -> CompiledPort is not written back (stays 0).
func TestCompiledPort_OverrideAware(t *testing.T) {
	t.Run("override reflected in CompiledPort and Endpoint", func(t *testing.T) {
		topo := &model.Topology{
			Project: model.Project{ID: "cp-ovr", Name: "CompiledPort Override"},
			Domains: []model.Domain{{
				ID: "domain-1", Name: "ovr-net", CIDR: "10.44.0.0/24",
				AllocationMode: "auto", RoutingMode: "babel",
			}},
			Nodes: []model.Node{
				publicRouterNode("node-a", "alpha", "a.example"),
				publicRouterNode("node-b", "beta", "b.example"),
			},
			Edges: []model.Edge{
				{ID: "e1", FromNodeID: "node-a", ToNodeID: "node-b", Type: "public-endpoint",
					EndpointHost: "b.example", EndpointPort: 51900, Transport: "udp", IsEnabled: true},
			},
		}

		c := NewCompiler()
		result, err := c.Compile(topo, testKeys2())
		if err != nil {
			t.Fatalf("Compile failed: %v", err)
		}

		edge := findEdge(result.Topology.Edges, "e1")
		if edge == nil {
			t.Fatalf("result should contain edge e1")
		}
		if edge.CompiledPort != 51900 {
			t.Errorf("CompiledPort = %d, want 51900 (override value)", edge.CompiledPort)
		}

		// CompiledPort must equal the port carried in the rendered endpoint
		fwd := findPeer(result.PeerMap["node-a"], "node-b")
		if fwd == nil {
			t.Fatalf("node-a should have a peer pointing at node-b")
		}
		if got := extractPortFromEndpoint(fwd.Endpoint); got != edge.CompiledPort {
			t.Errorf("rendered endpoint port = %d, CompiledPort = %d, the two must match", got, edge.CompiledPort)
		}
	})

	t.Run("empty endpoint_host leaves no CompiledPort", func(t *testing.T) {
		topo := &model.Topology{
			Project: model.Project{ID: "cp-empty", Name: "CompiledPort Empty"},
			Domains: []model.Domain{{
				ID: "domain-1", Name: "empty-net", CIDR: "10.45.0.0/24",
				AllocationMode: "auto", RoutingMode: "babel",
			}},
			Nodes: []model.Node{
				publicRouterNode("node-a", "alpha", "a.example"),
				publicRouterNode("node-b", "beta", "b.example"),
			},
			Edges: []model.Edge{
				{ID: "e1", FromNodeID: "node-a", ToNodeID: "node-b", Type: "public-endpoint",
					EndpointHost: "", EndpointPort: 0, Transport: "udp", IsEnabled: true},
			},
		}

		c := NewCompiler()
		result, err := c.Compile(topo, testKeys2())
		if err != nil {
			t.Fatalf("Compile failed: %v", err)
		}

		edge := findEdge(result.Topology.Edges, "e1")
		if edge == nil {
			t.Fatalf("result should contain edge e1")
		}
		if edge.CompiledPort != 0 {
			t.Errorf("CompiledPort should not be written back when endpoint_host is empty, got %d", edge.CompiledPort)
		}

		// The forward peer should likewise have no Endpoint line
		fwd := findPeer(result.PeerMap["node-a"], "node-b")
		if fwd == nil {
			t.Fatalf("node-a should have a peer pointing at node-b")
		}
		if fwd.Endpoint != "" {
			t.Errorf("no Endpoint should be generated when endpoint_host is empty, got %q", fwd.Endpoint)
		}
	})
}

// testKeys2 provides the keys needed for the two-node tests.
func testKeys2() map[string]KeyPair {
	return map[string]KeyPair{
		"node-a": {PrivateKey: "privkey-a-fake", PublicKey: "pubkey-a-fake"},
		"node-b": {PrivateKey: "privkey-b-fake", PublicKey: "pubkey-b-fake"},
	}
}

// testKeys4 provides the keys needed for the hub + three-spoke tests.
func testKeys4() map[string]KeyPair {
	return map[string]KeyPair{
		"node-h": {PrivateKey: "privkey-h-fake", PublicKey: "pubkey-h-fake"},
		"node-a": {PrivateKey: "privkey-a-fake", PublicKey: "pubkey-a-fake"},
		"node-b": {PrivateKey: "privkey-b-fake", PublicKey: "pubkey-b-fake"},
		"node-c": {PrivateKey: "privkey-c-fake", PublicKey: "pubkey-c-fake"},
	}
}
