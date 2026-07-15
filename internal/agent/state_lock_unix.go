//go:build !windows

package agent

import (
	"os"

	"golang.org/x/sys/unix"
)

func lockFilePlatform(f *os.File, nonBlocking bool) (func() error, error) {
	operation := unix.LOCK_EX
	if nonBlocking {
		operation |= unix.LOCK_NB
	}
	if err := unix.Flock(int(f.Fd()), operation); err != nil {
		return nil, err
	}
	return func() error {
		return unix.Flock(int(f.Fd()), unix.LOCK_UN)
	}, nil
}
