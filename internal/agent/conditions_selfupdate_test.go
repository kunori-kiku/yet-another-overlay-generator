package agent

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/kunorikiku/yet-another-overlay-generator/internal/model"
)

// TestClassifySelfUpdateBlock pins the deferral-error → curated-reason mapping the panel surfaces:
// the common fleet case (a target/pin mismatch — the user's live "self-test version … != desired"
// log) maps to a re-arm-the-pins hint; an in-flight error returns "" (the Active condition owns it);
// nil returns "". Every non-empty reason is a curated one-liner (never the raw error / multi-line).
func TestClassifySelfUpdateBlock(t *testing.T) {
	cases := []struct {
		name      string
		err       error
		wantEmpty bool
		wantHint  string // a substring the curated reason must contain (when non-empty)
	}{
		{"nil", nil, true, ""},
		{"in-flight is not blocked", errors.New("self-update to v3 already in flight; awaiting restart"), true, ""},
		{"version mismatch (the live case)", errors.New(`self-test version "v2.0.0-beta.9" != desired "v2.0.0-beta.10"; refusing`), false, "re-arm"},
		{"hash mismatch", errors.New("self-update hash mismatch for linux-amd64: got x, want y (refusing)"), false, "re-arm"},
		{"self-test run failure", errors.New("self-test of new binary failed: exit 1"), false, "self-test"},
		{"no pin for arch", errors.New(`no signed self-update pin for "linux-arm64"`), false, "architecture"},
		{"unsupported arch", errors.New(`self-update unsupported on arch "riscv64"`), false, "architecture"},
		{"download failure", errors.New("download https://x/y: connection refused"), false, "download"},
		{"unknown", errors.New("something odd happened"), false, "journalctl"},
	}
	for _, tc := range cases {
		got := classifySelfUpdateBlock(tc.err)
		if tc.wantEmpty {
			if got != "" {
				t.Errorf("%s: classifySelfUpdateBlock = %q, want \"\"", tc.name, got)
			}
			continue
		}
		if got == "" {
			t.Errorf("%s: classifySelfUpdateBlock = \"\", want a curated reason", tc.name)
			continue
		}
		if !strings.Contains(got, tc.wantHint) {
			t.Errorf("%s: reason %q does not contain %q", tc.name, got, tc.wantHint)
		}
		if strings.Contains(got, "\n") {
			t.Errorf("%s: reason is not a one-liner: %q", tc.name, got)
		}
	}
}

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
		// Blocked: a stalled rollout (pin/version mismatch) surfaces so the panel shows WHY a node lags.
		{"blocked (deferred refusal)", &State{Health: "applied", SelfUpdateBlocked: "the fetched agent binary does not match the rollout target version"},
			true, reasonSelfUpdateBlocked, model.ConditionStatusWarn},
		// Precedence: Blocked is LOWEST — an in-flight swap or a durable abandonment outranks it.
		{"in-flight beats blocked", &State{PendingUpdate: &PendingUpdate{To: "v3"}, SelfUpdateBlocked: "mismatch"},
			true, reasonSelfUpdateActive, model.ConditionStatusWarn},
		{"abandoned beats blocked", &State{AbandonedAgentVersion: "v2", SelfUpdateBlocked: "mismatch"},
			true, reasonSelfUpdateAbandoned, model.ConditionStatusWarn},
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
