package agent

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
)

func TestSaveStateRoundTripAndPermissions(t *testing.T) {
	stateDir := filepath.Join(t.TempDir(), "nested", "state")
	want := &State{
		NodeID:             "alpha",
		LastCompiledAt:     "2026-07-15T01:02:03Z",
		LastChecksum:       "sha256:first",
		LastResult:         LastResultOK,
		LastSigned:         true,
		MembershipEpoch:    7,
		MembershipVerified: true,
		AppliedAt:          "2026-07-15T01:02:04Z",
		Health:             "applied",
	}

	if err := SaveState(stateDir, want); err != nil {
		t.Fatalf("SaveState: %v", err)
	}
	got, err := LoadState(stateDir)
	if err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("round trip mismatch:\n got: %#v\nwant: %#v", got, want)
	}

	dirInfo, err := os.Stat(stateDir)
	if err != nil {
		t.Fatalf("stat state dir: %v", err)
	}
	if gotMode := dirInfo.Mode().Perm(); gotMode != 0700 {
		t.Fatalf("state dir mode = %04o, want 0700", gotMode)
	}
	assertPrivateStateFile(t, stateDir)
	assertNoStateTemps(t, stateDir)

	// A replacement must not inherit an existing target's overly broad mode.
	if err := os.Chmod(statePath(stateDir), 0666); err != nil {
		t.Fatalf("widen existing state mode: %v", err)
	}
	want.LastChecksum = "sha256:replacement"
	want.MembershipEpoch++
	if err := SaveState(stateDir, want); err != nil {
		t.Fatalf("replace state: %v", err)
	}
	assertPrivateStateFile(t, stateDir)
	assertNoStateTemps(t, stateDir)

	got, err = LoadState(stateDir)
	if err != nil {
		t.Fatalf("LoadState replacement: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("replacement mismatch:\n got: %#v\nwant: %#v", got, want)
	}
}

func TestSaveStateConcurrentWritersRemainAtomic(t *testing.T) {
	stateDir := filepath.Join(t.TempDir(), "state")
	const writers = 16

	start := make(chan struct{})
	errs := make(chan error, writers)
	var wg sync.WaitGroup
	for i := 0; i < writers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			errs <- SaveState(stateDir, &State{
				NodeID:          fmt.Sprintf("node-%d", i),
				LastChecksum:    fmt.Sprintf("sha256:%d", i),
				LastResult:      LastResultOK,
				MembershipEpoch: int64(i),
			})
		}(i)
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent SaveState: %v", err)
		}
	}

	got, err := LoadState(stateDir)
	if err != nil {
		t.Fatalf("LoadState after concurrent saves: %v", err)
	}
	if !strings.HasPrefix(got.NodeID, "node-") || got.LastResult != LastResultOK {
		t.Fatalf("final state is not one complete writer result: %#v", got)
	}
	assertPrivateStateFile(t, stateDir)
	assertNoStateTemps(t, stateDir)
}

func TestStateLockRejectsConcurrentOperationAndReleasesCleanly(t *testing.T) {
	stateDir := t.TempDir()
	releaseFirst, err := acquireStateLock(stateDir)
	if err != nil {
		t.Fatalf("acquire first state lock: %v", err)
	}
	if releaseSecond, err := acquireStateLock(stateDir); err == nil {
		_ = releaseSecond()
		t.Fatal("second state lock acquired while the first was held")
	} else if !strings.Contains(err.Error(), "state directory is busy") {
		t.Fatalf("second state lock error = %v, want a clear busy error", err)
	}
	if err := releaseFirst(); err != nil {
		t.Fatalf("release first state lock: %v", err)
	}
	releaseSecond, err := acquireStateLock(stateDir)
	if err != nil {
		t.Fatalf("reacquire state lock after release: %v", err)
	}
	if err := releaseSecond(); err != nil {
		t.Fatalf("release second state lock: %v", err)
	}
}

func TestRunRefusesCompetingStateOwnerBeforeFetch(t *testing.T) {
	stateDir := t.TempDir()
	release, err := acquireStateLock(stateDir)
	if err != nil {
		t.Fatalf("acquire state owner: %v", err)
	}
	defer func() { _ = release() }()

	fetched := false
	_, err = Run(&Config{
		NodeID:   "alpha",
		Source:   fetchTrackingSource{fetched: &fetched},
		StateDir: stateDir,
	})
	if err == nil || !strings.Contains(err.Error(), "state directory is busy") {
		t.Fatalf("competing Run error = %v, want clear busy refusal", err)
	}
	if fetched {
		t.Fatal("competing Run fetched while another state owner held the lease")
	}
}

func assertPrivateStateFile(t *testing.T, stateDir string) {
	t.Helper()
	info, err := os.Stat(statePath(stateDir))
	if err != nil {
		t.Fatalf("stat state file: %v", err)
	}
	if gotMode := info.Mode().Perm(); gotMode != 0600 {
		t.Fatalf("state file mode = %04o, want 0600", gotMode)
	}
}

func assertNoStateTemps(t *testing.T, stateDir string) {
	t.Helper()
	entries, err := os.ReadDir(stateDir)
	if err != nil {
		t.Fatalf("read state dir: %v", err)
	}
	if len(entries) != 1 || entries[0].Name() != stateFileName {
		names := make([]string, 0, len(entries))
		for _, entry := range entries {
			names = append(names, entry.Name())
		}
		t.Fatalf("state dir contains unexpected files: %v", names)
	}
}
