package agent

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kunorikiku/yet-another-overlay-generator/internal/probemetric"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/probepolicy"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/runtimecontract"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/telemetrymetric"
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

func maximumAdmissionProbeRows() ([]probemetric.Result, []probemetric.Result) {
	host := strings.Repeat("a", 63) + "." + strings.Repeat("b", 63) + "." + strings.Repeat("c", 63) + "." + strings.Repeat("d", 61)
	baseTime := time.Date(2026, time.July, 16, 10, 20, 30, 123456789, time.UTC)
	latency := 4999.9
	latest := make([]probemetric.Result, 0, probepolicy.MaxProbes)
	for i := 0; i < probepolicy.MaxProbes; i++ {
		latest = append(latest, probemetric.Result{
			ID:   strings.Repeat("p", 60) + fmt.Sprintf("%03d", i),
			Type: "tcp", Host: host, Port: 65535,
			Status: probemetric.StatusSuccess, LatencyMS: &latency,
			CheckedAt: baseTime.Add(time.Duration(i) * time.Second).Format(time.RFC3339Nano),
		})
	}
	samples := make([]probemetric.Result, 0, probemetric.MaxRecentSamples)
	for i := 0; i < probemetric.MaxRecentSamples; i++ {
		result := latest[i%len(latest)]
		result.CheckedAt = baseTime.Add(time.Duration(100+i) * time.Second).Format(time.RFC3339Nano)
		result.IntervalMS = int64(probepolicy.MaxIntervalSeconds) * int64(time.Second/time.Millisecond)
		samples = append(samples, result)
	}
	return latest, samples
}

func admissionWireGuardPeers(count int) []wgPeerHealth {
	peers := make([]wgPeerHealth, count)
	endpointHost := strings.Repeat("e", 130) + ".example"
	for i := range peers {
		peers[i] = wgPeerHealth{
			Peer: fmt.Sprintf("peer-%06d", i), Interface: fmt.Sprintf("wg-%08x", i),
			Endpoint:      fmt.Sprintf("%s:%d", endpointHost, 10000+i),
			LastHandshake: int64(1_752_659_200 + i), Status: "stale",
		}
	}
	return peers
}

func decodeProbeMetricForAdmissionTest(t *testing.T, value any) []probemetric.Result {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return probemetric.DecodeArray(raw, probemetric.MaxRecentSamples, true)
}

func TestTelemetrySequencer_AdaptiveAdmissionRetainsLargestNewestProbeSuffix(t *testing.T) {
	latest, samples := maximumAdmissionProbeRows()
	peers := admissionWireGuardPeers(180)
	metrics := map[string]any{
		telemetrymetric.ProbeResultsKey:   latest,
		telemetrymetric.ProbeSamplesKey:   samples,
		telemetrymetric.WireGuardPeersKey: peers,
		telemetrymetric.ResourceKey:       hostResource{Load1: 1.25, Load5: 1, Load15: 0.75, MemTotalKB: 1024, MemAvailKB: 512},
	}
	initialRaw, err := json.Marshal(metrics)
	if err != nil {
		t.Fatal(err)
	}
	if len(initialRaw) <= telemetryprotocol.MaxMetricsBytes {
		t.Fatalf("quantified fixture is only %d bytes; want over %d", len(initialRaw), telemetryprotocol.MaxMetricsBytes)
	}
	withoutSamples := make(map[string]any, len(metrics)-1)
	for key, value := range metrics {
		if key != telemetrymetric.ProbeSamplesKey {
			withoutSamples[key] = value
		}
	}
	baseRaw, err := json.Marshal(withoutSamples)
	if err != nil {
		t.Fatal(err)
	}
	if len(baseRaw) > telemetryprotocol.MaxMetricsBytes {
		t.Fatalf("180-peer core fixture is %d bytes; probe trimming could not be isolated", len(baseRaw))
	}
	sourceSamples := append([]probemetric.Result(nil), samples...)
	sourceLatest := append([]probemetric.Result(nil), latest...)

	sequencer := &telemetrySequencer{bootID: "00112233445566778899aabbccddeeff"}
	snapshot, err := sequencer.snapshot(nil, metrics, time.Now(), 30*time.Second)
	if err != nil {
		t.Fatalf("adaptive snapshot with %d probes/%d peers: %v", len(latest), len(peers), err)
	}
	admittedRaw, err := json.Marshal(snapshot.Metrics)
	if err != nil {
		t.Fatal(err)
	}
	if len(admittedRaw) > telemetryprotocol.MaxMetricsBytes {
		t.Fatalf("admitted metrics = %d bytes, limit %d", len(admittedRaw), telemetryprotocol.MaxMetricsBytes)
	}
	if _, ok := snapshot.Metrics[telemetrymetric.WireGuardPeersKey]; !ok {
		t.Fatal("180-peer detail was shed even though trimming recent attempts was sufficient")
	}
	retained := decodeProbeMetricForAdmissionTest(t, snapshot.Metrics[telemetrymetric.ProbeSamplesKey])
	if len(retained) == 0 || len(retained) >= len(samples) {
		t.Fatalf("retained probe suffix = %d, want 1..%d", len(retained), len(samples)-1)
	}
	wantSuffix := samples[len(samples)-len(retained):]
	if !reflect.DeepEqual(retained, wantSuffix) {
		t.Fatalf("retained attempts are not the newest suffix: first=%+v want=%+v", retained[0], wantSuffix[0])
	}
	oneMore := make(map[string]any, len(metrics))
	for key, value := range metrics {
		oneMore[key] = value
	}
	oneMore[telemetrymetric.ProbeSamplesKey] = samples[len(samples)-len(retained)-1:]
	oneMoreRaw, err := json.Marshal(oneMore)
	if err != nil {
		t.Fatal(err)
	}
	if len(oneMoreRaw) <= telemetryprotocol.MaxMetricsBytes {
		t.Fatalf("admission retained only %d samples although %d fit in %d bytes", len(retained), len(retained)+1, len(oneMoreRaw))
	}
	if gotLatest := decodeProbeMetricForAdmissionTest(t, snapshot.Metrics[telemetrymetric.ProbeResultsKey]); !reflect.DeepEqual(gotLatest, latest) {
		t.Fatalf("probe_results changed during admission: got=%+v want=%+v", gotLatest, latest)
	}
	if got := metrics[telemetrymetric.ProbeSamplesKey].([]probemetric.Result); !reflect.DeepEqual(got, sourceSamples) {
		t.Fatal("adaptive admission mutated the sampler-owned probe sample window")
	}
	if got := metrics[telemetrymetric.ProbeResultsKey].([]probemetric.Result); !reflect.DeepEqual(got, sourceLatest) {
		t.Fatal("adaptive admission mutated probe_results")
	}
	if got := metrics[telemetrymetric.WireGuardPeersKey].([]wgPeerHealth); len(got) != len(peers) {
		t.Fatalf("adaptive admission mutated source WireGuard peers: %d != %d", len(got), len(peers))
	}
}

func TestTelemetrySequencer_AdaptiveAdmissionOmitsProbeSamplesWhenNoEntryFits(t *testing.T) {
	_, samples := maximumAdmissionProbeRows()
	emptyCoreRaw, err := json.Marshal(map[string]any{"core": ""})
	if err != nil {
		t.Fatal(err)
	}
	core := strings.Repeat("x", telemetryprotocol.MaxMetricsBytes-len(emptyCoreRaw))
	metrics := map[string]any{"core": core, telemetrymetric.ProbeSamplesKey: samples[:1]}
	coreRaw, err := json.Marshal(map[string]any{"core": core})
	if err != nil {
		t.Fatal(err)
	}
	if len(coreRaw) != telemetryprotocol.MaxMetricsBytes {
		t.Fatalf("core fixture = %d bytes, want exactly %d", len(coreRaw), telemetryprotocol.MaxMetricsBytes)
	}

	sequencer := &telemetrySequencer{bootID: "00112233445566778899aabbccddeeff"}
	snapshot, err := sequencer.snapshot(nil, metrics, time.Now(), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := snapshot.Metrics[telemetrymetric.ProbeSamplesKey]; ok {
		t.Fatal("probe_samples remained when even one completed attempt could not fit")
	}
	if snapshot.Metrics["core"] != core {
		t.Fatal("core metric changed while omitting probe_samples")
	}
	if got := metrics[telemetrymetric.ProbeSamplesKey].([]probemetric.Result); len(got) != 1 || !reflect.DeepEqual(got[0], samples[0]) {
		t.Fatal("zero-fit admission mutated its source sample")
	}
}

func TestTelemetrySequencer_AdaptiveAdmissionShedsOversizedWireGuardDetail(t *testing.T) {
	peers := admissionWireGuardPeers(400)
	metrics := map[string]any{
		telemetrymetric.ResourceKey:       hostResource{Load1: 0.5, Load5: 0.25, Load15: 0.1},
		telemetrymetric.WireGuardPeersKey: peers,
	}
	initialRaw, err := json.Marshal(metrics)
	if err != nil {
		t.Fatal(err)
	}
	if len(initialRaw) <= telemetryprotocol.MaxMetricsBytes {
		t.Fatalf("WireGuard fixture = %d bytes, want over %d", len(initialRaw), telemetryprotocol.MaxMetricsBytes)
	}
	conditions := []runtimecontract.Condition{{
		Type: runtimecontract.ConditionTypeWireGuard, Status: runtimecontract.ConditionStatusWarn,
		Reason: "SomePeersDown", Message: "aggregate remains",
	}}
	sequencer := &telemetrySequencer{bootID: "00112233445566778899aabbccddeeff"}
	snapshot, err := sequencer.snapshot(conditions, metrics, time.Now(), 30*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := snapshot.Metrics[telemetrymetric.WireGuardPeersKey]; ok {
		t.Fatal("oversized best-effort WireGuard detail was not shed")
	}
	if _, ok := snapshot.Metrics[telemetrymetric.ResourceKey]; !ok {
		t.Fatal("core resource metric was lost with WireGuard detail")
	}
	if len(snapshot.Conditions) != 1 || snapshot.Conditions[0].Type != runtimecontract.ConditionTypeWireGuard || snapshot.Conditions[0].Message != "aggregate remains" {
		t.Fatalf("aggregate WireGuard condition changed: %+v", snapshot.Conditions)
	}
	if got := metrics[telemetrymetric.WireGuardPeersKey].([]wgPeerHealth); len(got) != len(peers) {
		t.Fatalf("WireGuard shedding mutated sampler source: %d != %d", len(got), len(peers))
	}
}

func TestTelemetrySequencer_GenericOversizeStillErrorsWithoutAllocatingSequence(t *testing.T) {
	sequencer := &telemetrySequencer{bootID: "00112233445566778899aabbccddeeff"}
	oversized := map[string]any{"blob": strings.Repeat("x", telemetryprotocol.MaxMetricsBytes)}
	if _, err := sequencer.snapshot(nil, oversized, time.Now(), time.Second); err == nil {
		t.Fatal("generic over-limit metric was admitted")
	}
	if sequencer.nextSequence != 0 {
		t.Fatalf("rejected snapshot advanced sequence to %d", sequencer.nextSequence)
	}
	accepted, err := sequencer.snapshot(nil, map[string]any{"core": "ok"}, time.Now(), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if accepted.Sequence != 1 || sequencer.nextSequence != 1 {
		t.Fatalf("first accepted sequence = %d next=%d, want 1/1", accepted.Sequence, sequencer.nextSequence)
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

type capabilityTransitionPoster struct {
	mu       sync.Mutex
	calls    []TelemetrySample
	receipts []bool
}

func (p *capabilityTransitionPoster) PostTelemetry(sample TelemetrySample) (TelemetryReceipt, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls = append(p.calls, sample)
	index := len(p.calls) - 1
	enabled := index < len(p.receipts) && p.receipts[index]
	return TelemetryReceipt{
		BootID: sample.BootID, AcknowledgedSequence: sample.Sequence,
		ReceivedAt: time.Now().UTC(), Reliable: true, ProbeSamplesV1: enabled,
	}, nil
}

func TestTelemetryUploader_ProbeSamplesCapabilityEnablesAndRollsBack(t *testing.T) {
	poster := &capabilityTransitionPoster{receipts: []bool{true, false, false}}
	queue := newTelemetryReplayQueue()
	for sequence := uint64(1); sequence <= 3; sequence++ {
		sample := reliableTestSample(sequence)
		sample.Metrics = map[string]any{
			"sequence": sequence,
			probemetric.SamplesMetricKey: []probemetric.Result{{
				ID: "dns", Type: "icmp", Host: "resolver.example", Status: probemetric.StatusFailure,
				CheckedAt: sample.SampledAt.Format(time.RFC3339Nano), FailureReason: probemetric.FailureTimeout,
			}},
		}
		queue.enqueue(sample)
	}

	var enableCallbacks atomic.Int32
	var disableCallbacks atomic.Int32
	capabilities := newTelemetryCapabilityState(
		func() { enableCallbacks.Add(1) },
		func() { disableCallbacks.Add(1) },
	)
	done := make(chan struct{})
	stopped := make(chan struct{})
	go func() {
		runTelemetryUploaderWithCapabilities(
			poster, queue, done, &telemetryLogger{w: &bytes.Buffer{}}, capabilities,
			func(int, TelemetrySample) time.Duration { return 0 },
		)
		close(stopped)
	}()
	waitForTelemetryTest(t, func() bool {
		_, pending := queue.front()
		return !pending
	})
	close(done)
	select {
	case <-stopped:
	case <-time.After(3 * time.Second):
		t.Fatal("capability uploader did not stop")
	}

	poster.mu.Lock()
	calls := append([]TelemetrySample(nil), poster.calls...)
	poster.mu.Unlock()
	if len(calls) != 3 {
		t.Fatalf("calls = %d, want 3", len(calls))
	}
	_, initialSamples := calls[0].Metrics[probemetric.SamplesMetricKey]
	_, enabledSamples := calls[1].Metrics[probemetric.SamplesMetricKey]
	_, rollbackSamples := calls[2].Metrics[probemetric.SamplesMetricKey]
	if initialSamples || !enabledSamples || rollbackSamples {
		t.Fatalf("probe_samples presence = initial:%v enabled:%v rollback:%v", initialSamples, enabledSamples, rollbackSamples)
	}
	if enableCallbacks.Load() != 1 || disableCallbacks.Load() != 1 || capabilities.probeSamplesEnabled() {
		t.Fatalf("capability transitions: enable=%d disable=%d final-enabled=%v", enableCallbacks.Load(), disableCallbacks.Load(), capabilities.probeSamplesEnabled())
	}
	for index, call := range calls {
		if call.Sequence != uint64(index+1) {
			t.Fatalf("call %d sequence=%d", index, call.Sequence)
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

func (*countingReliableSampler) MetricDefinitions() []telemetrymetric.Definition {
	return []telemetrymetric.Definition{testMetricDefinition("count")}
}

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

type capabilityHeartbeatPoster struct {
	probeSamplesV1 bool
	calls          chan TelemetrySample
}

func (*capabilityHeartbeatPoster) Telemetry([]runtimecontract.Condition, map[string]any) error {
	return nil
}

func (p *capabilityHeartbeatPoster) PostTelemetry(sample TelemetrySample) (TelemetryReceipt, error) {
	p.calls <- sample
	return TelemetryReceipt{
		BootID: sample.BootID, AcknowledgedSequence: sample.Sequence,
		ReceivedAt: time.Now().UTC(), Reliable: true, ProbeSamplesV1: p.probeSamplesV1,
	}, nil
}

type scriptedCapabilityHeartbeatPoster struct {
	mu       sync.Mutex
	receipts []bool
	calls    chan TelemetrySample
}

func (*scriptedCapabilityHeartbeatPoster) Telemetry([]runtimecontract.Condition, map[string]any) error {
	return nil
}

func (p *scriptedCapabilityHeartbeatPoster) PostTelemetry(sample TelemetrySample) (TelemetryReceipt, error) {
	p.mu.Lock()
	// Consume from the front without retaining an index that races the buffered call notification.
	enabled := false
	if len(p.receipts) > 0 {
		enabled = p.receipts[0]
		p.receipts = p.receipts[1:]
	}
	p.mu.Unlock()
	p.calls <- sample
	return TelemetryReceipt{
		BootID: sample.BootID, AcknowledgedSequence: sample.Sequence,
		ReceivedAt: time.Now().UTC(), Reliable: true, ProbeSamplesV1: enabled,
	}, nil
}

type completionKickHeartbeatSampler struct {
	mu            sync.Mutex
	kick          func()
	calls         int
	active        int
	maxActive     int
	secondStarted chan struct{}
	releaseSecond chan struct{}
	secondOnce    sync.Once
}

func (*completionKickHeartbeatSampler) Name() string { return "completion-kick" }

func (*completionKickHeartbeatSampler) MetricDefinitions() []telemetrymetric.Definition {
	return []telemetrymetric.Definition{telemetrymetric.ProbeResults, telemetrymetric.ProbeSamples}
}

func (s *completionKickHeartbeatSampler) setProbeCompletionKick(kick func()) {
	s.mu.Lock()
	s.kick = kick
	s.mu.Unlock()
}

func (s *completionKickHeartbeatSampler) trigger(count int) {
	s.mu.Lock()
	kick := s.kick
	s.mu.Unlock()
	for i := 0; i < count; i++ {
		if kick != nil {
			kick()
		}
	}
}

func (s *completionKickHeartbeatSampler) Sample(time.Time) ([]runtimecontract.Condition, map[string]any) {
	s.mu.Lock()
	s.calls++
	call := s.calls
	s.active++
	if s.active > s.maxActive {
		s.maxActive = s.active
	}
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		s.active--
		s.mu.Unlock()
	}()
	if call == 2 && s.secondStarted != nil {
		s.secondOnce.Do(func() { close(s.secondStarted) })
		<-s.releaseSecond
	}
	result := probemetric.Result{
		ID: "dns", Type: "icmp", Host: "resolver.example", Status: probemetric.StatusFailure,
		CheckedAt: time.Now().UTC().Format(time.RFC3339Nano), FailureReason: probemetric.FailureTimeout,
		IntervalMS: 30_000,
	}
	latest := result
	latest.IntervalMS = 0
	return nil, map[string]any{
		probemetric.LatestMetricKey:  []probemetric.Result{latest},
		probemetric.SamplesMetricKey: []probemetric.Result{result},
	}
}

func (s *completionKickHeartbeatSampler) snapshotCounts() (calls, maxActive int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls, s.maxActive
}

func TestRunReliableHeartbeat_NoCapabilityKeepsConfiguredCadence(t *testing.T) {
	poster := &capabilityHeartbeatPoster{calls: make(chan TelemetrySample, 8)}
	sampler := &completionKickHeartbeatSampler{}
	done := make(chan struct{})
	stopped := make(chan struct{})
	go func() {
		RunHeartbeat(poster, NewTelemetryForTest(sampler), time.Hour, nil, done, io.Discard)
		close(stopped)
	}()

	var first TelemetrySample
	select {
	case first = <-poster.calls:
	case <-time.After(3 * time.Second):
		t.Fatal("initial heartbeat was not posted")
	}
	if _, leaked := first.Metrics[probemetric.SamplesMetricKey]; leaked {
		t.Fatalf("initial heartbeat sent probe_samples before capability negotiation: %+v", first.Metrics)
	}
	sampler.trigger(100)
	time.Sleep(100 * time.Millisecond)
	if calls, _ := sampler.snapshotCounts(); calls != 1 {
		t.Fatalf("no-capability completion kicks changed one-hour cadence: samples=%d", calls)
	}
	select {
	case extra := <-poster.calls:
		t.Fatalf("no-capability controller received an early heartbeat: %+v", extra)
	default:
	}
	close(done)
	select {
	case <-stopped:
	case <-time.After(3 * time.Second):
		t.Fatal("no-capability heartbeat did not stop")
	}
}

func TestRunReliableHeartbeat_CapabilityEnablesImmediateCoalescedNonoverlappingFlush(t *testing.T) {
	poster := &capabilityHeartbeatPoster{probeSamplesV1: true, calls: make(chan TelemetrySample, 16)}
	sampler := &completionKickHeartbeatSampler{
		secondStarted: make(chan struct{}),
		releaseSecond: make(chan struct{}),
	}
	done := make(chan struct{})
	stopped := make(chan struct{})
	go func() {
		RunHeartbeat(poster, NewTelemetryForTest(sampler), time.Hour, nil, done, io.Discard)
		close(stopped)
	}()

	var first TelemetrySample
	select {
	case first = <-poster.calls:
	case <-time.After(3 * time.Second):
		t.Fatal("initial capability heartbeat was not posted")
	}
	if _, leaked := first.Metrics[probemetric.SamplesMetricKey]; leaked {
		t.Fatalf("pre-negotiation heartbeat sent probe_samples: %+v", first.Metrics)
	}
	select {
	case <-sampler.secondStarted:
	case <-time.After(3 * time.Second):
		t.Fatal("capability receipt did not trigger an immediate collection")
	}
	// While that collection is deliberately blocked, a completion burst can leave at most one
	// additional collection pending. Collection itself remains single-goroutine/non-overlapping.
	sampler.trigger(100)
	close(sampler.releaseSecond)

	select {
	case second := <-poster.calls:
		if _, ok := second.Metrics[probemetric.SamplesMetricKey]; !ok {
			t.Fatalf("post-negotiation heartbeat omitted probe_samples: %+v", second.Metrics)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("second heartbeat was not posted")
	}
	select {
	case third := <-poster.calls:
		if _, ok := third.Metrics[probemetric.SamplesMetricKey]; !ok {
			t.Fatalf("completion-flush heartbeat omitted probe_samples: %+v", third.Metrics)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("coalesced completion heartbeat was not posted")
	}
	time.Sleep(100 * time.Millisecond)
	if calls, maxActive := sampler.snapshotCounts(); calls != 3 || maxActive != 1 {
		t.Fatalf("collection calls/max-active = %d/%d, want 3/1", calls, maxActive)
	}
	select {
	case extra := <-poster.calls:
		t.Fatalf("completion burst was not coalesced: extra=%+v", extra)
	default:
	}
	close(done)
	select {
	case <-stopped:
	case <-time.After(3 * time.Second):
		t.Fatal("capability heartbeat did not stop")
	}
}

func TestRunReliableHeartbeat_RollbackDisablesQueuedSamplesAndCompletionKicks(t *testing.T) {
	poster := &scriptedCapabilityHeartbeatPoster{
		receipts: []bool{true, false, false},
		calls:    make(chan TelemetrySample, 8),
	}
	sampler := &completionKickHeartbeatSampler{}
	done := make(chan struct{})
	stopped := make(chan struct{})
	go func() {
		RunHeartbeat(poster, NewTelemetryForTest(sampler), time.Hour, nil, done, io.Discard)
		close(stopped)
	}()

	var first TelemetrySample
	select {
	case first = <-poster.calls:
	case <-time.After(3 * time.Second):
		t.Fatal("initial rollback-test heartbeat was not posted")
	}
	if _, ok := first.Metrics[probemetric.SamplesMetricKey]; ok {
		t.Fatalf("initial heartbeat sent unnegotiated probe samples: %+v", first.Metrics)
	}
	var second TelemetrySample
	select {
	case second = <-poster.calls:
	case <-time.After(3 * time.Second):
		t.Fatal("capability did not trigger the second rollback-test heartbeat")
	}
	if _, ok := second.Metrics[probemetric.SamplesMetricKey]; !ok {
		t.Fatalf("capability-enabled heartbeat omitted probe samples: %+v", second.Metrics)
	}
	// The second receipt represents a controller rollback. It must schedule one immediate clean beat
	// rather than leaving the discovery request's additive key in an rc.9 latest-value overlay until
	// the next (possibly very slow) configured interval.
	var third TelemetrySample
	select {
	case third = <-poster.calls:
	case <-time.After(3 * time.Second):
		t.Fatal("post-rollback cleanup heartbeat was not posted")
	}
	if _, ok := third.Metrics[probemetric.SamplesMetricKey]; ok {
		t.Fatalf("queued heartbeat retained probe samples after controller rollback: %+v", third.Metrics)
	}

	// Reaching the third POST proves the sequential uploader already processed the second receipt and
	// disabled the capability. Probe completion nudges must now leave the one-hour cadence unchanged.
	sampler.trigger(100)
	time.Sleep(100 * time.Millisecond)
	if calls, _ := sampler.snapshotCounts(); calls != 3 {
		t.Fatalf("rollback left probe-driven heartbeat kicks enabled: samples=%d", calls)
	}
	select {
	case extra := <-poster.calls:
		t.Fatalf("rollback controller received a probe-driven heartbeat: %+v", extra)
	default:
	}
	close(done)
	select {
	case <-stopped:
	case <-time.After(3 * time.Second):
		t.Fatal("rollback heartbeat did not stop")
	}
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
