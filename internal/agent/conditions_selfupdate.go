package agent

import (
	"strconv"
	"strings"
	"time"

	"github.com/kunorikiku/yet-another-overlay-generator/internal/model"
)

// conditions_selfupdate.go derives the STRUCTURED self-update condition (plan-3) from the agent's
// persisted State — the replacement for the panel grepping the free-form State.Health markers
// (selfupdate.go ReconcileSelfUpdatePromote/FinalizeSelfUpdate/rollbackAndAbandon, agent.go
// recordSuccess/recordFailure). The legacy Health strings are STILL set unchanged (old controllers/
// panels parse them); this is the new source of truth the panel prefers.
//
// CUSTODY (PRINCIPLES — signed self-update): this file is READ-ONLY over State. It touches NEITHER
// the AgentVersionFloor (selfupdate.go advances it), the PendingUpdate breadcrumb, verify.go, NOR the
// swap/re-exec path. It only DESCRIBES the state the reconcile/finalize chain already wrote.

// Closed reason enum for the self-update lifecycle condition (model.ConditionTypeSelfUpdate).
const (
	reasonSelfUpdateActive       = "Active"                      // a swap is in flight (breadcrumb present, not Confirmed)
	reasonSelfUpdateProbationary = "HealthConfirmedProbationary" // passed the health gate, awaiting one clean cycle
	reasonSelfUpdateUpdated      = "Updated"                     // finalized (floor advanced; transient — one report)
	reasonSelfUpdateAbandoned    = "Abandoned"                   // rolled back at the cap / health gate (durable until retargeted)
)

// selfUpdateCondition derives the structured selfupdate condition from the PRIOR persisted State.
// It MUST be passed the PRIOR state (prev), not the freshly-rebuilt apply state: recordSuccess/
// recordFailure reset Health to "applied"/"degraded", so the terminal "self-updated to ..." marker
// only survives on prev. The bool is false when there is nothing to report (steady idle / never
// self-updated) — expressed as (model.Condition{}, false), NEVER a nil pointer. Pure (no I/O).
//
// Precedence: an in-flight breadcrumb (durable) is authoritative over the transient Health string;
// a durable AbandonedAgentVersion (preserved across applies until the operator retargets) is
// authoritative over the one-cycle "self-updated to" marker. The settled-updated state otherwise
// needs no condition — the reported agentVersion + the panel's version compare already show "applied".
func selfUpdateCondition(prev *State, now time.Time) (model.Condition, bool) {
	if prev == nil {
		return model.Condition{}, false
	}
	switch {
	case prev.PendingUpdate != nil && prev.PendingUpdate.Confirmed:
		return classify(model.ConditionTypeSelfUpdate, model.ConditionStatusWarn, reasonSelfUpdateProbationary,
			"self-update to "+prev.PendingUpdate.To+" health-confirmed; probationary until one clean cycle", now), true
	case prev.PendingUpdate != nil:
		return classify(model.ConditionTypeSelfUpdate, model.ConditionStatusWarn, reasonSelfUpdateActive,
			"self-update to "+prev.PendingUpdate.To+" in flight (attempt "+strconv.Itoa(prev.PendingUpdate.Attempts)+")", now), true
	case prev.AbandonedAgentVersion != "":
		return classify(model.ConditionTypeSelfUpdate, model.ConditionStatusWarn, reasonSelfUpdateAbandoned,
			"self-update to "+prev.AbandonedAgentVersion+" abandoned (rolled back); change the target to retry", now), true
	case strings.HasPrefix(prev.Health, "self-updated to "):
		return classify(model.ConditionTypeSelfUpdate, model.ConditionStatusOK, reasonSelfUpdateUpdated,
			"self-updated to "+strings.TrimPrefix(prev.Health, "self-updated to "), now), true
	}
	return model.Condition{}, false
}
