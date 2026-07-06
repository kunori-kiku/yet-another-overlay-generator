package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kunorikiku/yet-another-overlay-generator/internal/model"
)

// TestClassifyMimic is the curated-reason CONTRACT (perpetual): every closed outcome token maps to a
// fixed reason + status + short message (never a raw error), and an unknown token maps to a generic
// warn (no panic, no passthrough). This is the owner's "clear catch, not a lengthy duplicate."
func TestClassifyMimic(t *testing.T) {
	cases := []struct {
		outcome string
		reason  string
		status  string
	}{
		{model.MimicOutcomeActive, mimicReasonActive, model.ConditionStatusOK},
		{model.MimicOutcomeKernelTooOld, mimicReasonKernelTooOld, model.ConditionStatusWarn},
		{model.MimicOutcomeEbpfLoad, mimicReasonEbpfLoadFailed, model.ConditionStatusWarn},
		{model.MimicOutcomeInstallFailed, mimicReasonInstallFailed, model.ConditionStatusWarn},
		{model.MimicOutcomeFellBackToUDP, mimicReasonFellBackToUDP, model.ConditionStatusWarn},
		{model.MimicOutcomeEgressUnresolved, mimicReasonEgressUnresolved, model.ConditionStatusWarn},
		{model.MimicOutcomeNativeDowngraded, mimicReasonNativeDowngraded, model.ConditionStatusOK},
		{model.MimicOutcomeModuleUnavailable, mimicReasonModuleUnavailable, model.ConditionStatusWarn},
		{"some_future_token", mimicReasonUnknown, model.ConditionStatusWarn},
		{"", mimicReasonUnknown, model.ConditionStatusWarn},
	}
	for _, tc := range cases {
		reason, status, message := classifyMimic(tc.outcome)
		if reason != tc.reason || status != tc.status {
			t.Errorf("classifyMimic(%q) = (%s,%s), want (%s,%s)", tc.outcome, reason, status, tc.reason, tc.status)
		}
		// Message is a curated single line, never empty, never multi-line.
		if message == "" || strings.Contains(message, "\n") {
			t.Errorf("classifyMimic(%q) message not a curated one-liner: %q", tc.outcome, message)
		}
	}
}

// TestReadMimicCondition covers the breadcrumb read: a present file ⇒ the right mimic condition with
// the breadcrumb ts as Since and the message capped at ConditionMessageMax; an absent file ⇒
// (zero,false); malformed JSON ⇒ a generic warn (not a crash).
func TestReadMimicCondition(t *testing.T) {
	now := time.Date(2026, 6, 22, 13, 0, 0, 0, time.UTC)
	dir := t.TempDir()
	path := filepath.Join(dir, "mimic-status.json")

	// Absent ⇒ no condition.
	if c, has := readMimicCondition(path, now); has {
		t.Fatalf("absent breadcrumb must yield no condition, got %+v", c)
	}

	// Present (fell_back_to_udp) ⇒ a mimic/FellBackToUDP/warn condition with the ts as Since.
	write := func(s string) {
		if err := os.WriteFile(path, []byte(s), 0o600); err != nil {
			t.Fatalf("write breadcrumb: %v", err)
		}
	}
	write(`{"outcome":"fell_back_to_udp","egress":"eth0","ts":"2026-06-22T12:59:00Z"}`)
	c, has := readMimicCondition(path, now)
	if !has || c.Type != model.ConditionTypeMimic || c.Reason != mimicReasonFellBackToUDP || c.Status != model.ConditionStatusWarn {
		t.Fatalf("fell_back_to_udp: has=%v cond=%+v", has, c)
	}
	if c.Since != "2026-06-22T12:59:00Z" {
		t.Errorf("Since = %q, want the breadcrumb ts 2026-06-22T12:59:00Z", c.Since)
	}
	if c.Message == "" || len([]rune(c.Message)) > model.ConditionMessageMax {
		t.Errorf("message must be a curated, capped line, got %q", c.Message)
	}

	// Malformed JSON ⇒ a generic warn (Unknown), not a crash.
	write(`{not json`)
	c, has = readMimicCondition(path, now)
	if !has || c.Reason != mimicReasonUnknown || c.Status != model.ConditionStatusWarn {
		t.Fatalf("malformed breadcrumb must yield a generic warn, got has=%v cond=%+v", has, c)
	}
}

// TestCollectConditions_IncludesMimic proves the mimic producer is drained into the report set: with
// a breadcrumb fixture present (path injected), collectConditions includes the mimic condition.
func TestCollectConditions_IncludesMimic(t *testing.T) {
	now := time.Date(2026, 6, 22, 13, 0, 0, 0, time.UTC)
	dir := t.TempDir()
	path := filepath.Join(dir, "mimic-status.json")
	if err := os.WriteFile(path, []byte(`{"outcome":"active","ts":"2026-06-22T12:59:00Z"}`), 0o600); err != nil {
		t.Fatalf("write breadcrumb: %v", err)
	}
	origPath := mimicBreadcrumbPath
	t.Cleanup(func() { mimicBreadcrumbPath = origPath })
	mimicBreadcrumbPath = path

	// wgShowFn is stubbed hermetic by TestMain; selfUpdate has nothing (nil prev fields).
	got := collectConditions(&State{}, true, now)
	if _, ok := findCond(got, model.ConditionTypeMimic); !ok {
		t.Fatalf("collectConditions did not include the mimic condition: %+v", got)
	}
}
