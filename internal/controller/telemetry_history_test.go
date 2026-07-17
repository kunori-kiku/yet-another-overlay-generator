package controller

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kunorikiku/yet-another-overlay-generator/internal/devicemetric"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/probemetric"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/telemetrymetric"
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

func encodedProbeMetric(t *testing.T, results ...probemetric.Result) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(results)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func encodedDeviceSamplesMetric(t *testing.T, samples ...devicemetric.Sample) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(devicemetric.SamplesMetric{Samples: samples})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// pauseAfterUnlockKV is a test backend that can pause one withLock caller after its storage
// critical section has been released but before withLock returns to the store core. That exposes the
// exact stale-GET/newer-PUT ordering window that existed when history-cap publication happened after
// withLock returned.
type pauseAfterUnlockKV struct {
	*memkv
	pause  chan struct{}
	paused chan struct{}
	resume chan struct{}
}

func (p *pauseAfterUnlockKV) withLock(fn func() error) error {
	p.memkv.mu.Lock()
	err := fn()
	p.memkv.mu.Unlock()
	select {
	case <-p.pause:
		close(p.paused)
		<-p.resume
	default:
	}
	return err
}

func TestTelemetryHistoryProjectorCatalogParity(t *testing.T) {
	if _, exists := telemetryHistoryProjectors[telemetrymetric.DeviceInventory.Key]; exists {
		t.Fatal("categorical device_inventory must remain live-only and have no history projector")
	}
	if err := validateTelemetryHistoryProjectorRegistry(); err != nil {
		t.Fatalf("projector registry validation: %v", err)
	}
	base := time.Date(2026, 7, 16, 10, 0, 0, 0, time.UTC)
	cpu, latency := 25.0, 3.5
	probe := probemetric.Result{
		ID: "fixture", Type: "icmp", Host: "fixture.example", Status: probemetric.StatusSuccess,
		LatencyMS: &latency, CheckedAt: base.Format(time.RFC3339Nano), IntervalMS: 30_000,
	}
	blockID := devicemetric.SeriesID(devicemetric.KindBlockDevice, []byte("fixture-block"))
	filesystemID := devicemetric.SeriesID(devicemetric.KindFilesystem, []byte("fixture-filesystem"))
	gpuID := devicemetric.SeriesID(devicemetric.KindGPU, []byte("fixture-gpu"))
	deviceFixture := encodedDeviceSamplesMetric(t,
		devicemetric.Sample{SeriesID: blockID, Kind: devicemetric.KindBlockDevice, Values: map[devicemetric.NumericKey]float64{
			devicemetric.DiskReadBytesPerSecond:  0,
			devicemetric.DiskWriteBytesPerSecond: 20,
			devicemetric.DiskIOBusyPct:           30,
		}},
		devicemetric.Sample{SeriesID: filesystemID, Kind: devicemetric.KindFilesystem, Values: map[devicemetric.NumericKey]float64{
			devicemetric.DiskFilesystemUsedPct: 40,
		}},
		devicemetric.Sample{SeriesID: gpuID, Kind: devicemetric.KindGPU, Values: map[devicemetric.NumericKey]float64{
			devicemetric.GPUUtilizationPct: 50,
			devicemetric.GPUVRAMUsedPct:    60,
		}},
	)
	fixtures := map[string]json.RawMessage{
		telemetrymetric.Resource.Key:      sampleMetrics(&cpu, 1)[telemetrymetric.Resource.Key],
		telemetrymetric.ProbeSamples.Key:  encodedProbeMetric(t, probe),
		telemetrymetric.ProbeResults.Key:  encodedProbeMetric(t, probe),
		telemetrymetric.DeviceSamples.Key: deviceFixture,
	}
	charted := make(map[string]telemetrymetric.ChartFamily)
	for _, definition := range telemetrymetric.Charted() {
		charted[definition.Key] = definition.ChartFamily
		registration, ok := telemetryHistoryProjectors[definition.Key]
		if !ok || registration.project == nil {
			t.Errorf("charted telemetry metric %q has no controller history projector", definition.Key)
			continue
		}
		if registration.family != definition.ChartFamily {
			t.Errorf("projector %q family = %q, catalog declares %q", definition.Key, registration.family, definition.ChartFamily)
		}
		raw, ok := fixtures[definition.Key]
		if !ok {
			t.Errorf("charted telemetry metric %q has no valid runtime fixture", definition.Key)
			continue
		}
		projection := registration.project(raw, base, 30*time.Second)
		record := telemetryHistoryRecordFromMetrics(map[string]json.RawMessage{definition.Key: raw}, base, 30*time.Second)
		switch definition.ChartFamily {
		case telemetrymetric.ChartFamilyResource:
			if projection.resource == nil || len(projection.probes) != 0 || record.Resource == nil || len(record.ProbeAttempts) != 0 {
				t.Errorf("resource fixture %q did not reach only the resource family: projection=%+v record=%+v", definition.Key, projection, record)
			}
		case telemetrymetric.ChartFamilyProbe:
			if projection.resource != nil || len(projection.probes) == 0 || record.Resource != nil || len(record.ProbeAttempts) == 0 {
				t.Errorf("probe fixture %q did not reach only the probe family: projection=%+v record=%+v", definition.Key, projection, record)
			}
		case telemetrymetric.ChartFamilyDevice:
			if projection.resource != nil || len(projection.probes) != 0 || len(projection.devices) != 3 ||
				record.Resource != nil || len(record.ProbeAttempts) != 0 || len(record.DeviceSamples) != 3 {
				t.Errorf("device fixture %q did not reach only the device family: projection=%+v record=%+v", definition.Key, projection, record)
			}
			seenKeys := make(map[devicemetric.NumericKey]struct{})
			for _, sample := range record.DeviceSamples {
				if sample.TS != base.UTC() {
					t.Errorf("device fixture timestamp = %v, want %v", sample.TS, base.UTC())
				}
				wantSeries, err := devicemetric.HistorySeriesID(sample.Kind, sample.DeviceID)
				if err != nil || sample.SeriesID != wantSeries {
					t.Errorf("device fixture series = %q, %v; want %q", sample.SeriesID, err, wantSeries)
				}
				for key := range sample.Values {
					seenKeys[key] = struct{}{}
				}
			}
			if len(seenKeys) != len(devicemetric.NumericDefinitions()) {
				t.Errorf("device fixture numeric keys = %d, want all %d", len(seenKeys), len(devicemetric.NumericDefinitions()))
			}
		default:
			t.Errorf("runtime projector fixture has no assertion for chart family %q", definition.ChartFamily)
		}
	}
	for key := range telemetryHistoryProjectors {
		if _, ok := charted[key]; !ok {
			t.Errorf("controller history projector %q is not cataloged as charted", key)
		}
	}
	for key := range fixtures {
		if _, ok := charted[key]; !ok {
			t.Errorf("runtime projector fixture %q is not cataloged as charted", key)
		}
	}
	if len(telemetryHistoryProjectors) != len(charted) {
		t.Errorf("history projector/catalog cardinality = %d/%d, want exact parity", len(telemetryHistoryProjectors), len(charted))
	}
	if len(fixtures) != len(charted) {
		t.Errorf("history fixture/catalog cardinality = %d/%d, want exact parity", len(fixtures), len(charted))
	}
	families := make(map[telemetrymetric.ChartFamily]struct{})
	for _, family := range telemetrymetric.ChartFamilies() {
		families[family] = struct{}{}
		if telemetryHistoryFamilyAccumulators[family] == nil {
			t.Errorf("charted family %q has no controller history accumulator", family)
		}
	}
	for family := range telemetryHistoryFamilyAccumulators {
		if _, ok := families[family]; !ok {
			t.Errorf("controller history accumulator %q is not a charted catalog family", family)
		}
	}
	if len(telemetryHistoryFamilyAccumulators) != len(families) {
		t.Errorf("history accumulator/catalog family cardinality = %d/%d, want exact parity", len(telemetryHistoryFamilyAccumulators), len(families))
	}
}

func TestDeviceHistoryProjectionIsStrictAndPreservesZeroAsAReading(t *testing.T) {
	at := time.Date(2026, 7, 17, 1, 2, 3, 0, time.FixedZone("fixture", 10*60*60))
	deviceID := devicemetric.SeriesID(devicemetric.KindBlockDevice, []byte("disk"))
	raw := encodedDeviceSamplesMetric(t, devicemetric.Sample{
		SeriesID: deviceID,
		Kind:     devicemetric.KindBlockDevice,
		Values:   map[devicemetric.NumericKey]float64{devicemetric.DiskReadBytesPerSecond: 0},
	})
	samples := deviceHistorySamplesFromRaw(raw, at)
	if len(samples) != 1 || samples[0].TS != at.UTC() {
		t.Fatalf("device history projection = %+v, want one UTC sample", samples)
	}
	if value, present := samples[0].Values[devicemetric.DiskReadBytesPerSecond]; !present || value != 0 {
		t.Fatalf("valid zero reading = %v/%v, want 0/true", value, present)
	}
	if _, present := samples[0].Values[devicemetric.DiskWriteBytesPerSecond]; present {
		t.Fatal("missing device reading was fabricated instead of remaining a gap")
	}

	for name, malformed := range map[string]json.RawMessage{
		"unknown numeric key": []byte(fmt.Sprintf(`{"samples":[{"series_id":%q,"kind":"block_device","values":{"future":1}}]}`, deviceID)),
		"unknown envelope":    append(append(json.RawMessage{}, raw[:len(raw)-1]...), []byte(`,"future":true}`)...),
		"trailing value":      append(append(json.RawMessage{}, raw...), []byte(` {}`)...),
	} {
		if projected := deviceHistorySamplesFromRaw(malformed, at); len(projected) != 0 {
			t.Errorf("%s projected malformed device samples: %+v", name, projected)
		}
	}

	record := telemetryHistoryRecord{RecordedAt: at.UTC(), DeviceSamples: samples}
	encoded, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &envelope); err != nil {
		t.Fatal(err)
	}
	if len(envelope["recorded_at"]) == 0 || len(envelope["ts"]) != 0 {
		t.Fatalf("device-only JSONL shape = %s, want recorded_at and no legacy resource ts", encoded)
	}
}

func TestDeviceHistoryUsesCollectionCadenceAndDedupesCachedHeartbeats(t *testing.T) {
	h := newTelemetryHistory("", 100, nil)
	base := time.Date(2026, 7, 17, 2, 0, 0, 0, time.UTC)
	deviceID := devicemetric.SeriesID(devicemetric.KindGPU, []byte("cadence-gpu"))
	metric := func(sampledAt time.Time, value float64) map[string]json.RawMessage {
		raw, err := json.Marshal(devicemetric.SamplesMetric{
			Samples: []devicemetric.Sample{{
				SeriesID: deviceID, Kind: devicemetric.KindGPU,
				Values: map[devicemetric.NumericKey]float64{devicemetric.GPUUtilizationPct: value},
			}},
			SampledAt: sampledAt.Format(time.RFC3339Nano),
		})
		if err != nil {
			t.Fatal(err)
		}
		return map[string]json.RawMessage{telemetrymetric.DeviceSamples.Key: raw}
	}

	// A faster heartbeat repeats the same live sample; only the actual collection is retained.
	h.appendMetrics("tn", "n1", metric(base, 10), base.Add(5*time.Second), 10*time.Second)
	h.appendMetrics("tn", "n1", metric(base, 10), base.Add(15*time.Second), 10*time.Second)
	// A completion-triggered beat captures the next collection even though an ordinary upload could
	// be slower than the device cadence.
	h.appendMetrics("tn", "n1", metric(base.Add(30*time.Second), 20), base.Add(31*time.Second), time.Minute)
	seriesID, err := devicemetric.HistorySeriesID(devicemetric.KindGPU, deviceID)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := h.querySnapshotFilteredContext(
		context.Background(), "tn", "n1", base.Add(-time.Second), base.Add(time.Minute),
		TelemetryHistoryQueryOptions{DeviceSeriesID: seriesID},
	)
	if err != nil || len(snapshot.Devices) != 2 {
		t.Fatalf("collection-cadence history = %+v err=%v, want two real observations", snapshot.Devices, err)
	}
	if !snapshot.Devices[0].TS.Equal(base) || !snapshot.Devices[1].TS.Equal(base.Add(30*time.Second)) {
		t.Fatalf("device timestamps follow uploads instead of collections: %+v", snapshot.Devices)
	}

	// An unbounded node clock is not allowed to manufacture a future collection.
	if projected := deviceHistorySamplesFromRaw(
		metric(base.Add(24*time.Hour), 30)[telemetrymetric.DeviceSamples.Key], base,
	); len(projected) != 0 {
		t.Fatalf("future collection timestamp projected: %+v", projected)
	}
}

func TestProbeHistoryProjectsSamplesAndRC9FallbackWithoutDuplicates(t *testing.T) {
	h := newTelemetryHistory("", 100, nil)
	base := time.Date(2026, 7, 16, 10, 0, 0, 0, time.UTC)
	latency := 12.5
	success := probemetric.Result{
		ID: "service", Type: "tcp", Host: "one.example", Port: 443,
		Status: probemetric.StatusSuccess, LatencyMS: &latency,
		CheckedAt: base.Add(-10 * time.Second).Format(time.RFC3339Nano), IntervalMS: 60_000,
	}
	failure := probemetric.Result{
		// Reusing the human id for a changed destination must form a separate exact-target series.
		ID: "service", Type: "tcp", Host: "two.example", Port: 443,
		Status: probemetric.StatusFailure, FailureReason: probemetric.FailureConnectionRefused,
		CheckedAt: base.Add(-5 * time.Second).Format(time.RFC3339Nano), IntervalMS: 30_000,
	}
	pending := probemetric.Result{
		ID: "pending", Type: "icmp", Host: "pending.example", Status: probemetric.StatusPending,
	}
	metrics := map[string]json.RawMessage{
		telemetrymetric.ProbeSamples.Key: encodedProbeMetric(t, success, failure),
		// rc.9 latest repeats success and includes pending. The high-fidelity copy must win and pending
		// never becomes a completed history attempt.
		telemetrymetric.ProbeResults.Key: encodedProbeMetric(t, success, pending),
	}
	h.appendMetrics("tn", "n1", metrics, base, 30*time.Second)
	h.appendMetrics("tn", "n1", metrics, base.Add(30*time.Second), 30*time.Second)

	got, err := h.queryProbes("tn", "n1", base.Add(-time.Minute), base.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("overlapping samples + fallback + repeated snapshot = %d attempts, want 2: %+v", len(got), got)
	}
	if got[0].SeriesID == got[1].SeriesID || got[0].Host != "one.example" || got[1].Host != "two.example" {
		t.Fatalf("exact destination series were spliced or reordered: %+v", got)
	}
	if got[0].IntervalMS != 60_000 || got[0].LatencyMS == nil || *got[0].LatencyMS != latency {
		t.Fatalf("high-fidelity success was not retained: %+v", got[0])
	}
	if got[1].LatencyMS != nil || got[1].FailureReason != probemetric.FailureConnectionRefused {
		t.Fatalf("failure manufactured latency or lost reason: %+v", got[1])
	}
}

func TestProbeHistoryURLMismatchSurvivesRestartWithoutActualStatus(t *testing.T) {
	dir := t.TempDir()
	h := newTelemetryHistory(dir, 100, nil)
	base := time.Date(2026, 7, 17, 10, 0, 0, 0, time.UTC)
	latency := 18.75
	resultFor := func(expectedStatus int) probemetric.Result {
		return probemetric.Result{
			ID: "health", Type: "url", URL: "https://service.example/ready",
			ExpectedStatus: expectedStatus, ActualStatus: 500,
			Status: probemetric.StatusFailure, LatencyMS: &latency,
			CheckedAt:     base.Format(time.RFC3339Nano),
			FailureReason: probemetric.FailureUnexpectedStatus, IntervalMS: 30_000,
		}
	}
	expects204 := resultFor(204)
	expects200 := resultFor(200)
	h.appendMetrics("tn", "n1", map[string]json.RawMessage{
		telemetrymetric.ProbeSamples.Key: encodedProbeMetric(t, expects204, expects200),
		// The latest snapshot repeats one exact attempt; it must not survive as a duplicate.
		telemetrymetric.ProbeResults.Key: encodedProbeMetric(t, expects204),
	}, base, 30*time.Second)
	h.flushOnce()

	path, err := h.nodeFile("tn", "n1")
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), `"actual_status"`) {
		t.Fatalf("categorical actual status leaked into durable history: %s", raw)
	}
	if !strings.Contains(string(raw), `"expected_status":204`) || !strings.Contains(string(raw), `"unexpected_status"`) {
		t.Fatalf("durable URL identity/outcome missing: %s", raw)
	}

	restarted := newTelemetryHistory(dir, 100, nil)
	got, err := restarted.queryProbes("tn", "n1", base.Add(-time.Second), base.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("reloaded URL histories = %d, want two exact expected-status series: %+v", len(got), got)
	}
	seen := map[int]bool{}
	for _, sample := range got {
		seen[sample.ExpectedStatus] = true
		if sample.URL != expects204.URL || sample.Status != probemetric.StatusFailure ||
			sample.FailureReason != probemetric.FailureUnexpectedStatus || sample.LatencyMS == nil || *sample.LatencyMS != latency {
			t.Fatalf("reloaded URL mismatch lost retained chart data: %+v", sample)
		}
	}
	if !seen[200] || !seen[204] || got[0].SeriesID == got[1].SeriesID {
		t.Fatalf("expected status did not separate URL series: %+v", got)
	}
}

func TestProbeHistoryBoundsAttemptTimestampsAndTenant(t *testing.T) {
	h := newTelemetryHistory("", 100, nil)
	base := time.Date(2026, 7, 16, 10, 0, 0, 0, time.UTC)
	latency := 1.0
	results := []probemetric.Result{
		{ID: "old", Type: "icmp", Host: "old.example", Status: probemetric.StatusSuccess, LatencyMS: &latency, CheckedAt: base.Add(-maxTelemetryReplayAge - time.Second).Format(time.RFC3339Nano)},
		{ID: "future", Type: "icmp", Host: "future.example", Status: probemetric.StatusSuccess, LatencyMS: &latency, CheckedAt: base.Add(maxTelemetryFutureSkew + time.Second).Format(time.RFC3339Nano)},
		{ID: "ok", Type: "icmp", Host: "ok.example", Status: probemetric.StatusSuccess, LatencyMS: &latency, CheckedAt: base.Format(time.RFC3339Nano)},
	}
	h.appendMetrics("tenant-a", "node", map[string]json.RawMessage{
		telemetrymetric.ProbeSamples.Key: encodedProbeMetric(t, results...),
	}, base, time.Minute)
	got, err := h.queryProbes("tenant-a", "node", base.Add(-25*time.Hour), base.Add(time.Hour))
	if err != nil || len(got) != 1 || got[0].ID != "ok" {
		t.Fatalf("bounded attempt history = %+v, err=%v; want only ok", got, err)
	}
	other, err := h.queryProbes("tenant-b", "node", base.Add(-time.Hour), base.Add(time.Hour))
	if err != nil || len(other) != 0 {
		t.Fatalf("probe history crossed tenant custody: %+v, err=%v", other, err)
	}
}

func TestProbeHistoryFlushRestartAndLegacyResourceCompatibility(t *testing.T) {
	dir := t.TempDir()
	h := newTelemetryHistory(dir, 100, nil)
	base := time.Date(2026, 7, 16, 10, 0, 0, 0, time.UTC)
	latency := 4.25
	result := probemetric.Result{
		ID: "dns", Type: "icmp", Host: "resolver.example", Status: probemetric.StatusSuccess,
		LatencyMS: &latency, CheckedAt: base.Format(time.RFC3339Nano), IntervalMS: 60_000,
	}
	h.appendMetrics("tn", "n1", map[string]json.RawMessage{
		telemetrymetric.ProbeSamples.Key: encodedProbeMetric(t, result),
	}, base, 30*time.Second)
	legacy := ResourceSample{TS: base.Add(time.Second), Load1: 7}
	h.append("tn", "n1", legacy)
	h.flushOnce()

	h2 := newTelemetryHistory(dir, 100, nil)
	snapshot, err := h2.querySnapshot("tn", "n1", base.Add(-time.Second), base.Add(time.Minute))
	if err != nil || len(snapshot.Probes) != 1 || len(snapshot.Resources) != 1 {
		t.Fatalf("combined cross-restart snapshot = %+v, err=%v; want one probe and one resource", snapshot, err)
	}
	probes, err := h2.queryProbes("tn", "n1", base.Add(-time.Second), base.Add(time.Minute))
	if err != nil || len(probes) != 1 || probes[0].ID != "dns" {
		t.Fatalf("cross-restart probe history = %+v, err=%v", probes, err)
	}
	resources, err := h2.query("tn", "n1", base.Add(-time.Second), base.Add(time.Minute))
	if err != nil || len(resources) != 1 || resources[0].Load1 != 7 {
		t.Fatalf("legacy flat resource line no longer round-trips: %+v, err=%v", resources, err)
	}

	// The volatile deduper intentionally restarts empty. A repeated rc.9 snapshot may be appended once,
	// but query-time exact dedupe must still expose one attempt across disk + the new in-memory tail.
	h2.appendMetrics("tn", "n1", map[string]json.RawMessage{
		telemetrymetric.ProbeResults.Key: encodedProbeMetric(t, result),
	}, base.Add(30*time.Second), 30*time.Second)
	probes, err = h2.queryProbes("tn", "n1", base.Add(-time.Second), base.Add(time.Minute))
	if err != nil || len(probes) != 1 {
		t.Fatalf("post-restart fallback duplicate leaked through query: %+v, err=%v", probes, err)
	}
}

func TestProbeHistoryPersistsFullSampleWindowPlusLatestFallback(t *testing.T) {
	dir := t.TempDir()
	h := newTelemetryHistory(dir, 100, nil)
	base := time.Date(2026, 7, 16, 10, 0, 0, 0, time.UTC)
	latency := 2.0
	samples := make([]probemetric.Result, 0, probemetric.MaxRecentSamples)
	latest := make([]probemetric.Result, 0, 16)
	for probeIndex := 0; probeIndex < 16; probeIndex++ {
		id := fmt.Sprintf("probe-%02d", probeIndex)
		host := fmt.Sprintf("host-%02d.example", probeIndex)
		for attemptIndex := 4; attemptIndex > 0; attemptIndex-- {
			samples = append(samples, probemetric.Result{
				ID: id, Type: "icmp", Host: host, Status: probemetric.StatusSuccess,
				LatencyMS: &latency, CheckedAt: base.Add(-time.Duration(attemptIndex) * time.Second).Format(time.RFC3339Nano),
			})
		}
		latest = append(latest, probemetric.Result{
			ID: id, Type: "icmp", Host: host, Status: probemetric.StatusSuccess,
			LatencyMS: &latency, CheckedAt: base.Format(time.RFC3339Nano),
		})
	}
	h.appendMetrics("tn", "n1", map[string]json.RawMessage{
		telemetrymetric.ProbeSamples.Key: encodedProbeMetric(t, samples...),
		telemetrymetric.ProbeResults.Key: encodedProbeMetric(t, latest...),
	}, base, time.Second)
	h.flushOnce()

	restarted := newTelemetryHistory(dir, 100, nil)
	got, err := restarted.queryProbes("tn", "n1", base.Add(-time.Minute), base.Add(time.Minute))
	want := probemetric.MaxRecentSamples + len(latest)
	if err != nil || len(got) != want {
		t.Fatalf("persisted full probe window + fallback = %d, err=%v; want %d", len(got), err, want)
	}
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
	s, ok := resourceSampleFromMetrics(sampleMetrics(&cpu, 1.5), at, 30*time.Second)
	if !ok || s.Load1 != 1.5 || s.CpuPct == nil || *s.CpuPct != 42.5 || s.MemTotalKB != 2048 || s.IntervalMS != 30000 || !s.TS.Equal(at) {
		t.Fatalf("parse = %+v ok=%v", s, ok)
	}
	if _, ok := resourceSampleFromMetrics(map[string]json.RawMessage{"other": json.RawMessage(`1`)}, at, 0); ok {
		t.Error("absent resource key must be ok=false")
	}
	if _, ok := resourceSampleFromMetrics(map[string]json.RawMessage{"resource": json.RawMessage(`{not json`)}, at, 0); ok {
		t.Error("malformed resource must be ok=false")
	}
	if s2, ok := resourceSampleFromMetrics(sampleMetrics(nil, 2.0), at, 0); !ok || s2.CpuPct != nil {
		t.Errorf("cpu-absent sample should be ok with nil CpuPct, got %+v ok=%v", s2, ok)
	}
}

func TestTelemetryHistoryRecordPreservesLegacyResourceJSON(t *testing.T) {
	cpu := 42.5
	sample := ResourceSample{
		TS: time.Unix(1000, 0).UTC(), IntervalMS: 30_000, CpuPct: &cpu,
		Load1: 1, Load5: 2, Load15: 3, MemTotalKB: 2048, MemAvailKB: 1024,
	}
	legacy, err := json.Marshal(sample)
	if err != nil {
		t.Fatal(err)
	}
	additive, err := json.Marshal(telemetryHistoryRecord{Resource: &sample, RecordedAt: sample.TS})
	if err != nil {
		t.Fatal(err)
	}
	if string(additive) != string(legacy) {
		t.Fatalf("resource-only history line changed:\nlegacy  %s\nadditive %s", legacy, additive)
	}
}

func TestHistory_MemRingCapEvicts(t *testing.T) {
	h := newTelemetryHistory("", 3, nil) // in-memory, cap 3
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
	h := newTelemetryHistory("", 0, nil) // cap 0 = disabled
	h.append("tn", "n1", ResourceSample{TS: time.Unix(1, 0), Load1: 1})
	got, err := h.query("tn", "n1", time.Unix(0, 0), time.Unix(100, 0))
	if err != nil || len(got) != 0 {
		t.Fatalf("disabled history must append nothing + query empty, got %+v err=%v", got, err)
	}
	latency := 1.0
	at := time.Unix(2, 0).UTC()
	h.appendMetrics("tn", "n1", map[string]json.RawMessage{
		telemetrymetric.ProbeResults.Key: encodedProbeMetric(t, probemetric.Result{
			ID: "disabled", Type: "icmp", Host: "disabled.example", Status: probemetric.StatusSuccess,
			LatencyMS: &latency, CheckedAt: at.Format(time.RFC3339Nano),
		}),
	}, at, 30*time.Second)
	probes, err := h.queryProbes("tn", "n1", time.Unix(0, 0), time.Unix(100, 0))
	if err != nil || len(probes) != 0 {
		t.Fatalf("disabled history retained probes: %+v, err=%v", probes, err)
	}
}

// TestHistory_DisabledCapSeededAcrossRestart covers the review fix: a tenant that persisted cap=0
// (history disabled) must NOT get samples written to disk after a controller restart, even though the
// in-memory cap cache starts empty. append buffers optimistically (defaultCap), but the flusher SEEDS
// the cap from settings (capLoader, off the heartbeat path) and drops the samples before any write.
func TestHistory_DisabledCapSeededAcrossRestart(t *testing.T) {
	dir := t.TempDir()
	loaderCalls := 0
	h := newTelemetryHistory(dir, DefaultTelemetryHistoryCap, func(TenantID) int {
		loaderCalls++
		return 0 // operator persisted cap=0 (history disabled)
	})
	h.append("tn", "n1", ResourceSample{TS: time.Unix(1, 0), Load1: 1}) // empty cache → defaultCap → buffered
	h.flushOnce()                                                       // seeds cap=0 → drops → no file
	if loaderCalls == 0 {
		t.Fatal("the flusher must consult the cap loader to seed an unseen tenant")
	}
	if _, err := os.Stat(filepath.Join(dir, "tn", "n1.jsonl")); !os.IsNotExist(err) {
		t.Fatalf("a persisted cap=0 (disabled) tenant must NOT get a history file after restart, err=%v", err)
	}
}

func TestHistory_SeedDoesNotOverwriteConcurrentSettingsCap(t *testing.T) {
	loaded := make(chan struct{})
	resume := make(chan struct{})
	h := newTelemetryHistory(t.TempDir(), DefaultTelemetryHistoryCap, func(TenantID) int {
		close(loaded)
		<-resume
		return 0 // stale persisted value read before the concurrent settings write
	})
	done := make(chan struct{})
	go func() {
		h.ensureSeeded("tn")
		close(done)
	}()
	<-loaded
	h.setCap("tn", 7) // models the newer value published by PutSettings
	close(resume)
	<-done
	if got := h.capFor("tn"); got != 7 {
		t.Fatalf("stale startup seed overwrote concurrent settings cap: got %d, want 7", got)
	}
}

func TestSettingsCapCacheOrdering_PutWinsOverStaleGet(t *testing.T) {
	backend := &pauseAfterUnlockKV{
		memkv:  newMemkv(),
		pause:  make(chan struct{}, 1),
		paused: make(chan struct{}),
		resume: make(chan struct{}),
	}
	history := newTelemetryHistory("", DefaultTelemetryHistoryCap, nil)
	core := newStoreCore(backend, history)
	ctx := context.Background()
	zero := 0
	if err := core.PutSettings(ctx, "tn", ControllerSettings{TelemetryHistoryCap: &zero}); err != nil {
		t.Fatal(err)
	}

	// Pause the GET after it has read the old cap and released the backend lock. A concurrent PUT can
	// now complete. The GET must not publish its stale value after the PUT's newer value.
	backend.pause <- struct{}{}
	getDone := make(chan error, 1)
	go func() {
		_, err := core.GetSettings(ctx, "tn")
		getDone <- err
	}()
	<-backend.paused
	newCap := 7
	if err := core.PutSettings(ctx, "tn", ControllerSettings{TelemetryHistoryCap: &newCap}); err != nil {
		t.Fatal(err)
	}
	close(backend.resume)
	if err := <-getDone; err != nil {
		t.Fatal(err)
	}
	if got := history.capFor("tn"); got != newCap {
		t.Fatalf("stale GetSettings overwrote newer PutSettings cap: got %d, want %d", got, newCap)
	}
}

func TestHistory_FlushAndQueryRoundTrip(t *testing.T) {
	dir := t.TempDir()
	h := newTelemetryHistory(dir, 100, nil)
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
	h2 := newTelemetryHistory(dir, 100, nil)
	got, err := h2.query("tn", "n1", base, base.Add(time.Hour))
	if err != nil || len(got) != 10 || got[0].Load1 != 0 || got[9].Load1 != 9 {
		t.Fatalf("cross-restart query = %d (%v), want 10 in order", len(got), err)
	}
	if win, _ := h2.query("tn", "n1", base.Add(3*time.Second), base.Add(5*time.Second)); len(win) != 3 {
		t.Fatalf("window [3s,5s] inclusive = %d, want 3", len(win))
	}
}

func TestHistory_QuerySeesInflightFlushExactlyOnce(t *testing.T) {
	dir := t.TempDir()
	h := newTelemetryHistory(dir, 100, nil)
	base := time.Unix(1000, 0).UTC()
	// Two distinct observations deliberately share a timestamp. The merge must remove only the
	// disk/in-flight copies, not collapse different values merely because their times match.
	h.append("tn", "n1", ResourceSample{TS: base, Load1: 1})
	h.append("tn", "n1", ResourceSample{TS: base, Load1: 2})

	writeStarted := make(chan struct{})
	allowWrite := make(chan struct{})
	written := make(chan struct{})
	allowReturn := make(chan struct{})
	flushDone := make(chan struct{})
	var allowWriteOnce sync.Once
	var allowReturnOnce sync.Once
	releaseWrite := func() { allowWriteOnce.Do(func() { close(allowWrite) }) }
	releaseReturn := func() { allowReturnOnce.Do(func() { close(allowReturn) }) }
	defer releaseWrite()
	defer releaseReturn()

	writeBatch := h.writeBatch
	h.writeBatch = func(tn TenantID, nodeID string, samples []telemetryHistoryRecord, cap int) error {
		close(writeStarted)
		<-allowWrite
		err := writeBatch(tn, nodeID, samples, cap)
		close(written)
		<-allowReturn
		return err
	}
	go func() {
		h.flushOnce()
		close(flushDone)
	}()

	select {
	case <-writeStarted:
	case <-time.After(3 * time.Second):
		t.Fatal("flush did not reach the injected writer")
	}
	assertLoads := func(stage string) {
		t.Helper()
		got, err := h.query("tn", "n1", base.Add(-time.Second), base.Add(time.Second))
		if err != nil || len(got) != 2 || got[0].Load1 != 1 || got[1].Load1 != 2 {
			t.Fatalf("%s query = %+v, err=%v; want the two ordered observations exactly once", stage, got, err)
		}
	}

	// The buffer has been drained and disk is still empty: visibility comes from inflight.
	assertLoads("before write")
	releaseWrite()
	select {
	case <-written:
	case <-time.After(3 * time.Second):
		t.Fatal("flush did not finish the disk append")
	}
	// Disk now contains the batch while inflight intentionally remains set: the merge deduplicates it.
	assertLoads("after write before completion")
	releaseReturn()
	select {
	case <-flushDone:
	case <-time.After(3 * time.Second):
		t.Fatal("flush did not complete")
	}
	assertLoads("after completion")
}

func TestHistory_CompactOverCap(t *testing.T) {
	dir := t.TempDir()
	h := newTelemetryHistory(dir, 5, nil) // cap 5; the FILE compacts once it passes cap*slack=10 lines
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
	h := newTelemetryHistory(dir, 100, nil)
	h.append("tn", "n1", ResourceSample{TS: time.Unix(1, 0), Load1: 1})
	h.flushOnce() // MkdirAll(dir/tn) fails (dir/tn is a file) → writeJSONL errors → requeue
	// Assert the buffer directly (the same bad path would fail query's read too — this isolates the
	// re-queue behavior): the sample must be back in the buffer, not lost.
	h.mu.Lock()
	e := h.nodes["tn"]["n1"]
	n := len(e.buf)
	inflight := len(e.inflight)
	h.mu.Unlock()
	if n != 1 || inflight != 0 {
		t.Fatalf("a failed flush must re-queue the sample (1 buffered, 0 in-flight), got %d/%d", n, inflight)
	}
}

func TestHistory_ConcurrentAppendFlush(t *testing.T) {
	dir := t.TempDir()
	h := newTelemetryHistory(dir, 10000, nil)
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
	latency := 8.5
	metrics := sampleMetrics(&cpu, 1.0)
	metrics[telemetrymetric.ProbeSamples.Key] = encodedProbeMetric(t, probemetric.Result{
		ID: "tcp-main", Type: "tcp", Host: "example.net", Port: 443,
		Status: probemetric.StatusSuccess, LatencyMS: &latency, CheckedAt: at.Format(time.RFC3339Nano),
	})
	if err := s.RecordTelemetry(ctx, "tn", "n1", nil, metrics, "v1", at); err != nil {
		t.Fatal(err)
	}
	snapshot, err := s.QueryTelemetryHistorySnapshot(ctx, "tn", "n1", time.Unix(0, 0), time.Unix(2000, 0))
	if err != nil || len(snapshot.Resources) != 1 || len(snapshot.Probes) != 1 || snapshot.Probes[0].ID != "tcp-main" {
		t.Fatalf("combined history snapshot = %+v err=%v", snapshot, err)
	}
	*snapshot.Resources[0].CpuPct = 0
	*snapshot.Probes[0].LatencyMS = 0
	independent, err := s.QueryTelemetryHistorySnapshot(ctx, "tn", "n1", time.Unix(0, 0), time.Unix(2000, 0))
	if err != nil || *independent.Resources[0].CpuPct != cpu || *independent.Probes[0].LatencyMS != latency {
		t.Fatalf("query result mutation leaked into retained history: %+v err=%v", independent, err)
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

func TestHistory_LogicalCapAppliesBeforePhysicalCompaction(t *testing.T) {
	dir := t.TempDir()
	h := newTelemetryHistory(dir, 5, nil)
	base := time.Unix(10_000, 0).UTC()
	for batch := 0; batch < 2; batch++ {
		for i := 0; i < 5; i++ {
			value := batch*5 + i
			h.append("tn", "n1", ResourceSample{TS: base.Add(time.Duration(value) * time.Second), Load1: float64(value)})
		}
		h.flushOnce()
	}
	p := filepath.Join(dir, "tn", "n1.jsonl")
	if got := countLines(p); got != 10 {
		t.Fatalf("physical slack file lines = %d, want 10 before the >cap*2 compaction trigger", got)
	}
	got, err := h.query("tn", "n1", base.Add(-time.Second), base.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 5 || got[0].Load1 != 5 || got[4].Load1 != 9 {
		t.Fatalf("logical cap query = %+v, want newest five values 5..9", got)
	}
}

func TestHistory_CapReductionIsImmediateForMemAndOfflineFile(t *testing.T) {
	base := time.Unix(20_000, 0).UTC()
	for _, tc := range []struct {
		name string
		dir  func(*testing.T) string
	}{
		{name: "mem", dir: func(*testing.T) string { return "" }},
		{name: "file", dir: func(t *testing.T) string { return t.TempDir() }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := tc.dir(t)
			h := newTelemetryHistory(dir, 10, nil)
			for i := 0; i < 10; i++ {
				h.append("tn", "n1", ResourceSample{TS: base.Add(time.Duration(i) * time.Second), Load1: float64(i)})
			}
			if dir != "" {
				h.flushOnce()
			}
			h.setCap("tn", 3)
			got, err := h.query("tn", "n1", base.Add(-time.Second), base.Add(time.Hour))
			if err != nil {
				t.Fatal(err)
			}
			if len(got) != 3 || got[0].Load1 != 7 || got[2].Load1 != 9 {
				t.Fatalf("reduced-cap query = %+v, want newest three values 7..9", got)
			}
			if dir != "" {
				if physical := countLines(filepath.Join(dir, "tn", "n1.jsonl")); physical != 10 {
					t.Fatalf("offline file was unexpectedly rewritten synchronously: lines=%d, want 10", physical)
				}
			}
		})
	}
}

func TestHistory_ByteCeilingWinsOverRecordTarget(t *testing.T) {
	dir := t.TempDir()
	h := newTelemetryHistory(dir, 100, nil)
	base := time.Unix(30_000, 0).UTC()
	records := make([]telemetryHistoryRecord, 10)
	for i := range records {
		sample := ResourceSample{TS: base.Add(time.Duration(i) * time.Second), Load1: float64(i)}
		records[i] = telemetryHistoryRecord{Resource: &sample, RecordedAt: sample.TS}
	}
	var threeLineBudget int64
	for _, record := range records[7:] {
		size, err := telemetryHistoryRecordEncodedBytes(record)
		if err != nil {
			t.Fatal(err)
		}
		threeLineBudget += size
	}
	for i := 0; i < 5; i++ {
		h.appendRecord("tn", "n1", records[i])
	}
	h.flushOnce()
	p := filepath.Join(dir, "tn", "n1.jsonl")
	if info, err := os.Stat(p); err != nil || info.Size() <= threeLineBudget {
		t.Fatalf("five-line setup size = %v err=%v, want larger than three-line budget %d", func() int64 {
			if info == nil {
				return 0
			}
			return info.Size()
		}(), err, threeLineBudget)
	}
	h.maxFileBytes = threeLineBudget
	h.compactTargetBytes = threeLineBudget
	for i := 5; i < 10; i++ {
		h.appendRecord("tn", "n1", records[i])
	}
	h.flushOnce()
	info, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() > threeLineBudget {
		t.Fatalf("history file size = %d, exceeds hard test ceiling %d", info.Size(), threeLineBudget)
	}
	if lines := countLines(p); lines != 3 {
		t.Fatalf("byte-bounded file lines = %d, want newest three", lines)
	}
	got, err := h.query("tn", "n1", base.Add(-time.Second), base.Add(time.Hour))
	if err != nil || len(got) != 3 || got[0].Load1 != 7 || got[2].Load1 != 9 {
		t.Fatalf("byte-bounded query = %+v err=%v, want values 7..9", got, err)
	}
}

func TestHistory_TornTailIsToleratedThenRepairedBeforeAppend(t *testing.T) {
	dir := t.TempDir()
	h := newTelemetryHistory(dir, 100, nil)
	base := time.Unix(40_000, 0).UTC()
	h.append("tn", "n1", ResourceSample{TS: base, Load1: 1})
	h.flushOnce()
	p := filepath.Join(dir, "tn", "n1.jsonl")
	f, err := os.OpenFile(p, os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write([]byte(`{"ts":`)); err != nil {
		_ = f.Close()
		t.Fatal(err)
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	before, err := h.query("tn", "n1", base.Add(-time.Second), base.Add(time.Hour))
	if err != nil || len(before) != 1 || before[0].Load1 != 1 {
		t.Fatalf("query with torn tail = %+v err=%v, want intact prior line only", before, err)
	}
	h.append("tn", "n1", ResourceSample{TS: base.Add(time.Second), Load1: 2})
	h.flushOnce()
	after, err := h.query("tn", "n1", base.Add(-time.Second), base.Add(time.Hour))
	if err != nil || len(after) != 2 || after[0].Load1 != 1 || after[1].Load1 != 2 {
		t.Fatalf("query after torn-tail repair = %+v err=%v, want intact values 1,2", after, err)
	}
	data, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if len(data) == 0 || data[len(data)-1] != '\n' {
		t.Fatalf("repaired JSONL still has a torn tail: %q", data)
	}
	if lines := countLines(p); lines != 2 {
		t.Fatalf("repaired JSONL lines = %d, want 2", lines)
	}
}

func TestHistory_FileVolatileByteBudgetAndMemStoreParity(t *testing.T) {
	base := time.Unix(50_000, 0).UTC()
	payload := strings.Repeat("x", 300<<10)
	appendLarge := func(h *telemetryHistory) {
		for i := 0; i < 30; i++ {
			at := base.Add(time.Duration(i) * time.Second)
			resource := ResourceSample{TS: at, Load1: float64(i)}
			h.appendRecord("tn", "n1", telemetryHistoryRecord{
				Resource: &resource, RecordedAt: at,
				ProbeAttempts: []ProbeHistorySample{{
					SeriesID: fmt.Sprintf("series-%d", i), ID: fmt.Sprintf("probe-%d", i), Type: "icmp", Host: "example.net",
					Status: probemetric.StatusFailure, CheckedAt: at, FailureReason: payload,
				}},
			})
		}
	}

	fileHistory := newTelemetryHistory(t.TempDir(), 100, nil)
	appendLarge(fileHistory)
	fileHistory.mu.Lock()
	fileEntry := fileHistory.nodes["tn"]["n1"]
	fileLen, fileBytes := len(fileEntry.buf), fileEntry.bufBytes
	fileHistory.mu.Unlock()
	if fileBytes > maxFileTelemetryHistoryVolatileBytes || fileLen >= 30 {
		t.Fatalf("FileStore volatile tail = %d records/%d bytes, want oldest eviction below %d bytes", fileLen, fileBytes, maxFileTelemetryHistoryVolatileBytes)
	}
	fileHistory.writeBatch = func(TenantID, string, []telemetryHistoryRecord, int) error { return errors.New("disk unavailable") }
	fileHistory.flushOnce()
	fileHistory.mu.Lock()
	fileEntry = fileHistory.nodes["tn"]["n1"]
	fileLen, fileBytes = len(fileEntry.buf), fileEntry.bufBytes
	inflight := len(fileEntry.inflight)
	fileHistory.mu.Unlock()
	if fileBytes > maxFileTelemetryHistoryVolatileBytes || inflight != 0 {
		t.Fatalf("requeued FileStore tail = %d records/%d bytes, inflight=%d; want bounded buffer and no inflight", fileLen, fileBytes, inflight)
	}

	memHistory := newTelemetryHistory("", 100, nil)
	appendLarge(memHistory)
	memHistory.mu.Lock()
	memEntry := memHistory.nodes["tn"]["n1"]
	memLen, memBytes := len(memEntry.buf), memEntry.bufBytes
	memHistory.mu.Unlock()
	if memLen != 30 || memBytes <= maxFileTelemetryHistoryVolatileBytes || memBytes > maxMemTelemetryHistoryVolatileBytes {
		t.Fatalf("MemStore history = %d records/%d bytes, want all 30 above FileStore budget and below MemStore ceiling", memLen, memBytes)
	}
}

func TestHistory_FilteredSnapshotPushesExactSeriesIntoDiskScan(t *testing.T) {
	dir := t.TempDir()
	h := newTelemetryHistory(dir, 100, nil)
	base := time.Unix(60_000, 0).UTC()
	probeSeriesID := ""
	selectedDeviceID := devicemetric.SeriesID(devicemetric.KindGPU, []byte("selected-gpu"))
	selectedDeviceSeriesID, err := devicemetric.HistorySeriesID(devicemetric.KindGPU, selectedDeviceID)
	if err != nil {
		t.Fatal(err)
	}
	otherDeviceID := devicemetric.SeriesID(devicemetric.KindGPU, []byte("other-gpu"))
	otherDeviceSeriesID, err := devicemetric.HistorySeriesID(devicemetric.KindGPU, otherDeviceID)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		at := base.Add(time.Duration(i) * time.Second)
		resource := ResourceSample{TS: at, Load1: float64(i)}
		attempts := make([]ProbeHistorySample, 0, 2)
		for _, spec := range []struct{ id, host string }{{"selected", "selected.example"}, {"other", "other.example"}} {
			latency := float64(i + 1)
			result := probemetric.Result{
				ID: spec.id, Type: "icmp", Host: spec.host, Status: probemetric.StatusSuccess,
				LatencyMS: &latency, CheckedAt: at.Format(time.RFC3339Nano), IntervalMS: 30_000,
			}
			id := probemetric.SeriesID(result)
			if spec.id == "selected" {
				probeSeriesID = id
			}
			attempts = append(attempts, ProbeHistorySample{
				SeriesID: id, ID: result.ID, Type: result.Type, Host: result.Host, Status: result.Status,
				LatencyMS: &latency, CheckedAt: at, IntervalMS: result.IntervalMS,
			})
		}
		h.appendRecord("tn", "n1", telemetryHistoryRecord{
			Resource: &resource, RecordedAt: at, ProbeAttempts: attempts,
			DeviceSamples: []DeviceHistorySample{
				{SeriesID: selectedDeviceSeriesID, DeviceID: selectedDeviceID, Kind: devicemetric.KindGPU, TS: at, Values: map[devicemetric.NumericKey]float64{devicemetric.GPUUtilizationPct: float64(i)}},
				{SeriesID: otherDeviceSeriesID, DeviceID: otherDeviceID, Kind: devicemetric.KindGPU, TS: at, Values: map[devicemetric.NumericKey]float64{devicemetric.GPUUtilizationPct: float64(i + 50)}},
			},
		})
	}
	h.flushOnce()
	from, to := base.Add(-time.Second), base.Add(time.Hour)
	zero, err := h.querySnapshotFilteredContext(context.Background(), "tn", "n1", from, to, TelemetryHistoryQueryOptions{})
	if err != nil || len(zero.Resources) != 3 || len(zero.Probes) != 0 || len(zero.Devices) != 0 {
		t.Fatalf("zero filtered snapshot = %+v err=%v, want resources only", zero, err)
	}
	selected, err := h.querySnapshotFilteredContext(context.Background(), "tn", "n1", from, to, TelemetryHistoryQueryOptions{
		AllProbeSeries: true, DeviceSeriesID: selectedDeviceSeriesID,
	})
	if err != nil || len(selected.Resources) != 3 || len(selected.Probes) != 6 || len(selected.Devices) != 3 {
		t.Fatalf("exact filtered snapshot = %+v err=%v, want resources, all probes, and one exact device", selected, err)
	}
	for i, sample := range selected.Devices {
		if sample.SeriesID != selectedDeviceSeriesID || sample.DeviceID != selectedDeviceID {
			t.Fatalf("filtered snapshot leaked another device series: %+v", sample)
		}
		if got := sample.Values[devicemetric.GPUUtilizationPct]; got != float64(i) {
			t.Fatalf("selected device value[%d] = %v, want %d (including valid zero)", i, got, i)
		}
	}
	exactProbe, err := h.querySnapshotFilteredContext(context.Background(), "tn", "n1", from, to, TelemetryHistoryQueryOptions{ProbeSeriesID: probeSeriesID})
	if err != nil || len(exactProbe.Probes) != 3 || len(exactProbe.Devices) != 0 {
		t.Fatalf("exact probe snapshot = %+v err=%v, want only the selected probe", exactProbe, err)
	}
	for _, sample := range exactProbe.Probes {
		if sample.SeriesID != probeSeriesID || sample.ID != "selected" {
			t.Fatalf("filtered snapshot leaked another probe series: %+v", sample)
		}
	}
	all, err := h.querySnapshot("tn", "n1", from, to)
	if err != nil || len(all.Probes) != 6 || len(all.Devices) != 0 {
		t.Fatalf("legacy unfiltered snapshot = %+v err=%v, want all probes and no broad devices", all, err)
	}
}

func TestHistory_FilteredDeviceDecoderSkipsNonSelectedNumericPayloads(t *testing.T) {
	at := time.Unix(65_000, 0).UTC()
	selectedDeviceID := devicemetric.SeriesID(devicemetric.KindGPU, []byte("selected"))
	selectedSeriesID, err := devicemetric.HistorySeriesID(devicemetric.KindGPU, selectedDeviceID)
	if err != nil {
		t.Fatal(err)
	}
	otherDeviceID := devicemetric.SeriesID(devicemetric.KindGPU, []byte("other"))
	otherSeriesID, err := devicemetric.HistorySeriesID(devicemetric.KindGPU, otherDeviceID)
	if err != nil {
		t.Fatal(err)
	}
	selectedRaw, err := json.Marshal(DeviceHistorySample{
		SeriesID: selectedSeriesID, DeviceID: selectedDeviceID, Kind: devicemetric.KindGPU, TS: at,
		Values: map[devicemetric.NumericKey]float64{devicemetric.GPUUtilizationPct: 0},
	})
	if err != nil {
		t.Fatal(err)
	}
	line := []byte(fmt.Sprintf(
		`{"recorded_at":%q,"device_samples":[{"series_id":%q,"device_id":%q,"kind":"gpu","ts":%q,"values":"malformed-and-must-not-be-decoded"},%s]}`,
		at.Format(time.RFC3339Nano), otherSeriesID, otherDeviceID, at.Format(time.RFC3339Nano), selectedRaw,
	))
	record, err := decodeTelemetryHistoryRecordForFilter(line, telemetryHistoryFilter{deviceSeriesID: selectedSeriesID})
	if err != nil || len(record.DeviceSamples) != 1 || record.DeviceSamples[0].SeriesID != selectedSeriesID {
		t.Fatalf("selector-aware decode = %+v err=%v, want only selected device", record, err)
	}
	if value, present := record.DeviceSamples[0].Values[devicemetric.GPUUtilizationPct]; !present || value != 0 {
		t.Fatalf("selected valid zero = %v/%v, want 0/true", value, present)
	}
	withoutDevices, err := decodeTelemetryHistoryRecordForFilter(line, telemetryHistoryFilter{})
	if err != nil || len(withoutDevices.DeviceSamples) != 0 {
		t.Fatalf("omitted device selector decoded device values: %+v err=%v", withoutDevices, err)
	}
}

func TestHistory_ContextCancellationInterruptsTailScan(t *testing.T) {
	dir := t.TempDir()
	h := newTelemetryHistory(dir, 2_000, nil)
	base := time.Unix(70_000, 0).UTC()
	for i := 0; i < 1_000; i++ {
		h.append("tn", "n1", ResourceSample{TS: base.Add(time.Duration(i) * time.Second), Load1: float64(i)})
	}
	h.flushOnce()
	ctx := &cancelAfterHistoryChecksContext{Context: context.Background(), remaining: 3}
	_, err := readHistoryJSONLTail(ctx, filepath.Join(dir, "tn", "n1.jsonl"), base.Add(-time.Second), base.Add(time.Hour), 1_000, h.fileByteLimit(), telemetryHistoryFilter{allProbes: true})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("tail scan error = %v, want context cancellation during bounded backward scan", err)
	}
}

func TestHistory_StartupAndCapQueueConvergeOfflineFiles(t *testing.T) {
	t.Run("startup byte ceiling", func(t *testing.T) {
		dir := t.TempDir()
		writer := newTelemetryHistory(dir, 100, nil)
		base := time.Unix(80_000, 0).UTC()
		records := make([]telemetryHistoryRecord, 10)
		for i := range records {
			sample := ResourceSample{TS: base.Add(time.Duration(i) * time.Second), Load1: float64(i)}
			records[i] = telemetryHistoryRecord{Resource: &sample, RecordedAt: sample.TS}
			writer.appendRecord("tn", "n1", records[i])
		}
		writer.flushOnce()
		var budget int64
		for _, record := range records[7:] {
			size, err := telemetryHistoryRecordEncodedBytes(record)
			if err != nil {
				t.Fatal(err)
			}
			budget += size
		}
		restarted := newTelemetryHistory(dir, 100, nil)
		restarted.maxFileBytes = budget
		restarted.compactTargetBytes = budget
		restarted.start()
		defer restarted.close()
		waitForHistoryLines(t, filepath.Join(dir, "tn", "n1.jsonl"), 3)
	})

	t.Run("cap change queue", func(t *testing.T) {
		dir := t.TempDir()
		h := newTelemetryHistory(dir, 10, nil)
		base := time.Unix(90_000, 0).UTC()
		for i := 0; i < 10; i++ {
			h.append("tn", "n1", ResourceSample{TS: base.Add(time.Duration(i) * time.Second), Load1: float64(i)})
		}
		h.flushOnce()
		h.start()
		defer h.close()
		h.setCap("tn", 3)
		waitForHistoryLines(t, filepath.Join(dir, "tn", "n1.jsonl"), 3)
	})

	t.Run("cap change coalescer retains every tenant", func(t *testing.T) {
		h := newTelemetryHistory(t.TempDir(), 10, nil)
		h.maintenancePending = make(map[TenantID]struct{})
		h.maintenanceWake = make(chan struct{}, 1)
		for i := 0; i < 128; i++ {
			h.scheduleMaintenance(TenantID(fmt.Sprintf("tenant-%03d", i)))
		}
		pending := h.takePendingMaintenance()
		if len(pending) != 128 {
			t.Fatalf("pending maintenance tenants = %d, want 128", len(pending))
		}
		for i, tenant := range pending {
			want := TenantID(fmt.Sprintf("tenant-%03d", i))
			if tenant != want {
				t.Fatalf("pending[%d] = %q, want %q", i, tenant, want)
			}
		}
		if len(h.maintenanceWake) != 1 {
			t.Fatalf("coalesced wake count = %d, want 1", len(h.maintenanceWake))
		}
	})
}

func TestCopyHistoryContextUsesBoundedStreamingBuffer(t *testing.T) {
	source := &historyReadProbe{remaining: 2 << 20}
	if err := copyHistoryContext(context.Background(), io.Discard, source); err != nil {
		t.Fatal(err)
	}
	if source.maxRequest > historyIOChunkBytes || source.calls < 2 {
		t.Fatalf("stream reads: max request=%d calls=%d, want <=%d and multiple chunks", source.maxRequest, source.calls, historyIOChunkBytes)
	}
}

type cancelAfterHistoryChecksContext struct {
	context.Context
	remaining int
}

func (c *cancelAfterHistoryChecksContext) Err() error {
	if c.remaining <= 1 {
		c.remaining = 0
		return context.Canceled
	}
	c.remaining--
	return nil
}

type historyReadProbe struct {
	remaining  int
	maxRequest int
	calls      int
}

func (r *historyReadProbe) Read(p []byte) (int, error) {
	if r.remaining == 0 {
		return 0, io.EOF
	}
	r.calls++
	if len(p) > r.maxRequest {
		r.maxRequest = len(p)
	}
	n := len(p)
	if n > r.remaining {
		n = r.remaining
	}
	r.remaining -= n
	return n, nil
}

func waitForHistoryLines(t *testing.T, p string, want int) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if got := countLines(p); got == want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("history file %s did not converge to %d lines (got %d)", p, want, countLines(p))
}
