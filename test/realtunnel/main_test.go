//go:build linux && integration

package realtunnel

import (
	"fmt"
	"os"
	"strings"
	"testing"
)

// main_test.go — TestMain (pre/post orphan sweep) + the Phase-2 nspawn-lifecycle proof. The sweep
// reclaims any of OUR objects (objectPrefix-named machines / veths / bridges / netns) left by a
// crashed prior run, so a re-run starts clean; TestMain runs it before and after the suite.

// runToken disambiguates this run's object names from a concurrent local run (CI runs one at a time).
var runToken = os.Getpid()

// machineName builds a prefixed, unique container name for a node base name (machine names may be
// long; only network devices are length-capped).
func machineName(base string) string {
	return fmt.Sprintf("%s-%s-%d", objectPrefix, base, runToken)
}

// bridgeName builds a prefixed, unique BRIDGE name that fits the 15-char network-device cap
// (objectPrefix "yrt" + "br" + a 5-digit token ⇒ ≤ "yrtbr99999", 10 chars). Still objectPrefix-
// prefixed so sweepOrphans reclaims it.
func bridgeName() string {
	return fmt.Sprintf("%sbr%d", objectPrefix, runToken%100000)
}

func TestMain(m *testing.M) {
	sweepOrphans()
	code := m.Run()
	sweepOrphans()
	os.Exit(code)
}

// sweepOrphans terminates every machine and deletes every veth/bridge/netns whose name starts with
// objectPrefix — best-effort, never fatal (it runs outside a *testing.T). It is the crash-recovery
// net: a killed run leaves containers/links behind, and the next run must not trip over them.
func sweepOrphans() {
	// Machines.
	if out, err := tryRun("machinectl", "list", "--no-legend", "--no-pager"); err == nil {
		for _, line := range strings.Split(out, "\n") {
			fields := strings.Fields(line)
			if len(fields) > 0 && strings.HasPrefix(fields[0], objectPrefix) {
				_, _ = tryRun("machinectl", "terminate", fields[0])
			}
		}
	}
	// veth / bridge links.
	if out, err := tryRun("ip", "-o", "link", "show"); err == nil {
		for _, line := range strings.Split(out, "\n") {
			// "<idx>: <name>@... : ..." — take the name token, strip any @peer suffix.
			parts := strings.SplitN(line, ":", 3)
			if len(parts) < 2 {
				continue
			}
			name := strings.TrimSpace(parts[1])
			if at := strings.Index(name, "@"); at >= 0 {
				name = name[:at]
			}
			if strings.HasPrefix(name, objectPrefix) {
				_, _ = tryRun("ip", "link", "del", name)
			}
		}
	}
	// Network namespaces.
	if out, err := tryRun("ip", "-o", "netns", "list"); err == nil {
		for _, line := range strings.Split(out, "\n") {
			fields := strings.Fields(line)
			if len(fields) > 0 && strings.HasPrefix(fields[0], objectPrefix) {
				_, _ = tryRun("ip", "netns", "del", fields[0])
			}
		}
	}
}

// TestNspawnLifecycle (Phase 2) proves the Option-B mechanism end to end with no assertions on the
// overlay yet: a container boots the base rootfs under real systemd, runs a command inside, and
// tears down cleanly. If this can't pass, no scenario can.
func TestNspawnLifecycle(t *testing.T) {
	rootfs := requireCapabilities(t)
	c := bootContainer(t, rootfs, machineName("life"), bootOpts{})

	// systemd booted (running or degraded — a minimal container legitimately has some inactive units).
	state := strings.TrimSpace(c.exec(t, "systemctl", "is-system-running", "--wait"))
	if state != "running" && state != "degraded" {
		t.Fatalf("container systemd did not boot: is-system-running=%q", state)
	}
	// The WireGuard userspace is present inside (the data-plane prerequisite).
	if out := c.exec(t, "wg", "--version"); !strings.Contains(out, "wireguard-tools") {
		t.Fatalf("wg missing inside container: %q", out)
	}
	t.Logf("nspawn lifecycle OK (systemd %s; wg present)", state)
}
