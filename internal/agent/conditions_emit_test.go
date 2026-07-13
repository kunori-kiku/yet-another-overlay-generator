package agent

import (
	"errors"
	"testing"
	"time"

	"github.com/kunorikiku/yet-another-overlay-generator/internal/runtimecontract"
)

// findCond returns the first condition of the given type, or false.
func findCond(conds []runtimecontract.Condition, typ string) (runtimecontract.Condition, bool) {
	for _, c := range conds {
		if c.Type == typ {
			return c, true
		}
	}
	return runtimecontract.Condition{}, false
}

// TestCollectConditions_Funnel proves collectConditions fans in all three sources through the single
// classify chokepoint: configapply ALWAYS present; selfupdate appended when prev reports activity;
// wireguard appended when the (injected) probe returns a dump — and that a bare cycle (nil prev,
// failing probe) emits ONLY configapply (back-compat: the report's conditions array stays minimal).
func TestCollectConditions_Funnel(t *testing.T) {
	now := time.Date(2026, 6, 22, 10, 0, 0, 0, time.UTC)
	orig := wgShowFn
	t.Cleanup(func() { wgShowFn = orig })

	// Bare cycle: no self-update activity + a failing WG probe → only configapply.
	wgShowFn = func() ([]byte, error) { return nil, errors.New("wg: not available") }
	bare := collectConditions(nil, true, now)
	if len(bare) != 1 {
		t.Fatalf("bare cycle: len = %d, want 1 (configapply only): %+v", len(bare), bare)
	}
	if _, ok := findCond(bare, runtimecontract.ConditionTypeConfigApply); !ok {
		t.Fatalf("bare cycle missing configapply: %+v", bare)
	}

	// Active self-update + a healthy WG dump → configapply + selfupdate + wireguard.
	dump := ifaceLine("wg-a") + "\n" + peerLine("wg-a", now.Add(-15*time.Second).Unix())
	wgShowFn = func() ([]byte, error) { return []byte(dump), nil }
	prev := &State{PendingUpdate: &PendingUpdate{To: "v2.0.0-beta.9", Confirmed: true}}
	full := collectConditions(prev, false, now)

	if ca, ok := findCond(full, runtimecontract.ConditionTypeConfigApply); !ok || ca.Reason != "DegradedKeepingLastGood" {
		t.Fatalf("expected degraded configapply, got %+v", full)
	}
	if su, ok := findCond(full, runtimecontract.ConditionTypeSelfUpdate); !ok || su.Reason != reasonSelfUpdateProbationary {
		t.Fatalf("expected selfupdate/Probationary, got %+v", full)
	}
	if wg, ok := findCond(full, runtimecontract.ConditionTypeWireGuard); !ok || wg.Reason != reasonWGAllPeersUp {
		t.Fatalf("expected wireguard/AllPeersUp, got %+v", full)
	}
}
