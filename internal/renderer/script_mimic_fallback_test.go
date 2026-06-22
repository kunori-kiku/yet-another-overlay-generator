package renderer

import (
	"os/exec"
	"strings"
	"testing"

	"github.com/kunorikiku/yet-another-overlay-generator/internal/compiler"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/model"
)

// mimicFallbackPeers returns a single mimic peer with the given RESOLVED per-link fallback policy
// (plan-4 PeerInfo.MimicFallback), reusing the mimicRenderNode fixture.
func mimicFallbackPeers(fallback string) []compiler.PeerInfo {
	return []compiler.PeerInfo{
		{NodeID: "node-2", NodeName: "beta", InterfaceName: "wg-beta", ListenPort: 51820,
			LocalTransitIP: "10.10.0.1", LocalLinkLocal: "fe80::1", Mimic: true, MimicFallback: fallback, MTU: 1408},
	}
}

// requireBashParses fails if the rendered install.sh is not syntactically valid bash (catches any
// if/fi imbalance the policy-branch template surgery might introduce). Skips if bash is unavailable.
func requireBashParses(t *testing.T, script string) {
	t.Helper()
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash not available for -n syntax check")
	}
	cmd := exec.Command(bash, "-n")
	cmd.Stdin = strings.NewReader(script)
	if out, cerr := cmd.CombinedOutput(); cerr != nil {
		t.Fatalf("rendered install.sh is not valid bash: %v\n%s", cerr, out)
	}
}

// TestRenderInstallScript_MimicFallbackUDP: policy=udp ⇒ the kernel-eBPF gate + the unit-start branch
// fall back to plain UDP (not exit 1), write categorized breadcrumbs, and de-provision a half-applied
// filter. The breadcrumb helper + every outcome token + the script's bash syntax are asserted.
func TestRenderInstallScript_MimicFallbackUDP(t *testing.T) {
	script, err := RenderInstallScript(mimicRenderNode(), mimicFallbackPeers("udp"), true)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	required := []string{
		"_mimic_breadcrumb()",                    // helper defined
		model.MimicBreadcrumbPath,                // writes to the agent-read path
		"falling back to plain UDP (policy=udp)", // the udp-gated fallback messaging
		`if systemctl enable --now "mimic@`,      // start is branched, not a hard run
		"systemctl disable --now \"mimic@",       // de-provision the half-applied filter on start-fail
		model.MimicOutcomeActive,
		model.MimicOutcomeKernelTooOld,
		model.MimicOutcomeEbpfLoad,
		model.MimicOutcomeFellBackToUDP,
		model.MimicOutcomeInstallFailed,
	}
	for _, frag := range required {
		if !strings.Contains(script, frag) {
			t.Errorf("policy=udp install.sh missing fragment %q", frag)
		}
	}
	if strings.Contains(script, "(no fallback)") {
		t.Errorf("policy=udp must not render a fail-closed (no fallback) branch")
	}
	requireBashParses(t, script)
}

// TestRenderInstallScript_MimicFallbackNone: policy=none ⇒ the gated branches fail closed (exit 1)
// but STILL write a categorized breadcrumb before aborting; no "(policy=udp)" fallback messaging.
func TestRenderInstallScript_MimicFallbackNone(t *testing.T) {
	script, err := RenderInstallScript(mimicRenderNode(), mimicFallbackPeers("none"), true)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	for _, frag := range []string{
		"_mimic_breadcrumb()",
		model.MimicBreadcrumbPath,
		"(no fallback)",                // the fail-closed messaging
		model.MimicOutcomeKernelTooOld, // breadcrumb-before-abort
	} {
		if !strings.Contains(script, frag) {
			t.Errorf("policy=none install.sh missing fragment %q", frag)
		}
	}
	if strings.Contains(script, "(policy=udp)") {
		t.Errorf("policy=none must NOT render a udp fallback branch")
	}
	requireBashParses(t, script)
}

// TestRenderInstallScript_NonMimic_NoBreadcrumb: a non-mimic node references NO breadcrumb path and
// defines NO breadcrumb helper — the mimic block is absent entirely (back-compat).
func TestRenderInstallScript_NonMimic_NoBreadcrumb(t *testing.T) {
	peers := []compiler.PeerInfo{
		{NodeID: "node-2", NodeName: "beta", InterfaceName: "wg-beta", ListenPort: 51820,
			LocalTransitIP: "10.10.0.1", LocalLinkLocal: "fe80::1", Mimic: false, MTU: 1420},
	}
	script, err := RenderInstallScript(mimicRenderNode(), peers, true)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if strings.Contains(script, model.MimicBreadcrumbPath) {
		t.Errorf("a non-mimic node must not reference the mimic breadcrumb path")
	}
	if strings.Contains(script, "_mimic_breadcrumb") {
		t.Errorf("a non-mimic node must not define the breadcrumb helper")
	}
	requireBashParses(t, script)
}
