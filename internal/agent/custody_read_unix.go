//go:build !windows

package agent

import (
	"fmt"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

// readCustodyFilePlatform opens the direct parent as a descriptor and then opens
// the basename relative to it. O_NOFOLLOW on both opens closes the path-swap and
// final-symlink races; fstat validation ensures the bytes come from the object we
// actually approved. O_NONBLOCK prevents a substituted FIFO/device from hanging
// the agent before its type can be rejected.
func readCustodyFilePlatform(path string, policy custodyReadPolicy) ([]byte, error) {
	cleanPath := filepath.Clean(path)
	dirPath := filepath.Dir(cleanPath)
	base := filepath.Base(cleanPath)

	dirFD, err := unix.Open(dirPath, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_DIRECTORY|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, fmt.Errorf("agent: %w", &os.PathError{Op: "open custody directory", Path: dirPath, Err: err})
	}
	dir := os.NewFile(uintptr(dirFD), dirPath)
	if dir == nil {
		_ = unix.Close(dirFD)
		return nil, fmt.Errorf("agent: open custody directory %s: invalid descriptor", dirPath)
	}
	defer func() { _ = dir.Close() }()

	dirInfo, err := dir.Stat()
	if err != nil {
		return nil, fmt.Errorf("agent: inspect opened custody directory %s: %w", dirPath, err)
	}
	if err := validateSecureOwnedDir(dirPath, dirInfo); err != nil {
		return nil, fmt.Errorf("agent: custody directory rejected: %w", err)
	}

	fd, err := unix.Openat(dirFD, base, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
	if err != nil {
		return nil, fmt.Errorf("agent: %w", &os.PathError{Op: "open custody file", Path: cleanPath, Err: err})
	}
	f := os.NewFile(uintptr(fd), cleanPath)
	if f == nil {
		_ = unix.Close(fd)
		return nil, fmt.Errorf("agent: open custody file %s: invalid descriptor", cleanPath)
	}
	defer func() { _ = f.Close() }()

	info, err := f.Stat()
	if err != nil {
		return nil, fmt.Errorf("agent: inspect opened custody file %s: %w", cleanPath, err)
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("agent: custody file %s must be a regular file, not a link or special file", cleanPath)
	}
	if err := validateFileOwnerPlatform(info); err != nil {
		return nil, fmt.Errorf("agent: custody file %s is unsafe: %w", cleanPath, err)
	}
	perm := info.Mode().Perm()
	if policy == custodyReadSecret {
		if perm&0o077 != 0 {
			return nil, fmt.Errorf("agent: custody secret %s has group/world-accessible mode %04o (want no group/world permissions)", cleanPath, perm)
		}
	} else if perm&0o022 != 0 {
		return nil, fmt.Errorf("agent: protected custody file %s has group/world-writable mode %04o", cleanPath, perm)
	}

	return readCustodyBytes(f, cleanPath)
}
