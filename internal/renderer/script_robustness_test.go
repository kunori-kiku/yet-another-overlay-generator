package renderer

import (
	"strings"
	"testing"

	"github.com/kunorikiku/yet-another-overlay-generator/internal/compiler"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/model"
)

// robustnessTestNode returns a forwarding-capable router node for the install-script robustness tests.
func robustnessTestNode() *model.Node {
	return &model.Node{
		ID:        "node-1",
		Name:      "alpha",
		Role:      "router",
		Platform:  "debian",
		OverlayIP: "10.11.0.1",
		Capabilities: model.NodeCapabilities{
			CanForward: true,
		},
	}
}

// robustnessTestPeers returns two per-peer interfaces so that both the wg-quick startup block and the
// SNAT block are rendered in their multi-interface / multi-CIDR shape.
func robustnessTestPeers() []compiler.PeerInfo {
	return []compiler.PeerInfo{
		{NodeID: "n2", NodeName: "beta", InterfaceName: "wg-beta",
			ListenPort: 51820, LocalTransitIP: "10.10.0.1", LocalLinkLocal: "fe80::1"},
		{NodeID: "n3", NodeName: "gamma", InterfaceName: "wg-gamma",
			ListenPort: 51821, LocalTransitIP: "10.10.0.3", LocalLinkLocal: "fe80::3"},
	}
}

// TestRenderInstallScript_D52_IptablesLoopDelete verifies D52: the iptables SNAT cleanup no longer
// deletes by an exact rule (including --to-source <current overlay IP>). Instead it parses
// iptables-save and deletes every POSTROUTING SNAT rule matching the wg interface + transit source
// pool in full, regardless of what --to-source is. This way a reinstall/uninstall after an overlay-IP
// change clears the stale rules left behind, avoiding incorrect source rewrites.
func TestRenderInstallScript_D52_IptablesLoopDelete(t *testing.T) {
	script, err := RenderInstallScript(robustnessTestNode(), robustnessTestPeers(), true)
	if err != nil {
		t.Fatalf("render failed: %v", err)
	}

	// Under the default pool the loop-delete should be iptables-save parse + full-rule delete. Assert
	// the stable template substrings: the pipe head (iptables-save), the order-independent chained
	// grep -F filters (POSTROUTING / SNAT / egress interface wg-+ / source pool), the substitution
	// rewriting -A to -D, and the iptables -t nat call in the delete branch that removes the whole rule.
	loopDeleteFragments := []string{
		`iptables-save -t nat 2>/dev/null \`,
		`| grep -E '^-A POSTROUTING '`,
		`| grep -F -- '-j SNAT'`,
		`| grep -F -- '-o wg-+'`,
		`| grep -F -- '-s 10.10.0.0/24'`,
		`_snat_del="${_snat_rule/#-A/-D}"`,
		`iptables -t nat $_snat_del 2>/dev/null || true`,
	}
	for _, frag := range loopDeleteFragments {
		if !strings.Contains(script, frag) {
			t.Errorf("D52: missing iptables-save loop-delete fragment:\n  %q", frag)
		}
	}

	// The loop-delete must appear in both cleanup contexts: the pre-install _overlay_snat_cleanup
	// function and the uninstall section's "Remove overlay SNAT rule and service" block. Count the
	// occurrences of the pipe head.
	pipeHead := `iptables-save -t nat 2>/dev/null \`
	if got := strings.Count(script, pipeHead); got < 2 {
		t.Errorf("D52: loop-delete should appear in both the install cleanup and the uninstall cleanup, got %d occurrences", got)
	}

	// Key negative assertion: the old "exact-match delete" form (quoted -o "wg-+" with --to-source)
	// must no longer appear in any *cleanup* path. The persistent systemd unit uses the unquoted
	// -o wg-+, which does not collide with this substring, so this assertion specifically targets the
	// cleanup form that D52 removed.
	staleExactDelete := `iptables -t nat -D POSTROUTING -o "wg-+"`
	if strings.Contains(script, staleExactDelete) {
		t.Errorf("D52 regression: cleanup path still uses exact-match delete (with --to-source); it should use loop-delete:\n  %q", staleExactDelete)
	}
}

// TestRenderInstallScript_D53_WgQuickFailureTolerant verifies D53: bringing up each WireGuard
// interface in Phase 3 tolerates failure — `if ! wg-quick up ...; then` collects failures (without
// set -e aborting outright), warns on stderr and continues so that later steps such as babeld still
// run; the end of the script prints a failure summary and exits with a non-zero code when there were
// failures (deployment tooling can still detect failure), but the exit happens after the remaining steps.
func TestRenderInstallScript_D53_WgQuickFailureTolerant(t *testing.T) {
	script, err := RenderInstallScript(robustnessTestNode(), robustnessTestPeers(), true)
	if err != nil {
		t.Fatalf("render failed: %v", err)
	}

	// Failure accumulator initialization.
	if !strings.Contains(script, `FAILED_INTERFACES=""`) {
		t.Errorf("D53: missing FAILED_INTERFACES accumulator initialization")
	}

	// Each interface must use the if ! wg-quick up form (set -e safe), accumulating and warning on failure.
	tolerantFragments := []string{
		`if ! wg-quick up "wg-beta"; then`,
		`if ! wg-quick up "wg-gamma"; then`,
		`FAILED_INTERFACES="$FAILED_INTERFACES wg-beta"`,
		`FAILED_INTERFACES="$FAILED_INTERFACES wg-gamma"`,
		`continuing with remaining setup" >&2`,
	}
	for _, frag := range tolerantFragments {
		if !strings.Contains(script, frag) {
			t.Errorf("D53: missing failure-tolerant fragment:\n  %q", frag)
		}
	}

	// A "bare" wg-quick up (without an if guard) must never appear again — that would abort the
	// script under set -e. After rendering, each interface gets one startup line; the bare form looks
	// like `\nwg-quick up "wg-beta"\n`.
	for _, iface := range []string{"wg-beta", "wg-gamma"} {
		bare := "\nwg-quick up \"" + iface + "\""
		if strings.Contains(script, bare) {
			t.Errorf("D53 regression: interface %s is still brought up with a bare wg-quick up (no set -e guard)", iface)
		}
	}

	// End-of-script summary block: print the list and exit non-zero when there were failures.
	summaryFragments := []string{
		`if [ -n "$FAILED_INTERFACES" ]; then`,
		`the following WireGuard interface(s) failed to start:$FAILED_INTERFACES" >&2`,
		`exit 1`,
	}
	for _, frag := range summaryFragments {
		if !strings.Contains(script, frag) {
			t.Errorf("D53: missing end-of-script failure summary fragment:\n  %q", frag)
		}
	}

	// Ordering: the babeld configuration must come after the wg-quick startup block and before the
	// summary exit, proving that a "half-started" state cannot happen (even if an interface fails,
	// babeld is already configured; the non-zero exit happens last).
	startIdx := strings.Index(script, `FAILED_INTERFACES=""`)
	babelIdx := strings.Index(script, "Configuring babeld systemd service")
	summaryIdx := strings.Index(script, `if [ -n "$FAILED_INTERFACES" ]; then`)
	if startIdx < 0 || babelIdx < 0 || summaryIdx < 0 {
		t.Fatalf("D53: missing key anchors (start=%d babel=%d summary=%d)", startIdx, babelIdx, summaryIdx)
	}
	if !(startIdx < babelIdx && babelIdx < summaryIdx) {
		t.Errorf("D53: order should be wg startup(%d) -> babeld config(%d) -> failure summary exit(%d)", startIdx, babelIdx, summaryIdx)
	}
}

// parallelLinksPeers returns two parallel links (primary + backup) pointing at the same peer, with
// distinct interface names and distinct listen ports / transit IPs — simulating a node holding both a
// primary and a backup tunnel toward one neighbor.
func parallelLinksPeers() []compiler.PeerInfo {
	return []compiler.PeerInfo{
		{NodeID: "n2", NodeName: "beta", InterfaceName: "wg-beta",
			ListenPort: 51820, LocalTransitIP: "10.10.0.1", LocalLinkLocal: "fe80::1"},
		// backup: edge-aware interface name (just a different shape), with its own port and transit IP.
		{NodeID: "n2", NodeName: "beta", InterfaceName: "wg-beta-bk1",
			ListenPort: 51821, LocalTransitIP: "10.10.0.3", LocalLinkLocal: "fe80::3", LinkCost: 384},
	}
}

// TestRenderInstallScript_ParallelLinks_BothInterfacesEveryPhase verifies that the install script of
// a parallel-links (primary + backup) node lists both interfaces in every per-interface phase:
//   - Phase 3 startup (D53's `if ! wg-quick up "<iface>"`)
//   - Phase 3 boot autostart (systemctl enable wg-quick@"<iface>")
//   - the uninstall section's stop / disable / delete config (wg-quick down / systemctl disable / rm config file)
//
// The install script expands the template's {{ range .WgInterfaces }} block per interface from the
// PeerInfo list, so both links (two InterfaceNames) must appear exactly once in each per-interface
// block; a missing one means some tunnel would not be started / enabled / cleaned up.
func TestRenderInstallScript_ParallelLinks_BothInterfacesEveryPhase(t *testing.T) {
	script, err := RenderInstallScript(robustnessTestNode(), parallelLinksPeers(), true)
	if err != nil {
		t.Fatalf("render failed: %v", err)
	}

	ifaces := []string{"wg-beta", "wg-beta-bk1"}

	for _, iface := range ifaces {
		// Phase 3 startup: the D53 fault-tolerant form of wg-quick up.
		if !strings.Contains(script, `if ! wg-quick up "`+iface+`"; then`) {
			t.Errorf("startup phase missing the wg-quick up line for interface %s", iface)
		}
		// Phase 3 boot autostart.
		if !strings.Contains(script, `systemctl enable wg-quick@"`+iface+`"`) {
			t.Errorf("startup phase missing the systemctl enable line for interface %s", iface)
		}
		// Uninstall section stop: wg-quick down.
		if !strings.Contains(script, `wg-quick down "`+iface+`"`) {
			t.Errorf("uninstall/cleanup phase missing the wg-quick down line for interface %s", iface)
		}
		// Uninstall section disable of the systemd unit.
		if !strings.Contains(script, `systemctl disable "wg-quick@`+iface+`"`) {
			t.Errorf("uninstall/cleanup phase missing the systemctl disable line for interface %s", iface)
		}
		// Config-file cleanup (deleted once in the uninstall section and once in Phase 0).
		if !strings.Contains(script, `/etc/wireguard/`+iface+`.conf`) {
			t.Errorf("script missing the config-file path reference for interface %s", iface)
		}
	}

	// Non-empty gate: the backup interface (wg-beta-bk1) is genuinely new — it must differ from the
	// primary (wg-beta), otherwise the template might have expanded only one link and the test passes
	// spuriously. Count that both up lines appear.
	if strings.Count(script, `if ! wg-quick up "wg-beta"; then`) < 1 ||
		strings.Count(script, `if ! wg-quick up "wg-beta-bk1"; then`) < 1 {
		t.Errorf("the startup lines for both the primary and backup interfaces must appear and be distinct, actual script:\n%s", script)
	}
}

// TestRenderClientInstallScript_RobustnessUnaffected verifies that the client install script is
// unaffected by the D52/D53 changes: a client uses a single wg0 interface, no Babel, and no SNAT, so
// it should contain neither the per-peer FAILED_INTERFACES tolerance block nor the iptables-save
// loop-delete. The client retains its original single-interface wg-quick up behavior.
func TestRenderClientInstallScript_RobustnessUnaffected(t *testing.T) {
	node := &model.Node{
		ID:        "client-1",
		Name:      "laptop",
		Role:      "client",
		Platform:  "debian",
		OverlayIP: "10.11.0.9",
	}

	script, err := RenderClientInstallScript(node)
	if err != nil {
		t.Fatalf("render failed: %v", err)
	}

	// The client does not introduce a per-peer failure accumulator.
	if strings.Contains(script, "FAILED_INTERFACES") {
		t.Errorf("client script should not contain the per-peer FAILED_INTERFACES tolerance block")
	}

	// The client has no SNAT, so iptables-save loop-delete should not appear.
	if strings.Contains(script, "iptables-save -t nat") {
		t.Errorf("client script should not contain iptables-save loop-delete (client has no SNAT)")
	}

	// The client still starts with a single wg0 interface (original behavior unchanged).
	if !strings.Contains(script, `wg-quick up "wg0"`) {
		t.Errorf("client script should keep its single-interface wg0 startup")
	}
}
