package agent

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kunorikiku/yet-another-overlay-generator/internal/devicemetric"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/probepolicy"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/telemetrymetric"
)

type deviceTelemetryTestResult struct {
	inventory devicemetric.InventoryMetric
	samples   devicemetric.SamplesMetric
}

type deviceTelemetryTestCollector struct {
	calls        chan time.Time
	results      chan deviceTelemetryTestResult
	returned     chan struct{}
	ignoreCancel bool
	count        atomic.Int64
	resets       atomic.Int64
}

func (c *deviceTelemetryTestCollector) ResetRateBaselines() {
	c.resets.Add(1)
}

func newDeviceTelemetryTestCollector(ignoreCancel bool) *deviceTelemetryTestCollector {
	return &deviceTelemetryTestCollector{
		calls: make(chan time.Time, 4), results: make(chan deviceTelemetryTestResult, 4),
		returned: make(chan struct{}, 4), ignoreCancel: ignoreCancel,
	}
}

func (c *deviceTelemetryTestCollector) Collect(ctx context.Context, now time.Time) (devicemetric.InventoryMetric, devicemetric.SamplesMetric) {
	c.count.Add(1)
	c.calls <- now
	var result deviceTelemetryTestResult
	if c.ignoreCancel {
		result = <-c.results
	} else {
		select {
		case result = <-c.results:
		case <-ctx.Done():
			result = deviceTelemetryTestResult{
				inventory: devicemetric.InventoryMetric{Devices: []devicemetric.InventoryEntry{}},
				samples:   devicemetric.SamplesMetric{Samples: []devicemetric.Sample{}},
			}
		}
	}
	c.returned <- struct{}{}
	return result.inventory, result.samples
}

func deviceTelemetryTestMetrics(value float64) deviceTelemetryTestResult {
	id := devicemetric.SeriesID(devicemetric.KindBlockDevice, []byte("test-disk"))
	return deviceTelemetryTestResult{
		inventory: devicemetric.InventoryMetric{Devices: []devicemetric.InventoryEntry{{
			SeriesID: id, Kind: devicemetric.KindBlockDevice, Label: "disk-a", Status: devicemetric.StatusOK,
		}}},
		samples: devicemetric.SamplesMetric{Samples: []devicemetric.Sample{{
			SeriesID: id, Kind: devicemetric.KindBlockDevice,
			Values: map[devicemetric.NumericKey]float64{devicemetric.DiskReadBytesPerSecond: value},
		}}},
	}
}

func saveDeviceTelemetryTestPolicy(t *testing.T, stateDir string, enabled bool) {
	t.Helper()
	state := &State{NodeID: "node-a", LastResult: LastResultOK}
	if enabled {
		raw, err := probepolicy.MarshalSuccessor(probepolicy.SuccessorPolicy{
			Devices: &probepolicy.DevicePolicy{Mode: probepolicy.DeviceModeAllEligibleV1},
		})
		if err != nil {
			t.Fatal(err)
		}
		state.ActiveTelemetryPolicy = raw
	}
	if err := SaveState(stateDir, state); err != nil {
		t.Fatal(err)
	}
}

func waitForDeviceTelemetryTest(t *testing.T, condition func() bool) {
	t.Helper()
	deadline := time.NewTimer(2 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	for {
		if condition() {
			return
		}
		select {
		case <-deadline.C:
			t.Fatal("timed out waiting for device telemetry")
		case <-ticker.C:
		}
	}
}

func deviceTelemetryTestState(s *deviceTelemetrySampler) (active, ready bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.active, s.ready
}

func deviceTelemetryTestWorkerDone(s *deviceTelemetrySampler) <-chan struct{} {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.workerDone
}

func TestDeviceTelemetrySampler_SignedOptInControlsCollection(t *testing.T) {
	stateDir := t.TempDir()
	saveDeviceTelemetryTestPolicy(t, stateDir, false)
	collector := newDeviceTelemetryTestCollector(false)
	ticks := make(chan struct{}, 2)
	sampler := newDeviceTelemetrySampler(stateDir)
	sampler.collector = collector
	collectedAt := time.Unix(100, 0).UTC()
	sampler.now = func() time.Time { return collectedAt }
	var completionKicks atomic.Int64
	sampler.setTelemetryCompletionKick(func() { completionKicks.Add(1) })
	sampler.wait = func(ctx context.Context, delay time.Duration) bool {
		if delay == 0 {
			return ctx.Err() == nil
		}
		if delay != deviceTelemetryInterval {
			t.Errorf("device cadence = %s, want %s", delay, deviceTelemetryInterval)
			return false
		}
		select {
		case <-ticks:
			return true
		case <-ctx.Done():
			return false
		}
	}

	if _, metrics := sampler.Sample(time.Time{}); len(metrics) != 0 || collector.count.Load() != 0 {
		t.Fatalf("disabled sampler emitted/worked: metrics=%v calls=%d", metrics, collector.count.Load())
	}

	saveDeviceTelemetryTestPolicy(t, stateDir, true)
	if _, metrics := sampler.Sample(time.Time{}); len(metrics) != 0 {
		t.Fatalf("sampler emitted before its first collection: %v", metrics)
	}
	workerDone := deviceTelemetryTestWorkerDone(sampler)
	select {
	case sampledAt := <-collector.calls:
		if !sampledAt.Equal(time.Unix(100, 0)) {
			t.Fatalf("collector time = %s", sampledAt)
		}
	case <-time.After(time.Second):
		t.Fatal("signed opt-in did not start device collection")
	}
	collector.results <- deviceTelemetryTestMetrics(12)
	<-collector.returned

	var metrics map[string]any
	waitForDeviceTelemetryTest(t, func() bool {
		_, metrics = sampler.Sample(time.Time{})
		return len(metrics) == 2
	})
	inventory, inventoryOK := metrics[telemetrymetric.DeviceInventoryKey].(devicemetric.InventoryMetric)
	samples, samplesOK := metrics[telemetrymetric.DeviceSamplesKey].(devicemetric.SamplesMetric)
	if !inventoryOK || !samplesOK || devicemetric.ValidatePair(inventory, samples) != nil {
		t.Fatalf("device metric pair = %#v / %#v", metrics[telemetrymetric.DeviceInventoryKey], metrics[telemetrymetric.DeviceSamplesKey])
	}
	waitForDeviceTelemetryTest(t, func() bool { return completionKicks.Load() == 1 })
	if samples.SampledAt != collectedAt.Format(time.RFC3339Nano) {
		t.Fatalf("first collection timestamp/kicks = %q/%d", samples.SampledAt, completionKicks.Load())
	}

	// Returned snapshots must not alias the cadence worker's cache.
	inventory.Devices[0].Label = "mutated"
	samples.Samples[0].Values[devicemetric.DiskReadBytesPerSecond] = 99
	_, next := sampler.Sample(time.Time{})
	if got := next[telemetrymetric.DeviceInventoryKey].(devicemetric.InventoryMetric).Devices[0].Label; got != "disk-a" {
		t.Fatalf("inventory snapshot aliased caller mutation: %q", got)
	}
	if got := next[telemetrymetric.DeviceSamplesKey].(devicemetric.SamplesMetric).Samples[0].Values[devicemetric.DiskReadBytesPerSecond]; got != 12 {
		t.Fatalf("numeric snapshot aliased caller mutation: %v", got)
	}

	// Collection cadence advances independently of heartbeat calls.
	collectedAt = collectedAt.Add(deviceTelemetryInterval)
	ticks <- struct{}{}
	select {
	case <-collector.calls:
	case <-time.After(time.Second):
		t.Fatal("device cadence did not trigger a second collection")
	}
	collector.results <- deviceTelemetryTestMetrics(0)
	<-collector.returned
	waitForDeviceTelemetryTest(t, func() bool {
		_, next = sampler.Sample(time.Time{})
		metric, ok := next[telemetrymetric.DeviceSamplesKey].(devicemetric.SamplesMetric)
		return ok && len(metric.Samples) == 1 &&
			metric.Samples[0].Values[devicemetric.DiskReadBytesPerSecond] == 0
	})
	waitForDeviceTelemetryTest(t, func() bool { return completionKicks.Load() == 2 })
	if completionKicks.Load() != 2 {
		t.Fatalf("completion kicks = %d, want one per collection", completionKicks.Load())
	}

	saveDeviceTelemetryTestPolicy(t, stateDir, false)
	if _, metrics := sampler.Sample(time.Time{}); len(metrics) != 0 {
		t.Fatalf("signed omission retained device metrics: %v", metrics)
	}
	if active, ready := deviceTelemetryTestState(sampler); active || ready {
		t.Fatalf("signed omission retained scheduler state: active=%t ready=%t", active, ready)
	}
	if collector.resets.Load() != 1 {
		t.Fatalf("signed removal baseline resets = %d, want 1", collector.resets.Load())
	}
	select {
	case <-workerDone:
	case <-time.After(time.Second):
		t.Fatal("disabled device scheduler did not stop")
	}
}

func TestDeviceTelemetrySampler_CancelFencesStaleCompletion(t *testing.T) {
	stateDir := t.TempDir()
	saveDeviceTelemetryTestPolicy(t, stateDir, true)
	collector := newDeviceTelemetryTestCollector(true)
	sampler := newDeviceTelemetrySampler(stateDir)
	sampler.collector = collector
	sampler.wait = waitDeviceTelemetryDelay

	_, _ = sampler.Sample(time.Time{})
	workerDone := deviceTelemetryTestWorkerDone(sampler)
	select {
	case <-collector.calls:
	case <-time.After(time.Second):
		t.Fatal("enabled sampler did not start collection")
	}
	saveDeviceTelemetryTestPolicy(t, stateDir, false)
	if _, metrics := sampler.Sample(time.Time{}); len(metrics) != 0 {
		t.Fatalf("disabled sampler emitted metrics: %v", metrics)
	}

	collector.results <- deviceTelemetryTestMetrics(33)
	<-collector.returned
	select {
	case <-workerDone:
	case <-time.After(time.Second):
		t.Fatal("stale provider completion did not unwind")
	}
	_, metrics := sampler.Sample(time.Time{})
	active, ready := deviceTelemetryTestState(sampler)
	if len(metrics) != 0 || active || ready {
		t.Fatalf("stale provider completion republished: metrics=%v active=%t ready=%t", metrics, active, ready)
	}
}
