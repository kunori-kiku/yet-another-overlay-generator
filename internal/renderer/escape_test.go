package renderer

import (
	"strings"
	"testing"

	"github.com/kunorikiku/yet-another-overlay-generator/internal/compiler"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/model"
)

// This file is the perpetual injection gate for the script/deploy renderers.
// Generated install scripts run as root on every fleet node and the deploy
// script runs on the OPERATOR's own machine, so any user text interpolated into
// them without quoting is a command-injection path (audit theme T4 — D7, D15,
// D16, D43; docs/spec/security/security.md). The hostile-fixture tests below
// render the real artifacts with a weaponised node name and ssh_host and assert
// the payloads come out INERT: present only inside their known-safe quoted form,
// never as a live command-substitution / statement-separator outside quotes.
//
// These tests must keep passing forever; a regression here means a renderer
// stopped escaping a user field and reopened the injection path.

// nodeNamePayload is a node name that would execute `touch /tmp/pwned` as root
// the moment it reaches an unquoted shell context (command substitution).
const nodeNamePayload = `x$(touch /tmp/pwned)`

// sshHostPayload is an ssh_host that would run `rm -rf $HOME` on the operator's
// machine if interpolated unquoted (statement separator + variable expansion +
// trailing comment to swallow the rest of the line).
const sshHostPayload = `x; rm -rf $HOME #`

// ----------------------------------------------------------------------------
// Unit tests for the two escaping helpers.
// ----------------------------------------------------------------------------

func TestBashSingleQuote(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"empty", "", "''"},
		{"plain", "user@host", "'user@host'"},
		{"embedded single quote", "don't", `'don'\''t'`},
		{"dollar and substitution", `x$(touch /tmp/pwned)`, `'x$(touch /tmp/pwned)'`},
		{"backtick", "a`b`c", "'a`b`c'"},
		{"statement separator", `x; rm -rf $HOME #`, `'x; rm -rf $HOME #'`},
		{"double quote stays literal", `a"b`, `'a"b'`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := bashSingleQuote(c.in); got != c.want {
				t.Errorf("bashSingleQuote(%q) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}

func TestPowerShellArgQuote(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"empty", "", `""`},
		{"plain", "user@host", `"user@host"`},
		{"embedded double quote", `a"b`, "\"a`\"b\""},
		{"backtick escaped first", "a`b", "\"a``b\""},
		{"dollar expansion neutralised", "$HOME", "\"`$HOME\""},
		{"substitution neutralised", `$(rm -rf /)`, "\"`$(rm -rf /)\""},
		{"space stays one arg", "my alias", `"my alias"`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := powerShellArgQuote(c.in); got != c.want {
				t.Errorf("powerShellArgQuote(%q) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}

// TestPowerShellArgQuote_BacktickEscapedBeforeOthers guards the ordering
// invariant: the helper must escape backticks first, otherwise the backticks it
// introduces to escape `"` and `$` would themselves be doubled and corrupt the
// output. A literal user backtick adjacent to a dollar sign exercises both.
func TestPowerShellArgQuote_BacktickEscapedBeforeOthers(t *testing.T) {
	got := powerShellArgQuote("`$x")
	want := "\"```$x\"" // user backtick -> ``, then $ -> `$ ; total prefix ```
	if got != want {
		t.Errorf("powerShellArgQuote(\"`$x\") = %q, want %q", got, want)
	}
}

// ----------------------------------------------------------------------------
// Hostile-fixture rendering: the artifacts must come out inert.
// ----------------------------------------------------------------------------

// assertMarkerOnlyInToken verifies that every occurrence of dangerousMarker in
// haystack is contained within safeToken (the known-safe escaped form), with one
// exception: a leading-# bash comment line never executes, so a marker there is
// harmless (the per-node "# --- Node: <name> ---" header is such a line). It
// requires the escaped token to be present (payload preserved) and then, on
// every NON-comment line, deletes the token and asserts the marker is gone — so
// any marker living OUTSIDE the escaped token in an executable line trips the
// test.
func assertMarkerOnlyInToken(t *testing.T, label, haystack, dangerousMarker, safeToken string) {
	t.Helper()
	if !strings.Contains(haystack, safeToken) {
		t.Errorf("%s: escaped token %q not found — payload was dropped or mangled, not rendered inert", label, safeToken)
		return
	}
	for i, line := range strings.Split(haystack, "\n") {
		if !strings.Contains(line, dangerousMarker) {
			continue
		}
		if strings.HasPrefix(strings.TrimSpace(line), "#") {
			continue // bash comment: inert by definition
		}
		residue := strings.ReplaceAll(line, safeToken, "")
		if strings.Contains(residue, dangerousMarker) {
			t.Errorf("%s: line %d has marker %q OUTSIDE escaped token %q — injection path open: %q",
				label, i+1, dangerousMarker, safeToken, line)
		}
	}
}

func hostileDeployTopology() *model.Topology {
	return &model.Topology{
		Project: model.Project{ID: "p1", Name: "hostile-proj"},
		Domains: []model.Domain{
			{ID: "d1", Name: "dom", CIDR: "10.20.0.0/24", RoutingMode: "babel"},
		},
		Nodes: []model.Node{
			{
				ID:        "node-1",
				Name:      nodeNamePayload,
				Role:      "router",
				DomainID:  "d1",
				OverlayIP: "10.20.0.1",
				SSHHost:   sshHostPayload, // user@host where host carries the payload
				SSHUser:   "root",
				Capabilities: model.NodeCapabilities{
					CanForward: true,
				},
			},
		},
	}
}

func TestRenderDeployScripts_BashRendersPayloadInert(t *testing.T) {
	topo := hostileDeployTopology()
	peerMap := map[string][]compiler.PeerInfo{
		"node-1": {
			{NodeID: "node-2", NodeName: "beta", InterfaceName: "wg-beta",
				ListenPort: 51820, LocalTransitIP: "10.10.0.1", LocalLinkLocal: "fe80::1"},
		},
	}
	babelConfigs := map[string]string{"node-1": "babeld.conf-content"}

	bash, _, err := RenderDeployScripts(topo, peerMap, babelConfigs)
	if err != nil {
		t.Fatalf("RenderDeployScripts failed: %v", err)
	}

	// The deploy script must NOT contain the raw command-substitution sequence
	// from the node name outside its single-quoted form.
	nameToken := bashSingleQuote(nodeNamePayload) // 'x$(touch /tmp/pwned)'
	assertMarkerOnlyInToken(t, "bash deploy / node name", bash, "$(touch /tmp/pwned)", nameToken)

	// The SSH target is "root@x; rm -rf $HOME #"; its dangerous markers must
	// likewise live only inside the single-quoted target token.
	targetToken := bashSingleQuote("root@" + sshHostPayload) // 'root@x; rm -rf $HOME #'
	assertMarkerOnlyInToken(t, "bash deploy / ssh target (separator)", bash, "; rm -rf", targetToken)
	assertMarkerOnlyInToken(t, "bash deploy / ssh target (var)", bash, "$HOME", targetToken)

	// Defence-in-depth sanity: there must be no bare `ssh root@x;` form (an
	// unquoted target followed by the injected separator). The escaped target
	// is always single-quoted, so this exact unquoted sequence must be absent.
	if strings.Contains(bash, "root@x; rm -rf") {
		t.Errorf("bash deploy: found unquoted ssh target with injected separator")
	}
}

func TestRenderDeployScripts_PowerShellRendersPayloadInert(t *testing.T) {
	topo := hostileDeployTopology()
	peerMap := map[string][]compiler.PeerInfo{
		"node-1": {
			{NodeID: "node-2", NodeName: "beta", InterfaceName: "wg-beta",
				ListenPort: 51820, LocalTransitIP: "10.10.0.1", LocalLinkLocal: "fe80::1"},
		},
	}
	babelConfigs := map[string]string{"node-1": "babeld.conf-content"}

	_, ps1, err := RenderDeployScripts(topo, peerMap, babelConfigs)
	if err != nil {
		t.Fatalf("RenderDeployScripts failed: %v", err)
	}

	// In PowerShell the node name reaches two distinct contexts:
	//  1. The Write-Host call sites, escaped via powerShellArgQuote (the `$`
	//     neutralised so $(...) cannot run as a PS subexpression).
	//  2. The bash here-string echo lines, escaped via bashSingleQuote (run on
	//     the remote shell).
	psNameToken := powerShellArgQuote(nodeNamePayload) // "x`$(touch /tmp/pwned)"
	bashNameToken := bashSingleQuote(nodeNamePayload)  // 'x$(touch /tmp/pwned)'

	// The raw PS-executable subexpression `$(touch` (dollar NOT backtick-escaped)
	// must never appear: every dollar from the payload is neutralised in one of
	// the two known-safe tokens. Strip both tokens, then assert the raw form is
	// gone.
	residue := strings.ReplaceAll(ps1, psNameToken, "")
	residue = strings.ReplaceAll(residue, bashNameToken, "")
	if strings.Contains(residue, "$(touch /tmp/pwned)") {
		t.Errorf("ps1 deploy: raw command substitution from node name survives outside an escaped token — injection path open")
	}
	// Both escaped forms must actually be present (payload preserved, not dropped).
	if !strings.Contains(ps1, psNameToken) {
		t.Errorf("ps1 deploy: PowerShell-escaped node name token %q missing", psNameToken)
	}
	if !strings.Contains(ps1, bashNameToken) {
		t.Errorf("ps1 deploy: bash-escaped node name token %q missing from here-string", bashNameToken)
	}

	// The ssh target reaches the & ssh / & scp call sites; powerShellArgQuote
	// neutralises its `$` and keeps it one argument. The raw `$HOME` and the
	// raw separator must not survive outside the escaped target tokens.
	psTargetToken := powerShellArgQuote("root@" + sshHostPayload)
	psScpDestToken := powerShellArgQuote("root@" + sshHostPayload + ":/tmp/node-1-install.sh")
	residue = strings.ReplaceAll(ps1, psTargetToken, "")
	residue = strings.ReplaceAll(residue, psScpDestToken, "")
	if strings.Contains(residue, "$HOME") {
		t.Errorf("ps1 deploy: raw $HOME from ssh target survives outside an escaped token")
	}
	if !strings.Contains(ps1, psTargetToken) {
		t.Errorf("ps1 deploy: PowerShell-escaped ssh target token %q missing", psTargetToken)
	}
}

func TestRenderInstallScript_NodeNameRendersInert(t *testing.T) {
	node := &model.Node{
		ID:        "node-1",
		Name:      nodeNamePayload,
		Role:      "router",
		Platform:  "debian",
		OverlayIP: "10.20.0.1",
		Capabilities: model.NodeCapabilities{
			CanForward: true,
		},
	}
	peers := []compiler.PeerInfo{
		{NodeID: "node-2", NodeName: "beta", InterfaceName: "wg-beta",
			ListenPort: 51820, LocalTransitIP: "10.10.0.1", LocalLinkLocal: "fe80::1"},
	}

	script, err := RenderInstallScript(node, peers, true)
	if err != nil {
		t.Fatalf("RenderInstallScript failed: %v", err)
	}

	nameToken := bashSingleQuote(nodeNamePayload) // 'x$(touch /tmp/pwned)'
	if !strings.Contains(script, nameToken) {
		t.Errorf("install script: escaped node-name token %q missing", nameToken)
	}
	assertInstallScriptInert(t, "per-peer install", script, nameToken)
}

func TestRenderClientInstallScript_NodeNameRendersInert(t *testing.T) {
	node := &model.Node{
		ID:        "client-1",
		Name:      nodeNamePayload,
		Role:      "client",
		Platform:  "debian",
		OverlayIP: "10.20.0.2",
	}

	script, err := RenderClientInstallScript(node)
	if err != nil {
		t.Fatalf("RenderClientInstallScript failed: %v", err)
	}

	nameToken := bashSingleQuote(nodeNamePayload)
	if !strings.Contains(script, nameToken) {
		t.Errorf("client install script: escaped node-name token %q missing", nameToken)
	}
	assertInstallScriptInert(t, "client install", script, nameToken)
}

// assertInstallScriptInert checks that the command-substitution marker from the
// node name only ever appears inside the escaped token OR on a bash comment line
// (a leading-# line is never executed, so the marker there is harmless). Any
// other occurrence is a live root-executed command substitution.
func assertInstallScriptInert(t *testing.T, label, script, nameToken string) {
	t.Helper()
	const marker = "$(touch /tmp/pwned)"
	for i, line := range strings.Split(script, "\n") {
		if !strings.Contains(line, marker) {
			continue
		}
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "#") {
			continue // comment line: inert by definition
		}
		// The escaped token must be the only carrier of the marker on this line.
		residue := strings.ReplaceAll(line, nameToken, "")
		if strings.Contains(residue, marker) {
			t.Errorf("%s: line %d has marker outside escaped token (injection): %q", label, i+1, line)
		}
	}
}
