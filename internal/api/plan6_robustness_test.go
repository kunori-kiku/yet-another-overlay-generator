package api

// plan6_robustness_test.go covers the plan-6 backend-robustness items that live on the
// HTTP surface: the per-IP /enroll brute-force throttle and graceful shutdown draining a
// listener. Both run in-process over loopback, stdlib only.

import (
	"context"
	"errors"
	"net"
	"net/http"
	"testing"
	"time"

	"golang.zx2c4.com/wireguard/wgctrl/wgtypes"
)

// TestControllerHTTP_EnrollThrottle drives failed /enroll attempts (bad enrollment token)
// from the same loopback IP and asserts the per-IP limiter trips: the first
// maxLoginFailures attempts reach the enroll logic (and are rejected for the bad token,
// never 429), and the next attempt is throttled with 429 — independent of token validity.
func TestControllerHTTP_EnrollThrottle(t *testing.T) {
	env := newCtlTestEnv(t)

	wgPriv, err := wgtypes.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("wg GeneratePrivateKey: %v", err)
	}
	badReq := enrollRequestJSON{
		Token:       "bogus-enrollment-token",
		NodeID:      "attacker-node",
		WGPublicKey: wgPriv.PublicKey().String(),
	}

	for i := 0; i < maxLoginFailures; i++ {
		status := doJSON(t, http.MethodPost, env.agentURL("enroll"), "", badReq, nil)
		if status == http.StatusTooManyRequests {
			t.Fatalf("attempt %d throttled too early (429); want the limiter to allow the first %d", i+1, maxLoginFailures)
		}
		if status == http.StatusOK {
			t.Fatalf("attempt %d unexpectedly succeeded with a bogus token", i+1)
		}
	}

	// The next attempt is over the threshold → 429 regardless of token validity.
	status := doJSON(t, http.MethodPost, env.agentURL("enroll"), "", badReq, nil)
	if status != http.StatusTooManyRequests {
		t.Fatalf("attempt %d status = %d, want 429 (per-IP enroll throttle)", maxLoginFailures+1, status)
	}
}

// TestControllerHTTP_EnrollSuccessRefundsThrottle asserts a SUCCESSFUL enroll refunds its
// reserved slot, so an operator bulk-enrolling many nodes from one IP never trips the
// limiter even though it exceeds maxLoginFailures successful enrolls.
func TestControllerHTTP_EnrollSuccessRefundsThrottle(t *testing.T) {
	env := newCtlTestEnv(t)
	for i := 0; i < maxLoginFailures+5; i++ {
		// enrollNode fails the test if any enroll does not return 200, which is exactly the
		// assertion: success refunds, so none of these (well past the failure cap) is throttled.
		env.enrollNode(t, "bulk-node-"+itoa(int64(i)))
	}
}

// TestServerGracefulShutdown asserts Server.Shutdown drains a running listener: after it
// returns, ListenAndServe yields http.ErrServerClosed and the shared base context (which
// unblocks in-flight long-polls) is cancelled.
func TestServerGracefulShutdown(t *testing.T) {
	s := NewServer()

	// Grab a free loopback port, then hand its address to ListenAndServe.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve port: %v", err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()

	errc := make(chan error, 1)
	go func() { errc <- s.ListenAndServe(addr) }()

	// Wait until /api/health answers, which also guarantees s.httpSrv is published.
	waitHealthy(t, addr)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := s.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}

	select {
	case err := <-errc:
		if !errors.Is(err, http.ErrServerClosed) {
			t.Fatalf("ListenAndServe returned %v, want http.ErrServerClosed", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("ListenAndServe did not return after Shutdown")
	}

	if s.baseCtx.Err() == nil {
		t.Errorf("Shutdown must cancel the base context so in-flight long-polls unblock")
	}
}

// waitHealthy polls GET /api/health on addr until it answers 200 or the deadline elapses.
func waitHealthy(t *testing.T, addr string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := http.Get("http://" + addr + "/api/health")
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("server at %s did not become healthy", addr)
}
