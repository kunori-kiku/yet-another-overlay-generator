package agent

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

// TestDecideSelfUpdate is the pure decision table (plan-9): noop / downgrade-refuse / floor-refuse
// / forced / after-apply / misconfig.
func TestDecideSelfUpdate(t *testing.T) {
	cat := func(ver, min string) *agentCatalog { return &agentCatalog{Version: ver, MinVersion: min} }
	cases := []struct {
		name      string
		cat       *agentCatalog
		running   string
		floor     string
		abandoned string
		want      updateDecision
	}{
		{"no catalog", nil, "1.0.0", "", "", updateSkip},
		{"no version", &agentCatalog{}, "1.0.0", "", "", updateSkip},
		{"already at desired", cat("1.0.0", ""), "1.0.0", "", "", updateSkip},
		{"after-apply forward", cat("1.1.0", ""), "1.0.0", "", "", updateAfterApply},
		{"downgrade below running refused", cat("1.0.0", ""), "1.1.0", "", "", updateRefuse},
		{"downgrade below floor refused", cat("1.1.0", ""), "1.0.0", "1.2.0", "", updateRefuse},
		{"forced when below min", cat("1.2.0", "1.2.0"), "1.0.0", "", "", updateForced},
		{"forced target reaches min", cat("1.3.0", "1.2.0"), "1.0.0", "", "", updateForced},
		{"forced but target below min is misconfig", cat("1.1.0", "1.2.0"), "1.0.0", "", "", updateRefuse},
		{"legacy empty running updates", cat("1.0.0", ""), "", "", "", updateAfterApply},
		{"legacy empty running forced below min", cat("1.0.0", "1.0.0"), "", "", "", updateForced},
		{"abandoned target refused", cat("1.1.0", ""), "1.0.0", "", "1.1.0", updateRefuse},
		{"non-abandoned target proceeds", cat("1.2.0", ""), "1.0.0", "", "1.1.0", updateAfterApply},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, reason := decideSelfUpdate(tc.cat, tc.running, tc.floor, tc.abandoned)
			if got != tc.want {
				t.Errorf("decideSelfUpdate = %v (%s), want %v", got, reason, tc.want)
			}
		})
	}
}

// TestParseAgentCatalog covers present/absent/no-version/malformed.
func TestParseAgentCatalog(t *testing.T) {
	if parseAgentCatalog(map[string][]byte{}) != nil {
		t.Errorf("absent artifacts.json must yield nil")
	}
	if parseAgentCatalog(map[string][]byte{"artifacts.json": []byte(`{"agent":{}}`)}) != nil {
		t.Errorf("empty agent version must yield nil")
	}
	if parseAgentCatalog(map[string][]byte{"artifacts.json": []byte(`not json`)}) != nil {
		t.Errorf("malformed artifacts.json must yield nil (fail-safe)")
	}
	cat := parseAgentCatalog(map[string][]byte{"artifacts.json": []byte(
		`{"schema":1,"agent":{"version":"1.2.0","min_version":"1.1.0","release_url":"https://x/dl","bins":{"linux-amd64":{"asset":"a","sha256":"deadbeef"}}}}`)})
	if cat == nil || cat.Version != "1.2.0" || cat.MinVersion != "1.1.0" || cat.ReleaseURL != "https://x/dl" {
		t.Fatalf("parsed catalog wrong: %+v", cat)
	}
	if cat.Bins["linux-amd64"].SHA256 != "deadbeef" {
		t.Errorf("bin pin not parsed: %+v", cat.Bins)
	}
}

// fakeBinary writes an executable shell script that prints version (the self-test reads this), and
// returns its bytes + hex SHA-256. Linux-only (CI + dev are linux).
func fakeBinary(t *testing.T, version string) ([]byte, string) {
	t.Helper()
	content := []byte("#!/bin/sh\necho " + version + "\n")
	sum := sha256.Sum256(content)
	return content, hex.EncodeToString(sum[:])
}

// stubSwap points osExecutable at `self` and records the re-exec target instead of replacing the
// process. Returns the recorded-target pointer and a restore func.
func stubSwap(t *testing.T, self string) (*string, func()) {
	t.Helper()
	oldExec, oldOSExe := execFn, osExecutable
	var execed string
	execFn = func(argv0 string, argv, env []string) error { execed = argv0; return nil }
	osExecutable = func() (string, error) { return self, nil }
	return &execed, func() { execFn, osExecutable = oldExec, oldOSExe }
}

func selfUpdateCatalog(t *testing.T, srv *httptest.Server, version, sha string) *agentCatalog {
	t.Helper()
	return &agentCatalog{
		Version:    version,
		ReleaseURL: srv.URL + "/dl",
		Bins:       map[string]binPin{"linux-" + runtime.GOARCH: {Asset: "yaog-agent-linux-" + runtime.GOARCH, SHA256: sha}},
	}
}

// TestPerformSelfUpdate_Happy: download + verify-against-pin + self-test + swap + re-exec; the
// breadcrumb is written and the binary on disk is replaced.
func TestPerformSelfUpdate_Happy(t *testing.T) {
	if runtime.GOARCH != "amd64" && runtime.GOARCH != "arm64" {
		t.Skipf("self-update scoped to amd64/arm64; arch is %s", runtime.GOARCH)
	}
	bin, sha := fakeBinary(t, "1.2.0")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write(bin) }))
	defer srv.Close()

	dir := t.TempDir()
	self := filepath.Join(dir, "yaog-agent")
	if err := os.WriteFile(self, []byte("OLD BINARY"), 0o755); err != nil {
		t.Fatal(err)
	}
	stateDir := filepath.Join(dir, "state")
	execed, restore := stubSwap(t, self)
	defer restore()

	cfg := &Config{NodeID: "n1", StateDir: stateDir}
	cat := selfUpdateCatalog(t, srv, "1.2.0", sha)
	swapped, err := performSelfUpdate(cfg, cat, "1.0.0", "", io.Discard)
	if err != nil {
		t.Fatalf("performSelfUpdate: %v", err)
	}
	if !swapped {
		t.Errorf("a completed swap must report swapped=true")
	}
	if *execed != self {
		t.Errorf("re-exec target = %q, want %q", *execed, self)
	}
	got, _ := os.ReadFile(self)
	if string(got) != string(bin) {
		t.Errorf("binary not swapped in; on-disk content = %q", got)
	}
	st, _ := LoadState(stateDir)
	if st.PendingUpdate == nil || st.PendingUpdate.To != "1.2.0" || st.PendingUpdate.From != "1.0.0" {
		t.Errorf("breadcrumb not written: %+v", st.PendingUpdate)
	}
}

// TestPerformSelfUpdate_StateReadFailureIsFailClosed pins the custody boundary around the
// self-update breadcrumb. The swap must never replace unreadable state with a fresh, stripped
// record: that would erase PendingApply and the anti-rollback floors immediately before changing
// the running binary.
func TestPerformSelfUpdate_StateReadFailureIsFailClosed(t *testing.T) {
	if runtime.GOARCH != "amd64" && runtime.GOARCH != "arm64" {
		t.Skipf("self-update scoped to amd64/arm64; arch is %s", runtime.GOARCH)
	}
	bin, sha := fakeBinary(t, "1.2.0")

	t.Run("initial state read", func(t *testing.T) {
		var requests atomic.Int32
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			requests.Add(1)
			_, _ = w.Write(bin)
		}))
		defer srv.Close()

		dir := t.TempDir()
		self := filepath.Join(dir, "yaog-agent")
		if err := os.WriteFile(self, []byte("OLD"), 0o755); err != nil {
			t.Fatal(err)
		}
		stateDir := filepath.Join(dir, "state")
		if err := os.MkdirAll(stateDir, 0o700); err != nil {
			t.Fatal(err)
		}
		corrupt := []byte("{ unreadable custody state")
		if err := os.WriteFile(statePath(stateDir), corrupt, 0o600); err != nil {
			t.Fatal(err)
		}

		execed, restore := stubSwap(t, self)
		defer restore()
		swapped, err := performSelfUpdate(
			&Config{NodeID: "n1", StateDir: stateDir},
			selfUpdateCatalog(t, srv, "1.2.0", sha),
			"1.0.0", "", io.Discard,
		)
		if err == nil || !strings.Contains(err.Error(), "load state before self-update") {
			t.Fatalf("initial state failure = (swapped=%v, err=%v), want fail-closed read error", swapped, err)
		}
		if swapped || *execed != "" || requests.Load() != 0 {
			t.Fatalf("unreadable state reached download/swap: swapped=%v execed=%q requests=%d", swapped, *execed, requests.Load())
		}
		if got, readErr := os.ReadFile(statePath(stateDir)); readErr != nil || !bytes.Equal(got, corrupt) {
			t.Fatalf("unreadable state was replaced: %q, %v", got, readErr)
		}
		if got, readErr := os.ReadFile(self); readErr != nil || string(got) != "OLD" {
			t.Fatalf("binary changed despite unreadable state: %q, %v", got, readErr)
		}
	})

	t.Run("reload after download", func(t *testing.T) {
		requestStarted := make(chan struct{})
		releaseDownload := make(chan struct{})
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			close(requestStarted)
			<-releaseDownload
			_, _ = w.Write(bin)
		}))
		defer srv.Close()

		dir := t.TempDir()
		self := filepath.Join(dir, "yaog-agent")
		if err := os.WriteFile(self, []byte("OLD"), 0o755); err != nil {
			t.Fatal(err)
		}
		stateDir := filepath.Join(dir, "state")
		prior := &State{
			NodeID:            "n1",
			LastCompiledAt:    "2026-07-16T03:00:00Z",
			LastChecksum:      "last-good",
			MembershipEpoch:   7,
			AgentVersionFloor: "1.0.0",
			PendingApply: &PendingApply{
				CompiledAt:      "2026-07-16T04:00:00Z",
				BundleSHA256:    strings.Repeat("a", 64),
				Action:          LastActionApply,
				StartedAt:       "2026-07-16T04:01:00Z",
				MembershipEpoch: 8,
			},
		}
		if err := SaveState(stateDir, prior); err != nil {
			t.Fatal(err)
		}

		execed, restore := stubSwap(t, self)
		defer restore()
		type result struct {
			swapped bool
			err     error
		}
		done := make(chan result, 1)
		go func() {
			swapped, err := performSelfUpdate(
				&Config{NodeID: "n1", StateDir: stateDir},
				selfUpdateCatalog(t, srv, "1.2.0", sha),
				"1.0.0", "", io.Discard,
			)
			done <- result{swapped: swapped, err: err}
		}()
		<-requestStarted
		corrupt := []byte("{ custody failed during download")
		if err := os.WriteFile(statePath(stateDir), corrupt, 0o600); err != nil {
			t.Fatal(err)
		}
		close(releaseDownload)

		gotResult := <-done
		if gotResult.err == nil || !strings.Contains(gotResult.err.Error(), "reload state before self-update breadcrumb") {
			t.Fatalf("late state failure = (swapped=%v, err=%v), want fail-closed reload error", gotResult.swapped, gotResult.err)
		}
		if gotResult.swapped || *execed != "" {
			t.Fatalf("late unreadable state reached swap: swapped=%v execed=%q", gotResult.swapped, *execed)
		}
		if got, readErr := os.ReadFile(statePath(stateDir)); readErr != nil || !bytes.Equal(got, corrupt) {
			t.Fatalf("late unreadable state was replaced: %q, %v", got, readErr)
		}
		if got, readErr := os.ReadFile(self); readErr != nil || string(got) != "OLD" {
			t.Fatalf("binary changed after late state failure: %q, %v", got, readErr)
		}
		if _, statErr := os.Stat(self + ".bak"); !os.IsNotExist(statErr) {
			t.Fatalf("rollback backup created despite pre-swap refusal: %v", statErr)
		}
	})
}

// TestPerformSelfUpdate_HashMismatchRefused is the CUSTODY guard: a binary whose bytes do not
// match the signed pin is NEVER swapped in or exec'd.
func TestPerformSelfUpdate_HashMismatchRefused(t *testing.T) {
	if runtime.GOARCH != "amd64" && runtime.GOARCH != "arm64" {
		t.Skipf("arch %s", runtime.GOARCH)
	}
	bin, _ := fakeBinary(t, "1.2.0")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write(bin) }))
	defer srv.Close()

	dir := t.TempDir()
	self := filepath.Join(dir, "yaog-agent")
	_ = os.WriteFile(self, []byte("OLD"), 0o755)
	execed, restore := stubSwap(t, self)
	defer restore()

	cfg := &Config{NodeID: "n1", StateDir: filepath.Join(dir, "state")}
	cat := selfUpdateCatalog(t, srv, "1.2.0", "00"+hex.EncodeToString(make([]byte, 31))) // wrong 64-hex
	swapped, err := performSelfUpdate(cfg, cat, "1.0.0", "", io.Discard)
	if err == nil {
		t.Fatalf("expected a hash-mismatch refusal")
	}
	if swapped {
		t.Errorf("CUSTODY VIOLATION: reported swapped=true despite hash mismatch")
	}
	if *execed != "" {
		t.Errorf("CUSTODY VIOLATION: re-exec happened despite hash mismatch")
	}
	if got, _ := os.ReadFile(self); string(got) != "OLD" {
		t.Errorf("CUSTODY VIOLATION: binary swapped despite hash mismatch (content=%q)", got)
	}
	if _, e := os.Stat(self + ".bak"); e == nil {
		t.Errorf("no .bak should exist on a refused update")
	}
}

// TestPerformSelfUpdate_SelfTestFailRefused: a hash-matching binary that reports the WRONG version
// is refused (no swap, no exec).
func TestPerformSelfUpdate_SelfTestFailRefused(t *testing.T) {
	if runtime.GOARCH != "amd64" && runtime.GOARCH != "arm64" {
		t.Skipf("arch %s", runtime.GOARCH)
	}
	bin, sha := fakeBinary(t, "9.9.9") // prints 9.9.9 but the catalog desires 1.2.0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write(bin) }))
	defer srv.Close()

	dir := t.TempDir()
	self := filepath.Join(dir, "yaog-agent")
	_ = os.WriteFile(self, []byte("OLD"), 0o755)
	execed, restore := stubSwap(t, self)
	defer restore()

	cfg := &Config{NodeID: "n1", StateDir: filepath.Join(dir, "state")}
	cat := selfUpdateCatalog(t, srv, "1.2.0", sha) // hash matches the 9.9.9 binary, but version != desired
	swapped, err := performSelfUpdate(cfg, cat, "1.0.0", "", io.Discard)
	if err == nil {
		t.Fatalf("expected a self-test version-mismatch refusal")
	}
	if swapped || *execed != "" || func() bool { g, _ := os.ReadFile(self); return string(g) != "OLD" }() {
		t.Errorf("CUSTODY VIOLATION: swapped/exec'd a binary that failed its self-test")
	}
}

// TestPerformSelfUpdate_PostSwapExecFailKeepsBreadcrumb pins the R1-1 fix: when the swap completes
// but the re-exec fails, performSelfUpdate reports swapped=true (so the caller must NOT recordFailure)
// and the on-disk breadcrumb survives for the next-boot reconcile.
func TestPerformSelfUpdate_PostSwapExecFailKeepsBreadcrumb(t *testing.T) {
	if runtime.GOARCH != "amd64" && runtime.GOARCH != "arm64" {
		t.Skipf("arch %s", runtime.GOARCH)
	}
	bin, sha := fakeBinary(t, "1.2.0")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write(bin) }))
	defer srv.Close()

	dir := t.TempDir()
	self := filepath.Join(dir, "yaog-agent")
	_ = os.WriteFile(self, []byte("OLD"), 0o755)
	stateDir := filepath.Join(dir, "state")

	oldExec, oldOSExe := execFn, osExecutable
	defer func() { execFn, osExecutable = oldExec, oldOSExe }()
	execFn = func(string, []string, []string) error { return fmt.Errorf("exec failed") } // simulate a post-swap exec failure
	osExecutable = func() (string, error) { return self, nil }

	cfg := &Config{NodeID: "n1", StateDir: stateDir}
	cat := selfUpdateCatalog(t, srv, "1.2.0", sha)
	swapped, err := performSelfUpdate(cfg, cat, "1.0.0", "", io.Discard)
	if err == nil || !swapped {
		t.Fatalf("post-swap exec failure must report (swapped=true, err!=nil); got (%v, %v)", swapped, err)
	}
	st, _ := LoadState(stateDir)
	if st.PendingUpdate == nil || st.PendingUpdate.To != "1.2.0" {
		t.Errorf("breadcrumb must survive a post-swap exec failure (R1-1); got %+v", st.PendingUpdate)
	}
	if got, _ := os.ReadFile(self); string(got) != string(bin) {
		t.Errorf("binary should be swapped before the failed exec; on-disk=%q", got)
	}
}

// TestPerformSelfUpdate_InFlightGuard pins NEW-MAJOR-1: a second performSelfUpdate while a breadcrumb
// is already pending must NOT re-swap (it would overwrite the .bak rollback target with the
// already-installed new binary and reset Attempts).
func TestPerformSelfUpdate_InFlightGuard(t *testing.T) {
	if runtime.GOARCH != "amd64" && runtime.GOARCH != "arm64" {
		t.Skipf("arch %s", runtime.GOARCH)
	}
	bin, sha := fakeBinary(t, "1.2.0")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write(bin) }))
	defer srv.Close()
	dir := t.TempDir()
	self := filepath.Join(dir, "yaog-agent")
	_ = os.WriteFile(self, []byte("NEW"), 0o755)        // already swapped
	_ = os.WriteFile(self+".bak", []byte("OLD"), 0o755) // the precious rollback target
	stateDir := filepath.Join(dir, "state")
	mustSave(t, stateDir, &State{NodeID: "n1", PendingUpdate: &PendingUpdate{From: "1.0.0", To: "1.2.0", Attempts: 2}})

	_, restore := stubSwap(t, self)
	defer restore()
	cfg := &Config{NodeID: "n1", StateDir: stateDir}
	cat := selfUpdateCatalog(t, srv, "1.2.0", sha)
	swapped, err := performSelfUpdate(cfg, cat, "1.0.0", "", io.Discard)
	if err == nil || swapped {
		t.Fatalf("an in-flight breadcrumb must block a re-swap; got (swapped=%v, err=%v)", swapped, err)
	}
	if got, _ := os.ReadFile(self + ".bak"); string(got) != "OLD" {
		t.Errorf("the .bak rollback target must NOT be overwritten by a blocked re-swap; got %q", got)
	}
	st, _ := LoadState(stateDir)
	if st.PendingUpdate.Attempts != 2 {
		t.Errorf("a blocked re-swap must not reset Attempts; got %d, want 2", st.PendingUpdate.Attempts)
	}
}

// TestPerformSelfUpdate_ProxyFallbackToDirect pins plan-8 Part B: when the proxy source fails (a
// gh-proxy timeout/error — the live failure mode), performSelfUpdate falls back to a DIRECT GitHub
// fetch and still swaps. The proxy is modeled as a server that always 500s; ghProxy is its URL prefix
// (so ghProxy+ReleaseURL routes to it), while the direct ReleaseURL serves the real binary.
func TestPerformSelfUpdate_ProxyFallbackToDirect(t *testing.T) {
	if runtime.GOARCH != "amd64" && runtime.GOARCH != "arm64" {
		t.Skipf("arch %s", runtime.GOARCH)
	}
	bin, sha := fakeBinary(t, "1.2.0")
	direct := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write(bin) }))
	defer direct.Close()
	var proxyHits int32
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&proxyHits, 1)
		w.WriteHeader(http.StatusBadGateway) // the proxy is down/slow → 502, as in the live log
	}))
	defer proxy.Close()

	dir := t.TempDir()
	self := filepath.Join(dir, "yaog-agent")
	_ = os.WriteFile(self, []byte("OLD"), 0o755)
	execed, restore := stubSwap(t, self)
	defer restore()

	cfg := &Config{NodeID: "n1", StateDir: filepath.Join(dir, "state")}
	cat := selfUpdateCatalog(t, direct, "1.2.0", sha) // ReleaseURL = direct.URL + "/dl"
	swapped, err := performSelfUpdate(cfg, cat, "1.0.0", proxy.URL+"/", io.Discard)
	if err != nil {
		t.Fatalf("fallback to direct must succeed; got %v", err)
	}
	if !swapped || *execed != self {
		t.Errorf("a fallback download must still swap+re-exec; swapped=%v execed=%q", swapped, *execed)
	}
	if atomic.LoadInt32(&proxyHits) == 0 {
		t.Errorf("the proxy source must be tried FIRST (then fall back to direct)")
	}
	if got, _ := os.ReadFile(self); string(got) != string(bin) {
		t.Errorf("direct fallback binary not swapped in; on-disk=%q", got)
	}
}

// TestStallReader_FiresOnIdle pins plan-8 Part C: with no bytes flowing for the timeout, the stall
// watchdog cancels the request context and stalled() reports true (so downloadTo surfaces a clear
// stall error instead of an opaque context-cancel).
func TestStallReader_FiresOnIdle(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	// blockingReader.Read blocks until the ctx is cancelled (the watchdog fires cancel), modeling an
	// http body whose read is aborted when the request context is cancelled — so the io.Copy goroutine
	// unwinds cleanly rather than leaking.
	sr := newStallReader(blockingReader{ctx: ctx}, 30*time.Millisecond, cancel)
	done := make(chan struct{})
	go func() { _, _ = io.Copy(io.Discard, sr); close(done) }()
	select {
	case <-ctx.Done():
		if !sr.stalled() {
			t.Errorf("stalled() must be true after the watchdog fired")
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("stall watchdog did not fire within 2s (timeout was 30ms)")
	}
	sr.stop()
	<-done // the reader unblocks on cancel; assert no goroutine leak
}

// TestStallReader_ResetsOnProgress pins the other half of Part C: a slow-but-progressing transfer
// (bytes arriving faster than the timeout) keeps resetting the watchdog and is NOT aborted.
func TestStallReader_ResetsOnProgress(t *testing.T) {
	_, cancel := context.WithCancel(context.Background())
	// dripReader returns one byte every 5ms for 8 chunks, then EOF — each chunk is well within the
	// 40ms stall window, so the watchdog must never fire.
	sr := newStallReader(&dripReader{remaining: 8, gap: 5 * time.Millisecond}, 40*time.Millisecond, cancel)
	n, err := io.Copy(io.Discard, sr)
	sr.stop()
	if err != nil {
		t.Fatalf("a progressing transfer must not error; got %v", err)
	}
	if n != 8 {
		t.Errorf("expected 8 bytes copied, got %d", n)
	}
	if sr.stalled() {
		t.Errorf("the watchdog must NOT fire while bytes keep flowing")
	}
}

// blockingReader.Read blocks until its context is cancelled, then returns the context error.
type blockingReader struct{ ctx context.Context }

func (b blockingReader) Read(p []byte) (int, error) { <-b.ctx.Done(); return 0, b.ctx.Err() }

// dripReader returns a single byte per Read, `gap` apart, `remaining` times, then io.EOF.
type dripReader struct {
	remaining int
	gap       time.Duration
}

func (d *dripReader) Read(p []byte) (int, error) {
	if d.remaining <= 0 {
		return 0, io.EOF
	}
	time.Sleep(d.gap)
	d.remaining--
	p[0] = 'x'
	return 1, nil
}

// TestDownloadTo_SlowProgressingSucceeds is the integrated Part-C happy path: a body that arrives in
// small chunks with brief gaps (well inside the stall window) downloads fully — the behavior the old
// single TOTAL timeout broke.
func TestDownloadTo_SlowProgressingSucceeds(t *testing.T) {
	payload := bytes.Repeat([]byte("y"), 4096)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fl, _ := w.(http.Flusher)
		for i := 0; i < len(payload); i += 512 {
			_, _ = w.Write(payload[i : i+512])
			if fl != nil {
				fl.Flush()
			}
			time.Sleep(2 * time.Millisecond)
		}
	}))
	defer srv.Close()
	out := filepath.Join(t.TempDir(), "dl")
	if err := downloadTo(context.Background(), srv.URL, out); err != nil {
		t.Fatalf("a slow-but-progressing download must succeed; got %v", err)
	}
	if got, _ := os.ReadFile(out); !bytes.Equal(got, payload) {
		t.Errorf("downloaded %d bytes, want %d (content mismatch)", len(got), len(payload))
	}
}

// TestDownloadTo_Non200 surfaces a non-200 (e.g. a 502 from a down proxy) as an error.
func TestDownloadTo_Non200(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer srv.Close()
	err := downloadTo(context.Background(), srv.URL, filepath.Join(t.TempDir(), "dl"))
	if err == nil || !strings.Contains(err.Error(), "502") {
		t.Errorf("a non-200 must error with the status; got %v", err)
	}
}

// TestDownloadTo_StallSurfacesClearError pins the integrated Part-C stall path: a body that sends
// headers then stops returning bytes trips the watchdog (shrunk here so the test is fast) and yields
// the clear "download stalled" error, not an opaque context-cancel.
func TestDownloadTo_StallSurfacesClearError(t *testing.T) {
	old := selfUpdateStallTimeout
	selfUpdateStallTimeout = 40 * time.Millisecond
	defer func() { selfUpdateStallTimeout = old }()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		if fl, ok := w.(http.Flusher); ok {
			fl.Flush() // headers out, then no body bytes — the read stalls
		}
		<-r.Context().Done() // unblock when the client cancels (no handler-goroutine leak)
	}))
	defer srv.Close()
	err := downloadTo(context.Background(), srv.URL, filepath.Join(t.TempDir(), "dl"))
	if err == nil || !strings.Contains(err.Error(), "stalled") {
		t.Errorf("a stalled body must surface a clear 'stalled' error; got %v", err)
	}
}

// TestDownloadTo_HeaderTimeout: a mirror that accepts the connection but never sends response headers
// fails fast via ResponseHeaderTimeout (shrunk here), rather than hanging to the absolute cap.
func TestDownloadTo_HeaderTimeout(t *testing.T) {
	old := selfUpdateHeaderTimeout
	selfUpdateHeaderTimeout = 40 * time.Millisecond
	defer func() { selfUpdateHeaderTimeout = old }()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done() // never write headers
	}))
	defer srv.Close()
	err := downloadTo(context.Background(), srv.URL, filepath.Join(t.TempDir(), "dl"))
	if err == nil {
		t.Errorf("a mirror that never sends headers must error fast (ResponseHeaderTimeout)")
	}
}

// TestReconcileSelfUpdatePromote_Probation: booted as the target, a clean health gate marks the
// update PROBATIONARY (Confirmed) — it does NOT advance the floor, drop .bak, or re-exec yet;
// FinalizeSelfUpdate (after a full cycle) does that.
func TestReconcileSelfUpdatePromote_Probation(t *testing.T) {
	dir := t.TempDir()
	self := filepath.Join(dir, "yaog-agent")
	_ = os.WriteFile(self, []byte("NEW"), 0o755)
	_ = os.WriteFile(self+".bak", []byte("OLD"), 0o755)
	stateDir := filepath.Join(dir, "state")
	mustSave(t, stateDir, &State{NodeID: "n1", PendingUpdate: &PendingUpdate{From: "1.0.0", To: "1.1.0"}})

	execed, restore := stubSwap(t, self)
	defer restore()
	if err := ReconcileSelfUpdatePromote(stateDir, "1.1.0", func() error { return nil }, io.Discard); err != nil {
		t.Fatalf("promote: %v", err)
	}

	if *execed != "" {
		t.Errorf("probation must not re-exec")
	}
	st, _ := LoadState(stateDir)
	if st.PendingUpdate == nil || !st.PendingUpdate.Confirmed {
		t.Fatalf("health-gate pass must mark the breadcrumb Confirmed (probationary), got %+v", st.PendingUpdate)
	}
	if st.AgentVersionFloor == "1.1.0" {
		t.Errorf("floor must NOT advance until FinalizeSelfUpdate (probation, not yet promoted)")
	}
	if _, e := os.Stat(self + ".bak"); e != nil {
		t.Errorf(".bak must be KEPT during probation (rollback target)")
	}

	// FinalizeSelfUpdate (after a clean cycle) promotes: floor advances, breadcrumb + .bak cleared.
	FinalizeSelfUpdate(stateDir, "1.1.0", io.Discard)
	st, _ = LoadState(stateDir)
	if st.PendingUpdate != nil {
		t.Errorf("finalize must clear the breadcrumb")
	}
	if st.AgentVersionFloor != "1.1.0" {
		t.Errorf("finalize must advance the floor to 1.1.0; got %q", st.AgentVersionFloor)
	}
	if _, e := os.Stat(self + ".bak"); e == nil {
		t.Errorf(".bak must be removed after finalize")
	}
}

// TestFinalizeSelfUpdate_ClearsSelfUpdateBlocked pins the beta.16 sticky-Blocked fix: a SUCCESSFUL
// self-update (finalize) drops a leftover SelfUpdateBlocked latch from the earlier failed/deferred
// attempts, so the node stops reporting "Blocked" once it is actually on the target — without waiting
// for the next generation apply or a reachable idle retry.
func TestFinalizeSelfUpdate_ClearsSelfUpdateBlocked(t *testing.T) {
	dir := t.TempDir()
	self := filepath.Join(dir, "yaog-agent")
	_ = os.WriteFile(self, []byte("NEW"), 0o755)
	_ = os.WriteFile(self+".bak", []byte("OLD"), 0o755)
	stateDir := filepath.Join(dir, "state")
	mustSave(t, stateDir, &State{
		NodeID:                "n1",
		SelfUpdateBlocked:     "could not download the update binary from the release",
		AbandonedAgentVersion: "0.9.0",
		AbandonedReason:       "the update was rolled back",
		PendingUpdate:         &PendingUpdate{From: "1.0.0", To: "1.1.0", Confirmed: true},
	})
	_, restore := stubSwap(t, self)
	defer restore()

	FinalizeSelfUpdate(stateDir, "1.1.0", io.Discard)

	st, _ := LoadState(stateDir)
	if st.PendingUpdate != nil {
		t.Errorf("finalize must clear the breadcrumb")
	}
	if st.AgentVersionFloor != "1.1.0" {
		t.Errorf("finalize must advance the floor; got %q", st.AgentVersionFloor)
	}
	if st.SelfUpdateBlocked != "" {
		t.Errorf("finalize must clear the stale SelfUpdateBlocked latch (beta.16); got %q", st.SelfUpdateBlocked)
	}
	if st.AbandonedAgentVersion != "" || st.AbandonedReason != "" {
		t.Errorf("finalize must clear AbandonedAgentVersion+AbandonedReason (plan-9); got %q / %q", st.AbandonedAgentVersion, st.AbandonedReason)
	}
}

// Finalization has two phases: persist the advanced floor/cleared breadcrumb, then delete .bak.
// A failed first phase must retain both the old durable state and the only rollback artifact.
func TestFinalizeSelfUpdate_StateCommitFailureRetainsBreadcrumbAndBackup(t *testing.T) {
	dir := t.TempDir()
	self := filepath.Join(dir, "yaog-agent")
	if err := os.WriteFile(self, []byte("NEW"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(self+".bak", []byte("OLD"), 0o755); err != nil {
		t.Fatal(err)
	}
	stateDir := filepath.Join(dir, "state")
	if err := SaveState(stateDir, &State{
		NodeID:            "n1",
		AgentVersionFloor: "1.0.0",
		PendingUpdate:     &PendingUpdate{From: "1.0.0", To: "1.1.0", Attempts: 1, Confirmed: true},
	}); err != nil {
		t.Fatal(err)
	}

	_, restoreSwap := stubSwap(t, self)
	defer restoreSwap()
	originalSave := saveSelfUpdateTerminalState
	saveSelfUpdateTerminalState = func(string, *State) error { return fmt.Errorf("injected finalization sync failure") }
	t.Cleanup(func() { saveSelfUpdateTerminalState = originalSave })
	var stderr strings.Builder
	FinalizeSelfUpdate(stateDir, "1.1.0", &stderr)

	st, err := LoadState(stateDir)
	if err != nil {
		t.Fatal(err)
	}
	if st.PendingUpdate == nil || st.AgentVersionFloor != "1.0.0" {
		t.Fatalf("failed finalization changed durable custody state: %+v", st)
	}
	if got, err := os.ReadFile(self + ".bak"); err != nil || string(got) != "OLD" {
		t.Fatalf("failed finalization removed rollback backup: %q, %v", got, err)
	}
	if !strings.Contains(stderr.String(), "retaining breadcrumb and rollback backup") ||
		!strings.Contains(stderr.String(), "injected finalization sync failure") {
		t.Fatalf("finalization failure was not surfaced: %q", stderr.String())
	}
}

// TestFinalizeSelfUpdate_NoopLeavesBlockedIntact pins the custody guard the beta.16 Blocked-clear sits
// under: FinalizeSelfUpdate must be a NO-OP (latch + breadcrumb + floor untouched) when the breadcrumb
// is not Confirmed, or the running build is not the target. A genuinely-blocked node that has NOT yet
// reached the target must keep its Blocked latch so a stalled rollout stays visible.
func TestFinalizeSelfUpdate_NoopLeavesBlockedIntact(t *testing.T) {
	cases := []struct {
		name      string
		confirmed bool
		build     string // the running buildVersion passed to FinalizeSelfUpdate
	}{
		{"not confirmed", false, "1.1.0"},
		{"wrong build (not yet on target)", true, "1.0.0"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			self := filepath.Join(dir, "yaog-agent")
			_ = os.WriteFile(self, []byte("NEW"), 0o755)
			stateDir := filepath.Join(dir, "state")
			mustSave(t, stateDir, &State{
				NodeID:            "n1",
				AgentVersionFloor: "1.0.0",
				SelfUpdateBlocked: "could not download the update binary",
				PendingUpdate:     &PendingUpdate{From: "1.0.0", To: "1.1.0", Confirmed: tc.confirmed},
			})
			_, restore := stubSwap(t, self)
			defer restore()

			FinalizeSelfUpdate(stateDir, tc.build, io.Discard)

			st, _ := LoadState(stateDir)
			if st.PendingUpdate == nil {
				t.Errorf("a no-op finalize must NOT clear the breadcrumb")
			}
			if st.AgentVersionFloor != "1.0.0" {
				t.Errorf("a no-op finalize must NOT advance the floor; got %q", st.AgentVersionFloor)
			}
			if st.SelfUpdateBlocked == "" {
				t.Errorf("a no-op finalize must LEAVE the Blocked latch intact (a stalled rollout stays visible)")
			}
		})
	}
}

// TestReconcileSelfUpdatePromote_HealthFailureRetriesBeforeAbandon pins the rc.13 regression fix:
// one failed health GET keeps the new binary, breadcrumb, and rollback backup so systemd can retry.
// Only exhausting the shared persisted boot-attempt ceiling rolls back and abandons the target.
func TestReconcileSelfUpdatePromote_HealthFailureRetriesBeforeAbandon(t *testing.T) {
	dir := t.TempDir()
	self := filepath.Join(dir, "yaog-agent")
	_ = os.WriteFile(self, []byte("NEW-RETRYING"), 0o755)
	_ = os.WriteFile(self+".bak", []byte("OLD-GOOD"), 0o755)
	stateDir := filepath.Join(dir, "state")
	mustSave(t, stateDir, &State{NodeID: "n1", AgentVersionFloor: "1.0.0", PendingUpdate: &PendingUpdate{From: "1.0.0", To: "1.1.0"}})

	execed, restore := stubSwap(t, self)
	defer restore()
	var stderr strings.Builder
	for attempt := 1; attempt <= maxSelfUpdateAttempts; attempt++ {
		if err := ReconcileSelfUpdateEarly(stateDir, "1.1.0", io.Discard); err != nil {
			t.Fatalf("early reconcile attempt %d: %v", attempt, err)
		}
		err := ReconcileSelfUpdatePromote(stateDir, "1.1.0", func() error { return io.EOF }, &stderr)
		if err == nil || !strings.Contains(err.Error(), fmt.Sprintf("attempt %d/%d", attempt, maxSelfUpdateAttempts)) {
			t.Fatalf("health failure attempt %d must request a bounded retry, got %v", attempt, err)
		}
		st, loadErr := LoadState(stateDir)
		if loadErr != nil {
			t.Fatal(loadErr)
		}
		if st.PendingUpdate == nil || st.PendingUpdate.Attempts != attempt || st.PendingUpdate.Confirmed {
			t.Fatalf("attempt %d did not retain the unconfirmed breadcrumb: %+v", attempt, st.PendingUpdate)
		}
		if st.AbandonedAgentVersion != "" || st.AbandonedReason != "" {
			t.Fatalf("attempt %d abandoned the target prematurely: %q / %q", attempt, st.AbandonedAgentVersion, st.AbandonedReason)
		}
		if got, _ := os.ReadFile(self); string(got) != "NEW-RETRYING" {
			t.Fatalf("attempt %d rolled back prematurely; binary=%q", attempt, got)
		}
		if got, _ := os.ReadFile(self + ".bak"); string(got) != "OLD-GOOD" {
			t.Fatalf("attempt %d lost the rollback backup; backup=%q", attempt, got)
		}
		if *execed != "" {
			t.Fatalf("attempt %d re-execed before the ceiling: %q", attempt, *execed)
		}
	}
	if !strings.Contains(stderr.String(), "retrying after restart") {
		t.Fatalf("retry path did not explain its action: %q", stderr.String())
	}

	// The next boot crosses the same ceiling used for swap/crash failures and takes the existing
	// crash-safe rollback path before another health request can run.
	if err := ReconcileSelfUpdateEarly(stateDir, "1.1.0", io.Discard); err != nil {
		t.Fatalf("cap reconcile: %v", err)
	}
	if *execed != self {
		t.Errorf("attempt-cap rollback must re-exec the restored binary")
	}
	if got, _ := os.ReadFile(self); string(got) != "OLD-GOOD" {
		t.Errorf("binary not rolled back at the attempt cap; content=%q", got)
	}
	st, _ := LoadState(stateDir)
	if st.PendingUpdate != nil {
		t.Errorf("breadcrumb not cleared after attempt-cap rollback")
	}
	if st.AgentVersionFloor != "1.0.0" {
		t.Errorf("floor must be preserved (not advanced, not wiped) on rollback; got %q", st.AgentVersionFloor)
	}
	if st.AbandonedAgentVersion != "1.1.0" {
		t.Errorf("attempt-cap rollback must remember the abandoned target; got %q", st.AbandonedAgentVersion)
	}
	if st.AbandonedReason == "" || strings.Contains(st.AbandonedReason, "\n") {
		t.Errorf("rollback must record a curated one-line reason; got %q", st.AbandonedReason)
	}
}

// If state is unreadable when an attempt-cap rollback records abandonment, it may restore the binary
// but must not manufacture a fresh state that erases configuration/membership floors or PendingApply.
func TestRollbackAndAbandon_StateReloadFailureDoesNotWipeCustody(t *testing.T) {
	dir := t.TempDir()
	self := filepath.Join(dir, "yaog-agent")
	if err := os.WriteFile(self, []byte("NEW"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(self+".bak", []byte("OLD"), 0o755); err != nil {
		t.Fatal(err)
	}
	stateDir := filepath.Join(dir, "state")
	if err := SaveState(stateDir, &State{
		NodeID:            "n1",
		LastCompiledAt:    "2026-07-16T03:00:00Z",
		MembershipEpoch:   7,
		AgentVersionFloor: "1.0.0",
		PendingApply: &PendingApply{
			CompiledAt:      "2026-07-16T04:00:00Z",
			BundleSHA256:    strings.Repeat("a", 64),
			Action:          LastActionApply,
			MembershipEpoch: 8,
			StartedAt:       "2026-07-16T04:01:00Z",
		},
		PendingUpdate: &PendingUpdate{From: "1.0.0", To: "1.1.0", Attempts: maxSelfUpdateAttempts + 1},
	}); err != nil {
		t.Fatal(err)
	}

	execed, restore := stubSwap(t, self)
	defer restore()
	corrupt := []byte("{ custody failed during attempt-cap rollback")
	if err := os.WriteFile(statePath(stateDir), corrupt, 0o600); err != nil {
		t.Fatal(err)
	}
	var stderr strings.Builder
	rollbackAndAbandon(stateDir, "1.1.0", &PendingUpdate{
		From: "1.0.0", To: "1.1.0", Attempts: maxSelfUpdateAttempts + 1,
	}, "attempt cap exceeded", &stderr)

	if got, readErr := os.ReadFile(statePath(stateDir)); readErr != nil || !bytes.Equal(got, corrupt) {
		t.Fatalf("rollback replaced unreadable custody state: %q, %v", got, readErr)
	}
	if got, readErr := os.ReadFile(self); readErr != nil || string(got) != "OLD" {
		t.Fatalf("rollback did not restore prior binary: %q, %v", got, readErr)
	}
	if *execed != self {
		t.Fatalf("restored binary was not re-execed: %q", *execed)
	}
	if !strings.Contains(stderr.String(), "could not read custody state") {
		t.Fatalf("rollback state failure was not surfaced: %q", stderr.String())
	}
}

// When abandonment cannot be committed, a backup that was not consumed by a rollback must remain
// available and the pending breadcrumb must remain the durable source of truth for a later retry.
func TestRollbackAndAbandon_StateCommitFailureRetainsBreadcrumbAndBackup(t *testing.T) {
	dir := t.TempDir()
	self := filepath.Join(dir, "yaog-agent")
	if err := os.WriteFile(self, []byte("OLD"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(self+".bak", []byte("ROLLBACK"), 0o755); err != nil {
		t.Fatal(err)
	}
	stateDir := filepath.Join(dir, "state")
	pu := &PendingUpdate{From: "0.9.0", To: "1.1.0", Attempts: 4}
	if err := SaveState(stateDir, &State{
		NodeID:            "n1",
		AgentVersionFloor: "1.0.0",
		PendingUpdate:     pu,
	}); err != nil {
		t.Fatal(err)
	}

	_, restoreSwap := stubSwap(t, self)
	defer restoreSwap()
	originalSave := saveSelfUpdateTerminalState
	saveSelfUpdateTerminalState = func(string, *State) error { return fmt.Errorf("injected abandonment sync failure") }
	t.Cleanup(func() { saveSelfUpdateTerminalState = originalSave })
	var stderr strings.Builder
	// Running 1.0.0 is not the failed target 1.1.0, so this path must not consume .bak.
	rollbackAndAbandon(stateDir, "1.0.0", pu, "attempt cap exceeded", &stderr)

	st, err := LoadState(stateDir)
	if err != nil {
		t.Fatal(err)
	}
	if st.PendingUpdate == nil || st.AbandonedAgentVersion != "" || st.AgentVersionFloor != "1.0.0" {
		t.Fatalf("failed abandonment changed durable custody state: %+v", st)
	}
	if got, err := os.ReadFile(self + ".bak"); err != nil || string(got) != "ROLLBACK" {
		t.Fatalf("failed abandonment removed rollback backup: %q, %v", got, err)
	}
	if !strings.Contains(stderr.String(), "leaving its breadcrumb/backup intact") ||
		!strings.Contains(stderr.String(), "injected abandonment sync failure") {
		t.Fatalf("abandonment failure was not surfaced: %q", stderr.String())
	}
}

// TestReconcileSelfUpdatePromote_ProbationRebootResumes: a Confirmed binary that reboots before
// finalizing RESUMES probation (it does NOT immediately roll back — a benign reboot must not
// falsely abandon a healthy binary). A genuinely-crashing binary is bounded by the Attempts cap
// (Phase A), exercised separately.
func TestReconcileSelfUpdatePromote_ProbationRebootResumes(t *testing.T) {
	dir := t.TempDir()
	self := filepath.Join(dir, "yaog-agent")
	_ = os.WriteFile(self, []byte("NEW"), 0o755)
	_ = os.WriteFile(self+".bak", []byte("OLD"), 0o755)
	stateDir := filepath.Join(dir, "state")
	mustSave(t, stateDir, &State{NodeID: "n1", PendingUpdate: &PendingUpdate{From: "1.0.0", To: "1.1.0", Confirmed: true}})

	execed, restore := stubSwap(t, self)
	defer restore()
	if err := ReconcileSelfUpdatePromote(stateDir, "1.1.0", func() error { return nil }, io.Discard); err != nil {
		t.Fatalf("resume probation: %v", err)
	}

	if *execed != "" {
		t.Errorf("a benign probation reboot must NOT roll back (no re-exec); it should resume probation")
	}
	st, _ := LoadState(stateDir)
	if st.PendingUpdate == nil || !st.PendingUpdate.Confirmed {
		t.Errorf("resume must keep the Confirmed breadcrumb so the next cycle can finalize; got %+v", st.PendingUpdate)
	}
	if st.AbandonedAgentVersion != "" {
		t.Errorf("a benign probation reboot must NOT abandon the target; got %q", st.AbandonedAgentVersion)
	}
	if got, _ := os.ReadFile(self); string(got) != "NEW" {
		t.Errorf("resume must keep the new binary in place; got %q", got)
	}
}

// TestReconcileSelfUpdate_ProbationCrashLoopAbandons: a Confirmed binary that keeps rebooting
// (crashing during probation, never finalizing) is bounded by Phase A's Attempts cap — it rolls
// back to .bak and abandons the target.
func TestReconcileSelfUpdate_ProbationCrashLoopAbandons(t *testing.T) {
	dir := t.TempDir()
	self := filepath.Join(dir, "yaog-agent")
	_ = os.WriteFile(self, []byte("NEW-CRASHY"), 0o755)
	_ = os.WriteFile(self+".bak", []byte("OLD-GOOD"), 0o755)
	stateDir := filepath.Join(dir, "state")
	mustSave(t, stateDir, &State{NodeID: "n1", PendingUpdate: &PendingUpdate{From: "1.0.0", To: "1.1.0", Confirmed: true}})

	_, restore := stubSwap(t, self)
	defer restore()
	abandoned := false
	for i := 0; i < maxSelfUpdateAttempts+2; i++ {
		if err := ReconcileSelfUpdateEarly(stateDir, "1.1.0", io.Discard); err != nil { // Phase A bumps; abandons at cap
			t.Fatalf("early reconcile attempt %d: %v", i, err)
		}
		st, _ := LoadState(stateDir)
		if st.PendingUpdate == nil {
			abandoned = true
			if got, _ := os.ReadFile(self); string(got) != "OLD-GOOD" {
				t.Errorf("crash-loop abandon must roll back to .bak; got %q", got)
			}
			if st.AbandonedAgentVersion != "1.1.0" {
				t.Errorf("crash-loop abandon must remember the target; got %q", st.AbandonedAgentVersion)
			}
			break
		}
		if err := ReconcileSelfUpdatePromote(stateDir, "1.1.0", func() error { return nil }, io.Discard); err != nil { // resumes (Confirmed)
			t.Fatalf("resume probation: %v", err)
		}
	}
	if !abandoned {
		t.Errorf("a binary that crashes throughout probation must be abandoned at the attempt cap")
	}
}

// TestReconcileSelfUpdateEarly_AbandonAtCap: a target that never takes effect (boots keep coming up
// as `from`) is abandoned by the EARLY phase after the attempt cap — breadcrumb cleared, loop ends.
func TestReconcileSelfUpdateEarly_AbandonAtCap(t *testing.T) {
	dir := t.TempDir()
	self := filepath.Join(dir, "yaog-agent")
	_ = os.WriteFile(self, []byte("OLD"), 0o755) // still running `from` — swap never stuck
	stateDir := filepath.Join(dir, "state")
	mustSave(t, stateDir, &State{NodeID: "n1", PendingUpdate: &PendingUpdate{From: "1.0.0", To: "1.1.0"}})

	_, restore := stubSwap(t, self)
	defer restore()
	cleared := false
	for i := 0; i < maxSelfUpdateAttempts+2; i++ {
		if err := ReconcileSelfUpdateEarly(stateDir, "1.0.0", io.Discard); err != nil {
			t.Fatalf("early reconcile attempt %d: %v", i, err)
		}
		st, _ := LoadState(stateDir)
		if st.PendingUpdate == nil {
			cleared = true
			if st.AbandonedAgentVersion != "1.1.0" {
				t.Errorf("abandon-at-cap must remember the target; got %q", st.AbandonedAgentVersion)
			}
			break
		}
	}
	if !cleared {
		t.Errorf("a never-applying update must be abandoned at the attempt cap, not loop forever")
	}
}

func TestReconcileSelfUpdateEarlySerializesWithStateOwner(t *testing.T) {
	stateDir := filepath.Join(t.TempDir(), "state")
	mustSave(t, stateDir, &State{
		NodeID:        "n1",
		PendingUpdate: &PendingUpdate{From: "1.0.0", To: "1.1.0"},
	})
	release, err := acquireStateLock(stateDir)
	if err != nil {
		t.Fatalf("acquire competing state owner: %v", err)
	}
	if err := ReconcileSelfUpdateEarly(stateDir, "1.0.0", io.Discard); err == nil {
		_ = release()
		t.Fatal("early reconcile entered while the common state owner held the lease")
	}
	st, err := LoadState(stateDir)
	if err != nil {
		_ = release()
		t.Fatalf("load state after refused reconcile: %v", err)
	}
	if st.PendingUpdate == nil || st.PendingUpdate.Attempts != 0 {
		_ = release()
		t.Fatalf("refused reconcile mutated pending update: %+v", st.PendingUpdate)
	}
	if err := release(); err != nil {
		t.Fatalf("release competing state owner: %v", err)
	}

	if err := ReconcileSelfUpdateEarly(stateDir, "1.0.0", io.Discard); err != nil {
		t.Fatalf("reconcile after lease release: %v", err)
	}
	st, err = LoadState(stateDir)
	if err != nil {
		t.Fatalf("load state after serialized reconcile: %v", err)
	}
	if st.PendingUpdate == nil || st.PendingUpdate.Attempts != 1 {
		t.Fatalf("serialized reconcile attempts = %+v, want 1", st.PendingUpdate)
	}
}

// TestRecordPreservesSelfUpdateState pins F1/R1-5/R1-1: a routine apply (success OR failure) must
// NOT wipe the health-confirmed AgentVersionFloor or an in-flight breadcrumb (else a later signed
// downgrade slips below the floor, or a forced re-exec-fail loses the breadcrumb and bricks).
func TestRecordPreservesSelfUpdateState(t *testing.T) {
	dir := t.TempDir()
	cfg := &Config{NodeID: "n1", StateDir: dir}
	prev := &State{
		NodeID:                "n1",
		AgentVersionFloor:     "1.1.0",
		AbandonedAgentVersion: "0.9.0",
		AbandonedReason:       "the update was rolled back",
		PendingUpdate:         &PendingUpdate{From: "1.1.0", To: "1.2.0", Attempts: 1},
		SelfUpdateBlocked:     "a stale deferred-update reason",
	}
	man := &manifestInfo{NodeID: "n1", CompiledAt: "2026-06-16T00:00:00Z", Checksum: "abc"}

	recordSuccess(cfg, prev, man, &VerifyResult{Signed: true}, 0, nil)
	st, _ := LoadState(dir)
	if st.AgentVersionFloor != "1.1.0" {
		t.Errorf("recordSuccess wiped AgentVersionFloor (custody regression); got %q", st.AgentVersionFloor)
	}
	if st.PendingUpdate == nil || st.PendingUpdate.To != "1.2.0" {
		t.Errorf("recordSuccess wiped the in-flight breadcrumb; got %+v", st.PendingUpdate)
	}
	if st.AbandonedAgentVersion != "0.9.0" {
		t.Errorf("recordSuccess wiped AbandonedAgentVersion; got %q", st.AbandonedAgentVersion)
	}
	if st.AbandonedReason != "the update was rolled back" {
		t.Errorf("recordSuccess wiped AbandonedReason (plan-9); got %q", st.AbandonedReason)
	}
	// plan-8 Part D: a clean (new-generation) apply must DROP the deferred-self-update Blocked latch —
	// recordSuccess rebuilds State and deliberately does NOT carry SelfUpdateBlocked forward (the
	// stable-generation clear lives in the retry path). Pin it so a future refactor that "preserves"
	// it cannot silently re-introduce the stuck-Blocked condition.
	if st.SelfUpdateBlocked != "" {
		t.Errorf("recordSuccess must clear SelfUpdateBlocked on a clean apply; got %q", st.SelfUpdateBlocked)
	}

	recordFailure(cfg, prev, "boom")
	st, _ = LoadState(dir)
	if st.AgentVersionFloor != "1.1.0" || st.PendingUpdate == nil || st.AbandonedAgentVersion != "0.9.0" || st.AbandonedReason != "the update was rolled back" {
		t.Errorf("recordFailure wiped self-update custody state; got floor=%q pending=%+v abandoned=%q reason=%q",
			st.AgentVersionFloor, st.PendingUpdate, st.AbandonedAgentVersion, st.AbandonedReason)
	}
}

// TestRenameOrCopy moves a file and leaves the destination executable with identical content.
func TestRenameOrCopy(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src")
	dst := filepath.Join(dir, "dst")
	_ = os.WriteFile(src, []byte("payload"), 0o755)
	if err := renameOrCopy(src, dst); err != nil {
		t.Fatalf("renameOrCopy: %v", err)
	}
	got, err := os.ReadFile(dst)
	if err != nil || string(got) != "payload" {
		t.Fatalf("dst content = %q err=%v", got, err)
	}
}

func mustSave(t *testing.T, stateDir string, s *State) {
	t.Helper()
	if err := SaveState(stateDir, s); err != nil {
		t.Fatalf("SaveState: %v", err)
	}
}

// ensure syscall is referenced (execFn type) even if the build tags vary.
var _ = syscall.Exec
