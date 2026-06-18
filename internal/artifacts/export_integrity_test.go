package artifacts

import (
	"bufio"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestExport_ChecksumsCoversInstallScript verifies that install.sh -- the trust anchor
// executed as root -- is included in checksums.sha256 (audit item D24), and that the
// recorded hash matches the on-disk file contents.
func TestExport_ChecksumsCoversInstallScript(t *testing.T) {
	result := minimalCompileResult()
	outputDir := t.TempDir()

	if _, err := Export(result, outputDir); err != nil {
		t.Fatalf("Export failed: %v", err)
	}

	nodeDir := filepath.Join(outputDir, "alpha")

	// Read checksums.sha256 and parse out the hash recorded on the install.sh line.
	checksumsPath := filepath.Join(nodeDir, "checksums.sha256")
	recordedHash, ok := readChecksumFor(t, checksumsPath, "install.sh")
	if !ok {
		t.Fatalf("checksums.sha256 missing the install.sh line (D24: trust anchor not covered); file contents:\n%s",
			mustReadFile(t, checksumsPath))
	}

	// Compute the hash of install.sh's actual on-disk contents and compare against the recorded value.
	installPath := filepath.Join(nodeDir, "install.sh")
	actualBytes := mustReadFileBytes(t, installPath)
	actualHash := fmt.Sprintf("%x", sha256.Sum256(actualBytes))

	if recordedHash != actualHash {
		t.Errorf("install.sh recorded checksum does not match actual contents:\n  recorded: %s\n  actual: %s", recordedHash, actualHash)
	}

	// manifest.json carries compile-time timestamps such as compiled_at and is deliberately excluded from integrity checking per spec.
	if _, present := readChecksumFor(t, checksumsPath, "manifest.json"); present {
		t.Errorf("manifest.json should not appear in checksums.sha256 (explicitly excluded by spec)")
	}
}

// readChecksumFor parses a sha256sum-style checksum file and returns the hash for the
// given relative path. A checksum line has the format "<hex>  <relpath>" (separated by two
// spaces).
func readChecksumFor(t *testing.T, checksumsPath, relPath string) (string, bool) {
	t.Helper()

	f, err := os.Open(checksumsPath)
	if err != nil {
		t.Fatalf("failed to read checksums.sha256: %v", err)
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			continue
		}
		fields := strings.SplitN(line, "  ", 2)
		if len(fields) != 2 {
			continue
		}
		if fields[1] == relPath {
			return fields[0], true
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("failed to scan checksums.sha256: %v", err)
	}
	return "", false
}

func mustReadFile(t *testing.T, path string) string {
	t.Helper()
	return string(mustReadFileBytes(t, path))
}

func mustReadFileBytes(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("failed to read %s: %v", path, err)
	}
	return data
}
