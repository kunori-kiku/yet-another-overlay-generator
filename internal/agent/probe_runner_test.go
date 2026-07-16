package agent

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/kunorikiku/yet-another-overlay-generator/internal/bundlesig"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/model"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/probemetric"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/probepolicy"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/telemetryprotocol"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/trustlist"
)

func saveActiveProbePolicy(t *testing.T, stateDir string, probes []model.TelemetryProbe) []byte {
	t.Helper()
	raw, err := probepolicy.Marshal(probes)
	if err != nil {
		t.Fatalf("Marshal probe policy: %v", err)
	}
	if err := SaveState(stateDir, &State{
		NodeID:                "alpha",
		LastResult:            LastResultOK,
		ActiveTelemetryPolicy: raw,
	}); err != nil {
		t.Fatalf("SaveState: %v", err)
	}
	return raw
}

func activePolicyProbeID(t *testing.T, raw []byte) string {
	t.Helper()
	policy, err := probepolicy.Parse(raw)
	if err != nil {
		t.Fatalf("Parse active policy: %v", err)
	}
	if len(policy.Probes) != 1 {
		t.Fatalf("active policy probes = %+v, want one", policy.Probes)
	}
	return policy.Probes[0].ID
}

func waitProbeStatus(t *testing.T, sampler *activeProbeSampler, id, status string) activeProbeResult {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		sampler.mu.Lock()
		runtime := sampler.probes[id]
		if runtime != nil && runtime.result.Status == status {
			result := runtime.result
			sampler.mu.Unlock()
			return result
		}
		sampler.mu.Unlock()
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("probe %q never reached status %q", id, status)
	return activeProbeResult{}
}

func waitProbeSampleCount(t *testing.T, sampler *activeProbeSampler, want int) []activeProbeResult {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		sampler.mu.Lock()
		if len(sampler.samples) >= want {
			samples := append([]activeProbeResult(nil), sampler.samples...)
			sampler.mu.Unlock()
			return samples
		}
		sampler.mu.Unlock()
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("probe sample count never reached %d", want)
	return nil
}

func TestActiveProbeSampler_NonBlockingResultAndNoOverlap(t *testing.T) {
	dir := t.TempDir()
	probe := model.TelemetryProbe{ID: "tls", Type: model.TelemetryProbeTCP, Host: "service.example", Port: 443}
	saveActiveProbePolicy(t, dir, []model.TelemetryProbe{probe})

	sampler := newActiveProbeSampler(dir)
	t.Cleanup(sampler.clear)
	sampler.jitter = func(string, model.TelemetryProbe) time.Duration { return 0 }
	started := make(chan struct{}, 2)
	release := make(chan struct{})
	var calls atomic.Int32
	sampler.attempt = func(ctx context.Context, _ model.TelemetryProbe) string {
		calls.Add(1)
		started <- struct{}{}
		select {
		case <-release:
			return ""
		case <-ctx.Done():
			return "timeout"
		}
	}

	now := time.Now().UTC()
	before := time.Now()
	_, metrics := sampler.Sample(now)
	if elapsed := time.Since(before); elapsed > 100*time.Millisecond {
		t.Fatalf("Sample blocked on network attempt for %s", elapsed)
	}
	results := metrics[probeResultsMetricKey].([]activeProbeResult)
	if len(results) != 1 || results[0].Status != probeStatusPending || results[0].Host != probe.Host {
		t.Fatalf("initial results = %+v", results)
	}
	if _, present := metrics[probeSamplesMetricKey]; present {
		t.Fatalf("pending probe leaked into completed sample window: %+v", metrics[probeSamplesMetricKey])
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("probe attempt did not start")
	}
	for i := 0; i < 5; i++ {
		sampler.Sample(now.Add(time.Hour))
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("overlapping attempts = %d, want 1", got)
	}
	close(release)
	result := waitProbeStatus(t, sampler, probe.ID, probeStatusSuccess)
	if result.CheckedAt == "" || result.LatencyMS == nil || result.FailureReason != "" {
		t.Fatalf("success result = %+v", result)
	}

	_, metrics = sampler.Sample(now)
	results = metrics[probeResultsMetricKey].([]activeProbeResult)
	raw, err := json.Marshal(results)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"id":"tls"`, `"type":"tcp"`, `"host":"service.example"`, `"port":443`, `"status":"success"`, `"latency_ms"`, `"checked_at"`} {
		if !strings.Contains(string(raw), want) {
			t.Fatalf("wire result %s missing %s", raw, want)
		}
	}
	if strings.Contains(string(raw), `"interval_ms"`) {
		t.Fatalf("legacy probe_results shape gained interval_ms: %s", raw)
	}
}

func TestActiveProbeSampler_CarriesMultipleCompletedAttemptsBetweenHeartbeats(t *testing.T) {
	dir := t.TempDir()
	probe := model.TelemetryProbe{
		ID: "tls", Type: model.TelemetryProbeTCP, Host: "service.example", Port: 443,
		IntervalSeconds: 30,
	}
	saveActiveProbePolicy(t, dir, []model.TelemetryProbe{probe})

	sampler := newActiveProbeSampler(dir)
	t.Cleanup(sampler.clear)
	sampler.jitter = func(string, model.TelemetryProbe) time.Duration { return 0 }
	base := time.Date(2026, time.July, 16, 10, 0, 0, 0, time.UTC)
	var elapsedNanos atomic.Int64
	sampler.monotonicNow = func() time.Time { return base.Add(time.Duration(elapsedNanos.Load())) }
	var waits atomic.Int32
	sampler.wait = func(ctx context.Context, delay time.Duration) bool {
		if waits.Add(1) > 3 {
			<-ctx.Done()
			return false
		}
		elapsedNanos.Add(int64(delay))
		return ctx.Err() == nil
	}
	var attempts atomic.Int32
	sampler.attempt = func(context.Context, model.TelemetryProbe) string {
		if attempts.Add(1) == 2 {
			return probeFailureTimeout
		}
		return ""
	}
	sampler.checkedAtNow = func() time.Time {
		return base.Add(time.Duration(attempts.Load()) * time.Second)
	}

	// One heartbeat starts the independent cadence loop. Three attempts complete before another
	// heartbeat samples the telemetry framework.
	sampler.Sample(base)
	wantSamples := waitProbeSampleCount(t, sampler, 3)
	if got := attempts.Load(); got != 3 {
		t.Fatalf("attempts between heartbeats = %d, want 3", got)
	}
	_, metrics := sampler.Sample(base.Add(2 * time.Minute))
	samples := metrics[probeSamplesMetricKey].([]activeProbeResult)
	if len(samples) != 3 || len(wantSamples) != 3 {
		t.Fatalf("probe_samples = %+v, want three completed attempts", samples)
	}
	for i, sample := range samples {
		if sample.Status == probeStatusPending || sample.CheckedAt == "" || sample.IntervalMS != 30000 {
			t.Fatalf("probe_samples[%d] = %+v, want completed attempt with 30s cadence", i, sample)
		}
	}
	if samples[1].Status != probeStatusFailure || samples[1].FailureReason != probeFailureTimeout {
		t.Fatalf("middle failed sample = %+v, want timeout", samples[1])
	}
	latest := metrics[probeResultsMetricKey].([]activeProbeResult)
	if len(latest) != 1 || latest[0].Status != probeStatusSuccess || latest[0].IntervalMS != 0 {
		t.Fatalf("legacy latest probe_results = %+v", latest)
	}
}

func TestActiveProbeSampler_HighWaterCoalescesAndPreservesMaximumLegalProbeCompletions(t *testing.T) {
	sampler := newActiveProbeSampler(t.TempDir())
	for i := 0; i < probepolicy.MaxProbes; i++ {
		id := fmt.Sprintf("probe-%02d", i)
		probe := model.TelemetryProbe{ID: id, Type: model.TelemetryProbeICMP, Host: fmt.Sprintf("node-%02d.example", i)}
		sampler.order = append(sampler.order, id)
		sampler.probes[id] = &probeRuntime{probe: probe, result: configuredProbeResult(probe, probeStatusPending)}
	}

	delivered := make(map[string]struct{})
	kicks := 0
	pendingKick := false
	base := time.Date(2026, time.July, 16, 10, 0, 0, 0, time.UTC)
	for attempt := 0; attempt < 80; attempt++ {
		id := sampler.order[attempt%len(sampler.order)]
		probe := sampler.probes[id].probe
		result := configuredProbeResult(probe, probeStatusFailure)
		result.CheckedAt = base.Add(time.Duration(attempt) * time.Second).Format(time.RFC3339Nano)
		result.FailureReason = probeFailureTimeout
		result.IntervalMS = 30_000

		sampler.mu.Lock()
		shouldKick := sampler.appendCompletedSampleLocked(result)
		sampler.mu.Unlock()
		if shouldKick {
			kicks++
			if pendingKick {
				t.Fatalf("attempt %d generated a second kick before the pending collection", attempt)
			}
			pendingKick = true
		}

		// Model the maximum legal sixteen-probe round completing after the 32-attempt high-water
		// signal but before the heartbeat goroutine gets scheduled. The 64-entry window still has a
		// full round of headroom, and the next batch gets its own coalesced signal after collection.
		if attempt == 47 || attempt == 79 {
			if !pendingKick {
				t.Fatalf("attempt %d reached collection point without a high-water kick", attempt)
			}
			sampler.mu.Lock()
			metrics := sampler.snapshotLocked()
			sampler.mu.Unlock()
			for _, sample := range metrics[probeSamplesMetricKey].([]activeProbeResult) {
				delivered[sample.ID+"\x00"+sample.CheckedAt] = struct{}{}
			}
			pendingKick = false
		}
	}
	if kicks != 2 {
		t.Fatalf("high-water kicks = %d, want 2", kicks)
	}
	if len(delivered) != 80 {
		t.Fatalf("unique delivered attempts = %d, want all 80 across overlapping bounded snapshots", len(delivered))
	}
}

func TestActiveProbeSampler_AttemptPanicBecomesNetworkErrorAndCadenceSurvives(t *testing.T) {
	dir := t.TempDir()
	probe := model.TelemetryProbe{
		ID: "tls", Type: model.TelemetryProbeTCP, Host: "service.example", Port: 443,
		IntervalSeconds: 30,
	}
	saveActiveProbePolicy(t, dir, []model.TelemetryProbe{probe})

	sampler := newActiveProbeSampler(dir)
	t.Cleanup(sampler.clear)
	sampler.jitter = func(string, model.TelemetryProbe) time.Duration { return 0 }
	var waits atomic.Int32
	sampler.wait = func(ctx context.Context, _ time.Duration) bool {
		if waits.Add(1) > 2 {
			<-ctx.Done()
			return false
		}
		return ctx.Err() == nil
	}
	var attempts atomic.Int32
	sampler.attempt = func(context.Context, model.TelemetryProbe) string {
		if attempts.Add(1) == 1 {
			panic("probe implementation panic")
		}
		return ""
	}

	// The probe loop runs outside Telemetry.sampleGuarded, so it must contain its own panic boundary.
	// A broken attempt is reported with a closed failure category and the next signed cadence still runs.
	sampler.Sample(time.Now().UTC())
	samples := waitProbeSampleCount(t, sampler, 2)
	if got := attempts.Load(); got != 2 {
		t.Fatalf("attempts after panic = %d, want 2", got)
	}
	if samples[0].Status != probeStatusFailure || samples[0].FailureReason != probeFailureNetworkError || samples[0].LatencyMS != nil {
		t.Fatalf("panicked attempt sample = %+v, want failure/network_error", samples[0])
	}
	if samples[1].Status != probeStatusSuccess || samples[1].FailureReason != "" || samples[1].LatencyMS == nil {
		t.Fatalf("post-panic attempt sample = %+v, want success", samples[1])
	}
}

func TestProbeLatencyMilliseconds_ClampsNegativeElapsed(t *testing.T) {
	started := time.Unix(1_000, 0)
	if got := probeLatencyMilliseconds(started, started.Add(-time.Second)); got != 0 {
		t.Fatalf("negative elapsed latency = %v, want 0", got)
	}
	if got := probeLatencyMilliseconds(started, started.Add(125*time.Millisecond)); got != 125 {
		t.Fatalf("positive elapsed latency = %v, want 125", got)
	}
}

func TestActiveProbeSampler_CompletedWindowIsBoundedAndExcludesPending(t *testing.T) {
	sampler := newActiveProbeSampler(t.TempDir())
	sampler.mu.Lock()
	sampler.appendCompletedSampleLocked(activeProbeResult{ID: "pending", Status: probeStatusPending})
	for i := 0; i < probemetric.MaxRecentSamples+10; i++ {
		latency := float64(i)
		sampler.appendCompletedSampleLocked(activeProbeResult{
			ID: "tls", Type: model.TelemetryProbeTCP, Host: "service.example", Port: 443,
			Status: probeStatusSuccess, LatencyMS: &latency,
			CheckedAt:  time.Unix(int64(i), 0).UTC().Format(time.RFC3339Nano),
			IntervalMS: 30000,
		})
	}
	samples := append([]activeProbeResult(nil), sampler.samples...)
	sampler.mu.Unlock()

	if len(samples) != probemetric.MaxRecentSamples {
		t.Fatalf("completed sample window = %d, want %d", len(samples), probemetric.MaxRecentSamples)
	}
	if samples[0].CheckedAt != time.Unix(10, 0).UTC().Format(time.RFC3339Nano) {
		t.Fatalf("oldest retained sample = %+v, want attempt 10", samples[0])
	}
	if samples[len(samples)-1].CheckedAt != time.Unix(int64(probemetric.MaxRecentSamples+9), 0).UTC().Format(time.RFC3339Nano) {
		t.Fatalf("newest retained sample = %+v", samples[len(samples)-1])
	}
	for _, sample := range samples {
		if sample.Status == probeStatusPending {
			t.Fatalf("pending row entered completed window: %+v", sample)
		}
	}
}

func TestActiveProbeSampler_PolicyChangeFiltersSamplesAndClearDropsAll(t *testing.T) {
	dir := t.TempDir()
	keep := model.TelemetryProbe{ID: "keep", Type: model.TelemetryProbeTCP, Host: "keep.example", Port: 443}
	changed := model.TelemetryProbe{ID: "changed", Type: model.TelemetryProbeTCP, Host: "old.example", Port: 443}
	removed := model.TelemetryProbe{ID: "removed", Type: model.TelemetryProbeICMP, Host: "remove.example"}
	saveActiveProbePolicy(t, dir, []model.TelemetryProbe{keep, changed, removed})

	sampler := newActiveProbeSampler(dir)
	t.Cleanup(sampler.clear)
	// Keep each scheduler parked until policy reconciliation cancels it; this test controls samples
	// directly and performs no outbound attempt.
	sampler.wait = func(ctx context.Context, _ time.Duration) bool {
		<-ctx.Done()
		return false
	}
	sampler.jitter = func(string, model.TelemetryProbe) time.Duration { return 0 }
	sampler.Sample(time.Now())
	sampler.mu.Lock()
	for _, probe := range []model.TelemetryProbe{keep, changed, removed} {
		latency := 1.0
		sampler.appendCompletedSampleLocked(activeProbeResult{
			ID: probe.ID, Type: probe.Type, Host: probe.Host, Port: probe.Port,
			Status: probeStatusSuccess, LatencyMS: &latency, CheckedAt: time.Now().UTC().Format(time.RFC3339Nano), IntervalMS: 60000,
		})
	}
	sampler.mu.Unlock()

	// An interval-only edit keeps the same executable series. Reusing an id for a new host and removing
	// a probe both discard their old rolling samples immediately.
	keep.IntervalSeconds = 30
	changed.Host = "new.example"
	saveActiveProbePolicy(t, dir, []model.TelemetryProbe{keep, changed})
	_, metrics := sampler.Sample(time.Now())
	samples := metrics[probeSamplesMetricKey].([]activeProbeResult)
	if len(samples) != 1 || samples[0].ID != "keep" || samples[0].Host != "keep.example" {
		t.Fatalf("samples after policy change = %+v, want only unchanged destination", samples)
	}

	if err := SaveState(dir, &State{NodeID: "alpha", LastResult: LastResultOK}); err != nil {
		t.Fatal(err)
	}
	if _, metrics := sampler.Sample(time.Now()); metrics != nil {
		t.Fatalf("signed policy clear still emitted telemetry: %+v", metrics)
	}
	sampler.mu.Lock()
	defer sampler.mu.Unlock()
	if len(sampler.samples) != 0 || len(sampler.probes) != 0 {
		t.Fatalf("sampler after clear: samples=%+v probes=%+v", sampler.samples, sampler.probes)
	}
}

func TestActiveProbeSampler_MaxWindowFitsTelemetryAdmission(t *testing.T) {
	sampler := newActiveProbeSampler(t.TempDir())
	host := strings.Repeat("a", 63) + "." + strings.Repeat("b", 63) + "." + strings.Repeat("c", 63) + "." + strings.Repeat("d", 61)
	checkedAt := time.Date(2026, time.July, 16, 10, 20, 30, 123456789, time.UTC).Format(time.RFC3339Nano)
	sampler.mu.Lock()
	for i := 0; i < probepolicy.MaxProbes; i++ {
		id := strings.Repeat("p", 60) + fmt.Sprintf("%03d", i)
		probe := model.TelemetryProbe{ID: id, Type: model.TelemetryProbeTCP, Host: host, Port: 65535}
		latency := 4999.9
		result := activeProbeResult{
			ID: id, Type: probe.Type, Host: probe.Host, Port: probe.Port,
			Status: probeStatusSuccess, LatencyMS: &latency, CheckedAt: checkedAt,
		}
		sampler.order = append(sampler.order, id)
		sampler.probes[id] = &probeRuntime{probe: probe, result: result}
	}
	for i := 0; i < probemetric.MaxRecentSamples; i++ {
		id := sampler.order[i%len(sampler.order)]
		result := sampler.probes[id].result
		result.IntervalMS = int64(probepolicy.MaxIntervalSeconds) * 1000
		sampler.appendCompletedSampleLocked(result)
	}
	metrics := sampler.snapshotLocked()
	sampler.mu.Unlock()

	raw, err := json.Marshal(metrics)
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) > telemetryprotocol.MaxMetricsBytes {
		t.Fatalf("max probe metrics = %d bytes, exceeds %d", len(raw), telemetryprotocol.MaxMetricsBytes)
	}
	sequencer := &telemetrySequencer{bootID: "00112233445566778899aabbccddeeff"}
	if _, err := sequencer.snapshot(nil, metrics, time.Now(), 30*time.Second); err != nil {
		t.Fatalf("maximum bounded probe window rejected by telemetry admission: %v", err)
	}
}

func TestActiveProbeSampler_MonotonicScheduleIgnoresHeartbeatWallClock(t *testing.T) {
	dir := t.TempDir()
	probe := model.TelemetryProbe{
		ID: "tls", Type: model.TelemetryProbeTCP, Host: "service.example", Port: 443,
		IntervalSeconds: 30,
	}
	saveActiveProbePolicy(t, dir, []model.TelemetryProbe{probe})

	sampler := newActiveProbeSampler(dir)
	t.Cleanup(sampler.clear)
	sampler.jitter = func(string, model.TelemetryProbe) time.Duration { return 0 }
	waiting := make(chan time.Duration, 2)
	releaseInterval := make(chan struct{})
	var intervalWaits atomic.Int32
	sampler.wait = func(ctx context.Context, delay time.Duration) bool {
		if delay <= 0 {
			return ctx.Err() == nil
		}
		if intervalWaits.Add(1) > 1 {
			<-ctx.Done()
			return false
		}
		waiting <- delay
		select {
		case <-releaseInterval:
			return true
		case <-ctx.Done():
			return false
		}
	}
	var calls atomic.Int32
	sampler.attempt = func(context.Context, model.TelemetryProbe) string {
		calls.Add(1)
		return ""
	}

	sampler.Sample(time.Date(2099, time.January, 1, 0, 0, 0, 0, time.UTC))
	waitProbeStatus(t, sampler, probe.ID, probeStatusSuccess)
	if got := calls.Load(); got != 1 {
		t.Fatalf("initial attempts = %d, want 1", got)
	}
	select {
	case delay := <-waiting:
		if delay != 30*time.Second {
			t.Fatalf("next probe delay = %s, want 30s", delay)
		}
	case <-time.After(time.Second):
		t.Fatal("probe scheduler did not wait for its signed interval")
	}

	// Heartbeat wall time does not drive the independently waiting probe scheduler.
	sampler.Sample(time.Date(2199, time.January, 1, 0, 0, 0, 0, time.UTC))
	time.Sleep(20 * time.Millisecond)
	if got := calls.Load(); got != 1 {
		t.Fatalf("heartbeat wall clock triggered attempts = %d, want 1", got)
	}

	// Releasing the monotonic interval starts the next attempt without another heartbeat.
	close(releaseInterval)
	deadline := time.Now().Add(time.Second)
	for calls.Load() != 2 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if got := calls.Load(); got != 2 {
		t.Fatalf("attempts at monotonic interval = %d, want 2", got)
	}
}

func TestActiveProbeSampler_MonotonicLatencyUsesWallOnlyForCheckedAt(t *testing.T) {
	dir := t.TempDir()
	probe := model.TelemetryProbe{ID: "tls", Type: model.TelemetryProbeTCP, Host: "service.example", Port: 443}
	saveActiveProbePolicy(t, dir, []model.TelemetryProbe{probe})

	sampler := newActiveProbeSampler(dir)
	t.Cleanup(sampler.clear)
	sampler.jitter = func(string, model.TelemetryProbe) time.Duration { return 0 }
	base := time.Date(2099, time.January, 1, 0, 0, 0, 0, time.UTC)
	var elapsedNanos atomic.Int64
	sampler.monotonicNow = func() time.Time {
		return base.Add(time.Duration(elapsedNanos.Load()))
	}
	// Simulate wall time stepping backward while monotonic elapsed time still advances.
	checkedAt := time.Date(1999, time.December, 31, 23, 59, 59, 0, time.FixedZone("test", 9*60*60))
	sampler.checkedAtNow = func() time.Time { return checkedAt }
	sampler.attempt = func(context.Context, model.TelemetryProbe) string {
		elapsedNanos.Add(int64(125 * time.Millisecond))
		return ""
	}

	sampler.Sample(time.Date(2099, time.January, 1, 0, 0, 0, 0, time.UTC))
	result := waitProbeStatus(t, sampler, probe.ID, probeStatusSuccess)
	if result.LatencyMS == nil || *result.LatencyMS != 125 {
		t.Fatalf("latency = %v, want 125ms", result.LatencyMS)
	}
	if want := checkedAt.UTC().Format(time.RFC3339Nano); result.CheckedAt != want {
		t.Fatalf("checked_at = %q, want UTC wall time %q", result.CheckedAt, want)
	}
}

func TestActiveProbeSampler_BoundsConcurrencyAndCancelsOnSignedOmission(t *testing.T) {
	dir := t.TempDir()
	probes := make([]model.TelemetryProbe, 12)
	for i := range probes {
		probes[i] = model.TelemetryProbe{
			ID: fmt.Sprintf("p-%02d", i), Type: model.TelemetryProbeTCP,
			Host: fmt.Sprintf("host-%02d.example", i), Port: 443,
		}
	}
	saveActiveProbePolicy(t, dir, probes)
	sampler := newActiveProbeSampler(dir)
	t.Cleanup(sampler.clear)
	sampler.jitter = func(string, model.TelemetryProbe) time.Duration { return 0 }
	release := make(chan struct{})
	done := make(chan struct{}, len(probes))
	var current, maximum atomic.Int32
	sampler.attempt = func(ctx context.Context, _ model.TelemetryProbe) string {
		n := current.Add(1)
		for {
			old := maximum.Load()
			if n <= old || maximum.CompareAndSwap(old, n) {
				break
			}
		}
		select {
		case <-release:
		case <-ctx.Done():
		}
		current.Add(-1)
		done <- struct{}{}
		return ""
	}
	sampler.Sample(time.Now().UTC())
	deadline := time.Now().Add(time.Second)
	for maximum.Load() < maxConcurrentProbes && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if got := maximum.Load(); got != maxConcurrentProbes {
		t.Fatalf("maximum concurrency before release = %d, want %d", got, maxConcurrentProbes)
	}
	close(release)
	for range probes {
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Fatal("bounded probe queue did not drain")
		}
	}
	if got := maximum.Load(); got > maxConcurrentProbes {
		t.Fatalf("maximum concurrency = %d, exceeds %d", got, maxConcurrentProbes)
	}

	// A newly active attempt is canceled when a successful policy omission reaches state.
	cancelProbe := model.TelemetryProbe{ID: "cancel", Type: model.TelemetryProbeICMP, Host: "192.0.2.1"}
	saveActiveProbePolicy(t, dir, []model.TelemetryProbe{cancelProbe})
	startedCancel := make(chan struct{})
	canceled := make(chan struct{})
	sampler.attempt = func(ctx context.Context, _ model.TelemetryProbe) string {
		close(startedCancel)
		<-ctx.Done()
		close(canceled)
		return "timeout"
	}
	sampler.Sample(time.Now().UTC())
	select {
	case <-startedCancel:
	case <-time.After(time.Second):
		t.Fatal("cancelable attempt did not start")
	}
	if err := SaveState(dir, &State{NodeID: "alpha", LastResult: LastResultOK}); err != nil {
		t.Fatal(err)
	}
	sampler.Sample(time.Now().UTC())
	select {
	case <-canceled:
	case <-time.After(time.Second):
		t.Fatal("signed policy omission did not cancel the active attempt")
	}
	if metrics := sampler.snapshot(); metrics != nil {
		t.Fatalf("cleared policy still reports metrics: %+v", metrics)
	}
}

func TestPerformProbeAttempt_TCPResolvesDNSWithoutExtraDependency(t *testing.T) {
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	accepted := make(chan struct{})
	go func() {
		conn, err := listener.Accept()
		if err == nil {
			_ = conn.Close()
			close(accepted)
		}
	}()
	port := listener.Addr().(*net.TCPAddr).Port
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if reason := performProbeAttempt(ctx, model.TelemetryProbe{
		ID: "local", Type: model.TelemetryProbeTCP, Host: "localhost", Port: port,
	}); reason != "" {
		t.Fatalf("TCP DNS probe failed: %s", reason)
	}
	select {
	case <-accepted:
	case <-time.After(time.Second):
		t.Fatal("listener did not accept TCP probe")
	}
}

type testTimeoutError struct{}

func (testTimeoutError) Error() string   { return "timeout" }
func (testTimeoutError) Timeout() bool   { return true }
func (testTimeoutError) Temporary() bool { return true }

func TestProbeFailureClassification_DNSAndTCP(t *testing.T) {
	probe := model.TelemetryProbe{ID: "tls", Type: model.TelemetryProbeTCP, Host: "bad.example", Port: 443}
	if got := performResolvedProbeAttempt(context.Background(), probe, nil, errors.New("resolver failed")); got != probeFailureDNSFailed {
		t.Fatalf("DNS resolution failure = %q, want dns_failed", got)
	}
	refused := &net.OpError{Op: "dial", Net: "tcp", Err: syscall.ECONNREFUSED}
	if got := classifyProbeError(refused); got != probeFailureConnectionRefused {
		t.Fatalf("TCP refusal = %q, want connection_refused", got)
	}
	timedOut := &net.OpError{Op: "dial", Net: "tcp", Err: testTimeoutError{}}
	if got := classifyProbeError(timedOut); got != probeFailureTimeout {
		t.Fatalf("TCP timeout = %q, want timeout", got)
	}
	if got := classifyProbeError(os.ErrPermission); got != probeFailurePermissionDenied {
		t.Fatalf("permission failure = %q, want permission_denied", got)
	}
	if got := classifyProbeError(syscall.ENETUNREACH); got != probeFailureNetworkUnreachable {
		t.Fatalf("network route failure = %q, want network_unreachable", got)
	}
	if got := classifyProbeError(errors.New("opaque network failure")); got != probeFailureNetworkError {
		t.Fatalf("opaque failure = %q, want network_error", got)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if got := performResolvedProbeAttempt(ctx, probe, []net.IP{net.ParseIP("192.0.2.1")}, nil); got != probeFailureTimeout {
		t.Fatalf("canceled TCP attempt = %q, want timeout", got)
	}
}

func TestICMPPacketHelpers(t *testing.T) {
	message := []byte{8, 0, 0, 0, 0x12, 0x34, 0, 1, 'y', 'a', 'o', 'g'}
	binary.BigEndian.PutUint16(message[2:4], icmpChecksum(message))
	if got := icmpChecksum(message); got != 0 {
		t.Fatalf("checksum over completed packet = %#x, want 0", got)
	}
	ipv4 := append([]byte{0x45, 0, 0, 32, 0, 0, 0, 0, 64, 1, 0, 0, 127, 0, 0, 1, 127, 0, 0, 1}, message...)
	if got := stripIPHeader(ipv4); string(got) != string(message) {
		t.Fatalf("stripIPHeader(v4) = %x, want %x", got, message)
	}
}

func TestStartupProbeJitterIncludesNodeIdentity(t *testing.T) {
	probe := model.TelemetryProbe{ID: "control", Type: model.TelemetryProbeTCP, Host: "control.example", Port: 443}
	alpha := startupProbeJitter("alpha", probe)
	if got := startupProbeJitter("alpha", probe); got != alpha {
		t.Fatalf("same node/probe jitter changed: %s then %s", alpha, got)
	}
	// These fixed inputs pin the fleet-spreading seed. A collision is possible in principle, but
	// this chosen pair is deterministic and intentionally exercises distinct offsets.
	if beta := startupProbeJitter("beta", probe); beta == alpha {
		t.Fatalf("distinct node identities received the same pinned startup jitter %s", alpha)
	}
}

func TestActiveTelemetryPolicy_RequiresKeystoneAndPreservesLastKnownGood(t *testing.T) {
	oldRaw, err := probepolicy.Marshal([]model.TelemetryProbe{{
		ID: "old", Type: model.TelemetryProbeICMP, Host: "old.example",
	}})
	if err != nil {
		t.Fatal(err)
	}
	newRaw, err := probepolicy.Marshal([]model.TelemetryProbe{{
		ID: "new", Type: model.TelemetryProbeTCP, Host: "new.example", Port: 443,
	}})
	if err != nil {
		t.Fatal(err)
	}
	bundle := newSignedBundle(t, "2026-07-16T00:00:00Z")
	checksummed := map[string]string{
		"install.sh":              string(bundle.files["install.sh"]),
		"wireguard/wg-alpha.conf": string(bundle.files["wireguard/wg-alpha.conf"]),
		"babel/babeld.conf":       string(bundle.files["babel/babeld.conf"]),
		"sysctl/99-overlay.conf":  string(bundle.files["sysctl/99-overlay.conf"]),
		probepolicy.FileName:      string(newRaw),
	}
	canonical := bundlesig.Canonicalize(checksummed)
	bundle.files[probepolicy.FileName] = newRaw
	bundle.files["checksums.sha256"] = canonical
	bundle.files["bundle.sig"] = []byte(base64.StdEncoding.EncodeToString(bundlesig.Sign(canonical, bundle.priv)) + "\n")

	operatorPub, operatorPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	addEd25519Membership(t, bundle.files, operatorPriv, 7)

	dir := t.TempDir()
	if err := SaveState(dir, &State{
		NodeID: "alpha", LastResult: LastResultOK, ActiveTelemetryPolicy: oldRaw,
	}); err != nil {
		t.Fatal(err)
	}
	_, runErr := Run(&Config{
		NodeID: "alpha", Source: staticBundleSource(bundle.files), PinnedPubPEM: bundle.pubPEM,
		StateDir: dir, KeyPath: t.TempDir() + "/private.key",
	})
	if runErr == nil || !strings.Contains(runErr.Error(), "off-host operator credential") {
		t.Fatalf("Run without keystone = %v, want active-policy refusal", runErr)
	}
	state, err := LoadState(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := activePolicyProbeID(t, state.ActiveTelemetryPolicy); got != "old" {
		t.Fatalf("failed candidate replaced last-known-good policy: %s", state.ActiveTelemetryPolicy)
	}

	runResult, err := Run(&Config{
		NodeID:          "alpha",
		Source:          staticBundleSource(bundle.files),
		PinnedPubPEM:    bundle.pubPEM,
		OperatorCredPEM: bundlesig.MarshalPublicKeyPEM(operatorPub),
		OperatorCredAlg: string(trustlist.AlgEd25519),
		StateDir:        dir,
		KeyPath:         t.TempDir() + "/private.key",
		Stdout:          io.Discard,
		Stderr:          io.Discard,
	})
	if err != nil {
		t.Fatalf("Run with valid keystone membership: %v", err)
	}
	if !runResult.Applied {
		t.Fatalf("Run with valid keystone membership = %+v, want applied", runResult)
	}
	state, err = LoadState(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := activePolicyProbeID(t, state.ActiveTelemetryPolicy); got != "new" || !state.MembershipVerified {
		t.Fatalf("successful signed activation state = %+v", state)
	}

	// Failure preserves and signed omission clears the last-known-good policy.
	cfg := &Config{NodeID: "alpha", StateDir: dir}
	man := &manifestInfo{NodeID: "alpha", CompiledAt: "2026-07-16T00:00:00Z", Checksum: "sum"}
	recordFailure(cfg, state, "candidate failed")
	failed, _ := LoadState(dir)
	if got := activePolicyProbeID(t, failed.ActiveTelemetryPolicy); got != "new" {
		t.Fatal("recordFailure did not preserve active last-known-good policy")
	}
	if err := recordSuccess(cfg, failed, man, &VerifyResult{Signed: true}, 7, nil); err != nil {
		t.Fatal(err)
	}
	cleared, _ := LoadState(dir)
	if len(cleared.ActiveTelemetryPolicy) != 0 {
		t.Fatalf("signed omission did not clear policy: %s", cleared.ActiveTelemetryPolicy)
	}
}

func TestVerifyBundle_RejectsUncoveredTelemetryPolicy(t *testing.T) {
	bundle := newSignedBundle(t, "2026-07-16T00:00:00Z")
	bundle.files[probepolicy.FileName] = json.RawMessage(`{"version":1,"probes":[{"id":"p","type":"icmp","host":"example.com"}]}`)
	if _, err := VerifyBundle(bundle.files, bundle.pubPEM); err == nil || !strings.Contains(err.Error(), "telemetry.json present but not covered") {
		t.Fatalf("VerifyBundle uncovered telemetry.json = %v", err)
	}
}
