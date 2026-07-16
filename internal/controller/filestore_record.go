package controller

// filestore_record.go owns stable-record custody after the root/tenant/
// collection directories have been established. All reads, existence checks,
// deletes, and append opens reject final-component links and special files;
// descriptor-bearing operations are relative to a pinned os.Root.

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

const storeFileOpenAttempts = 3

// validateStoreFileInfo rejects final-component symlinks and special files and
// applies the platform custody checks to a stable FileStore record. Existing
// record modes are deliberately not tightened or rejected here: all controller
// writes are 0600, but older/restored state may be more permissive and must not
// acquire a surprise compatibility failure merely from this no-follow fix.
func validateStoreFileInfo(path string, info os.FileInfo) error {
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return fmt.Errorf("controller: custody file %s must be a regular file, not a symlink or special file", path)
	}
	if err := validateStoreFilePlatform(info); err != nil {
		return fmt.Errorf("controller: custody file %s is unsafe: %w", path, err)
	}
	return nil
}

// openSecureStoreParent pins the direct parent directory behind an os.Root and
// proves that the pathname still names that same secure directory. Subsequent
// record operations are relative to this handle, so replacing or moving the
// parent cannot redirect an open outside the directory that was validated.
func openSecureStoreParent(path string) (*os.Root, os.FileInfo, error) {
	parent := filepath.Dir(path)
	for attempt := 0; attempt < storeFileOpenAttempts; attempt++ {
		pathInfo, err := os.Lstat(parent)
		if err != nil {
			return nil, nil, fmt.Errorf("controller: inspect custody directory %s: %w", parent, err)
		}
		if err := validateSecureStoreDir(parent, pathInfo); err != nil {
			return nil, nil, err
		}
		root, err := os.OpenRoot(parent)
		if err != nil {
			return nil, nil, fmt.Errorf("controller: open custody directory %s: %w", parent, err)
		}
		openedInfo, err := root.Stat(".")
		if err != nil {
			_ = root.Close()
			return nil, nil, fmt.Errorf("controller: inspect opened custody directory %s: %w", parent, err)
		}
		if err := validateSecureStoreDir(parent, openedInfo); err != nil {
			_ = root.Close()
			return nil, nil, err
		}
		currentInfo, err := os.Lstat(parent)
		if err != nil {
			_ = root.Close()
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return nil, nil, fmt.Errorf("controller: re-inspect custody directory %s: %w", parent, err)
		}
		if err := validateSecureStoreDir(parent, currentInfo); err != nil {
			_ = root.Close()
			return nil, nil, err
		}
		if !os.SameFile(pathInfo, openedInfo) || !os.SameFile(currentInfo, openedInfo) {
			_ = root.Close()
			continue
		}
		return root, openedInfo, nil
	}
	return nil, nil, fmt.Errorf("controller: custody directory %s changed repeatedly while opening", parent)
}

func revalidateOpenedStoreParent(path string, openedInfo os.FileInfo) error {
	parent := filepath.Dir(path)
	currentInfo, err := os.Lstat(parent)
	if err != nil {
		return fmt.Errorf("controller: re-inspect custody directory %s: %w", parent, err)
	}
	if err := validateSecureStoreDir(parent, currentInfo); err != nil {
		return err
	}
	if !os.SameFile(currentInfo, openedInfo) {
		return fmt.Errorf("controller: custody directory %s changed while accessing %s", parent, filepath.Base(path))
	}
	return nil
}

// openExistingStoreFile opens one already-existing stable record without ever
// trusting a pathname-only observation. The caller receives a descriptor bound
// to the same regular, platform-valid inode observed at the stable pathname.
// Reads and append writes therefore cannot be redirected through a planted or
// concurrently swapped final-component symlink.
func openExistingStoreFile(path string, flag int) (*os.File, error) {
	if flag&(os.O_CREATE|os.O_EXCL|os.O_TRUNC) != 0 {
		return nil, fmt.Errorf("controller: invalid existing custody-file open flags for %s", path)
	}
	for attempt := 0; attempt < storeFileOpenAttempts; attempt++ {
		root, parentInfo, err := openSecureStoreParent(path)
		if err != nil {
			return nil, err
		}
		name := filepath.Base(path)
		pathInfo, err := root.Lstat(name)
		if err != nil {
			_ = root.Close()
			return nil, fmt.Errorf("controller: inspect custody file %s: %w", path, err)
		}
		if err := validateStoreFileInfo(path, pathInfo); err != nil {
			_ = root.Close()
			return nil, err
		}

		f, err := root.OpenFile(name, storeFileOpenFlags(flag), 0)
		if err != nil {
			_ = root.Close()
			return nil, fmt.Errorf("controller: open custody file %s: %w", path, err)
		}
		openedInfo, err := f.Stat()
		if err != nil {
			_ = f.Close()
			_ = root.Close()
			return nil, fmt.Errorf("controller: inspect opened custody file %s: %w", path, err)
		}
		if err := validateStoreFileInfo(path, openedInfo); err != nil {
			_ = f.Close()
			_ = root.Close()
			return nil, err
		}

		// Re-observe the stable pathname after opening. Requiring both pathname
		// observations to name the opened inode closes the ordinary rename/symlink
		// swap window; os.Root supplies the kernel-level confinement.
		currentInfo, err := root.Lstat(name)
		if err != nil {
			_ = f.Close()
			_ = root.Close()
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return nil, fmt.Errorf("controller: re-inspect custody file %s: %w", path, err)
		}
		if err := validateStoreFileInfo(path, currentInfo); err != nil {
			_ = f.Close()
			_ = root.Close()
			return nil, err
		}
		if !os.SameFile(pathInfo, openedInfo) || !os.SameFile(currentInfo, openedInfo) {
			_ = f.Close()
			_ = root.Close()
			continue
		}
		if err := revalidateOpenedStoreParent(path, parentInfo); err != nil {
			_ = f.Close()
			_ = root.Close()
			return nil, err
		}
		if err := root.Close(); err != nil {
			_ = f.Close()
			return nil, fmt.Errorf("controller: close custody directory for %s: %w", path, err)
		}
		return f, nil
	}
	return nil, fmt.Errorf("controller: custody file %s changed repeatedly while opening", path)
}

// readStoreFile reads a stable record through its validated descriptor. It
// intentionally retains os.ReadFile's unbounded behavior because several
// existing store records (topologies and bundles) have collection-specific
// bounds above this storage layer.
func readStoreFile(path string) ([]byte, error) {
	f, err := openExistingStoreFile(path, os.O_RDONLY)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	data, err := io.ReadAll(f)
	if err != nil {
		return nil, fmt.Errorf("controller: read custody file %s: %w", path, err)
	}
	return data, nil
}

// storeFileExists is the metadata-only existence check used by telemetry. Lstat
// plus regular-file/platform validation rejects a symlink or FIFO without
// opening it, preserving the low-cost heartbeat path and eliminating any
// special-file blocking risk.
func storeFileExists(path string) (bool, error) {
	if err := validateSecureStoreDirIfExists(filepath.Dir(path)); err != nil {
		return false, err
	}
	info, err := os.Lstat(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		return false, fmt.Errorf("controller: inspect custody file %s: %w", path, err)
	}
	if err := validateStoreFileInfo(path, info); err != nil {
		return false, err
	}
	return true, nil
}

// removeStoreFile validates and unlinks a stable record relative to the same
// pinned parent handle. It returns removed=false for an absent record. Rooted
// removal cannot be redirected by replacing the parent pathname between the
// custody check and unlink.
func removeStoreFile(path string) (removed bool, err error) {
	root, parentInfo, err := openSecureStoreParent(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		return false, err
	}
	defer root.Close()

	name := filepath.Base(path)
	pathInfo, err := root.Lstat(name)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		return false, fmt.Errorf("controller: inspect custody file %s before removal: %w", path, err)
	}
	if err := validateStoreFileInfo(path, pathInfo); err != nil {
		return false, err
	}
	f, err := root.OpenFile(name, storeFileOpenFlags(os.O_RDONLY), 0)
	if err != nil {
		return false, fmt.Errorf("controller: open custody file %s before removal: %w", path, err)
	}
	openedInfo, statErr := f.Stat()
	closeErr := f.Close()
	if statErr != nil {
		return false, fmt.Errorf("controller: inspect custody file %s before removal: %w", path, statErr)
	}
	if closeErr != nil {
		return false, fmt.Errorf("controller: close custody file %s before removal: %w", path, closeErr)
	}
	if err := validateStoreFileInfo(path, openedInfo); err != nil {
		return false, err
	}
	currentInfo, err := root.Lstat(name)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		return false, fmt.Errorf("controller: re-inspect custody file %s before removal: %w", path, err)
	}
	if err := validateStoreFileInfo(path, currentInfo); err != nil {
		return false, err
	}
	if !os.SameFile(pathInfo, openedInfo) || !os.SameFile(currentInfo, openedInfo) {
		return false, fmt.Errorf("controller: custody file %s changed while preparing removal", path)
	}
	if err := root.Remove(name); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		return false, fmt.Errorf("controller: remove custody file %s: %w", path, err)
	}
	if err := syncOpenedStoreDirectory(root); err != nil {
		return true, fmt.Errorf("controller: sync custody directory after removing %s: %w", path, err)
	}
	if err := revalidateOpenedStoreParent(path, parentInfo); err != nil {
		return true, err
	}
	return true, nil
}

// openStoreFileForAppend safely opens or creates an append-only stable record.
// The absent case uses O_EXCL, validates the opened descriptor against the
// newly installed pathname, and durably commits the empty file's directory
// entry before returning it to a caller that may append durable content.
func openStoreFileForAppend(path string) (*os.File, error) {
	for attempt := 0; attempt < storeFileOpenAttempts; attempt++ {
		root, parentInfo, err := openSecureStoreParent(path)
		if err != nil {
			return nil, err
		}
		name := filepath.Base(path)
		_, err = root.Lstat(name)
		if err == nil {
			_ = root.Close()
			return openExistingStoreFile(path, os.O_WRONLY|os.O_APPEND)
		}
		if !errors.Is(err, os.ErrNotExist) {
			_ = root.Close()
			return nil, fmt.Errorf("controller: inspect custody file %s: %w", path, err)
		}

		f, err := root.OpenFile(name, storeFileOpenFlags(os.O_CREATE|os.O_EXCL|os.O_WRONLY|os.O_APPEND), 0600)
		if errors.Is(err, os.ErrExist) {
			_ = root.Close()
			continue
		}
		if err != nil {
			_ = root.Close()
			return nil, fmt.Errorf("controller: create custody file %s: %w", path, err)
		}
		openedInfo, err := f.Stat()
		if err != nil {
			_ = f.Close()
			_ = root.Close()
			return nil, fmt.Errorf("controller: inspect created custody file %s: %w", path, err)
		}
		if err := validateStoreFileInfo(path, openedInfo); err != nil {
			_ = f.Close()
			_ = root.Close()
			return nil, err
		}
		pathInfo, err := root.Lstat(name)
		if err != nil {
			_ = f.Close()
			_ = root.Close()
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return nil, fmt.Errorf("controller: inspect created custody path %s: %w", path, err)
		}
		if err := validateStoreFileInfo(path, pathInfo); err != nil {
			_ = f.Close()
			_ = root.Close()
			return nil, err
		}
		if !os.SameFile(pathInfo, openedInfo) {
			_ = f.Close()
			_ = root.Close()
			continue
		}
		if err := revalidateOpenedStoreParent(path, parentInfo); err != nil {
			_ = f.Close()
			_ = root.Close()
			return nil, err
		}
		// Persist creation before the first append. If either sync fails, the
		// caller returns without writing a line; a retry can safely reuse the
		// empty regular file rather than risking an appended record whose name
		// was not durable.
		if err := f.Sync(); err != nil {
			_ = f.Close()
			_ = root.Close()
			return nil, fmt.Errorf("controller: sync created custody file %s: %w", path, err)
		}
		if err := syncOpenedStoreDirectory(root); err != nil {
			_ = f.Close()
			_ = root.Close()
			return nil, fmt.Errorf("controller: sync directory for %s: %w", filepath.Base(path), err)
		}
		if err := revalidateOpenedStoreParent(path, parentInfo); err != nil {
			_ = f.Close()
			_ = root.Close()
			return nil, err
		}
		if err := root.Close(); err != nil {
			_ = f.Close()
			return nil, fmt.Errorf("controller: close custody directory for %s: %w", path, err)
		}
		return f, nil
	}
	return nil, fmt.Errorf("controller: custody file %s changed repeatedly while creating", path)
}
