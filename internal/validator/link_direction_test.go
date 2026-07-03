package validator

import (
	"testing"

	"github.com/kunorikiku/yet-another-overlay-generator/internal/model"
)

// linkDirectionTopo builds two public routers joined by one a->b edge with the given
// endpoint_host and link_direction (the minimal direction-rule surface).
func linkDirectionTopo(direction, endpointHost string) *model.Topology {
	return &model.Topology{
		Project: model.Project{ID: "dir-validate", Name: "Dir Validate"},
		Domains: []model.Domain{
			{ID: "domain-1", Name: "net", CIDR: "10.10.0.0/24", AllocationMode: "auto", RoutingMode: "babel"},
		},
		Nodes: []model.Node{
			{ID: "a", Name: "a", Role: "router", DomainID: "domain-1", Platform: "debian",
				Capabilities:    model.NodeCapabilities{CanAcceptInbound: true, CanForward: true, HasPublicIP: true},
				PublicEndpoints: []model.PublicEndpoint{{ID: "a-ep", Host: "a.example", Port: 51820}}},
			{ID: "b", Name: "b", Role: "router", DomainID: "domain-1", Platform: "debian",
				Capabilities:    model.NodeCapabilities{CanAcceptInbound: true, CanForward: true, HasPublicIP: true},
				PublicEndpoints: []model.PublicEndpoint{{ID: "b-ep", Host: "b.example", Port: 51820}}},
		},
		Edges: []model.Edge{
			{ID: "edge-1", FromNodeID: "a", ToNodeID: "b", Type: "public-endpoint",
				EndpointHost: endpointHost, Transport: "udp", LinkDirection: direction, IsEnabled: true},
		},
	}
}

// TestValidate_LinkDirectionEnum pins the schema enum: "" (≡ both) / "both" / "forward" are
// accepted; anything else — including the deliberately-absent "reverse" (D11: one spelling;
// single-linking the other way is expressed by flipping the edge) — is rejected with
// CodeEdgeLinkDirectionInvalid.
func TestValidate_LinkDirectionEnum(t *testing.T) {
	accept := []string{"", "both", "forward"}
	for _, v := range accept {
		if hasCode(ValidateSchema(linkDirectionTopo(v, "b.example")), CodeEdgeLinkDirectionInvalid) {
			t.Errorf("link_direction=%q should be accepted, got CodeEdgeLinkDirectionInvalid", v)
		}
	}

	reject := []string{"reverse", "Forward", "one-way", "single"}
	for _, v := range reject {
		if !hasCode(ValidateSchema(linkDirectionTopo(v, "b.example")), CodeEdgeLinkDirectionInvalid) {
			t.Errorf("link_direction=%q should be rejected with CodeEdgeLinkDirectionInvalid", v)
		}
	}
}

// TestValidate_LinkDirectionForwardHappyPath pins the negative space: a single enabled forward
// edge carrying an endpoint_host between two routers produces NO direction finding at either
// stage.
func TestValidate_LinkDirectionForwardHappyPath(t *testing.T) {
	topo := linkDirectionTopo("forward", "b.example")
	for _, code := range []Code{CodeEdgeLinkDirectionInvalid, CodeEdgeLinkDirectionConflict,
		CodeEdgeLinkDirectionForwardNoEndpoint, CodeEdgeLinkDirectionClientEdge} {
		if hasCode(ValidateSchema(topo), code) || hasCode(ValidateSemantic(topo), code) {
			t.Errorf("valid forward edge should carry no %s", code)
		}
	}
}

// TestValidate_LinkDirectionConflict pins the pair rule: a direction-bearing primary-class edge
// whose node pair has ANY other enabled primary-class edge (opposite OR same direction) errors
// with CodeEdgeLinkDirectionConflict — pair-folding would silently ignore the direction. A
// disabled sibling does not conflict; a backup sibling is its own link and does not conflict.
func TestValidate_LinkDirectionConflict(t *testing.T) {
	withSibling := func(sib model.Edge) *model.Topology {
		topo := linkDirectionTopo("forward", "b.example")
		topo.Edges = append(topo.Edges, sib)
		return topo
	}

	t.Run("opposite-direction sibling conflicts", func(t *testing.T) {
		topo := withSibling(model.Edge{ID: "edge-2", FromNodeID: "b", ToNodeID: "a",
			Type: "public-endpoint", Transport: "udp", IsEnabled: true})
		if !hasCode(ValidateSemantic(topo), CodeEdgeLinkDirectionConflict) {
			t.Fatalf("an enabled opposite-direction primary-class sibling must conflict")
		}
	})

	t.Run("same-direction duplicate sibling conflicts", func(t *testing.T) {
		topo := withSibling(model.Edge{ID: "edge-2", FromNodeID: "a", ToNodeID: "b",
			Type: "public-endpoint", Transport: "udp", IsEnabled: true})
		if !hasCode(ValidateSemantic(topo), CodeEdgeLinkDirectionConflict) {
			t.Fatalf("an enabled same-direction primary-class duplicate must conflict")
		}
	})

	t.Run("disabled sibling does not conflict", func(t *testing.T) {
		topo := withSibling(model.Edge{ID: "edge-2", FromNodeID: "b", ToNodeID: "a",
			Type: "public-endpoint", Transport: "udp", IsEnabled: false})
		if hasCode(ValidateSemantic(topo), CodeEdgeLinkDirectionConflict) {
			t.Fatalf("a disabled sibling must not conflict")
		}
	})

	t.Run("backup sibling does not conflict", func(t *testing.T) {
		topo := withSibling(model.Edge{ID: "edge-2", FromNodeID: "a", ToNodeID: "b",
			Type: "public-endpoint", Transport: "udp", Role: model.EdgeRoleBackup, IsEnabled: true})
		if hasCode(ValidateSemantic(topo), CodeEdgeLinkDirectionConflict) {
			t.Fatalf("a backup sibling is its own link and must not conflict")
		}
	})

	t.Run("direction on a backup edge never pair-conflicts", func(t *testing.T) {
		// The backup edge carries the direction; the pair's primary edge exists — a backup is its
		// own link, so folding cannot shadow its direction.
		topo := linkDirectionTopo("", "b.example")
		topo.Edges = append(topo.Edges, model.Edge{ID: "edge-2", FromNodeID: "a", ToNodeID: "b",
			Type: "public-endpoint", EndpointHost: "b.example", Transport: "udp",
			Role: model.EdgeRoleBackup, LinkDirection: "forward", IsEnabled: true})
		if hasCode(ValidateSemantic(topo), CodeEdgeLinkDirectionConflict) {
			t.Fatalf("a direction-bearing backup edge must not pair-conflict")
		}
	})
}

// TestValidate_LinkDirectionForwardNoEndpoint pins the dead-link rule: forward with no
// endpoint_host errors (the forward peer only ever dials the edge's endpoint host, and the
// reverse dial is suppressed — no side could initiate). A disabled edge is exempt (semantic
// rules skip disabled edges, matching the sibling validators).
func TestValidate_LinkDirectionForwardNoEndpoint(t *testing.T) {
	if !hasCode(ValidateSemantic(linkDirectionTopo("forward", "")), CodeEdgeLinkDirectionForwardNoEndpoint) {
		t.Fatalf("forward without endpoint_host must error with CodeEdgeLinkDirectionForwardNoEndpoint")
	}

	topo := linkDirectionTopo("forward", "")
	topo.Edges[0].IsEnabled = false
	if hasCode(ValidateSemantic(topo), CodeEdgeLinkDirectionForwardNoEndpoint) {
		t.Fatalf("a disabled edge must not trip the forward-no-endpoint rule")
	}

	// "both" never requires a host (today's default semantics are untouched).
	if hasCode(ValidateSemantic(linkDirectionTopo("both", "")), CodeEdgeLinkDirectionForwardNoEndpoint) {
		t.Fatalf("both must not require an endpoint_host")
	}
}

// TestValidate_LinkDirectionClientEdge pins the client rule: a direction on a client-touching
// edge errors with CodeEdgeLinkDirectionClientEdge and (root cause first) skips the dial rules.
func TestValidate_LinkDirectionClientEdge(t *testing.T) {
	topo := linkDirectionTopo("forward", "b.example")
	topo.Nodes[0].Role = "client"
	topo.Nodes[0].Capabilities = model.NodeCapabilities{}
	topo.Nodes[0].PublicEndpoints = nil

	result := ValidateSemantic(topo)
	if !hasCode(result, CodeEdgeLinkDirectionClientEdge) {
		t.Fatalf("a direction on a client-touching edge must error with CodeEdgeLinkDirectionClientEdge")
	}
	if hasCode(result, CodeEdgeLinkDirectionForwardNoEndpoint) {
		t.Fatalf("the client rule is the root cause; the dial rules must be skipped for that edge")
	}
}
