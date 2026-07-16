package api

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"testing"
	"time"

	"github.com/kunorikiku/yet-another-overlay-generator/internal/controller"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/probemetric"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/telemetrymetric"
)

func apiResourceMetrics(cpu *float64, load1 float64, memTotal, memAvail uint64) map[string]json.RawMessage {
	obj := map[string]any{"load1": load1, "load5": 0.0, "load15": 0.0, "mem_total_kb": memTotal, "mem_available_kb": memAvail}
	if cpu != nil {
		obj["cpu_pct"] = *cpu
	}
	raw, _ := json.Marshal(obj)
	return map[string]json.RawMessage{"resource": raw}
}

func apiProbeMetric(t *testing.T, results ...probemetric.Result) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(results)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestTelemetryHistoryFamilyEncoderCatalogParityAndRuntimeFixtures(t *testing.T) {
	if err := validateTelemetryHistoryFamilyEncoderRegistry(); err != nil {
		t.Fatalf("history family encoder registry validation: %v", err)
	}
	expected := make(map[telemetrymetric.ChartFamily]struct{})
	for _, family := range telemetrymetric.ChartFamilies() {
		if _, duplicate := expected[family]; duplicate {
			t.Errorf("catalog returned chart family %q more than once", family)
		}
		expected[family] = struct{}{}
		if telemetryHistoryFamilyEncoders[family] == nil {
			t.Errorf("charted telemetry family %q has no API encoder", family)
		}
	}
	for family := range telemetryHistoryFamilyEncoders {
		if _, ok := expected[family]; !ok {
			t.Errorf("API history encoder %q is not a cataloged chart family", family)
		}
	}
	if len(telemetryHistoryFamilyEncoders) != len(expected) {
		t.Errorf("history encoder/catalog family cardinality = %d/%d, want exact parity", len(telemetryHistoryFamilyEncoders), len(expected))
	}

	base := time.Unix(1000, 0).UTC()
	cpu, latency := 40.0, 8.0
	wire := probemetric.Result{ID: "fixture", Type: "icmp", Host: "fixture.example"}
	fixture := controller.TelemetryHistorySnapshot{
		Resources: []controller.ResourceSample{{TS: base, CpuPct: &cpu, Load1: 1, Load5: 2, Load15: 3}},
		Probes: []controller.ProbeHistorySample{{
			SeriesID: probemetric.SeriesID(wire), ID: wire.ID, Type: wire.Type, Host: wire.Host,
			Status: probemetric.StatusSuccess, LatencyMS: &latency, CheckedAt: base,
		}},
	}
	for family, encoder := range telemetryHistoryFamilyEncoders {
		var response historyResponse
		encoder(&response, fixture, time.Minute, telemetryHistoryEncodingOptions{includeProbes: true})
		switch family {
		case telemetrymetric.ChartFamilyResource:
			if len(response.Buckets) == 0 || response.Probes != nil {
				t.Errorf("resource family fixture did not reach only buckets: %+v", response)
			}
		case telemetrymetric.ChartFamilyProbe:
			if len(response.Probes) == 0 || response.Buckets != nil {
				t.Errorf("probe family fixture did not reach only probes: %+v", response)
			}
		default:
			t.Errorf("runtime fixture has no assertion for chart family %q", family)
		}
	}

	combined, err := encodeTelemetryHistoryFamilies(
		fixture, time.Minute, telemetryHistoryEncodingOptions{includeProbes: true},
	)
	if err != nil || len(combined.Buckets) == 0 || len(combined.Probes) == 0 {
		t.Fatalf("catalog-driven combined encoding = %+v err=%v", combined, err)
	}
	empty, err := encodeTelemetryHistoryFamilies(
		controller.TelemetryHistorySnapshot{}, time.Minute,
		telemetryHistoryEncodingOptions{includeProbes: true},
	)
	if err != nil || empty.Buckets == nil || empty.Probes == nil || len(empty.Buckets) != 0 || len(empty.Probes) != 0 {
		t.Fatalf("empty family encoding must preserve non-null additive arrays: %+v err=%v", empty, err)
	}
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

func TestAggregateProbeHistorySeparatesTargetsAndPreservesFailures(t *testing.T) {
	base := time.Unix(0, 0).UTC()
	latency10, latency20 := 10.0, 20.0
	resultFor := func(host string, checkedAt time.Time, status string, latency *float64, reason string) controller.ProbeHistorySample {
		wire := probemetric.Result{ID: "service", Type: "tcp", Host: host, Port: 443}
		return controller.ProbeHistorySample{
			SeriesID: probemetric.SeriesID(wire), ID: wire.ID, Type: wire.Type, Host: wire.Host, Port: wire.Port,
			Status: status, LatencyMS: latency, CheckedAt: checkedAt, FailureReason: reason, IntervalMS: 30_000,
		}
	}
	samples := []controller.ProbeHistorySample{
		resultFor("one.example", base.Add(time.Second), probemetric.StatusSuccess, &latency10, ""),
		resultFor("one.example", base.Add(2*time.Second), probemetric.StatusFailure, nil, probemetric.FailureTimeout),
		resultFor("one.example", base.Add(31*time.Second), probemetric.StatusSuccess, &latency20, ""),
		resultFor("two.example", base.Add(3*time.Second), probemetric.StatusFailure, nil, probemetric.FailureConnectionRefused),
	}
	series := aggregateProbeHistory(samples, 30*time.Second)
	if len(series) != 2 || series[0].Host != "one.example" || series[1].Host != "two.example" {
		t.Fatalf("exact targets were not separated/most-recent ordered: %+v", series)
	}
	if len(series[0].Buckets) != 2 {
		t.Fatalf("one.example buckets = %+v, want 2", series[0].Buckets)
	}
	first := series[0].Buckets[0]
	if first.Attempts != 2 || first.Successes != 1 || first.Failures != 1 || first.IntervalMS != 30_000 || first.LatencyMS == nil || first.LatencyMS.Avg != 10 || first.FailureReasons[probemetric.FailureTimeout] != 1 {
		t.Fatalf("success/failure/latency bucket = %+v", first)
	}
	other := series[1].Buckets[0]
	if other.LatencyMS != nil || other.Failures != 1 || other.FailureReasons[probemetric.FailureConnectionRefused] != 1 {
		t.Fatalf("failure must not become zero latency: %+v", other)
	}
}

func TestAggregateProbeHistoryCarriesPerBucketCadenceTransitions(t *testing.T) {
	base := time.Unix(0, 0).UTC()
	latency := 4.0
	wire := probemetric.Result{ID: "cadence", Type: "icmp", Host: "cadence.example"}
	series := aggregateProbeHistory([]controller.ProbeHistorySample{
		{
			SeriesID: probemetric.SeriesID(wire), ID: wire.ID, Type: wire.Type, Host: wire.Host,
			Status: probemetric.StatusSuccess, LatencyMS: &latency,
			CheckedAt: base.Add(time.Second), IntervalMS: 30_000,
		},
		{
			SeriesID: probemetric.SeriesID(wire), ID: wire.ID, Type: wire.Type, Host: wire.Host,
			Status: probemetric.StatusSuccess, LatencyMS: &latency,
			CheckedAt: base.Add(31 * time.Second), IntervalMS: 300_000,
		},
	}, 30*time.Second)
	if len(series) != 1 || len(series[0].Buckets) != 2 {
		t.Fatalf("cadence transition series = %+v", series)
	}
	if series[0].Buckets[0].IntervalMS != 30_000 || series[0].Buckets[1].IntervalMS != 300_000 {
		t.Fatalf("per-bucket cadence was not preserved: %+v", series[0].Buckets)
	}
	if series[0].IntervalMS != 300_000 {
		t.Fatalf("series compatibility cadence = %d, want newest 300000", series[0].IntervalMS)
	}
}

func TestAggregateProbeHistoryBoundsSeriesCardinality(t *testing.T) {
	base := time.Unix(1000, 0).UTC()
	latency := 1.0
	var samples []controller.ProbeHistorySample
	for i := 0; i < maxProbeHistorySeries+3; i++ {
		wire := probemetric.Result{ID: "shared", Type: "icmp", Host: fmt.Sprintf("host-%d.example", i)}
		samples = append(samples, controller.ProbeHistorySample{
			SeriesID: probemetric.SeriesID(wire), ID: wire.ID, Type: wire.Type, Host: wire.Host,
			Status: probemetric.StatusSuccess, LatencyMS: &latency, CheckedAt: base.Add(time.Duration(i) * time.Second),
		})
	}
	series := aggregateProbeHistory(samples, time.Second)
	if len(series) != maxProbeHistorySeries {
		t.Fatalf("series count = %d, want cap %d", len(series), maxProbeHistorySeries)
	}
	if series[0].Host != fmt.Sprintf("host-%d.example", maxProbeHistorySeries+2) {
		t.Fatalf("newest series was not selected first: %+v", series[0])
	}
	for _, item := range series {
		if item.Host == "host-0.example" || item.Host == "host-1.example" || item.Host == "host-2.example" {
			t.Fatalf("old series survived most-recent cap: %q", item.Host)
		}
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

func TestEffectiveHistoryStepEnforcesGlobalResponseBucketBudget(t *testing.T) {
	window := 24 * time.Hour
	from := time.Unix(1, 0).UTC() // deliberately off the epoch grid
	to := from.Add(window)
	latency := 1.0
	history := controller.TelemetryHistorySnapshot{}
	for at := from; !at.After(to); at = at.Add(time.Minute) {
		history.Resources = append(history.Resources, controller.ResourceSample{TS: at, Load1: 1})
		for i := 0; i < maxProbeHistorySeries; i++ {
			wire := probemetric.Result{ID: fmt.Sprintf("probe-%d", i), Type: "icmp", Host: fmt.Sprintf("host-%d.example", i)}
			history.Probes = append(history.Probes, controller.ProbeHistorySample{
				SeriesID: probemetric.SeriesID(wire), ID: wire.ID, Type: wire.Type, Host: wire.Host,
				Status: probemetric.StatusSuccess, LatencyMS: &latency, CheckedAt: at,
			})
		}
	}
	step := effectiveHistoryStepForStreams(
		window, time.Second, history.Resources, telemetryHistoryStreamCount(history),
	)
	response, err := encodeTelemetryHistoryFamilies(
		history, step, telemetryHistoryEncodingOptions{includeProbes: true},
	)
	if err != nil {
		t.Fatal(err)
	}
	total := len(response.Buckets)
	for _, series := range response.Probes {
		total += len(series.Buckets)
	}
	if total > maxHistoryBuckets {
		t.Fatalf("global response buckets = %d, want <= %d (step %v)", total, maxHistoryBuckets, step)
	}
	if len(response.Probes) != maxProbeHistorySeries {
		t.Fatalf("probe series = %d, want %d", len(response.Probes), maxProbeHistorySeries)
	}
}

func TestParseTelemetryHistoryEncodingOptions(t *testing.T) {
	t.Run("omitted preserves all probes", func(t *testing.T) {
		options, err := parseTelemetryHistoryEncodingOptions(url.Values{})
		if err != nil || !options.includeProbes || options.probeSelector != nil {
			t.Fatalf("options = %+v err=%v", options, err)
		}
	})
	t.Run("resource only", func(t *testing.T) {
		options, err := parseTelemetryHistoryEncodingOptions(url.Values{"include_probes": {"false"}})
		if err != nil || options.includeProbes || options.probeSelector != nil {
			t.Fatalf("options = %+v err=%v", options, err)
		}
	})
	t.Run("exact TCP selector", func(t *testing.T) {
		options, err := parseTelemetryHistoryEncodingOptions(url.Values{
			"probe_id": {"main"}, "probe_type": {"tcp"},
			"probe_host": {"db.example"}, "probe_port": {"5432"},
		})
		want := probeHistorySelector{ID: "main", Type: "tcp", Host: "db.example", Port: 5432}
		if err != nil || options.probeSelector == nil || *options.probeSelector != want {
			t.Fatalf("selector = %+v err=%v, want %+v", options.probeSelector, err, want)
		}
	})

	invalid := []url.Values{
		{"include_probes": {"sometimes"}},
		{"include_probes": {""}},
		{"include_probes": {"false"}, "probe_id": {"main"}},
		{"probe_id": {"main"}},
		{"probe_id": {"main"}, "probe_type": {"tcp"}, "probe_host": {"db.example"}},
		{"probe_id": {"main"}, "probe_type": {"icmp"}, "probe_host": {"db.example"}, "probe_port": {"7"}},
	}
	for i, query := range invalid {
		if _, err := parseTelemetryHistoryEncodingOptions(query); err == nil {
			t.Errorf("invalid query %d unexpectedly succeeded: %v", i, query)
		}
	}
}

func TestHandleNodeHistory(t *testing.T) {
	env := newCtlTestEnv(t)
	env.enrollNode(t, "node-1")
	ctx := context.Background()
	base := time.Now().UTC().Truncate(time.Second).Add(-time.Hour)
	cpu := 40.0
	for i := 0; i < 6; i++ {
		at := base.Add(time.Duration(i) * time.Minute)
		metrics := apiResourceMetrics(&cpu, float64(i), 2048, 1024)
		latency := float64(10 + i)
		metrics[telemetrymetric.ProbeSamples.Key] = apiProbeMetric(t,
			probemetric.Result{
				ID: "tcp-main", Type: "tcp", Host: "example.net", Port: 443,
				Status: probemetric.StatusSuccess, LatencyMS: &latency,
				CheckedAt: at.Format(time.RFC3339Nano), IntervalMS: 60_000,
			},
			probemetric.Result{
				ID: "icmp-dns", Type: "icmp", Host: "resolver.example",
				Status: probemetric.StatusSuccess, LatencyMS: &latency,
				CheckedAt: at.Format(time.RFC3339Nano), IntervalMS: 60_000,
			},
		)
		if err := env.store.RecordTelemetry(ctx, testTenant, "node-1", nil, metrics, "v1", at); err != nil {
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
	if len(resp.Probes) != 2 {
		t.Fatalf("additive probe history missing from response: %+v", resp.Probes)
	}

	// Exact selection returns only the requested executable destination while resource history stays
	// present. Omitting the selector above remains all-series compatible for older callers.
	var selected historyResponse
	selectedURL := url + "&probe_id=tcp-main&probe_type=tcp&probe_host=example.net&probe_port=443"
	if status := doJSON(t, http.MethodGet, selectedURL, testOperatorToken, nil, &selected); status != http.StatusOK {
		t.Fatalf("selected history: status %d, want 200", status)
	}
	if len(selected.Buckets) == 0 || len(selected.Probes) != 1 || selected.Probes[0].ID != "tcp-main" || selected.Probes[0].Host != "example.net" {
		t.Fatalf("exact selected history = %+v", selected)
	}

	// Resource-only callers can avoid all probe aggregation/response bytes explicitly.
	var resourcesOnly historyResponse
	if status := doJSON(t, http.MethodGet, url+"&include_probes=false", testOperatorToken, nil, &resourcesOnly); status != http.StatusOK {
		t.Fatalf("resource-only history: status %d, want 200", status)
	}
	if len(resourcesOnly.Buckets) == 0 || resourcesOnly.Probes == nil || len(resourcesOnly.Probes) != 0 {
		t.Fatalf("resource-only history = %+v", resourcesOnly)
	}

	for _, suffix := range []string{
		"&include_probes=maybe",
		"&probe_id=tcp-main",
		"&include_probes=false&probe_id=tcp-main&probe_type=tcp&probe_host=example.net&probe_port=443",
	} {
		if status := doJSON(t, http.MethodGet, url+suffix, testOperatorToken, nil, nil); status != http.StatusBadRequest {
			t.Errorf("invalid selector %q: status %d, want 400", suffix, status)
		}
	}

	// Auto first resolves the observed one-minute cadence, then widens it just enough for the global
	// resource + two-probe response budget.
	var auto historyResponse
	autoFrom := base.Add(-6 * time.Hour).Format(time.RFC3339)
	autoURL := histURL + "?node=node-1&from=" + autoFrom + "&to=" + to
	if status := doJSON(t, http.MethodGet, autoURL, testOperatorToken, nil, &auto); status != http.StatusOK {
		t.Fatalf("auto: status %d", status)
	}
	wantAutoStep := ceilDurationDiv(6*time.Hour+10*time.Minute, int64(maxHistoryBuckets/3-1)).String()
	if auto.Step != wantAutoStep {
		t.Errorf("six-hour all-series Auto step = %q, want globally budgeted %q", auto.Step, wantAutoStep)
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
	wideStep, err := time.ParseDuration(wide.Step)
	if err != nil || wideStep <= time.Second {
		t.Errorf("all-series 1ns step must widen beyond the legacy 1s floor for the global budget, got %q", wide.Step)
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
	if off.Step != "1m0s" || len(off.Buckets) != 0 || len(off.Probes) != 0 {
		t.Errorf("disabled explicit response = step %q, %d resource buckets, %d probes; want 1m0s and none", off.Step, len(off.Buckets), len(off.Probes))
	}

	// Disabled Auto cannot infer cadence because history is intentionally not queried; retain the
	// safe 30s Auto fallback while still returning an empty successful response.
	var autoOff historyResponse
	if status := doJSON(t, http.MethodGet, autoURL, testOperatorToken, nil, &autoOff); status != http.StatusOK || !autoOff.Disabled {
		t.Errorf("disabled Auto: status %d disabled=%v, want 200 + disabled", status, autoOff.Disabled)
	}
	if autoOff.Step != "30s" || len(autoOff.Buckets) != 0 || len(autoOff.Probes) != 0 {
		t.Errorf("disabled Auto response = step %q, %d resource buckets, %d probes; want 30s and none", autoOff.Step, len(autoOff.Buckets), len(autoOff.Probes))
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
