package controller

// filestore_io.go — filekv path-safety helpers and the atomic, crash-consistent write primitive
// (temp-file + fsync + rename) plus the JSON read/write helpers. The per-collection path resolution is
// pathFor (filestore.go); this file holds the tenant-dir helpers, the traversal-safe component check,
// and the durable byte writer shared by every rename-atomic writer in the store.

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// --- path helpers -----------------------------------------------------------

// sanitizeComponent rejects values unsafe as a single path component: empty, ".", "..", or anything
// containing a path separator (or NUL). This prevents a malicious/malformed TenantID/NodeID/hash from
// escaping the store root via path traversal.
func sanitizeComponent(kind, v string) (string, error) {
	if v == "" {
		return "", fmt.Errorf("controller: %s must not be empty", kind)
	}
	if v == "." || v == ".." {
		return "", fmt.Errorf("controller: %s %q is not a valid path component", kind, v)
	}
	if strings.ContainsRune(v, '/') || strings.ContainsRune(v, os.PathSeparator) ||
		strings.ContainsRune(v, '\x00') {
		return "", fmt.Errorf("controller: %s %q must not contain a path separator", kind, v)
	}
	return v, nil
}

// tenantDir returns (without creating) the directory for a tenant, after validating the TenantID is a
// safe path component.
func (fs *filekv) tenantDir(t TenantID) (string, error) {
	tc, err := sanitizeComponent("tenant id", string(t))
	if err != nil {
		return "", err
	}
	// The root was validated at construction, but it remains an external
	// filesystem object and may have been replaced since. Revalidate it at each
	// operation, then validate an existing tenant without creating one on reads.
	if err := revalidateSecureStoreDir(fs.root); err != nil {
		return "", fmt.Errorf("controller: unsafe filestore root: %w", err)
	}
	dir := filepath.Join(fs.root, tc)
	if err := validateSecureStoreDirIfExists(dir); err != nil {
		return "", err
	}
	return dir, nil
}

// ensureTenantDir creates the tenant directory and its sub-directories (0700).
func (fs *filekv) ensureTenantDir(t TenantID) (string, error) {
	tc, err := sanitizeComponent("tenant id", string(t))
	if err != nil {
		return "", err
	}
	dir, err := ensureSecureStoreChild(fs.root, tc)
	if err != nil {
		return "", fmt.Errorf("controller: create tenant custody: %w", err)
	}
	for _, sub := range fileCollectionSubdirs() {
		if _, err := ensureSecureStoreChild(dir, sub); err != nil {
			return "", fmt.Errorf("controller: create tenant custody: %w", err)
		}
	}
	if err := revalidateSecureStoreDir(fs.root); err != nil {
		return "", fmt.Errorf("controller: unsafe filestore root: %w", err)
	}
	if err := revalidateSecureStoreDir(dir); err != nil {
		return "", err
	}
	return dir, nil
}

// fileCollectionSubdirs derives the custody tree from the frozen collection
// registry so adding a keyed collection cannot accidentally omit creation and
// security validation for its directory.
func fileCollectionSubdirs() []string {
	seen := make(map[string]struct{})
	for _, spec := range fileColls {
		if !spec.singleton {
			seen[spec.subdir] = struct{}{}
		}
	}
	out := make([]string, 0, len(seen))
	for sub := range seen {
		out = append(out, sub)
	}
	sort.Strings(out)
	return out
}

// --- atomic JSON IO ---------------------------------------------------------

// writeJSONAtomic marshals v (pretty-printed) and writes it to path via a temp-file + rename so a crash
// cannot leave a truncated file. The parent directory must already exist.
//
// Crash-consistency (B2): the temp file's bytes are fsync'd BEFORE the rename, and the parent directory
// is fsync'd AFTER the rename, so a power loss between the two cannot leave the target naming a
// not-yet-durable inode. For the identity/credential/trust-list store, a silently-corrupt record is
// worse than an error.
func writeJSONAtomic(path string, v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return fmt.Errorf("controller: marshal %s: %w", filepath.Base(path), err)
	}
	return writeBytesDurable(path, data)
}

// writeBytesDurable writes data to path via a temp-file + rename with the crash-consistency guarantee
// writeJSONAtomic documents (fsync the temp bytes before the rename, fsync the parent dir after). The
// parent directory must already exist. This is the single durable-write primitive shared by every
// rename-atomic writer in the store (put for per-record files, writeAuditJSONL for the audit rewrite),
// so the fsync dance lives in exactly one place (B2).
func writeBytesDurable(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := revalidateSecureStoreDir(dir); err != nil {
		return fmt.Errorf("controller: unsafe parent for %s: %w", filepath.Base(path), err)
	}
	// CreateTemp uses an unpredictable same-directory name and O_EXCL-style
	// creation. Unlike the historical <record>.tmp + O_TRUNC path, a planted
	// symlink can neither redirect nor clobber another file.
	f, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return fmt.Errorf("controller: write %s: %w", filepath.Base(path), err)
	}
	tmp := f.Name()
	removeTemp := true
	defer func() {
		if removeTemp {
			_ = os.Remove(tmp)
		}
	}()
	if err := f.Chmod(0600); err != nil {
		_ = f.Close()
		return fmt.Errorf("controller: protect %s temporary file: %w", filepath.Base(path), err)
	}
	if err := validateOpenedStoreTemp(tmp, f); err != nil {
		_ = f.Close()
		return err
	}
	if _, werr := f.Write(data); werr != nil {
		_ = f.Close()
		return fmt.Errorf("controller: write %s: %w", filepath.Base(path), werr)
	}
	if serr := f.Sync(); serr != nil {
		_ = f.Close()
		return fmt.Errorf("controller: sync %s: %w", filepath.Base(path), serr)
	}
	if cerr := f.Close(); cerr != nil {
		return fmt.Errorf("controller: close %s: %w", filepath.Base(path), cerr)
	}
	// Revalidate the custody boundary immediately before publishing the opened
	// inode, then atomically replace the stable record name.
	if err := revalidateSecureStoreDir(dir); err != nil {
		return fmt.Errorf("controller: unsafe parent for %s: %w", filepath.Base(path), err)
	}
	if err := replaceStoreFileAtomic(tmp, path); err != nil {
		return fmt.Errorf("controller: install %s: %w", filepath.Base(path), err)
	}
	removeTemp = false
	// fsync the parent directory so the rename (a directory metadata change) is
	// itself durable. Windows uses MOVEFILE_WRITE_THROUGH in replaceStoreFileAtomic.
	if err := syncStoreDirectory(dir); err != nil {
		return fmt.Errorf("controller: sync directory for %s: %w", filepath.Base(path), err)
	}
	return nil
}

// readJSON reads and unmarshals path into v. A missing file is reported via os.IsNotExist on the
// returned error so callers can map it to errKVNotFound / ErrNotFound.
func readJSON(path string, v any) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(data, v); err != nil {
		return fmt.Errorf("controller: parse %s: %w", filepath.Base(path), err)
	}
	return nil
}
