package agent

import (
	"fmt"
	"os"
	"strings"
)

// EnsureSecureOwnedDir creates dir at 0700 when absent, then rejects a symlink,
// non-directory, ownership by another user, or group/world-writable permissions.
// Unsafe pre-existing custody directories are rejected rather than legitimized.
func EnsureSecureOwnedDir(dir string) error {
	if strings.TrimSpace(dir) == "" {
		return fmt.Errorf("directory must not be empty")
	}
	if info, err := os.Lstat(dir); err == nil {
		return validateSecureOwnedDir(dir, info)
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("inspect directory %s: %w", dir, err)
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf("create directory %s: %w", dir, err)
	}
	info, err := os.Lstat(dir)
	if err != nil {
		return fmt.Errorf("inspect created directory %s: %w", dir, err)
	}
	return validateSecureOwnedDir(dir, info)
}

func validateSecureOwnedDir(dir string, info os.FileInfo) error {
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return fmt.Errorf("directory %s must be a real directory, not a symlink or special file", dir)
	}
	if err := validateSecureDirPlatform(dir, info); err != nil {
		return fmt.Errorf("directory %s is unsafe: %w", dir, err)
	}
	return nil
}

func validateOwnedRegularFile(path string, info os.FileInfo) error {
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return fmt.Errorf("file %s must be a regular file, not a symlink or special file", path)
	}
	if err := validateFileOwnerPlatform(info); err != nil {
		return fmt.Errorf("file %s is unsafe: %w", path, err)
	}
	return nil
}
