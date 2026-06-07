package renderer

import (
	"strings"
	"testing"
)

// spliceTestToken is the placeholder the AgentHeld splice replaces; it mirrors
// render.PrivateKeyPlaceholder but is duplicated here so this renderer test stays independent of the
// render package (no import cycle, no cross-package coupling).
const spliceTestToken = "PRIVATEKEY_PLACEHOLDER"

// TestRenderInstallScriptSigned_SpliceBlockPresent asserts the per-peer install.sh, rendered with
// CustodySplice{Enabled:true}, gains the AgentHeld custody-splice block: it references
// /etc/wireguard/agent.key, matches the placeholder PrivateKey line, fails closed when the key file
// is missing/empty, and runs in Phase 2 AFTER the integrity/checksum verification.
func TestRenderInstallScriptSigned_SpliceBlockPresent(t *testing.T) {
	script, err := RenderInstallScriptSigned(
		sigTestRouterNode(), sigTestPeers(), true, "",
		CustodySplice{Enabled: true, Token: spliceTestToken},
	)
	if err != nil {
		t.Fatalf("render spliced install script: %v", err)
	}

	// References the locally-held key file.
	if !strings.Contains(script, "/etc/wireguard/agent.key") {
		t.Errorf("spliced install.sh must reference /etc/wireguard/agent.key")
	}
	// Matches the exact placeholder PrivateKey line.
	wantLine := "PrivateKey = " + spliceTestToken
	if !strings.Contains(script, wantLine) {
		t.Errorf("spliced install.sh must match the placeholder line %q", wantLine)
	}
	// Reads the key via command substitution (no sed/regex).
	if !strings.Contains(script, `_agent_key="$(cat /etc/wireguard/agent.key)"`) {
		t.Errorf("spliced install.sh must read the key via command substitution")
	}
	// Fail-closed when the key file is missing or empty.
	if !strings.Contains(script, "[ ! -s /etc/wireguard/agent.key ]") {
		t.Errorf("spliced install.sh must fail closed when /etc/wireguard/agent.key is missing or empty")
	}
	if !strings.Contains(script, "expects an agent-held private key") {
		t.Errorf("spliced install.sh must emit a fail-closed error when the agent key is absent")
	}
	// Injection-safe: no sed-based in-place edit of the conf.
	if strings.Contains(script, "sed -i") {
		t.Errorf("spliced install.sh must not use sed -i (injection-safe requirement)")
	}

	// Ordering: the splice must run AFTER the integrity/checksum verify (which already ran over the
	// pristine placeholder bundle) and within Phase 2 (deploy).
	checksumIdx := strings.Index(script, "Verifying file integrity")
	phase2Idx := strings.Index(script, "Phase 2: Deploy Configuration")
	spliceIdx := strings.Index(script, "/etc/wireguard/agent.key")
	if checksumIdx < 0 {
		t.Fatalf("install.sh missing the integrity-verification step")
	}
	if phase2Idx < 0 {
		t.Fatalf("install.sh missing Phase 2 marker")
	}
	if spliceIdx <= checksumIdx {
		t.Errorf("splice (idx=%d) must appear AFTER the integrity check (idx=%d)", spliceIdx, checksumIdx)
	}
	if spliceIdx <= phase2Idx {
		t.Errorf("splice (idx=%d) must appear within Phase 2 (Phase2 idx=%d)", spliceIdx, phase2Idx)
	}
}

// TestRenderInstallScriptSigned_SpliceDisabledByteIdentical asserts CustodySplice{} (disabled)
// produces output byte-identical to the same call with an explicitly-disabled splice, and that the
// disabled output carries no agent.key remnant.
func TestRenderInstallScriptSigned_SpliceDisabledByteIdentical(t *testing.T) {
	node := sigTestRouterNode()
	peers := sigTestPeers()

	zero, err := RenderInstallScriptSigned(node, peers, true, "", CustodySplice{})
	if err != nil {
		t.Fatalf("render zero-splice install script: %v", err)
	}
	explicitDisabled, err := RenderInstallScriptSigned(node, peers, true, "", CustodySplice{Enabled: false, Token: spliceTestToken})
	if err != nil {
		t.Fatalf("render explicit-disabled install script: %v", err)
	}

	if zero != explicitDisabled {
		t.Errorf("CustodySplice{} must be byte-identical to an explicitly-disabled splice")
	}
	// Stronger: a disabled splice (with no signing key) must be byte-identical to the
	// plain renderer — proving the splice param adds nothing on the air-gap path.
	plain, err := RenderInstallScript(node, peers, true)
	if err != nil {
		t.Fatalf("render plain install script: %v", err)
	}
	if zero != plain {
		t.Errorf("disabled-splice, unsigned install.sh must be byte-identical to RenderInstallScript")
	}
	if strings.Contains(zero, "agent.key") {
		t.Errorf("disabled-splice install.sh must contain no agent.key remnant")
	}
}

// TestRenderClientInstallScriptSigned_SpliceBlockPresent mirrors the per-peer assertion for the
// client (single wg0) path.
func TestRenderClientInstallScriptSigned_SpliceBlockPresent(t *testing.T) {
	script, err := RenderClientInstallScriptSigned(
		sigTestClientNode(), "",
		CustodySplice{Enabled: true, Token: spliceTestToken},
	)
	if err != nil {
		t.Fatalf("render spliced client install script: %v", err)
	}

	if !strings.Contains(script, "/etc/wireguard/agent.key") {
		t.Errorf("spliced client install.sh must reference /etc/wireguard/agent.key")
	}
	wantLine := "PrivateKey = " + spliceTestToken
	if !strings.Contains(script, wantLine) {
		t.Errorf("spliced client install.sh must match the placeholder line %q", wantLine)
	}
	if !strings.Contains(script, `_agent_key="$(cat /etc/wireguard/agent.key)"`) {
		t.Errorf("spliced client install.sh must read the key via command substitution")
	}
	if !strings.Contains(script, "[ ! -s /etc/wireguard/agent.key ]") {
		t.Errorf("spliced client install.sh must fail closed when /etc/wireguard/agent.key is missing or empty")
	}
	if !strings.Contains(script, "expects an agent-held private key") {
		t.Errorf("spliced client install.sh must emit a fail-closed error when the agent key is absent")
	}
	if strings.Contains(script, "sed -i") {
		t.Errorf("spliced client install.sh must not use sed -i (injection-safe requirement)")
	}

	checksumIdx := strings.Index(script, "Verifying file integrity")
	spliceIdx := strings.Index(script, "/etc/wireguard/agent.key")
	if checksumIdx < 0 {
		t.Fatalf("client install.sh missing the integrity-verification step")
	}
	if spliceIdx <= checksumIdx {
		t.Errorf("client splice (idx=%d) must appear AFTER the integrity check (idx=%d)", spliceIdx, checksumIdx)
	}
}

// TestRenderClientInstallScriptSigned_SpliceDisabledByteIdentical is the client-path back-compat
// assertion for the disabled splice.
func TestRenderClientInstallScriptSigned_SpliceDisabledByteIdentical(t *testing.T) {
	node := sigTestClientNode()

	zero, err := RenderClientInstallScriptSigned(node, "", CustodySplice{})
	if err != nil {
		t.Fatalf("render zero-splice client install script: %v", err)
	}
	explicitDisabled, err := RenderClientInstallScriptSigned(node, "", CustodySplice{Enabled: false, Token: spliceTestToken})
	if err != nil {
		t.Fatalf("render explicit-disabled client install script: %v", err)
	}

	if zero != explicitDisabled {
		t.Errorf("client CustodySplice{} must be byte-identical to an explicitly-disabled splice")
	}
	// Stronger: a disabled splice (with no signing key) must be byte-identical to the plain
	// client renderer.
	plain, err := RenderClientInstallScript(node)
	if err != nil {
		t.Fatalf("render plain client install script: %v", err)
	}
	if zero != plain {
		t.Errorf("disabled-splice, unsigned client install.sh must be byte-identical to RenderClientInstallScript")
	}
	if strings.Contains(zero, "agent.key") {
		t.Errorf("disabled-splice client install.sh must contain no agent.key remnant")
	}
}
