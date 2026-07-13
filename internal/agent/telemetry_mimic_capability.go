package agent

import (
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/kunorikiku/yet-another-overlay-generator/internal/runtimecontract"
)

// mimicCapabilityMetricKey carries the PRE-DEPLOY "can this node run mimic" heuristic (plan-3 of
// mimic-runtime-reliability): the panel warns BEFORE an operator sets transport=tcp on a node whose
// kernel cannot build the mimic DKMS module — the exact stale-kernel case (linux-headers-<kernel>
// pruned from the repo → the module never builds → mimic exit 22) the live fleet hit. Node-global
// (the module is not per-interface), so it is unaffected by the per-node egress override.
const mimicCapabilityMetricKey = "mimic_capability"

// mimicCapability classifies whether the mimic kernel module is (or can be) usable on this node, over
// PURE filesystem reads (no shell, no dkms build, no modprobe — inspection only; the DEFINITIVE answer
// stays the deploy-time `mimic` Node Condition):
//
//	"ready"       — the mimic module is loaded, or already built for the running kernel
//	"buildable"   — not built yet, but kernel headers ARE installed so DKMS can build it
//	"unbuildable" — not built AND kernel headers for the running kernel are absent (a stale kernel whose
//	                headers were pruned from the repo → mimic will not run without a reboot / headers)
//
// A kernel that cannot be read at all yields NO metric (the panel shows nothing), never a false class.
type mimicCapability struct {
	Capability string `json:"capability"`
	Kernel     string `json:"kernel"`
}

// Probe readers, indirected so a test injects fixtures without a real /proc or /lib/modules. Best-effort.
var (
	procModulesFn = func() ([]byte, error) { return os.ReadFile("/proc/modules") }
	// moduleBuiltFn reports whether a mimic .ko is present for the kernel (a dkms `updates/dkms` build or
	// the distro `kernel/` tree), read from modules.dep — which lists every installed module's path.
	moduleBuiltFn = func(kernel string) bool {
		dep, err := os.ReadFile(filepath.Join("/lib/modules", kernel, "modules.dep"))
		if err != nil {
			return false
		}
		return strings.Contains(string(dep), "mimic.ko")
	}
	// headersPresentFn reports whether kernel headers for `kernel` are installed — /lib/modules/<k>/build
	// is the standard symlink linux-headers-<k> creates; if it resolves, DKMS can build against it.
	headersPresentFn = func(kernel string) bool {
		_, err := os.Stat(filepath.Join("/lib/modules", kernel, "build"))
		return err == nil
	}
)

// mimicCapabilitySampler emits metrics["mimic_capability"] via PURE filesystem reads (no shell, no
// build). Best-effort + self-contained, mirroring nativeXDPSampler: nil on a kernel-read failure so a
// non-Linux / probe-less host contributes nothing.
type mimicCapabilitySampler struct{}

func (mimicCapabilitySampler) Name() string { return "mimic-capability" }

func (mimicCapabilitySampler) Sample(_ time.Time) ([]runtimecontract.Condition, map[string]any) {
	kRaw, kerr := kernelReleaseFn()
	if kerr != nil {
		return nil, nil // no /proc/sys/kernel/osrelease (non-Linux) → no signal
	}
	kernel := strings.TrimSpace(string(kRaw))
	if kernel == "" {
		return nil, nil
	}
	loaded := false
	if mods, merr := procModulesFn(); merr == nil {
		loaded = mimicModuleLoaded(mods)
	}
	capability := classifyMimicCapability(loaded, moduleBuiltFn(kernel), headersPresentFn(kernel))
	return nil, map[string]any{mimicCapabilityMetricKey: mimicCapability{Capability: capability, Kernel: kernel}}
}

// mimicModuleLoaded reports whether /proc/modules lists the mimic module (the first field of each line
// is the module name).
func mimicModuleLoaded(procModules []byte) bool {
	for _, line := range strings.Split(string(procModules), "\n") {
		if name, _, ok := strings.Cut(line, " "); ok && name == "mimic" {
			return true
		}
	}
	return false
}

// classifyMimicCapability maps the three filesystem signals to a capability class (see mimicCapability).
// Loaded or built ⇒ ready; else headers present ⇒ buildable; else unbuildable (the stale-kernel case).
func classifyMimicCapability(loaded, built, headers bool) string {
	switch {
	case loaded || built:
		return "ready"
	case headers:
		return "buildable"
	default:
		return "unbuildable"
	}
}
