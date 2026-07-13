package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kunorikiku/yet-another-overlay-generator/internal/model"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/runtimecontract"
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
		{model.MimicOutcomeActive, mimicReasonActive, runtimecontract.ConditionStatusOK},
		{model.MimicOutcomeKernelTooOld, mimicReasonKernelTooOld, runtimecontract.ConditionStatusWarn},
		{model.MimicOutcomeEbpfLoad, mimicReasonEbpfLoadFailed, runtimecontract.ConditionStatusWarn},
		{model.MimicOutcomeInstallFailed, mimicReasonInstallFailed, runtimecontract.ConditionStatusWarn},
		{model.MimicOutcomeFellBackToUDP, mimicReasonFellBackToUDP, runtimecontract.ConditionStatusWarn},
		{model.MimicOutcomeEgressUnresolved, mimicReasonEgressUnresolved, runtimecontract.ConditionStatusWarn},
		{model.MimicOutcomeNativeDowngraded, mimicReasonNativeDowngraded, runtimecontract.ConditionStatusOK},
		{model.MimicOutcomeModuleUnavailable, mimicReasonModuleUnavailable, runtimecontract.ConditionStatusWarn},
		{"some_future_token", mimicReasonUnknown, runtimecontract.ConditionStatusWarn},
		{"", mimicReasonUnknown, runtimecontract.ConditionStatusWarn},
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
	if !has || c.Type != runtimecontract.ConditionTypeMimic || c.Reason != mimicReasonFellBackToUDP || c.Status != runtimecontract.ConditionStatusWarn {
		t.Fatalf("fell_back_to_udp: has=%v cond=%+v", has, c)
	}
	if c.Since != "2026-06-22T12:59:00Z" {
		t.Errorf("Since = %q, want the breadcrumb ts 2026-06-22T12:59:00Z", c.Since)
	}
	if c.Message == "" || len([]rune(c.Message)) > runtimecontract.ConditionMessageMax {
		t.Errorf("message must be a curated, capped line, got %q", c.Message)
	}

	// Malformed JSON ⇒ a generic warn (Unknown), not a crash.
	write(`{not json`)
	c, has = readMimicCondition(path, now)
	if !has || c.Reason != mimicReasonUnknown || c.Status != runtimecontract.ConditionStatusWarn {
		t.Fatalf("malformed breadcrumb must yield a generic warn, got has=%v cond=%+v", has, c)
	}
}

// TestReadMimicCondition_LiveReconcile covers the rc.4 live reconcile: the mimic condition re-probes
// the mimic@<egress> unit each heartbeat, so a runtime stop/crash surfaces as a live warn instead of
// the frozen deploy-time "active". mimicUnitActiveFn is injected (no systemd).
func TestReadMimicCondition_LiveReconcile(t *testing.T) {
	orig := mimicUnitActiveFn
	defer func() { mimicUnitActiveFn = orig }()
	now := time.Now()
	path := filepath.Join(t.TempDir(), "mimic-status.json")
	write := func(outcome, egress string) {
		if err := os.WriteFile(path, []byte(`{"outcome":"`+outcome+`","egress":"`+egress+`","ts":"2026-07-07T00:00:00Z"}`), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	// active breadcrumb + unit NOT active -> live Stopped (warn).
	mimicUnitActiveFn = func(string) bool { return false }
	write("active", "eth0")
	if c, _ := readMimicCondition(path, now); c.Reason != mimicReasonStopped || c.Status != runtimecontract.ConditionStatusWarn {
		t.Errorf("active breadcrumb + stopped unit = (%s,%s), want (%s,warn)", c.Reason, c.Status, mimicReasonStopped)
	}

	// active breadcrumb + unit active -> Active (no override).
	mimicUnitActiveFn = func(string) bool { return true }
	write("active", "eth0")
	if c, _ := readMimicCondition(path, now); c.Reason != mimicReasonActive {
		t.Errorf("active breadcrumb + active unit = %s, want %s", c.Reason, mimicReasonActive)
	}

	// native_downgraded_skb + unit NOT active -> Stopped (it too expects mimic running).
	mimicUnitActiveFn = func(string) bool { return false }
	write("native_downgraded_skb", "eth0")
	if c, _ := readMimicCondition(path, now); c.Reason != mimicReasonStopped {
		t.Errorf("native_downgraded + stopped unit should reconcile to Stopped, got %s", c.Reason)
	}

	// fell_back_to_udp + unit NOT active -> unchanged (mimic is intentionally not running there).
	write("fell_back_to_udp", "eth0")
	if c, _ := readMimicCondition(path, now); c.Reason != mimicReasonFellBackToUDP {
		t.Errorf("fell_back should NOT reconcile, got %s", c.Reason)
	}

	// active breadcrumb + EMPTY egress -> trust the breadcrumb (no live check possible) -> Active.
	mimicUnitActiveFn = func(string) bool { return false }
	write("active", "")
	if c, _ := readMimicCondition(path, now); c.Reason != mimicReasonActive {
		t.Errorf("active + empty egress should trust the breadcrumb, got %s", c.Reason)
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
	if _, ok := findCond(got, runtimecontract.ConditionTypeMimic); !ok {
		t.Fatalf("collectConditions did not include the mimic condition: %+v", got)
	}
}
