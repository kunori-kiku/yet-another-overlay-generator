package agent

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"golang.zx2c4.com/wireguard/wgctrl/wgtypes"
)

// DefaultKeyPath is where the agent stores its locally-held WireGuard private
// key. install.sh reads this same path (custody-gated) to splice the key into the
// copied /etc/wireguard confs over the PRIVATEKEY_PLACEHOLDER sentinel.
const DefaultKeyPath = "/etc/wireguard/agent.key"

// EnsureKey is the idempotent key-generation step. If keyPath already securely
// holds a valid WireGuard private key it is reused (so re-running keygen never
// rotates the node's identity); an unsafe or malformed existing file is rejected
// without chmodding or trusting its contents. When absent, a fresh key is
// generated with wgtypes.GeneratePrivateKey and written mode 0600. The matching public key is
// returned (base64, wgtypes.Key.String() form) so the caller can print/register
// it — that public key is what the controller renders the fleet from.
//
// The private key is the ONLY secret the agent persists, and it is written
// exclusively to keyPath at mode 0600. It is never logged, never returned, and
// never written anywhere else.
func EnsureKey(keyPath string) (pubKey string, created bool, err error) {
	if strings.TrimSpace(keyPath) == "" {
		return "", false, fmt.Errorf("agent: empty key path")
	}
	dir := filepath.Dir(keyPath)
	if err := EnsureSecureOwnedDir(dir); err != nil {
		return "", false, fmt.Errorf("agent: secure key dir: %w", err)
	}
	// Key creation waits for an in-flight creator/rotation, then rechecks the
	// installed key. Unlike the top-level agent operation lease, contention here
	// is an ordinary short critical section rather than a competing host apply.
	release, err := acquireExclusiveFileLock(keyPath+".lock", "WireGuard key", false)
	if err != nil {
		return "", false, err
	}
	defer func() { _ = release() }()

	if data, readErr := ReadPrivateFile(keyPath); readErr == nil {
		// Existing key: parse and reuse. A corrupt file is a hard error rather
		// than a silent rotate — rotating identity on corruption would orphan the
		// node's registered public key and is never what an operator wants.
		key, parseErr := wgtypes.ParseKey(strings.TrimSpace(string(data)))
		if parseErr != nil {
			return "", false, fmt.Errorf("agent: existing key at %s is unparseable (refusing to overwrite): %w", keyPath, parseErr)
		}
		return key.PublicKey().String(), false, nil
	} else if !errors.Is(readErr, os.ErrNotExist) {
		return "", false, fmt.Errorf("agent: read existing key %s: %w", keyPath, readErr)
	}

	// Generate and persist a new key.
	pub, err := generateAndWriteKeyLocked(keyPath)
	if err != nil {
		return "", false, err
	}
	return pub, true, nil
}

// RegenerateKey force-rotates the local WireGuard private key: it ALWAYS generates a
// fresh private key and writes it to keyPath (mode 0600), overwriting any existing key
// there, and returns the corresponding public key. Unlike EnsureKey (which reuses an
// existing key so it never rotates identity), RegenerateKey is the explicit rotation
// path — it is driven by a controller-requested fleet rekey, after which the agent
// re-registers the NEW public key and awaits the operator's redeploy.
//
// As with EnsureKey, the private key is the only secret persisted, written exclusively
// to keyPath at mode 0600; it is never logged, never returned, and never written
// anywhere else. The write is atomic (temp file + rename) so a crash mid-rotation
// cannot leave a truncated key.
func RegenerateKey(keyPath string) (pubKey string, err error) {
	if strings.TrimSpace(keyPath) == "" {
		return "", fmt.Errorf("agent: empty key path")
	}
	dir := filepath.Dir(keyPath)
	if err := EnsureSecureOwnedDir(dir); err != nil {
		return "", fmt.Errorf("agent: secure key dir: %w", err)
	}
	release, err := acquireExclusiveFileLock(keyPath+".lock", "WireGuard key", false)
	if err != nil {
		return "", err
	}
	defer func() { _ = release() }()
	if info, statErr := os.Lstat(keyPath); statErr == nil {
		if err := validateOwnedRegularFile(keyPath, info); err != nil {
			return "", err
		}
	} else if !os.IsNotExist(statErr) {
		return "", fmt.Errorf("agent: inspect key %s: %w", keyPath, statErr)
	}
	return generateAndWriteKeyLocked(keyPath)
}

// generateAndWriteKey generates a fresh WireGuard private key and writes it to keyPath
// (mode 0600, parent dir 0700), atomically via a temp file + rename so a crash mid-write
// cannot leave a truncated key. It returns the corresponding public key. It is the shared
// implementation behind EnsureKey's create path and RegenerateKey's unconditional rotate.
func generateAndWriteKeyLocked(keyPath string) (pubKey string, err error) {
	key, genErr := wgtypes.GeneratePrivateKey()
	if genErr != nil {
		return "", fmt.Errorf("agent: generate private key: %w", genErr)
	}
	dir := filepath.Dir(keyPath)
	tmpFile, err := os.CreateTemp(dir, "."+filepath.Base(keyPath)+".tmp-*")
	if err != nil {
		return "", fmt.Errorf("agent: create key temp file: %w", err)
	}
	tmp := tmpFile.Name()
	removeTemp := true
	defer func() {
		if removeTemp {
			_ = os.Remove(tmp)
		}
	}()
	if err := tmpFile.Chmod(0600); err != nil {
		_ = tmpFile.Close()
		return "", fmt.Errorf("agent: protect key temp file: %w", err)
	}
	if _, err := tmpFile.Write([]byte(key.String() + "\n")); err != nil {
		_ = tmpFile.Close()
		return "", fmt.Errorf("agent: write key: %w", err)
	}
	if err := tmpFile.Sync(); err != nil {
		_ = tmpFile.Close()
		return "", fmt.Errorf("agent: sync key temp file: %w", err)
	}
	if err := tmpFile.Close(); err != nil {
		return "", fmt.Errorf("agent: close key temp file: %w", err)
	}
	if err := replaceFileAtomic(tmp, keyPath); err != nil {
		return "", fmt.Errorf("agent: install key: %w", err)
	}
	removeTemp = false
	if err := os.Chmod(keyPath, 0600); err != nil {
		return "", fmt.Errorf("agent: protect installed key: %w", err)
	}
	if err := syncDirectory(dir); err != nil {
		return "", fmt.Errorf("agent: sync key directory: %w", err)
	}
	return key.PublicKey().String(), nil
}
