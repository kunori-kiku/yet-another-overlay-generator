//go:build !windows

package agent

import (
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/kunorikiku/yet-another-overlay-generator/internal/runtimecontract"
)

// installerGuardianScript keeps the inherited state lock in this outer bash
// while the inner bash executes install.sh. The inner command explicitly closes
// fd 3 so long-lived commands started by the installer cannot accidentally keep
// the lease after the installer itself exits. The statements after the inner
// bash also prevent a shell last-command exec optimisation from replacing the
// guardian and applying the close redirection to its only lease descriptor.
const installerGuardianScript = `bash "$@" 3>&-; rc=$?; exit "$rc"`

func validateInstallerPlatform() error { return nil }

func newInstallerCommand(lease *stateLease, scriptPath string, installArgs []string) (*exec.Cmd, error) {
	if lease == nil || lease.file == nil {
		return nil, fmt.Errorf("agent: install.sh requires a live state lease")
	}
	args := []string{"-c", installerGuardianScript, "yaog-agent-install-guardian", scriptPath}
	args = append(args, installArgs...)
	cmd := exec.Command("bash", args...)
	cmd.Env = installerCommandEnvironment(os.Environ())
	// ExtraFiles[0] becomes fd 3 and refers to the same open file description as
	// the parent's flock. flock ownership survives fork/exec and is released only
	// after both the parent and guardian descriptors are closed.
	cmd.ExtraFiles = []*os.File{lease.file}
	return cmd, nil
}

// installerCommandEnvironment supplies only capabilities implemented by this agent binary. It
// removes any inherited value first so a service-manager/user environment cannot spoof or suppress
// the marker through duplicate environment entries.
func installerCommandEnvironment(base []string) []string {
	name := runtimecontract.InstallerCapabilityTelemetryPolicyV1Env
	prefix := name + "="
	out := make([]string, 0, len(base)+1)
	for _, entry := range base {
		if strings.HasPrefix(entry, prefix) {
			continue
		}
		out = append(out, entry)
	}
	return append(out, prefix+"1")
}
