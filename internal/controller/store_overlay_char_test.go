package controller

// store_overlay_char_test.go — the plan-8 telemetry-overlay CHARACTERIZATION net. Added BEFORE the
// storecore collapse and kept PERPETUAL afterward. It pins the three overlay properties the split
// impls did NOT jointly characterize, so the collapse (memkv ADOPTS FileStore's volatile+monotonic+
// clone-on-read overlay) is provably behavior-preserving where it must be and deliberate where it
// changes:
//
//   - M1 restart non-survival (FileStore-specific: only a durable backend can be reopened) — the
//     overlay is VOLATILE; a reopened store has NO telemetry on the durable record. Green throughout.
//   - M2 monotonic out-of-order (cross-impl) — an older-observedAt heartbeat NEVER regresses a newer
//     one. This is the ONE deliberate MemStore behavior CHANGE: memkv's pre-collapse durable-write had
//     no monotonic gate (it regressed to the older beat), so this subtest is EXPECTED RED for MemStore
//     until the collapse, then green. FileStore is green throughout.
//   - M3 report<->heartbeat coherence (cross-impl) — a /report's fresher conditions win over an older
//     heartbeat, and a newer heartbeat then wins over the report. Green throughout for both impls.

import (
	"context"
	"testing"
	"time"

	"github.com/kunorikiku/yet-another-overlay-generator/internal/runtimecontract"
)

// M1 — restart non-survival. After a heartbeat, the durable node record carries NO telemetry: the
// overlay lives only in memory, so a reopened FileStore (a controller restart) starts with a clean
// custody record and the telemetry self-heals within one interval. Pins the deliberate volatility
// (no fsync-per-beat) that the overlay exists to provide. FileStore-specific by nature — MemStore has
// no reopen — so it characterizes the durable backend directly.
func TestOverlayM1_RestartNonSurvival(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	s, err := NewFileStore(root)
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}
	if err := s.UpsertNode(ctx, tenant, Node{NodeID: "node-1", Status: NodeApproved,
		WGPublicKey: "AetxbtqeRdq7xOMpbaVK3St4vAoSMsCzTSLvtqs8BTw=", AppliedGeneration: 4, LastChecksum: "csum-4"}); err != nil {
		t.Fatalf("UpsertNode: %v", err)
	}
	beat := time.Date(2026, 7, 13, 12, 0, 0, 0, time.UTC)
	conds := []runtimecontract.Condition{{Type: runtimecontract.ConditionTypeWireGuard, Status: runtimecontract.ConditionStatusOK, Reason: "AllPeersUp"}}
	if err := s.RecordTelemetry(ctx, tenant, "node-1", conds, nil, "v-live", beat); err != nil {
		t.Fatalf("RecordTelemetry: %v", err)
	}
	// Live read reflects the beat (overlay merged).
	if got, err := s.GetNode(ctx, tenant, "node-1"); err != nil || len(got.Conditions) != 1 || !got.LastSeen.Equal(beat) {
		t.Fatalf("pre-restart GetNode did not reflect the beat: conds=%d lastSeen=%v err=%v", len(got.Conditions), got.LastSeen, err)
	}

	// Reopen the store on the same root (models a controller restart). The overlay is gone; the
	// durable node record must show NO telemetry.
	s2, err := NewFileStore(root)
	if err != nil {
		t.Fatalf("NewFileStore(reopen): %v", err)
	}
	got, err := s2.GetNode(ctx, tenant, "node-1")
	if err != nil {
		t.Fatalf("GetNode after reopen: %v", err)
	}
	if !got.LastSeen.IsZero() {
		t.Fatalf("telemetry LastSeen survived a restart = %v, want zero (overlay is volatile)", got.LastSeen)
	}
	if got.Conditions != nil {
		t.Fatalf("telemetry Conditions survived a restart = %+v, want nil (overlay is volatile)", got.Conditions)
	}
	if got.Telemetry != nil {
		t.Fatalf("telemetry map survived a restart = %+v, want nil (overlay is volatile)", got.Telemetry)
	}
	// The custody fields DID survive (they are durable, not overlay).
	if got.AppliedGeneration != 4 || got.LastChecksum != "csum-4" {
		t.Fatalf("custody fields lost across restart: gen=%d csum=%q, want 4/csum-4", got.AppliedGeneration, got.LastChecksum)
	}
}

// M2 — monotonic out-of-order. A heartbeat observed at T2 followed by a LATER-ARRIVING but
// OLDER-observed heartbeat (T1 < T2) must keep the T2 reading: LastSeen stays T2 and the conditions
// stay the T2 set. This is the deliberate MemStore behavior CHANGE — before the storecore collapse,
// memkv's durable RecordTelemetry had no monotonic gate and regressed LastSeen/Conditions to the
// older T1 beat, so the MemStore subtest is EXPECTED RED until the collapse (FileStore is green
// throughout). After the collapse both share the volatile overlay's monotonic writtenAt gate.
func TestOverlayM2_MonotonicOutOfOrder(t *testing.T) {
	for _, impl := range storeImpls() {
		impl := impl
		t.Run(impl.name, func(t *testing.T) {
			ctx := context.Background()
			s := impl.factory(t)
			if err := s.UpsertNode(ctx, tenant, Node{NodeID: "node-1", Status: NodeApproved}); err != nil {
				t.Fatalf("UpsertNode: %v", err)
			}
			t1 := time.Date(2026, 7, 13, 12, 0, 0, 0, time.UTC)
			t2 := t1.Add(30 * time.Second)
			newer := []runtimecontract.Condition{{Type: runtimecontract.ConditionTypeWireGuard, Status: runtimecontract.ConditionStatusOK, Reason: "NEWER"}}
			older := []runtimecontract.Condition{{Type: runtimecontract.ConditionTypeWireGuard, Status: runtimecontract.ConditionStatusWarn, Reason: "OLDER"}}

			// The NEWER beat (T2) arrives first.
			if err := s.RecordTelemetry(ctx, tenant, "node-1", newer, nil, "v2", t2); err != nil {
				t.Fatalf("RecordTelemetry(T2): %v", err)
			}
			// A stale beat (T1 < T2) arrives late — it must NOT regress the record.
			if err := s.RecordTelemetry(ctx, tenant, "node-1", older, nil, "v1", t1); err != nil {
				t.Fatalf("RecordTelemetry(T1): %v", err)
			}

			got, err := s.GetNode(ctx, tenant, "node-1")
			if err != nil {
				t.Fatalf("GetNode: %v", err)
			}
			if !got.LastSeen.Equal(t2) {
				t.Fatalf("LastSeen = %v, want %v (a stale beat must not regress LastSeen)", got.LastSeen, t2)
			}
			if len(got.Conditions) != 1 || got.Conditions[0].Reason != "NEWER" {
				t.Fatalf("Conditions = %+v, want the NEWER set kept (a stale beat must not regress conditions)", got.Conditions)
			}
		})
	}
}

// M3 — report<->heartbeat coherence. A heartbeat, then a fresher /report (SetAppliedGeneration), then
// a newer heartbeat: GetNode shows the report's conditions after the report, and the newer heartbeat's
// conditions after it. This pins the SetAppliedGeneration overlay-refresh (so a stale heartbeat overlay
// never permanently shadows a just-written report) AND the monotonic hand-back to a later heartbeat.
// Green throughout for both impls; the collapse must not regress it.
func TestOverlayM3_ReportHeartbeatCoherence(t *testing.T) {
	for _, impl := range storeImpls() {
		impl := impl
		t.Run(impl.name, func(t *testing.T) {
			ctx := context.Background()
			s := impl.factory(t)
			if err := s.UpsertNode(ctx, tenant, Node{NodeID: "node-1", Status: NodeApproved}); err != nil {
				t.Fatalf("UpsertNode: %v", err)
			}
			t1 := time.Date(2026, 7, 13, 12, 0, 0, 0, time.UTC)
			t2 := t1.Add(30 * time.Second)
			t3 := t2.Add(30 * time.Second)

			// Heartbeat at T1.
			hb1 := []runtimecontract.Condition{{Type: runtimecontract.ConditionTypeWireGuard, Status: runtimecontract.ConditionStatusWarn, Reason: "HEARTBEAT1"}}
			if err := s.RecordTelemetry(ctx, tenant, "node-1", hb1, nil, "", t1); err != nil {
				t.Fatalf("RecordTelemetry(hb1): %v", err)
			}
			// A fresher /report at T2 (custody path) with its own conditions.
			report := []runtimecontract.Condition{{Type: runtimecontract.ConditionTypeConfigApply, Status: runtimecontract.ConditionStatusOK, Reason: "REPORT"}}
			if err := s.SetAppliedGeneration(ctx, tenant, "node-1", 7, "csum-7", "applied", "", report, t2); err != nil {
				t.Fatalf("SetAppliedGeneration(report): %v", err)
			}
			got, err := s.GetNode(ctx, tenant, "node-1")
			if err != nil {
				t.Fatalf("GetNode after report: %v", err)
			}
			if len(got.Conditions) != 1 || got.Conditions[0].Reason != "REPORT" {
				t.Fatalf("after a fresher report, Conditions = %+v, want the REPORT set (report must not be shadowed by the stale heartbeat)", got.Conditions)
			}
			// A NEWER heartbeat at T3 wins over the report.
			hb2 := []runtimecontract.Condition{{Type: runtimecontract.ConditionTypeWireGuard, Status: runtimecontract.ConditionStatusOK, Reason: "HEARTBEAT2"}}
			if err := s.RecordTelemetry(ctx, tenant, "node-1", hb2, nil, "", t3); err != nil {
				t.Fatalf("RecordTelemetry(hb2): %v", err)
			}
			got, err = s.GetNode(ctx, tenant, "node-1")
			if err != nil {
				t.Fatalf("GetNode after hb2: %v", err)
			}
			if len(got.Conditions) != 1 || got.Conditions[0].Reason != "HEARTBEAT2" {
				t.Fatalf("after a newer heartbeat, Conditions = %+v, want the HEARTBEAT2 set", got.Conditions)
			}
			// Custody stayed put across the heartbeat (M3 must not disturb the report's generation).
			if got.AppliedGeneration != 7 || got.LastChecksum != "csum-7" {
				t.Fatalf("heartbeat disturbed custody: gen=%d csum=%q, want 7/csum-7", got.AppliedGeneration, got.LastChecksum)
			}
		})
	}
}
