package agent

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"net"
	"os"
	"sort"
	"strconv"
	"sync"
	"syscall"
	"time"

	"github.com/kunorikiku/yet-another-overlay-generator/internal/model"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/probemetric"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/probepolicy"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/runtimecontract"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/telemetrymetric"
)

const (
	probeResultsMetricKey          = probemetric.LatestMetricKey
	probeSamplesMetricKey          = probemetric.SamplesMetricKey
	probeStatusPending             = probemetric.StatusPending
	probeStatusSuccess             = probemetric.StatusSuccess
	probeStatusFailure             = probemetric.StatusFailure
	probeFailureDNSFailed          = probemetric.FailureDNSFailed
	probeFailureTimeout            = probemetric.FailureTimeout
	probeFailurePermissionDenied   = probemetric.FailurePermissionDenied
	probeFailureConnectionRefused  = probemetric.FailureConnectionRefused
	probeFailureNetworkUnreachable = probemetric.FailureNetworkUnreachable
	probeFailureNetworkError       = probemetric.FailureNetworkError
	maxConcurrentProbes            = 4
	maxResolvedAddresses           = 8
)

// activeProbeResult keeps the existing package-local name used by the focused scheduler tests while
// making probemetric.Result the single agent/controller wire contract.
type activeProbeResult = probemetric.Result

type probeAttemptFunc func(context.Context, model.TelemetryProbe) string

type probeRuntime struct {
	probe    model.TelemetryProbe
	result   activeProbeResult
	next     time.Time
	running  bool
	neverRun bool
	cancel   context.CancelFunc
}

// activeProbeSampler owns scheduling for the current last-known-good policy. Sample itself performs
// no network I/O: due attempts run asynchronously with a four-worker ceiling and no per-probe overlap.
type activeProbeSampler struct {
	stateDir string
	attempt  probeAttemptFunc
	jitter   func(string, model.TelemetryProbe) time.Duration
	// wait keeps probe cadence independent from heartbeat cadence. Production uses a cancellable
	// monotonic timer; tests inject a deterministic gate so multiple attempts need no wall-clock sleep.
	wait func(context.Context, time.Duration) bool
	// monotonicNow is the scheduling/elapsed clock; time.Now retains its monotonic reading.
	monotonicNow func() time.Time
	// checkedAtNow is wall time used only for the checked_at wire timestamp.
	checkedAtNow func() time.Time
	slots        chan struct{}

	mu           sync.Mutex
	hasPolicy    bool
	policyDigest [sha256.Size]byte
	order        []string
	probes       map[string]*probeRuntime
	// samples is the rolling completed-attempt window emitted as metrics["probe_samples"]. It never
	// contains initial pending rows. A half-window high-water kick leaves 32 slots (two maximum
	// sixteen-probe rounds) of scheduling headroom before collection; reliable snapshots then preserve
	// overlapping windows across upload retries.
	samples                []activeProbeResult
	completedSinceSnapshot int
	completionKickPending  bool
	probeCompletionKick    func()
}

func newActiveProbeSampler(stateDir string) *activeProbeSampler {
	return &activeProbeSampler{
		stateDir:     stateDir,
		attempt:      performProbeAttempt,
		jitter:       startupProbeJitter,
		wait:         waitProbeDelay,
		monotonicNow: time.Now,
		checkedAtNow: func() time.Time { return time.Now().UTC() },
		slots:        make(chan struct{}, maxConcurrentProbes),
		probes:       make(map[string]*probeRuntime),
		samples:      make([]activeProbeResult, 0, probemetric.MaxRecentSamples),
	}
}

func (*activeProbeSampler) Name() string { return "active-probes" }

func (*activeProbeSampler) MetricDefinitions() []telemetrymetric.Definition {
	return []telemetrymetric.Definition{telemetrymetric.ProbeResults, telemetrymetric.ProbeSamples}
}

func (s *activeProbeSampler) setProbeCompletionKick(kick func()) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.probeCompletionKick = kick
}

func (s *activeProbeSampler) Sample(_ time.Time) ([]runtimecontract.Condition, map[string]any) {
	state, err := LoadState(s.stateDir)
	if err != nil || state == nil {
		// A transient state read failure must neither activate a new destination nor stop heartbeat
		// delivery. Freeze the previously verified scheduler and expose its last results.
		return nil, s.snapshot()
	}
	if len(state.ActiveTelemetryPolicy) == 0 {
		s.clear()
		return nil, nil
	}
	policy, err := probepolicy.ParseActive(state.ActiveTelemetryPolicy)
	if err != nil {
		// Corrupt or hand-edited state is not an authorization source. Cancel active work and fail
		// closed until a future verified apply commits a valid replacement.
		s.clear()
		return nil, nil
	}
	digest := sha256.Sum256(state.ActiveTelemetryPolicy)
	now := s.monotonicNow()
	s.reconcile(policy.Probes, digest, now)

	type scheduled struct {
		runtime *probeRuntime
		delay   time.Duration
		ctx     context.Context
	}
	var due []scheduled
	s.mu.Lock()
	for _, id := range s.order {
		runtime := s.probes[id]
		if runtime == nil || runtime.running || now.Before(runtime.next) {
			continue
		}
		runtime.running = true
		delay := time.Duration(0)
		if runtime.neverRun {
			delay = s.jitter(state.NodeID, runtime.probe)
		}
		ctx, cancel := context.WithCancel(context.Background())
		runtime.cancel = cancel
		due = append(due, scheduled{runtime: runtime, delay: delay, ctx: ctx})
	}
	metrics := s.snapshotLocked()
	s.mu.Unlock()

	for _, item := range due {
		go s.execute(item.ctx, item.runtime, item.delay)
	}
	return nil, metrics
}

func (s *activeProbeSampler) reconcile(probes []model.TelemetryProbe, digest [sha256.Size]byte, now time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.hasPolicy && s.policyDigest == digest {
		return
	}
	old := s.probes
	next := make(map[string]*probeRuntime, len(probes))
	order := make([]string, 0, len(probes))
	for _, probe := range probes {
		order = append(order, probe.ID)
		if prior := old[probe.ID]; prior != nil && prior.probe == probe {
			next[probe.ID] = prior
			continue
		}
		next[probe.ID] = &probeRuntime{
			probe:    probe,
			result:   configuredProbeResult(probe, probeStatusPending),
			next:     now,
			neverRun: true,
		}
	}
	for id, prior := range old {
		if next[id] != prior && prior.cancel != nil {
			prior.cancel()
		}
	}
	// A changed/removed destination must not remain in the rolling live window indefinitely. Retain
	// history for exact executable destinations that remain authorized; interval/timeout edits keep the
	// same series, while an id reused for another host/type/port starts clean.
	retained := s.samples[:0]
	for _, sample := range s.samples {
		if runtime := next[sample.ID]; runtime != nil && probeResultMatchesProbe(sample, runtime.probe) {
			retained = append(retained, sample)
		}
	}
	s.samples = retained
	s.hasPolicy = true
	s.policyDigest = digest
	s.order = order
	s.probes = next
}

func (s *activeProbeSampler) clear() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, runtime := range s.probes {
		if runtime.cancel != nil {
			runtime.cancel()
		}
	}
	s.hasPolicy = false
	s.policyDigest = [sha256.Size]byte{}
	s.order = nil
	s.probes = make(map[string]*probeRuntime)
	s.samples = nil
	s.completedSinceSnapshot = 0
	s.completionKickPending = false
}

func (s *activeProbeSampler) snapshot() map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.order) == 0 {
		return nil
	}
	return s.snapshotLocked()
}

func (s *activeProbeSampler) snapshotLocked() map[string]any {
	// A collection has captured every attempt currently retained. Permit one later high-water kick for
	// the next uncollected batch; the samples themselves remain as a rolling window so reliable replay
	// and controller deduplication can bridge retries and overlapping snapshots.
	s.completedSinceSnapshot = 0
	s.completionKickPending = false
	results := make([]activeProbeResult, 0, len(s.order))
	for _, id := range s.order {
		if runtime := s.probes[id]; runtime != nil {
			results = append(results, runtime.result)
		}
	}
	metrics := map[string]any{probeResultsMetricKey: results}
	if len(s.samples) > 0 {
		// Copy the slice header/backing values before leaving the sampler lock. The reliable heartbeat
		// sequencer deep-copies the resulting JSON immediately, but this also prevents a later append or
		// policy reconciliation from mutating a snapshot already returned to a caller.
		samples := append([]activeProbeResult(nil), s.samples...)
		metrics[probeSamplesMetricKey] = samples
	}
	return metrics
}

func (s *activeProbeSampler) execute(runtimeCtx context.Context, runtime *probeRuntime, delay time.Duration) {
	defer s.markProbeRuntimeStopped(runtime)
	for {
		if !s.wait(runtimeCtx, delay) {
			return
		}
		select {
		case s.slots <- struct{}{}:
		case <-runtimeCtx.Done():
			return
		}

		s.mu.Lock()
		current := s.probes[runtime.probe.ID]
		if current != runtime || !runtime.running || runtimeCtx.Err() != nil {
			s.mu.Unlock()
			<-s.slots
			return
		}
		timeout := time.Duration(probepolicy.EffectiveTimeoutMilliseconds(runtime.probe)) * time.Millisecond
		attemptCtx, cancelAttempt := context.WithTimeout(runtimeCtx, timeout)
		s.mu.Unlock()

		started := s.monotonicNow()
		failureReason := performProbeAttemptSafely(s.attempt, attemptCtx, runtime.probe)
		finished := s.monotonicNow()
		checkedAt := s.checkedAtNow().UTC()
		cancelAttempt()
		<-s.slots

		// Cancellation means the signed policy changed or cleared while this attempt was in flight. The
		// result belongs to an authorization that is no longer current and must not enter either latest
		// telemetry or the recent-attempt window.
		if runtimeCtx.Err() != nil {
			return
		}
		result := configuredProbeResult(runtime.probe, probeStatusSuccess)
		result.CheckedAt = checkedAt.Format(time.RFC3339Nano)
		if failureReason == "" {
			ms := probeLatencyMilliseconds(started, finished)
			result.LatencyMS = &ms
		} else {
			result.Status = probeStatusFailure
			result.FailureReason = failureReason
		}

		s.mu.Lock()
		if s.probes[runtime.probe.ID] != runtime || !runtime.running || runtimeCtx.Err() != nil {
			s.mu.Unlock()
			return
		}
		runtime.result = result
		sample := result
		interval := time.Duration(probepolicy.EffectiveIntervalSeconds(runtime.probe)) * time.Second
		sample.IntervalMS = interval.Milliseconds()
		shouldKick := s.appendCompletedSampleLocked(sample)
		completionKick := s.probeCompletionKick
		runtime.neverRun = false
		runtime.next = finished.Add(interval)
		s.mu.Unlock()
		if shouldKick && completionKick != nil {
			completionKick()
		}

		// Attempts are scheduled by their signed cadence, not by the upload heartbeat. The rolling sample
		// window is what lets the next heartbeat carry every completion since its previous collection.
		delay = interval
	}
}

func performProbeAttemptSafely(attempt probeAttemptFunc, ctx context.Context, probe model.TelemetryProbe) (failureReason string) {
	defer func() {
		if recover() != nil {
			// A sampler panic is already isolated by the telemetry framework, but active attempts run on
			// their own cadence goroutine. Preserve the same daemon-liveness invariant here and expose only
			// the closed, non-sensitive failure category.
			failureReason = probeFailureNetworkError
		}
	}()
	return attempt(ctx, probe)
}

func probeLatencyMilliseconds(started, finished time.Time) float64 {
	elapsed := finished.Sub(started)
	if elapsed < 0 {
		// Production time.Now values carry a monotonic component, but a defensive clamp keeps a broken
		// clock seam or unusual platform behavior from emitting an invalid negative latency.
		elapsed = 0
	}
	return math.Round(float64(elapsed)/float64(time.Millisecond)*10) / 10
}

func (s *activeProbeSampler) markProbeRuntimeStopped(runtime *probeRuntime) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.probes[runtime.probe.ID] == runtime {
		runtime.running = false
		runtime.cancel = nil
	}
}

func waitProbeDelay(ctx context.Context, delay time.Duration) bool {
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

func configuredProbeResult(probe model.TelemetryProbe, status string) activeProbeResult {
	return activeProbeResult{ID: probe.ID, Type: probe.Type, Host: probe.Host, Port: probe.Port, Status: status}
}

func (s *activeProbeSampler) appendCompletedSampleLocked(sample activeProbeResult) bool {
	if !probemetric.Completed(sample) {
		return false
	}
	if len(s.samples) < probemetric.MaxRecentSamples {
		s.samples = append(s.samples, sample)
	} else {
		copy(s.samples, s.samples[1:])
		s.samples[len(s.samples)-1] = sample
	}
	s.completedSinceSnapshot++
	if !s.completionKickPending && s.completedSinceSnapshot >= probemetric.MaxRecentSamples/2 {
		s.completionKickPending = true
		return true
	}
	return false
}

func probeResultMatchesProbe(result activeProbeResult, probe model.TelemetryProbe) bool {
	return result.ID == probe.ID && result.Type == probe.Type && result.Host == probe.Host && result.Port == probe.Port
}

// startupProbeJitter spreads a newly activated policy over at most five seconds. Including the
// managed node identity prevents an identical fleet-wide policy from synchronizing every source;
// the stable hash keeps a given node/probe pair deterministic across restarts.
func startupProbeJitter(nodeID string, probe model.TelemetryProbe) time.Duration {
	interval := time.Duration(probepolicy.EffectiveIntervalSeconds(probe)) * time.Second
	maxJitter := interval / 10
	if maxJitter > 5*time.Second {
		maxJitter = 5 * time.Second
	}
	if maxJitter <= 0 {
		return 0
	}
	sum := sha256.Sum256([]byte(nodeID + "\x00" + probe.ID + "\x00" + probe.Type + "\x00" + probe.Host + "\x00" + strconv.Itoa(probe.Port)))
	n := binary.BigEndian.Uint32(sum[:4])
	return time.Duration(uint64(n) % uint64(maxJitter+1))
}

func performProbeAttempt(ctx context.Context, probe model.TelemetryProbe) string {
	addresses, err := resolveProbeHost(ctx, probe.Host)
	return performResolvedProbeAttempt(ctx, probe, addresses, err)
}

func performResolvedProbeAttempt(ctx context.Context, probe model.TelemetryProbe, addresses []net.IP, resolveErr error) string {
	if resolveErr != nil {
		if errors.Is(resolveErr, context.DeadlineExceeded) || errors.Is(resolveErr, context.Canceled) {
			return probeFailureTimeout
		}
		return probeFailureDNSFailed
	}

	var lastErr error
	for _, ip := range addresses {
		if err := ctx.Err(); err != nil {
			return probeFailureTimeout
		}
		switch probe.Type {
		case model.TelemetryProbeTCP:
			dialer := &net.Dialer{}
			conn, err := dialer.DialContext(ctx, "tcp", net.JoinHostPort(ip.String(), strconv.Itoa(probe.Port)))
			if err == nil {
				_ = conn.Close()
				return ""
			}
			lastErr = err
		case model.TelemetryProbeICMP:
			if err := sendICMPEcho(ctx, ip); err == nil {
				return ""
			} else {
				lastErr = err
				if classifyProbeError(err) == probeFailurePermissionDenied {
					return probeFailurePermissionDenied
				}
			}
		default:
			return probeFailureNetworkError
		}
	}
	return classifyProbeError(lastErr)
}

// resolveProbeHost deliberately resolves on every attempt. A DNS name is operator-authorized as a
// name (not pinned to its current answers); answers are bounded so one response cannot fan out an
// unbounded number of outbound attempts.
func resolveProbeHost(ctx context.Context, host string) ([]net.IP, error) {
	if ip := net.ParseIP(host); ip != nil {
		return []net.IP{ip}, nil
	}
	resolved, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil {
		return nil, err
	}
	addresses := make([]net.IP, 0, len(resolved))
	seen := make(map[string]struct{}, len(resolved))
	for _, item := range resolved {
		ip := item.IP
		if ip == nil {
			continue
		}
		key := ip.String()
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		addresses = append(addresses, append(net.IP(nil), ip...))
		if len(addresses) == maxResolvedAddresses {
			break
		}
	}
	if len(addresses) == 0 {
		return nil, fmt.Errorf("no IP addresses")
	}
	// Stable ordering makes retries and tests predictable while still trying both families.
	sort.SliceStable(addresses, func(i, j int) bool {
		return bytes.Compare(addresses[i], addresses[j]) < 0
	})
	return addresses, nil
}

func sendICMPEcho(ctx context.Context, ip net.IP) error {
	is4 := ip.To4() != nil
	network := "ip6:ipv6-icmp"
	echoType, replyType := byte(128), byte(129)
	remote := &net.IPAddr{IP: ip}
	if is4 {
		network = "ip4:icmp"
		echoType, replyType = 8, 0
		remote.IP = ip.To4()
	}
	conn, err := net.DialIP(network, nil, remote)
	if err != nil {
		return err
	}
	defer conn.Close()
	deadline, ok := ctx.Deadline()
	if !ok {
		return fmt.Errorf("ICMP probe context has no deadline")
	}
	if err := conn.SetDeadline(deadline); err != nil {
		return err
	}

	packet := make([]byte, 16)
	packet[0] = echoType
	packet[1] = 0
	if _, err := rand.Read(packet[4:]); err != nil {
		return err
	}
	// For IPv6 raw ICMP sockets the kernel calculates the pseudo-header checksum. IPv4 needs the
	// ordinary ICMP checksum in the message itself.
	if is4 {
		binary.BigEndian.PutUint16(packet[2:4], icmpChecksum(packet))
	}
	if _, err := conn.Write(packet); err != nil {
		return err
	}

	buf := make([]byte, 2048)
	for {
		n, err := conn.Read(buf)
		if err != nil {
			return err
		}
		reply := stripIPHeader(buf[:n])
		if len(reply) < len(packet) || reply[0] != replyType || reply[1] != 0 {
			continue
		}
		if bytes.Equal(reply[4:len(packet)], packet[4:]) {
			return nil
		}
	}
}

func stripIPHeader(packet []byte) []byte {
	if len(packet) == 0 {
		return packet
	}
	switch packet[0] >> 4 {
	case 4:
		headerLen := int(packet[0]&0x0f) * 4
		if headerLen >= 20 && headerLen <= len(packet) {
			return packet[headerLen:]
		}
	case 6:
		if len(packet) >= 40 {
			return packet[40:]
		}
	}
	return packet
}

func icmpChecksum(message []byte) uint16 {
	var sum uint32
	for len(message) >= 2 {
		sum += uint32(binary.BigEndian.Uint16(message[:2]))
		message = message[2:]
	}
	if len(message) == 1 {
		sum += uint32(message[0]) << 8
	}
	for sum>>16 != 0 {
		sum = (sum & 0xffff) + (sum >> 16)
	}
	return ^uint16(sum)
}

func classifyProbeError(err error) string {
	if err == nil {
		return probeFailureNetworkError
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return probeFailureTimeout
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return probeFailureTimeout
	}
	switch {
	case errors.Is(err, os.ErrPermission), errors.Is(err, syscall.EPERM), errors.Is(err, syscall.EACCES):
		return probeFailurePermissionDenied
	case errors.Is(err, syscall.ECONNREFUSED):
		return probeFailureConnectionRefused
	case errors.Is(err, syscall.ENETUNREACH), errors.Is(err, syscall.EHOSTUNREACH):
		return probeFailureNetworkUnreachable
	default:
		return probeFailureNetworkError
	}
}
