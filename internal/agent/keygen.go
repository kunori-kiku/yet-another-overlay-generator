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
	key, genErr := wgtypes.GeneratePrivateKey()
	if genErr != nil {
		return "", false, fmt.Errorf("agent: generate private key: %w", genErr)
	}
	if err := os.MkdirAll(filepath.Dir(keyPath), 0700); err != nil {
		return "", false, fmt.Errorf("agent: create key dir: %w", err)
	}
	// Write the private key as wgtypes.Key.String() (base64), mode 0600. Write to
	// a temp file then rename so a crash mid-write cannot leave a truncated key.
	tmp := keyPath + ".tmp"
	if err := os.WriteFile(tmp, []byte(key.String()+"\n"), 0600); err != nil {
		return "", false, fmt.Errorf("agent: write key: %w", err)
	}
	if err := os.Rename(tmp, keyPath); err != nil {
		_ = os.Remove(tmp)
		return "", false, fmt.Errorf("agent: install key: %w", err)
	}

	return key.PublicKey().String(), true, nil
}
