package renderer

import (
	"strings"
	"testing"

	"github.com/kunorikiku/yet-another-overlay-generator/internal/compiler"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/model"
)

// mimicRenderNode returns a forwarding-capable debian router node for the mimic install-script tests.
func mimicRenderNode() *model.Node {
	return &model.Node{
		ID:        "node-1",
		Name:      "alpha",
		Role:      "router",
		Platform:  "debian",
		OverlayIP: "10.50.0.1",
		Capabilities: model.NodeCapabilities{
			CanForward: true,
		},
	}
}

// TestRenderInstallScript_MimicPeer_ProvisionsMimic covers contract item 2: when a node has a mimic
// peer (PeerInfo.Mimic==true), the install script must provision mimic — roughly including: mimic
// package install, egress-NIC runtime probe, /etc/mimic config write, one filter = local= line per
// port (carrying that interface's listen port), mimic@<egress> enable, and the uninstall section's
// mimic teardown.
func TestRenderInstallScript_MimicPeer_ProvisionsMimic(t *testing.T) {
	node := mimicRenderNode()
	peers := []compiler.PeerInfo{
		// mimic interface: listen port 51820 (should end up in the filter line).
		{NodeID: "node-2", NodeName: "beta", InterfaceName: "wg-beta",
			ListenPort: 51820, LocalTransitIP: "10.10.0.1", LocalLinkLocal: "fe80::1",
			Mimic: true, MTU: 1408},
	}

	script, err := RenderInstallScript(node, peers, true)
	if err != nil {
		t.Fatalf("render failed: %v", err)
	}

	// Required mimic-provisioning fragments (presence assertions).
	required := []string{
		// 1) mimic install ladder: distro first (command -v / _pm_install), otherwise the SHA-256-verified
		//    GitHub .deb fallback — read the integrity-verified artifacts.json, hardened curl, sha256sum check.
		"command -v mimic",
		"_pm_install mimic",
		"artifacts.json",
		"--proto '=https,http'",
		"sha256sum -c -",
		// 2) egress NIC + IP runtime probe (mimic attaches to the default-route interface, not the wg interface)
		"ip route show default",
		"ip route get 1.1.1.1",
		// 3) /etc/mimic config directory and write
		"mkdir -p /etc/mimic",
		"/etc/mimic/",
		// 4) one filter = local= line per listen port, formatted via the IPv6-safe _mimic_ipport helper
		"filter = local=",
		"_mimic_ipport()",
		// 5) mimic@<egress> enable and start
		`systemctl enable --now "mimic@`,
		// 6) the uninstall section's mimic teardown (disable + delete config)
		`systemctl disable --now "mimic@`,
		"rm -f \"/etc/mimic/",
	}
	for _, frag := range required {
		if !strings.Contains(script, frag) {
			t.Errorf("the mimic node's install script should contain fragment %q, but it is missing", frag)
		}
	}

	// The listen port must appear inside the local= filter line, in the new IPv6-safe form
	// (a stronger correlated assertion: not just 51820 appearing in isolation).
	if !strings.Contains(script, `filter = local=$(_mimic_ipport "$MIMIC_EGRESS_IP" 51820)`) {
		t.Errorf("the mimic filter line should carry the interface listen port 51820 via _mimic_ipport, but it is missing")
	}
}

// TestRenderInstallScript_MimicGitHubFallback_VerifiesBeforeInstall is the SCRIPT-LEVEL mimic
// custody guard (PERPETUAL, custody tier): when the distro lacks mimic, install.sh downloads the
// .deb, VERIFIES it against the SHA-256 pin read from the integrity-verified artifacts.json, and
// only THEN installs it — and FAILS CLOSED (no install) on a missing pin or a non-apt host. It
// pins the "downloaded bytes verified against the controller-signed artifacts.json pin" boundary
// (PRINCIPLES "generated scripts run as root" + the signed-artifact custody invariant).
func TestRenderInstallScript_MimicGitHubFallback_VerifiesBeforeInstall(t *testing.T) {
	node := mimicRenderNode()
	peers := []compiler.PeerInfo{
		{NodeID: "node-2", NodeName: "beta", InterfaceName: "wg-beta",
			ListenPort: 51820, LocalTransitIP: "10.10.0.1", LocalLinkLocal: "fe80::1", Mimic: true, MTU: 1408},
	}
	script, err := RenderInstallScript(node, peers, true)
	if err != nil {
		t.Fatalf("render: %v", err)
	}

	// Verify-before-install ordering: the SHA-256 check must precede the apt-get install of the
	// downloaded .deb, so an unverified binary can never be installed.
	verifyIdx := strings.Index(script, "sha256sum -c -")
	installIdx := strings.Index(script, "apt-get install -y $_mimic_install")
	if verifyIdx < 0 || installIdx < 0 || verifyIdx >= installIdx {
		t.Errorf("mimic .deb must be SHA-256-verified before install (verify=%d, install=%d)", verifyIdx, installIdx)
	}
	// The pin comes from the integrity-verified artifacts.json, not from untrusted transport
	// (no trust in an upstream .sha256 sidecar). It MUST be read from the bundle copy whose hash
	// was verified — "$SCRIPT_DIR/artifacts.json" — not a bare cwd-relative path that, under an
	// invocation like `cd /tmp && sudo bash /bundle/install.sh`, could consume an unverified file.
	if !strings.Contains(script, `"$SCRIPT_DIR/artifacts.json"`) {
		t.Errorf("mimic fallback must read its pin from $SCRIPT_DIR/artifacts.json (the integrity-verified copy)")
	}
	if strings.Contains(script, " artifacts.json)") || strings.Contains(script, "-f artifacts.json ") {
		t.Errorf("mimic fallback must not read artifacts.json by a bare cwd-relative path")
	}
	// Fail-closed guards: a non-apt host and a missing pin both abort rather than install blind.
	for _, frag := range []string{
		"mimic is not in this distro's repositories",
		"no pinned mimic .deb for",
	} {
		if !strings.Contains(script, frag) {
			t.Errorf("mimic fallback missing fail-closed guard %q", frag)
		}
	}
}

// TestRenderInstallScript_MimicTwoPackage_And_Fallback pins the two-package install + the
// fail-degradable provisioning contract (the rc.2 fix): the rendered install.sh reads BOTH the mimic
// and the mimic-dkms pins from artifacts.json and installs them together (so mimic's Depends:
// mimic-modules resolves from the local dkms .deb), and — per the node's resolved mimic_fallback
// policy — either skips to plain UDP (_MIMIC_SKIP=1, policy=udp) or fails closed at the call site
// (exit 1, policy=none), never a bare set -e abort inside the provisioning function.
func TestRenderInstallScript_MimicTwoPackage_And_Fallback(t *testing.T) {
	node := mimicRenderNode()

	// Structure common to both policies: a policy-agnostic provisioning function that reads the dkms
	// companion pin and installs both .debs in one apt-get (the collected $_mimic_install list).
	structural := []string{
		"_mimic_provision() {",
		`.mimic.debs[$k].dkms_asset // ""`,
		`.mimic.debs[$k].dkms_sha256 // ""`,
		"apt-get install -y $_mimic_install",
	}

	// policy=none (default): fail closed at the call site (exit 1), the fail-closed branch only.
	nonePeers := []compiler.PeerInfo{
		{NodeID: "n2", NodeName: "beta", InterfaceName: "wg-beta", ListenPort: 51820,
			LocalTransitIP: "10.10.0.1", LocalLinkLocal: "fe80::1", Mimic: true, MTU: 1408},
	}
	noneScript, err := RenderInstallScript(node, nonePeers, true)
	if err != nil {
		t.Fatalf("render (none): %v", err)
	}
	for _, frag := range structural {
		if !strings.Contains(noneScript, frag) {
			t.Errorf("two-package structural fragment %q missing (policy=none)", frag)
		}
	}
	if !strings.Contains(noneScript, "mimic_fallback policy is fail-closed") {
		t.Errorf("policy=none must render the fail-closed branch")
	}
	if strings.Contains(noneScript, "_MIMIC_SKIP=1") {
		t.Errorf("policy=none must NOT render the _MIMIC_SKIP=1 (udp) branch")
	}

	// policy=udp: every mimic peer resolves to "udp" -> a provisioning failure degrades to plain UDP.
	udpPeers := []compiler.PeerInfo{
		{NodeID: "n2", NodeName: "beta", InterfaceName: "wg-beta", ListenPort: 51820,
			LocalTransitIP: "10.10.0.1", LocalLinkLocal: "fe80::1", Mimic: true, MTU: 1408,
			MimicFallback: "udp"},
	}
	udpScript, err := RenderInstallScript(node, udpPeers, true)
	if err != nil {
		t.Fatalf("render (udp): %v", err)
	}
	for _, frag := range structural {
		if !strings.Contains(udpScript, frag) {
			t.Errorf("two-package structural fragment %q missing (policy=udp)", frag)
		}
	}
	if !strings.Contains(udpScript, "_MIMIC_SKIP=1") || !strings.Contains(udpScript, "falling back to plain UDP (policy=udp)") {
		t.Errorf("policy=udp must render the _MIMIC_SKIP=1 degrade branch")
	}
	if strings.Contains(udpScript, "mimic_fallback policy is fail-closed") {
		t.Errorf("policy=udp must NOT render the fail-closed branch")
	}
}

// TestRenderClientInstallScript_MimicTwoPackage_And_Fallback is the CLIENT-variant twin of the node
// two-package/fail-degradable test: a client whose single wg0 link is transport=tcp (ClientPeerInfo.
// Mimic) renders the SAME byte-identical _mimic_provision restructure (both debs + the policy-aware
// fallback branches) in the client install template. The node and client templates are
// hand-maintained copies and NO golden pairs a client role with a tcp edge, so the client mimic path
// needs its own coverage (else a client-only regression would ship green).
func TestRenderClientInstallScript_MimicTwoPackage_And_Fallback(t *testing.T) {
	node := &model.Node{ID: "client-1", Name: "cli", Role: "client", Platform: "debian", OverlayIP: "10.50.0.9"}
	structural := []string{
		"_mimic_provision() {",
		`.mimic.debs[$k].dkms_asset // ""`,
		`.mimic.debs[$k].dkms_sha256 // ""`,
		"apt-get install -y $_mimic_install",
	}
	base := compiler.ClientPeerInfo{
		NodeID: "client-1", NodeName: "cli", OverlayIP: "10.50.0.9",
		Mimic: true, MTU: 1408, ListenPort: 51820,
		PrivateKey:      "+NOf/2l0pnbz3g7hm+DMiVowUoYPwppUs8z5iz01+V4=",
		RouterPublicKey: "SH/Te0Jw3dIijStvm889gwZ919RXSGrwEL8hnSRwB0U=",
		RouterEndpoint:  "router.example.com:51820",
		DomainCIDRs:     []string{"10.50.0.0/24"},
	}

	// policy=none (fail-closed): the fail-closed branch, no _MIMIC_SKIP.
	none := base
	none.MimicFallback = "none"
	noneScript, err := RenderClientInstallScript(node, &none)
	if err != nil {
		t.Fatalf("render client (none): %v", err)
	}
	for _, frag := range structural {
		if !strings.Contains(noneScript, frag) {
			t.Errorf("client two-package fragment %q missing (policy=none)", frag)
		}
	}
	if !strings.Contains(noneScript, "mimic_fallback policy is fail-closed") {
		t.Errorf("client policy=none must render the fail-closed branch")
	}
	if strings.Contains(noneScript, "_MIMIC_SKIP=1") {
		t.Errorf("client policy=none must NOT render the _MIMIC_SKIP=1 (udp) branch")
	}

	// policy=udp: a provisioning failure degrades wg0 to plain UDP.
	udp := base
	udp.MimicFallback = "udp"
	udpScript, err := RenderClientInstallScript(node, &udp)
	if err != nil {
		t.Fatalf("render client (udp): %v", err)
	}
	for _, frag := range structural {
		if !strings.Contains(udpScript, frag) {
			t.Errorf("client two-package fragment %q missing (policy=udp)", frag)
		}
	}
	if !strings.Contains(udpScript, "_MIMIC_SKIP=1") || !strings.Contains(udpScript, "falling back to plain UDP (policy=udp)") {
		t.Errorf("client policy=udp must render the _MIMIC_SKIP=1 degrade branch")
	}
	if strings.Contains(udpScript, "mimic_fallback policy is fail-closed") {
		t.Errorf("client policy=udp must NOT render the fail-closed branch")
	}
}

// TestRenderInstallScript_MimicPorts_DedupSorted covers: the listen ports of multiple mimic interfaces
// each emit one filter line in the script, deduplicated and in ascending order. A non-mimic
// interface's port must not appear in any filter line.
func TestRenderInstallScript_MimicPorts_DedupSorted(t *testing.T) {
	node := mimicRenderNode()
	peers := []compiler.PeerInfo{
		// Out of order + one duplicate port, to verify dedup and sorting.
		{NodeID: "n3", NodeName: "gamma", InterfaceName: "wg-gamma",
			ListenPort: 51822, LocalTransitIP: "10.10.0.3", LocalLinkLocal: "fe80::3", Mimic: true, MTU: 1408},
		{NodeID: "n2", NodeName: "beta", InterfaceName: "wg-beta",
			ListenPort: 51820, LocalTransitIP: "10.10.0.1", LocalLinkLocal: "fe80::1", Mimic: true, MTU: 1408},
		{NodeID: "n2b", NodeName: "beta2", InterfaceName: "wg-beta2",
			ListenPort: 51820, LocalTransitIP: "10.10.0.5", LocalLinkLocal: "fe80::5", Mimic: true, MTU: 1408},
		// Non-mimic interface: its port 51999 must never appear in a filter line.
		{NodeID: "n4", NodeName: "delta", InterfaceName: "wg-delta",
			ListenPort: 51999, LocalTransitIP: "10.10.0.7", LocalLinkLocal: "fe80::7", Mimic: false, MTU: 0},
	}

	script, err := RenderInstallScript(node, peers, true)
	if err != nil {
		t.Fatalf("render failed: %v", err)
	}

	// One local= filter line each for the two deduplicated mimic ports (new IPv6-safe _mimic_ipport form).
	localFilter := func(port string) string {
		return `filter = local=$(_mimic_ipport "$MIMIC_EGRESS_IP" ` + port + `)`
	}
	for _, port := range []string{"51820", "51822"} {
		if c := strings.Count(script, localFilter(port)); c != 1 {
			t.Errorf("mimic port %s should appear in exactly 1 local= filter line, got %d", port, c)
		}
	}

	// Ascending order: the 51820 filter line should come before 51822.
	i20 := strings.Index(script, localFilter("51820"))
	i22 := strings.Index(script, localFilter("51822"))
	if i20 < 0 || i22 < 0 || i20 >= i22 {
		t.Errorf("mimic filter lines should be in ascending port order (51820 before 51822), got idx20=%d idx22=%d", i20, i22)
	}

	// Negative assertion: a non-mimic interface's port 51999 must not enter any filter line.
	if strings.Contains(script, localFilter("51999")) {
		t.Errorf("a non-mimic interface's port 51999 should not appear in a mimic filter line")
	}
}

// TestRenderInstallScript_UdpOnly_NoMimic covers the converse of contract item 2: a node with only
// udp peers (no PeerInfo.Mimic==true) must not have any mimic provisioning in its install script —
// neither "/etc/mimic" nor "mimic@" should appear, and it must not install the mimic package or emit
// a filter line.
func TestRenderInstallScript_UdpOnly_NoMimic(t *testing.T) {
	node := mimicRenderNode()
	peers := []compiler.PeerInfo{
		{NodeID: "node-2", NodeName: "beta", InterfaceName: "wg-beta",
			ListenPort: 51820, LocalTransitIP: "10.10.0.1", LocalLinkLocal: "fe80::1", Mimic: false, MTU: 0},
		{NodeID: "node-3", NodeName: "gamma", InterfaceName: "wg-gamma",
			ListenPort: 51821, LocalTransitIP: "10.10.0.3", LocalLinkLocal: "fe80::3", Mimic: false, MTU: 0},
	}

	script, err := RenderInstallScript(node, peers, true)
	if err != nil {
		t.Fatalf("render failed: %v", err)
	}

	absent := []string{
		"/etc/mimic",
		"mimic@",
		"_pm_install mimic",
		"filter = local=",
		"Provisioning mimic",
		"--proto '=https,http'",
	}
	for _, frag := range absent {
		if strings.Contains(script, frag) {
			t.Errorf("the install script of a udp-only node should not contain mimic fragment %q, but it appeared", frag)
		}
	}
}

// TestRenderInstallScript_MimicXDPMode covers the per-node xdp_mode override: the default (XDPMode
// left empty) writes "xdp_mode = skb" (generic XDP, compatible with VPS NICs that do not support
// native); when a node explicitly sets "native", it writes "xdp_mode = native".
func TestRenderInstallScript_MimicXDPMode(t *testing.T) {
	mimicPeers := []compiler.PeerInfo{
		{NodeID: "node-2", NodeName: "beta", InterfaceName: "wg-beta",
			ListenPort: 51820, LocalTransitIP: "10.10.0.1", LocalLinkLocal: "fe80::1",
			Mimic: true, MTU: 1408},
	}

	// default (empty) -> skb
	defNode := mimicRenderNode()
	defScript, err := RenderInstallScript(defNode, mimicPeers, true)
	if err != nil {
		t.Fatalf("render failed: %v", err)
	}
	if !strings.Contains(defScript, "xdp_mode = skb") {
		t.Errorf("default should write 'xdp_mode = skb', but it is missing")
	}
	if strings.Contains(defScript, "xdp_mode = native") {
		t.Errorf("default should not write 'xdp_mode = native', but it appeared")
	}

	// explicit native -> native
	natNode := mimicRenderNode()
	natNode.XDPMode = "native"
	natScript, err := RenderInstallScript(natNode, mimicPeers, true)
	if err != nil {
		t.Fatalf("render failed: %v", err)
	}
	if !strings.Contains(natScript, "xdp_mode = native") {
		t.Errorf("XDPMode=native should write 'xdp_mode = native', but it is missing")
	}
	if strings.Contains(natScript, "xdp_mode = skb") {
		t.Errorf("XDPMode=native should not write 'xdp_mode = skb', but it appeared")
	}
}

// TestRenderInstallScript_MimicRemoteFilters covers the route-independent remote= filter (the root
// fix for "mimic local= used the wrong source IP and did nothing"): every mimic peer this node DIALS
// (PeerInfo.Endpoint != "") emits a `filter = remote=<resolved-ip>:<port>` line via the install-time
// resolver; an inbound-only mimic peer (Endpoint=="") emits none. IPv6 endpoints are parsed via
// net.SplitHostPort and the bracketed host is resolved.
func TestRenderInstallScript_MimicRemoteFilters(t *testing.T) {
	node := mimicRenderNode()
	peers := []compiler.PeerInfo{
		// dialed peer with an IPv4 endpoint -> remote= filter
		{NodeID: "n2", NodeName: "beta", InterfaceName: "wg-beta", ListenPort: 51820,
			LocalTransitIP: "10.10.0.1", LocalLinkLocal: "fe80::1", Mimic: true, MTU: 1408,
			Endpoint: "203.0.113.5:51820"},
		// dialed peer with an IPv6 endpoint -> remote= filter, host parsed without brackets
		{NodeID: "n3", NodeName: "gamma", InterfaceName: "wg-gamma", ListenPort: 51821,
			LocalTransitIP: "10.10.0.3", LocalLinkLocal: "fe80::3", Mimic: true, MTU: 1408,
			Endpoint: "[2001:db8::5]:51821"},
		// inbound-only mimic peer (no endpoint we dial) -> NO remote= line, but its local= port stands
		{NodeID: "n4", NodeName: "delta", InterfaceName: "wg-delta", ListenPort: 51822,
			LocalTransitIP: "10.10.0.5", LocalLinkLocal: "fe80::5", Mimic: true, MTU: 1408,
			Endpoint: ""},
	}
	script, err := RenderInstallScript(node, peers, true)
	if err != nil {
		t.Fatalf("render failed: %v", err)
	}

	// The IPv4 dialed peer must produce a resolve call + a remote= filter on its endpoint port.
	if !strings.Contains(script, "_mimic_resolve '203.0.113.5'") {
		t.Errorf("expected an install-time resolve of the IPv4 peer endpoint host, missing")
	}
	// The IPv6 host must be parsed WITHOUT brackets for resolution (net.SplitHostPort strips them).
	if !strings.Contains(script, "_mimic_resolve '2001:db8::5'") {
		t.Errorf("expected the IPv6 peer endpoint host parsed bracket-free for resolution, missing")
	}
	// Both dialed peers emit a remote= filter on the resolved IP (IPv6-safe via _mimic_ipport).
	if c := strings.Count(script, `filter = remote=$(_mimic_ipport "$_mimic_rip"`); c != 2 {
		t.Errorf("expected exactly 2 remote= filter lines (the two dialed peers), got %d", c)
	}
	// All three mimic listen ports still get a local= line (the inbound-only peer included).
	for _, p := range []string{"51820", "51821", "51822"} {
		if !strings.Contains(script, `filter = local=$(_mimic_ipport "$MIMIC_EGRESS_IP" `+p+`)`) {
			t.Errorf("expected a local= filter for listen port %s, missing", p)
		}
	}
}

// TestRenderInstallScript_MimicEgressGuards covers the loopback/unresolved-egress guard (the literal
// "using local" failure): a loopback or empty egress src is dropped rather than written as a dead
// local=127.0.0.1 filter, and the new egress_unresolved breadcrumb is emitted. Also pins the
// egress-detection command shape (previously unasserted) so the owner runbook stays in sync.
func TestRenderInstallScript_MimicEgressGuards(t *testing.T) {
	node := mimicRenderNode()
	peers := []compiler.PeerInfo{
		{NodeID: "n2", NodeName: "beta", InterfaceName: "wg-beta", ListenPort: 51820,
			LocalTransitIP: "10.10.0.1", LocalLinkLocal: "fe80::1", Mimic: true, MTU: 1408},
	}
	script, err := RenderInstallScript(node, peers, true)
	if err != nil {
		t.Fatalf("render failed: %v", err)
	}
	for _, frag := range []string{
		// egress-detection command shape (the runbook's compare points)
		"ip route show default",
		"ip route get 1.1.1.1",
		// loopback/empty rejection: a 127.* or ::1 src is blanked so it is treated as unresolved
		`case "$MIMIC_EGRESS_IP" in 127.*|::1)`,
		// the new closed-enum breadcrumb token surfaces the unresolved-egress outcome to the agent
		"egress_unresolved",
		// the IPv6-safe filter helpers are defined
		"_mimic_ipport()",
		"_mimic_resolve()",
	} {
		if !strings.Contains(script, frag) {
			t.Errorf("mimic egress guard fragment %q missing", frag)
		}
	}
	// A dead loopback literal must never be hardcoded into a filter line.
	if strings.Contains(script, "local=127.0.0.1:") {
		t.Errorf("a loopback local= filter must never be rendered")
	}
}

// TestCollectMimicRemotes unit-tests the endpoint collector: only dialed mimic peers contribute, the
// set is deduped + deterministically ordered, and unparseable/zero-port/empty endpoints are skipped.
func TestCollectMimicRemotes(t *testing.T) {
	peers := []compiler.PeerInfo{
		{Mimic: true, Endpoint: "203.0.113.9:51820"},
		{Mimic: true, Endpoint: "203.0.113.1:51820"},   // out of order -> sorts before .9
		{Mimic: true, Endpoint: "203.0.113.1:51820"},   // duplicate -> deduped
		{Mimic: true, Endpoint: "[2001:db8::1]:51900"}, // IPv6 -> host parsed bracket-free
		{Mimic: true, Endpoint: ""},                    // inbound-only -> skipped
		{Mimic: true, Endpoint: "garbage-no-port"},     // unparseable -> skipped
		{Mimic: true, Endpoint: "203.0.113.2:0"},       // zero port -> skipped
		{Mimic: true, Endpoint: "203.0.113.3:70000"},   // out-of-range port -> skipped (parity w/ TS)
		{Mimic: false, Endpoint: "198.51.100.1:51820"}, // non-mimic -> skipped
	}
	got := collectMimicRemotes(peers)
	want := []MimicEndpoint{
		{Host: "2001:db8::1", Port: 51900},
		{Host: "203.0.113.1", Port: 51820},
		{Host: "203.0.113.9", Port: 51820},
	}
	if len(got) != len(want) {
		t.Fatalf("collectMimicRemotes returned %d entries, want %d: %+v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("entry %d = %+v, want %+v", i, got[i], want[i])
		}
	}
}

// TestResolveMimicXDPMode directly covers the normalization function: only "native" passes through;
// everything else (empty / skb / invalid) falls back to skb.
func TestResolveMimicXDPMode(t *testing.T) {
	cases := map[string]string{"": "skb", "skb": "skb", "native": "native", "Native": "skb", "generic": "skb"}
	for in, want := range cases {
		if got := resolveMimicXDPMode(in); got != want {
			t.Errorf("resolveMimicXDPMode(%q) = %q, want %q", in, got, want)
		}
	}
}
