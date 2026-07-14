package renderer

import (
	"crypto/sha256"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kunorikiku/yet-another-overlay-generator/internal/compiler"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/model"
)

// TestInstallScript_ArtifactsJSONCoverageGuard proves the standalone-verifier signed-set coverage
// guard by EXECUTING the rendered install.sh (a render + string-contains check cannot prove refusal).
//
// The defect: install.sh verifies bundle.sig over checksums.sha256 then runs `sha256sum -c`, neither of
// which flags a file NOT listed in the manifest. The mimic fallback then reads artifacts.json for the
// .deb pins it installs as root — so a present-but-unlisted artifacts.json (added to a bundle without
// re-signing) would smuggle in attacker-chosen pins that still pass both checks. The guard (mirroring
// internal/agent/verify.go's check) refuses an unlisted artifacts.json before any pin is read.
func TestInstallScript_ArtifactsJSONCoverageGuard(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not available")
	}
	if os.Geteuid() == 0 {
		t.Skip("must run as non-root to observe the guard firing before the root check")
	}

	node := &model.Node{
		ID: "n1", Name: "alpha", Role: "router", Platform: "debian", OverlayIP: "10.50.0.1",
		Capabilities: model.NodeCapabilities{CanForward: true},
	}
	// Mimic peer → HasMimic → the install.sh reads artifacts.json (the .deb pins) and therefore carries
	// the coverage guard. A non-mimic node never references artifacts.json, so the guard is (correctly)
	// absent there — see internal/render TestAll_ZeroFetchSettings_OmitsArtifactsJSON.
	peers := []compiler.PeerInfo{{
		NodeID: "n2", NodeName: "beta", InterfaceName: "wg-beta",
		ListenPort: 51820, LocalTransitIP: "10.10.0.1", LocalLinkLocal: "fe80::1", MTU: 1408, Mimic: true,
	}}
	script, err := RenderInstallScript(node, peers, false)
	if err != nil {
		t.Fatalf("render: %v", err)
	}

	// Neutralize Phase 0's host cleanup (stop legacy interfaces, rm stale /etc config) with no-op
	// shims on PATH so the test exercises the checksum/guard path HERMETICALLY (host-independent).
	// Without them, a dev host with a real /etc/sysctl.d overlay file fails `rm -f` under `set -e`
	// before the guard is ever reached. sha256sum/grep are left real — the guard needs them.
	stubDir := t.TempDir()
	for _, name := range []string{"systemctl", "wg", "wg-quick", "ip", "modprobe", "sysctl", "rm"} {
		if err := os.WriteFile(filepath.Join(stubDir, name), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}

	// run writes the rendered install.sh into a temp bundle dir with a checksums.sha256 that ALWAYS
	// lists (and matches) payload.txt, an unrelated artifacts.json, and — iff listArtifacts — a matching
	// artifacts.json line. It runs `bash install.sh` (install mode, non-root) and returns exit + output.
	run := func(t *testing.T, listArtifacts bool) (int, string) {
		t.Helper()
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "install.sh"), []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
		payload := []byte("payload\n")
		if err := os.WriteFile(filepath.Join(dir, "payload.txt"), payload, 0o644); err != nil {
			t.Fatal(err)
		}
		psum := sha256.Sum256(payload)
		manifest := fmt.Sprintf("%x  payload.txt\n", psum)
		// The artifacts.json an attacker would smuggle in (or a legitimately-signed one).
		aj := []byte(`{"mimic":{"release_url":"https://attacker.example/x"}}` + "\n")
		if err := os.WriteFile(filepath.Join(dir, "artifacts.json"), aj, 0o644); err != nil {
			t.Fatal(err)
		}
		if listArtifacts {
			asum := sha256.Sum256(aj)
			manifest += fmt.Sprintf("%x  artifacts.json\n", asum)
		}
		if err := os.WriteFile(filepath.Join(dir, "checksums.sha256"), []byte(manifest), 0o644); err != nil {
			t.Fatal(err)
		}
		cmd := exec.Command("bash", "install.sh")
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "PATH="+stubDir+string(os.PathListSeparator)+os.Getenv("PATH"))
		var out strings.Builder
		cmd.Stdout = &out
		cmd.Stderr = &out
		runErr := cmd.Run()
		code := 0
		if ee, ok := runErr.(*exec.ExitError); ok {
			code = ee.ExitCode()
		} else if runErr != nil {
			t.Fatalf("exec install.sh: %v\n%s", runErr, out.String())
		}
		return code, out.String()
	}

	t.Run("unlisted artifacts.json is refused before the root check", func(t *testing.T) {
		code, out := run(t, false)
		if code == 0 {
			t.Fatalf("expected non-zero exit for an unlisted artifacts.json; got 0\n%s", out)
		}
		if !strings.Contains(out, "not covered by the signed checksums") {
			t.Fatalf("expected the coverage-guard refusal; got:\n%s", out)
		}
		if strings.Contains(out, "must be run as root") {
			t.Fatalf("guard must fire BEFORE the root check, but the root check ran:\n%s", out)
		}
	})

	t.Run("listed artifacts.json passes the guard", func(t *testing.T) {
		_, out := run(t, true)
		if strings.Contains(out, "not covered by the signed checksums") {
			t.Fatalf("guard wrongly refused a LISTED (signed) artifacts.json:\n%s", out)
		}
		// The guard did not fire → execution proceeds to the root check, which fails (non-root).
		if !strings.Contains(out, "must be run as root") {
			t.Fatalf("expected execution to reach the root check past the guard; got:\n%s", out)
		}
	})
}
