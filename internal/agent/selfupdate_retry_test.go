package agent

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// armedArtifacts builds a verified-bundle file map carrying an agent self-update catalog (the input
// RetryDeferredSelfUpdate parses). releaseURL is the catalog's release_url (the binary lives at
// releaseURL/<asset>); version + sha pin the per-arch binary.
func armedArtifacts(releaseURL, version, sha string) map[string][]byte {
	aj := fmt.Sprintf(`{"schema":1,"agent":{"version":%q,"release_url":%q,"bins":{"linux-%s":{"asset":%q,"sha256":%q}}}}`,
		version, releaseURL, runtime.GOARCH, "yaog-agent-linux-"+runtime.GOARCH, sha)
	return map[string][]byte{"artifacts.json": []byte(aj)}
}

// countingFetch returns a verifiedFetch stub that yields (files, err) and counts its invocations, so a
// test can assert the EXPENSIVE fetch is skipped when nothing is armed.
func countingFetch(files map[string][]byte, err error, calls *int) func() (map[string][]byte, error) {
	return func() (map[string][]byte, error) { *calls++; return files, err }
}

// TestRetryDeferredSelfUpdate_NoopWhenNilParams: self-update disabled (p==nil) ⇒ no-op, no fetch.
func TestRetryDeferredSelfUpdate_NoopWhenNilParams(t *testing.T) {
	calls := 0
	attempted, err := RetryDeferredSelfUpdate(nil, "n1", t.TempDir(), countingFetch(nil, nil, &calls), io.Discard)
	if attempted || err != nil || calls != 0 {
		t.Errorf("nil params must be a no-op without fetching; attempted=%v err=%v calls=%d", attempted, err, calls)
	}
}

// TestRetryDeferredSelfUpdate_NoopWhenNotBlocked: nothing armed (SelfUpdateBlocked=="") ⇒ no fetch.
func TestRetryDeferredSelfUpdate_NoopWhenNotBlocked(t *testing.T) {
	dir := t.TempDir()
	mustSave(t, dir, &State{NodeID: "n1"}) // no SelfUpdateBlocked
	calls := 0
	p := &SelfUpdateParams{RunningVersion: "1.0.0"}
	attempted, err := RetryDeferredSelfUpdate(p, "n1", dir, countingFetch(nil, nil, &calls), io.Discard)
	if attempted || err != nil || calls != 0 {
		t.Errorf("an un-armed node must not fetch; attempted=%v err=%v calls=%d", attempted, err, calls)
	}
}

// TestRetryDeferredSelfUpdate_NoopWhenInFlight: an in-flight swap (PendingUpdate set) is owned by the
// boot reconcile, not the retry — even with a Blocked latch present, do not fetch or re-swap.
func TestRetryDeferredSelfUpdate_NoopWhenInFlight(t *testing.T) {
	dir := t.TempDir()
	mustSave(t, dir, &State{NodeID: "n1", SelfUpdateBlocked: "stale", PendingUpdate: &PendingUpdate{From: "1.0.0", To: "1.1.0"}})
	calls := 0
	p := &SelfUpdateParams{RunningVersion: "1.0.0"}
	attempted, err := RetryDeferredSelfUpdate(p, "n1", dir, countingFetch(nil, nil, &calls), io.Discard)
	if attempted || err != nil || calls != 0 {
		t.Errorf("an in-flight swap must block the retry without fetching; attempted=%v err=%v calls=%d", attempted, err, calls)
	}
}

// An unresolved root-side configuration mutation owns recovery before an unrelated
// deferred binary swap. Even an old Blocked latch must not fetch or re-exec until the
// main apply cycle has converged and cleared PendingApply.
func TestRetryDeferredSelfUpdate_NoopWhileApplyPending(t *testing.T) {
	dir := t.TempDir()
	mustSave(t, dir, &State{
		NodeID:            "n1",
		SelfUpdateBlocked: "stale",
		PendingApply: &PendingApply{
			CompiledAt:      "2026-07-16T06:00:00Z",
			BundleSHA256:    "0000000000000000000000000000000000000000000000000000000000000000",
			Action:          LastActionApply,
			MembershipEpoch: 7,
			StartedAt:       "2026-07-16T06:00:01Z",
		},
	})
	calls := 0
	p := &SelfUpdateParams{RunningVersion: "1.0.0"}
	attempted, err := RetryDeferredSelfUpdate(p, "n1", dir, countingFetch(nil, nil, &calls), io.Discard)
	if attempted || err != nil || calls != 0 {
		t.Errorf("a pending apply must block deferred self-update without fetching; attempted=%v err=%v calls=%d", attempted, err, calls)
	}
}

// TestRetryDeferredSelfUpdate_FetchErrorKeepsBlocked: a fetch/verify failure keeps last-good — the
// Blocked latch is preserved (retry next tick), not cleared.
func TestRetryDeferredSelfUpdate_FetchErrorKeepsBlocked(t *testing.T) {
	dir := t.TempDir()
	mustSave(t, dir, &State{NodeID: "n1", SelfUpdateBlocked: "could not download…"})
	p := &SelfUpdateParams{RunningVersion: "1.0.0"}
	attempted, err := RetryDeferredSelfUpdate(p, "n1", dir, func() (map[string][]byte, error) {
		return nil, fmt.Errorf("fetch: connection refused")
	}, io.Discard)
	if attempted || err == nil {
		t.Errorf("a fetch error must surface (attempted=false, err!=nil); got attempted=%v err=%v", attempted, err)
	}
	if st, _ := LoadState(dir); st.SelfUpdateBlocked == "" {
		t.Errorf("a fetch error must NOT clear the Blocked latch")
	}
}

// TestRetryDeferredSelfUpdate_ClearsBlockedOnSkip: the catalog now resolves to the running version
// (the update already took effect on a prior re-exec) ⇒ updateSkip ⇒ clear the stale latch, no swap.
func TestRetryDeferredSelfUpdate_ClearsBlockedOnSkip(t *testing.T) {
	dir := t.TempDir()
	mustSave(t, dir, &State{NodeID: "n1", SelfUpdateBlocked: "stale-from-before-the-update"})
	p := &SelfUpdateParams{RunningVersion: "1.2.0"}
	files := armedArtifacts("http://unused/dl", "1.2.0", "deadbeef") // version == running ⇒ updateSkip
	attempted, err := RetryDeferredSelfUpdate(p, "n1", dir, func() (map[string][]byte, error) { return files, nil }, io.Discard)
	if attempted || err != nil {
		t.Errorf("an already-at-desired catalog must not attempt a swap; attempted=%v err=%v", attempted, err)
	}
	if st, _ := LoadState(dir); st.SelfUpdateBlocked != "" {
		t.Errorf("updateSkip must CLEAR the stale Blocked latch; got %q", st.SelfUpdateBlocked)
	}
}

// TestRetryDeferredSelfUpdate_AttemptsAndSwapsWhenArmed: armed (desired > running, valid pin) ⇒ the
// retry downloads, verifies, swaps, and re-execs — WITHOUT a new generation.
func TestRetryDeferredSelfUpdate_AttemptsAndSwapsWhenArmed(t *testing.T) {
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
	mustSave(t, stateDir, &State{NodeID: "n1", SelfUpdateBlocked: "could not download the update binary…"})
	execed, restore := stubSwap(t, self)
	defer restore()

	p := &SelfUpdateParams{RunningVersion: "1.0.0"}
	files := armedArtifacts(srv.URL+"/dl", "1.2.0", sha)
	attempted, err := RetryDeferredSelfUpdate(p, "n1", stateDir, func() (map[string][]byte, error) { return files, nil }, io.Discard)
	if !attempted || err != nil {
		t.Fatalf("an armed retry must attempt+succeed; attempted=%v err=%v", attempted, err)
	}
	if *execed != self {
		t.Errorf("a successful retry must re-exec the swapped binary; execed=%q", *execed)
	}
	if got, _ := os.ReadFile(self); string(got) != string(bin) {
		t.Errorf("the new binary must be swapped in; on-disk=%q", got)
	}
}

// TestRetryDeferredSelfUpdate_ReRecordsBlockedOnDownloadFail: armed but the download still fails (the
// source is down) ⇒ no swap, and the curated Blocked reason is refreshed so the next tick retries.
func TestRetryDeferredSelfUpdate_ReRecordsBlockedOnDownloadFail(t *testing.T) {
	if runtime.GOARCH != "amd64" && runtime.GOARCH != "arm64" {
		t.Skipf("arch %s", runtime.GOARCH)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer srv.Close()

	dir := t.TempDir()
	self := filepath.Join(dir, "yaog-agent")
	_ = os.WriteFile(self, []byte("OLD"), 0o755)
	stateDir := filepath.Join(dir, "state")
	mustSave(t, stateDir, &State{NodeID: "n1", SelfUpdateBlocked: "an older reason"})
	execed, restore := stubSwap(t, self)
	defer restore()

	p := &SelfUpdateParams{RunningVersion: "1.0.0"}
	files := armedArtifacts(srv.URL+"/dl", "1.2.0", "deadbeef")
	attempted, err := RetryDeferredSelfUpdate(p, "n1", stateDir, func() (map[string][]byte, error) { return files, nil }, io.Discard)
	if !attempted || err == nil {
		t.Fatalf("a failed download must report (attempted=true, err!=nil); got attempted=%v err=%v", attempted, err)
	}
	if *execed != "" {
		t.Errorf("a failed download must NOT swap/re-exec; execed=%q", *execed)
	}
	st, _ := LoadState(stateDir)
	if st.SelfUpdateBlocked == "" {
		t.Errorf("a failed download must refresh the Blocked latch (not clear it)")
	}
}
