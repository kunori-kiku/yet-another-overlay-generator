package api

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/kunorikiku/yet-another-overlay-generator/internal/controller"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/model"
)

// TestMapConditions is the table test for the pure projection helper (plan-2): nil/empty in => nil
// out (so the nodeJSON omitempty drops the field), N-element in => verbatim field projection with the
// server-stamped ObservedAt carried through.
func TestMapConditions(t *testing.T) {
	if got := mapConditions(nil); got != nil {
		t.Fatalf("mapConditions(nil) = %v, want nil", got)
	}
	if got := mapConditions([]controller.NodeCondition{}); got != nil {
		t.Fatalf("mapConditions(empty) = %v, want nil", got)
	}

	obs := time.Date(2026, 6, 22, 12, 0, 0, 0, time.UTC)
	in := []controller.NodeCondition{
		{Condition: model.Condition{
			Type: model.ConditionTypeConfigApply, Status: model.ConditionStatusOK,
			Reason: "Applied", Message: "configuration applied", Since: "2026-06-22T11:59:00Z",
		}, ObservedAt: obs},
		{Condition: model.Condition{
			Type: model.ConditionTypeMimic, Status: model.ConditionStatusWarn,
			Reason: "FellBackToUDP", Message: "mimic unavailable; running plain UDP", Since: "",
		}, ObservedAt: obs},
	}
	got := mapConditions(in)
	if len(got) != 2 {
		t.Fatalf("mapConditions len = %d, want 2", len(got))
	}
	if got[0] != (conditionJSON{Type: "configapply", Status: "ok", Reason: "Applied",
		Message: "configuration applied", Since: "2026-06-22T11:59:00Z", ObservedAt: obs}) {
		t.Fatalf("condition[0] = %+v", got[0])
	}
	if got[1].Type != "mimic" || got[1].Status != "warn" || got[1].Reason != "FellBackToUDP" ||
		got[1].Since != "" || !got[1].ObservedAt.Equal(obs) {
		t.Fatalf("condition[1] = %+v", got[1])
	}
}

// TestHandleNodes_EmitsConditions pins the operator-nodes wire contract (plan-2): a node WITH
// conditions serializes them under "conditions"; a node WITHOUT conditions omits the field entirely
// (omitempty back-compat) so a pre-conditions fleet's served JSON is byte-identical.
func TestHandleNodes_EmitsConditions(t *testing.T) {
	env := newCtlTestEnv(t)
	// Both nodes only need to EXIST in the registry (enrollNode creates the record); HandleNodes
	// lists all nodes regardless of status, and SetAppliedGeneration needs only an existing node — so
	// no stage/promote is required for this view test.
	env.enrollNode(t, "node-cond")
	env.enrollNode(t, "node-bare")

	obs := time.Date(2026, 6, 22, 12, 0, 0, 0, time.UTC)
	cond := model.Condition{
		Type: model.ConditionTypeConfigApply, Status: model.ConditionStatusOK,
		Reason: "Applied", Message: "configuration applied", Since: "2026-06-22T11:59:00Z",
	}
	if err := env.store.SetAppliedGeneration(context.Background(), testTenant, "node-cond",
		1, "cafebabe", "applied", "v2.0.0-beta.9", []model.Condition{cond}, obs); err != nil {
		t.Fatalf("SetAppliedGeneration(node-cond): %v", err)
	}

	var nodes []nodeJSON
	if status := doJSON(t, http.MethodGet, env.opURL("nodes"), testOperatorToken, nil, &nodes); status != http.StatusOK {
		t.Fatalf("nodes: status %d, want 200", status)
	}

	var sawCond, sawBare bool
	for _, n := range nodes {
		switch n.NodeID {
		case "node-cond":
			sawCond = true
			if len(n.Conditions) != 1 {
				t.Fatalf("node-cond conditions = %d, want 1", len(n.Conditions))
			}
			c := n.Conditions[0]
			if c.Type != "configapply" || c.Status != "ok" || c.Reason != "Applied" ||
				c.Message != "configuration applied" || !c.ObservedAt.Equal(obs) {
				t.Fatalf("node-cond condition = %+v", c)
			}
		case "node-bare":
			sawBare = true
			if n.Conditions != nil {
				t.Fatalf("node-bare must omit conditions, got %+v", n.Conditions)
			}
		}
	}
	if !sawCond || !sawBare {
		t.Fatalf("nodes view missing a node (cond=%v bare=%v)", sawCond, sawBare)
	}
}
