package agent

import (
	"context"
	"sync"
	"time"

	"github.com/kunorikiku/yet-another-overlay-generator/internal/devicemetric"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/probepolicy"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/runtimecontract"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/telemetrymetric"
)

const deviceTelemetryInterval = 30 * time.Second

type deviceMetricsCollector interface {
	Collect(context.Context, time.Time) (devicemetric.InventoryMetric, devicemetric.SamplesMetric)
}

type deviceRateBaselineResetter interface {
	ResetRateBaselines()
}

// deviceTelemetrySampler keeps local hardware discovery independent from heartbeat upload. The
// signed last-known-good successor policy is still the sole activation boundary: Sample reconciles
// that durable authority, while one cancellable worker owns the fixed collection cadence. The same
// collector survives policy and heartbeat transitions so disk deltas and its one-provider-worker
// ceiling remain effective.
type deviceTelemetrySampler struct {
	stateDir  string
	collector deviceMetricsCollector
	now       func() time.Time
	wait      func(context.Context, time.Duration) bool

	mu             sync.Mutex
	authorized     bool
	active         bool
	generation     uint64
	cancel         context.CancelFunc
	workerDone     chan struct{}
	ready          bool
	inventory      devicemetric.InventoryMetric
	samples        devicemetric.SamplesMetric
	completionKick func()
}

func newDeviceTelemetrySampler(stateDir string) *deviceTelemetrySampler {
	return &deviceTelemetrySampler{
		stateDir:  stateDir,
		collector: newDeviceCollector(deviceCollectorDeps{}),
		now:       time.Now,
		wait:      waitDeviceTelemetryDelay,
	}
}

func (*deviceTelemetrySampler) Name() string { return "automatic-devices" }

func (*deviceTelemetrySampler) MetricDefinitions() []telemetrymetric.Definition {
	return []telemetrymetric.Definition{telemetrymetric.DeviceInventory, telemetrymetric.DeviceSamples}
}

func (s *deviceTelemetrySampler) setTelemetryCompletionKick(kick func()) {
	s.mu.Lock()
	s.completionKick = kick
	s.mu.Unlock()
}

func (s *deviceTelemetrySampler) Sample(time.Time) ([]runtimecontract.Condition, map[string]any) {
	state, err := LoadState(s.stateDir)
	if err != nil || state == nil {
		// Match active probes: a transient state-read failure cannot grant new authority or revoke the
		// last policy we already parsed. Keep the existing worker and expose only its bounded snapshot.
		return nil, s.snapshot()
	}
	if len(state.ActiveTelemetryPolicy) == 0 {
		s.deactivate()
		return nil, nil
	}
	policy, err := probepolicy.ParseActive(state.ActiveTelemetryPolicy)
	if err != nil || policy.Devices == nil {
		// A malformed durable value is not authority. A valid v1/probe-only policy is an explicit
		// absence of device authority and follows the same cancellation/clear path.
		s.deactivate()
		return nil, nil
	}

	s.activate()
	return nil, s.snapshot()
}

func (s *deviceTelemetrySampler) activate() {
	s.mu.Lock()
	s.authorized = true
	if s.active {
		s.mu.Unlock()
		return
	}
	s.generation++
	generation := s.generation
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	s.cancel = cancel
	s.workerDone = done
	s.active = true
	s.mu.Unlock()

	go s.run(ctx, generation, done)
}

func (s *deviceTelemetrySampler) deactivate() {
	s.mu.Lock()
	if !s.authorized && !s.active && s.cancel == nil && !s.ready {
		s.mu.Unlock()
		return
	}
	hadAuthority := s.authorized
	s.authorized = false
	s.generation++ // fence a provider that cannot return promptly after cancellation
	if s.cancel != nil {
		s.cancel()
	}
	s.active = false
	s.cancel = nil
	s.ready = false
	s.inventory = devicemetric.InventoryMetric{}
	s.samples = devicemetric.SamplesMetric{}
	s.mu.Unlock()
	if hadAuthority {
		if resetter, ok := s.collector.(deviceRateBaselineResetter); ok {
			resetter.ResetRateBaselines()
		}
	}
}

func (s *deviceTelemetrySampler) run(ctx context.Context, generation uint64, done chan struct{}) {
	panicked := true
	defer func() {
		_ = recover() // background telemetry must never take down the daemon
		s.mu.Lock()
		if s.workerDone == done {
			s.workerDone = nil
		}
		if s.generation == generation {
			s.active = false
			s.cancel = nil
			if panicked {
				s.ready = false
				s.inventory = devicemetric.InventoryMetric{}
				s.samples = devicemetric.SamplesMetric{}
			}
		}
		s.mu.Unlock()
		close(done)
	}()

	delay := time.Duration(0)
	for s.wait(ctx, delay) {
		collectedAt := s.now().UTC()
		inventory, samples := s.collector.Collect(ctx, collectedAt)
		if ctx.Err() != nil {
			panicked = false
			return
		}
		samples.SampledAt = collectedAt.Format(time.RFC3339Nano)
		if err := devicemetric.ValidatePair(inventory, samples); err != nil {
			s.clearSnapshot(generation)
			delay = deviceTelemetryInterval
			continue
		}

		s.mu.Lock()
		if s.generation != generation || !s.active || ctx.Err() != nil {
			s.mu.Unlock()
			panicked = false
			return
		}
		s.inventory = cloneDeviceInventory(inventory)
		s.samples = cloneDeviceSamples(samples)
		s.ready = true
		completionKick := s.completionKick
		s.mu.Unlock()
		if completionKick != nil {
			completionKick()
		}
		delay = deviceTelemetryInterval
	}
	panicked = false
}

func (s *deviceTelemetrySampler) clearSnapshot(generation uint64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.generation != generation || !s.active {
		return
	}
	s.ready = false
	s.inventory = devicemetric.InventoryMetric{}
	s.samples = devicemetric.SamplesMetric{}
}

func (s *deviceTelemetrySampler) snapshot() map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.active || !s.ready {
		return nil
	}
	return map[string]any{
		telemetrymetric.DeviceInventoryKey: cloneDeviceInventory(s.inventory),
		telemetrymetric.DeviceSamplesKey:   cloneDeviceSamples(s.samples),
	}
}

func waitDeviceTelemetryDelay(ctx context.Context, delay time.Duration) bool {
	if delay <= 0 {
		return ctx.Err() == nil
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-timer.C:
		return true
	case <-ctx.Done():
		return false
	}
}
