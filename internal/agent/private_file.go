package agent

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// WritePrivateFileAtomic durably replaces path with data at mode 0600. The
// destination directory must be owned by the running agent and must not be
// group/world writable; existing symlink and special-file destinations are
// rejected. The same-directory private temp plus atomic replacement means a
// crash cannot expose a truncated secret or inherit an old permissive mode.
func WritePrivateFileAtomic(path string, data []byte) error {
	if strings.TrimSpace(path) == "" {
		return fmt.Errorf("agent: private file path must not be empty")
	}
	dir := filepath.Dir(path)
	if err := EnsureSecureOwnedDir(dir); err != nil {
		return fmt.Errorf("agent: secure private-file directory: %w", err)
	}
	if info, err := os.Lstat(path); err == nil {
		if err := validateOwnedRegularFile(path, info); err != nil {
			return err
		}
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("agent: inspect private file %s: %w", path, err)
	}

	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return fmt.Errorf("agent: create private-file temp: %w", err)
	}
	tmpName := tmp.Name()
	removeTemp := true
	defer func() {
		if removeTemp {
			_ = os.Remove(tmpName)
		}
	}()
	if err := tmp.Chmod(0600); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("agent: protect private-file temp: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("agent: write private-file temp: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("agent: sync private-file temp: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("agent: close private-file temp: %w", err)
	}
	if err := replaceFileAtomic(tmpName, path); err != nil {
		return fmt.Errorf("agent: install private file: %w", err)
	}
	removeTemp = false
	if err := os.Chmod(path, 0600); err != nil {
		return fmt.Errorf("agent: protect installed private file: %w", err)
	}
	if err := syncDirectory(dir); err != nil {
		return fmt.Errorf("agent: sync private-file directory: %w", err)
	}
	return nil
}
