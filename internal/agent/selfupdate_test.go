package agent

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"syscall"
	"testing"
)

// TestDecideSelfUpdate is the pure decision table (plan-9): noop / downgrade-refuse / floor-refuse
// / forced / after-apply / misconfig.
func TestDecideSelfUpdate(t *testing.T) {
	cat := func(ver, min string) *agentCatalog { return &agentCatalog{Version: ver, MinVersion: min} }
	cases := []struct {
		name    string
		cat     *agentCatalog
		running string
		floor   string
		want    updateDecision
	}{
		{"no catalog", nil, "1.0.0", "", updateSkip},
		{"no version", &agentCatalog{}, "1.0.0", "", updateSkip},
		{"already at desired", cat("1.0.0", ""), "1.0.0", "", updateSkip},
		{"after-apply forward", cat("1.1.0", ""), "1.0.0", "", updateAfterApply},
		{"downgrade below running refused", cat("1.0.0", ""), "1.1.0", "", updateRefuse},
		{"downgrade below floor refused", cat("1.1.0", ""), "1.0.0", "1.2.0", updateRefuse},
		{"forced when below min", cat("1.2.0", "1.2.0"), "1.0.0", "", updateForced},
		{"forced target reaches min", cat("1.3.0", "1.2.0"), "1.0.0", "", updateForced},
		{"forced but target below min is misconfig", cat("1.1.0", "1.2.0"), "1.0.0", "", updateRefuse},
		{"legacy empty running updates", cat("1.0.0", ""), "", "", updateAfterApply},
		{"legacy empty running forced below min", cat("1.0.0", "1.0.0"), "", "", updateForced},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, reason := decideSelfUpdate(tc.cat, tc.running, tc.floor)
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
	if err := performSelfUpdate(cfg, cat, "1.0.0", "", io.Discard); err != nil {
		t.Fatalf("performSelfUpdate: %v", err)
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
	err := performSelfUpdate(cfg, cat, "1.0.0", "", io.Discard)
	if err == nil {
		t.Fatalf("expected a hash-mismatch refusal")
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
	if err := performSelfUpdate(cfg, cat, "1.0.0", "", io.Discard); err == nil {
		t.Fatalf("expected a self-test version-mismatch refusal")
	}
	if *execed != "" || func() bool { g, _ := os.ReadFile(self); return string(g) != "OLD" }() {
		t.Errorf("CUSTODY VIOLATION: swapped/exec'd a binary that failed its self-test")
	}
}

// TestReconcileSelfUpdate_Promote: booted as the target, a clean health gate promotes — floor
// advances, breadcrumb + .bak cleared, no re-exec.
func TestReconcileSelfUpdate_Promote(t *testing.T) {
	dir := t.TempDir()
	self := filepath.Join(dir, "yaog-agent")
	_ = os.WriteFile(self, []byte("NEW"), 0o755)
	_ = os.WriteFile(self+".bak", []byte("OLD"), 0o755)
	stateDir := filepath.Join(dir, "state")
	mustSave(t, stateDir, &State{NodeID: "n1", PendingUpdate: &PendingUpdate{From: "1.0.0", To: "1.1.0"}})

	execed, restore := stubSwap(t, self)
	defer restore()
	ReconcileSelfUpdate(stateDir, "1.1.0", func() error { return nil }, io.Discard)

	if *execed != "" {
		t.Errorf("promote must not re-exec")
	}
	st, _ := LoadState(stateDir)
	if st.PendingUpdate != nil {
		t.Errorf("breadcrumb not cleared after promote")
	}
	if st.AgentVersionFloor != "1.1.0" {
		t.Errorf("floor = %q, want 1.1.0 (advances only on health-confirmed promote)", st.AgentVersionFloor)
	}
	if _, e := os.Stat(self + ".bak"); e == nil {
		t.Errorf(".bak should be removed after promote")
	}
}

// TestReconcileSelfUpdate_Rollback: booted as the target but unhealthy → rollback to .bak + re-exec.
func TestReconcileSelfUpdate_Rollback(t *testing.T) {
	dir := t.TempDir()
	self := filepath.Join(dir, "yaog-agent")
	_ = os.WriteFile(self, []byte("NEW-BROKEN"), 0o755)
	_ = os.WriteFile(self+".bak", []byte("OLD-GOOD"), 0o755)
	stateDir := filepath.Join(dir, "state")
	mustSave(t, stateDir, &State{NodeID: "n1", PendingUpdate: &PendingUpdate{From: "1.0.0", To: "1.1.0"}})

	execed, restore := stubSwap(t, self)
	defer restore()
	ReconcileSelfUpdate(stateDir, "1.1.0", func() error { return fmt.Errorf("poll failed") }, io.Discard)

	if *execed != self {
		t.Errorf("rollback must re-exec the restored binary")
	}
	if got, _ := os.ReadFile(self); string(got) != "OLD-GOOD" {
		t.Errorf("binary not rolled back; content=%q", got)
	}
	st, _ := LoadState(stateDir)
	if st.PendingUpdate != nil {
		t.Errorf("breadcrumb not cleared after rollback")
	}
	if st.AgentVersionFloor == "1.1.0" {
		t.Errorf("floor must NOT advance on a rolled-back update")
	}
}

// TestReconcileSelfUpdate_AbandonAtCap: a target that never takes effect (boots keep coming up as
// `from`) is abandoned after the attempt cap — the breadcrumb is cleared so the loop ends.
func TestReconcileSelfUpdate_AbandonAtCap(t *testing.T) {
	dir := t.TempDir()
	self := filepath.Join(dir, "yaog-agent")
	_ = os.WriteFile(self, []byte("OLD"), 0o755) // still running `from` — swap never stuck
	stateDir := filepath.Join(dir, "state")
	mustSave(t, stateDir, &State{NodeID: "n1", PendingUpdate: &PendingUpdate{From: "1.0.0", To: "1.1.0"}})

	_, restore := stubSwap(t, self)
	defer restore()
	// Boot repeatedly as `from`; the health gate never runs (buildVersion != To). After the cap
	// the breadcrumb must be cleared (abandoned), ending the restart loop.
	cleared := false
	for i := 0; i < maxSelfUpdateAttempts+2; i++ {
		ReconcileSelfUpdate(stateDir, "1.0.0", func() error { return nil }, io.Discard)
		st, _ := LoadState(stateDir)
		if st.PendingUpdate == nil {
			cleared = true
			break
		}
	}
	if !cleared {
		t.Errorf("a never-applying update must be abandoned at the attempt cap, not loop forever")
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
