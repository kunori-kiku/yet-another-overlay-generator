//go:build windows

package agent

import (
	"fmt"
	"os/exec"
)

// LockFileEx byte-range locks belong to the locking process. Inheriting or
// duplicating the file handle into bash does not transfer the parent's lock, so
// a parent crash would reopen the same concurrent-apply window this boundary is
// meant to close. Controller node application is Linux-only; keep verification
// and the other portable agent commands available, but refuse root application
// on Windows rather than advertise serialization the OS cannot provide here.
func validateInstallerPlatform() error {
	return fmt.Errorf("agent: install.sh execution is unsupported on Windows; apply the bundle on a supported Linux node (kit verify remains available)")
}

func newInstallerCommand(_ *stateLease, _ string, _ []string) (*exec.Cmd, error) {
	return nil, validateInstallerPlatform()
}
