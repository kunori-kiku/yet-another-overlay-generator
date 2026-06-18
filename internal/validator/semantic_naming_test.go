package validator

import (
	"testing"

	"github.com/kunorikiku/yet-another-overlay-generator/internal/model"
)

// nameCollisionTopology builds a minimal topology with only two nodes that both belong to the
// same Domain and are connected to each other, so that it passes semantic validation in every
// respect except node names, thereby focusing the assertions on the node-name collision rules
// (N1-N3 of Spec D).
func nameCollisionTopology(firstName, secondName string) *model.Topology {
	return &model.Topology{
		Project: model.Project{ID: "test-001", Name: "Test Project"},
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
				ID:       "node-1",
				Name:     firstName,
				Hostname: "first.example.com",
				Platform: "debian",
				Role:     "router",
				DomainID: "domain-1",
				Capabilities: model.NodeCapabilities{
					CanAcceptInbound: true,
					CanForward:       true,
					HasPublicIP:      true,
				},
			},
			{
				ID:       "node-2",
				Name:     secondName,
				Hostname: "second.example.com",
				Platform: "ubuntu",
				Role:     "router",
				DomainID: "domain-1",
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

// TestValidateSemantic_NodeNameCollisions covers, table-driven, the three naming-uniqueness
// invariants of Spec D:
//   - install-script filename collision (N2): "Web 1" and "web-1" both normalize to web-1.install.sh.
//   - raw-name collision (N1): two "Alpha" are exactly identical.
//   - WireGuard interface-name collision (N3): "db.east" and "db-east" both normalize to wg-db-east.
//   - two non-colliding names ("alpha" and "beta") should pass validation.
func TestValidateSemantic_NodeNameCollisions(t *testing.T) {
	cases := []struct {
		name        string
		firstName   string
		secondName  string
		expectError bool
	}{
		{
			name:        "install-script filename collision",
			firstName:   "Web 1",
			secondName:  "web-1",
			expectError: true,
		},
		{
			name:        "raw-name collision",
			firstName:   "Alpha",
			secondName:  "Alpha",
			expectError: true,
		},
		{
			name:        "WireGuard interface-name collision",
			firstName:   "db.east",
			secondName:  "db-east",
			expectError: true,
		},
		{
			name:        "non-colliding names",
			firstName:   "alpha",
			secondName:  "beta",
			expectError: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			topo := nameCollisionTopology(tc.firstName, tc.secondName)
			result := ValidateSemantic(topo)
			if tc.expectError {
				// A collision should be reported on the second node's name field.
				assertHasError(t, result, "nodes[1].name")
			} else {
				// Non-colliding names should not trigger any name-field error.
				for _, e := range result.Errors {
					if contains(e.Field, "nodes[1].name") {
						t.Errorf("names %q and %q should not produce a collision error, but got: %s",
							tc.firstName, tc.secondName, e.Error())
					}
				}
			}
		})
	}
}
