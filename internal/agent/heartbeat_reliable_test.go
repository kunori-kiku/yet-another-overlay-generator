package agent

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kunorikiku/yet-another-overlay-generator/internal/runtimecontract"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/telemetryprotocol"
)

func reliableTestSample(sequence uint64) TelemetrySample {
	return TelemetrySample{
		BootID: "00112233445566778899aabbccddeeff", Sequence: sequence,
		SampledAt: time.Date(2026, 7, 16, 10, 0, int(sequence), 0, time.UTC),
		Interval:  30 * time.Second,
		Metrics:   map[string]any{"sequence": sequence},
	}
}

func waitForTelemetryTest(t *testing.T, condition func() bool) {
	t.Helper()
	deadline := time.NewTimer(3 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(time.Millisecond)
	defer tick.Stop()
	for {
		if condition() {
			return
		}
		select {
		case <-deadline.C:
			t.Fatal("timed out waiting for telemetry condition")
		case <-tick.C:
		}
	}
}

func TestTelemetrySequencer_ProducesImmutableBoundedSnapshots(t *testing.T) {
	sequencer := &telemetrySequencer{bootID: "00112233445566778899aabbccddeeff"}
	conditions := []runtimecontract.Condition{{Type: "test", Reason: "before"}}
	metrics := map[string]any{
		"counter": uint64(^uint64(0)),
		"nested":  map[string]any{"value": "before"},
	}
	sample, err := sequencer.snapshot(conditions, metrics, time.Now(), 17*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	conditions[0].Reason = "after"
	metrics["counter"] = uint64(0)
	metrics["nested"].(map[string]any)["value"] = "after"
	if sample.Conditions[0].Reason != "before" || sample.Metrics["nested"].(map[string]any)["value"] != "before" {
		t.Fatalf("queued sample aliased mutable sampler output: %+v", sample)
	}
	raw, err := json.Marshal(sample.Metrics["counter"])
	if err != nil || string(raw) != "18446744073709551615" {
		t.Fatalf("large integer lost precision: raw=%s err=%v", raw, err)
	}
	if sample.Interval != 17*time.Second {
		t.Fatalf("sample interval=%v", sample.Interval)
	}

	tooMany := make(map[string]any, telemetryprotocol.MaxMetrics+1)
	for i := 0; i < telemetryprotocol.MaxMetrics+1; i++ {
		tooMany[string(rune('a'+i))] = i
	}
	if _, err := sequencer.snapshot(nil, tooMany, time.Now(), time.Second); err == nil {
		t.Fatal("over-key-limit metrics were queued")
	}
	if _, err := sequencer.snapshot(nil, map[string]any{"blob": strings.Repeat("x", telemetryprotocol.MaxMetricsBytes)}, time.Now(), time.Second); err == nil {
		t.Fatal("over-byte-limit metrics were queued")
	}
}

func TestTelemetryReplayQueue_IsBoundedAndSequenceStaysMonotonic(t *testing.T) {
	queue := newTelemetryReplayQueue()
	drops := 0
	for sequence := uint64(1); sequence <= telemetryReplayCapacity+8; sequence++ {
		if queue.enqueue(reliableTestSample(sequence)) {
			drops++
		}
	}
	if len(queue.pending) != telemetryReplayCapacity || drops != 8 {
		t.Fatalf("pending=%d drops=%d, want %d/8", len(queue.pending), drops, telemetryReplayCapacity)
	}
	if queue.pending[0].Sequence != 9 || queue.pending[len(queue.pending)-1].Sequence != telemetryReplayCapacity+8 {
		t.Fatalf("retained sequence window = %d..%d", queue.pending[0].Sequence, queue.pending[len(queue.pending)-1].Sequence)
	}
	for failures := 1; failures < 100; failures++ {
		delay := telemetryRetryDelay(failures, reliableTestSample(1))
		if delay < telemetryRetryBase || delay > telemetryRetryMax {
			t.Fatalf("retry %d delay=%v outside bounds", failures, delay)
		}
	}
	if got := telemetryRetryDelay(100, reliableTestSample(1)); got != telemetryRetryMax {
		t.Fatalf("capped retry delay=%v, want %v", got, telemetryRetryMax)
	}
}

type retryTestPoster struct {
	mu             sync.Mutex
	failures       int
	permanentFirst bool
	calls          []TelemetrySample
	delivered      chan TelemetrySample
}

func (p *retryTestPoster) PostTelemetry(sample TelemetrySample) (TelemetryReceipt, error) {
	p.mu.Lock()
	p.calls = append(p.calls, sample)
	call := len(p.calls)
	if p.permanentFirst && call == 1 {
		p.mu.Unlock()
		return TelemetryReceipt{}, permanentTelemetry(errors.New("rejected"))
	}
	if p.failures > 0 {
		p.failures--
		p.mu.Unlock()
		return TelemetryReceipt{}, errors.New("temporary transport failure")
	}
	p.mu.Unlock()
	if p.delivered != nil {
		p.delivered <- sample
	}
	return TelemetryReceipt{
		BootID: sample.BootID, AcknowledgedSequence: sample.Sequence,
		ReceivedAt: time.Now().UTC(), Reliable: true,
	}, nil
}

func (p *retryTestPoster) callSnapshot() []TelemetrySample {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]TelemetrySample(nil), p.calls...)
}

func startTelemetryUploaderTest(t *testing.T, poster ReliableTelemetryPoster, queue *telemetryReplayQueue, delay func(int, TelemetrySample) time.Duration) func() {
	t.Helper()
	done := make(chan struct{})
	stopped := make(chan struct{})
	go func() {
		runTelemetryUploaderWithDelay(poster, queue, done, &telemetryLogger{w: &bytes.Buffer{}}, delay)
		close(stopped)
	}()
	return func() {
		close(done)
		select {
		case <-stopped:
		case <-time.After(3 * time.Second):
			t.Fatal("telemetry uploader did not stop")
		}
	}
}

func TestTelemetryUploader_RetryableFailureSurvivesMoreThanEightAttempts(t *testing.T) {
	poster := &retryTestPoster{failures: 9, delivered: make(chan TelemetrySample, 1)}
	queue := newTelemetryReplayQueue()
	sample := reliableTestSample(1)
	queue.enqueue(sample)
	stop := startTelemetryUploaderTest(t, poster, queue, func(int, TelemetrySample) time.Duration { return 0 })
	defer stop()

	select {
	case delivered := <-poster.delivered:
		if !sameTelemetrySample(delivered, sample) {
			t.Fatalf("delivered sample=%+v", delivered)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("retryable sample was dropped before recovery")
	}
	waitForTelemetryTest(t, func() bool {
		_, pending := queue.front()
		return !pending
	})
	calls := poster.callSnapshot()
	if len(calls) != 10 {
		t.Fatalf("calls=%d, want 10 (nine failures plus success)", len(calls))
	}
	for _, call := range calls {
		if !sameTelemetrySample(call, sample) || !call.SampledAt.Equal(sample.SampledAt) {
			t.Fatalf("retry changed immutable sample: %+v", call)
		}
	}
}

func TestTelemetryUploader_PermanentFailureAdvancesHead(t *testing.T) {
	poster := &retryTestPoster{permanentFirst: true, delivered: make(chan TelemetrySample, 2)}
	queue := newTelemetryReplayQueue()
	queue.enqueue(reliableTestSample(1))
	queue.enqueue(reliableTestSample(2))
	stop := startTelemetryUploaderTest(t, poster, queue, func(int, TelemetrySample) time.Duration { return 0 })
	defer stop()

	select {
	case delivered := <-poster.delivered:
		if delivered.Sequence != 2 {
			t.Fatalf("delivered sequence=%d, want 2", delivered.Sequence)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("permanent rejection blocked the next sample")
	}
	calls := poster.callSnapshot()
	if len(calls) < 2 || calls[0].Sequence != 1 || calls[1].Sequence != 2 {
		t.Fatalf("call order=%+v", calls)
	}
}

type blockingReplayPoster struct {
	mu        sync.Mutex
	calls     []TelemetrySample
	started   chan struct{}
	release   chan struct{}
	delivered chan TelemetrySample
	once      sync.Once
}

func (p *blockingReplayPoster) PostTelemetry(sample TelemetrySample) (TelemetryReceipt, error) {
	p.mu.Lock()
	p.calls = append(p.calls, sample)
	p.mu.Unlock()
	if sample.Sequence == 1 {
		p.once.Do(func() { close(p.started) })
		<-p.release
		return TelemetryReceipt{}, errors.New("lost response")
	}
	p.delivered <- sample
	return TelemetryReceipt{
		BootID: sample.BootID, AcknowledgedSequence: sample.Sequence,
		ReceivedAt: time.Now().UTC(), Reliable: true,
	}, nil
}

func TestTelemetryUploader_CapacityEvictionAdvancesRetryingHead(t *testing.T) {
	poster := &blockingReplayPoster{
		started: make(chan struct{}), release: make(chan struct{}), delivered: make(chan TelemetrySample, telemetryReplayCapacity),
	}
	queue := newTelemetryReplayQueue()
	queue.enqueue(reliableTestSample(1))
	stop := startTelemetryUploaderTest(t, poster, queue, func(int, TelemetrySample) time.Duration { return time.Hour })
	defer stop()
	select {
	case <-poster.started:
	case <-time.After(3 * time.Second):
		t.Fatal("uploader did not start the head sample")
	}
	for sequence := uint64(2); sequence <= 33; sequence++ {
		queue.enqueue(reliableTestSample(sequence))
	}
	if front, ok := queue.front(); !ok || front.Sequence != 2 {
		t.Fatalf("front after capacity eviction=%+v ok=%v, want sequence 2", front, ok)
	}
	close(poster.release)
	select {
	case delivered := <-poster.delivered:
		if delivered.Sequence != 2 {
			t.Fatalf("first post-eviction delivery=%d, want 2", delivered.Sequence)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("evicted in-flight head continued blocking replay")
	}
	poster.mu.Lock()
	calls := append([]TelemetrySample(nil), poster.calls...)
	poster.mu.Unlock()
	if len(calls) < 2 || calls[0].Sequence != 1 || calls[1].Sequence != 2 {
		t.Fatalf("call order=%+v", calls)
	}
}

type countingReliableSampler struct {
	calls atomic.Int64
}

func (*countingReliableSampler) Name() string { return "counting" }

func (s *countingReliableSampler) Sample(time.Time) ([]runtimecontract.Condition, map[string]any) {
	return nil, map[string]any{"count": s.calls.Add(1)}
}

type blockingHeartbeatPoster struct {
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

func (*blockingHeartbeatPoster) Telemetry([]runtimecontract.Condition, map[string]any) error {
	return nil
}

func (p *blockingHeartbeatPoster) PostTelemetry(TelemetrySample) (TelemetryReceipt, error) {
	p.once.Do(func() { close(p.started) })
	<-p.release
	return TelemetryReceipt{}, errors.New("controller unavailable")
}

func TestRunReliableHeartbeat_SamplingContinuesWhileUploadBlocks(t *testing.T) {
	poster := &blockingHeartbeatPoster{started: make(chan struct{}), release: make(chan struct{})}
	sampler := &countingReliableSampler{}
	telemetry := NewTelemetryForTest(sampler)
	kick := make(chan struct{}, 64)
	done := make(chan struct{})
	stopped := make(chan struct{})
	var stderr bytes.Buffer
	go func() {
		RunHeartbeat(poster, telemetry, time.Hour, kick, done, &stderr)
		close(stopped)
	}()
	select {
	case <-poster.started:
	case <-time.After(3 * time.Second):
		t.Fatal("initial telemetry upload did not start")
	}
	for i := 0; i < 40; i++ {
		kick <- struct{}{}
	}
	waitForTelemetryTest(t, func() bool { return sampler.calls.Load() >= 41 })
	close(done)
	close(poster.release)
	select {
	case <-stopped:
	case <-time.After(3 * time.Second):
		t.Fatal("heartbeat sampler loop did not stop")
	}
}
