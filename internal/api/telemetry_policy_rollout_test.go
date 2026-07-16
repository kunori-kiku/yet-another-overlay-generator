package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/kunorikiku/yet-another-overlay-generator/internal/apierr"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/model"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/probepolicy"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/telemetrycap"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/telemetrymetric"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/validator"
)

type telemetryPolicyErrorEnvelope struct {
	Error struct {
		Code   string            `json:"code"`
		Params map[string]string `json:"params"`
	} `json:"error"`
}

func postTelemetryPolicyJSON(t *testing.T, url string, body any, successOut any) (int, telemetryPolicyErrorEnvelope) {
	t.Helper()
	var raw []byte
	var err error
	if body != nil {
		raw, err = json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
	}
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+testOperatorToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusOK {
		if successOut != nil {
			if err := json.NewDecoder(resp.Body).Decode(successOut); err != nil {
				t.Fatal(err)
			}
		}
		return resp.StatusCode, telemetryPolicyErrorEnvelope{}
	}
	var envelope telemetryPolicyErrorEnvelope
	if err := json.NewDecoder(resp.Body).Decode(&envelope); err != nil {
		t.Fatalf("decode %d error response: %v", resp.StatusCode, err)
	}
	return resp.StatusCode, envelope
}

func deviceTelemetryTopology() *model.Topology {
	topo := smallTopo()
	topo.Nodes[0].TelemetryDevices = &model.TelemetryDevicePolicy{Mode: string(probepolicy.DeviceModeAllEligibleV1)}
	return topo
}

func TestTelemetryPolicyRolloutHTTPContract(t *testing.T) {
	t.Setenv("YAOG_BUNDLE_SIGNING_KEY", "")
	env := newCtlTestEnv(t)
	env.enrollNode(t, "node-1")
	env.enrollNode(t, "node-2")
	topo := deviceTelemetryTopology()

	status, envelope := postTelemetryPolicyJSON(t, env.opURL("deploy-preview"), topo, nil)
	if status != http.StatusPreconditionFailed || envelope.Error.Code != string(apierr.CodeTelemetryPolicyUpgradeRequired) {
		t.Fatalf("normal preview = %d/%q, want 412/%q", status, envelope.Error.Code, apierr.CodeTelemetryPolicyUpgradeRequired)
	}
	if envelope.Error.Params["count"] != "1" || envelope.Error.Params["nodes"] != "node-1" {
		t.Fatalf("readiness params = %v", envelope.Error.Params)
	}

	status, envelope = postTelemetryPolicyJSON(t, env.opURL("deploy-preview")+"?telemetry_policy_mode=future", topo, nil)
	if status != http.StatusBadRequest || envelope.Error.Code != string(apierr.CodeReqFieldInvalid) || envelope.Error.Params["field"] != "telemetry_policy_mode" {
		t.Fatalf("invalid preview mode = %d/%q/%v", status, envelope.Error.Code, envelope.Error.Params)
	}

	var preview deployPreviewResponseJSON
	status, _ = postTelemetryPolicyJSON(t, env.opURL("deploy-preview")+"?telemetry_policy_mode=upgrade-agents-first", topo, &preview)
	if status != http.StatusOK || len(preview.TelemetryPolicyOmittedNodes) != 1 || preview.TelemetryPolicyOmittedNodes[0] != "node-1" {
		t.Fatalf("upgrade preview = %d/%+v", status, preview)
	}

	raw, err := json.Marshal(topo)
	if err != nil {
		t.Fatal(err)
	}
	if status := doRaw(t, http.MethodPost, env.opURL("update-topology"), testOperatorToken, raw); status != http.StatusOK {
		t.Fatalf("update topology = %d", status)
	}
	status, envelope = postTelemetryPolicyJSON(t, env.opURL("stage"), map[string]any{"telemetry_policy_mode": "future"}, nil)
	if status != http.StatusBadRequest || envelope.Error.Code != string(apierr.CodeReqFieldInvalid) {
		t.Fatalf("invalid stage mode = %d/%q", status, envelope.Error.Code)
	}
	var staged stageResponseJSON
	status, _ = postTelemetryPolicyJSON(t, env.opURL("stage"), map[string]any{"telemetry_policy_mode": "upgrade-agents-first"}, &staged)
	if status != http.StatusOK || len(staged.TelemetryPolicyOmittedNodes) != 1 || staged.TelemetryPolicyOmittedNodes[0] != "node-1" {
		t.Fatalf("upgrade stage = %d/%+v", status, staged)
	}
}

func TestTelemetryPolicyInvalidDeviceModeIsStructuredBeforeReadiness(t *testing.T) {
	env := newCtlTestEnv(t)
	env.enrollNode(t, "node-1")
	env.enrollNode(t, "node-2")
	topo := deviceTelemetryTopology()
	topo.Nodes[0].TelemetryDevices.Mode = "future-mode"

	status, envelope := postTelemetryPolicyJSON(t, env.opURL("deploy-preview"), topo, nil)
	if status != http.StatusUnprocessableEntity || envelope.Error.Code != string(apierr.CodeTopologyValidationFailed) {
		t.Fatalf("invalid device preview = %d/%q", status, envelope.Error.Code)
	}
	if envelope.Error.Params["field"] != "nodes[0].telemetry_devices" ||
		envelope.Error.Params["validation_code"] != string(validator.CodeNodeTelemetryDevicesInvalid) {
		t.Fatalf("invalid device validation params = %v", envelope.Error.Params)
	}
}

func TestTelemetryPolicyDeviceOnlyRequiresKeystoneBeforeStageMutation(t *testing.T) {
	t.Setenv("YAOG_BUNDLE_SIGNING_KEY", "")
	env := newCtlTestEnv(t)
	env.enrollNode(t, "node-1")
	env.enrollNode(t, "node-2")
	ctx := context.Background()
	capabilities, err := json.Marshal(telemetrymetric.AgentCapabilitiesMetric{Capabilities: []string{
		telemetrycap.DeviceV1,
		telemetrycap.PolicyV2,
	}})
	if err != nil {
		t.Fatal(err)
	}
	if err := env.store.RecordTelemetry(ctx, testTenant, "node-1", nil, map[string]json.RawMessage{
		telemetrymetric.AgentCapabilitiesKey: capabilities,
	}, "dev", time.Unix(1_000, 0).UTC()); err != nil {
		t.Fatal(err)
	}
	topo := deviceTelemetryTopology()

	status, envelope := postTelemetryPolicyJSON(t, env.opURL("deploy-preview"), topo, nil)
	if status != http.StatusPreconditionFailed || envelope.Error.Code != string(apierr.CodeTelemetryProbesRequireKeystone) {
		t.Fatalf("device-only preview = %d/%q", status, envelope.Error.Code)
	}
	raw, err := json.Marshal(topo)
	if err != nil {
		t.Fatal(err)
	}
	if status := doRaw(t, http.MethodPost, env.opURL("update-topology"), testOperatorToken, raw); status != http.StatusOK {
		t.Fatalf("update topology = %d", status)
	}
	before, err := env.store.GetTopology(ctx, testTenant)
	if err != nil {
		t.Fatal(err)
	}
	status, envelope = postTelemetryPolicyJSON(t, env.opURL("stage"), nil, nil)
	if status != http.StatusPreconditionFailed || envelope.Error.Code != string(apierr.CodeTelemetryProbesRequireKeystone) {
		t.Fatalf("device-only stage = %d/%q", status, envelope.Error.Code)
	}
	after, err := env.store.GetTopology(ctx, testTenant)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before.JSON, after.JSON) {
		t.Fatal("refused device-only stage mutated stored topology or allocation pins")
	}
	if generation, err := env.store.CurrentGeneration(ctx, testTenant); err != nil || generation != 0 {
		t.Fatalf("generation after refused stage = %d, %v", generation, err)
	}
	if status := doJSON(t, http.MethodPost, env.opURL("promote"), testOperatorToken, struct{}{}, nil); status != http.StatusConflict {
		t.Fatalf("promote after refused device-only stage = %d, want 409", status)
	}
}
