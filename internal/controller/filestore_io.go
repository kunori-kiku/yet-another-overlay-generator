package controller

// filestore_io.go — FileStore path-safety helpers and the atomic, crash-consistent write
// primitive (temp-file + fsync + rename) plus the JSON read/write helpers. Split from
// filestore.go (plan-2); no logic change.

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// --- path helpers -----------------------------------------------------------

// sanitizeComponent rejects values that are unsafe to use as a single path
// component: empty, ".", "..", or anything containing a path separator (or a
// platform-specific separator / NUL). This prevents a malicious or malformed
// TenantID/NodeID from escaping the store root via path traversal.
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

// tenantDir returns (and does not create) the directory for a tenant, after
// validating the TenantID is a safe path component.
func (fs *FileStore) tenantDir(t TenantID) (string, error) {
	tc, err := sanitizeComponent("tenant id", string(t))
	if err != nil {
		return "", err
	}
	return filepath.Join(fs.root, tc), nil
}

// ensureTenantDir creates the tenant directory and its sub-directories (0700).
func (fs *FileStore) ensureTenantDir(t TenantID) (string, error) {
	dir, err := fs.tenantDir(t)
	if err != nil {
		return "", err
	}
	for _, sub := range []string{"", "nodes", "bundles", "tokens", "login-challenges", "apitokens", "operators", "sessions", "topology-history"} {
		if err := os.MkdirAll(filepath.Join(dir, sub), 0700); err != nil {
			return "", fmt.Errorf("controller: create tenant dir: %w", err)
		}
	}
	return dir, nil
}

// nodePath returns the on-disk path for a node record after validating nodeID.
func (fs *FileStore) nodePath(dir, nodeID string) (string, error) {
	nc, err := sanitizeComponent("node id", nodeID)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "nodes", nc+".json"), nil
}

// bundlePath returns the on-disk path for a node's staged/current bundle.
func (fs *FileStore) bundlePath(dir, nodeID, kind string) (string, error) {
	nc, err := sanitizeComponent("node id", nodeID)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "bundles", nc+"."+kind+".json"), nil
}

// tokenPath returns the on-disk path for an enrollment token after validating the
// tokenHash is a safe single path component (it is a hex SHA-256 in practice, but
// it is sanitized like any other untrusted key to prevent path traversal).
func (fs *FileStore) tokenPath(dir, tokenHash string) (string, error) {
	tc, err := sanitizeComponent("token hash", tokenHash)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "tokens", tc+".json"), nil
}

// loginChallengePath returns the on-disk path for a passkey login challenge after
// validating the challengeHash is a safe single path component (a hex SHA-256 in
// practice, sanitized like any untrusted key to prevent path traversal).
func (fs *FileStore) loginChallengePath(dir, challengeHash string) (string, error) {
	cc, err := sanitizeComponent("login challenge hash", challengeHash)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "login-challenges", cc+".json"), nil
}

// apiTokenPath returns the on-disk path for a node API token's reverse-index entry
// after validating the hash is a safe single path component (a hex SHA-256 in
// practice, sanitized like any untrusted key to prevent path traversal).
func (fs *FileStore) apiTokenPath(dir, tokenHash string) (string, error) {
	tc, err := sanitizeComponent("api token hash", tokenHash)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "apitokens", tc+".json"), nil
}

// operatorPath returns the on-disk path for an operator account after validating the
// username is a safe single path component.
func (fs *FileStore) operatorPath(dir, username string) (string, error) {
	uc, err := sanitizeComponent("operator username", username)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "operators", uc+".json"), nil
}

// sessionPath returns the on-disk path for an operator session after validating the
// token hash is a safe single path component (a hex SHA-256 in practice, sanitized
// like any untrusted key to prevent path traversal).
func (fs *FileStore) sessionPath(dir, tokenHash string) (string, error) {
	tc, err := sanitizeComponent("session token hash", tokenHash)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "sessions", tc+".json"), nil
}

// --- atomic JSON IO ---------------------------------------------------------

// writeJSONAtomic marshals v and writes it to path via a temp-file + rename so a
// crash cannot leave a truncated file. The parent directory must already exist.
//
// Crash-consistency (B2): the temp file's bytes are fsync'd to stable storage BEFORE the
// rename, and the parent directory is fsync'd AFTER the rename, so a power loss between the
// two steps cannot leave the target naming a not-yet-durable inode. Without these syncs the
// rename can be ordered ahead of the data write on some filesystems, exposing a zero-length
// or stale file after a crash — for the identity/credential/trust-list store that backs the
// keystone, a silently-corrupt record is worse than an error.
func writeJSONAtomic(path string, v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return fmt.Errorf("controller: marshal %s: %w", filepath.Base(path), err)
	}
	return writeBytesDurable(path, data)
}

// writeBytesDurable writes data to path via a temp-file + rename so a crash cannot leave a
// truncated file, with the same crash-consistency guarantee writeJSONAtomic documents: the
// temp file's bytes are fsync'd to stable storage BEFORE the rename, and the parent directory
// is fsync'd AFTER the rename, so a power loss between the two steps cannot leave the target
// naming a not-yet-durable inode. The parent directory must already exist. This is the single
// durable-write primitive shared by every rename-atomic writer in the store (writeJSONAtomic
// for the per-record JSON files, writeAuditJSONL for the audit-log rotation/migration rewrite)
// so the fsync dance lives in exactly one place (B2).
func writeBytesDurable(path string, data []byte) error {
	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0600)
	if err != nil {
		return fmt.Errorf("controller: write %s: %w", filepath.Base(path), err)
	}
	if _, werr := f.Write(data); werr != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return fmt.Errorf("controller: write %s: %w", filepath.Base(path), werr)
	}
	if serr := f.Sync(); serr != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return fmt.Errorf("controller: sync %s: %w", filepath.Base(path), serr)
	}
	if cerr := f.Close(); cerr != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("controller: close %s: %w", filepath.Base(path), cerr)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("controller: install %s: %w", filepath.Base(path), err)
	}
	// fsync the parent directory so the rename (a directory metadata change) is itself
	// durable; otherwise a crash could lose the rename and leave the OLD file in place.
	// Best-effort: a dir that cannot be opened/synced (e.g. some network FS) must not fail
	// an otherwise-committed write — the rename already landed.
	if dir, derr := os.Open(filepath.Dir(path)); derr == nil {
		_ = dir.Sync()
		_ = dir.Close()
	}
	return nil
}

// readJSON reads and unmarshals path into v. A missing file is reported via
// os.IsNotExist on the returned error so callers can map it to ErrNotFound.
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
