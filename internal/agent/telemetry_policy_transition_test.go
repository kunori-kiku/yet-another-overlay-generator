package agent

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/kunorikiku/yet-another-overlay-generator/internal/model"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/probepolicy"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/telemetrycap"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/telemetrymetric"
)

func TestTelemetryPolicyTransition_LastKnownGoodV1V2V1OmissionAndFailures(t *testing.T) {
	v1, err := probepolicy.Marshal([]model.TelemetryProbe{{
		ID: "legacy", Type: model.TelemetryProbeICMP, Host: "legacy.example",
	}})
	if err != nil {
		t.Fatal(err)
	}
	v2, err := probepolicy.MarshalSuccessor(probepolicy.SuccessorPolicy{
		Devices: &probepolicy.DevicePolicy{Mode: probepolicy.DeviceModeAllEligibleV1},
	})
	if err != nil {
		t.Fatal(err)
	}

	var active []byte
	applySuccessfulCandidate := func(files map[string][]byte, credential bool) error {
		candidate, err := candidateTelemetryPolicy(files, credential)
		if err != nil {
			return err
		}
		active = append(active[:0], candidate...)
		return nil
	}
	assertActive := func(want []byte) {
		t.Helper()
		if !bytes.Equal(active, want) {
			t.Fatalf("active telemetry policy = %s, want %s", active, want)
		}
	}

	if err := applySuccessfulCandidate(map[string][]byte{
		probepolicy.FileName: append([]byte(" \n"), v1...),
	}, true); err != nil {
		t.Fatalf("activate v1: %v", err)
	}
	assertActive(v1)
	if err := applySuccessfulCandidate(map[string][]byte{probepolicy.SuccessorFileName: v2}, true); err != nil {
		t.Fatalf("activate v2: %v", err)
	}
	assertActive(v2)
	if err := applySuccessfulCandidate(map[string][]byte{
		probepolicy.FileName: []byte(`{"version":1,"probes":[{"id":"broken","type":"icmp","host":""}]}`),
	}, true); err == nil {
		t.Fatal("invalid v1 unexpectedly replaced successor policy")
	}
	assertActive(v2)
	if err := applySuccessfulCandidate(map[string][]byte{probepolicy.FileName: v1}, true); err != nil {
		t.Fatalf("return to v1: %v", err)
	}
	assertActive(v1)

	for name, candidate := range map[string]map[string][]byte{
		"malformed successor": {probepolicy.SuccessorFileName: []byte(`{"version":2,"devices":{"mode":"future"}}`)},
		"both members": {
			probepolicy.FileName:          v1,
			probepolicy.SuccessorFileName: v2,
		},
	} {
		t.Run(name, func(t *testing.T) {
			before := append([]byte(nil), active...)
			if err := applySuccessfulCandidate(candidate, true); err == nil {
				t.Fatal("invalid candidate unexpectedly succeeded")
			}
			assertActive(before)
		})
	}
	before := append([]byte(nil), active...)
	if err := applySuccessfulCandidate(map[string][]byte{probepolicy.FileName: v1}, false); err == nil {
		t.Fatal("uncredentialed policy unexpectedly succeeded")
	}
	assertActive(before)

	if err := applySuccessfulCandidate(nil, true); err != nil {
		t.Fatalf("signed omission: %v", err)
	}
	if len(active) != 0 {
		t.Fatalf("signed omission did not clear active policy: %s", active)
	}

	stateDir := t.TempDir()
	previous := &State{NodeID: "alpha", LastResult: LastResultOK, ActiveTelemetryPolicy: append([]byte(nil), v2...)}
	if err := SaveState(stateDir, previous); err != nil {
		t.Fatal(err)
	}
	cfg := &Config{NodeID: "alpha", StateDir: stateDir, InstallArgs: []string{"--uninstall"}}
	manifest := &manifestInfo{NodeID: "alpha", CompiledAt: "2026-07-17T00:00:00Z", Checksum: "sum"}
	if err := recordSuccess(cfg, previous, manifest, &VerifyResult{Signed: true}, 0, nil); err != nil {
		t.Fatalf("record successful uninstall: %v", err)
	}
	uninstalled, err := LoadState(stateDir)
	if err != nil {
		t.Fatal(err)
	}
	if uninstalled.LastAction != LastActionUninstall || len(uninstalled.ActiveTelemetryPolicy) != 0 {
		t.Fatalf("successful uninstall retained successor policy: %+v", uninstalled)
	}
}

func TestAgentCapabilities_SamplerAdvertisesOnlyImplementedSuccessorSupport(t *testing.T) {
	conditions, metrics := (agentCapabilitiesSampler{}).Sample(time.Unix(1, 0))
	if conditions != nil {
		t.Fatalf("agent capability sampler emitted conditions: %+v", conditions)
	}
	metric, ok := metrics[telemetrymetric.AgentCapabilitiesKey].(telemetrymetric.AgentCapabilitiesMetric)
	if !ok {
		t.Fatalf("agent capability metric = %#v", metrics[telemetrymetric.AgentCapabilitiesKey])
	}
	want := []string{telemetrycap.DeviceV1, telemetrycap.PolicyV2, telemetrycap.URLV1}
	if len(metric.Capabilities) != len(want) {
		t.Fatalf("agent capabilities = %v, want %v", metric.Capabilities, want)
	}
	for i := range want {
		if metric.Capabilities[i] != want[i] {
			t.Fatalf("agent capabilities = %v, want %v", metric.Capabilities, want)
		}
	}
}

func TestTelemetryPolicyTransition_VerifyBundleRejectsDualOrUncoveredSuccessor(t *testing.T) {
	successor := []byte(`{"version":2,"devices":{"mode":"all-eligible-v1"}}`)
	uncovered := newSignedBundle(t, "2026-07-17T00:00:00Z")
	uncovered.files[probepolicy.SuccessorFileName] = successor
	if _, err := VerifyBundle(uncovered.files, uncovered.pubPEM); err == nil ||
		!strings.Contains(err.Error(), probepolicy.SuccessorFileName+" present but not covered") {
		t.Fatalf("VerifyBundle uncovered successor policy = %v", err)
	}

	dual := newSignedBundle(t, "2026-07-17T00:00:00Z")
	dual.files[probepolicy.FileName] = []byte(`{"version":1,"probes":[{"id":"p","type":"icmp","host":"example.com"}]}`)
	dual.files[probepolicy.SuccessorFileName] = successor
	if _, err := VerifyBundle(dual.files, dual.pubPEM); err == nil || !strings.Contains(err.Error(), "contains both") {
		t.Fatalf("VerifyBundle dual telemetry policies = %v", err)
	}
}
