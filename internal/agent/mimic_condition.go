package agent

import (
	"encoding/json"
	"os"
	"time"

	"github.com/kunorikiku/yet-another-overlay-generator/internal/model"
)

// mimic_condition.go reads the mimic-provisioning breadcrumb install.sh writes
// (model.MimicBreadcrumbPath) and turns it into the structured `mimic` Node Condition (plan-5). The
// breadcrumb's outcome is a CLOSED enum (model.MimicOutcome*); classifyMimic maps it to a curated
// reason + a SHORT fixed message — NEVER a raw error string (the curation invariant). A node with no
// tcp (mimic) link never writes the breadcrumb → ENOENT → no condition (the absence is the zero
// value, not an error).

// mimicReason* are the closed set of CamelCase reason codes returned by classifyMimic — the curated
// classification of install.sh's breadcrumb outcome (plain string constants; classifyMimic returns
// string, matching the model.ConditionStatus*/ConditionType* idiom).
const (
	mimicReasonActive           = "Active"
	mimicReasonKernelTooOld     = "KernelTooOld"
	mimicReasonEbpfLoadFailed   = "EbpfLoadFailed"
	mimicReasonInstallFailed    = "InstallFailed"
	mimicReasonFellBackToUDP    = "FellBackToUDP"
	mimicReasonEgressUnresolved = "EgressUnresolved"
	mimicReasonUnknown          = "Unknown"
)

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
		return mimicReasonActive, model.ConditionStatusOK, "mimic TCP-shaping active"
	case model.MimicOutcomeKernelTooOld:
		return mimicReasonKernelTooOld, model.ConditionStatusWarn, "Mimic unavailable: kernel lacks eBPF"
	case model.MimicOutcomeEbpfLoad:
		return mimicReasonEbpfLoadFailed, model.ConditionStatusWarn, "Mimic eBPF load failed"
	case model.MimicOutcomeInstallFailed:
		return mimicReasonInstallFailed, model.ConditionStatusWarn, "Mimic install failed"
	case model.MimicOutcomeFellBackToUDP:
		return mimicReasonFellBackToUDP, model.ConditionStatusWarn, "Mimic: fell back to plain UDP"
	case model.MimicOutcomeEgressUnresolved:
		return mimicReasonEgressUnresolved, model.ConditionStatusWarn, "Mimic: no routable egress IP"
	default:
		return mimicReasonUnknown, model.ConditionStatusWarn, "Mimic status unrecognized"
	}
}

// readMimicCondition reads the breadcrumb at path and returns the `mimic` model.Condition the report
// should carry, or (model.Condition{}, false) when the breadcrumb is absent (no tcp link / never
// provisioned). A malformed/garbled breadcrumb yields a generic warn condition (not a crash). The
// Condition is built via classify() (plan-1) so the message is capped at model.ConditionMessageMax at
// the single chokepoint; Since is the breadcrumb ts when parseable, else now. path is injectable so
// tests never touch /var/lib.
func readMimicCondition(path string, now time.Time) (model.Condition, bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return model.Condition{}, false // ENOENT (no mimic link) or unreadable → no condition
	}
	var bc mimicBreadcrumb
	// A garbled breadcrumb is classified as an unknown outcome (generic warn), never a crash.
	_ = json.Unmarshal(data, &bc)
	reason, status, message := classifyMimic(bc.Outcome)
	since := now
	if bc.TS != "" {
		if t, perr := time.Parse(time.RFC3339, bc.TS); perr == nil {
			since = t
		}
	}
	return classify(model.ConditionTypeMimic, status, reason, message, since), true
}
