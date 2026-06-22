package agent

import (
	"strings"
	"testing"
	"time"

	"github.com/kunorikiku/yet-another-overlay-generator/internal/model"
)

// TestCollectConditions_MirrorsHealth pins plan-1's contract: the per-cycle condition set is exactly
// one configapply condition that mirrors State.Health, with no other behavior change.
func TestCollectConditions_MirrorsHealth(t *testing.T) {
	now := time.Date(2026, 6, 22, 10, 0, 0, 0, time.UTC)

	got := collectConditions(true, now)
	if len(got) != 1 {
		t.Fatalf("success: len = %d, want 1", len(got))
	}
	if c := got[0]; c.Type != model.ConditionTypeConfigApply || c.Status != model.ConditionStatusOK ||
		c.Reason != "Applied" || c.Message != "configuration applied" {
		t.Fatalf("success condition = %+v, want configapply/ok/Applied", got[0])
	}
	if got[0].Since != now.Format(time.RFC3339) {
		t.Fatalf("Since = %q, want %q", got[0].Since, now.Format(time.RFC3339))
	}

	bad := collectConditions(false, now)
	if len(bad) != 1 {
		t.Fatalf("failure: len = %d, want 1", len(bad))
	}
	if c := bad[0]; c.Type != model.ConditionTypeConfigApply || c.Status != model.ConditionStatusWarn ||
		c.Reason != "DegradedKeepingLastGood" || c.Message != "keeping last-good configuration" {
		t.Fatalf("failure condition = %+v, want configapply/warn/DegradedKeepingLastGood", bad[0])
	}
}

// TestClassify_CapsMessage pins the curation invariant (outline D5): classify truncates Message to
// ConditionMessageMax runes so a multi-line / oversize detail can never leak through as a tooltip.
func TestClassify_CapsMessage(t *testing.T) {
	long := strings.Repeat("x", model.ConditionMessageMax+50)
	c := classify(model.ConditionTypeMimic, model.ConditionStatusError, "Probe", long, time.Now())
	if n := len([]rune(c.Message)); n != model.ConditionMessageMax {
		t.Fatalf("capped message length = %d, want %d", n, model.ConditionMessageMax)
	}

	short := "kernel lacks eBPF"
	c2 := classify(model.ConditionTypeMimic, model.ConditionStatusWarn, "KernelTooOld", short, time.Now())
	if c2.Message != short {
		t.Fatalf("short message altered: got %q, want %q", c2.Message, short)
	}
}
