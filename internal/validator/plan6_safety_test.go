package validator

import (
	"testing"

	"github.com/kunorikiku/yet-another-overlay-generator/internal/model"
)

// TestValidateSchema_EndpointHostCharset pins the edge endpoint_host charset gate (plan-6).
// endpoint_host is rendered into the per-peer WireGuard config `Endpoint =` line that root's
// wg-quick parses, so whitespace and control/metacharacters (which would corrupt the config)
// must be rejected at schema time; hostnames, IPv4, and bracketed IPv6 must pass.
func TestValidateSchema_EndpointHostCharset(t *testing.T) {
	cases := []struct {
		name        string
		host        string
		expectError bool
	}{
		{"command substitution", "host$(reboot)", true},
		{"backtick", "host`id`", true},
		{"statement separator", "1.2.3.4;reboot", true},
		{"whitespace", "1.2.3.4 evil", true},
		{"double quote", `host"evil`, true},
		{"pipe", "1.2.3.4|nc", true},
		{"ipv4", "203.0.113.7", false},
		{"hostname", "relay.east.example.com", false},
		{"bracketed ipv6", "[2001:db8::1]", false},
		{"empty (optional)", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			topo := validTopology()
			topo.Edges[0].EndpointHost = tc.host
			result := ValidateSchema(topo)
			if tc.expectError {
				assertHasError(t, result, "edges[0].endpoint_host")
				return
			}
			for _, e := range result.Errors {
				if contains(e.Field, "edges[0].endpoint_host") {
					t.Errorf("endpoint_host %q should be accepted, got: %s", tc.host, e.Error())
				}
			}
		})
	}
}

// TestValidateSchema_PublicEndpointHostCharset pins the node public_endpoints[].host charset
// gate (plan-6): the same WireGuard-config sink as endpoint_host. The field path includes the
// index so a second endpoint's bad host is located precisely.
func TestValidateSchema_PublicEndpointHostCharset(t *testing.T) {
	cases := []struct {
		name        string
		host        string
		expectError bool
	}{
		{"command substitution", "host$(reboot)", true},
		{"whitespace", "1.2.3.4 ; rm", true},
		{"backtick", "h`id`", true},
		{"ipv4", "198.51.100.9", false},
		{"hostname", "gw.example.net", false},
		{"bracketed ipv6", "[2001:db8::dead]", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			topo := validTopology()
			topo.Nodes[0].PublicEndpoints = []model.PublicEndpoint{{Host: tc.host, Port: 51820}}
			result := ValidateSchema(topo)
			if tc.expectError {
				assertHasError(t, result, "nodes[0].public_endpoints[0].host")
				return
			}
			for _, e := range result.Errors {
				if contains(e.Field, "public_endpoints[0].host") {
					t.Errorf("public_endpoints host %q should be accepted, got: %s", tc.host, e.Error())
				}
			}
		})
	}
}

// TestValidateSchema_TransitCIDR pins the domain transit_cidr gate (plan-6): parseable,
// IPv4-only, and /8–/30 so it can be enumerated and still hold a per-link transit pair.
// Empty is accepted (the compiler falls back to the default pool).
func TestValidateSchema_TransitCIDR(t *testing.T) {
	cases := []struct {
		name        string
		cidr        string
		expectError bool
	}{
		{"unparseable", "not-a-cidr", true},
		{"ipv6", "fd00::/64", true},
		{"too large /7", "10.0.0.0/7", true},
		{"too small /31", "10.10.0.0/31", true},
		{"valid /24", "10.20.0.0/24", false},
		{"valid /30 boundary", "10.20.0.0/30", false},
		{"valid /8 boundary", "10.0.0.0/8", false},
		{"empty (default)", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			topo := validTopology()
			topo.Domains[0].TransitCIDR = tc.cidr
			result := ValidateSchema(topo)
			if tc.expectError {
				assertHasError(t, result, "domains[0].transit_cidr")
				return
			}
			for _, e := range result.Errors {
				if contains(e.Field, "transit_cidr") {
					t.Errorf("transit_cidr %q should be accepted, got: %s", tc.cidr, e.Error())
				}
			}
		})
	}
}

// TestValidateSchema_TopologyCountBound pins the DoS count bound (plan-6 item 6): an
// over-limit node or edge count is rejected with the coded error AND short-circuits — only
// the root error is reported (the per-entity passes never run), so an oversized payload does
// not also generate thousands of downstream errors.
func TestValidateSchema_TopologyCountBound(t *testing.T) {
	t.Run("too many nodes short-circuits", func(t *testing.T) {
		topo := validTopology()
		// Blow past the node ceiling with placeholder nodes (intentionally invalid otherwise,
		// to prove the bound short-circuits before per-node validation runs).
		topo.Nodes = make([]model.Node, maxTopologyNodes+1)
		result := ValidateSchema(topo)
		assertHasError(t, result, "nodes")
		if !hasCode(result, CodeTopologyTooManyNodes) {
			t.Fatalf("expected CodeTopologyTooManyNodes, got: %v", result.Errors)
		}
		// Short-circuit: no per-entity errors (e.g. project / per-node id) accompany it.
		if hasCode(result, CodeProjectIDRequired) || hasCode(result, CodeNodeIDRequired) {
			t.Errorf("count bound must short-circuit before per-entity passes; got: %v", result.Errors)
		}
	})

	t.Run("too many edges rejected", func(t *testing.T) {
		topo := validTopology()
		topo.Edges = make([]model.Edge, maxTopologyEdges+1)
		result := ValidateSchema(topo)
		if !hasCode(result, CodeTopologyTooManyEdges) {
			t.Fatalf("expected CodeTopologyTooManyEdges, got: %v", result.Errors)
		}
	})

	t.Run("within bounds accepted", func(t *testing.T) {
		topo := validTopology()
		result := ValidateSchema(topo)
		if hasCode(result, CodeTopologyTooManyNodes) || hasCode(result, CodeTopologyTooManyEdges) {
			t.Errorf("a 2-node topology must not trip the count bound; got: %v", result.Errors)
		}
	})

	t.Run("semantic pass guards silently", func(t *testing.T) {
		topo := validTopology()
		topo.Nodes = make([]model.Node, maxTopologyNodes+1)
		// ValidateSemantic short-circuits on the bound WITHOUT re-reporting (schema is the
		// canonical reporter), so it must return cleanly and never run the O(n²) passes.
		result := ValidateSemantic(topo)
		if len(result.Errors) != 0 {
			t.Errorf("semantic guard must short-circuit silently on an oversized topology; got: %v", result.Errors)
		}
	})
}

// TestValidateSchema_SchemaVersionForwardCompat pins the forward-compat fail-closed guard
// (plan-6 item 7): a topology stamped with an alloc-schema version newer than this build is
// rejected; the current version and absent/0 are accepted.
func TestValidateSchema_SchemaVersionForwardCompat(t *testing.T) {
	t.Run("future version rejected", func(t *testing.T) {
		topo := validTopology()
		topo.AllocSchemaVersion = model.CurrentAllocSchemaVersion + 1
		result := ValidateSchema(topo)
		if !hasCode(result, CodeTopologySchemaVersionUnsupported) {
			t.Fatalf("expected CodeTopologySchemaVersionUnsupported, got: %v", result.Errors)
		}
	})

	t.Run("current version accepted", func(t *testing.T) {
		topo := validTopology()
		topo.AllocSchemaVersion = model.CurrentAllocSchemaVersion
		result := ValidateSchema(topo)
		if hasCode(result, CodeTopologySchemaVersionUnsupported) {
			t.Errorf("the current version must be accepted; got: %v", result.Errors)
		}
	})

	t.Run("absent (zero) accepted", func(t *testing.T) {
		topo := validTopology()
		topo.AllocSchemaVersion = 0
		result := ValidateSchema(topo)
		if hasCode(result, CodeTopologySchemaVersionUnsupported) {
			t.Errorf("an absent/zero version must be accepted; got: %v", result.Errors)
		}
	})
}

// hasCode reports whether result carries an error with the given coded Code.
func hasCode(result *ValidationResult, code Code) bool {
	for _, e := range result.Errors {
		if e.Code == string(code) {
			return true
		}
	}
	return false
}
