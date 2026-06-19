package edgecase

import (
	"context"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"testing"
)

// corpus_test.go — the committed corpus writer + drift guard (plan-16 / 3.4, Phase 2). Run with
// -update to (re)write internal/edgecase/corpus/<name>.json from Corpus(); run without it (the CI
// default) to assert the committed JSON still matches what the generator emits, so the corpus and
// the builders can never silently drift apart. The committed JSON is the load-bearing artifact:
// plan-5/1.5's conformance harness and plan-15/3.6's netns bring-up both read these files and
// select fixtures by the `class` field, so the file shape (full Fixture incl. class, not bare
// Topology) is part of the contract.

var update = flag.Bool("update", false, "rewrite internal/edgecase/corpus/*.json from Corpus()")

const corpusDir = "corpus"

// marshalFixture renders one fixture as the committed-on-disk JSON: indented (reviewable diffs)
// and newline-terminated. A single definition keeps the writer and the verifier byte-for-byte
// in agreement.
func marshalFixture(t *testing.T, f Fixture) []byte {
	t.Helper()
	b, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		t.Fatalf("marshal fixture %q: %v", f.Name, err)
	}
	return append(b, '\n')
}

// TestCorpusWriteOrVerify is the golden-file gate. With -update it writes the corpus; otherwise it
// fails on any drift (missing file, stale bytes, or an orphaned file with no generator entry).
func TestCorpusWriteOrVerify(t *testing.T) {
	corpus := Corpus()

	if *update {
		if err := os.MkdirAll(corpusDir, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", corpusDir, err)
		}
		want := map[string]bool{}
		for _, f := range corpus {
			path := filepath.Join(corpusDir, f.Name+".json")
			if err := os.WriteFile(path, marshalFixture(t, f), 0o644); err != nil {
				t.Fatalf("write %s: %v", path, err)
			}
			want[f.Name+".json"] = true
			t.Logf("wrote %s", path)
		}
		// Remove orphaned corpus files so a renamed/deleted fixture does not leave a stale JSON.
		pruneOrphans(t, want)
		return
	}

	for _, f := range corpus {
		path := filepath.Join(corpusDir, f.Name+".json")
		got, err := os.ReadFile(path)
		if err != nil {
			t.Errorf("read %s: %v (run `go test -run TestCorpusWriteOrVerify -update ./internal/edgecase/`)", path, err)
			continue
		}
		if want := marshalFixture(t, f); string(got) != string(want) {
			t.Errorf("%s is stale; regenerate with -update", path)
		}
	}

	// Guard against orphaned corpus files (a fixture was renamed/removed but its JSON lingered).
	names := map[string]bool{}
	for _, f := range corpus {
		names[f.Name+".json"] = true
	}
	entries, err := os.ReadDir(corpusDir)
	if err != nil {
		t.Fatalf("read dir %s: %v", corpusDir, err)
	}
	for _, e := range entries {
		if filepath.Ext(e.Name()) == ".json" && !names[e.Name()] {
			t.Errorf("orphaned corpus file %s has no Corpus() entry; rerun -update", e.Name())
		}
	}
}

func pruneOrphans(t *testing.T, keep map[string]bool) {
	t.Helper()
	entries, err := os.ReadDir(corpusDir)
	if err != nil {
		t.Fatalf("read dir %s: %v", corpusDir, err)
	}
	for _, e := range entries {
		if filepath.Ext(e.Name()) != ".json" || keep[e.Name()] {
			continue
		}
		path := filepath.Join(corpusDir, e.Name())
		if err := os.Remove(path); err != nil {
			t.Fatalf("remove orphan %s: %v", path, err)
		}
		t.Logf("removed orphan %s", path)
	}
}

// TestCorpusCompilesOrCodedError pins the baseline contract every corpus fixture must satisfy: the
// compile path either succeeds or returns a normal error — it never panics and never returns
// (nil, nil). This is the cheap, deterministic floor; FuzzCompile (fuzz_test.go) extends the
// no-panic property to mutated inputs. A panic here fails the test via the framework's recover.
func TestCorpusCompilesOrCodedError(t *testing.T) {
	for _, f := range Corpus() {
		f := f
		t.Run(f.Name, func(t *testing.T) {
			res, err := Compile(context.Background(), f.Topo)
			switch {
			case err != nil:
				t.Logf("[%s] compile rejected (expected for some adversarial inputs): %v", f.Class, err)
			case res == nil:
				t.Fatalf("[%s] compile returned (nil, nil) — neither a result nor an error", f.Class)
			default:
				t.Logf("[%s] compiled: %d node(s), %d peer-map entr(ies)", f.Class, len(res.Topology.Nodes), len(res.PeerMap))
			}
		})
	}
}
