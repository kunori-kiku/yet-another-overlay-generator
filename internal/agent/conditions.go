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

// configApplyCondition mirrors the existing State.Health with no behavior change (plan-1):
//   - success -> status ok,   reason "Applied",                 message "configuration applied"
//   - failure -> status warn, reason "DegradedKeepingLastGood", message "keeping last-good configuration"
//
// Every detail string is a Go-emitted constant — never prev.LastError / err.Error() (the curation
// invariant).
func configApplyCondition(ok bool, now time.Time) model.Condition {
	if ok {
		return classify(model.ConditionTypeConfigApply, model.ConditionStatusOK,
			"Applied", "configuration applied", now)
	}
	return classify(model.ConditionTypeConfigApply, model.ConditionStatusWarn,
		"DegradedKeepingLastGood", "keeping last-good configuration", now)
}

// collectConditions builds the per-cycle condition set the agent reports about itself. It is the
// SINGLE funnel every emitter goes through (plan-1 configapply, plan-3 selfupdate + wireguard,
// plan-5 mimic), so the curation + message-cap invariant lives in one place (classify). prev is the
// PRIOR persisted State (recordSuccess/recordFailure pass it): the configapply condition reflects the
// apply outcome (ok), the selfupdate condition is derived from prev (whose Health still holds a
// terminal marker the new state resets — see selfUpdateCondition), and the wireguard condition is a
// best-effort `wg show` sample that yields nothing on a probe error (never fails a cycle).
func collectConditions(prev *State, ok bool, now time.Time) []model.Condition {
	conds := []model.Condition{configApplyCondition(ok, now)}
	if c, has := selfUpdateCondition(prev, now); has {
		conds = append(conds, c)
	}
	if c, has := sampleWireGuardCondition(now); has {
		conds = append(conds, c)
	}
	if c, has := readMimicCondition(mimicBreadcrumbPath, now); has {
		conds = append(conds, c)
	}
	return conds
}

// mimicBreadcrumbPath is where the agent reads install.sh's mimic-provisioning breadcrumb (plan-5).
// Indirected through a package var so tests inject a fixture path without touching /var/lib; the
// production default is model.MimicBreadcrumbPath (the same path install.sh writes).
var mimicBreadcrumbPath = model.MimicBreadcrumbPath
