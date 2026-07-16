package agent

// heartbeat_reliable.go adds delivery reliability around the existing telemetry samplers while keeping
// the transport plain HTTP and the payload observability-only. Samples live only in this bounded memory
// queue; agent restart drops them, and controller acknowledgement state is likewise volatile by design.

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"sync"
	"sync/atomic"
	"time"

	"github.com/kunorikiku/yet-another-overlay-generator/internal/runtimecontract"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/telemetryprotocol"
)

const (
	telemetryReplayCapacity       = 32 // includes the sample currently being uploaded
	telemetryReplayMaxSampleBytes = 128 << 10
	telemetryRetryBase            = time.Second
	telemetryRetryMax             = 30 * time.Second
)

// TelemetrySample is one immutable observation in the agent's bounded replay queue. BootID scopes the
// monotonically increasing Sequence to this daemon process; SampledAt is the agent observation time.
// None of these fields are custody state or secret material.
type TelemetrySample struct {
	Conditions []runtimecontract.Condition
	Metrics    map[string]any
	BootID     string
	Sequence   uint64
	SampledAt  time.Time
	Interval   time.Duration
}

// TelemetryReceipt acknowledges delivery. Reliable=false means a legacy controller accepted the exact
// legacy JSON body but did not understand the optional metadata headers; rolling upgrades still work,
// but exact duplicate suppression is unavailable until the controller is upgraded.
type TelemetryReceipt struct {
	BootID               string
	AcknowledgedSequence uint64
	ReceivedAt           time.Time
	Reliable             bool
	Duplicate            bool
}

// ReliableTelemetryPoster is the optional protocol-v2 transport implemented by ControllerClient. The
// original TelemetryPoster remains unchanged so tests and alternate legacy posters keep working.
type ReliableTelemetryPoster interface {
	PostTelemetry(sample TelemetrySample) (TelemetryReceipt, error)
}

type permanentTelemetryError struct {
	err error
}

func (e *permanentTelemetryError) Error() string { return e.err.Error() }
func (e *permanentTelemetryError) Unwrap() error { return e.err }

func permanentTelemetry(err error) error {
	if err == nil {
		return nil
	}
	return &permanentTelemetryError{err: err}
}

func isPermanentTelemetryError(err error) bool {
	_, ok := err.(*permanentTelemetryError)
	return ok
}

type telemetryLogger struct {
	mu sync.Mutex
	w  io.Writer
}

func (l *telemetryLogger) printf(format string, args ...any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	fmt.Fprintf(l.w, format, args...)
}

func (l *telemetryLogger) println(args ...any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	fmt.Fprintln(l.w, args...)
}

type telemetrySequencer struct {
	bootID       string
	nextSequence uint64
}

func newTelemetrySequencer() *telemetrySequencer {
	return &telemetrySequencer{bootID: newTelemetryBootID()}
}

func (s *telemetrySequencer) snapshot(conditions []runtimecontract.Condition, metrics map[string]any, sampledAt time.Time, interval time.Duration) (TelemetrySample, error) {
	wire := struct {
		Conditions []runtimecontract.Condition `json:"conditions,omitempty"`
		Metrics    map[string]any              `json:"metrics,omitempty"`
	}{Conditions: conditions, Metrics: metrics}
	if len(metrics) > telemetryprotocol.MaxMetrics {
		return TelemetrySample{}, fmt.Errorf("telemetry metrics contain %d keys (limit %d)", len(metrics), telemetryprotocol.MaxMetrics)
	}
	metricsRaw, err := json.Marshal(metrics)
	if err != nil {
		return TelemetrySample{}, fmt.Errorf("marshal telemetry metrics: %w", err)
	}
	if len(metricsRaw) > telemetryprotocol.MaxMetricsBytes {
		return TelemetrySample{}, fmt.Errorf("telemetry metrics are %d bytes (limit %d)", len(metricsRaw), telemetryprotocol.MaxMetricsBytes)
	}
	raw, err := json.Marshal(wire)
	if err != nil {
		return TelemetrySample{}, fmt.Errorf("marshal telemetry snapshot: %w", err)
	}
	if len(raw) > telemetryReplayMaxSampleBytes {
		return TelemetrySample{}, fmt.Errorf("telemetry snapshot is %d bytes (limit %d)", len(raw), telemetryReplayMaxSampleBytes)
	}
	var immutable struct {
		Conditions []runtimecontract.Condition `json:"conditions,omitempty"`
		Metrics    map[string]any              `json:"metrics,omitempty"`
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&immutable); err != nil {
		return TelemetrySample{}, fmt.Errorf("copy telemetry snapshot: %w", err)
	}
	s.nextSequence++
	return TelemetrySample{
		Conditions: immutable.Conditions,
		Metrics:    immutable.Metrics,
		BootID:     s.bootID,
		Sequence:   s.nextSequence,
		SampledAt:  sampledAt.UTC(),
		Interval:   interval,
	}, nil
}

// telemetryReplayQueue is a mutex-owned bounded ring. Its front remains queued while an upload is in
// flight, so capacity is an exact total rather than "waiting plus one". A producer never waits: once
// full it evicts the oldest sample, including a currently retrying front. The uploader notices the
// identity change and advances instead of retrying an observation no longer retained.
type telemetryReplayQueue struct {
	mu      sync.Mutex
	pending []TelemetrySample
	changed chan struct{}
}

func newTelemetryReplayQueue() *telemetryReplayQueue {
	return &telemetryReplayQueue{
		pending: make([]TelemetrySample, 0, telemetryReplayCapacity),
		changed: make(chan struct{}, 1),
	}
}

func (q *telemetryReplayQueue) enqueue(sample TelemetrySample) (dropped bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if len(q.pending) == telemetryReplayCapacity {
		copy(q.pending, q.pending[1:])
		q.pending[len(q.pending)-1] = TelemetrySample{}
		q.pending = q.pending[:len(q.pending)-1]
		dropped = true
	}
	q.pending = append(q.pending, sample)
	q.signalLocked()
	return dropped
}

func (q *telemetryReplayQueue) front() (TelemetrySample, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if len(q.pending) == 0 {
		return TelemetrySample{}, false
	}
	return q.pending[0], true
}

func (q *telemetryReplayQueue) isFront(sample TelemetrySample) bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	return len(q.pending) > 0 && sameTelemetrySample(q.pending[0], sample)
}

func (q *telemetryReplayQueue) removeFront(sample TelemetrySample) bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	if len(q.pending) == 0 || !sameTelemetrySample(q.pending[0], sample) {
		return false
	}
	copy(q.pending, q.pending[1:])
	q.pending[len(q.pending)-1] = TelemetrySample{}
	q.pending = q.pending[:len(q.pending)-1]
	q.signalLocked()
	return true
}

func (q *telemetryReplayQueue) signalLocked() {
	select {
	case q.changed <- struct{}{}:
	default:
	}
}

func sameTelemetrySample(a, b TelemetrySample) bool {
	return a.BootID == b.BootID && a.Sequence == b.Sequence
}

func postTelemetrySafely(poster ReliableTelemetryPoster, sample TelemetrySample) (receipt TelemetryReceipt, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("agent: telemetry poster panic: %v", recovered)
		}
	}()
	return poster.PostTelemetry(sample)
}

func runTelemetryUploader(poster ReliableTelemetryPoster, queue *telemetryReplayQueue, done <-chan struct{}, log *telemetryLogger) {
	runTelemetryUploaderWithDelay(poster, queue, done, log, telemetryRetryDelay)
}

func runTelemetryUploaderWithDelay(poster ReliableTelemetryPoster, queue *telemetryReplayQueue, done <-chan struct{}, log *telemetryLogger, retryDelay func(int, TelemetrySample) time.Duration) {
	for {
		sample, ok := queue.front()
		if !ok {
			select {
			case <-done:
				return
			case <-queue.changed:
				continue
			}
		}

		failures := 0
		for {
			receipt, err := postTelemetrySafely(poster, sample)
			if err == nil && (!receipt.Reliable || (receipt.BootID == sample.BootID && receipt.AcknowledgedSequence == sample.Sequence && !receipt.ReceivedAt.IsZero())) {
				queue.removeFront(sample)
				break
			}
			if err == nil {
				err = fmt.Errorf("agent: invalid telemetry acknowledgement")
			}
			if isPermanentTelemetryError(err) {
				log.printf("agent: telemetry heartbeat: dropping permanently rejected sample %s/%d: %v\n", sample.BootID, sample.Sequence, err)
				queue.removeFront(sample)
				break
			}
			failures++
			if !queue.isFront(sample) {
				break
			}
			log.printf("agent: telemetry heartbeat: %v\n", err)
			delay := retryDelay(failures, sample)
			timer := time.NewTimer(delay)
			advance := false
			wait := true
			for wait {
				select {
				case <-done:
					stopTelemetryTimer(timer)
					return
				case <-timer.C:
					wait = false
				case <-queue.changed:
					if !queue.isFront(sample) {
						stopTelemetryTimer(timer)
						advance = true
						wait = false
					}
				}
			}
			if advance {
				break
			}
		}
	}
}

func stopTelemetryTimer(timer *time.Timer) {
	if timer.Stop() {
		return
	}
	select {
	case <-timer.C:
	default:
	}
}

// telemetryRetryDelay is exponential with deterministic per-boot/sample jitter. Determinism keeps tests
// stable while random boot IDs spread a recovering fleet. The delay is always capped.
func telemetryRetryDelay(failures int, sample TelemetrySample) time.Duration {
	exponent := failures - 1
	if exponent < 0 {
		exponent = 0
	}
	if exponent > 5 {
		exponent = 5
	}
	base := telemetryRetryBase * time.Duration(1<<exponent)
	seed := sample.Sequence + uint64(failures)*12345
	for i := 0; i < len(sample.BootID); i++ {
		seed = seed*131 + uint64(sample.BootID[i])
	}
	jitter := time.Duration(seed%1000) * time.Millisecond
	delay := base + jitter
	if delay > telemetryRetryMax {
		return telemetryRetryMax
	}
	return delay
}

var fallbackTelemetryBootCounter uint64

func newTelemetryBootID() string {
	var id [16]byte
	if _, err := rand.Read(id[:]); err == nil {
		return hex.EncodeToString(id[:])
	}
	// Boot ids are correlation identifiers, not credentials. Preserve uniqueness if the OS entropy
	// source is unexpectedly unavailable without making telemetry failure fatal to the agent.
	counter := atomic.AddUint64(&fallbackTelemetryBootCounter, 1)
	return fmt.Sprintf("%016x%016x", uint64(time.Now().UnixNano()), counter)
}

func runReliableHeartbeat(poster ReliableTelemetryPoster, tel *Telemetry, interval time.Duration, kick, done <-chan struct{}, stderr io.Writer) {
	log := &telemetryLogger{w: stderr}
	queue := newTelemetryReplayQueue()
	go runTelemetryUploader(poster, queue, done, log)
	sequencer := newTelemetrySequencer()

	collect := func() {
		defer func() {
			if recovered := recover(); recovered != nil {
				log.printf("agent: telemetry heartbeat: recovered from panic: %v\n", recovered)
			}
		}()
		sampledAt := time.Now().UTC()
		conditions, metrics := tel.Collect(sampledAt)
		if len(conditions) == 0 && len(metrics) == 0 {
			return
		}
		sample, err := sequencer.snapshot(conditions, metrics, sampledAt, interval)
		if err != nil {
			log.printf("agent: telemetry heartbeat: %v\n", err)
			return
		}
		if queue.enqueue(sample) {
			log.println("agent: telemetry heartbeat: replay queue full; dropped oldest sample")
		}
	}

	collect()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-done:
			return
		case <-ticker.C:
			collect()
		case <-kick:
			collect()
		}
	}
}
