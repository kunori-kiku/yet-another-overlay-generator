//go:build windows

package agent

import (
	"os"

	"golang.org/x/sys/windows"
)

func lockFilePlatform(f *os.File, nonBlocking bool) (func() error, error) {
	handle := windows.Handle(f.Fd())
	var overlapped windows.Overlapped
	flags := uint32(windows.LOCKFILE_EXCLUSIVE_LOCK)
	if nonBlocking {
		flags |= windows.LOCKFILE_FAIL_IMMEDIATELY
	}
	if err := windows.LockFileEx(handle, flags, 0, 1, 0, &overlapped); err != nil {
		return nil, err
	}
	return func() error {
		return windows.UnlockFileEx(handle, 0, 1, 0, &overlapped)
	}, nil
}
