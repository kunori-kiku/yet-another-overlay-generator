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
	"github.com/kunorikiku/yet-another-overlay-generator/internal/probepolicy"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/runtimecontract"
)

const (
	probeResultsMetricKey          = "probe_results"
	probeStatusPending             = "pending"
	probeStatusSuccess             = "success"
	probeStatusFailure             = "failure"
	probeFailureDNSFailed          = "dns_failed"
	probeFailureTimeout            = "timeout"
	probeFailurePermissionDenied   = "permission_denied"
	probeFailureConnectionRefused  = "connection_refused"
	probeFailureNetworkUnreachable = "network_unreachable"
	probeFailureNetworkError       = "network_error"
	maxConcurrentProbes            = 4
	maxResolvedAddresses           = 8
)

// activeProbeResult is the generic, typed result carried in metrics["probe_results"]. Host is the
// configured value (never a resolver-selected address), so DNS does not leak transient answers into
// controller state. FailureReason is a small stable category, never a raw platform/network error.
type activeProbeResult struct {
	ID            string   `json:"id"`
	Type          string   `json:"type"`
	Host          string   `json:"host"`
	Port          int      `json:"port,omitempty"`
	Status        string   `json:"status"`
	LatencyMS     *float64 `json:"latency_ms,omitempty"`
	CheckedAt     string   `json:"checked_at,omitempty"`
	FailureReason string   `json:"failure_reason,omitempty"`
}

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
}

func newActiveProbeSampler(stateDir string) *activeProbeSampler {
	return &activeProbeSampler{
		stateDir:     stateDir,
		attempt:      performProbeAttempt,
		jitter:       startupProbeJitter,
		monotonicNow: time.Now,
		checkedAtNow: func() time.Time { return time.Now().UTC() },
		slots:        make(chan struct{}, maxConcurrentProbes),
		probes:       make(map[string]*probeRuntime),
	}
}

func (*activeProbeSampler) Name() string { return "active-probes" }

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
	policy, err := probepolicy.Parse(state.ActiveTelemetryPolicy)
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
		due = append(due, scheduled{runtime: runtime, delay: delay})
	}
	results := s.snapshotLocked()
	s.mu.Unlock()

	for _, item := range due {
		go s.execute(item.runtime, item.delay)
	}
	return nil, map[string]any{probeResultsMetricKey: results}
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
}

func (s *activeProbeSampler) snapshot() map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.order) == 0 {
		return nil
	}
	return map[string]any{probeResultsMetricKey: s.snapshotLocked()}
}

func (s *activeProbeSampler) snapshotLocked() []activeProbeResult {
	results := make([]activeProbeResult, 0, len(s.order))
	for _, id := range s.order {
		if runtime := s.probes[id]; runtime != nil {
			results = append(results, runtime.result)
		}
	}
	return results
}

func (s *activeProbeSampler) execute(runtime *probeRuntime, delay time.Duration) {
	if delay > 0 {
		timer := time.NewTimer(delay)
		<-timer.C
	}
	s.slots <- struct{}{}
	defer func() { <-s.slots }()

	s.mu.Lock()
	current := s.probes[runtime.probe.ID]
	if current != runtime || !runtime.running {
		s.mu.Unlock()
		return
	}
	timeout := time.Duration(probepolicy.EffectiveTimeoutMilliseconds(runtime.probe)) * time.Millisecond
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	runtime.cancel = cancel
	s.mu.Unlock()

	started := s.monotonicNow()
	failureReason := s.attempt(ctx, runtime.probe)
	finished := s.monotonicNow()
	checkedAt := s.checkedAtNow().UTC()
	cancel()

	result := configuredProbeResult(runtime.probe, probeStatusSuccess)
	result.CheckedAt = checkedAt.Format(time.RFC3339Nano)
	if failureReason == "" {
		ms := math.Round(float64(finished.Sub(started))/float64(time.Millisecond)*10) / 10
		result.LatencyMS = &ms
	} else {
		result.Status = probeStatusFailure
		result.FailureReason = failureReason
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.probes[runtime.probe.ID] != runtime {
		return
	}
	runtime.result = result
	runtime.running = false
	runtime.neverRun = false
	runtime.cancel = nil
	runtime.next = finished.Add(time.Duration(probepolicy.EffectiveIntervalSeconds(runtime.probe)) * time.Second)
}

func configuredProbeResult(probe model.TelemetryProbe, status string) activeProbeResult {
	return activeProbeResult{ID: probe.ID, Type: probe.Type, Host: probe.Host, Port: probe.Port, Status: status}
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
