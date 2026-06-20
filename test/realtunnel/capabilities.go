//go:build linux && integration

package realtunnel

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

// capabilities.go — the preflight that lets the tier t.Skip cleanly (never falsely fail) on a
// checkout lacking a prerequisite, and the shared object-naming + root command runner. Option B
// (the owner's choice) brings each node up as a real systemd-nspawn container, so the prerequisites
// are: root, systemd-nspawn, the WireGuard kernel module, and a base rootfs (REALTUNNEL_ROOTFS) that
// already carries systemd + wireguard-tools + babeld + iproute2 + iptables/nft.

// objectPrefix namespaces the host objects this tier creates directly — the machines and the underlay
// bridge — so the orphan sweep + teardown can find and remove exactly ours, never a co-tenant's.
// (systemd-nspawn's host veths inherit the machine name with a "vb-"/"ve-" prefix, and its netns is
// anonymous; both die when the machine is terminated, and sweepOrphans matches the veths as a
// crash-orphan backstop.) It is kept SHORT because network-device names are capped at 15 chars
// (IFNAMSIZ): a bridge "yrtbr<token>" must fit.
// The PID-derived token keeps concurrent local runs from colliding; CI runs one at a time.
const objectPrefix = "yrt"

// rootfsEnv names the env var pointing at the prebuilt base rootfs.
const rootfsEnv = "REALTUNNEL_ROOTFS"

// rootfsRecipe is printed on skip so a developer can build the base rootfs in one command. The
// include list must carry every tool install.sh requires (ensure_cmd: wireguard-tools, iproute2,
// openssl, iptables/nftables, babeld) because the container runs on an isolated underlay bridge with
// no internet — a missing tool cannot be apt-fetched at install time, it must be pre-baked here.
const rootfsRecipe = "sudo debootstrap --variant=minbase --components=main,universe " +
	"--include=systemd,systemd-sysv,udev,dbus,wireguard-tools,babeld,iproute2,iptables,nftables,openssl,iputils-ping,kmod " +
	"noble /tmp/yaog-rt-rootfs https://mirrors.tuna.tsinghua.edu.cn/ubuntu/ && export REALTUNNEL_ROOTFS=/tmp/yaog-rt-rootfs"

// requireCapabilities t.Skips (not fails) unless every prerequisite for the systemd-nspawn tier is
// present, with a precise message naming what is missing. Returns the resolved base-rootfs path.
func requireCapabilities(t *testing.T) string {
	t.Helper()
	if os.Geteuid() != 0 {
		t.Skip("realtunnel: requires root — run `sudo go test -tags integration ./test/realtunnel/...`")
	}
	if _, err := exec.LookPath("systemd-nspawn"); err != nil {
		t.Skip("realtunnel: systemd-nspawn not found — `sudo apt-get install systemd-container`")
	}
	if !wireguardModuleAvailable() {
		t.Skip("realtunnel: the WireGuard kernel module is unavailable (load it: `modprobe wireguard`)")
	}
	rootfs := os.Getenv(rootfsEnv)
	if rootfs == "" {
		t.Skipf("realtunnel: %s is unset — build a base rootfs:\n  %s", rootfsEnv, rootfsRecipe)
	}
	if st, err := os.Stat(rootfs); err != nil || !st.IsDir() {
		t.Skipf("realtunnel: %s=%q is not a directory — rebuild it:\n  %s", rootfsEnv, rootfs, rootfsRecipe)
	}
	if _, err := os.Stat(rootfs + "/usr/lib/systemd/systemd"); err != nil {
		t.Skipf("realtunnel: rootfs %q has no systemd (incomplete debootstrap?) — rebuild:\n  %s", rootfs, rootfsRecipe)
	}
	return rootfs
}

// wireguardModuleAvailable reports whether the WireGuard data path is usable — either the module is
// already loaded (lsmod) or modinfo can find it to be loaded on first use.
func wireguardModuleAvailable() bool {
	if data, err := os.ReadFile("/proc/modules"); err == nil && strings.Contains(string(data), "wireguard") {
		return true
	}
	return exec.Command("modinfo", "wireguard").Run() == nil
}

// run executes a command as root, returning combined output; it fails the test on a non-zero exit
// with the full output (the diagnostic the data-plane debugging depends on).
func run(t *testing.T, name string, args ...string) string {
	t.Helper()
	out, err := exec.Command(name, args...).CombinedOutput()
	if err != nil {
		t.Fatalf("command failed: %s %s\n  err: %v\n  output:\n%s", name, strings.Join(args, " "), err, out)
	}
	return string(out)
}

// tryRun executes a command best-effort (teardown / sweep paths), returning combined output and the
// error without failing the test — so a cleanup of an already-absent object is a no-op, not a fault.
func tryRun(name string, args ...string) (string, error) {
	out, err := exec.Command(name, args...).CombinedOutput()
	return string(out), err
}
