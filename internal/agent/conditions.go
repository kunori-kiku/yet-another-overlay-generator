package agent

import (
	"time"

	"github.com/kunorikiku/yet-another-overlay-generator/internal/model"
)

// conditions.go is the agent-side condition collector (plan-1). It turns the per-cycle apply
// outcome into the structured model.Condition set the agent reports about itself. The single
// classify() chokepoint enforces the curation + message-cap invariant so every emitter (plan-1
// configapply, plan-3 selfupdate/wireguard, plan-5 mimic) funnels through one place.

// classify maps a (type, status, reason, detail) tuple into a curated model.Condition with a
// SINGLE, length-capped Message — never a raw multi-line stderr dump (the curation invariant, HIGH
// for this subject). It is the one chokepoint every condition emitter funnels through, so the
// curation + message-cap invariant is enforced in ONE place. detail is the human line the caller
// already curated (e.g. "kernel lacks eBPF"); classify never reads err.Error() itself — it caps and
// stamps what it is given.
func classify(condType, status, reason, detail string, since time.Time) model.Condition {
	msg := detail
	if r := []rune(msg); len(r) > model.ConditionMessageMax {
		msg = string(r[:model.ConditionMessageMax])
	}
	return model.Condition{
		Type:    condType,
		Status:  status,
		Reason:  reason,
		Message: msg,
		Since:   since.UTC().Format(time.RFC3339),
	}
}

// collectConditions builds the per-cycle condition set from the apply outcome. plan-1 wires exactly
// ONE condition — configapply — that MIRRORS the existing State.Health with no behavior change:
//   - success -> status ok,   reason "Applied",                 message "configuration applied"
//   - failure -> status warn, reason "DegradedKeepingLastGood", message "keeping last-good configuration"
//
// plan-3/plan-5 extend this to append selfupdate/wireguard/mimic conditions; the signature stays.
// Every detail string is a Go-emitted constant — never prev.LastError / err.Error() (the curation
// invariant): plan-1 carries no upstream text at all; plan-5 (mimic) is the first to classify a
// failure category, and even then passes a curated category string, never the raw dump.
func collectConditions(ok bool, now time.Time) []model.Condition {
	if ok {
		return []model.Condition{
			classify(model.ConditionTypeConfigApply, model.ConditionStatusOK,
				"Applied", "configuration applied", now),
		}
	}
	return []model.Condition{
		classify(model.ConditionTypeConfigApply, model.ConditionStatusWarn,
			"DegradedKeepingLastGood", "keeping last-good configuration", now),
	}
}
