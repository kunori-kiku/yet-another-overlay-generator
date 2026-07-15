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

	nodeDir := filepath.Join(outputDir, "n1")

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

// TestExport_EgressOverrideIsSigned proves the per-node mimic egress-interface override (a new config
// surface, plan-2 of mimic-runtime-reliability) rides the SIGNED install.sh rather than any unsigned
// path. The override renders INTO install.sh (byte-proven by the localcompile
// 28-mimic-tcp-egress-override golden: MIMIC_EGRESS_IF='wan0'), and install.sh is a checksummed bundle
// member covered by bundle.sig → keystone (TestExport_ChecksumsCoversInstallScript). This ties the
// two: an install.sh carrying the override marker appears in checksums.sha256 with a MATCHING hash, so
// the override cannot be tampered without failing the signature. (Auto-detect nodes carry no baked
// value — the egress is the node's own runtime routing table — so there is nothing to sign there.)
func TestExport_EgressOverrideIsSigned(t *testing.T) {
	result := minimalCompileResult()
	// The renderer emits MIMIC_EGRESS_IF='wan0' for an override node (byte-proven by the localcompile
	// golden); assert that whatever install.sh carries is what gets signed.
	result.InstallScripts["n1"] = "#!/usr/bin/env bash\nMIMIC_EGRESS_IF='wan0'\n"
	outputDir := t.TempDir()
	if _, err := Export(result, outputDir); err != nil {
		t.Fatalf("Export failed: %v", err)
	}
	nodeDir := filepath.Join(outputDir, "n1")

	installBytes := mustReadFileBytes(t, filepath.Join(nodeDir, "install.sh"))
	if !strings.Contains(string(installBytes), "MIMIC_EGRESS_IF='wan0'") {
		t.Fatalf("the egress override was not written into install.sh")
	}
	recordedHash, ok := readChecksumFor(t, filepath.Join(nodeDir, "checksums.sha256"), "install.sh")
	if !ok {
		t.Fatalf("install.sh carrying the egress override is NOT in checksums.sha256 — the new surface would be unsigned")
	}
	actualHash := fmt.Sprintf("%x", sha256.Sum256(installBytes))
	if recordedHash != actualHash {
		t.Errorf("checksums.sha256 does not cover the override install.sh:\n  recorded: %s\n  actual: %s", recordedHash, actualHash)
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
