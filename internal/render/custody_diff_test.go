package render

import (
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
	air, err := compiler.NewCompiler().Compile(airTopo, airKeys)
	if err != nil {
		t.Fatalf("Compile(AirGap): %v", err)
	}
	if err := All(air, airKeys); err != nil {
		t.Fatalf("All(AirGap): %v", err)
	}

	heldTopo := custodyTopology(rk, pk, ck, false)
	heldKeys, err := GenerateKeys(heldTopo, AgentHeld)
	if err != nil {
		t.Fatalf("GenerateKeys(AgentHeld): %v", err)
	}
	held, err := compiler.NewCompiler().Compile(heldTopo, heldKeys)
	if err != nil {
		t.Fatalf("Compile(AgentHeld): %v", err)
	}
	if err := All(held, heldKeys); err != nil {
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

	// Everything else must be byte-identical between the two custody modes.
	assertMapsEqual(t, "BabelConfigs", air.BabelConfigs, held.BabelConfigs)
	assertMapsEqual(t, "SysctlConfigs", air.SysctlConfigs, held.SysctlConfigs)
	assertMapsEqual(t, "InstallScripts", air.InstallScripts, held.InstallScripts)
	assertMapsEqual(t, "DeployScripts", air.DeployScripts, held.DeployScripts)
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
