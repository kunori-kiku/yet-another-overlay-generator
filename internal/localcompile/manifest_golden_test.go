package localcompile

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/kunorikiku/yet-another-overlay-generator/internal/render"
)

// manifest_golden_test.go — the conformance golden + fail/verdict tests, re-homed from the retired
// internal/conformance package (framework-refactor plan-5). The WASM-vs-golden gate
// (scripts/wasm-conformance-gate.mjs) covers the SUCCESS/render path byte-for-byte; the fail/verdict
// two-channel coverage (validator + apierr) has NO wasm-gate counterpart, so it MUST survive as a Go
// test here. These tests reuse the localcompile contract loader's flag (updateGolden), on-disk shape
// (fixture), test signer (loadTestSigner), and corpus/signing paths (topologiesDir, signingKeyPEM).

const (
	// manifestGoldenDir holds the BuildManifest success goldens the WASM gate also compares against
	// (repointed there from internal/conformance/testdata/golden — scripts/wasm-conformance-gate.mjs
	// GOLDEN_DIR). Named distinctly from the contract-golden `goldenDir` const already in this package.
	manifestGoldenDir = "testdata/golden"

	// failCorpusDir holds the conformance-only FAIL fixtures: topologies that compile to an ERROR (so
	// they cannot live in the success-golden corpus) and whose frozen value is the two-channel
	// verdict. They span BOTH channels — a validator failure (the compiler rejects the topology with a
	// plain validation wrap; verdict.validator carries the code, verdict.apierr is empty) and an
	// apierr failure (a coded compile-resource error; verdict.apierr carries the code, verdict.validator
	// is clean). The fail goldens additionally pin healed_edges, computed for every fixture independent
	// of the verdict (the heal-collision fail fixture is the real-repair heal input).
	failCorpusDir = "testdata/fail"
	failGoldenDir = "testdata/golden/fail"
)

// parseFixture loads one on-disk fixture file from dir and resolves it into a Fixture (parsed
// topology + resolved custody + the throwaway test signer when the fixture opts in). It reuses the
// contract loader's on-disk `fixture` shape and `loadTestSigner`, so the JSON shape + the test key
// are parsed in exactly one place. It is shared by the success and fail loaders.
func parseFixture(t *testing.T, dir, name string) Fixture {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		t.Fatalf("read fixture %s: %v", name, err)
	}
	var od fixture
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
	return fx
}

// loadCorpus reads every *.json fixture under the shared contract topologies dir, sorted by file name
// so the corpus order is stable across platforms, and resolves each into a Fixture. It is the SAME
// success corpus the contract-golden test (loadFixtures) and the WASM gate consume — one corpus dir,
// no duplication.
func loadCorpus(t *testing.T) []Fixture {
	t.Helper()
	entries, err := os.ReadDir(topologiesDir)
	if err != nil {
		t.Fatalf("read corpus dir %s: %v", topologiesDir, err)
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
		fixtures = append(fixtures, parseFixture(t, topologiesDir, name))
	}
	if len(fixtures) < 8 {
		t.Fatalf("conformance corpus must hold >=8 fixtures, found %d", len(fixtures))
	}
	return fixtures
}

// loadFailCorpus reads every *.json FAIL fixture under failCorpusDir, sorted by file name. These are
// the conformance-only fixtures that compile to an error; the harness freezes their two-channel
// verdict + healed_edges rather than a full artifact projection. It requires >=2 fixtures so the
// corpus always spans both failure channels.
func loadFailCorpus(t *testing.T) []Fixture {
	t.Helper()
	entries, err := os.ReadDir(failCorpusDir)
	if err != nil {
		t.Fatalf("read fail corpus dir %s: %v", failCorpusDir, err)
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
		fixtures = append(fixtures, parseFixture(t, failCorpusDir, name))
	}
	if len(fixtures) < 2 {
		t.Fatalf("conformance FAIL corpus must hold >=2 fixtures (one per channel), found %d", len(fixtures))
	}
	return fixtures
}

// TestGolden is the Go-side success oracle: each fixture's canonical BuildManifest must byte-equal
// the committed golden under testdata/golden/<fixture>.json — the SAME goldens the permanent
// WASM-vs-golden gate asserts against (belt-and-suspenders with the gate; also keeps BuildManifest
// exercised natively). Run with -update to (re)freeze after an intentional pipeline change.
func TestGolden(t *testing.T) {
	if err := os.MkdirAll(manifestGoldenDir, 0o755); err != nil {
		t.Fatalf("ensure golden dir: %v", err)
	}
	for _, fx := range loadCorpus(t) {
		fx := fx
		t.Run(fx.Name, func(t *testing.T) {
			got := mustManifestBytes(t, fx)

			goldenPath := filepath.Join(manifestGoldenDir, fx.Name+".json")
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

// TestGoldenFail is golden mode over the FAIL corpus: each fail fixture's canonical manifest (its
// two-channel verdict + healed_edges; nil topology/allocations/files/checksums because no artifacts
// exist for a failing compile) must byte-equal the committed golden under
// testdata/golden/fail/<fixture>.json. It exercises the apierr channel (a coded compile-resource
// error) and the validator channel (a validation-wrap rejection) — the two-channel fail machinery the
// success corpus alone leaves uncovered (every success fixture has an empty apierr channel), and the
// coverage the WASM gate does NOT provide (the gate is success-corpus only). Run with -update to
// (re)freeze after an intentional pipeline change.
func TestGoldenFail(t *testing.T) {
	if err := os.MkdirAll(failGoldenDir, 0o755); err != nil {
		t.Fatalf("ensure fail golden dir: %v", err)
	}
	for _, fx := range loadFailCorpus(t) {
		fx := fx
		t.Run(fx.Name, func(t *testing.T) {
			got := mustManifestBytes(t, fx)

			goldenPath := filepath.Join(failGoldenDir, fx.Name+".json")
			if *updateGolden {
				if err := os.WriteFile(goldenPath, got, 0o644); err != nil {
					t.Fatalf("write fail golden %s: %v", goldenPath, err)
				}
				return
			}

			want, err := os.ReadFile(goldenPath)
			if err != nil {
				t.Fatalf("read fail golden %s (run with -update to generate): %v", goldenPath, err)
			}
			if diff := FirstDivergence(want, got); diff != "" {
				t.Errorf("fail fixture %s diverges from golden %s:\n%s", fx.Name, goldenPath, diff)
			}
		})
	}
}

// TestGoldenFail_SpansBothChannels asserts the fail corpus is not degenerate: across the whole fail
// corpus at least one fixture populates the apierr channel (a coded compile-resource error with an
// empty validator-error set) and at least one populates ONLY the validator channel (a validation-wrap
// rejection with an empty apierr channel). This guards against a future edit that silently collapses
// both fail fixtures onto the same channel, which would leave one channel of the two-channel verdict
// untested. It also confirms — on every run, not just at -update time — that the verdict routing in
// BuildManifest sends each failure to the correct channel.
func TestGoldenFail_SpansBothChannels(t *testing.T) {
	var sawValidatorOnly, sawApierr bool
	for _, fx := range loadFailCorpus(t) {
		m, err := BuildManifest(fx)
		if err != nil {
			t.Fatalf("build manifest for fail fixture %s: %v", fx.Name, err)
		}
		// A failing compile produces no artifacts: the success projections must stay nil so the fail
		// golden never accidentally freezes a half-built bundle.
		if m.Topology != nil || m.Allocations != nil || m.Files != nil || m.Checksums != nil {
			t.Errorf("fail fixture %s populated a success projection (topology/allocations/files/checksums must be nil on a failing compile)", fx.Name)
		}
		switch {
		case len(m.Verdict.Apierr) > 0:
			sawApierr = true
			// An apierr-channel fail is a clean compile-resource failure: the topology passed
			// validation, so the validator channel carries no ERROR-level codes (it may carry
			// warnings, which is fine — but this corpus's apierr fixture is fully clean).
		case len(m.Verdict.Validator) > 0:
			sawValidatorOnly = true
		default:
			t.Errorf("fail fixture %s has an empty verdict on BOTH channels; a fail fixture must populate at least one", fx.Name)
		}
	}
	if !sawApierr {
		t.Error("fail corpus has no apierr-channel fixture; it must span BOTH channels (add a coded compile-resource failure, e.g. transit-pool exhaustion)")
	}
	if !sawValidatorOnly {
		t.Error("fail corpus has no validator-only fixture; it must span BOTH channels (add a validation-wrap rejection, e.g. client missing endpoint_host)")
	}
}

// TestGolden_Deterministic proves the oracle is pure: the SAME fixture built twice yields a
// byte-identical manifest. It runs every fixture (success AND fail) so the proof covers the
// signing-on, AgentHeld, parallel-link, and both failure paths, not just the simplest one. A
// non-deterministic projection (e.g. an unsorted map leaking iteration order) reds here independently
// of the golden freeze.
func TestGolden_Deterministic(t *testing.T) {
	all := append(loadCorpus(t), loadFailCorpus(t)...)
	for _, fx := range all {
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

// TestConformanceComparator_SelfCompare exercises the FirstDivergence comparator the WASM gate mirrors
// in JS: a manifest byte-compared against ITSELF must report no divergence, and a one-byte mutation
// must be reported with a first-divergence location.
func TestConformanceComparator_SelfCompare(t *testing.T) {
	fixtures := loadCorpus(t)
	ref := mustManifestBytes(t, fixtures[0])

	if diff := FirstDivergence(ref, ref); diff != "" {
		t.Errorf("identical manifests must not diverge, got:\n%s", diff)
	}

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

// mustManifestBytes builds a fixture's manifest and serializes it, failing the test on any oracle or
// marshal error.
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

// FirstDivergence byte-compares an expected (Go-oracle) manifest against an actual manifest and
// returns "" when they are identical, or a human-readable first-divergence report — the byte offset,
// the line/column, and a short window of context from each side — otherwise. It mirrors the JS
// firstDivergence in scripts/wasm-conformance-gate.mjs so a drift is pinpointed, not just flagged.
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

// lineCol returns the 1-based line and column of byte offset off in b. A divergence at or past EOF
// reports the position of the last byte plus one (so a truncation is located too).
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

// window returns up to 24 bytes of b starting at off, for the divergence report. An offset at EOF
// yields "<EOF>" so a length mismatch is legible.
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
