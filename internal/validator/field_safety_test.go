package validator

import (
	"testing"

	"github.com/kunorikiku/yet-another-overlay-generator/internal/model"
)

// TestValidateSchema_NodeNameCharset covers node-name charset validation (D15 defense in depth).
// A node name is derived into a WireGuard interface name and interpolated into install scripts
// executed as root, so names containing shell metacharacters (quotes, backticks, $, ;, etc.)
// must be rejected at the schema stage, while names containing only letters, digits, spaces,
// dots, underscores, and hyphens should pass.
func TestValidateSchema_NodeNameCharset(t *testing.T) {
	cases := []struct {
		name        string
		nodeName    string
		expectError bool
	}{
		{name: "backtick command injection", nodeName: "node`id`", expectError: true},
		{name: "dollar-sign command substitution", nodeName: "node$(whoami)", expectError: true},
		{name: "semicolon command chaining", nodeName: "node; rm -rf /", expectError: true},
		{name: "double-quote break-out", nodeName: `node"evil`, expectError: true},
		{name: "single-quote break-out", nodeName: "node'evil", expectError: true},
		{name: "clean hyphenated name", nodeName: "node-alpha", expectError: false},
		{name: "clean name with space, dot, underscore", nodeName: "Web 1.east_a", expectError: false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			topo := validTopology()
			topo.Nodes[0].Name = tc.nodeName
			result := ValidateSchema(topo)
			if tc.expectError {
				assertHasError(t, result, "nodes[0].name")
			} else {
				for _, e := range result.Errors {
					if contains(e.Field, "nodes[0].name") {
						t.Errorf("name %q should not trigger a charset error, but got: %s", tc.nodeName, e.Error())
					}
				}
			}
		})
	}
}

// TestValidateSchema_WGPublicKey covers WireGuard public-key format validation (plan-4). A public key
// is rendered VERBATIM into peers' root-parsed wg configs via a non-escaping template, so a malformed
// value (bad base64, wrong length, or embedded whitespace/newline) must be rejected at the schema
// stage; a clean 32-byte standard-base64 key passes, and an empty value is skipped (a managed node's
// key comes from the enrollment registry, not the topology).
func TestValidateSchema_WGPublicKey(t *testing.T) {
	cases := []struct {
		name        string
		key         string
		expectError bool
	}{
		{name: "valid 32-byte standard base64", key: "AetxbtqeRdq7xOMpbaVK3St4vAoSMsCzTSLvtqs8BTw=", expectError: false},
		{name: "empty is skipped (managed key comes from the registry)", key: "", expectError: false},
		{name: "not base64 (contains hyphen)", key: "not-a-valid-key", expectError: true},
		{name: "valid base64 but wrong length", key: "QUJD", expectError: true},
		{name: "embedded newline (config-injection vector)", key: "AetxbtqeRdq7xOMpbaVK3St4\nvAoSMsCzTSLvtqs8BTw=", expectError: true},
		{name: "surrounding whitespace", key: "  AetxbtqeRdq7xOMpbaVK3St4vAoSMsCzTSLvtqs8BTw=  ", expectError: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			topo := validTopology()
			topo.Nodes[0].WireGuardPublicKey = tc.key
			result := ValidateSchema(topo)
			if tc.expectError {
				assertHasError(t, result, "nodes[0].wireguard_public_key")
			} else {
				for _, e := range result.Errors {
					if contains(e.Field, "nodes[0].wireguard_public_key") {
						t.Errorf("key %q should not trigger a WG-key error, but got: %s", tc.key, e.Error())
					}
				}
			}
		})
	}
}

// TestValidateSchema_NodeIDCharset covers node-ID charset validation (plan-7). A node ID is a
// path/file/interface-name component (the operator deploy-script filename, the manual-bundle
// Content-Disposition), so spaces, '/', and shell metacharacters are rejected; a clean slug/uuid
// passes. (An EMPTY id is a separate "required" error, not a charset error.)
func TestValidateSchema_NodeIDCharset(t *testing.T) {
	cases := []struct {
		name        string
		nodeID      string
		expectError bool
	}{
		{name: "clean slug", nodeID: "node-alpha", expectError: false},
		{name: "uuid-style with dot and underscore", nodeID: "node-8f3a1c2e.4b5d_6", expectError: false},
		{name: "space", nodeID: "node alpha", expectError: true},
		{name: "path traversal", nodeID: "../etc/passwd", expectError: true},
		{name: "command substitution", nodeID: "node$(whoami)", expectError: true},
		{name: "semicolon", nodeID: "node;rm -rf /", expectError: true},
		{name: "slash", nodeID: "a/b", expectError: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			topo := validTopology()
			topo.Nodes[0].ID = tc.nodeID
			result := ValidateSchema(topo)
			if tc.expectError {
				assertHasError(t, result, "nodes[0].id")
			} else {
				for _, e := range result.Errors {
					if contains(e.Field, "nodes[0].id") {
						t.Errorf("id %q should not trigger a charset error, but got: %s", tc.nodeID, e.Error())
					}
				}
			}
		})
	}
}

// TestValidateSchema_EndpointPortRequiresHost covers require-explicit-host (plan-1 residual): an edge
// with an endpoint_port override but no endpoint_host is rejected (a port cannot be dialed without a
// host — otherwise the compiler silently drops the override while the panel badge claims it active).
func TestValidateSchema_EndpointPortRequiresHost(t *testing.T) {
	hasCode := func(topo *model.Topology) bool {
		for _, e := range ValidateSchema(topo).Errors {
			if e.Code == string(CodeEdgeEndpointPortWithoutHost) {
				return true
			}
		}
		return false
	}

	// Default validTopology edge[0] has host + port → no port-without-host error.
	if hasCode(validTopology()) {
		t.Fatal("valid topology (host+port) must not trigger endpoint_port_without_host")
	}

	// A port override with NO host → rejected, on the endpoint_port field.
	portOnly := validTopology()
	portOnly.Edges[0].EndpointHost = "" // EndpointPort stays 51820 > 0
	assertHasError(t, ValidateSchema(portOnly), "edges[0].endpoint_port")
	if !hasCode(portOnly) {
		t.Error("a port override without a host must trigger CodeEdgeEndpointPortWithoutHost")
	}

	// Clearing BOTH (no port, no host) is fine — the default auto endpoint.
	neither := validTopology()
	neither.Edges[0].EndpointHost = ""
	neither.Edges[0].EndpointPort = 0
	if hasCode(neither) {
		t.Error("no port + no host must not trigger endpoint_port_without_host")
	}
}

// TestValidateSchema_SSHFieldCharset covers SSH-field charset validation (D44).
// When non-empty, ssh_host / ssh_alias / ssh_user are interpolated into the bash and
// PowerShell deploy scripts executed on the operator's machine; values containing
// whitespace or shell metacharacters must be rejected, and clean values should pass.
func TestValidateSchema_SSHFieldCharset(t *testing.T) {
	cases := []struct {
		name        string
		mutate      func(n *model.Node)
		field       string
		expectError bool
	}{
		{
			name:        "ssh_host command substitution",
			mutate:      func(n *model.Node) { n.SSHHost = "host$(reboot)" },
			field:       "nodes[0].ssh_host",
			expectError: true,
		},
		{
			name:        "ssh_host contains whitespace",
			mutate:      func(n *model.Node) { n.SSHHost = "1.2.3.4 evil" },
			field:       "nodes[0].ssh_host",
			expectError: true,
		},
		{
			name:        "ssh_alias backtick",
			mutate:      func(n *model.Node) { n.SSHAlias = "alias`id`" },
			field:       "nodes[0].ssh_alias",
			expectError: true,
		},
		{
			name:        "ssh_user semicolon",
			mutate:      func(n *model.Node) { n.SSHUser = "root;reboot" },
			field:       "nodes[0].ssh_user",
			expectError: true,
		},
		{
			name:        "clean ssh_host",
			mutate:      func(n *model.Node) { n.SSHHost = "203.0.113.5" },
			field:       "nodes[0].ssh_host",
			expectError: false,
		},
		{
			name:        "clean ssh_host with colon and at",
			mutate:      func(n *model.Node) { n.SSHHost = "user@host.example.com:2222" },
			field:       "nodes[0].ssh_host",
			expectError: false,
		},
		{
			name:        "clean ssh_user",
			mutate:      func(n *model.Node) { n.SSHUser = "deploy-user_1" },
			field:       "nodes[0].ssh_user",
			expectError: false,
		},
		{
			name:        "empty SSH fields do not error",
			mutate:      func(n *model.Node) { n.SSHHost = ""; n.SSHAlias = ""; n.SSHUser = "" },
			field:       "nodes[0].ssh_host",
			expectError: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			topo := validTopology()
			tc.mutate(&topo.Nodes[0])
			result := ValidateSchema(topo)
			if tc.expectError {
				assertHasError(t, result, tc.field)
			} else {
				for _, e := range result.Errors {
					if contains(e.Field, tc.field) {
						t.Errorf("field %s should not trigger a charset error, but got: %s", tc.field, e.Error())
					}
				}
			}
		})
	}
}

// TestValidateSchema_SSHKeyPathCharset pins the ssh_key_path validation half of
// the deploy-script command-injection fix. ssh_key_path is spliced into the
// operator's bash + PowerShell deploy commands (ssh/scp -i <path>); unlike the
// connection fields it permits real path characters (/ \ ~ : space) but must
// still reject every shell metacharacter. A regression here reopens the
// injection path the renderer escaping also guards.
func TestValidateSchema_SSHKeyPathCharset(t *testing.T) {
	cases := []struct {
		name        string
		keyPath     string
		expectError bool
	}{
		// Hostile: shell metacharacters that enable injection.
		{"command substitution", `/keys/x$(reboot).pem`, true},
		{"powershell quote break", `/keys/k".pem`, true},
		{"backtick", "/keys/k`id`.pem", true},
		{"statement separator", `/keys/k.pem;reboot`, true},
		{"pipe", `/keys/k.pem|cat`, true},
		{"single quote", `/keys/k'.pem`, true},
		// Clean: realistic key paths on Linux and Windows.
		{"linux absolute path", `/home/user/.ssh/id_ed25519`, false},
		{"tilde home path", `~/.ssh/id_rsa`, false},
		{"relative path", `./keys/deploy.pem`, false},
		{"windows backslash path", `C:\Users\me\.ssh\id_rsa`, false},
		{"windows path with space", `C:/Users/John Doe/key.pem`, false},
		{"empty is allowed", ``, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			topo := validTopology()
			topo.Nodes[0].SSHKeyPath = tc.keyPath
			result := ValidateSchema(topo)
			if tc.expectError {
				assertHasError(t, result, "nodes[0].ssh_key_path")
			} else {
				for _, e := range result.Errors {
					if contains(e.Field, "nodes[0].ssh_key_path") {
						t.Errorf("ssh_key_path %q should be accepted, got: %s", tc.keyPath, e.Error())
					}
				}
			}
		})
	}
}

// portRangeTopology builds a minimal topology: a single router node with the given hostname,
// plus enabled edges to peerCount peers. Each edge is a deduplicated node pair, so this router
// gets peerCount per-peer interfaces, with an effective listen-port range of
// [51820, 51820+peerCount-1] (the base port is uniformly 51820), used to cover effective
// port-range validation (D11).
func portRangeTopology(peerCount int, hostname string) *model.Topology {
	topo := &model.Topology{
		Project: model.Project{ID: "test-port", Name: "Port Range Test"},
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
				ID:       "hub",
				Name:     "hub",
				Hostname: hostname,
				Role:     "router",
				DomainID: "domain-1",
				Capabilities: model.NodeCapabilities{
					CanAcceptInbound: true,
					CanForward:       true,
					HasPublicIP:      true,
				},
			},
		},
	}

	for i := 0; i < peerCount; i++ {
		peerID := "peer-" + itoaTest(i)
		topo.Nodes = append(topo.Nodes, model.Node{
			ID:       peerID,
			Name:     "peer-" + itoaTest(i),
			Role:     "router",
			DomainID: "domain-1",
			Capabilities: model.NodeCapabilities{
				CanAcceptInbound: true,
				CanForward:       true,
				HasPublicIP:      true,
			},
		})
		topo.Edges = append(topo.Edges, model.Edge{
			ID:         "edge-" + itoaTest(i),
			FromNodeID: "hub",
			ToNodeID:   peerID,
			Type:       "direct",
			Transport:  "udp",
			IsEnabled:  true,
		})
	}

	return topo
}

// transportTopology builds a two-node single-edge topology whose edge transport is given by
// the parameter, used to cover the tcp reserved-value warning.
func transportTopology(transport string) *model.Topology {
	return &model.Topology{
		Project: model.Project{ID: "test-transport", Name: "Transport Test"},
		Domains: []model.Domain{
			{ID: "domain-1", Name: "net", CIDR: "10.10.0.0/24", AllocationMode: "auto", RoutingMode: "babel"},
		},
		Nodes: []model.Node{
			{ID: "a", Name: "a", Role: "router", DomainID: "domain-1",
				Capabilities: model.NodeCapabilities{CanAcceptInbound: true, CanForward: true, HasPublicIP: true}},
			{ID: "b", Name: "b", Role: "router", DomainID: "domain-1",
				Capabilities: model.NodeCapabilities{CanAcceptInbound: true, CanForward: true, HasPublicIP: true}},
		},
		Edges: []model.Edge{
			{ID: "edge-1", FromNodeID: "a", ToNodeID: "b", Type: "direct", Transport: transport, IsEnabled: true},
		},
	}
}

// TestValidateSchema_TcpTransportNoReservedWarning covers the new contract after
// mimic-tcp-transport landed: tcp is now an implemented, valid value (the link is wrapped by
// mimic), so the schema stage produces neither a transport error nor the v1.3.0
// "reserved/unimplemented" warning (that warning has been removed). Semantic validation of the
// Linux endpoints is handled by validateMimicTransport (see mimic_test.go), not in the schema
// layer. udp likewise has no warning.
func TestValidateSchema_TcpTransportNoReservedWarning(t *testing.T) {
	for _, transport := range []string{"tcp", "udp"} {
		result := ValidateSchema(transportTopology(transport))
		for _, e := range result.Errors {
			if containsSubstring(e.Field, "transport") {
				t.Fatalf("%s is a valid transport value and should not produce a transport error, got: %v", transport, result.Errors)
			}
		}
		for _, w := range result.Warnings {
			if containsSubstring(w.Field, "transport") {
				t.Errorf("%s transport should no longer produce a transport warning (the reserved warning was removed), got: %v", transport, result.Warnings)
			}
		}
	}
}

// itoaTest is a small in-test integer-to-string helper that avoids importing the standard
// library strconv to keep the test self-contained.
func itoaTest(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf []byte
	for n > 0 {
		buf = append([]byte{byte('0' + n%10)}, buf...)
		n /= 10
	}
	if neg {
		buf = append([]byte{'-'}, buf...)
	}
	return string(buf)
}

// TestValidateSemantic_EffectivePortRangeInBounds verifies that a node with the uniform base
// port 51820, connected to 8 peers (ports 51820..51827), does not trigger an out-of-bounds
// error. After the base port was removed it is no longer possible to artificially construct an
// out-of-bounds base -- under base=51820 the overflow rule would require tens of thousands of
// interfaces to trigger, so it has degenerated into a defensive safeguard, and the overflow
// error path is no longer unit-tested.
func TestValidateSemantic_EffectivePortRangeInBounds(t *testing.T) {
	topo := portRangeTopology(8, "")
	result := ValidateSemantic(topo)
	for _, e := range result.Errors {
		if e.Code == string(CodeNodeEffectivePortRangeOverflow) {
			t.Errorf("base 51820 + 8 interfaces (51820-51827) should not overflow, but got: %s", e.Error())
		}
	}
}

// sameHostTopology builds two router nodes sharing the same non-empty hostname, each connected
// to an independent set of peers (interface counts settable independently). After the base port
// became uniformly 51820, the old "same-host effective-range overlap" rule was removed --
// otherwise any two co-located nodes would be falsely judged to overlap, blocking all
// "multiple nodes on one machine" deployments. This helper is used to cover the regression that
// co-located nodes must pass validation.
func sameHostTopology(hostname string, ifacesA, ifacesB int) *model.Topology {
	topo := &model.Topology{
		Project: model.Project{ID: "test-samehost", Name: "Same Host Test"},
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
				ID:       "hub-a",
				Name:     "hub-a",
				Hostname: hostname,
				Role:     "router",
				DomainID: "domain-1",
				Capabilities: model.NodeCapabilities{
					CanAcceptInbound: true,
					CanForward:       true,
					HasPublicIP:      true,
				},
			},
			{
				ID:       "hub-b",
				Name:     "hub-b",
				Hostname: hostname,
				Role:     "router",
				DomainID: "domain-1",
				Capabilities: model.NodeCapabilities{
					CanAcceptInbound: true,
					CanForward:       true,
					HasPublicIP:      true,
				},
			},
		},
	}

	addPeers := func(hubID, tag string, count int) {
		for i := 0; i < count; i++ {
			peerID := tag + "-peer-" + itoaTest(i)
			topo.Nodes = append(topo.Nodes, model.Node{
				ID:       peerID,
				Name:     peerID,
				Role:     "router",
				DomainID: "domain-1",
				Capabilities: model.NodeCapabilities{
					CanAcceptInbound: true,
					CanForward:       true,
					HasPublicIP:      true,
				},
			})
			topo.Edges = append(topo.Edges, model.Edge{
				ID:         tag + "-edge-" + itoaTest(i),
				FromNodeID: hubID,
				ToNodeID:   peerID,
				Type:       "direct",
				Transport:  "udp",
				IsEnabled:  true,
			})
		}
	}

	addPeers("hub-a", "a", ifacesA)
	addPeers("hub-b", "b", ifacesB)

	return topo
}

// TestValidateSemantic_CoHostedNodesValidateClean is the regression guard for the listen_port
// removal: after the base port became uniformly 51820, two nodes sharing the same hostname (each
// with >=1 per-peer interface) necessarily have overlapping effective port ranges -- the old
// "same-host range overlap" rule would therefore wrongly kill all "multiple nodes on one
// machine" deployments, so that rule was removed. This is exactly such a co-located scenario;
// it asserts no effective port-range overflow error (CodeNodeEffectivePortRangeOverflow) is
// produced.
func TestValidateSemantic_CoHostedNodesValidateClean(t *testing.T) {
	topo := sameHostTopology("shared.example.com", 3, 3)
	result := ValidateSemantic(topo)
	for _, e := range result.Errors {
		if e.Code == string(CodeNodeEffectivePortRangeOverflow) {
			t.Errorf("co-located nodes (multiple nodes on one machine) should no longer produce an effective port-range overflow error, but got: %s", e.Error())
		}
	}
}
