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
	"github.com/kunorikiku/yet-another-overlay-generator/internal/probepolicy"
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

func TestActiveProbeSampler_NonBlockingResultAndNoOverlap(t *testing.T) {
	dir := t.TempDir()
	probe := model.TelemetryProbe{ID: "tls", Type: model.TelemetryProbeTCP, Host: "service.example", Port: 443}
	saveActiveProbePolicy(t, dir, []model.TelemetryProbe{probe})

	sampler := newActiveProbeSampler(dir)
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
}

func TestActiveProbeSampler_MonotonicScheduleIgnoresHeartbeatWallClock(t *testing.T) {
	dir := t.TempDir()
	probe := model.TelemetryProbe{
		ID: "tls", Type: model.TelemetryProbeTCP, Host: "service.example", Port: 443,
		IntervalSeconds: 30,
	}
	saveActiveProbePolicy(t, dir, []model.TelemetryProbe{probe})

	sampler := newActiveProbeSampler(dir)
	sampler.jitter = func(string, model.TelemetryProbe) time.Duration { return 0 }
	base := time.Unix(1_000, 0)
	var elapsedNanos atomic.Int64
	sampler.monotonicNow = func() time.Time {
		return base.Add(time.Duration(elapsedNanos.Load()))
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

	// A heartbeat wall time far beyond the interval must not make the probe due.
	elapsedNanos.Store(int64(29 * time.Second))
	sampler.Sample(time.Date(2199, time.January, 1, 0, 0, 0, 0, time.UTC))
	time.Sleep(20 * time.Millisecond)
	if got := calls.Load(); got != 1 {
		t.Fatalf("attempts before monotonic interval = %d, want 1", got)
	}

	// Conversely, moving the heartbeat wall time backward must not postpone a due probe.
	elapsedNanos.Store(int64(30 * time.Second))
	sampler.Sample(time.Date(1970, time.January, 1, 0, 0, 0, 0, time.UTC))
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
