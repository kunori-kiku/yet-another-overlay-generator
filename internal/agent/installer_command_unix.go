//go:build !windows

package agent

import (
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/kunorikiku/yet-another-overlay-generator/internal/telemetrycap"
)

// installerGuardianScript keeps the inherited state lock in this outer bash
// while the exact resolved bash path passed as $0 executes install.sh. The inner command explicitly closes
// fd 3 so long-lived commands started by the installer cannot accidentally keep
// the lease after the installer itself exits. The statements after the inner
// bash also prevent a shell last-command exec optimisation from replacing the
// guardian and applying the close redirection to its only lease descriptor.
const installerGuardianScript = `"$0" "$@" 3>&-; rc=$?; exit "$rc"`

func validateInstallerPlatform() error { return nil }

func newInstallerCommand(lease *stateLease, scriptPath string, installArgs []string) (*exec.Cmd, error) {
	if lease == nil || lease.file == nil {
		return nil, fmt.Errorf("agent: install.sh requires a live state lease")
	}
	bashPath, err := exec.LookPath("bash")
	if err != nil {
		return nil, fmt.Errorf("agent: locate bash for install.sh: %w", err)
	}
	args := []string{"-c", installerGuardianScript, bashPath, scriptPath}
	args = append(args, installArgs...)
	cmd := exec.Command(bashPath, args...)
	cmd.Env = installerCommandEnvironment(os.Environ())
	// ExtraFiles[0] becomes fd 3 and refers to the same open file description as
	// the parent's flock. flock ownership survives fork/exec and is released only
	// after both the parent and guardian descriptors are closed.
	cmd.ExtraFiles = []*os.File{lease.file}
	return cmd, nil
}

// installerCommandEnvironment supplies only capabilities implemented by this agent binary. It
// removes any inherited value first so a service-manager/user environment cannot spoof or suppress
// the marker through duplicate environment entries. Bash startup hooks and exported functions are
// also removed: otherwise BASH_ENV or an imported `bash` function could restore an unsupported
// marker after this sanitizer and bypass the installer's fail-before-mutation capability gate.
func installerCommandEnvironment(base []string) []string {
	known := telemetrycap.InstallerEnvironments()
	out := make([]string, 0, len(base)+2)
	for _, entry := range base {
		if strings.HasPrefix(entry, "BASH_ENV=") || strings.HasPrefix(entry, "BASH_FUNC_") {
			continue
		}
		remove := false
		for _, name := range known {
			if strings.HasPrefix(entry, name+"=") {
				remove = true
				break
			}
		}
		if remove {
			continue
		}
		out = append(out, entry)
	}
	implemented := append([]string{telemetrycap.PolicyV1}, implementedSuccessorTelemetryCapabilities...)
	for _, capability := range implemented {
		definition, ok := telemetrycap.Lookup(capability)
		if !ok {
			panic("agent: implemented telemetry capability has no launcher contract: " + capability)
		}
		out = append(out, definition.InstallerEnvironment+"=1")
	}
	return out
}
