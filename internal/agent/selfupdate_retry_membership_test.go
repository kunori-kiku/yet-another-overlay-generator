package agent

import (
	"crypto/ed25519"
	"crypto/rand"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/kunorikiku/yet-another-overlay-generator/internal/bundlesig"
)

// operatorCredPEM builds a valid ed25519 operator-credential public-key PEM (the shape a keystone-ON
// node pins). The private half is discarded: these tests only need a NON-EMPTY, well-formed credential
// so VerifyMembership runs its fail-closed path — a bundle without a signed trustlist is refused.
func operatorCredPEM(t *testing.T) []byte {
	t.Helper()
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate ed25519 key: %v", err)
	}
	return bundlesig.MarshalPublicKeyPEM(pub)
}

// TestWithMembershipGate_KeystoneOnRefusesUnverifiedBundle: with an operator credential pinned
// (keystone ON), a fetched bundle carrying no signed trustlist FAILS VerifyMembership, so the gate
// returns an error and NO files — a caller that swaps on the returned files fails closed. This is the
// unit that closes the beta.15 keystone-bypass on the deferred-retry path.
func TestWithMembershipGate_KeystoneOnRefusesUnverifiedBundle(t *testing.T) {
	dir := t.TempDir()
	// artifacts.json only: VerifyBundle-shaped, but no trustlist.json/.sig ⇒ membership fails.
	raw := func() (map[string][]byte, error) {
		return armedArtifacts("https://example/v9.9.9", "9.9.9", "deadbeef"), nil
	}
	cfg := MembershipConfig{NodeID: "n1", OperatorCredPEM: operatorCredPEM(t), OperatorCredAlg: "ed25519"}
	files, err := WithMembershipGate(raw, cfg, dir)()
	if err == nil {
		t.Fatal("keystone-ON must refuse a bundle with no signed membership")
	}
	if files != nil {
		t.Fatalf("a membership failure must return NO files (fail-closed); got %d files", len(files))
	}
}

// TestWithMembershipGate_KeystoneOffPassesThrough: no operator credential (keystone OFF) ⇒ the gate is
// a no-op and returns the fetched files verbatim (dev / air-gap parity with the old verifiedFetch).
func TestWithMembershipGate_KeystoneOffPassesThrough(t *testing.T) {
	dir := t.TempDir()
	want := armedArtifacts("https://example/v9.9.9", "9.9.9", "deadbeef")
	files, err := WithMembershipGate(func() (map[string][]byte, error) { return want, nil }, MembershipConfig{NodeID: "n1"}, dir)()
	if err != nil {
		t.Fatalf("keystone-OFF gate must be a no-op: %v", err)
	}
	if len(files) != len(want) {
		t.Fatalf("keystone-OFF gate must pass the files through; got %d want %d", len(files), len(want))
	}
}

// TestWithMembershipGate_PropagatesFetchError: an underlying fetch error is returned unchanged, and no
// membership work is attempted.
func TestWithMembershipGate_PropagatesFetchError(t *testing.T) {
	dir := t.TempDir()
	sentinel := io.ErrUnexpectedEOF
	_, err := WithMembershipGate(func() (map[string][]byte, error) { return nil, sentinel },
		MembershipConfig{NodeID: "n1", OperatorCredPEM: operatorCredPEM(t)}, dir)()
	if err != sentinel {
		t.Fatalf("fetch error must propagate unchanged; got %v", err)
	}
}

// A verified fetch can take long enough for local state I/O to fail afterward. The membership
// wrapper must not silently reset the effective epoch to zero and return swap-authorizing files.
func TestWithMembershipGate_StateReloadFailureIsFailClosed(t *testing.T) {
	dir := t.TempDir()
	if err := SaveState(dir, &State{
		NodeID:          "n1",
		MembershipEpoch: 7,
		PendingApply: &PendingApply{
			CompiledAt:      "2026-07-16T04:00:00Z",
			BundleSHA256:    strings.Repeat("a", 64),
			Action:          LastActionApply,
			MembershipEpoch: 8,
			StartedAt:       "2026-07-16T04:01:00Z",
		},
	}); err != nil {
		t.Fatal(err)
	}
	corrupt := []byte("{ custody failed after verified fetch")
	raw := func() (map[string][]byte, error) {
		if err := os.WriteFile(statePath(dir), corrupt, 0o600); err != nil {
			t.Fatal(err)
		}
		return armedArtifacts("https://example/v9.9.9", "9.9.9", "deadbeef"), nil
	}
	cfg := MembershipConfig{NodeID: "n1", OperatorCredPEM: operatorCredPEM(t), OperatorCredAlg: "ed25519"}
	files, err := WithMembershipGate(raw, cfg, dir)()
	if err == nil || !strings.Contains(err.Error(), "load membership anti-rollback state") {
		t.Fatalf("state reload failure = %v, want fail-closed custody error", err)
	}
	if files != nil {
		t.Fatalf("state reload failure returned %d swap-authorizing files", len(files))
	}
	if got, readErr := os.ReadFile(statePath(dir)); readErr != nil || string(got) != string(corrupt) {
		t.Fatalf("membership reload failure rewrote custody state: %q, %v", got, readErr)
	}
}

// TestRetryDeferredSelfUpdate_KeystoneOnRefusesSwapOnMembershipFailure is the end-to-end regression for
// the beta.15 keystone-bypass: with a self-update deferral armed (SelfUpdateBlocked set) and keystone
// ON, the deferred retry — driven through the membership-verifying fetch the daemon now passes — must
// NOT attempt a binary swap when the served bundle fails membership; the Blocked latch persists so the
// next tick retries (fail-closed). Contrast TestRetryDeferredSelfUpdate_AttemptsAndSwapsWhenArmed,
// which swaps under keystone OFF.
func TestRetryDeferredSelfUpdate_KeystoneOnRefusesSwapOnMembershipFailure(t *testing.T) {
	dir := t.TempDir()
	mustSave(t, dir, &State{NodeID: "n1", SelfUpdateBlocked: "could not download the update binary"})
	calls := 0
	cfg := MembershipConfig{NodeID: "n1", OperatorCredPEM: operatorCredPEM(t), OperatorCredAlg: "ed25519"}
	gated := WithMembershipGate(countingFetch(armedArtifacts("https://example/v9.9.9", "9.9.9", "deadbeef"), nil, &calls), cfg, dir)
	p := &SelfUpdateParams{RunningVersion: "1.0.0"}

	attempted, err := RetryDeferredSelfUpdate(p, "n1", dir, gated, io.Discard)
	if attempted {
		t.Fatal("keystone-ON node must NOT attempt a swap when membership verification fails")
	}
	if err == nil {
		t.Fatal("expected a membership verification error to surface")
	}
	if calls != 1 {
		t.Fatalf("the gated fetch should run exactly once; got %d", calls)
	}
	if st, _ := LoadState(dir); st.SelfUpdateBlocked == "" {
		t.Fatal("the Blocked latch must persist on a membership failure (fail-closed; retry next tick)")
	}
}
