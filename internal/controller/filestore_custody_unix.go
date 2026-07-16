//go:build !windows

package controller

import (
	"fmt"
	"os"
	"syscall"

	"golang.org/x/sys/unix"
)

func validateSecureStoreDirPlatform(info os.FileInfo) error {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return fmt.Errorf("cannot determine owner")
	}
	if stat.Uid != uint32(os.Geteuid()) {
		return fmt.Errorf("owned by uid %d, running as uid %d", stat.Uid, os.Geteuid())
	}
	if info.Mode().Perm()&0022 != 0 {
		return fmt.Errorf("group/world-writable mode %04o", info.Mode().Perm())
	}
	return nil
}

// tightenOwnedStoreRoot repairs the common upgrade case where a bind-mounted state root was
// pre-created as 0775 by the host umask. It acts only on a real directory owned by this process.
// Opening with O_NOFOLLOW and changing the descriptor prevents a path swap from redirecting chmod.
// Nested custody directories are never auto-repaired: a later permission change there remains
// evidence of local tampering and is rejected by the ordinary validation path.
func tightenOwnedStoreRoot(path string, info os.FileInfo) (bool, error) {
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return false, nil
	}
	// Never reinterpret a shared/specially managed directory as a dedicated controller root.
	// In particular, a root-run controller pointed at /tmp must not turn 01777 into 0700.
	// Set-ID bits likewise carry administrator intent that this compatibility repair must not erase.
	if info.Mode()&(os.ModeSticky|os.ModeSetgid|os.ModeSetuid) != 0 {
		return false, nil
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != uint32(os.Geteuid()) || info.Mode().Perm()&0022 == 0 {
		return false, nil
	}

	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return false, fmt.Errorf("open owned filestore root without following links: %w", err)
	}
	defer unix.Close(fd)

	var opened unix.Stat_t
	if err := unix.Fstat(fd, &opened); err != nil {
		return false, fmt.Errorf("inspect opened filestore root: %w", err)
	}
	if opened.Uid != uint32(os.Geteuid()) {
		return false, nil
	}
	// Repeat the special-bit check on the opened object so a path replacement between Lstat and
	// Open cannot redirect the repair onto a sticky or set-ID directory.
	if uint64(opened.Mode)&uint64(unix.S_ISUID|unix.S_ISGID|unix.S_ISVTX) != 0 {
		return false, nil
	}
	if os.FileMode(opened.Mode).Perm()&0022 == 0 {
		return false, nil
	}
	if err := unix.Fchmod(fd, 0700); err != nil {
		return false, fmt.Errorf("tighten owned filestore root from mode %04o to 0700: %w", info.Mode().Perm(), err)
	}
	return true, nil
}

func validateStoreFilePlatform(info os.FileInfo) error {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return fmt.Errorf("cannot determine owner")
	}
	if stat.Uid != uint32(os.Geteuid()) {
		return fmt.Errorf("owned by uid %d, running as uid %d", stat.Uid, os.Geteuid())
	}
	return nil
}

// A regular file ignores O_NONBLOCK, while a pathname swapped to a FIFO between
// Lstat and open returns promptly for validation instead of blocking a store
// operation indefinitely. os.Root supplies O_NOFOLLOW/openat confinement.
func storeFileOpenFlags(flag int) int { return flag | unix.O_NONBLOCK }
