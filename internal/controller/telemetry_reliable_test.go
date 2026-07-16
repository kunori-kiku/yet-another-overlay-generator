package controller

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/kunorikiku/yet-another-overlay-generator/internal/runtimecontract"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/telemetrymetric"
)

func sequencedResourceMetric(load float64) map[string]json.RawMessage {
	raw, _ := json.Marshal(map[string]any{"load1": load})
	return map[string]json.RawMessage{"resource": raw}
}

func TestRecordTelemetrySequenced_DedupesAndSeparatesSampleFromReceiptTime(t *testing.T) {
	ctx := context.Background()
	store := NewMemStore()
	if err := store.UpsertNode(ctx, "tn", Node{NodeID: "n1"}); err != nil {
		t.Fatal(err)
	}

	bootID := "00112233445566778899aabbccddeeff"
	sampledAt := time.Date(2026, 7, 16, 10, 0, 0, 0, time.UTC)
	receivedAt := sampledAt.Add(2 * time.Minute)
	firstCondition := []runtimecontract.Condition{{
		Type: runtimecontract.ConditionTypeWireGuard, Status: runtimecontract.ConditionStatusOK,
		Reason: "First",
	}}
	receipt, err := store.RecordTelemetrySequenced(ctx, "tn", "n1", firstCondition, sequencedResourceMetric(1), "v1", bootID, 1, sampledAt, 30*time.Second, receivedAt)
	if err != nil {
		t.Fatalf("RecordTelemetrySequenced(first): %v", err)
	}
	if receipt.Duplicate || receipt.AcknowledgedSequence != 1 || !receipt.SampledAt.Equal(sampledAt) || !receipt.ReceivedAt.Equal(receivedAt) {
		t.Fatalf("first receipt = %+v", receipt)
	}

	// Simulate a response lost after controller acceptance. The retry is acknowledged but its changed
	// payload must not replace the first observation or append history a second time.
	retryReceivedAt := receivedAt.Add(time.Second)
	retry, err := store.RecordTelemetrySequenced(ctx, "tn", "n1",
		[]runtimecontract.Condition{{Type: runtimecontract.ConditionTypeWireGuard, Status: runtimecontract.ConditionStatusWarn, Reason: "RetryMustNotWin"}},
		sequencedResourceMetric(9), "v9", bootID, 1, sampledAt, 30*time.Second, retryReceivedAt)
	if err != nil {
		t.Fatalf("RecordTelemetrySequenced(retry): %v", err)
	}
	if !retry.Duplicate || retry.AcknowledgedSequence != 1 {
		t.Fatalf("retry receipt = %+v, want duplicate ack 1", retry)
	}

	node, err := store.GetNode(ctx, "tn", "n1")
	if err != nil {
		t.Fatal(err)
	}
	if len(node.Conditions) != 1 || node.Conditions[0].Reason != "First" || !node.Conditions[0].ObservedAt.Equal(receivedAt) {
		t.Fatalf("duplicate replaced authoritative condition: %+v", node.Conditions)
	}
	if !node.LastSeen.Equal(retryReceivedAt) {
		t.Fatalf("LastSeen = %v, want retry receipt %v", node.LastSeen, retryReceivedAt)
	}
	if node.LastAgentVersion != "v1" || string(node.Telemetry["resource"]) != `{"load1":1}` {
		t.Fatalf("duplicate replaced live telemetry: version=%q metrics=%s", node.LastAgentVersion, node.Telemetry["resource"])
	}

	history, err := store.QueryTelemetryHistory(ctx, "tn", "n1", sampledAt.Add(-time.Second), sampledAt.Add(time.Second))
	if err != nil || len(history) != 1 || !history[0].TS.Equal(sampledAt) || history[0].Load1 != 1 || history[0].IntervalMS != 30000 {
		t.Fatalf("history after exact retry = %+v, err=%v", history, err)
	}

	secondSampledAt := sampledAt.Add(30 * time.Second)
	secondReceivedAt := retryReceivedAt.Add(time.Second)
	if _, err := store.RecordTelemetrySequenced(ctx, "tn", "n1", nil, sequencedResourceMetric(2), "v2", bootID, 2, secondSampledAt, 30*time.Second, secondReceivedAt); err != nil {
		t.Fatalf("RecordTelemetrySequenced(second): %v", err)
	}
	history, err = store.QueryTelemetryHistory(ctx, "tn", "n1", sampledAt.Add(-time.Second), secondSampledAt.Add(time.Second))
	if err != nil || len(history) != 2 || !history[1].TS.Equal(secondSampledAt) || history[1].Load1 != 2 {
		t.Fatalf("history after sequence 2 = %+v, err=%v", history, err)
	}
}

func TestRecordTelemetrySequenced_RestartBoundaryIsExplicitlyVolatile(t *testing.T) {
	ctx := context.Background()
	bootID := "00112233445566778899aabbccddeeff"
	at := time.Date(2026, 7, 16, 10, 0, 0, 0, time.UTC)
	for i := 0; i < 2; i++ {
		store := NewMemStore()
		if err := store.UpsertNode(ctx, "tn", Node{NodeID: "n1"}); err != nil {
			t.Fatal(err)
		}
		receipt, err := store.RecordTelemetrySequenced(ctx, "tn", "n1", nil, sequencedResourceMetric(1), "v1", bootID, 1, at, 30*time.Second, at)
		if err != nil || receipt.Duplicate {
			t.Fatalf("fresh controller %d receipt=%+v err=%v; cursor must reset across restart", i, receipt, err)
		}
	}
}

func TestRecordTelemetrySequenced_ExactAckAndRetiredBootCannotReplaceLiveState(t *testing.T) {
	ctx := context.Background()
	store := NewMemStore()
	if err := store.UpsertNode(ctx, "tn", Node{NodeID: "n1"}); err != nil {
		t.Fatal(err)
	}
	base := time.Date(2026, 7, 16, 10, 0, 0, 0, time.UTC)
	bootA := "00112233445566778899aabbccddeeff"
	bootB := "ffeeddccbbaa99887766554433221100"
	bootC := "0123456789abcdeffedcba9876543210"
	if _, err := store.RecordTelemetrySequenced(ctx, "tn", "n1", nil, sequencedResourceMetric(1), "a1", bootA, 1, base, 30*time.Second, base.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := store.RecordTelemetrySequenced(ctx, "tn", "n1", nil, sequencedResourceMetric(3), "a3", bootA, 3, base.Add(time.Minute), 30*time.Second, base.Add(time.Minute+time.Second)); err != nil {
		t.Fatal(err)
	}
	stale, err := store.RecordTelemetrySequenced(ctx, "tn", "n1", nil, sequencedResourceMetric(9), "must-not-win", bootA, 2, base.Add(30*time.Second), 30*time.Second, base.Add(2*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if !stale.Duplicate || stale.AcknowledgedSequence != 2 {
		t.Fatalf("stale exact acknowledgement=%+v, want duplicate ack 2", stale)
	}

	bootBAt := base.Add(10 * time.Minute)
	if _, err := store.RecordTelemetrySequenced(ctx, "tn", "n1", nil, sequencedResourceMetric(100), "live-b", bootB, 1, bootBAt, 30*time.Second, bootBAt.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	delayedReceivedAt := bootBAt.Add(2 * time.Second)
	if _, err := store.RecordTelemetrySequenced(ctx, "tn", "n1", nil, sequencedResourceMetric(4), "retired-a", bootA, 4, base.Add(2*time.Minute), 30*time.Second, delayedReceivedAt); err != nil {
		t.Fatal(err)
	}
	// A previously unseen but clearly old boot is retained for history without displacing boot B.
	if _, err := store.RecordTelemetrySequenced(ctx, "tn", "n1", nil, sequencedResourceMetric(5), "stale-c", bootC, 1, base.Add(3*time.Minute), 30*time.Second, delayedReceivedAt.Add(time.Second)); err != nil {
		t.Fatal(err)
	}

	node, err := store.GetNode(ctx, "tn", "n1")
	if err != nil {
		t.Fatal(err)
	}
	if node.LastAgentVersion != "live-b" || string(node.Telemetry["resource"]) != `{"load1":100}` {
		t.Fatalf("retired boot replaced live state: version=%q telemetry=%s", node.LastAgentVersion, node.Telemetry["resource"])
	}
	if !node.LastSeen.Equal(delayedReceivedAt.Add(time.Second)) {
		t.Fatalf("LastSeen=%v, want latest authenticated receipt", node.LastSeen)
	}
	history, err := store.QueryTelemetryHistory(ctx, "tn", "n1", base.Add(-time.Second), bootBAt.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	var loads []float64
	for _, sample := range history {
		loads = append(loads, sample.Load1)
	}
	want := []float64{1, 3, 4, 5, 100}
	if len(loads) != len(want) {
		t.Fatalf("history loads=%v, want %v", loads, want)
	}
	for i := range want {
		if loads[i] != want[i] {
			t.Fatalf("history loads=%v, want %v", loads, want)
		}
	}
}

func TestRecordTelemetrySequenced_CurrentBootReclaimsAfterEvictedBootArrivesLate(t *testing.T) {
	ctx := context.Background()
	store := NewMemStore()
	if err := store.UpsertNode(ctx, "tn", Node{NodeID: "n1"}); err != nil {
		t.Fatal(err)
	}
	base := time.Date(2026, 7, 16, 10, 0, 0, 0, time.UTC)
	bootA := "00112233445566778899aabbccddeeff"
	bootB := "ffeeddccbbaa99887766554433221100"
	if _, err := store.RecordTelemetrySequenced(ctx, "tn", "n1", nil, sequencedResourceMetric(1), "boot-a", bootA, 1, base, 30*time.Second, base.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	bootBAt := base.Add(10 * time.Minute)
	if _, err := store.RecordTelemetrySequenced(ctx, "tn", "n1", nil, sequencedResourceMetric(2), "boot-b", bootB, 1, bootBAt, 30*time.Second, bootBAt.Add(time.Second)); err != nil {
		t.Fatal(err)
	}

	// Fill the bounded cursor set with clearly stale boots. Boot A is the oldest
	// non-active cursor, so the last insertion evicts it while boot B remains active.
	staleBoots := []string{
		"11111111111111111111111111111111",
		"22222222222222222222222222222222",
		"33333333333333333333333333333333",
	}
	for i, bootID := range staleBoots {
		if _, err := store.RecordTelemetrySequenced(ctx, "tn", "n1", nil, sequencedResourceMetric(float64(10+i)), "stale", bootID, 1,
			base.Add(time.Duration(i+1)*time.Minute), 30*time.Second, bootBAt.Add(time.Duration(i+2)*time.Second)); err != nil {
			t.Fatal(err)
		}
	}

	// The evicted boot is now indistinguishable from a new restart, but its trusted sample time is
	// older than the active boot. It remains valid history and liveness evidence without replacing
	// the newer live overlay.
	lateAReceivedAt := bootBAt.Add(6 * time.Second)
	if _, err := store.RecordTelemetrySequenced(ctx, "tn", "n1", nil, sequencedResourceMetric(30), "late-a", bootA, 2,
		bootBAt.Add(-time.Minute), 30*time.Second, lateAReceivedAt); err != nil {
		t.Fatal(err)
	}
	node, err := store.GetNode(ctx, "tn", "n1")
	if err != nil {
		t.Fatal(err)
	}
	if node.LastAgentVersion != "boot-b" || string(node.Telemetry["resource"]) != `{"load1":2}` {
		t.Fatalf("delayed evicted boot replaced newer live state: version=%q telemetry=%s", node.LastAgentVersion, node.Telemetry["resource"])
	}

	// A newer trusted sample from the known current boot must reclaim live state.
	if _, err := store.RecordTelemetrySequenced(ctx, "tn", "n1", nil, sequencedResourceMetric(40), "boot-b-current", bootB, 2,
		bootBAt.Add(time.Minute), 30*time.Second, bootBAt.Add(time.Minute+time.Second)); err != nil {
		t.Fatal(err)
	}
	node, err = store.GetNode(ctx, "tn", "n1")
	if err != nil {
		t.Fatal(err)
	}
	if node.LastAgentVersion != "boot-b-current" || string(node.Telemetry["resource"]) != `{"load1":40}` {
		t.Fatalf("current boot did not reclaim live state: version=%q telemetry=%s", node.LastAgentVersion, node.Telemetry["resource"])
	}
}

func TestAgentCapabilities_DelayedEvictedBootCannotRestoreLiveReadiness(t *testing.T) {
	ctx := context.Background()
	store := NewMemStore()
	if err := store.UpsertNode(ctx, "tn", Node{NodeID: "n1"}); err != nil {
		t.Fatal(err)
	}
	base := time.Date(2026, 7, 16, 10, 0, 0, 0, time.UTC)
	bootA := "00112233445566778899aabbccddeeff"
	bootB := "ffeeddccbbaa99887766554433221100"
	capabilityMetric := map[string]json.RawMessage{
		"agent_capabilities": json.RawMessage(`{"capabilities":["telemetry-policy-v2"]}`),
	}
	if _, err := store.RecordTelemetrySequenced(ctx, "tn", "n1", nil, capabilityMetric, "boot-a", bootA, 1,
		base, 30*time.Second, base.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	bootBAt := base.Add(10 * time.Minute)
	if _, err := store.RecordTelemetrySequenced(ctx, "tn", "n1", nil, nil, "boot-b", bootB, 1,
		bootBAt, 30*time.Second, bootBAt.Add(time.Second)); err != nil {
		t.Fatal(err)
	}

	// Fill the cursor set so boot A is evicted, then replay a later sequence from A whose sample
	// predates B. The newer heartbeat's omission must continue to mean "not confirmed".
	for i, bootID := range []string{
		"11111111111111111111111111111111",
		"22222222222222222222222222222222",
		"33333333333333333333333333333333",
	} {
		if _, err := store.RecordTelemetrySequenced(ctx, "tn", "n1", nil, nil, "stale", bootID, 1,
			base.Add(time.Duration(i+1)*time.Minute), 30*time.Second, bootBAt.Add(time.Duration(i+2)*time.Second)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := store.RecordTelemetrySequenced(ctx, "tn", "n1", nil, capabilityMetric, "late-a", bootA, 2,
		bootBAt.Add(-time.Minute), 30*time.Second, bootBAt.Add(6*time.Second)); err != nil {
		t.Fatal(err)
	}
	node, err := store.GetNode(ctx, "tn", "n1")
	if err != nil {
		t.Fatal(err)
	}
	if _, restored := node.Telemetry["agent_capabilities"]; restored {
		t.Fatalf("delayed older boot restored stale readiness: telemetry=%v", node.Telemetry)
	}

	// A genuinely newer sample from the active boot may confirm support again.
	if _, err := store.RecordTelemetrySequenced(ctx, "tn", "n1", nil, capabilityMetric, "boot-b-current", bootB, 2,
		bootBAt.Add(time.Minute), 30*time.Second, bootBAt.Add(time.Minute+time.Second)); err != nil {
		t.Fatal(err)
	}
	node, err = store.GetNode(ctx, "tn", "n1")
	if err != nil {
		t.Fatal(err)
	}
	if _, confirmed := node.Telemetry["agent_capabilities"]; !confirmed {
		t.Fatalf("fresh current-boot sample did not reconfirm readiness: telemetry=%v", node.Telemetry)
	}
}

func TestAgentCapabilities_NewBootWithRolledBackClockBecomesLiveAndClearsReadiness(t *testing.T) {
	ctx := context.Background()
	store := NewMemStore()
	if err := store.UpsertNode(ctx, "tn", Node{NodeID: "n1"}); err != nil {
		t.Fatal(err)
	}
	base := time.Date(2026, 7, 16, 10, 0, 0, 0, time.UTC)
	capable := map[string]json.RawMessage{
		telemetrymetric.AgentCapabilitiesKey: json.RawMessage(`{"capabilities":["telemetry-policy-v2"]}`),
		telemetrymetric.ResourceKey:          json.RawMessage(`{"load1":1}`),
	}
	if _, err := store.RecordTelemetrySequenced(ctx, "tn", "n1", nil, capable, "capable-a",
		"00112233445566778899aabbccddeeff", 1, base, 30*time.Second, base.Add(time.Second)); err != nil {
		t.Fatal(err)
	}

	// The restart is received later but its wall clock is one minute behind. That advisory rollback
	// must not freeze A's live version/metrics or let B inherit A's capability evidence.
	restartedAt := base.Add(-time.Minute)
	receivedAt := base.Add(2 * time.Second)
	metricsB := map[string]json.RawMessage{
		telemetrymetric.ResourceKey: json.RawMessage(`{"load1":2}`),
	}
	if _, err := store.RecordTelemetrySequenced(ctx, "tn", "n1", nil, metricsB, "downgraded-b",
		"ffeeddccbbaa99887766554433221100", 1, restartedAt, 30*time.Second, receivedAt); err != nil {
		t.Fatal(err)
	}
	node, err := store.GetNode(ctx, "tn", "n1")
	if err != nil {
		t.Fatal(err)
	}
	if node.LastAgentVersion != "downgraded-b" || string(node.Telemetry[telemetrymetric.ResourceKey]) != `{"load1":2}` {
		t.Fatalf("new boot did not become live by receipt: version=%q telemetry=%v", node.LastAgentVersion, node.Telemetry)
	}
	if _, inherited := node.Telemetry[telemetrymetric.AgentCapabilitiesKey]; inherited {
		t.Fatalf("new boot inherited predecessor capability readiness: telemetry=%v", node.Telemetry)
	}

	// A delayed later sequence from the now-retired capable boot has a newer advisory timestamp,
	// but receipt-ordered boot generations must prevent it from reauthorizing after B took over.
	if _, err := store.RecordTelemetrySequenced(ctx, "tn", "n1", nil, capable, "late-capable-a",
		"00112233445566778899aabbccddeeff", 2, base.Add(time.Minute), 30*time.Second, receivedAt.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	node, err = store.GetNode(ctx, "tn", "n1")
	if err != nil {
		t.Fatal(err)
	}
	if node.LastAgentVersion != "downgraded-b" {
		t.Fatalf("retired predecessor reclaimed live version: %q", node.LastAgentVersion)
	}
	if _, restored := node.Telemetry[telemetrymetric.AgentCapabilitiesKey]; restored {
		t.Fatalf("retired predecessor restored capability readiness: telemetry=%v", node.Telemetry)
	}
}

func TestTelemetrySampleTimeBounds_HandleExtremeYearsWithoutChartPoisoning(t *testing.T) {
	receivedAt := time.Date(2026, 7, 16, 10, 0, 0, 0, time.UTC)
	tests := []struct {
		name    string
		sampled time.Time
		want    time.Time
		trusted bool
	}{
		{name: "inside replay window", sampled: receivedAt.Add(-time.Hour), want: receivedAt.Add(-time.Hour), trusted: true},
		{name: "old boundary", sampled: receivedAt.Add(-maxTelemetryReplayAge), want: receivedAt.Add(-maxTelemetryReplayAge), trusted: true},
		{name: "too old", sampled: receivedAt.Add(-maxTelemetryReplayAge - time.Nanosecond), want: receivedAt},
		{name: "future boundary", sampled: receivedAt.Add(maxTelemetryFutureSkew), want: receivedAt.Add(maxTelemetryFutureSkew), trusted: true},
		{name: "too future", sampled: receivedAt.Add(maxTelemetryFutureSkew + time.Nanosecond), want: receivedAt},
		{name: "year one", sampled: time.Date(1, 1, 1, 0, 0, 0, 0, time.UTC), want: receivedAt},
		{name: "year 9999", sampled: time.Date(9999, 12, 31, 23, 59, 59, 0, time.UTC), want: receivedAt},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, trusted := normalizeTelemetrySampleTime(tt.sampled, receivedAt)
			if !got.Equal(tt.want) || trusted != tt.trusted {
				t.Fatalf("normalize(%v)=(%v,%v), want (%v,%v)", tt.sampled, got, trusted, tt.want, tt.trusted)
			}
		})
	}

	ctx := context.Background()
	store := NewMemStore()
	if err := store.UpsertNode(ctx, "tn", Node{NodeID: "n1"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.RecordTelemetrySequenced(ctx, "tn", "n1", nil, sequencedResourceMetric(7), "v1",
		"00112233445566778899aabbccddeeff", 1, time.Date(9999, 12, 31, 23, 59, 59, 0, time.UTC), 45*time.Second, receivedAt); err != nil {
		t.Fatal(err)
	}
	history, err := store.QueryTelemetryHistory(ctx, "tn", "n1", receivedAt.Add(-time.Second), receivedAt.Add(time.Second))
	if err != nil || len(history) != 1 || !history[0].TS.Equal(receivedAt) || history[0].IntervalMS != 45000 {
		t.Fatalf("normalized history=%+v err=%v", history, err)
	}
}
