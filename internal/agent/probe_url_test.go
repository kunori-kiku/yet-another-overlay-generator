package agent

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kunorikiku/yet-another-overlay-generator/internal/model"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/probemetric"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/probepolicy"
)

func TestPerformURLProbeAttempt_FixedGETExpectedAndMismatch(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.Method != http.MethodGet {
			t.Errorf("method = %s, want GET", r.Method)
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read request body: %v", err)
		}
		if len(body) != 0 || r.Header.Get("Authorization") != "" || r.Header.Get("Cookie") != "" {
			t.Errorf("fixed request acquired body or credentials: body=%q headers=%v", body, r.Header)
		}
		if r.URL.Path == "/ready" && r.URL.RawQuery == "full=1" {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	success := performURLProbeAttempt(context.Background(), model.TelemetryProbe{
		ID: "ready", Type: model.TelemetryProbeURL, URL: server.URL + "/ready?full=1",
		ExpectedStatus: http.StatusNoContent,
	})
	if success.FailureReason != "" || !success.ResponseComplete || success.ActualStatus != http.StatusNoContent {
		t.Fatalf("expected response outcome = %+v", success)
	}

	mismatch := performURLProbeAttempt(context.Background(), model.TelemetryProbe{
		ID: "status", Type: model.TelemetryProbeURL, URL: server.URL + "/other",
	})
	if mismatch.FailureReason != probeFailureUnexpectedStatus || !mismatch.ResponseComplete ||
		mismatch.ActualStatus != http.StatusInternalServerError {
		t.Fatalf("mismatched response outcome = %+v", mismatch)
	}
	if got := requests.Load(); got != 2 {
		t.Fatalf("request count = %d, want exactly two one-shot GETs", got)
	}
}

func TestPerformURLProbeAttempt_Default200IgnoresAmbientProxy(t *testing.T) {
	var proxyRequests atomic.Int32
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		proxyRequests.Add(1)
		http.Error(w, "proxy must not be used", http.StatusBadGateway)
	}))
	defer proxy.Close()
	for _, name := range []string{"HTTP_PROXY", "http_proxy"} {
		t.Setenv(name, proxy.URL)
	}
	for _, name := range []string{"NO_PROXY", "no_proxy"} {
		t.Setenv(name, "")
	}

	listener, err := net.Listen("tcp4", "0.0.0.0:0")
	if err != nil {
		t.Fatal(err)
	}
	var directRequests atomic.Int32
	direct := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		directRequests.Add(1)
		w.WriteHeader(http.StatusOK)
	})}
	go func() { _ = direct.Serve(listener) }()
	t.Cleanup(func() { _ = direct.Close() })

	port := listener.Addr().(*net.TCPAddr).Port
	target := "http://" + net.JoinHostPort(nonLoopbackIPv4(t), strconv.Itoa(port))
	outcome := performURLProbeAttempt(context.Background(), model.TelemetryProbe{
		ID: "default", Type: model.TelemetryProbeURL, URL: target,
	})
	if outcome.FailureReason != "" || !outcome.ResponseComplete || outcome.ActualStatus != http.StatusOK {
		t.Fatalf("default-200 direct outcome = %+v", outcome)
	}
	if got := directRequests.Load(); got != 1 {
		t.Fatalf("direct target requests = %d, want one", got)
	}
	if got := proxyRequests.Load(); got != 0 {
		t.Fatalf("ambient proxy received %d requests, want zero", got)
	}
}

func nonLoopbackIPv4(t *testing.T) string {
	t.Helper()
	interfaces, err := net.Interfaces()
	if err != nil {
		t.Fatal(err)
	}
	for _, iface := range interfaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		addresses, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, address := range addresses {
			var ip net.IP
			switch value := address.(type) {
			case *net.IPNet:
				ip = value.IP
			case *net.IPAddr:
				ip = value.IP
			}
			if ip4 := ip.To4(); ip4 != nil && !ip4.IsLoopback() && !ip4.IsLinkLocalUnicast() {
				return ip4.String()
			}
		}
	}
	t.Fatal("test host has no active non-loopback IPv4 address")
	return ""
}

func TestPerformURLProbeAttempt_DoesNotFollowRedirect(t *testing.T) {
	var targetRequests atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		targetRequests.Add(1)
	}))
	defer target.Close()
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL+"/should-not-run", http.StatusFound)
	}))
	defer redirect.Close()

	outcome := performURLProbeAttempt(context.Background(), model.TelemetryProbe{
		ID: "redirect", Type: model.TelemetryProbeURL, URL: redirect.URL,
		ExpectedStatus: http.StatusFound,
	})
	if outcome.FailureReason != "" || !outcome.ResponseComplete || outcome.ActualStatus != http.StatusFound {
		t.Fatalf("redirect response outcome = %+v", outcome)
	}
	if got := targetRequests.Load(); got != 0 {
		t.Fatalf("redirect target received %d requests, want zero", got)
	}
}

func TestURLProbeClient_ClosedTransportAndOrdinaryTLS(t *testing.T) {
	t.Setenv("HTTP_PROXY", "http://127.0.0.1:1")
	t.Setenv("HTTPS_PROXY", "http://127.0.0.1:1")
	const timeout = 750 * time.Millisecond
	client := newURLProbeClient(timeout)
	transport, ok := client.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("transport = %T, want *http.Transport", client.Transport)
	}
	if transport.Proxy != nil || !transport.DisableKeepAlives || !transport.DisableCompression ||
		transport.TLSClientConfig != nil || transport.ForceAttemptHTTP2 ||
		transport.TLSHandshakeTimeout != timeout || transport.ResponseHeaderTimeout != timeout ||
		transport.MaxResponseHeaderBytes != maxURLResponseHeaderBytes || client.Jar != nil || client.Timeout != timeout {
		t.Fatalf("URL client escaped closed transport contract: client=%+v transport=%+v", client, transport)
	}

	tlsServer := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer tlsServer.Close()
	outcome := performURLProbeAttempt(context.Background(), model.TelemetryProbe{
		ID: "tls", Type: model.TelemetryProbeURL, URL: tlsServer.URL,
	})
	if outcome.FailureReason != probeFailureNetworkError || outcome.ResponseComplete || outcome.ActualStatus != 0 {
		t.Fatalf("untrusted TLS outcome = %+v, want closed network_error", outcome)
	}
}

func TestPerformURLProbeAttempt_TimesOutBeforeResponseHeaders(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(2 * time.Second):
			w.WriteHeader(http.StatusOK)
		}
	}))
	defer server.Close()
	outcome := performURLProbeAttempt(context.Background(), model.TelemetryProbe{
		ID: "timeout", Type: model.TelemetryProbeURL, URL: server.URL, TimeoutMilliseconds: 100,
	})
	if outcome.FailureReason != probeFailureTimeout || outcome.ResponseComplete || outcome.ActualStatus != 0 {
		t.Fatalf("timed-out outcome = %+v, want timeout without response metadata", outcome)
	}
}

func TestPerformURLProbeAttempt_RejectsMalformedOrOversizedResponseHeaders(t *testing.T) {
	tests := map[string]string{
		"malformed": "HTTP/1.1 200 OK\r\nMissing-Colon\r\nContent-Length: 0\r\n\r\n",
		"oversized": fmt.Sprintf("HTTP/1.1 200 OK\r\nX-Oversized: %s\r\nContent-Length: 0\r\n\r\n",
			strings.Repeat("x", maxURLResponseHeaderBytes+8192)),
		"status-outside-http-range": "HTTP/1.1 600 Invalid\r\nContent-Length: 0\r\n\r\n",
	}
	for name, response := range tests {
		t.Run(name, func(t *testing.T) {
			address, done := serveRawHTTPResponse(t, response)
			outcome := performURLProbeAttempt(context.Background(), model.TelemetryProbe{
				ID: "headers", Type: model.TelemetryProbeURL, URL: "http://" + address,
			})
			if outcome.FailureReason != probeFailureNetworkError || outcome.ResponseComplete || outcome.ActualStatus != 0 {
				t.Fatalf("header failure outcome = %+v, want transport failure without response metadata", outcome)
			}
			select {
			case <-done:
			case <-time.After(3 * time.Second):
				t.Fatal("raw response server did not finish")
			}
		})
	}
}

func serveRawHTTPResponse(t *testing.T, response string) (string, <-chan struct{}) {
	t.Helper()
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer listener.Close()
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		_ = conn.SetDeadline(time.Now().Add(2 * time.Second))
		_, _ = io.WriteString(conn, response)
	}()
	return listener.Addr().String(), done
}

func TestClassifyURLProbeError_UnwrapsDNS(t *testing.T) {
	dnsFailure := &url.Error{Op: "Get", URL: "http://missing.invalid", Err: &net.DNSError{
		Err: "no such host", Name: "missing.invalid", IsNotFound: true,
	}}
	if got := classifyURLProbeError(dnsFailure); got != probeFailureDNSFailed {
		t.Fatalf("DNS failure = %q, want %q", got, probeFailureDNSFailed)
	}
	dnsTimeout := &url.Error{Op: "Get", URL: "http://slow.invalid", Err: &net.DNSError{
		Err: "timeout", Name: "slow.invalid", IsTimeout: true,
	}}
	if got := classifyURLProbeError(dnsTimeout); got != probeFailureTimeout {
		t.Fatalf("DNS timeout = %q, want %q", got, probeFailureTimeout)
	}
}

func TestActiveProbeSampler_URLMismatchRetainsLatencyAndActualStatus(t *testing.T) {
	dir := t.TempDir()
	probe := model.TelemetryProbe{
		ID: "health", Type: model.TelemetryProbeURL, URL: "https://service.example/health",
		ExpectedStatus: http.StatusNoContent,
	}
	raw, err := probepolicy.MarshalSuccessor(probepolicy.SuccessorPolicy{Probes: []model.TelemetryProbe{probe}})
	if err != nil {
		t.Fatal(err)
	}
	if err := SaveState(dir, &State{
		NodeID: "alpha", LastResult: LastResultOK, ActiveTelemetryPolicy: raw,
	}); err != nil {
		t.Fatal(err)
	}

	sampler := newActiveProbeSampler(dir)
	t.Cleanup(sampler.clear)
	sampler.jitter = func(string, model.TelemetryProbe) time.Duration { return 0 }
	base := time.Unix(100, 0)
	var elapsed atomic.Int64
	sampler.monotonicNow = func() time.Time { return base.Add(time.Duration(elapsed.Load())) }
	sampler.wait = func(ctx context.Context, delay time.Duration) bool {
		if delay == 0 {
			return ctx.Err() == nil
		}
		<-ctx.Done()
		return false
	}
	sampler.attempt = func(context.Context, model.TelemetryProbe) probeAttemptOutcome {
		elapsed.Add(int64(25 * time.Millisecond))
		return probeAttemptOutcome{
			FailureReason: probeFailureUnexpectedStatus, ResponseComplete: true,
			ActualStatus: http.StatusOK,
		}
	}
	sampler.Sample(time.Now())
	result := waitProbeStatus(t, sampler, probe.ID, probeStatusFailure)
	if result.URL != probe.URL || result.ExpectedStatus != http.StatusNoContent ||
		result.ActualStatus != http.StatusOK || result.FailureReason != probeFailureUnexpectedStatus ||
		result.LatencyMS == nil || *result.LatencyMS != 25 || !probemetric.Valid(result, false) {
		t.Fatalf("URL mismatch result = %+v", result)
	}
}
