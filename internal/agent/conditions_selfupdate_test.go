package agent

import (
	"strings"
	"testing"
	"time"

	"github.com/kunorikiku/yet-another-overlay-generator/internal/model"
)

// TestSelfUpdateCondition covers each self-update lifecycle outcome the structured condition must
// mirror, the precedence between durable signals, and the "nothing to report" sentinel.
func TestSelfUpdateCondition(t *testing.T) {
	now := time.Date(2026, 6, 22, 10, 0, 0, 0, time.UTC)

	cases := []struct {
		name       string
		prev       *State
		wantHas    bool
		wantReason string
		wantStatus string
	}{
		{"nil state", nil, false, "", ""},
		{"no activity", &State{Health: "applied"}, false, "", ""},
		{"in flight (not confirmed)", &State{PendingUpdate: &PendingUpdate{To: "v2.0.0-beta.9", Attempts: 1}},
			true, reasonSelfUpdateActive, model.ConditionStatusWarn},
		{"probationary (confirmed)", &State{PendingUpdate: &PendingUpdate{To: "v2.0.0-beta.9", Confirmed: true}},
			true, reasonSelfUpdateProbationary, model.ConditionStatusWarn},
		{"abandoned (durable)", &State{AbandonedAgentVersion: "v2.0.0-beta.9"},
			true, reasonSelfUpdateAbandoned, model.ConditionStatusWarn},
		{"updated (transient Health marker)", &State{Health: "self-updated to v2.0.0-beta.9"},
			true, reasonSelfUpdateUpdated, model.ConditionStatusOK},
		// Precedence: an in-flight breadcrumb wins over a stale abandoned-target memory (operator retargeted).
		{"in-flight beats abandoned", &State{PendingUpdate: &PendingUpdate{To: "v3"}, AbandonedAgentVersion: "v2"},
			true, reasonSelfUpdateActive, model.ConditionStatusWarn},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, has := selfUpdateCondition(tc.prev, now)
			if has != tc.wantHas {
				t.Fatalf("has = %v, want %v (cond %+v)", has, tc.wantHas, got)
			}
			if !has {
				return
			}
			if got.Type != model.ConditionTypeSelfUpdate || got.Reason != tc.wantReason || got.Status != tc.wantStatus {
				t.Fatalf("got {type:%s reason:%s status:%s}, want {selfupdate %s %s}",
					got.Type, got.Reason, got.Status, tc.wantReason, tc.wantStatus)
			}
			// Curation: message is a curated one-liner, never empty for a reported condition, never
			// a multi-line dump.
			if got.Message == "" || strings.Contains(got.Message, "\n") {
				t.Fatalf("message not a curated one-liner: %q", got.Message)
			}
		})
	}
}
