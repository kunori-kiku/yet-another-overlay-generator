package agent

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const stateLockFileName = ".run.lock"

// stateLease owns the open file description carrying the state directory's
// advisory lock. Keeping the descriptor available (rather than returning only
// an unlock closure) lets the Unix install guardian inherit the same kernel
// lease while install.sh is running. If the Go parent is killed, the lease then
// remains held until the guardian observes the installer exit.
type stateLease struct {
	file           *os.File
	unlockPlatform func() error
}

func (l *stateLease) release() error {
	if l == nil || l.file == nil {
		return nil
	}
	unlockErr := l.unlockPlatform()
	closeErr := l.file.Close()
	l.file = nil
	return errors.Join(unlockErr, closeErr)
}

// acquireStateLock takes an OS-backed exclusive advisory lock for one state directory. The
// returned release function unlocks and closes the descriptor. Keeping a stable lock inode is
// deliberate: lock ownership belongs to open descriptors, so a process crash releases it after any
// in-flight install guardian exits and never leaves a stale PID/timestamp sentinel that could wedge
// the agent permanently.
func acquireStateLock(stateDir string) (func() error, error) {
	lease, err := acquireStateLease(stateDir)
	if err != nil {
		return nil, err
	}
	return lease.release, nil
}

// acquireStateLease is the ownership-preserving form used by Run and
// RunControllerCycle. Callers that do not execute install.sh use the smaller
// acquireStateLock compatibility wrapper above.
func acquireStateLease(stateDir string) (*stateLease, error) {
	if strings.TrimSpace(stateDir) == "" {
		return nil, fmt.Errorf("agent: state dir must not be empty")
	}
	if err := EnsureSecureOwnedDir(stateDir); err != nil {
		return nil, fmt.Errorf("agent: secure state dir for run lock: %w", err)
	}
	return acquireExclusiveFileLease(filepath.Join(stateDir, stateLockFileName), "state directory", true)
}

func acquireExclusiveFileLease(lockPath, resource string, nonBlocking bool) (*stateLease, error) {
	if info, err := os.Lstat(lockPath); err == nil {
		if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
			return nil, fmt.Errorf("agent: %s lock path %s is not a regular file", resource, lockPath)
		}
	} else if !os.IsNotExist(err) {
		return nil, fmt.Errorf("agent: inspect %s lock: %w", resource, err)
	}
	f, err := os.OpenFile(lockPath, os.O_RDWR|os.O_CREATE, 0600)
	if err != nil {
		return nil, fmt.Errorf("agent: open %s lock: %w", resource, err)
	}
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		_ = f.Close()
		if err != nil {
			return nil, fmt.Errorf("agent: inspect opened %s lock: %w", resource, err)
		}
		return nil, fmt.Errorf("agent: opened %s lock is not a regular file", resource)
	}
	if err := f.Chmod(0600); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("agent: protect %s lock: %w", resource, err)
	}
	unlockPlatform, err := lockFilePlatform(f, nonBlocking)
	if err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("agent: %s is busy (another operation may be active): %w", resource, err)
	}
	return &stateLease{file: f, unlockPlatform: unlockPlatform}, nil
}

// acquireExclusiveFileLock keeps the release-only interface used by key
// generation, which never needs to transfer its lease to a subprocess.
func acquireExclusiveFileLock(lockPath, resource string, nonBlocking bool) (func() error, error) {
	lease, err := acquireExclusiveFileLease(lockPath, resource, nonBlocking)
	if err != nil {
		return nil, err
	}
	return lease.release, nil
}

// WithStateLock runs one top-level state/host custody operation under the same non-blocking
// cross-process lease used by Run. It is for controller-mode rekey and self-update boundaries that
// mutate the state directory outside Run; nested callers must use their already-held path instead.
func WithStateLock(stateDir string, fn func() error) error {
	release, err := acquireStateLock(stateDir)
	if err != nil {
		return err
	}
	defer func() { _ = release() }()
	return fn()
}
