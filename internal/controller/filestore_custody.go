package controller

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ensureSecureStoreRoot creates a configured FileStore root when it is absent,
// then validates the resulting path. MkdirAll is intentionally limited to the
// configured root: children below that custody boundary are created one path
// component at a time by ensureSecureStoreChild.
func ensureSecureStoreRoot(root string) (string, error) {
	if strings.TrimSpace(root) == "" {
		return "", fmt.Errorf("controller: filestore root must not be empty")
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return "", fmt.Errorf("controller: resolve filestore root: %w", err)
	}
	if info, err := os.Lstat(abs); err == nil {
		if err := validateSecureStoreDir(abs, info); err != nil {
			return "", fmt.Errorf("controller: unsafe filestore root: %w", err)
		}
		return abs, nil
	} else if !os.IsNotExist(err) {
		return "", fmt.Errorf("controller: inspect filestore root: %w", err)
	}
	if err := os.MkdirAll(abs, 0700); err != nil {
		return "", fmt.Errorf("controller: create filestore root: %w", err)
	}
	// Do not assume MkdirAll created the object we requested: another local
	// actor may have won the race, or a path may have been replaced meanwhile.
	if err := revalidateSecureStoreDir(abs); err != nil {
		return "", fmt.Errorf("controller: unsafe created filestore root: %w", err)
	}
	return abs, nil
}

// ensureSecureStoreChild creates one directory immediately below an already
// trusted parent. Both paths are checked before and after creation so a
// pre-existing symlink or permissive custody directory is never legitimized.
func ensureSecureStoreChild(parent, name string) (string, error) {
	if _, err := sanitizeComponent("store directory", name); err != nil {
		return "", err
	}
	if err := revalidateSecureStoreDir(parent); err != nil {
		return "", err
	}
	child := filepath.Join(parent, name)
	if info, err := os.Lstat(child); err == nil {
		if err := validateSecureStoreDir(child, info); err != nil {
			return "", err
		}
	} else if !os.IsNotExist(err) {
		return "", fmt.Errorf("controller: inspect custody directory %s: %w", child, err)
	} else if err := os.Mkdir(child, 0700); err != nil {
		// A concurrent creator is acceptable only if the object it installed
		// passes the same validation below.
		if !os.IsExist(err) {
			return "", fmt.Errorf("controller: create custody directory %s: %w", child, err)
		}
	}
	if err := revalidateSecureStoreDir(parent); err != nil {
		return "", err
	}
	if err := revalidateSecureStoreDir(child); err != nil {
		return "", err
	}
	return child, nil
}

// revalidateSecureStoreDir rejects a missing path as well as an unsafe one.
func revalidateSecureStoreDir(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("controller: inspect custody directory %s: %w", path, err)
	}
	return validateSecureStoreDir(path, info)
}

// validateSecureStoreDirIfExists is used by read/delete paths, where an absent
// tenant or collection still has the ordinary "not found" meaning.
func validateSecureStoreDirIfExists(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("controller: inspect custody directory %s: %w", path, err)
	}
	return validateSecureStoreDir(path, info)
}

func validateSecureStoreDir(path string, info os.FileInfo) error {
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return fmt.Errorf("controller: custody path %s must be a real directory, not a symlink or special file", path)
	}
	if err := validateSecureStoreDirPlatform(info); err != nil {
		return fmt.Errorf("controller: custody directory %s is unsafe: %w", path, err)
	}
	return nil
}

// validateOpenedStoreTemp validates the object behind an already-opened temp
// descriptor. This complements CreateTemp's O_EXCL creation: the store never
// trusts a pathname-only check for the bytes it is about to publish.
func validateOpenedStoreTemp(path string, f *os.File) error {
	info, err := f.Stat()
	if err != nil {
		return fmt.Errorf("controller: inspect temporary file %s: %w", path, err)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("controller: temporary file %s must be regular", path)
	}
	if err := validateStoreFilePlatform(info); err != nil {
		return fmt.Errorf("controller: temporary file %s is unsafe: %w", path, err)
	}
	return nil
}
