package validator

import (
	"testing"

	"github.com/kunorikiku/yet-another-overlay-generator/internal/model"
)

// mimicTransportTopology builds a two-node single-edge topology whose edge transport and both
// endpoints' platforms are given by parameters, used to cover the platform-constraint validation
// of mimic (tcp transport)
// (docs/spec/artifacts/mimic.md, compiler/validation.md, contract item 4).
//
// Similar to transportTopology in field_safety_test.go, but additionally parameterizes both
// endpoints' platforms so that the "tcp edge to a non-Linux platform" error case can be built.
func mimicTransportTopology(transport, fromPlatform, toPlatform string) *model.Topology {
	return &model.Topology{
		Project: model.Project{ID: "mimic-validate", Name: "Mimic Validate"},
		Domains: []model.Domain{
			{ID: "domain-1", Name: "net", CIDR: "10.10.0.0/24", AllocationMode: "auto", RoutingMode: "babel"},
		},
		Nodes: []model.Node{
			{ID: "a", Name: "a", Role: "router", DomainID: "domain-1", Platform: fromPlatform,
				Capabilities: model.NodeCapabilities{CanAcceptInbound: true, CanForward: true, HasPublicIP: true}},
			{ID: "b", Name: "b", Role: "router", DomainID: "domain-1", Platform: toPlatform,
				Capabilities: model.NodeCapabilities{CanAcceptInbound: true, CanForward: true, HasPublicIP: true}},
		},
		Edges: []model.Edge{
			{ID: "edge-1", FromNodeID: "a", ToNodeID: "b", Type: "direct", Transport: transport, IsEnabled: true},
		},
	}
}

// TestValidate_MimicTcpBetweenLinux_NoErrorNoWarning covers the happy path of contract item 4:
// a tcp edge between two debian/ubuntu nodes -> neither schema nor semantic should report a
// transport error, and the v1.3.0 "tcp reserved/unimplemented" warning must already be removed
// (no transport-related warning appears at all).
func TestValidate_MimicTcpBetweenLinux_NoErrorNoWarning(t *testing.T) {
	cases := []struct {
		name         string
		fromPF, toPF string
	}{
		{name: "debian <-> ubuntu", fromPF: "debian", toPF: "ubuntu"},
		{name: "ubuntu <-> debian", fromPF: "ubuntu", toPF: "debian"},
		// An empty platform is treated as Linux (allowed), consistent with how other platform
		// validations handle empty values.
		{name: "empty <-> debian (empty=Linux)", fromPF: "", toPF: "debian"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			topo := mimicTransportTopology("tcp", tc.fromPF, tc.toPF)

			// Schema stage: tcp is a valid value, should not error; and there should no longer be the v1.3.0 transport warning.
			schemaResult := ValidateSchema(topo)
			for _, e := range schemaResult.Errors {
				if containsSubstring(e.Field, "transport") {
					t.Errorf("a Linux<->Linux tcp edge should not produce a schema transport error, got: %v", schemaResult.Errors)
				}
			}
			for _, w := range schemaResult.Warnings {
				if containsSubstring(w.Field, "transport") {
					t.Errorf("the v1.3.0 tcp reserved warning should already be removed, but a schema warning was still produced: %v", schemaResult.Warnings)
				}
			}

			// Semantic stage: both ends are deployable Linux, the mimic platform constraint should allow it.
			semResult := ValidateSemantic(topo)
			for _, e := range semResult.Errors {
				if containsSubstring(e.Field, "transport") {
					t.Errorf("a Linux<->Linux tcp edge should not produce a semantic transport error, got: %v", semResult.Errors)
				}
			}
			for _, w := range semResult.Warnings {
				if containsSubstring(w.Field, "transport") {
					t.Errorf("a Linux<->Linux tcp edge should not produce a semantic transport warning, got: %v", semResult.Warnings)
				}
			}
		})
	}
}

// TestValidate_MimicTcpToNonLinux_Errors covers the error path of contract item 4:
// when either endpoint platform of a tcp edge is not a deployable Linux (debian / ubuntu),
// semantic validation must error, the error field locates to that edge's transport, and the
// error message names that edge ID.
func TestValidate_MimicTcpToNonLinux_Errors(t *testing.T) {
	cases := []struct {
		name         string
		fromPF, toPF string
	}{
		{name: "to non-Linux (windows)", fromPF: "debian", toPF: "windows"},
		{name: "from non-Linux (macos)", fromPF: "macos", toPF: "ubuntu"},
		{name: "both non-Linux", fromPF: "windows", toPF: "darwin"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			topo := mimicTransportTopology("tcp", tc.fromPF, tc.toPF)
			result := ValidateSemantic(topo)

			// The error must locate to the edge's transport field.
			assertHasError(t, result, "edges[0].transport")

			// The error message should name that edge (edge.ID="edge-1") to help the operator locate it.
			found := false
			for _, e := range result.Errors {
				if containsSubstring(e.Field, "edges[0].transport") && containsSubstring(e.Message, "edge-1") {
					found = true
				}
			}
			if !found {
				t.Errorf("the error message for a tcp edge to a non-Linux platform should name the edge ID (edge-1), got: %v", result.Errors)
			}
		})
	}
}

// TestValidate_UdpEdge_UnaffectedByMimic covers the invariant of contract item 4:
// a udp edge is entirely unaffected by the mimic platform constraint -- even if an endpoint is a
// non-Linux platform, a udp edge should not report a transport error due to the mimic rule, and
// should not produce any transport warning.
func TestValidate_UdpEdge_UnaffectedByMimic(t *testing.T) {
	// Deliberately set one end to a non-Linux platform: a udp edge should not trigger the mimic platform constraint.
	topo := mimicTransportTopology("udp", "debian", "windows")

	semResult := ValidateSemantic(topo)
	for _, e := range semResult.Errors {
		if containsSubstring(e.Field, "transport") {
			t.Errorf("a udp edge should not trigger the mimic platform constraint (transport error), got: %v", semResult.Errors)
		}
	}

	schemaResult := ValidateSchema(topo)
	for _, w := range schemaResult.Warnings {
		if containsSubstring(w.Field, "transport") {
			t.Errorf("a udp edge should not produce any transport warning, got: %v", schemaResult.Warnings)
		}
	}
	for _, e := range schemaResult.Errors {
		if containsSubstring(e.Field, "transport") {
			t.Errorf("a udp edge should not produce a schema transport error, got: %v", schemaResult.Errors)
		}
	}
}

// TestValidate_XDPModeEnum covers per-node xdp_mode enum validation:
// empty / "skb" / "native" are valid (no xdp_mode error); other values (including wrong casing)
// should error at the schema stage.
func TestValidate_XDPModeEnum(t *testing.T) {
	hasXDPErr := func(r *ValidationResult) bool {
		for _, e := range r.Errors {
			if containsSubstring(e.Field, "xdp_mode") {
				return true
			}
		}
		return false
	}

	for _, mode := range []string{"", "skb", "native"} {
		topo := mimicTransportTopology("tcp", "debian", "debian")
		topo.Nodes[0].XDPMode = mode
		if hasXDPErr(ValidateSchema(topo)) {
			t.Errorf("xdp_mode=%q is valid and should not error", mode)
		}
	}

	for _, mode := range []string{"Native", "generic", "xdp", "SKB"} {
		topo := mimicTransportTopology("tcp", "debian", "debian")
		topo.Nodes[0].XDPMode = mode
		if !hasXDPErr(ValidateSchema(topo)) {
			t.Errorf("xdp_mode=%q is invalid and should produce an xdp_mode error at the schema stage", mode)
		}
	}
}

// TestValidate_MimicEgressInterface covers the optional per-node egress-interface override: empty and
// plausible interface names are valid; malformed values (spaces, shell-injection chars, too long) error
// at the schema stage. The renderer shq-escapes the value, so this is a typo/UX guard.
func TestValidate_MimicEgressInterface(t *testing.T) {
	hasEgressErr := func(r *ValidationResult) bool {
		for _, e := range r.Errors {
			if containsSubstring(e.Field, "mimic_egress_interface") {
				return true
			}
		}
		return false
	}

	for _, iface := range []string{"", "eth0", "wan0", "ens5", "br-lan", "eth0.100"} {
		topo := mimicTransportTopology("tcp", "debian", "debian")
		topo.Nodes[0].MimicEgressInterface = iface
		if hasEgressErr(ValidateSchema(topo)) {
			t.Errorf("mimic_egress_interface=%q is valid and should not error", iface)
		}
	}

	for _, iface := range []string{"eth 0", "wan0; rm -rf /", "$(id)", "thisnameiswaytoolong", "a/b"} {
		topo := mimicTransportTopology("tcp", "debian", "debian")
		topo.Nodes[0].MimicEgressInterface = iface
		if !hasEgressErr(ValidateSchema(topo)) {
			t.Errorf("mimic_egress_interface=%q is invalid and should error at the schema stage", iface)
		}
	}
}
