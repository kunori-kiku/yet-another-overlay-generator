package agent

import (
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/kunorikiku/yet-another-overlay-generator/internal/runtimecontract"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/telemetrymetric"
)

// nativeXDPMetricKey carries the PRE-DEPLOY native-XDP capability heuristic (mimic-provisioning
// plan-4): a best-effort hint the panel reads so it can warn BEFORE an operator picks
// xdp_mode=native, rather than only learning at deploy time.
const nativeXDPMetricKey = telemetrymetric.NativeXDPKey

// nativeXDPCapability is the heuristic classification of whether this node's default-route (egress)
// NIC can attach an XDP program in DRIVER (native) mode. It is BEST-EFFORT — a driver-name + kernel
// heuristic over PURE sysfs/proc reads (no shell, no live-NIC XDP attach) — so it never over-promises
// and never mutates the NIC. The DEFINITIVE per-node answer is the deploy-time achieved-mode `mimic`
// Node Condition (plan-3's native→skb auto-downgrade); this only advises the operator up front.
//
//	"supported"   — a driver we know ships solid native XDP
//	"conditional" — virtio_net: native works only on recent kernels with enough queues (the common VPS
//	                case), so we do not promise it
//	"unsupported" — a driver we know lacks native XDP (common consumer NICs)
//	"unknown"     — an unlisted driver / no bound driver (never a false "unsupported")
type nativeXDPCapability struct {
	Capability string `json:"capability"`
	Driver     string `json:"driver"`
	Kernel     string `json:"kernel"`
}

// Probe readers, indirected so a test injects fixtures without a Linux sysfs/proc. Each is best-effort:
// a read error makes the sampler emit nothing (or "unknown"), never fail a heartbeat cycle.
var (
	procNetRouteFn  = func() ([]byte, error) { return os.ReadFile("/proc/net/route") }
	kernelReleaseFn = func() ([]byte, error) { return os.ReadFile("/proc/sys/kernel/osrelease") }
	nicDriverFn     = func(iface string) (string, error) {
		// /sys/class/net/<iface>/device/driver is a symlink to the bound driver module dir; its
		// basename is the driver name (e.g. "virtio_net", "ena"). A pure sysfs read — no shell.
		target, err := os.Readlink(filepath.Join("/sys/class/net", iface, "device", "driver"))
		if err != nil {
			return "", err
		}
		return filepath.Base(target), nil
	}
)

// nativeXDPSampler emits metrics["native_xdp"] — the egress NIC's native-XDP capability heuristic,
// via PURE sysfs/proc reads (no shell, no live-NIC XDP attach). Best-effort + self-contained, mirroring
// resourceSampler: nil on a route-read failure so a non-Linux / probe-less host contributes nothing.
type nativeXDPSampler struct{}

func (nativeXDPSampler) Name() string { return "native-xdp" }

func (nativeXDPSampler) MetricDefinitions() []telemetrymetric.Definition {
	return []telemetrymetric.Definition{telemetrymetric.NativeXDP}
}

func (nativeXDPSampler) Sample(_ time.Time) ([]runtimecontract.Condition, map[string]any) {
	routeRaw, err := procNetRouteFn()
	if err != nil {
		return nil, nil // no /proc/net/route (non-Linux / restricted) → no signal
	}
	iface := defaultRouteIface(routeRaw)
	if iface == "" {
		return nil, nil // no default route → cannot identify the egress NIC
	}
	kernel := ""
	if kRaw, kerr := kernelReleaseFn(); kerr == nil {
		kernel = strings.TrimSpace(string(kRaw))
	}
	driver, derr := nicDriverFn(iface)
	if derr != nil || driver == "" {
		// A NIC with no bound driver (or an unreadable link) → report "unknown" WITH the kernel, so the
		// operator sees an explicit "capability unknown" rather than a silently-absent signal.
		return nil, map[string]any{nativeXDPMetricKey: nativeXDPCapability{Capability: "unknown", Kernel: kernel}}
	}
	return nil, map[string]any{nativeXDPMetricKey: nativeXDPCapability{
		Capability: classifyNativeXDP(driver),
		Driver:     driver,
		Kernel:     kernel,
	}}
}

// defaultRouteIface returns the interface of the default route (hex Destination "00000000") from
// /proc/net/route, or "" if none. Column 0 is Iface, column 1 is the hex Destination; the header row
// (Iface Destination …) is skipped.
func defaultRouteIface(data []byte) string {
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 || fields[0] == "Iface" {
			continue
		}
		if fields[1] == "00000000" {
			return fields[0]
		}
	}
	return ""
}

// nativeXDPSupportedDrivers / nativeXDPUnsupportedDrivers are curated, CONSERVATIVE driver lists.
// "supported" = a driver known to ship solid native (driver-mode) XDP; "unsupported" = a common
// consumer NIC driver known to lack it. Everything unlisted is "unknown" (never a false "unsupported"
// — a wrong negative would needlessly discourage native, and plan-3's deploy-time auto-downgrade is the
// safety net regardless). virtio_net is special-cased to "conditional".
var (
	nativeXDPSupportedDrivers = map[string]bool{
		"ena": true, "mlx5_core": true, "mlx4_en": true, "i40e": true, "ixgbe": true,
		"ice": true, "igb": true, "igc": true, "bnxt_en": true, "nfp": true, "veth": true, "tun": true,
	}
	nativeXDPUnsupportedDrivers = map[string]bool{
		"e1000": true, "e1000e": true, "r8169": true, "r8168": true,
		"8139too": true, "8139cp": true, "atl1c": true, "alx": true,
	}
)

// classifyNativeXDP maps a NIC driver name to a native-XDP capability class (see nativeXDPCapability).
func classifyNativeXDP(driver string) string {
	switch {
	case nativeXDPSupportedDrivers[driver]:
		return "supported"
	case nativeXDPUnsupportedDrivers[driver]:
		return "unsupported"
	case driver == "virtio_net":
		return "conditional"
	default:
		return "unknown"
	}
}
