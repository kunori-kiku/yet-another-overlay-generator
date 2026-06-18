package validator

import (
	"testing"

	"github.com/kunorikiku/yet-another-overlay-generator/internal/model"
)

// This file covers the contracts of Plan 6 (Spec C: docs/spec/compiler/routing-modes.md)
// that belong to the "validation + normalization" partition:
//   - empty routing_mode normalizes to babel and round-trips (the topology object
//     explicitly carries babel afterward);
//   - static / none are rejected (not yet implemented);
//   - empty transport normalizes to udp;
//   - D50: a confirmed dead link with NAT on both ends and no endpoint_host in either
//     direction reports an error, whereas the same link only warns as long as at least
//     one direction carries an endpoint_host.
//
// Reuses validTopology / assertHasError / assertHasWarning and other helpers from validator_test.go.

// --- routing_mode normalization and round-trip ---

// TestRoutingMode_EmptyNormalizesToBabelAndRoundTrips verifies that an empty routing_mode
// is normalized to babel, and that this normalization is persisted into the topology object
// by write-back (round-trip): after validation the topology object itself explicitly carries babel.
func TestRoutingMode_EmptyNormalizesToBabelAndRoundTrips(t *testing.T) {
	topo := validTopology()
	topo.Domains[0].RoutingMode = ""

	result := ValidateSchema(topo)

	if !result.IsValid() {
		t.Fatalf("empty routing_mode normalized to babel should not produce validation errors, got: %v", result.Errors)
	}
	// round-trip assertion: normalization must be written back to the topology object so that
	// subsequent compilation/persistence explicitly carries babel.
	if got := topo.Domains[0].RoutingMode; got != "babel" {
		t.Errorf("empty routing_mode should be normalized and written back to babel, still got %q", got)
	}
}

// TestRoutingMode_StaticRejected verifies that the static mode is rejected (not yet implemented).
func TestRoutingMode_StaticRejected(t *testing.T) {
	topo := validTopology()
	topo.Domains[0].RoutingMode = "static"

	result := ValidateSchema(topo)

	assertHasError(t, result, "domains[0].routing_mode")
	assertErrorMessageContains(t, result, "domains[0].routing_mode", "static")
	assertErrorMessageContains(t, result, "domains[0].routing_mode", "babel")
}

// TestRoutingMode_NoneRejected verifies that the none mode is rejected (not yet implemented).
func TestRoutingMode_NoneRejected(t *testing.T) {
	topo := validTopology()
	topo.Domains[0].RoutingMode = "none"

	result := ValidateSchema(topo)

	assertHasError(t, result, "domains[0].routing_mode")
	assertErrorMessageContains(t, result, "domains[0].routing_mode", "none")
	assertErrorMessageContains(t, result, "domains[0].routing_mode", "babel")
}

// TestRoutingMode_BabelAccepted verifies that an explicit babel passes validation and is unchanged.
func TestRoutingMode_BabelAccepted(t *testing.T) {
	topo := validTopology()
	topo.Domains[0].RoutingMode = "babel"

	result := ValidateSchema(topo)

	if !result.IsValid() {
		t.Fatalf("explicit babel should not produce validation errors, got: %v", result.Errors)
	}
	if got := topo.Domains[0].RoutingMode; got != "babel" {
		t.Errorf("explicit babel should remain babel, got %q", got)
	}
}

// --- transport normalization ---

// TestTransport_EmptyNormalizesToUDP verifies that an empty transport is normalized to udp and written back to the topology object.
func TestTransport_EmptyNormalizesToUDP(t *testing.T) {
	topo := validTopology()
	topo.Edges[0].Transport = ""
	topo.Edges[1].Transport = ""

	result := ValidateSchema(topo)

	if !result.IsValid() {
		t.Fatalf("empty transport normalized to udp should not produce validation errors, got: %v", result.Errors)
	}
	if got := topo.Edges[0].Transport; got != "udp" {
		t.Errorf("empty transport should be normalized and written back to udp, still got %q", got)
	}
	if got := topo.Edges[1].Transport; got != "udp" {
		t.Errorf("empty transport should be normalized and written back to udp, still got %q", got)
	}
}

// TestTransport_InvalidRejected verifies that an invalid transport is still rejected by enum validation after normalization.
func TestTransport_InvalidRejected(t *testing.T) {
	topo := validTopology()
	topo.Edges[0].Transport = "sctp"

	result := ValidateSchema(topo)

	assertHasError(t, result, "edges[0].transport")
}

// --- D50: dead link with NAT on both ends and no endpoint ---

// natBothEndsTopology builds a minimal topology with both ends behind NAT, directly
// connected to each other. Neither node has a public IP, accepts inbound, or is a relay;
// both edges (bidirectional) carry no endpoint_host by default. This is exactly the
// "confirmed dead link" baseline that D50 is concerned with; each test can adjust the
// endpoint_host of one direction on top of it.
func natBothEndsTopology() *model.Topology {
	return &model.Topology{
		Project: model.Project{ID: "test", Name: "Test"},
		Domains: []model.Domain{{
			ID:             "domain-1",
			Name:           "test",
			CIDR:           "10.10.0.0/24",
			AllocationMode: "auto",
			RoutingMode:    "babel",
		}},
		Nodes: []model.Node{
			{
				ID: "nat-a", Name: "nat-a", Role: "peer", DomainID: "domain-1",
				Capabilities: model.NodeCapabilities{HasPublicIP: false, CanAcceptInbound: false},
			},
			{
				ID: "nat-b", Name: "nat-b", Role: "peer", DomainID: "domain-1",
				Capabilities: model.NodeCapabilities{HasPublicIP: false, CanAcceptInbound: false},
			},
		},
		Edges: []model.Edge{
			{ID: "e-ab", FromNodeID: "nat-a", ToNodeID: "nat-b", Type: "direct", Transport: "udp", IsEnabled: true},
			{ID: "e-ba", FromNodeID: "nat-b", ToNodeID: "nat-a", Type: "direct", Transport: "udp", IsEnabled: true},
		},
	}
}

// TestNATDeadLink_BothDirectionsEndpointless_Errors verifies that when NAT is on both ends,
// neither direction has an endpoint_host, and neither end accepts inbound, the link is judged
// a confirmed dead link and reports an error (rather than only a warning).
func TestNATDeadLink_BothDirectionsEndpointless_Errors(t *testing.T) {
	topo := natBothEndsTopology()
	// Neither edge carries an endpoint_host -- a confirmed dead link.

	result := ValidateSemantic(topo)

	// A dead link should report an error.
	if !hasErrorMentioning(result, "nat-a", "nat-b") {
		t.Errorf("a dead link with NAT on both ends and no endpoint_host in either direction should report an error, got: %v", result.Errors)
	}
}

// TestNATLink_OneDirectionHasEndpoint_OnlyWarns verifies that on the same both-ends-NAT link,
// as long as one direction (the reverse edge) carries an endpoint_host, a link can still be
// established, so it should be downgraded to a warning only, not a dead-link error.
func TestNATLink_OneDirectionHasEndpoint_OnlyWarns(t *testing.T) {
	topo := natBothEndsTopology()
	// The reverse edge nat-b -> nat-a carries an endpoint_host: nat-b can actively dial nat-a.
	topo.Edges[1].EndpointHost = "198.51.100.10"

	result := ValidateSemantic(topo)

	// Should not report a dead-link error.
	if hasErrorMentioning(result, "nat-a", "nat-b") {
		t.Errorf("should not report a dead-link error when one direction already carries an endpoint_host, got: %v", result.Errors)
	}
	// Should still retain the NAT warning (for the direction without an endpoint).
	if !hasWarningMentioning(result, "nat-a", "nat-b") {
		t.Errorf("the direction without an endpoint should retain the NAT warning, got: %v", result.Warnings)
	}
}

// TestNATLink_RelayEndpoint_OnlyWarns verifies that when one end is a relay (accepts inbound),
// even if neither direction has an endpoint_host, the link is not a confirmed dead link and
// should only warn, not error.
func TestNATLink_RelayEndpoint_OnlyWarns(t *testing.T) {
	topo := natBothEndsTopology()
	// Change nat-b to a relay: after capability inference a relay necessarily accepts inbound, so it can be dialed into.
	topo.Nodes[1].Role = "relay"

	result := ValidateSemantic(topo)

	if hasErrorMentioning(result, "nat-a", "nat-b") {
		t.Errorf("should not report a dead-link error when one end is a relay, got: %v", result.Errors)
	}
}

// --- local assertion helpers ---

// assertErrorMessageContains asserts that an error exists whose field matches fieldSubstring and whose message contains msgSubstring.
func assertErrorMessageContains(t *testing.T, result *ValidationResult, fieldSubstring, msgSubstring string) {
	t.Helper()
	for _, e := range result.Errors {
		if contains(e.Field, fieldSubstring) && contains(e.Message, msgSubstring) {
			return
		}
	}
	t.Errorf("no error found whose field contains %q and message contains %q, got: %v", fieldSubstring, msgSubstring, result.Errors)
}

// hasErrorMentioning reports whether an error exists whose message mentions both a and b.
func hasErrorMentioning(result *ValidationResult, a, b string) bool {
	for _, e := range result.Errors {
		if contains(e.Message, a) && contains(e.Message, b) {
			return true
		}
	}
	return false
}

// hasWarningMentioning reports whether a warning exists whose message mentions both a and b.
func hasWarningMentioning(result *ValidationResult, a, b string) bool {
	for _, w := range result.Warnings {
		if contains(w.Message, a) && contains(w.Message, b) {
			return true
		}
	}
	return false
}
