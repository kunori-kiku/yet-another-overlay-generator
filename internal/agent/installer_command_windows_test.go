//go:build windows

package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunRefusesWindowsApplyBeforeFetchOrStateMutation(t *testing.T) {
	stateDir := t.TempDir()
	fetched := false
	_, err := Run(&Config{
		NodeID:   "alpha",
		Source:   fetchTrackingSource{fetched: &fetched},
		StateDir: stateDir,
	})
	if err == nil || !strings.Contains(err.Error(), "install.sh execution is unsupported on Windows") {
		t.Fatalf("Run error = %v, want explicit unsupported apply refusal", err)
	}
	if fetched {
		t.Fatal("Windows apply refusal fetched a bundle")
	}
	for _, name := range []string{stateLockFileName, stateFileName} {
		if _, statErr := os.Stat(filepath.Join(stateDir, name)); !os.IsNotExist(statErr) {
			t.Fatalf("Windows apply refusal mutated %s: %v", name, statErr)
		}
	}
}
