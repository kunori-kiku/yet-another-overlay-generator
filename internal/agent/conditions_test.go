package agent

import (
	"strings"
	"testing"
	"time"

	"github.com/kunorikiku/yet-another-overlay-generator/internal/runtimecontract"
)

// TestConfigApplyCondition_MirrorsHealth pins plan-1's contract: the configapply condition mirrors
// State.Health (ok→Applied, failure→DegradedKeepingLastGood) with no behavior change.
func TestConfigApplyCondition_MirrorsHealth(t *testing.T) {
	now := time.Date(2026, 6, 22, 10, 0, 0, 0, time.UTC)

	ok := configApplyCondition(true, now)
	if ok.Type != runtimecontract.ConditionTypeConfigApply || ok.Status != runtimecontract.ConditionStatusOK ||
		ok.Reason != "Applied" || ok.Message != "configuration applied" {
		t.Fatalf("success condition = %+v, want configapply/ok/Applied", ok)
	}
	if ok.Since != now.Format(time.RFC3339) {
		t.Fatalf("Since = %q, want %q", ok.Since, now.Format(time.RFC3339))
	}

	bad := configApplyCondition(false, now)
	if bad.Type != runtimecontract.ConditionTypeConfigApply || bad.Status != runtimecontract.ConditionStatusWarn ||
		bad.Reason != "DegradedKeepingLastGood" || bad.Message != "keeping last-good configuration" {
		t.Fatalf("failure condition = %+v, want configapply/warn/DegradedKeepingLastGood", bad)
	}
}

// TestClassify_CapsMessage pins the curation invariant: classify truncates Message to
// ConditionMessageMax runes so a multi-line / oversize detail can never leak through as a tooltip.
func TestClassify_CapsMessage(t *testing.T) {
	long := strings.Repeat("x", runtimecontract.ConditionMessageMax+50)
	c := classify(runtimecontract.ConditionTypeMimic, runtimecontract.ConditionStatusError, "Probe", long, time.Now())
	if n := len([]rune(c.Message)); n != runtimecontract.ConditionMessageMax {
		t.Fatalf("capped message length = %d, want %d", n, runtimecontract.ConditionMessageMax)
	}

	short := "kernel lacks eBPF"
	c2 := classify(runtimecontract.ConditionTypeMimic, runtimecontract.ConditionStatusWarn, "KernelTooOld", short, time.Now())
	if c2.Message != short {
		t.Fatalf("short message altered: got %q, want %q", c2.Message, short)
	}
}
