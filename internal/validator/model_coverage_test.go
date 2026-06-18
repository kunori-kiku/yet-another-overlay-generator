package validator

import (
	"testing"

	"github.com/kunorikiku/yet-another-overlay-generator/internal/model"
)

// model_coverage_test.go covers several structural validation rules added in the "field coverage"
// partition (Spec E field-parity table + the coverage table in Spec docs/spec/compiler/validation.md):
//   - route_policies reserved-feature rejection (D10/D37/D62, semantic)
//   - MTU range (D64, schema)
//   - ssh_port range (D65, schema)
//   - router_id MAC-48 / IPv4 format (D66, schema)
//   - extra_prefixes IPv4 CIDR (D67, schema)
//
// Each table covers both "should pass" and "should reject" value classes in pairs, reusing the existing
// validator tests' validTopology()/assertHasError()/contains() helpers.

// assertNoErrorOnField asserts that the result contains no error whose field name contains
// fieldSubstring. Used by the "should pass" branch: a legal value must not trigger any validation
// error on the target field.
func assertNoErrorOnField(t *testing.T, result *ValidationResult, fieldSubstring, value string) {
	t.Helper()
	for _, e := range result.Errors {
		if contains(e.Field, fieldSubstring) {
			t.Errorf("value %q should not trigger an error on field %s, but got: %s", value, fieldSubstring, e.Error())
		}
	}
}

// TestValidateSemantic_RoutePoliciesReserved covers route_policies reserved-feature rejection (D10/D37/D62).
// route_policies is consumed by no renderer and the compiler merely passes it through unchanged, so a
// non-empty array must be rejected in the semantic-validation phase; an empty array (or nil) should pass.
func TestValidateSemantic_RoutePoliciesReserved(t *testing.T) {
	cases := []struct {
		name        string
		policies    []model.RoutePolicy
		expectError bool
	}{
		{
			name:        "nil route_policies passes",
			policies:    nil,
			expectError: false,
		},
		{
			name:        "zero-length route_policies passes",
			policies:    []model.RoutePolicy{},
			expectError: false,
		},
		{
			name: "single route_policy rejected",
			policies: []model.RoutePolicy{
				{ID: "rp-1", DomainID: "domain-1", DestinationCIDR: "192.168.0.0/24"},
			},
			expectError: true,
		},
		{
			name: "multiple route_policies rejected",
			policies: []model.RoutePolicy{
				{ID: "rp-1", DomainID: "domain-1", DestinationCIDR: "192.168.0.0/24"},
				{ID: "rp-2", DomainID: "domain-1", DestinationCIDR: "10.0.0.0/8"},
			},
			expectError: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			topo := validTopology()
			topo.RoutePolicies = tc.policies
			result := ValidateSemantic(topo)
			if tc.expectError {
				assertHasError(t, result, "route_policies")
			} else {
				assertNoErrorOnField(t, result, "route_policies", "")
			}
		})
	}
}

// TestValidateSchema_MTURange covers MTU range validation (D64).
// 0 means use the system default and should pass; when non-zero it must fall within [576, 65535],
// and out-of-range values (including 575 and 65536) should be rejected.
func TestValidateSchema_MTURange(t *testing.T) {
	cases := []struct {
		name        string
		mtu         int
		expectError bool
	}{
		{name: "0 uses default", mtu: 0, expectError: false},
		{name: "lower bound 576", mtu: 576, expectError: false},
		{name: "common 1420", mtu: 1420, expectError: false},
		{name: "upper bound 65535", mtu: 65535, expectError: false},
		{name: "below lower bound 575", mtu: 575, expectError: true},
		{name: "negative", mtu: -1, expectError: true},
		{name: "above upper bound 65536", mtu: 65536, expectError: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			topo := validTopology()
			topo.Nodes[0].MTU = tc.mtu
			result := ValidateSchema(topo)
			if tc.expectError {
				assertHasError(t, result, "nodes[0].mtu")
			} else {
				assertNoErrorOnField(t, result, "nodes[0].mtu", itoaTest(tc.mtu))
			}
		})
	}
}

// TestValidateSchema_SSHPortRange covers ssh_port range validation (D65).
// 0 means use the default port 22 and should pass; when non-zero it must fall within 1-65535, and
// out-of-range values should be rejected.
func TestValidateSchema_SSHPortRange(t *testing.T) {
	cases := []struct {
		name        string
		sshPort     int
		expectError bool
	}{
		{name: "0 uses default port", sshPort: 0, expectError: false},
		{name: "lower bound 1", sshPort: 1, expectError: false},
		{name: "common 22", sshPort: 22, expectError: false},
		{name: "upper bound 65535", sshPort: 65535, expectError: false},
		{name: "negative", sshPort: -1, expectError: true},
		{name: "above upper bound 65536", sshPort: 65536, expectError: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			topo := validTopology()
			topo.Nodes[0].SSHPort = tc.sshPort
			result := ValidateSchema(topo)
			if tc.expectError {
				assertHasError(t, result, "nodes[0].ssh_port")
			} else {
				assertNoErrorOnField(t, result, "nodes[0].ssh_port", itoaTest(tc.sshPort))
			}
		})
	}
}

// TestValidateSchema_RouterIDFormat covers router_id format validation (D66).
// Left empty, the compiler auto-generates it and it should pass; when non-empty it must be in MAC-48
// form or parseable as an IPv4 address, and is rejected if neither.
func TestValidateSchema_RouterIDFormat(t *testing.T) {
	cases := []struct {
		name        string
		routerID    string
		expectError bool
	}{
		{name: "empty auto-generated", routerID: "", expectError: false},
		{name: "valid MAC-48 lowercase", routerID: "02:11:22:33:44:55", expectError: false},
		{name: "valid MAC-48 uppercase", routerID: "AA:BB:CC:DD:EE:FF", expectError: false},
		{name: "valid IPv4", routerID: "10.0.0.1", expectError: false},
		{name: "MAC too few segments", routerID: "02:11:22:33:44", expectError: true},
		{name: "MAC non-hex digit", routerID: "02:11:22:33:44:GG", expectError: true},
		{name: "MAC segment too long", routerID: "002:11:22:33:44:55", expectError: true},
		{name: "IPv6 not accepted", routerID: "fe80::1", expectError: true},
		{name: "plain text", routerID: "router-one", expectError: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			topo := validTopology()
			topo.Nodes[0].RouterID = tc.routerID
			result := ValidateSchema(topo)
			if tc.expectError {
				assertHasError(t, result, "nodes[0].router_id")
			} else {
				assertNoErrorOnField(t, result, "nodes[0].router_id", tc.routerID)
			}
		})
	}
}

// TestValidateSchema_ExtraPrefixesIPv4CIDR covers extra_prefixes IPv4 CIDR validation (D67).
// An empty array should pass; each item must be parseable as an IPv4 CIDR, and non-CIDR, IPv6 CIDR,
// and bare IPs are all rejected.
func TestValidateSchema_ExtraPrefixesIPv4CIDR(t *testing.T) {
	cases := []struct {
		name        string
		prefixes    []string
		expectError bool
	}{
		{name: "empty array passes", prefixes: nil, expectError: false},
		{name: "single valid IPv4 CIDR", prefixes: []string{"192.168.0.0/24"}, expectError: false},
		{name: "multiple valid IPv4 CIDRs", prefixes: []string{"192.168.0.0/24", "10.0.0.0/8"}, expectError: false},
		{name: "non-CIDR text", prefixes: []string{"not-a-cidr"}, expectError: true},
		{name: "bare IP without prefix", prefixes: []string{"192.168.0.1"}, expectError: true},
		{name: "IPv6 CIDR rejected", prefixes: []string{"fd00::/8"}, expectError: true},
		{name: "first valid second invalid", prefixes: []string{"192.168.0.0/24", "bad"}, expectError: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			topo := validTopology()
			topo.Nodes[0].ExtraPrefixes = tc.prefixes
			result := ValidateSchema(topo)
			if tc.expectError {
				assertHasError(t, result, "nodes[0].extra_prefixes")
			} else {
				assertNoErrorOnField(t, result, "nodes[0].extra_prefixes", "")
			}
		})
	}
}
