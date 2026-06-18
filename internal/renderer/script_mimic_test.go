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
		// 4) one filter = local= line per port, carrying that interface's listen port 51820
		"filter = local=",
		":51820",
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

	// The listen port must appear inside the filter line (a stronger correlated assertion: not just
	// 51820 appearing in isolation).
	if !strings.Contains(script, "filter = local=${MIMIC_EGRESS_IP}:51820") {
		t.Errorf("the mimic filter line should carry the interface listen port 51820 (local=...:51820), but it is missing")
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
	installIdx := strings.Index(script, `apt-get install -y "$_mimic_deb"`)
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

	// One filter line each for the two deduplicated mimic ports.
	for _, port := range []string{":51820", ":51822"} {
		if c := strings.Count(script, "filter = local=${MIMIC_EGRESS_IP}"+port); c != 1 {
			t.Errorf("mimic port %s should appear in exactly 1 filter line, got %d", port, c)
		}
	}

	// Ascending order: the 51820 filter line should come before 51822.
	i20 := strings.Index(script, "filter = local=${MIMIC_EGRESS_IP}:51820")
	i22 := strings.Index(script, "filter = local=${MIMIC_EGRESS_IP}:51822")
	if i20 < 0 || i22 < 0 || i20 >= i22 {
		t.Errorf("mimic filter lines should be in ascending port order (51820 before 51822), got idx20=%d idx22=%d", i20, i22)
	}

	// Negative assertion: a non-mimic interface's port 51999 must not enter any filter line.
	if strings.Contains(script, "filter = local=${MIMIC_EGRESS_IP}:51999") {
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
