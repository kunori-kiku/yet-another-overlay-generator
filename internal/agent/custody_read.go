package agent

import (
	"fmt"
	"io"
	"strings"
)

const maxCustodyFileBytes = 4 << 20

// custodyReadPolicy distinguishes secret material from public-but-integrity-critical
// pins/state. Both classes require a regular file in a validated direct parent
// directory. Unix additionally enforces ownership and permissions: secret files
// permit no group/world access; integrity files may be readable but not writable.
type custodyReadPolicy uint8

const (
	custodyReadIntegrity custodyReadPolicy = iota
	custodyReadSecret
)

// ReadPrivateFile reads a custody secret such as the node WireGuard private key
// or controller bearer token. It rejects unsafe parent directories, links, and
// special files. On Unix it also rejects wrong-owner files and any group/world
// permission. Validation occurs on the opened descriptor before bytes are trusted.
func ReadPrivateFile(path string) ([]byte, error) {
	return readCustodyFile(path, custodyReadSecret)
}

// ReadProtectedFile reads public-but-integrity-critical custody material such as
// persisted state or pinned signing/operator public keys. It has the same
// descriptor and ownership checks as ReadPrivateFile, but on Unix permits
// group/world reads while rejecting group/world writes.
func ReadProtectedFile(path string) ([]byte, error) {
	return readCustodyFile(path, custodyReadIntegrity)
}

func readCustodyFile(path string, policy custodyReadPolicy) ([]byte, error) {
	if strings.TrimSpace(path) == "" {
		return nil, fmt.Errorf("agent: custody file path must not be empty")
	}
	return readCustodyFilePlatform(path, policy)
}

func readCustodyBytes(r io.Reader, path string) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(r, maxCustodyFileBytes+1))
	if err != nil {
		return nil, fmt.Errorf("agent: read custody file %s: %w", path, err)
	}
	if len(data) > maxCustodyFileBytes {
		return nil, fmt.Errorf("agent: custody file %s exceeds %d bytes", path, maxCustodyFileBytes)
	}
	return data, nil
}
