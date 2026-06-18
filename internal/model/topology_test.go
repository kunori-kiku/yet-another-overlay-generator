package model

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// LoadTopologyFromFile reads the file at path and unmarshals it into a Topology (test helper).
func LoadTopologyFromFile(path string) (*Topology, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var topo Topology
	if err := json.Unmarshal(data, &topo); err != nil {
		return nil, err
	}
	return &topo, nil
}

func examplesDir() string {
	// From internal/model/ up to the repo root, then into examples/.
	return filepath.Join("..", "..", "examples")
}

func TestLoadSimpleMesh(t *testing.T) {
	topo, err := LoadTopologyFromFile(filepath.Join(examplesDir(), "simple-mesh", "topology.json"))
	if err != nil {
		t.Fatalf("failed to load simple-mesh: %v", err)
	}

	// Project fields.
	if topo.Project.ID != "simple-mesh-001" {
		t.Errorf("Project ID should be simple-mesh-001, got %s", topo.Project.ID)
	}
	if topo.Project.Name != "Simple Mesh" {
		t.Errorf("Project Name should be Simple Mesh, got %s", topo.Project.Name)
	}

	// Domain fields.
	if len(topo.Domains) != 1 {
		t.Fatalf("expected 1 Domain, got %d", len(topo.Domains))
	}
	if topo.Domains[0].CIDR != "10.11.0.0/24" {
		t.Errorf("Domain CIDR should be 10.11.0.0/24, got %s", topo.Domains[0].CIDR)
	}
	if topo.Domains[0].RoutingMode != "babel" {
		t.Errorf("Domain RoutingMode should be babel, got %s", topo.Domains[0].RoutingMode)
	}

	// Nodes.
	if len(topo.Nodes) != 3 {
		t.Fatalf("expected 3 nodes, got %d", len(topo.Nodes))
	}
	for _, node := range topo.Nodes {
		if node.Role != "router" {
			t.Errorf("node %s role should be router, got %s", node.Name, node.Role)
		}
		if !node.Capabilities.HasPublicIP {
			t.Errorf("node %s should have a public IP", node.Name)
		}
	}

	// Edges (full mesh of 3 nodes = 6 directed edges).
	if len(topo.Edges) != 6 {
		t.Fatalf("expected 6 edges, got %d", len(topo.Edges))
	}
}

func TestLoadNatHub(t *testing.T) {
	topo, err := LoadTopologyFromFile(filepath.Join(examplesDir(), "nat-hub", "topology.json"))
	if err != nil {
		t.Fatalf("failed to load nat-hub: %v", err)
	}

	if topo.Project.ID != "nat-hub-001" {
		t.Errorf("Project ID should be nat-hub-001, got %s", topo.Project.ID)
	}

	// Nodes.
	if len(topo.Nodes) != 3 {
		t.Fatalf("expected 3 nodes, got %d", len(topo.Nodes))
	}

	hub := topo.Nodes[0]
	if hub.Role != "router" {
		t.Errorf("hub role should be router, got %s", hub.Role)
	}
	if !hub.Capabilities.CanRelay {
		t.Errorf("hub should have can_relay=true")
	}
	if !hub.Capabilities.HasPublicIP {
		t.Errorf("hub should have has_public_ip=true")
	}

	// The NAT-bound clients behind the hub.
	for _, client := range topo.Nodes[1:] {
		if client.Role != "peer" {
			t.Errorf("node %s role should be peer, got %s", client.Name, client.Role)
		}
		if client.Capabilities.HasPublicIP {
			t.Errorf("node %s should not have a public IP", client.Name)
		}
		if client.Capabilities.CanAcceptInbound {
			t.Errorf("node %s should not accept inbound connections", client.Name)
		}
	}

	// 2 edges (each client to the hub).
	if len(topo.Edges) != 2 {
		t.Fatalf("expected 2 edges, got %d", len(topo.Edges))
	}
}

func TestLoadRelayTopology(t *testing.T) {
	topo, err := LoadTopologyFromFile(filepath.Join(examplesDir(), "relay-topology", "topology.json"))
	if err != nil {
		t.Fatalf("failed to load relay-topology: %v", err)
	}

	if topo.Project.ID != "relay-topo-001" {
		t.Errorf("Project ID should be relay-topo-001, got %s", topo.Project.ID)
	}

	// The relay node.
	relay := topo.Nodes[0]
	if relay.Role != "relay" {
		t.Errorf("role should be relay, got %s", relay.Role)
	}
	if !relay.Capabilities.CanRelay {
		t.Errorf("relay should have can_relay=true")
	}

	// peer-2 has a manually assigned IP.
	peer2 := topo.Nodes[2]
	if peer2.OverlayIP != "10.30.0.100" {
		t.Errorf("peer-2 IP should be 10.30.0.100, got %s", peer2.OverlayIP)
	}

	// peer-1 has no IP (auto-allocated later).
	peer1 := topo.Nodes[1]
	if peer1.OverlayIP != "" {
		t.Errorf("peer-1 should have no IP, got %s", peer1.OverlayIP)
	}

	// 2 edges.
	if len(topo.Edges) != 2 {
		t.Fatalf("expected 2 edges, got %d", len(topo.Edges))
	}
}

func TestTopologyDefaultValues(t *testing.T) {
	// Minimal JSON with only required fields.
	jsonData := `{
		"project": {"id": "test", "name": "Test"},
		"domains": [],
		"nodes": [],
		"edges": []
	}`

	var topo Topology
	if err := json.Unmarshal([]byte(jsonData), &topo); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}

	if topo.Project.Description != "" {
		t.Errorf("Description should be empty, got %s", topo.Project.Description)
	}
	if topo.Project.Version != "" {
		t.Errorf("Version should be empty, got %s", topo.Project.Version)
	}
	if topo.RoutePolicies != nil {
		t.Errorf("RoutePolicies should be nil, got %v", topo.RoutePolicies)
	}
}

func TestNodeSerialization(t *testing.T) {
	node := Node{
		ID:       "test-node",
		Name:     "test",
		Role:     "peer",
		DomainID: "domain-1",
		Capabilities: NodeCapabilities{
			CanAcceptInbound: false,
			CanForward:       false,
			CanRelay:         false,
			HasPublicIP:      false,
		},
	}

	data, err := json.Marshal(node)
	if err != nil {
		t.Fatalf("failed to marshal Node: %v", err)
	}

	var decoded Node
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("failed to unmarshal Node: %v", err)
	}

	if decoded.ID != node.ID {
		t.Errorf("ID mismatch: want %s, got %s", node.ID, decoded.ID)
	}
	if decoded.OverlayIP != "" {
		t.Errorf("OverlayIP should be empty, got %s", decoded.OverlayIP)
	}
}
