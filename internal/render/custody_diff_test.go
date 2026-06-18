package render

import (
	"context"
	"strings"
	"testing"

	"github.com/kunorikiku/yet-another-overlay-generator/internal/compiler"
)

// TestCustody_AgentHeldEqualsAirGapExceptPrivateKey proves the AgentHeld render is
// identical to the AirGap render for the same topology EXCEPT each node's own
// [Interface] PrivateKey line (the real key vs the placeholder). Both topology
// copies use identical key material, so peer PublicKey lines, transit IPs, ports,
// interface names, and the Babel/sysctl/install/deploy artifacts all match exactly
// — pinning the "air-gap path is frozen; only the private key is withheld"
// guarantee.
func TestCustody_AgentHeldEqualsAirGapExceptPrivateKey(t *testing.T) {
	rk, pk, ck := mustGenerateKey(t), mustGenerateKey(t), mustGenerateKey(t)

	airTopo := custodyTopology(rk, pk, ck, false)
	airKeys, err := GenerateKeys(airTopo, AirGap)
	if err != nil {
		t.Fatalf("GenerateKeys(AirGap): %v", err)
	}
	air, err := compiler.NewCompiler().Compile(context.Background(), airTopo, airKeys)
	if err != nil {
		t.Fatalf("Compile(AirGap): %v", err)
	}
	if err := All(air, airKeys, FetchSettings{}); err != nil {
		t.Fatalf("All(AirGap): %v", err)
	}

	heldTopo := custodyTopology(rk, pk, ck, false)
	heldKeys, err := GenerateKeys(heldTopo, AgentHeld)
	if err != nil {
		t.Fatalf("GenerateKeys(AgentHeld): %v", err)
	}
	held, err := compiler.NewCompiler().Compile(context.Background(), heldTopo, heldKeys)
	if err != nil {
		t.Fatalf("Compile(AgentHeld): %v", err)
	}
	if err := All(held, heldKeys, FetchSettings{}); err != nil {
		t.Fatalf("All(AgentHeld): %v", err)
	}

	// WireGuard configs: same key set, differing only on PrivateKey lines.
	if len(air.WireGuardConfigs) != len(held.WireGuardConfigs) {
		t.Fatalf("WireGuard config set differs: air=%d held=%d", len(air.WireGuardConfigs), len(held.WireGuardConfigs))
	}
	for key, airCfg := range air.WireGuardConfigs {
		heldCfg, ok := held.WireGuardConfigs[key]
		if !ok {
			t.Errorf("AgentHeld missing WG config %q", key)
			continue
		}
		assertDiffersOnlyOnPrivateKey(t, key, airCfg, heldCfg)
	}

	// Babel/sysctl/deploy artifacts must stay byte-identical between the two custody modes.
	assertMapsEqual(t, "BabelConfigs", air.BabelConfigs, held.BabelConfigs)
	assertMapsEqual(t, "SysctlConfigs", air.SysctlConfigs, held.SysctlConfigs)
	assertMapsEqual(t, "DeployScripts", air.DeployScripts, held.DeployScripts)

	// InstallScripts legitimately diverge: the AgentHeld install.sh gains a custody-splice block
	// (it must splice the agent-held private key into the copied conf at install time), while the
	// AirGap install.sh must NOT, so it stays frozen. Pin that asymmetry via the "agent.key" marker
	// instead of byte-equality.
	assertSpliceMarkerAsymmetry(t, air.InstallScripts, held.InstallScripts)
}

// assertSpliceMarkerAsymmetry verifies that, per node, the AirGap install.sh carries no custody
// splice (no /etc/wireguard/agent.key reference) while the AgentHeld install.sh does.
func assertSpliceMarkerAsymmetry(t *testing.T, air, held map[string]string) {
	t.Helper()
	const spliceMarker = "agent.key"
	if len(air) != len(held) {
		t.Errorf("InstallScripts: size differs (air=%d held=%d)", len(air), len(held))
	}
	for k, airScript := range air {
		heldScript, ok := held[k]
		if !ok {
			t.Errorf("InstallScripts: AgentHeld missing %q", k)
			continue
		}
		if strings.Contains(airScript, spliceMarker) {
			t.Errorf("InstallScripts[%q]: AirGap install.sh must NOT contain custody splice marker %q", k, spliceMarker)
		}
		if !strings.Contains(heldScript, spliceMarker) {
			t.Errorf("InstallScripts[%q]: AgentHeld install.sh must contain custody splice marker %q", k, spliceMarker)
		}
	}
}

// assertDiffersOnlyOnPrivateKey fails unless air and held are identical except for
// `PrivateKey =` lines, where held must carry exactly the placeholder.
func assertDiffersOnlyOnPrivateKey(t *testing.T, key, air, held string) {
	t.Helper()
	al := strings.Split(air, "\n")
	hl := strings.Split(held, "\n")
	if len(al) != len(hl) {
		t.Errorf("%s: line count differs (air=%d held=%d)", key, len(al), len(hl))
		return
	}
	for i := range al {
		if al[i] == hl[i] {
			continue
		}
		if strings.HasPrefix(strings.TrimSpace(al[i]), "PrivateKey") &&
			strings.TrimSpace(hl[i]) == "PrivateKey = "+PrivateKeyPlaceholder {
			continue
		}
		t.Errorf("%s line %d differs beyond the PrivateKey placeholder:\n air:  %q\n held: %q", key, i, al[i], hl[i])
	}
}

// assertMapsEqual fails unless two string maps are byte-identical.
func assertMapsEqual(t *testing.T, label string, a, b map[string]string) {
	t.Helper()
	if len(a) != len(b) {
		t.Errorf("%s: size differs (air=%d held=%d)", label, len(a), len(b))
	}
	for k, av := range a {
		bv, ok := b[k]
		if !ok {
			t.Errorf("%s: AgentHeld missing %q", label, k)
			continue
		}
		if av != bv {
			t.Errorf("%s[%q]: AgentHeld output differs from AirGap (must be byte-identical)", label, k)
		}
	}
}
