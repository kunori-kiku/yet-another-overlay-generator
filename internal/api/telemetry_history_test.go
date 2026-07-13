package api

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/kunorikiku/yet-another-overlay-generator/internal/controller"
)

func apiResourceMetrics(cpu *float64, load1 float64, memTotal, memAvail uint64) map[string]json.RawMessage {
	obj := map[string]any{"load1": load1, "load5": 0.0, "load15": 0.0, "mem_total_kb": memTotal, "mem_available_kb": memAvail}
	if cpu != nil {
		obj["cpu_pct"] = *cpu
	}
	raw, _ := json.Marshal(obj)
	return map[string]json.RawMessage{"resource": raw}
}

func TestAggregateHistory(t *testing.T) {
	from := time.Unix(0, 0).UTC()
	cpu := 50.0
	samples := []controller.ResourceSample{
		{TS: from.Add(0 * time.Second), Load1: 10, CpuPct: &cpu, MemTotalKB: 1000, MemAvailKB: 250}, // bucket 0
		{TS: from.Add(5 * time.Second), Load1: 20, MemTotalKB: 1000, MemAvailKB: 500},               // bucket 0 (no cpu)
		{TS: from.Add(10 * time.Second), Load1: 30, MemTotalKB: 0},                                  // bucket 1 (no mem)
		{TS: from.Add(15 * time.Second), Load1: 40, MemTotalKB: 0},                                  // bucket 1
		{TS: from.Add(30 * time.Second), Load1: 99, MemTotalKB: 0},                                  // bucket 3 (bucket 2 empty → omitted)
	}
	buckets := aggregateHistory(samples, from, 10*time.Second)
	if len(buckets) != 3 {
		t.Fatalf("expected 3 buckets (0,1,3 — 2 omitted as a gap), got %d", len(buckets))
	}
	// bucket 0: load1 avg 15/min 10/max 20; cpu present (only 1 sample had it) avg 50; mem present.
	b0 := buckets[0]
	if !b0.T.Equal(from) || b0.Load1.Avg != 15 || b0.Load1.Min != 10 || b0.Load1.Max != 20 {
		t.Errorf("bucket0 load1 = %+v", b0.Load1)
	}
	if b0.CpuPct == nil || b0.CpuPct.Avg != 50 {
		t.Errorf("bucket0 cpu should be present avg 50, got %+v", b0.CpuPct)
	}
	if b0.MemUsedPct == nil { // (1000-250)/1000=75, (1000-500)/1000=50 → avg 62.5
		t.Errorf("bucket0 mem should be present")
	} else if b0.MemUsedPct.Avg != 62.5 || b0.MemUsedPct.Max != 75 {
		t.Errorf("bucket0 mem = %+v, want avg 62.5 max 75", b0.MemUsedPct)
	}
	// bucket 1: load1 30..40; cpu ABSENT (no sample had it); mem ABSENT (memTotal 0).
	b1 := buckets[1]
	if !b1.T.Equal(from.Add(10*time.Second)) || b1.Load1.Min != 30 || b1.Load1.Max != 40 {
		t.Errorf("bucket1 load1 = %+v (t=%v)", b1.Load1, b1.T)
	}
	if b1.CpuPct != nil || b1.MemUsedPct != nil {
		t.Errorf("bucket1 cpu/mem must be absent (gap), got cpu=%+v mem=%+v", b1.CpuPct, b1.MemUsedPct)
	}
	// bucket 2 (from+20s) is omitted; the third bucket is index 3 (from+30s).
	if !buckets[2].T.Equal(from.Add(30 * time.Second)) {
		t.Errorf("third bucket must be index 3 (from+30s), got t=%v", buckets[2].T)
	}
	// Empty input / non-positive step → nil.
	if aggregateHistory(nil, from, time.Second) != nil || aggregateHistory(samples, from, 0) != nil {
		t.Error("empty samples or step<=0 must return nil")
	}
}

func TestHandleNodeHistory(t *testing.T) {
	env := newCtlTestEnv(t)
	env.enrollNode(t, "node-1")
	ctx := context.Background()
	base := time.Now().UTC().Truncate(time.Second).Add(-time.Hour)
	cpu := 40.0
	for i := 0; i < 6; i++ {
		if err := env.store.RecordTelemetry(ctx, testTenant, "node-1", nil,
			apiResourceMetrics(&cpu, float64(i), 2048, 1024), "v1", base.Add(time.Duration(i)*time.Minute)); err != nil {
			t.Fatal(err)
		}
	}
	from := base.Add(-time.Minute).Format(time.RFC3339)
	to := base.Add(10 * time.Minute).Format(time.RFC3339)
	histURL := env.opURL("node-history")

	// Happy path: buckets returned, not disabled.
	var resp historyResponse
	url := histURL + "?node=node-1&from=" + from + "&to=" + to + "&step=1m"
	if status := doJSON(t, http.MethodGet, url, testOperatorToken, nil, &resp); status != http.StatusOK {
		t.Fatalf("history: status %d, want 200", status)
	}
	if len(resp.Buckets) == 0 || resp.Disabled {
		t.Fatalf("expected non-empty buckets, not disabled; got %+v", resp)
	}

	// Unknown node → 404.
	if status := doJSON(t, http.MethodGet, histURL+"?node=ghost&from="+from+"&to="+to, testOperatorToken, nil, nil); status != http.StatusNotFound {
		t.Errorf("unknown node: status %d, want 404", status)
	}

	// Unauthenticated → 401 (operator-gated).
	if status := doJSON(t, http.MethodGet, url, "", nil, nil); status != http.StatusUnauthorized {
		t.Errorf("no token: status %d, want 401", status)
	}

	// Tiny step over a wide range → the server widens it (echoed step != the requested 1ns).
	var wide historyResponse
	wideURL := histURL + "?node=node-1&from=" + from + "&to=" + to + "&step=1ns"
	if status := doJSON(t, http.MethodGet, wideURL, testOperatorToken, nil, &wide); status != http.StatusOK {
		t.Fatalf("wide: status %d", status)
	}
	if wide.Step == "1ns" {
		t.Errorf("a 1ns step must be clamped/widened, got echoed step %q", wide.Step)
	}

	// History disabled (cap 0) → 200 with disabled=true + empty buckets.
	zero := 0
	if err := env.store.PutSettings(ctx, testTenant, controller.ControllerSettings{TelemetryHistoryCap: &zero}); err != nil {
		t.Fatal(err)
	}
	var off historyResponse
	if status := doJSON(t, http.MethodGet, url, testOperatorToken, nil, &off); status != http.StatusOK || !off.Disabled {
		t.Errorf("disabled: status %d disabled=%v, want 200 + disabled", status, off.Disabled)
	}
}
