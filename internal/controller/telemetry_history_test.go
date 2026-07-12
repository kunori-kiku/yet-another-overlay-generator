package controller

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// sampleMetrics builds an agent-shaped metrics["resource"] payload (snake_case wire, mirroring
// hostResource) with an optional cpu_pct.
func sampleMetrics(cpu *float64, load1 float64) map[string]json.RawMessage {
	obj := map[string]any{"load1": load1, "load5": 0.0, "load15": 0.0, "mem_total_kb": 2048, "mem_available_kb": 1024}
	if cpu != nil {
		obj["cpu_pct"] = *cpu
	}
	raw, _ := json.Marshal(obj)
	return map[string]json.RawMessage{"resource": raw}
}

func TestEffectiveHistoryCap(t *testing.T) {
	if c := (ControllerSettings{}).EffectiveHistoryCap(); c != DefaultTelemetryHistoryCap {
		t.Errorf("nil cap → default %d, got %d", DefaultTelemetryHistoryCap, c)
	}
	n := 42
	if c := (ControllerSettings{TelemetryHistoryCap: &n}).EffectiveHistoryCap(); c != 42 {
		t.Errorf("explicit cap 42, got %d", c)
	}
	zero := 0
	if c := (ControllerSettings{TelemetryHistoryCap: &zero}).EffectiveHistoryCap(); c != 0 {
		t.Errorf("explicit 0 (disable) must be honored, got %d", c)
	}
}

func TestResourceSampleFromMetrics(t *testing.T) {
	at := time.Unix(1000, 0).UTC()
	cpu := 42.5
	s, ok := resourceSampleFromMetrics(sampleMetrics(&cpu, 1.5), at)
	if !ok || s.Load1 != 1.5 || s.CpuPct == nil || *s.CpuPct != 42.5 || s.MemTotalKB != 2048 || !s.TS.Equal(at) {
		t.Fatalf("parse = %+v ok=%v", s, ok)
	}
	if _, ok := resourceSampleFromMetrics(map[string]json.RawMessage{"other": json.RawMessage(`1`)}, at); ok {
		t.Error("absent resource key must be ok=false")
	}
	if _, ok := resourceSampleFromMetrics(map[string]json.RawMessage{"resource": json.RawMessage(`{not json`)}, at); ok {
		t.Error("malformed resource must be ok=false")
	}
	if s2, ok := resourceSampleFromMetrics(sampleMetrics(nil, 2.0), at); !ok || s2.CpuPct != nil {
		t.Errorf("cpu-absent sample should be ok with nil CpuPct, got %+v ok=%v", s2, ok)
	}
}

func TestHistory_MemRingCapEvicts(t *testing.T) {
	h := newTelemetryHistory("", 3) // in-memory, cap 3
	base := time.Unix(0, 0).UTC()
	for i := 0; i < 5; i++ {
		h.append("tn", "n1", ResourceSample{TS: base.Add(time.Duration(i) * time.Second), Load1: float64(i)})
	}
	got, err := h.query("tn", "n1", base, base.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 || got[0].Load1 != 2 || got[2].Load1 != 4 {
		t.Fatalf("cap-3 ring should keep the last 3 (2,3,4), got %+v", got)
	}
}

func TestHistory_Disabled(t *testing.T) {
	h := newTelemetryHistory("", 0) // cap 0 = disabled
	h.append("tn", "n1", ResourceSample{TS: time.Unix(1, 0), Load1: 1})
	got, err := h.query("tn", "n1", time.Unix(0, 0), time.Unix(100, 0))
	if err != nil || len(got) != 0 {
		t.Fatalf("disabled history must append nothing + query empty, got %+v err=%v", got, err)
	}
}

func TestHistory_FlushAndQueryRoundTrip(t *testing.T) {
	dir := t.TempDir()
	h := newTelemetryHistory(dir, 100)
	base := time.Unix(1000, 0).UTC()
	for i := 0; i < 10; i++ {
		h.append("tn", "n1", ResourceSample{TS: base.Add(time.Duration(i) * time.Second), Load1: float64(i)})
	}
	if got, _ := h.query("tn", "n1", base, base.Add(time.Hour)); len(got) != 10 {
		t.Fatalf("pre-flush query = %d, want 10 (in-memory buffer)", len(got))
	}
	h.flushOnce()
	if got, _ := h.query("tn", "n1", base, base.Add(time.Hour)); len(got) != 10 {
		t.Fatalf("post-flush query = %d, want 10 (from JSONL)", len(got))
	}
	// A NEW history instance over the SAME dir (a controller restart) loads history from disk.
	h2 := newTelemetryHistory(dir, 100)
	got, err := h2.query("tn", "n1", base, base.Add(time.Hour))
	if err != nil || len(got) != 10 || got[0].Load1 != 0 || got[9].Load1 != 9 {
		t.Fatalf("cross-restart query = %d (%v), want 10 in order", len(got), err)
	}
	if win, _ := h2.query("tn", "n1", base.Add(3*time.Second), base.Add(5*time.Second)); len(win) != 3 {
		t.Fatalf("window [3s,5s] inclusive = %d, want 3", len(win))
	}
}

func TestHistory_CompactOverCap(t *testing.T) {
	dir := t.TempDir()
	h := newTelemetryHistory(dir, 5) // cap 5; the FILE compacts once it passes cap*slack=10 lines
	base := time.Unix(0, 0).UTC()
	ts := 0
	appendN := func(n int) {
		for i := 0; i < n; i++ {
			h.append("tn", "n1", ResourceSample{TS: base.Add(time.Duration(ts) * time.Second), Load1: float64(ts)})
			ts++
		}
	}
	// Flush <=cap samples per batch so the in-memory buffer never front-evicts an unflushed sample
	// (that safety bound is exercised only when the flusher stalls; here we drive FILE compaction).
	appendN(5)
	h.flushOnce() // file 5 lines
	appendN(5)
	h.flushOnce() // file 10 lines (10 > 10 is false → no compaction yet)
	appendN(1)
	h.flushOnce() // file 11 lines → 11 > 10 → compact to the last 5 (samples 6..10)
	if n := countLines(filepath.Join(dir, "tn", "n1.jsonl")); n != 5 {
		t.Fatalf("compacted file should have 5 lines, got %d", n)
	}
	got, _ := h.query("tn", "n1", base, base.Add(time.Hour))
	if len(got) != 5 || got[0].Load1 != 6 || got[4].Load1 != 10 {
		t.Fatalf("post-compact query should be the last 5 (6..10), got %+v", got)
	}
}

func TestHistory_FlushFailureRequeues(t *testing.T) {
	// A FILE where the tenant dir should go makes MkdirAll fail → writeJSONL fails → the samples must be
	// re-queued (never lost, never surfaced to the caller).
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "tn"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	h := newTelemetryHistory(dir, 100)
	h.append("tn", "n1", ResourceSample{TS: time.Unix(1, 0), Load1: 1})
	h.flushOnce() // MkdirAll(dir/tn) fails (dir/tn is a file) → writeJSONL errors → requeue
	// Assert the buffer directly (the same bad path would fail query's read too — this isolates the
	// re-queue behavior): the sample must be back in the buffer, not lost.
	h.mu.Lock()
	n := len(h.nodes["tn"]["n1"].buf)
	h.mu.Unlock()
	if n != 1 {
		t.Fatalf("a failed flush must re-queue the sample (1 buffered), got %d", n)
	}
}

func TestHistory_ConcurrentAppendFlush(t *testing.T) {
	dir := t.TempDir()
	h := newTelemetryHistory(dir, 10000)
	base := time.Unix(0, 0).UTC()
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := 0; i < 500; i++ {
			h.append("tn", "n1", ResourceSample{TS: base.Add(time.Duration(i) * time.Millisecond), Load1: float64(i)})
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < 50; i++ {
			h.flushOnce()
		}
	}()
	wg.Wait()
	h.flushOnce()
	if got, _ := h.query("tn", "n1", base, base.Add(time.Hour)); len(got) != 500 {
		t.Fatalf("concurrent append+flush lost/duplicated samples: got %d, want 500", len(got))
	}
}

// TestMemStore_HistoryWiring proves the store glue: RecordTelemetry appends a sample that
// QueryTelemetryHistory returns, and a cap of 0 via PutSettings disables retention.
func TestMemStore_HistoryWiring(t *testing.T) {
	ctx := context.Background()
	s := NewMemStore()
	if err := s.UpsertNode(ctx, "tn", Node{NodeID: "n1"}); err != nil {
		t.Fatal(err)
	}
	cpu := 33.0
	at := time.Unix(1000, 0).UTC()
	if err := s.RecordTelemetry(ctx, "tn", "n1", nil, sampleMetrics(&cpu, 1.0), "v1", at); err != nil {
		t.Fatal(err)
	}
	got, err := s.QueryTelemetryHistory(ctx, "tn", "n1", time.Unix(0, 0), time.Unix(2000, 0))
	if err != nil || len(got) != 1 || got[0].CpuPct == nil || *got[0].CpuPct != 33.0 {
		t.Fatalf("RecordTelemetry should append a queryable history sample, got %+v err=%v", got, err)
	}
	// Disable via settings (cap 0) → subsequent samples are not retained, and query returns empty.
	zero := 0
	if err := s.PutSettings(ctx, "tn", ControllerSettings{TelemetryHistoryCap: &zero}); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordTelemetry(ctx, "tn", "n1", nil, sampleMetrics(&cpu, 2.0), "v1", at.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if got2, _ := s.QueryTelemetryHistory(ctx, "tn", "n1", time.Unix(0, 0), time.Unix(2000, 0)); len(got2) != 0 {
		t.Fatalf("after cap=0 (disabled) query must be empty, got %d", len(got2))
	}
}

// TestFileStore_HistoryStartClose proves the flusher lifecycle: Start + a RecordTelemetry + Close (final
// drain) leaves the sample on disk, readable by a fresh store.
func TestFileStore_HistoryStartClose(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	fs, err := NewFileStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := fs.UpsertNode(ctx, "tn", Node{NodeID: "n1"}); err != nil {
		t.Fatal(err)
	}
	fs.Start()
	at := time.Unix(5000, 0).UTC()
	if err := fs.RecordTelemetry(ctx, "tn", "n1", nil, sampleMetrics(nil, 4.0), "v1", at); err != nil {
		t.Fatal(err)
	}
	fs.Close() // stops the flusher + final drain to disk

	fs2, err := NewFileStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	got, err := fs2.QueryTelemetryHistory(ctx, "tn", "n1", time.Unix(0, 0), time.Unix(10000, 0))
	if err != nil || len(got) != 1 || got[0].Load1 != 4.0 {
		t.Fatalf("Close should flush the sample durably; fresh store query = %d (%v), want 1", len(got), err)
	}
}
