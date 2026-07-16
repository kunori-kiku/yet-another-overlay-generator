package agent

import (
	"context"
	"errors"
	"net"
	"net/http"
	"time"

	"github.com/kunorikiku/yet-another-overlay-generator/internal/model"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/probepolicy"
)

const maxURLResponseHeaderBytes = 32 << 10

// newURLProbeClient builds a one-shot client whose behavior is entirely fixed by the executable
// probe contract. In particular it never inherits ambient proxy configuration and never follows a
// response-selected destination.
func newURLProbeClient(timeout time.Duration) *http.Client {
	dialer := &net.Dialer{Timeout: timeout, KeepAlive: -1}
	transport := &http.Transport{
		Proxy:                  nil,
		DialContext:            dialer.DialContext,
		ForceAttemptHTTP2:      false,
		DisableKeepAlives:      true,
		DisableCompression:     true,
		TLSHandshakeTimeout:    timeout,
		ResponseHeaderTimeout:  timeout,
		MaxResponseHeaderBytes: maxURLResponseHeaderBytes,
	}
	return &http.Client{
		Transport: transport,
		Timeout:   timeout,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

func performURLProbeAttempt(ctx context.Context, probe model.TelemetryProbe) probeAttemptOutcome {
	timeout := time.Duration(probepolicy.EffectiveTimeoutMilliseconds(probe)) * time.Millisecond
	client := newURLProbeClient(timeout)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, probe.URL, nil)
	if err != nil {
		return probeAttemptOutcome{FailureReason: probeFailureNetworkError}
	}
	resp, err := client.Do(req)
	if err != nil {
		return probeAttemptOutcome{FailureReason: classifyURLProbeError(err)}
	}
	_ = resp.Body.Close()
	// net/http accepts any three-digit decimal status on the wire, including values outside the
	// HTTP status-code range. Keep those malformed responses out of the authenticated result shape:
	// actual_status is deliberately constrained to 100..599 at every later boundary.
	if resp.StatusCode < 100 || resp.StatusCode > 599 {
		return probeAttemptOutcome{FailureReason: probeFailureNetworkError}
	}

	outcome := probeAttemptOutcome{ResponseComplete: true, ActualStatus: resp.StatusCode}
	if resp.StatusCode != probepolicy.EffectiveExpectedStatus(probe) {
		outcome.FailureReason = probeFailureUnexpectedStatus
	}
	return outcome
}

func classifyURLProbeError(err error) string {
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return probeFailureTimeout
	}
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		if dnsErr.Timeout() {
			return probeFailureTimeout
		}
		return probeFailureDNSFailed
	}
	return classifyProbeError(err)
}
