package controller

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/kunorikiku/yet-another-overlay-generator/internal/probemetric"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/runtimecontract"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/telemetrymetric"
)

// TestStoreTelemetryOverlay (plan-5 F3) pins the telemetry channel across BOTH Store impls: a heartbeat
// (RecordTelemetry) updates the four OBSERVABILITY fields (Conditions/Telemetry/LastAgentVersion/
// LastSeen) as seen by GetNode + ListNodes, leaves the deploy-CUSTODY fields untouched, deep-copies on
// read (a caller mutating a returned node cannot corrupt a later read), and returns ErrNotFound for an
// absent node. TouchLastSeen advances LastSeen only.
func TestStoreTelemetryOverlay(t *testing.T) {
	for _, impl := range storeImpls() {
		impl := impl
		t.Run(impl.name, func(t *testing.T) {
			ctx := context.Background()
			s := impl.factory(t)
			base := time.Date(2026, 7, 2, 12, 0, 0, 0, time.UTC)

			if err := s.UpsertNode(ctx, tenant, Node{
				NodeID: "node-1", Status: NodeApproved,
				WGPublicKey:       "AetxbtqeRdq7xOMpbaVK3St4vAoSMsCzTSLvtqs8BTw=",
				AppliedGeneration: 5, LastChecksum: "csum-5", LastHealth: "applied",
			}); err != nil {
				t.Fatalf("UpsertNode: %v", err)
			}

			conds := []runtimecontract.Condition{{Type: runtimecontract.ConditionTypeWireGuard, Status: runtimecontract.ConditionStatusOK, Reason: "AllPeersUp", Message: "2/2 up"}}
			latency := 7.5
			probeSample, err := json.Marshal([]probemetric.Result{{
				ID: "dns", Type: "icmp", Host: "resolver.example", Status: probemetric.StatusSuccess,
				LatencyMS: &latency, CheckedAt: base.Format(time.RFC3339Nano), IntervalMS: 30_000,
			}})
			if err != nil {
				t.Fatal(err)
			}
			probeLatest, err := json.Marshal([]probemetric.Result{{
				ID: "dns", Type: "icmp", Host: "resolver.example", Status: probemetric.StatusSuccess,
				LatencyMS: &latency, CheckedAt: base.Format(time.RFC3339Nano),
			}})
			if err != nil {
				t.Fatal(err)
			}
			wantProbeLatest := append([]byte(nil), probeLatest...)
			metrics := map[string]json.RawMessage{
				telemetrymetric.Resource.Key:     json.RawMessage(`{"load1":0.5}`),
				telemetrymetric.ProbeSamples.Key: probeSample,
				telemetrymetric.ProbeResults.Key: probeLatest,
				"future_metric":                  json.RawMessage(`{"value":1}`),
			}
			if err := s.RecordTelemetry(ctx, tenant, "node-1", conds, metrics, "v-new", base); err != nil {
				t.Fatalf("RecordTelemetry: %v", err)
			}
			// RecordTelemetry must own the RawMessage bytes, not just copy the map header. HTTP
			// decoders and other callers are free to reuse their input buffer after the call returns.
			copy(metrics[telemetrymetric.Resource.Key], []byte(`{"load1":9.5}`))
			copy(metrics["future_metric"], []byte(`{"value":9}`))
			for i := range metrics[telemetrymetric.ProbeSamples.Key] {
				metrics[telemetrymetric.ProbeSamples.Key][i] = 'x'
			}
			for i := range metrics[telemetrymetric.ProbeResults.Key] {
				metrics[telemetrymetric.ProbeResults.Key][i] = 'x'
			}
			delete(metrics, "future_metric")

			check := func(n Node, where string) {
				t.Helper()
				if len(n.Conditions) != 1 || n.Conditions[0].Reason != "AllPeersUp" {
					t.Fatalf("%s: Conditions = %+v, want the live AllPeersUp set", where, n.Conditions)
				}
				if n.Conditions[0].ObservedAt.IsZero() {
					t.Fatalf("%s: condition ObservedAt not server-stamped", where)
				}
				if string(n.Telemetry[telemetrymetric.Resource.Key]) != `{"load1":0.5}` {
					t.Fatalf("%s: Telemetry = %+v, want the resource metric", where, n.Telemetry)
				}
				if _, leaked := n.Telemetry[telemetrymetric.ProbeSamples.Key]; leaked {
					t.Fatalf("%s: history-only probe_samples leaked onto live telemetry: %+v", where, n.Telemetry)
				}
				if !bytes.Equal(n.Telemetry[telemetrymetric.ProbeResults.Key], wantProbeLatest) {
					t.Fatalf("%s: live probe_results were not cloned/preserved: %s", where, n.Telemetry[telemetrymetric.ProbeResults.Key])
				}
				if string(n.Telemetry["future_metric"]) != `{"value":1}` {
					t.Fatalf("%s: unknown forward-compatible metric was hidden or aliased: %+v", where, n.Telemetry)
				}
				if n.LastAgentVersion != "v-new" || !n.LastSeen.Equal(base) {
					t.Fatalf("%s: LastAgentVersion=%q LastSeen=%v, want v-new/%v", where, n.LastAgentVersion, n.LastSeen, base)
				}
				if n.AppliedGeneration != 5 || n.LastChecksum != "csum-5" || n.LastHealth != "applied" || n.WGPublicKey == "" || n.Status != NodeApproved {
					t.Fatalf("%s: telemetry changed a custody field: %+v", where, n)
				}
			}

			got, err := s.GetNode(ctx, tenant, "node-1")
			if err != nil {
				t.Fatalf("GetNode: %v", err)
			}
			check(got, "GetNode")

			list, err := s.ListNodes(ctx, tenant)
			if err != nil || len(list) != 1 {
				t.Fatalf("ListNodes: err=%v len=%d", err, len(list))
			}
			check(list[0], "ListNodes")

			probeHistory, err := s.QueryTelemetryProbeHistory(ctx, tenant, "node-1", base.Add(-time.Second), base.Add(time.Second))
			if err != nil || len(probeHistory) != 1 || probeHistory[0].ID != "dns" || probeHistory[0].IntervalMS != 30_000 {
				t.Fatalf("full admitted map did not retain history-only probe_samples: %+v err=%v", probeHistory, err)
			}

			// Reads must also own their RawMessage bytes. Replacing a map value would only prove
			// the map was copied; mutate the returned slice in place to catch byte-level aliasing.
			got.Conditions[0].Message = "TAMPERED"
			copy(got.Telemetry[telemetrymetric.Resource.Key], []byte(`{"load1":8.5}`))
			copy(got.Telemetry["future_metric"], []byte(`{"value":8}`))
			for i := range got.Telemetry[telemetrymetric.ProbeResults.Key] {
				got.Telemetry[telemetrymetric.ProbeResults.Key][i] = 'y'
			}
			isolated, err := s.GetNode(ctx, tenant, "node-1")
			if err != nil {
				t.Fatalf("GetNode(after returned-value mutation): %v", err)
			}
			check(isolated, "GetNode after returned-value mutation")

			// TouchLastSeen advances LastSeen only; conditions survive.
			later := base.Add(time.Minute)
			if err := s.TouchLastSeen(ctx, tenant, "node-1", later); err != nil {
				t.Fatalf("TouchLastSeen: %v", err)
			}
			touched, _ := s.GetNode(ctx, tenant, "node-1")
			if !touched.LastSeen.Equal(later) {
				t.Fatalf("TouchLastSeen: LastSeen=%v, want %v", touched.LastSeen, later)
			}
			if len(touched.Conditions) != 1 {
				t.Fatalf("TouchLastSeen dropped conditions: %+v", touched.Conditions)
			}

			// Absent node → ErrNotFound on both heartbeat paths.
			if err := s.RecordTelemetry(ctx, tenant, "ghost", conds, nil, "", base); !errors.Is(err, ErrNotFound) {
				t.Fatalf("RecordTelemetry(absent): err=%v, want ErrNotFound", err)
			}
			if err := s.TouchLastSeen(ctx, tenant, "ghost", base); !errors.Is(err, ErrNotFound) {
				t.Fatalf("TouchLastSeen(absent): err=%v, want ErrNotFound", err)
			}
		})
	}
}

// TestFileStoreTelemetryNoFsync (plan-5 F3) is the anti-DoS regression: a heartbeat must NOT rewrite
// the durable node file (no per-beat fsync), while the overlay is still served on read. It captures the
// node file's bytes + ModTime, fires RecordTelemetry + TouchLastSeen, and asserts the durable file is
// byte- and ModTime-identical — yet GetNode reflects the heartbeat.
func TestFileStoreTelemetryNoFsync(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	s, err := NewFileStore(root)
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}
	if err := s.UpsertNode(ctx, tenant, Node{NodeID: "node-1", Status: NodeApproved, WGPublicKey: "AetxbtqeRdq7xOMpbaVK3St4vAoSMsCzTSLvtqs8BTw="}); err != nil {
		t.Fatalf("UpsertNode: %v", err)
	}
	matches, _ := filepath.Glob(filepath.Join(root, "*", "nodes", "*.json"))
	if len(matches) != 1 {
		t.Fatalf("expected exactly one durable node file, got %v", matches)
	}
	nodeFile := matches[0]
	before, err := os.ReadFile(nodeFile)
	if err != nil {
		t.Fatalf("read node file: %v", err)
	}
	stBefore, err := os.Stat(nodeFile)
	if err != nil {
		t.Fatalf("stat node file: %v", err)
	}

	base := time.Date(2026, 7, 2, 12, 0, 0, 0, time.UTC)
	if err := s.RecordTelemetry(ctx, tenant, "node-1",
		[]runtimecontract.Condition{{Type: runtimecontract.ConditionTypeWireGuard, Status: runtimecontract.ConditionStatusOK, Message: "up"}},
		map[string]json.RawMessage{"resource": json.RawMessage(`{"load1":1}`)}, "v-new", base); err != nil {
		t.Fatalf("RecordTelemetry: %v", err)
	}
	if _, err := s.RecordTelemetrySequenced(ctx, tenant, "node-1",
		[]runtimecontract.Condition{{Type: runtimecontract.ConditionTypeWireGuard, Status: runtimecontract.ConditionStatusOK, Message: "up"}},
		map[string]json.RawMessage{"resource": json.RawMessage(`{"load1":1}`)}, "v-new",
		"00112233445566778899aabbccddeeff", 1, base.Add(time.Second), 30*time.Second, base.Add(2*time.Second)); err != nil {
		t.Fatalf("RecordTelemetrySequenced: %v", err)
	}
	if err := s.TouchLastSeen(ctx, tenant, "node-1", base.Add(3*time.Second)); err != nil {
		t.Fatalf("TouchLastSeen: %v", err)
	}

	after, err := os.ReadFile(nodeFile)
	if err != nil {
		t.Fatalf("re-read node file: %v", err)
	}
	stAfter, err := os.Stat(nodeFile)
	if err != nil {
		t.Fatalf("re-stat node file: %v", err)
	}
	if !bytes.Equal(before, after) {
		t.Fatalf("telemetry rewrote the durable node file (want no durable write)\nbefore=%s\nafter=%s", before, after)
	}
	if !stBefore.ModTime().Equal(stAfter.ModTime()) {
		t.Fatalf("telemetry changed the node file ModTime (want no durable write): %v -> %v", stBefore.ModTime(), stAfter.ModTime())
	}

	// The overlay is nonetheless served on read.
	got, err := s.GetNode(ctx, tenant, "node-1")
	if err != nil {
		t.Fatalf("GetNode: %v", err)
	}
	if got.LastAgentVersion != "v-new" || string(got.Telemetry["resource"]) != `{"load1":1}` {
		t.Fatalf("overlay not served on read: version=%q telemetry=%v", got.LastAgentVersion, got.Telemetry)
	}

	// Deep-copy isolation: the overlay is a SHARED in-memory structure, so a returned node must not
	// alias it — a caller mutating the returned Conditions/Telemetry must not corrupt a later read.
	got.Conditions[0].Message = "TAMPERED"
	copy(got.Telemetry["resource"], []byte(`{"load1":9}`))
	again, err := s.GetNode(ctx, tenant, "node-1")
	if err != nil {
		t.Fatalf("GetNode(again): %v", err)
	}
	if again.Conditions[0].Message != "up" || string(again.Telemetry["resource"]) != `{"load1":1}` {
		t.Fatalf("overlay corrupted by a caller mutating a returned node (applyTelemetryOverlay must deep-copy): conds=%+v telemetry=%v", again.Conditions, again.Telemetry)
	}
}
