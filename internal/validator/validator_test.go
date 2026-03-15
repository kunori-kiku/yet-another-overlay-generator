package validator

import (
	"testing"

	"github.com/kunorikiku/yet-another-overlay-generator/internal/model"
)

// --- Schema  ---

func TestValidateSchema_ValidTopology(t *testing.T) {
	topo := validTopology()
	result := ValidateSchema(topo)
	if !result.IsValid() {
		t.Errorf(" Schema ,  %d :", len(result.Errors))
		for _, e := range result.Errors {
			t.Errorf("  %s", e.Error())
		}
	}
}

func TestValidateSchema_EmptyProjectID(t *testing.T) {
	topo := validTopology()
	topo.Project.ID = ""
	result := ValidateSchema(topo)
	assertHasError(t, result, "project.id")
}

func TestValidateSchema_EmptyProjectName(t *testing.T) {
	topo := validTopology()
	topo.Project.Name = ""
	result := ValidateSchema(topo)
	assertHasError(t, result, "project.name")
}

func TestValidateSchema_NoDomains(t *testing.T) {
	topo := validTopology()
	topo.Domains = nil
	result := ValidateSchema(topo)
	assertHasError(t, result, "domains")
}

func TestValidateSchema_InvalidCIDR(t *testing.T) {
	topo := validTopology()
	topo.Domains[0].CIDR = "invalid-cidr"
	result := ValidateSchema(topo)
	assertHasError(t, result, "domains[0].cidr")
}

func TestValidateSchema_EmptyCIDR(t *testing.T) {
	topo := validTopology()
	topo.Domains[0].CIDR = ""
	result := ValidateSchema(topo)
	assertHasError(t, result, "domains[0].cidr")
}

func TestValidateSchema_InvalidAllocationMode(t *testing.T) {
	topo := validTopology()
	topo.Domains[0].AllocationMode = "invalid"
	result := ValidateSchema(topo)
	assertHasError(t, result, "domains[0].allocation_mode")
}

func TestValidateSchema_InvalidRoutingMode(t *testing.T) {
	topo := validTopology()
	topo.Domains[0].RoutingMode = "ospf"
	result := ValidateSchema(topo)
	assertHasError(t, result, "domains[0].routing_mode")
}

func TestValidateSchema_EmptyNodeID(t *testing.T) {
	topo := validTopology()
	topo.Nodes[0].ID = ""
	result := ValidateSchema(topo)
	assertHasError(t, result, "nodes[0].id")
}

func TestValidateSchema_InvalidNodeRole(t *testing.T) {
	topo := validTopology()
	topo.Nodes[0].Role = "supernode"
	result := ValidateSchema(topo)
	assertHasError(t, result, "nodes[0].role")
}

func TestValidateSchema_InvalidOverlayIP(t *testing.T) {
	topo := validTopology()
	topo.Nodes[0].OverlayIP = "not-an-ip"
	result := ValidateSchema(topo)
	assertHasError(t, result, "nodes[0].overlay_ip")
}

func TestValidateSchema_InvalidEdgeType(t *testing.T) {
	topo := validTopology()
	topo.Edges[0].Type = "magic-tunnel"
	result := ValidateSchema(topo)
	assertHasError(t, result, "edges[0].type")
}

func TestValidateSchema_SelfReferenceEdge(t *testing.T) {
	topo := validTopology()
	topo.Edges[0].FromNodeID = "node-1"
	topo.Edges[0].ToNodeID = "node-1"
	result := ValidateSchema(topo)
	assertHasError(t, result, "edges[0]")
}

func TestValidateSchema_InvalidPort(t *testing.T) {
	topo := validTopology()
	topo.Nodes[0].ListenPort = 70000
	result := ValidateSchema(topo)
	assertHasError(t, result, "nodes[0].listen_port")
}

// ---  ---

func TestValidateSemantic_ValidTopology(t *testing.T) {
	topo := validTopology()
	result := ValidateSemantic(topo)
	if !result.IsValid() {
		t.Errorf(",  %d :", len(result.Errors))
		for _, e := range result.Errors {
			t.Errorf("  %s", e.Error())
		}
	}
}

func TestValidateSemantic_NonExistentDomainRef(t *testing.T) {
	topo := validTopology()
	topo.Nodes[0].DomainID = "domain-nonexistent"
	result := ValidateSemantic(topo)
	assertHasError(t, result, "nodes[0].domain_id")
}

func TestValidateSemantic_NonExistentNodeRefInEdge(t *testing.T) {
	topo := validTopology()
	topo.Edges[0].FromNodeID = "nonexistent-node"
	result := ValidateSemantic(topo)
	assertHasError(t, result, "edges[0].from_node_id")
}

func TestValidateSemantic_DuplicateIP(t *testing.T) {
	topo := validTopology()
	topo.Nodes[0].OverlayIP = "10.10.0.1"
	topo.Nodes[1].OverlayIP = "10.10.0.1"
	result := ValidateSemantic(topo)
	assertHasError(t, result, "nodes[1].overlay_ip")
}

func TestValidateSemantic_IPOutsideDomainCIDR(t *testing.T) {
	topo := validTopology()
	topo.Nodes[0].OverlayIP = "192.168.1.1" //  10.10.0.0/24 
	result := ValidateSemantic(topo)
	assertHasError(t, result, "nodes[0].overlay_ip")
}

func TestValidateSemantic_DuplicateDomainID(t *testing.T) {
	topo := validTopology()
	topo.Domains = append(topo.Domains, model.Domain{
		ID:             "domain-1", // 
		Name:           "duplicate",
		CIDR:           "10.20.0.0/24",
		AllocationMode: "auto",
		RoutingMode:    "babel",
	})
	result := ValidateSemantic(topo)
	assertHasError(t, result, "domains[1].id")
}

func TestValidateSemantic_DuplicateNodeID(t *testing.T) {
	topo := validTopology()
	topo.Nodes = append(topo.Nodes, model.Node{
		ID:       "node-1", // 
		Name:     "duplicate-node",
		Role:     "peer",
		DomainID: "domain-1",
	})
	result := ValidateSemantic(topo)
	assertHasError(t, result, "nodes[2].id")
}

func TestValidateSemantic_IsolatedNode(t *testing.T) {
	topo := validTopology()
	// 
	topo.Nodes = append(topo.Nodes, model.Node{
		ID:       "node-isolated",
		Name:     "isolated-node",
		Role:     "peer",
		DomainID: "domain-1",
	})
	result := ValidateSemantic(topo)
	assertHasWarning(t, result, "topology")
}

func TestValidateSemantic_NATDirectConnect(t *testing.T) {
	topo := validTopology()
	//  NAT 
	topo.Nodes[0].Capabilities.HasPublicIP = false
	topo.Nodes[0].Capabilities.CanAcceptInbound = false
	topo.Nodes[1].Capabilities.HasPublicIP = false
	topo.Nodes[1].Capabilities.CanAcceptInbound = false
	result := ValidateSemantic(topo)
	//  NAT 
	found := false
	for _, w := range result.Warnings {
		if containsSubstring(w.Message, "NAT") || containsSubstring(w.Message, "") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf(" NAT ")
	}
}

func TestValidateSemantic_NATNodeNoOutbound(t *testing.T) {
	topo := &model.Topology{
		Project: model.Project{ID: "test", Name: "Test"},
		Domains: []model.Domain{{
			ID: "domain-1", Name: "test", CIDR: "10.10.0.0/24",
			AllocationMode: "auto", RoutingMode: "babel",
		}},
		Nodes: []model.Node{
			{
				ID: "nat-node", Name: "nat-peer", Role: "peer", DomainID: "domain-1",
				Capabilities: model.NodeCapabilities{HasPublicIP: false, CanAcceptInbound: false},
			},
			{
				ID: "pub-node", Name: "pub-server", Role: "router", DomainID: "domain-1",
				Capabilities: model.NodeCapabilities{HasPublicIP: true, CanAcceptInbound: true},
			},
		},
		// NAT 
		Edges: []model.Edge{},
	}
	result := ValidateSemantic(topo)
	// NAT（）
	// 
	if len(result.Warnings) == 0 {
		t.Errorf("")
	}
}

func TestValidateSemantic_NATViaRelay(t *testing.T) {
	topo := &model.Topology{
		Project: model.Project{ID: "test", Name: "Test"},
		Domains: []model.Domain{{
			ID: "domain-1", Name: "test", CIDR: "10.10.0.0/24",
			AllocationMode: "auto", RoutingMode: "babel",
		}},
		Nodes: []model.Node{
			{
				ID: "nat-1", Name: "nat-peer-1", Role: "peer", DomainID: "domain-1",
				Capabilities: model.NodeCapabilities{HasPublicIP: false, CanAcceptInbound: false},
			},
			{
				ID: "relay-1", Name: "relay", Role: "relay", DomainID: "domain-1",
				Capabilities: model.NodeCapabilities{HasPublicIP: true, CanAcceptInbound: true, CanRelay: true},
			},
		},
		Edges: []model.Edge{
			{ID: "e1", FromNodeID: "nat-1", ToNodeID: "relay-1", Type: "public-endpoint", IsEnabled: true},
		},
	}
	result := ValidateSemantic(topo)
	// NAT  relay ，""
	for _, w := range result.Warnings {
		if containsSubstring(w.Field, "nat_reachability") && containsSubstring(w.Message, "nat-peer-1") {
			t.Errorf("NAT  relay , : %s", w.Message)
		}
	}
}

func TestValidateSemantic_NoIsolatedWarningForSingleNode(t *testing.T) {
	topo := &model.Topology{
		Project: model.Project{ID: "test", Name: "Test"},
		Domains: []model.Domain{{
			ID:             "domain-1",
			Name:           "test",
			CIDR:           "10.10.0.0/24",
			AllocationMode: "auto",
			RoutingMode:    "babel",
		}},
		Nodes: []model.Node{{
			ID:       "node-1",
			Name:     "only-node",
			Role:     "peer",
			DomainID: "domain-1",
		}},
		Edges: []model.Edge{},
	}
	result := ValidateSemantic(topo)
	if len(result.Warnings) > 0 {
		t.Errorf("")
	}
}

// ---  ---

func validTopology() *model.Topology {
	return &model.Topology{
		Project: model.Project{
			ID:   "test-001",
			Name: "Test Project",
		},
		Domains: []model.Domain{
			{
				ID:             "domain-1",
				Name:           "test-network",
				CIDR:           "10.10.0.0/24",
				AllocationMode: "auto",
				RoutingMode:    "babel",
			},
		},
		Nodes: []model.Node{
			{
				ID:         "node-1",
				Name:       "node-alpha",
				Hostname:   "alpha.example.com",
				Platform:   "debian",
				Role:       "router",
				DomainID:   "domain-1",
				ListenPort: 51820,
				Capabilities: model.NodeCapabilities{
					CanAcceptInbound: true,
					CanForward:       true,
					HasPublicIP:      true,
				},
			},
			{
				ID:         "node-2",
				Name:       "node-beta",
				Hostname:   "beta.example.com",
				Platform:   "ubuntu",
				Role:       "router",
				DomainID:   "domain-1",
				ListenPort: 51820,
				Capabilities: model.NodeCapabilities{
					CanAcceptInbound: true,
					CanForward:       true,
					HasPublicIP:      true,
				},
			},
		},
		Edges: []model.Edge{
			{
				ID:           "edge-1",
				FromNodeID:   "node-1",
				ToNodeID:     "node-2",
				Type:         "direct",
				EndpointHost: "203.0.113.2",
				EndpointPort: 51820,
				Transport:    "udp",
				IsEnabled:    true,
			},
			{
				ID:           "edge-2",
				FromNodeID:   "node-2",
				ToNodeID:     "node-1",
				Type:         "direct",
				EndpointHost: "203.0.113.1",
				EndpointPort: 51820,
				Transport:    "udp",
				IsEnabled:    true,
			},
		},
	}
}

func assertHasError(t *testing.T, result *ValidationResult, fieldSubstring string) {
	t.Helper()
	for _, e := range result.Errors {
		if contains(e.Field, fieldSubstring) {
			return
		}
	}
	t.Errorf(" %q , 。: %v", fieldSubstring, result.Errors)
}

func assertHasWarning(t *testing.T, result *ValidationResult, fieldSubstring string) {
	t.Helper()
	for _, w := range result.Warnings {
		if contains(w.Field, fieldSubstring) {
			return
		}
	}
	t.Errorf(" %q , 。: %v", fieldSubstring, result.Warnings)
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && containsSubstring(s, substr))
}

func containsSubstring(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
