package model

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// LoadTopologyFromFile （）
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
	//  internal/model/  examples/
	return filepath.Join("..", "..", "examples")
}

func TestLoadSimpleMesh(t *testing.T) {
	topo, err := LoadTopologyFromFile(filepath.Join(examplesDir(), "simple-mesh", "topology.json"))
	if err != nil {
		t.Fatalf(" simple-mesh : %v", err)
	}

	//  Project
	if topo.Project.ID != "simple-mesh-001" {
		t.Errorf("Project ID  simple-mesh-001,  %s", topo.Project.ID)
	}
	if topo.Project.Name != "Simple Mesh" {
		t.Errorf("Project Name  Simple Mesh,  %s", topo.Project.Name)
	}

	//  Domain
	if len(topo.Domains) != 1 {
		t.Fatalf(" 1  Domain,  %d", len(topo.Domains))
	}
	if topo.Domains[0].CIDR != "10.11.0.0/24" {
		t.Errorf("Domain CIDR  10.11.0.0/24,  %s", topo.Domains[0].CIDR)
	}
	if topo.Domains[0].RoutingMode != "babel" {
		t.Errorf("Domain RoutingMode  babel,  %s", topo.Domains[0].RoutingMode)
	}

	// 
	if len(topo.Nodes) != 3 {
		t.Fatalf(" 3 ,  %d", len(topo.Nodes))
	}
	for _, node := range topo.Nodes {
		if node.Role != "router" {
			t.Errorf(" %s  router,  %s", node.Name, node.Role)
		}
		if !node.Capabilities.HasPublicIP {
			t.Errorf(" %s  IP", node.Name)
		}
	}

	// （ = 6 ）
	if len(topo.Edges) != 6 {
		t.Fatalf(" 6 ,  %d", len(topo.Edges))
	}
}

func TestLoadNatHub(t *testing.T) {
	topo, err := LoadTopologyFromFile(filepath.Join(examplesDir(), "nat-hub", "topology.json"))
	if err != nil {
		t.Fatalf(" nat-hub : %v", err)
	}

	if topo.Project.ID != "nat-hub-001" {
		t.Errorf("Project ID  nat-hub-001,  %s", topo.Project.ID)
	}

	// 
	if len(topo.Nodes) != 3 {
		t.Fatalf(" 3 ,  %d", len(topo.Nodes))
	}

	hub := topo.Nodes[0]
	if hub.Role != "router" {
		t.Errorf("Hub  router,  %s", hub.Role)
	}
	if !hub.Capabilities.CanRelay {
		t.Errorf("Hub  can_relay=true")
	}
	if !hub.Capabilities.HasPublicIP {
		t.Errorf("Hub  has_public_ip=true")
	}

	// NAT 
	for _, client := range topo.Nodes[1:] {
		if client.Role != "peer" {
			t.Errorf(" %s  peer,  %s", client.Name, client.Role)
		}
		if client.Capabilities.HasPublicIP {
			t.Errorf(" %s  IP", client.Name)
		}
		if client.Capabilities.CanAcceptInbound {
			t.Errorf(" %s ", client.Name)
		}
	}

	//  2 （ hub）
	if len(topo.Edges) != 2 {
		t.Fatalf(" 2 ,  %d", len(topo.Edges))
	}
}

func TestLoadRelayTopology(t *testing.T) {
	topo, err := LoadTopologyFromFile(filepath.Join(examplesDir(), "relay-topology", "topology.json"))
	if err != nil {
		t.Fatalf(" relay-topology : %v", err)
	}

	if topo.Project.ID != "relay-topo-001" {
		t.Errorf("Project ID  relay-topo-001,  %s", topo.Project.ID)
	}

	// 
	relay := topo.Nodes[0]
	if relay.Role != "relay" {
		t.Errorf(" relay,  %s", relay.Role)
	}
	if !relay.Capabilities.CanRelay {
		t.Errorf(" can_relay=true")
	}

	//  IP 
	peer2 := topo.Nodes[2]
	if peer2.OverlayIP != "10.30.0.100" {
		t.Errorf("peer-2  IP  10.30.0.100,  %s", peer2.OverlayIP)
	}

	// peer-1  IP（）
	peer1 := topo.Nodes[1]
	if peer1.OverlayIP != "" {
		t.Errorf("peer-1  IP,  %s", peer1.OverlayIP)
	}

	// 2 
	if len(topo.Edges) != 2 {
		t.Fatalf(" 2 ,  %d", len(topo.Edges))
	}
}

func TestTopologyDefaultValues(t *testing.T) {
	//  JSON 
	jsonData := `{
		"project": {"id": "test", "name": "Test"},
		"domains": [],
		"nodes": [],
		"edges": []
	}`

	var topo Topology
	if err := json.Unmarshal([]byte(jsonData), &topo); err != nil {
		t.Fatalf(": %v", err)
	}

	if topo.Project.Description != "" {
		t.Errorf("Description ,  %s", topo.Project.Description)
	}
	if topo.Project.Version != "" {
		t.Errorf("Version ,  %s", topo.Project.Version)
	}
	if topo.RoutePolicies != nil {
		t.Errorf("RoutePolicies  nil,  %v", topo.RoutePolicies)
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
		t.Fatalf(" Node : %v", err)
	}

	var decoded Node
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf(" Node : %v", err)
	}

	if decoded.ID != node.ID {
		t.Errorf("ID :  %s,  %s", node.ID, decoded.ID)
	}
	if decoded.OverlayIP != "" {
		t.Errorf("OverlayIP ,  %s", decoded.OverlayIP)
	}
	if decoded.ListenPort != 0 {
		t.Errorf("ListenPort  0,  %d", decoded.ListenPort)
	}
}
