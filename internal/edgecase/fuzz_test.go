package edgecase

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/kunorikiku/yet-another-overlay-generator/internal/model"
)

// fuzz_test.go — the FuzzCompile target (plan-16 / 3.4, Phase 3) over the real compile+render
// path. It asserts three invariants on the SHIPPED beta.8 behaviour and is a regression lock, not
// a pre-fix demonstration:
//
//  1. No panic. The compile path must answer with a value or an error, never tear down — the
//     latent-panic hunt, complementary to B1's shipped mux-level recover (internal/api/server.go).
//     The Go fuzzing engine fails the test on any panic, so the property is asserted implicitly by
//     simply driving Compile and not recovering.
//  2. Idempotent re-compile. A topology that compiles once compiles to byte-identical rendered
//     output on a second independent compile (deterministic allocation given fixed keys).
//  3. Order-independent. Reordering the edge slice yields byte-identical babeld.conf output — the
//     C1 regression lock on the shipped internal/renderer/babel.go peer sort. LinkKey identity
//     (internal/linkid) is position-independent and babel.go sorts peers by interface name, so a
//     pure edge-permutation must not change a single byte of the rendered babel config.
//
// Canonical babeld byte-equality across the TS/Go conformance boundary is plan-5/1.5's golden;
// this is the in-Go order-stability lock only, with no duplicate oracle.
//
// Bounded-CI command: `go test -run Fuzz -fuzztime=20s ./internal/edgecase/...` (seed corpus +
// short fuzz). Longer local exploration: `go test -fuzz=FuzzCompile -fuzztime=5m ./internal/edgecase/`.

// fuzzCompileTimeout bounds a single compile so a pathological mutated input cannot wedge the
// fuzzing engine. The allocator honors ctx (scan-budget + periodic ctx.Err polling, plan-8); this
// is the outer backstop for the whole pipeline.
const fuzzCompileTimeout = 5 * time.Second

// renderBytes serializes the full rendered surface (babel + wireguard) for the idempotency check.
func renderBytes(t *testing.T, babel, wg map[string]string) []byte {
	t.Helper()
	b, err := json.Marshal(struct {
		Babel map[string]string `json:"babel"`
		WG    map[string]string `json:"wg"`
	}{babel, wg})
	if err != nil {
		t.Fatalf("marshal render surface: %v", err)
	}
	return b
}

func FuzzCompile(f *testing.F) {
	// Seed from the committed corpus: each fixture's topology JSON becomes a fuzz seed, so the
	// engine starts from the known degenerate / charset / stability / dos / bringup shapes and
	// mutates outward from there.
	for _, fx := range Corpus() {
		b, err := json.Marshal(fx.Topo)
		if err != nil {
			f.Fatalf("seed marshal %q: %v", fx.Name, err)
		}
		f.Add(b)
	}

	f.Fuzz(func(t *testing.T, data []byte) {
		var topo model.Topology
		if err := json.Unmarshal(data, &topo); err != nil {
			return // not a topology; nothing to assert (the wire only ever carries valid JSON)
		}

		ctx, cancel := context.WithTimeout(context.Background(), fuzzCompileTimeout)
		defer cancel()

		// (1) No panic — driving Compile without recover IS the assertion (the engine fails on panic).
		res1, err := CompileAndRender(ctx, topo)
		if err != nil {
			return // a coded rejection is a valid outcome for an adversarial input
		}

		// (2) Idempotent re-compile: a second independent compile of the same input renders identically.
		res2, err := CompileAndRender(ctx, topo)
		if err != nil {
			t.Fatalf("compile succeeded then a re-compile of the SAME input failed: %v", err)
		}
		if a, b := renderBytes(t, res1.BabelConfigs, res1.WireGuardConfigs), renderBytes(t, res2.BabelConfigs, res2.WireGuardConfigs); string(a) != string(b) {
			t.Fatalf("non-idempotent compile: re-compiling the same input produced different rendered output")
		}

		// (3) Order-independent babel: permuting the edge slice must not change one byte of babeld.conf.
		shuffled := DeepCopy(topo)
		reverseEdges(shuffled.Edges)
		resR, err := CompileAndRender(ctx, shuffled)
		if err != nil {
			t.Fatalf("compile succeeded for an order, then failed when only the edge slice was reordered: %v", err)
		}
		if a, b := mustBabel(t, res1.BabelConfigs), mustBabel(t, resR.BabelConfigs); string(a) != string(b) {
			t.Fatalf("edge reorder changed babeld.conf output (C1 byte-stability regression)")
		}
	})
}

// reverseEdges reverses an edge slice in place — the simplest non-identity permutation, enough to
// catch any position-dependence in link identity or rendering.
func reverseEdges(edges []model.Edge) {
	for i, j := 0, len(edges)-1; i < j; i, j = i+1, j-1 {
		edges[i], edges[j] = edges[j], edges[i]
	}
}

// mustBabel marshals a babel config map to comparable bytes (sorted keys via encoding/json).
func mustBabel(t *testing.T, m map[string]string) []byte {
	t.Helper()
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatalf("marshal babel: %v", err)
	}
	return b
}
