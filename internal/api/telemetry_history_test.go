package api

import (
	"context"
	"encoding/json"
	"math"
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
	buckets := aggregateHistory(samples, 10*time.Second)
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
	if aggregateHistory(nil, time.Second) != nil || aggregateHistory(samples, 0) != nil {
		t.Error("empty samples or step<=0 must return nil")
	}
}

func TestAggregateHistoryUsesStableEpochAnchor(t *testing.T) {
	step := 30 * time.Second
	samples := []controller.ResourceSample{
		{TS: time.Unix(35, 0).UTC(), Load1: 1},
		{TS: time.Unix(65, 0).UTC(), Load1: 2},
	}
	buckets := aggregateHistory(samples, step)
	if len(buckets) != 2 {
		t.Fatalf("buckets = %d, want 2", len(buckets))
	}
	if want := time.Unix(30, 0).UTC(); !buckets[0].T.Equal(want) {
		t.Errorf("first bucket = %v, want stable epoch bucket %v", buckets[0].T, want)
	}
	if want := time.Unix(60, 0).UTC(); !buckets[1].T.Equal(want) {
		t.Errorf("second bucket = %v, want stable epoch bucket %v", buckets[1].T, want)
	}
	again := aggregateHistory(samples, step)
	if !again[0].T.Equal(buckets[0].T) || !again[1].T.Equal(buckets[1].T) {
		t.Fatalf("repeat aggregation re-phased buckets: first=%v second=%v", buckets, again)
	}
}

func historySamplesWithDeltas(deltas ...time.Duration) []controller.ResourceSample {
	at := time.Unix(0, 0).UTC()
	samples := []controller.ResourceSample{{TS: at}}
	for _, delta := range deltas {
		at = at.Add(delta)
		samples = append(samples, controller.ResourceSample{TS: at})
	}
	return samples
}

func TestEffectiveHistoryStep(t *testing.T) {
	advertised120 := historySamplesWithDeltas(30*time.Second, 30*time.Second)
	advertised120[len(advertised120)-1].IntervalMS = 120_000

	base := time.Unix(1000, 0).UTC()
	mixedKickSamples := []controller.ResourceSample{
		{TS: base.Add(2*time.Minute + time.Second)}, // latest kick has no advertised cadence
		{TS: base, IntervalMS: 60_000},              // older advertised cadence
		{TS: base.Add(time.Second)},                 // enrollment/reconnect kick
		{TS: base.Add(2 * time.Minute), IntervalMS: 120_000},
	}

	overflowingAdvertisement := historySamplesWithDeltas(time.Minute, time.Minute)
	overflowingAdvertisement[len(overflowingAdvertisement)-1].IntervalMS = math.MaxInt64

	tests := []struct {
		name      string
		window    time.Duration
		requested time.Duration
		samples   []controller.ResourceSample
		want      time.Duration
	}{
		{
			name:    "Auto infers jittered default cadence",
			window:  6 * time.Hour,
			samples: historySamplesWithDeltas(29*time.Second+600*time.Millisecond, 30*time.Second+400*time.Millisecond, 29*time.Second+800*time.Millisecond),
			want:    30 * time.Second,
		},
		{name: "Auto prefers advertised 120s cadence", window: time.Hour, samples: advertised120, want: 2 * time.Minute},
		{
			name:    "Auto lower median resists an outage gap",
			window:  6 * time.Hour,
			samples: historySamplesWithDeltas(30*time.Second, 30*time.Second, 30*time.Minute, 30*time.Second),
			want:    30 * time.Second,
		},
		{
			name:    "Auto ignores duplicate timestamps and zero deltas",
			window:  time.Hour,
			samples: historySamplesWithDeltas(0, 30*time.Second, 0, 30*time.Second),
			want:    30 * time.Second,
		},
		{name: "Auto falls back with only one positive delta", window: time.Hour, samples: historySamplesWithDeltas(2 * time.Minute), want: 30 * time.Second},
		{
			name:    "Auto rounds inferred lower median to nearest second",
			window:  time.Hour,
			samples: historySamplesWithDeltas(90*time.Second+600*time.Millisecond, 91*time.Second+600*time.Millisecond),
			want:    91 * time.Second,
		},
		{name: "Auto clamps inferred fast cadence to 30s", window: time.Hour, samples: historySamplesWithDeltas(10*time.Second, 10*time.Second), want: 30 * time.Second},
		{name: "Auto honors bucket cap", window: 24 * time.Hour, want: 86*time.Second + 486486487*time.Nanosecond},
		{name: "Auto chooses newest valid advertisement across mixed kick samples", window: time.Hour, samples: mixedKickSamples, want: 2 * time.Minute},
		{name: "Auto ignores overflowing advertised milliseconds", window: time.Hour, samples: overflowingAdvertisement, want: time.Minute},
		{name: "explicit fast step bypasses advertised cadence", window: 10 * time.Minute, requested: 5 * time.Second, samples: advertised120, want: 5 * time.Second},
		{name: "explicit step is widened only by bucket cap", window: time.Hour, requested: time.Second, samples: advertised120, want: 3*time.Second + 603603604*time.Nanosecond},
		{name: "explicit sub-second step keeps legacy floor", window: time.Minute, requested: time.Nanosecond, samples: advertised120, want: time.Second},
		{name: "explicit coarse step is preserved", window: 6 * time.Hour, requested: 5 * time.Minute, samples: advertised120, want: 5 * time.Minute},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := effectiveHistoryStep(tt.window, tt.requested, tt.samples); got != tt.want {
				t.Errorf("effectiveHistoryStep(%v, %v) = %v, want %v", tt.window, tt.requested, got, tt.want)
			}
		})
	}
}

func TestEffectiveHistoryStepCapsEpochAlignedBuckets(t *testing.T) {
	window := 24 * time.Hour
	step := effectiveHistoryStep(window, 0, nil)
	from := time.Unix(0, 1).UTC() // deliberately not aligned to the epoch bucket grid
	to := from.Add(window)
	start := historyBucketStart(from, step)
	var samples []controller.ResourceSample
	for bucket := start; !bucket.After(to); bucket = bucket.Add(step) {
		ts := bucket
		if ts.Before(from) {
			ts = from
		}
		samples = append(samples, controller.ResourceSample{TS: ts, Load1: 1})
	}
	if got := len(aggregateHistory(samples, step)); got > maxHistoryBuckets {
		t.Fatalf("epoch-aligned buckets = %d, want <= %d (step %v)", got, maxHistoryBuckets, step)
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
	if resp.Step != "1m0s" {
		t.Errorf("explicit 1m step = %q, want 1m0s", resp.Step)
	}

	// Auto resolves after querying samples, so legacy history without an advertised interval uses
	// the robust observed one-minute cadence.
	var auto historyResponse
	autoFrom := base.Add(-6 * time.Hour).Format(time.RFC3339)
	autoURL := histURL + "?node=node-1&from=" + autoFrom + "&to=" + to
	if status := doJSON(t, http.MethodGet, autoURL, testOperatorToken, nil, &auto); status != http.StatusOK {
		t.Fatalf("auto: status %d", status)
	}
	if auto.Step != "1m0s" {
		t.Errorf("six-hour Auto step = %q, want observed cadence 1m0s", auto.Step)
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
	if wide.Step != "1s" {
		t.Errorf("a 1ns explicit step must keep the legacy 1s floor, got echoed step %q", wide.Step)
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
	if off.Step != "1m0s" || len(off.Buckets) != 0 {
		t.Errorf("disabled explicit response = step %q, %d buckets; want 1m0s and none", off.Step, len(off.Buckets))
	}

	// Disabled Auto cannot infer cadence because history is intentionally not queried; retain the
	// safe 30s Auto fallback while still returning an empty successful response.
	var autoOff historyResponse
	if status := doJSON(t, http.MethodGet, autoURL, testOperatorToken, nil, &autoOff); status != http.StatusOK || !autoOff.Disabled {
		t.Errorf("disabled Auto: status %d disabled=%v, want 200 + disabled", status, autoOff.Disabled)
	}
	if autoOff.Step != "30s" || len(autoOff.Buckets) != 0 {
		t.Errorf("disabled Auto response = step %q, %d buckets; want 30s and none", autoOff.Step, len(autoOff.Buckets))
	}
}

func TestAggregateHistory_HugeFiniteValuesDoNotOverflow(t *testing.T) {
	at := time.Unix(0, 0).UTC()
	huge := math.MaxFloat64
	samples := []controller.ResourceSample{
		{TS: at, Load1: huge, Load5: huge, Load15: huge, CpuPct: &huge},
		{TS: at.Add(time.Second), Load1: huge, Load5: -huge, Load15: huge, CpuPct: &huge},
	}
	buckets := aggregateHistory(samples, time.Minute)
	if len(buckets) != 1 {
		t.Fatalf("buckets=%d, want 1", len(buckets))
	}
	values := []float64{
		buckets[0].Load1.Avg, buckets[0].Load5.Avg, buckets[0].Load15.Avg,
		buckets[0].CpuPct.Avg,
	}
	for _, value := range values {
		if math.IsNaN(value) || math.IsInf(value, 0) {
			t.Fatalf("aggregation produced non-finite value: %+v", buckets[0])
		}
	}
	if _, err := json.Marshal(buckets); err != nil {
		t.Fatalf("finite history bucket is not JSON-encodable: %v", err)
	}
}
