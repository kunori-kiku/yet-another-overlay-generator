package compiler

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/kunorikiku/yet-another-overlay-generator/internal/model"
)

// shippedExampleTopologies lists the example topologies shipped with the product in the repo.
// Paths are relative to this package directory (internal/compiler).
var shippedExampleTopologies = []string{
	"../../examples/simple-mesh/topology.json",
	"../../examples/nat-hub/topology.json",
	"../../examples/relay-topology/topology.json",
}

// loadExampleTopology reads and deserializes one example topology file.
func loadExampleTopology(t *testing.T, path string) *model.Topology {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("cannot read example topology %s: %v", path, err)
	}
	var topo model.Topology
	if err := json.Unmarshal(data, &topo); err != nil {
		t.Fatalf("cannot parse example topology %s: %v", path, err)
	}
	return &topo
}

// exampleKeys generates a test key pair for each node in the topology, so the compiled
// artifacts are fully populated (consistent with testKeys' style, but generated dynamically by node ID).
func exampleKeys(topo *model.Topology) map[string]KeyPair {
	keys := make(map[string]KeyPair, len(topo.Nodes))
	for _, node := range topo.Nodes {
		keys[node.ID] = KeyPair{
			PrivateKey: "privkey-" + node.ID + "-fake",
			PublicKey:  "pubkey-" + node.ID + "-fake",
		}
	}
	return keys
}

// TestExampleTopologiesDialCorrectPorts is the gatekeeper test for "examples are always deployable".
//
// It runs the full compilation pipeline on each example topology shipped with the
// product, then verifies the core invariant of port attribution: for each PeerInfo in
// the resulting PeerMap with a non-empty Endpoint, the port dialed in its Endpoint must
// equal the interface listen port the remote node allocated for this link (i.e. the
// ListenPort of the PeerInfo in the remote PeerMap that points back at this node).
//
// The moment someone hardcodes endpoint_port on an example edge again (the former #1
// defect: treating a node's public_endpoints[0].port as the dial port for every link),
// the dialed port will diverge from the port the remote actually listens on, and this
// test will immediately fail.
func TestExampleTopologiesDialCorrectPorts(t *testing.T) {
	for _, relPath := range shippedExampleTopologies {
		relPath := relPath
		t.Run(filepath.Base(filepath.Dir(relPath)), func(t *testing.T) {
			topo := loadExampleTopology(t, relPath)
			keys := exampleKeys(topo)

			c := NewCompiler()
			result, err := c.Compile(context.Background(), topo, keys)
			if err != nil {
				t.Fatalf("example %s failed to compile: %v", relPath, err)
			}

			// For each peer with an endpoint on each node, verify the dialed port
			// equals the interface listen port the remote allocated for this node.
			for nodeID, peers := range result.PeerMap {
				for _, p := range peers {
					if p.Endpoint == "" {
						continue // skip passive peers without an endpoint
					}

					dialedPort := extractPortFromEndpoint(p.Endpoint)

					// Find the entry pointing back at this node in the remote node's peer list.
					remotePeers := result.PeerMap[p.NodeID]
					found := false
					for _, rp := range remotePeers {
						if rp.NodeID != nodeID {
							continue
						}
						found = true
						if dialedPort != rp.ListenPort {
							t.Errorf("example %s: %s->%s dialed port=%d (endpoint %q), "+
								"but the interface ListenPort %s allocated for %s=%d",
								relPath, nodeID, p.NodeID, dialedPort, p.Endpoint,
								p.NodeID, nodeID, rp.ListenPort)
						}
						break
					}
					if !found {
						t.Errorf("example %s: %s has a peer pointing at %s (with endpoint %q), "+
							"but %s has no reverse peer pointing back at %s",
							relPath, nodeID, p.NodeID, p.Endpoint, p.NodeID, nodeID)
					}
				}
			}
		})
	}
}
