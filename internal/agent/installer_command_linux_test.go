//go:build linux

package agent

import (
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kunorikiku/yet-another-overlay-generator/internal/runtimecontract"
)

func TestInstallerGuardianForwardsArgumentAndExitStatusWithoutLeaseLeak(t *testing.T) {
	stateDir := t.TempDir()
	lease, err := acquireStateLease(stateDir)
	if err != nil {
		t.Fatalf("acquire state lease: %v", err)
	}
	defer func() { _ = lease.release() }()

	base := t.TempDir()
	// Shell metacharacters in the verified path must remain an opaque positional
	// argument to the constant guardian command.
	staging := filepath.Join(base, "stage; touch INJECTED; #")
	if err := os.Mkdir(staging, 0700); err != nil {
		t.Fatalf("mkdir staging: %v", err)
	}
	record := filepath.Join(base, "arg")
	capabilityRecord := filepath.Join(base, "capability")
	leaked := filepath.Join(base, "lease-leaked")
	t.Setenv("YAOG_GUARDIAN_ARG", record)
	t.Setenv("YAOG_GUARDIAN_CAPABILITY", capabilityRecord)
	t.Setenv("YAOG_GUARDIAN_LEAK", leaked)
	// The launcher must replace, rather than append ambiguously beside, an inherited value.
	t.Setenv(runtimecontract.InstallerCapabilityTelemetryPolicyV1Env, "spoofed")
	script := `#!/usr/bin/env bash
set -eu
printf '%s' "${1:-}" > "$YAOG_GUARDIAN_ARG"
printf '%s' "$YAOG_AGENT_CAP_TELEMETRY_POLICY_V1" > "$YAOG_GUARDIAN_CAPABILITY"
if target="$(readlink /proc/$$/fd/3 2>/dev/null)" && [[ "$target" == */.run.lock ]]; then
  : > "$YAOG_GUARDIAN_LEAK"
fi
exit 42
`
	if err := os.WriteFile(filepath.Join(staging, "install.sh"), []byte(script), 0755); err != nil {
		t.Fatalf("write installer: %v", err)
	}

	err = apply(&Config{
		InstallArgs: []string{"--uninstall"},
		Stdout:      io.Discard,
		Stderr:      io.Discard,
	}, staging, lease)
	if err == nil || !strings.Contains(err.Error(), "exit status 42") {
		t.Fatalf("guardian error = %v, want exact installer exit status 42", err)
	}
	arg, readErr := os.ReadFile(record)
	if readErr != nil || string(arg) != "--uninstall" {
		t.Fatalf("installer argument = %q, %v; want exact --uninstall", arg, readErr)
	}
	capability, readErr := os.ReadFile(capabilityRecord)
	if readErr != nil || string(capability) != "1" {
		t.Fatalf("installer capability = %q, %v; want launcher-owned value 1", capability, readErr)
	}
	if _, err := os.Stat(filepath.Join(staging, "INJECTED")); !os.IsNotExist(err) {
		t.Fatalf("shell metacharacters in installer path were evaluated: %v", err)
	}
	if _, err := os.Stat(leaked); !os.IsNotExist(err) {
		t.Fatalf("state lease descriptor leaked into real installer: %v", err)
	}
}

func TestInstallerLeaseSurvivesGoParentDeath(t *testing.T) {
	for _, mode := range []string{"direct", "already-held"} {
		t.Run(mode, func(t *testing.T) {
			stateDir := t.TempDir()
			markers := t.TempDir()
			started := filepath.Join(markers, "started")
			release := filepath.Join(markers, "release")
			done := filepath.Join(markers, "done")

			cmd := exec.Command(os.Args[0], "-test.run=^TestInstallerLeaseProcessHelper$")
			cmd.Env = append(os.Environ(),
				"YAOG_INSTALLER_LEASE_HELPER="+mode,
				"YAOG_INSTALLER_LEASE_STATE="+stateDir,
				"YAOG_INSTALLER_LEASE_STARTED="+started,
				"YAOG_INSTALLER_LEASE_RELEASE="+release,
				"YAOG_INSTALLER_LEASE_DONE="+done,
			)
			if err := cmd.Start(); err != nil {
				t.Fatalf("start helper: %v", err)
			}
			// Always release a surviving guardian, including after a failed assertion.
			defer func() {
				_ = os.WriteFile(release, []byte("release"), 0600)
				_ = cmd.Process.Kill()
				_, _ = cmd.Process.Wait()
			}()

			waitForInstallerMarker(t, started, 10*time.Second)
			if err := cmd.Process.Kill(); err != nil {
				t.Fatalf("kill Go parent: %v", err)
			}
			if err := cmd.Wait(); err == nil {
				t.Fatal("killed Go parent exited successfully")
			}

			if competing, err := acquireStateLease(stateDir); err == nil {
				_ = competing.release()
				t.Fatal("restarted operation acquired the lease while orphaned install.sh was still running")
			} else if !strings.Contains(err.Error(), "state directory is busy") {
				t.Fatalf("competing lease error = %v, want busy refusal", err)
			}

			if err := os.WriteFile(release, []byte("release"), 0600); err != nil {
				t.Fatalf("release installer: %v", err)
			}
			waitForInstallerMarker(t, done, 10*time.Second)
			deadline := time.Now().Add(10 * time.Second)
			for {
				lease, err := acquireStateLease(stateDir)
				if err == nil {
					_ = lease.release()
					break
				}
				if time.Now().After(deadline) {
					t.Fatalf("lease was not released after installer exit: %v", err)
				}
				time.Sleep(10 * time.Millisecond)
			}
		})
	}
}

func TestInstallerLeaseProcessHelper(t *testing.T) {
	mode := os.Getenv("YAOG_INSTALLER_LEASE_HELPER")
	if mode == "" {
		return
	}
	b := newSignedBundle(t, "2026-07-16T12:00:00Z")
	b.files["install.sh"] = []byte(`#!/usr/bin/env bash
set -eu
: > "$YAOG_INSTALLER_LEASE_STARTED"
while [ ! -e "$YAOG_INSTALLER_LEASE_RELEASE" ]; do sleep 0.02; done
: > "$YAOG_INSTALLER_LEASE_DONE"
`)
	resign(t, b)
	cfg := &Config{
		NodeID:       "alpha",
		Source:       staticBundleSource(b.files),
		PinnedPubPEM: b.pubPEM,
		StateDir:     os.Getenv("YAOG_INSTALLER_LEASE_STATE"),
		Stdout:       io.Discard,
		Stderr:       io.Discard,
	}
	var err error
	switch mode {
	case "direct":
		_, err = Run(cfg)
	case "already-held":
		lease, leaseErr := acquireStateLease(cfg.StateDir)
		if leaseErr != nil {
			t.Fatalf("helper acquire state lease: %v", leaseErr)
		}
		defer func() { _ = lease.release() }()
		_, err = run(cfg, lease)
	default:
		t.Fatalf("unknown helper mode %q", mode)
	}
	if err != nil {
		t.Fatalf("helper run: %v", err)
	}
}

func waitForInstallerMarker(t *testing.T, path string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		if _, err := os.Stat(path); err == nil {
			return
		} else if !os.IsNotExist(err) {
			t.Fatalf("stat marker %s: %v", path, err)
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for marker %s", path)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
