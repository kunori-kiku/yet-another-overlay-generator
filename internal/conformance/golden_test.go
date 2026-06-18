package conformance

import (
	"crypto/ed25519"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/kunorikiku/yet-another-overlay-generator/internal/bundlesig"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/render"
)

// updateGolden regenerates the committed conformance goldens instead of asserting against
// them. Run `go test ./internal/conformance/ -run TestGolden -update` after an INTENTIONAL
// pipeline change, review the diff, and commit. A plain `go test` (the gate + CI) must NEVER
// touch the goldens — the assertion below proves the corpus is the authoritative frozen
// oracle.
var updateGolden = flag.Bool("update", false, "regenerate the conformance golden manifests from the current Go oracle")

const (
	// corpusDir is plan-3's contract fixture directory, consumed DIRECTLY — there is ONE
	// corpus, the conformance harness does not duplicate it. The relative path reaches across
	// from internal/conformance/ to internal/localcompile/testdata/contract/.
	corpusTopologiesDir = "../localcompile/testdata/contract/topologies"
	corpusSigningKeyPEM = "../localcompile/testdata/contract/signing/test-signing-key.pem"
	goldenDir           = "testdata/golden"
)

// onDiskFixture is the JSON shape of a plan-3 contract fixture — kept byte-identical to the
// `fixture` type in internal/localcompile/contract_golden_test.go so the SAME files load in
// both harnesses. The conformance loader resolves it into the package-level Fixture.
type onDiskFixture struct {
	Name     string          `json:"name"`
	Doc      string          `json:"doc"`
	Custody  string          `json:"custody"`
	Signing  bool            `json:"signing"`
	Topology json.RawMessage `json:"topology"`
}

// loadCorpus reads every *.json fixture under plan-3's contract topologies dir, sorted by
// file name so the corpus order is stable across platforms, and resolves each into a Fixture
// (parsed topology + resolved custody + the throwaway test signer when the fixture opts in).
func loadCorpus(t *testing.T) []Fixture {
	t.Helper()
	entries, err := os.ReadDir(corpusTopologiesDir)
	if err != nil {
		t.Fatalf("read corpus dir %s: %v", corpusTopologiesDir, err)
	}
	var names []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".json") {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)

	fixtures := make([]Fixture, 0, len(names))
	for _, name := range names {
		raw, err := os.ReadFile(filepath.Join(corpusTopologiesDir, name))
		if err != nil {
			t.Fatalf("read fixture %s: %v", name, err)
		}
		var od onDiskFixture
		if err := json.Unmarshal(raw, &od); err != nil {
			t.Fatalf("parse fixture %s: %v", name, err)
		}
		if od.Name == "" {
			od.Name = strings.TrimSuffix(name, ".json")
		}

		fx := Fixture{Name: od.Name}
		if err := json.Unmarshal(od.Topology, &fx.Topology); err != nil {
			t.Fatalf("fixture %s: parse topology: %v", od.Name, err)
		}
		switch od.Custody {
		case "airgap", "":
			fx.Custody = render.AirGap
		case "agentheld":
			fx.Custody = render.AgentHeld
		default:
			t.Fatalf("fixture %s: unknown custody %q", od.Name, od.Custody)
		}
		if od.Signing {
			fx.Signer = loadTestSigner(t)
		}
		fixtures = append(fixtures, fx)
	}
	if len(fixtures) < 8 {
		t.Fatalf("conformance corpus must hold >=8 fixtures, found %d", len(fixtures))
	}
	return fixtures
}

// loadTestSigner builds a bundlesig.ConfigSigner from plan-3's committed THROWAWAY test
// Ed25519 key. It is a test-only key under testdata; it is NOT any production signing key.
func loadTestSigner(t *testing.T) bundlesig.ConfigSigner {
	t.Helper()
	pemBytes, err := os.ReadFile(corpusSigningKeyPEM)
	if err != nil {
		t.Fatalf("read test signing key: %v", err)
	}
	priv, err := bundlesig.LoadPrivateKeyPEM(pemBytes)
	if err != nil {
		t.Fatalf("parse test signing key: %v", err)
	}
	return &bundlesig.Signing{
		Priv:      priv,
		PubKeyPEM: bundlesig.MarshalPublicKeyPEM(priv.Public().(ed25519.PublicKey)),
	}
}

// TestGolden is golden mode: each fixture's canonical Go-oracle manifest must byte-equal the
// committed golden under testdata/golden/<fixture>.json. This is the Phase-0 standalone
// regression net — valuable regardless of the TS port (it would have caught the beta.5–beta.7
// pin-drift) — and the substrate the TS comparator (plan-4) targets. Run with -update to
// (re)freeze after an intentional pipeline change.
func TestGolden(t *testing.T) {
	if err := os.MkdirAll(goldenDir, 0o755); err != nil {
		t.Fatalf("ensure golden dir: %v", err)
	}
	for _, fx := range loadCorpus(t) {
		fx := fx
		t.Run(fx.Name, func(t *testing.T) {
			got := mustManifestBytes(t, fx)

			goldenPath := filepath.Join(goldenDir, fx.Name+".json")
			if *updateGolden {
				if err := os.WriteFile(goldenPath, got, 0o644); err != nil {
					t.Fatalf("write golden %s: %v", goldenPath, err)
				}
				return
			}

			want, err := os.ReadFile(goldenPath)
			if err != nil {
				t.Fatalf("read golden %s (run with -update to generate): %v", goldenPath, err)
			}
			if diff := FirstDivergence(want, got); diff != "" {
				t.Errorf("fixture %s diverges from golden %s:\n%s", fx.Name, goldenPath, diff)
			}
		})
	}
}

// TestGolden_Deterministic proves the oracle is pure: the SAME fixture built twice yields a
// byte-identical manifest. It runs every fixture so the proof covers the signing-on,
// AgentHeld, parallel-link, and fail paths, not just the simplest one. A non-deterministic
// projection (e.g. an unsorted map leaking iteration order) reds here independently of the
// golden freeze.
func TestGolden_Deterministic(t *testing.T) {
	for _, fx := range loadCorpus(t) {
		fx := fx
		t.Run(fx.Name, func(t *testing.T) {
			first := mustManifestBytes(t, fx)
			second := mustManifestBytes(t, fx)
			if diff := FirstDivergence(first, second); diff != "" {
				t.Errorf("fixture %s: two builds produced different manifests (oracle not deterministic):\n%s", fx.Name, diff)
			}
		})
	}
}

// TestConformanceComparator_SelfCompare exercises the conformance-mode comparator that
// plan-4 wires the TS-produced manifest into: a manifest byte-compared against ITSELF must
// report no divergence, and a one-byte mutation must be reported with a first-divergence
// location. This is the self-comparison stub for the cross-language byte assertion (the TS
// half lands in plan-4; here we prove the comparator's contract).
func TestConformanceComparator_SelfCompare(t *testing.T) {
	fixtures := loadCorpus(t)
	ref := mustManifestBytes(t, fixtures[0])

	if diff := FirstDivergence(ref, ref); diff != "" {
		t.Errorf("identical manifests must not diverge, got:\n%s", diff)
	}

	// Mutate one byte in the middle of the document and confirm the comparator catches it.
	mutated := append([]byte(nil), ref...)
	mid := len(mutated) / 2
	if mutated[mid] == 'X' {
		mutated[mid] = 'Y'
	} else {
		mutated[mid] = 'X'
	}
	if diff := FirstDivergence(ref, mutated); diff == "" {
		t.Errorf("comparator missed a one-byte mutation at offset %d", mid)
	}
}

// mustManifestBytes builds a fixture's manifest and serializes it, failing the test on any
// oracle or marshal error.
func mustManifestBytes(t *testing.T, fx Fixture) []byte {
	t.Helper()
	m, err := BuildManifest(fx)
	if err != nil {
		t.Fatalf("build manifest for fixture %s: %v", fx.Name, err)
	}
	b, err := Marshal(m)
	if err != nil {
		t.Fatalf("marshal manifest for fixture %s: %v", fx.Name, err)
	}
	return b
}

// FirstDivergence is the conformance-mode comparator: it byte-compares an expected (Go-oracle)
// manifest against an actual (TS-produced, in plan-4) manifest and returns "" when they are
// identical, or a human-readable first-divergence report — the byte offset, the line/column,
// and a short window of context from each side — otherwise. It is the comparator the
// cross-language gate uses so a TS drift is pinpointed, not just flagged. Kept in the package
// (not the test file) so plan-4's harness can call it.
func FirstDivergence(want, got []byte) string {
	if equalBytes(want, got) {
		return ""
	}
	n := len(want)
	if len(got) < n {
		n = len(got)
	}
	off := 0
	for off < n && want[off] == got[off] {
		off++
	}
	line, col := lineCol(want, off)
	return fmt.Sprintf(
		"first divergence at byte %d (line %d, col %d):\n  want: %q\n  got:  %q",
		off, line, col, window(want, off), window(got, off),
	)
}

func equalBytes(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// lineCol returns the 1-based line and column of byte offset off in b. A divergence at or
// past EOF reports the position of the last byte plus one (so a truncation is located too).
func lineCol(b []byte, off int) (line, col int) {
	line, col = 1, 1
	for i := 0; i < off && i < len(b); i++ {
		if b[i] == '\n' {
			line++
			col = 1
		} else {
			col++
		}
	}
	return line, col
}

// window returns up to 24 bytes of b starting at off, for the divergence report. An offset at
// EOF yields "<EOF>" so a length mismatch is legible.
func window(b []byte, off int) string {
	if off >= len(b) {
		return "<EOF>"
	}
	end := off + 24
	if end > len(b) {
		end = len(b)
	}
	return string(b[off:end])
}
