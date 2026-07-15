//go:build dast

// Package dast is the plan-21 (4.2) dynamic prong: a TEST-ONLY Go attack-driver that boots the
// cmd/e2eserver controller subprocess and replays the encoded beta.8 enrollment exploits at the LIVE
// wire (raw HTTP at the OS-assigned ports), asserting each is still refused. It is the live-process
// counterpart to the in-process internal/api HTTP-boundary tests (beta8_blockers_test.go etc.): those
// prove the guard in a handler under httptest; this proves it FIRES end-to-end against a real booted
// server over a socket — the B1-class regression ("present in source, not wired at the wire").
//
// Build-tagged `dast` so it is invisible to `go test ./...` (it boots a subprocess + needs cmd/
// e2eserver). Run it with: go test -tags dast ./internal/dast/  (it builds e2eserver itself).
//
// Each case names its NEGATIVE CONTROL — the guard you revert locally to confirm the case goes RED
// (proving it can actually catch the regression, not pass vacuously).
package dast

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"testing"
	"time"

	"golang.zx2c4.com/wireguard/wgctrl/wgtypes"
)

const (
	operatorBase = "/api/v1/operator/"
	agentBase    = "/api/v1/agent/"
	dastOpToken  = "dast-break-glass-token"
)

// server is a booted e2eserver subprocess with the addrs it printed on its E2E_READY line.
type server struct {
	panel, agent, enroll string
	cmd                  *exec.Cmd
}

// secureTestStateDir tightens a test-owned directory explicitly instead of relying on the
// invoking shell's umask. The controller's production custody check must continue rejecting
// group/world-writable state directories; it is the DAST fixture that owns this directory and
// therefore must establish its intended mode before boot.
func secureTestStateDir(t *testing.T, dir string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		return // Windows custody is ACL/reparse based; Unix mode bits are not authoritative.
	}
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatalf("protect DAST state dir: %v", err)
	}
}

// bootController builds + boots cmd/e2eserver in controller mode (seeds an operator, a break-glass
// bearer token, and one enrollment token), waits for E2E_READY, and registers teardown.
func bootController(t *testing.T) *server {
	t.Helper()
	bin := t.TempDir() + "/e2eserver"
	build := exec.Command("go", "build", "-o", bin, "../../cmd/e2eserver")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build e2eserver: %v\n%s", err, out)
	}
	stateDir := t.TempDir()
	secureTestStateDir(t, stateDir)
	cmd := exec.Command(bin,
		"--mode", "controller",
		"--state-dir", stateDir,
		"--operator-token", dastOpToken, // break-glass bearer (CSRF-exempt) — auth_controller.go:152
		"--enroll-node", "node-1",
	)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("stdout pipe: %v", err)
	}
	cmd.Stderr = cmd.Stdout
	if err := cmd.Start(); err != nil {
		t.Fatalf("start e2eserver: %v", err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })

	srv := &server{cmd: cmd}
	ready := make(chan struct{})
	died := make(chan struct{}) // closed if stdout reaches EOF without an E2E_READY line (early death)
	go func() {
		sc := bufio.NewScanner(stdout)
		sc.Buffer(make([]byte, 0, 64*1024), 1024*1024) // tolerate long boot lines (default cap is 64KB)
		for sc.Scan() {
			line := sc.Text()
			if strings.HasPrefix(line, "E2E_READY") {
				for _, f := range strings.Fields(line) {
					switch {
					case strings.HasPrefix(f, "panel="):
						srv.panel = "http://" + strings.TrimPrefix(f, "panel=")
					case strings.HasPrefix(f, "agent="):
						srv.agent = "http://" + strings.TrimPrefix(f, "agent=")
					case strings.HasPrefix(f, "enroll="):
						srv.enroll = strings.TrimPrefix(f, "enroll=")
					}
				}
				close(ready)
				return
			}
		}
		close(died) // EOF without READY → the boot crashed; fail fast instead of waiting out the timeout
	}()
	select {
	case <-ready:
	case <-died:
		t.Fatalf("e2eserver exited before printing E2E_READY (boot failed)")
	case <-time.After(30 * time.Second):
		t.Fatalf("e2eserver did not print E2E_READY within 30s")
	}
	if srv.panel == "" || srv.agent == "" {
		t.Fatalf("E2E_READY missing panel/agent addr (panel=%q agent=%q)", srv.panel, srv.agent)
	}
	return srv
}

func TestSecureTestStateDirTightensPermissiveMode(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix custody mode check")
	}
	dir := t.TempDir()
	if err := os.Chmod(dir, 0775); err != nil {
		t.Fatalf("make DAST state dir permissive: %v", err)
	}

	secureTestStateDir(t, dir)

	info, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("stat DAST state dir: %v", err)
	}
	if got := info.Mode().Perm(); got != 0700 {
		t.Fatalf("DAST state dir mode = %04o, want 0700", got)
	}
}

// postJSON fires a POST with an optional Bearer token and a JSON body, returning the status code.
func postJSON(t *testing.T, url, bearer string, body any) int {
	t.Helper()
	buf, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(buf))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST %s: %v", url, err)
	}
	defer func() { _, _ = io.Copy(io.Discard, resp.Body); _ = resp.Body.Close() }()
	return resp.StatusCode
}

// TestDAST_EnrollmentTokenTTLCapped (S6) — an over-cap TTL is rejected at the live wire (400),
// a within-cap TTL still mints (200). Negative control: remove the maxEnrollmentTokenTTLSeconds
// clamp in handler_deploy.go and the over-cap POST returns 200 → this test goes RED.
func TestDAST_EnrollmentTokenTTLCapped(t *testing.T) {
	srv := bootController(t)
	url := srv.panel + operatorBase + "enrollment-token"

	overCap := postJSON(t, url, dastOpToken, map[string]any{"node_id": "node-1", "ttl_seconds": 8 * 24 * 60 * 60})
	if overCap != http.StatusBadRequest {
		t.Fatalf("over-cap TTL: live status %d, want 400 (the S6 clamp must fire at the wire)", overCap)
	}
	withinCap := postJSON(t, url, dastOpToken, map[string]any{"node_id": "node-1", "ttl_seconds": 3600})
	if withinCap != http.StatusOK {
		t.Fatalf("within-cap TTL: live status %d, want 200", withinCap)
	}
}

// TestDAST_RevokeBlocksTokenResurrection (S4/S5) — after a node is revoked, a still-held enrollment
// token can no longer re-enroll (resurrect) it. Must NOT be 200 (401 = token purged on revoke / 409 =
// the NodeRevoked guard). Negative control: drop the PurgeEnrollmentTokensForNode call AND the
// ErrNodeRevoked guard locally → the replay returns 200 → this test goes RED.
func TestDAST_RevokeBlocksTokenResurrection(t *testing.T) {
	srv := bootController(t)

	wgPriv, err := wgtypes.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("GeneratePrivateKey: %v", err)
	}
	// 1. Enroll node-1 with the pre-minted token (the legitimate first enrollment).
	if st := postJSON(t, srv.agent+agentBase+"enroll", "", map[string]any{
		"enrollment_token": srv.enroll, "node_id": "node-1", "wg_public_key": wgPriv.PublicKey().String(),
	}); st != http.StatusOK {
		t.Fatalf("initial enroll: live status %d, want 200", st)
	}
	// 2. Mint a SECOND, still-outstanding token for node-1 (the leak/takeover vector).
	leak := mintEnrollmentToken(t, srv)
	// 3. Revoke node-1.
	if st := postJSON(t, srv.panel+operatorBase+"revoke", dastOpToken, map[string]any{"node_id": "node-1"}); st != http.StatusOK {
		t.Fatalf("revoke: live status %d, want 200", st)
	}
	// 4. Replay the leaked token — resurrection must be refused.
	wgPriv2, _ := wgtypes.GeneratePrivateKey()
	st := postJSON(t, srv.agent+agentBase+"enroll", "", map[string]any{
		"enrollment_token": leak, "node_id": "node-1", "wg_public_key": wgPriv2.PublicKey().String(),
	})
	if st == http.StatusOK {
		t.Fatalf("revoked node re-enrolled with an outstanding token (200) — resurrection NOT blocked at the wire")
	}
	if st != http.StatusUnauthorized && st != http.StatusConflict {
		t.Fatalf("resurrection attempt: live status %d, want 401 (token purged) or 409 (revoked guard)", st)
	}
}

// mintEnrollmentToken asks the operator route for a fresh enrollment token for node-1 and returns it.
func mintEnrollmentToken(t *testing.T, srv *server) string {
	t.Helper()
	buf, _ := json.Marshal(map[string]any{"node_id": "node-1", "ttl_seconds": 3600})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, srv.panel+operatorBase+"enrollment-token", bytes.NewReader(buf))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+dastOpToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("mint enrollment token: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("mint enrollment token: status %d, want 200", resp.StatusCode)
	}
	var out struct {
		Token string `json:"token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil || out.Token == "" {
		t.Fatalf("decode minted token: err=%v empty=%v", err, out.Token == "")
	}
	return out.Token
}
