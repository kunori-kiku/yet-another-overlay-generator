package agent

import (
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

// EnsureKey is the idempotent key-generation step. If keyPath already holds a
// valid WireGuard private key it is reused (so re-running keygen never rotates
// the node's identity); otherwise a fresh key is generated with
// wgtypes.GeneratePrivateKey and written mode 0600. The matching public key is
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

	if data, readErr := os.ReadFile(keyPath); readErr == nil {
		// Existing key: parse and reuse. A corrupt file is a hard error rather
		// than a silent rotate — rotating identity on corruption would orphan the
		// node's registered public key and is never what an operator wants.
		key, parseErr := wgtypes.ParseKey(strings.TrimSpace(string(data)))
		if parseErr != nil {
			return "", false, fmt.Errorf("agent: existing key at %s is unparseable (refusing to overwrite): %w", keyPath, parseErr)
		}
		return key.PublicKey().String(), false, nil
	} else if !os.IsNotExist(readErr) {
		return "", false, fmt.Errorf("agent: read key %s: %w", keyPath, readErr)
	}

	// Generate and persist a new key.
	pub, err := generateAndWriteKey(keyPath)
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
	return generateAndWriteKey(keyPath)
}

// generateAndWriteKey generates a fresh WireGuard private key and writes it to keyPath
// (mode 0600, parent dir 0700), atomically via a temp file + rename so a crash mid-write
// cannot leave a truncated key. It returns the corresponding public key. It is the shared
// implementation behind EnsureKey's create path and RegenerateKey's unconditional rotate.
func generateAndWriteKey(keyPath string) (pubKey string, err error) {
	key, genErr := wgtypes.GeneratePrivateKey()
	if genErr != nil {
		return "", fmt.Errorf("agent: generate private key: %w", genErr)
	}
	if err := os.MkdirAll(filepath.Dir(keyPath), 0700); err != nil {
		return "", fmt.Errorf("agent: create key dir: %w", err)
	}
	// Write the private key as wgtypes.Key.String() (base64), mode 0600. Write to
	// a temp file then rename so a crash mid-write cannot leave a truncated key.
	tmp := keyPath + ".tmp"
	if err := os.WriteFile(tmp, []byte(key.String()+"\n"), 0600); err != nil {
		return "", fmt.Errorf("agent: write key: %w", err)
	}
	if err := os.Rename(tmp, keyPath); err != nil {
		_ = os.Remove(tmp)
		return "", fmt.Errorf("agent: install key: %w", err)
	}
	return key.PublicKey().String(), nil
}
