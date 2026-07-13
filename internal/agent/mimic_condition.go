package agent

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"time"

	"github.com/kunorikiku/yet-another-overlay-generator/internal/model"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/runtimecontract"
)

// mimic_condition.go reads the mimic-provisioning breadcrumb install.sh writes
// (model.MimicBreadcrumbPath) and turns it into the structured `mimic` Node Condition (plan-5). The
// breadcrumb's outcome is a CLOSED enum (model.MimicOutcome*); classifyMimic maps it to a curated
// reason + a SHORT fixed message — NEVER a raw error string (the curation invariant). A node with no
// tcp (mimic) link never writes the breadcrumb → ENOENT → no condition (the absence is the zero
// value, not an error).

// mimicReason* are the closed set of CamelCase reason codes returned by classifyMimic — the curated
// classification of install.sh's breadcrumb outcome (plain string constants; classifyMimic returns
// string, matching the runtimecontract.ConditionStatus*/ConditionType* idiom).
const (
	mimicReasonActive            = "Active"
	mimicReasonKernelTooOld      = "KernelTooOld"
	mimicReasonEbpfLoadFailed    = "EbpfLoadFailed"
	mimicReasonInstallFailed     = "InstallFailed"
	mimicReasonFellBackToUDP     = "FellBackToUDP"
	mimicReasonEgressUnresolved  = "EgressUnresolved"
	mimicReasonNativeDowngraded  = "NativeDowngradedSkb"
	mimicReasonModuleUnavailable = "ModuleUnavailable"
	mimicReasonStopped           = "Stopped"
	mimicReasonUnknown           = "Unknown"
)

// mimicIsActiveTimeout bounds the `systemctl is-active` probe so a wedged systemctl can NEVER stall the
// telemetry heartbeat (mirrors wgShowTimeout). Generous: is-active returns in milliseconds.
const mimicIsActiveTimeout = 5 * time.Second

// mimicUnitActiveFn reports whether the mimic@<egress> unit is active NOW, under a bounded timeout,
// indirected so a test injects the state without systemd. False on ANY error (systemctl absent, not
// root, timeout, or the unit inactive/failed) — best-effort, mirroring wgShowFn.
var mimicUnitActiveFn = func(egress string) bool {
	if egress == "" {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), mimicIsActiveTimeout)
	defer cancel()
	// `systemctl is-active --quiet` exits 0 iff the unit is active; we only read the exit code.
	return exec.CommandContext(ctx, "systemctl", "is-active", "--quiet", "mimic@"+egress+".service").Run() == nil
}

// expectsMimicRunning reports whether a breadcrumb OUTCOME implies mimic@ should be RUNNING (so a
// not-active unit is a live-down discrepancy). Active + native-downgraded (still active, just skb)
// expect it up; every fallback/failure outcome does not (the unit is intentionally absent there).
func expectsMimicRunning(outcome string) bool {
	return outcome == model.MimicOutcomeActive || outcome == model.MimicOutcomeNativeDowngraded
}

// mimicBreadcrumb is the on-disk JSON install.sh writes. Only the closed outcome token is trusted;
// egress/ts are advisory (ts seeds the condition's Since when parseable; egress is never interpolated).
type mimicBreadcrumb struct {
	Outcome string `json:"outcome"`
	Egress  string `json:"egress"`
	TS      string `json:"ts"`
}

// classifyMimic maps a breadcrumb outcome token (model.MimicOutcome*) to a Condition reason, status,
// and a SHORT curated message (a single fixed line, never raw stderr). Active → ok; every failure /
// fallback → warn (a UDP fallback de-cloaks the link and must surface loudly). An unrecognized token
// maps to a generic warn so a future script value never crashes an old agent. The 160-rune cap is NOT
// applied here — readMimicCondition routes the message through classify() (the single cap chokepoint).
func classifyMimic(outcome string) (reason, status, message string) {
	switch outcome {
	case model.MimicOutcomeActive:
		return mimicReasonActive, runtimecontract.ConditionStatusOK, "mimic TCP-shaping active"
	case model.MimicOutcomeKernelTooOld:
		return mimicReasonKernelTooOld, runtimecontract.ConditionStatusWarn, "Mimic unavailable: kernel lacks eBPF"
	case model.MimicOutcomeEbpfLoad:
		return mimicReasonEbpfLoadFailed, runtimecontract.ConditionStatusWarn, "Mimic eBPF load failed"
	case model.MimicOutcomeInstallFailed:
		return mimicReasonInstallFailed, runtimecontract.ConditionStatusWarn, "Mimic install failed"
	case model.MimicOutcomeFellBackToUDP:
		return mimicReasonFellBackToUDP, runtimecontract.ConditionStatusWarn, "Mimic: fell back to plain UDP"
	case model.MimicOutcomeEgressUnresolved:
		return mimicReasonEgressUnresolved, runtimecontract.ConditionStatusWarn, "Mimic: no routable egress IP"
	case model.MimicOutcomeNativeDowngraded:
		// mimic IS active (skb mode) — not a degradation of function, only of the requested XDP mode;
		// OK status (the link works) with a distinct reason so the operator sees native did not take.
		return mimicReasonNativeDowngraded, runtimecontract.ConditionStatusOK, "Mimic active (skb; native XDP unsupported on this NIC)"
	case model.MimicOutcomeModuleUnavailable:
		// The .deb installed but the DKMS kernel module isn't built/loadable for the running kernel
		// (e.g. a stale kernel whose linux-headers were pruned). mimic can't run; honor-policy handled
		// it in install.sh (udp degraded / none failed closed) — surface WHY with a reboot hint.
		return mimicReasonModuleUnavailable, runtimecontract.ConditionStatusWarn, "Mimic: kernel module unavailable (reboot into the current kernel, or set mimic_fallback=udp)"
	default:
		return mimicReasonUnknown, runtimecontract.ConditionStatusWarn, "Mimic status unrecognized"
	}
}

// readMimicCondition reads the breadcrumb at path and returns the `mimic` runtimecontract.Condition the report
// should carry, or (runtimecontract.Condition{}, false) when the breadcrumb is absent (no tcp link / never
// provisioned). A malformed/garbled breadcrumb yields a generic warn condition (not a crash). The
// Condition is built via classify() (plan-1) so the message is capped at runtimecontract.ConditionMessageMax at
// the single chokepoint; Since is the breadcrumb ts when parseable, else now. path is injectable so
// tests never touch /var/lib.
func readMimicCondition(path string, now time.Time) (runtimecontract.Condition, bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return runtimecontract.Condition{}, false // ENOENT (no mimic link) or unreadable → no condition
	}
	var bc mimicBreadcrumb
	// A garbled breadcrumb is classified as an unknown outcome (generic warn), never a crash.
	_ = json.Unmarshal(data, &bc)
	reason, status, message := classifyMimic(bc.Outcome)
	// Live reconcile (the breadcrumb is a DEPLOY-TIME outcome): if it says mimic should be RUNNING but
	// the mimic@<egress> unit is not active NOW (stopped/failed/crashed at runtime), report the
	// live-down state instead of a stale "active" — so `systemctl stop mimic@` or a runtime crash
	// surfaces in the panel rather than a frozen apply-time snapshot.
	if bc.Egress != "" && expectsMimicRunning(bc.Outcome) && !mimicUnitActiveFn(bc.Egress) {
		reason, status, message = mimicReasonStopped, runtimecontract.ConditionStatusWarn, "Mimic unit not running (was active at deploy)"
	}
	since := now
	if bc.TS != "" {
		if t, perr := time.Parse(time.RFC3339, bc.TS); perr == nil {
			since = t
		}
	}
	return classify(runtimecontract.ConditionTypeMimic, status, reason, message, since), true
}
