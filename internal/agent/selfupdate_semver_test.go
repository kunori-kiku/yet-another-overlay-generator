package agent

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestReconcileSelfUpdate_VlessTargetSemverMatch proves the comparator-wedge fix: a v-LESS operator
// target ("2.0.0") reconciled against a v-PREFIXED released BuildVersion ("v2.0.0") must be recognized
// as the SAME release (SemVer compare), not treated as "swap never applied" (the old exact string
// compare) — which wedged the whole update channel (the floor never advanced, the in-flight guard then
// blocked every future update). EVERY other selfupdate test uses matching version strings, so these
// v-less cases are non-vacuous. It also asserts the fix RESTORES the AgentVersionFloor anti-downgrade
// custody (a security-relevant floor advance, not a cosmetic one).
func TestReconcileSelfUpdate_VlessTargetSemverMatch(t *testing.T) {
	t.Run("healthy v-less target: probation then finalize advances the floor, no false abandon", func(t *testing.T) {
		dir := t.TempDir()
		stateDir := filepath.Join(dir, "state")
		mustSave(t, stateDir, &State{NodeID: "n1", AgentVersionFloor: "1.0.0", PendingUpdate: &PendingUpdate{From: "1.0.0", To: "2.0.0"}})

		// buildVersion carries the released "v" prefix; the operator target omitted it.
		ReconcileSelfUpdatePromote(stateDir, "v2.0.0", func() error { return nil }, io.Discard)
		st, _ := LoadState(stateDir)
		if st.PendingUpdate == nil || !st.PendingUpdate.Confirmed {
			t.Fatalf("v-less target must be recognized as the running build and health-confirmed (probation); got %+v", st.PendingUpdate)
		}
		if st.AgentVersionFloor != "1.0.0" {
			t.Errorf("floor must NOT advance until finalize; got %q", st.AgentVersionFloor)
		}

		FinalizeSelfUpdate(stateDir, "v2.0.0", io.Discard)
		st, _ = LoadState(stateDir)
		if st.PendingUpdate != nil {
			t.Errorf("breadcrumb must clear after finalize")
		}
		if st.AgentVersionFloor != "2.0.0" {
			t.Errorf("finalize must advance the floor to the target (anti-downgrade custody); got %q", st.AgentVersionFloor)
		}
		if st.AbandonedAgentVersion != "" {
			t.Errorf("a healthy update must NOT write abandoned-target bookkeeping; got %q", st.AbandonedAgentVersion)
		}
	})

	t.Run("unhealthy v-less target: rolls back + abandons (not silently wedged)", func(t *testing.T) {
		dir := t.TempDir()
		self := filepath.Join(dir, "yaog-agent")
		_ = os.WriteFile(self, []byte("NEW-BROKEN"), 0o755)
		_ = os.WriteFile(self+".bak", []byte("OLD-GOOD"), 0o755)
		stateDir := filepath.Join(dir, "state")
		mustSave(t, stateDir, &State{NodeID: "n1", AgentVersionFloor: "1.0.0", PendingUpdate: &PendingUpdate{From: "1.0.0", To: "2.0.0"}})

		execed, restore := stubSwap(t, self)
		defer restore()
		ReconcileSelfUpdatePromote(stateDir, "v2.0.0", func() error { return fmt.Errorf("poll failed") }, io.Discard)

		// Before the fix: buildVersion "v2.0.0" != pu.To "2.0.0" → the "swap never applied" branch → NO
		// health gate, NO rollback (breadcrumb persists, channel wedged). After: it rolls back.
		if *execed != self {
			t.Errorf("v-less unhealthy target must roll back + re-exec the restored binary")
		}
		if got, _ := os.ReadFile(self); string(got) != "OLD-GOOD" {
			t.Errorf("binary not rolled back; content=%q", got)
		}
		st, _ := LoadState(stateDir)
		if st.PendingUpdate != nil {
			t.Errorf("breadcrumb must clear after rollback")
		}
		if st.AbandonedAgentVersion != "2.0.0" {
			t.Errorf("rollback must remember the abandoned (v-less) target to prevent re-arm; got %q", st.AbandonedAgentVersion)
		}
	})
}

// TestReconcileSelfUpdateEarly_SurfacesUnpersistableAttempt proves the brick-bound hardening: even
// with its stable lock inode available, a read-only state directory cannot persist the attempt bump,
// so startup gets a returned error plus the targeted diagnostic instead of silently continuing.
func TestReconcileSelfUpdateEarly_SurfacesUnpersistableAttempt(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root bypasses the read-only dir permission")
	}
	dir := t.TempDir()
	stateDir := filepath.Join(dir, "state")
	mustSave(t, stateDir, &State{NodeID: "n1", PendingUpdate: &PendingUpdate{From: "1.0.0", To: "1.1.0"}})
	// Seed the stable lock inode before making the directory read-only. Production
	// already has this file because the update was armed under the same state lease.
	release, err := acquireStateLock(stateDir)
	if err != nil {
		t.Fatalf("seed state lock: %v", err)
	}
	if err := release(); err != nil {
		t.Fatalf("release seeded state lock: %v", err)
	}
	// SaveState writes atomically (temp file + rename), so a read-only dir makes it fail.
	if err := os.Chmod(stateDir, 0o500); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chmod(stateDir, 0o700) }()

	var log bytes.Buffer
	if err := ReconcileSelfUpdateEarly(stateDir, "1.0.0", &log); err == nil {
		t.Fatal("read-only state directory unexpectedly persisted its attempt bump")
	}
	if !strings.Contains(log.String(), "could not persist self-update attempt") {
		t.Errorf("failed durable attempt must be diagnosed before startup refuses; log=%q", log.String())
	}
}
