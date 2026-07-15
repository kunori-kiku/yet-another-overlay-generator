//go:build !windows

package agent

import (
	"fmt"
	"os"
	"syscall"
)

func validateSecureDirPlatform(path string, info os.FileInfo) error {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return fmt.Errorf("cannot determine directory owner")
	}
	if stat.Uid != uint32(os.Geteuid()) {
		return fmt.Errorf("owned by uid %d, running as uid %d", stat.Uid, os.Geteuid())
	}
	if info.Mode().Perm()&0022 != 0 {
		return fmt.Errorf("group/world-writable mode %04o", info.Mode().Perm())
	}
	return nil
}

func validateFileOwnerPlatform(info os.FileInfo) error {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return fmt.Errorf("cannot determine file owner")
	}
	if stat.Uid != uint32(os.Geteuid()) {
		return fmt.Errorf("owned by uid %d, running as uid %d", stat.Uid, os.Geteuid())
	}
	return nil
}
