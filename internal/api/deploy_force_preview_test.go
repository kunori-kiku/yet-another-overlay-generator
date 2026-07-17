package api

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/kunorikiku/yet-another-overlay-generator/internal/apierr"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/bundlesig"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/controller"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/model"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/trustlist"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/validator"
)

// TestHandleDeployPreview_Wiring verifies the plan-6 preview endpoint HTTP wiring (the changed/unchanged
// LOGIC is proven end-to-end in internal/regression). Operator-gated POST of the current canvas.
func TestHandleDeployPreview_Wiring(t *testing.T) {
	env := newCtlTestEnv(t)
	// A minimal valid canvas (public-keys-only). No enrolled nodes → an empty preview, still 200.
	body := map[string]any{
		"project": map[string]any{"id": "p", "name": "P"},
		"domains": []any{map[string]any{"id": "d1", "name": "net", "cidr": "10.0.0.0/24", "allocation_mode": "auto", "routing_mode": "babel"}},
		"nodes":   []any{},
		"edges":   []any{},
	}
	var pv deployPreviewResponseJSON
	if status := doJSON(t, http.MethodPost, env.opURL("deploy-preview"), testOperatorToken, body, &pv); status != http.StatusOK {
		t.Fatalf("deploy-preview auth POST: status %d, want 200", status)
	}
	// No token → 401 (operator-gated).
	if status := doJSON(t, http.MethodPost, env.opURL("deploy-preview"), "", body, nil); status != http.StatusUnauthorized {
		t.Errorf("deploy-preview no-token: status %d, want 401", status)
	}
	// Wrong method (GET) → 405.
	if status := doJSON(t, http.MethodGet, env.opURL("deploy-preview"), testOperatorToken, nil, nil); status != http.StatusMethodNotAllowed {
		t.Errorf("deploy-preview GET: status %d, want 405", status)
	}
}

func TestBlankTelemetryDestinationDraftReturnsStructuredValidationWithoutMutatingServedDeploy(t *testing.T) {
	env := newCtlTestEnv(t)
	env.enrollNode(t, "node-1")
	env.enrollNode(t, "node-2")

	// Establish a real served generation before reproducing the edit sequence. Active telemetry is
	// absent from this first deploy, so it can be promoted before the test pins a keystone.
	topo := smallTopo()
	raw, err := json.Marshal(topo)
	if err != nil {
		t.Fatal(err)
	}
	if status := doRaw(t, http.MethodPost, env.opURL("update-topology"), testOperatorToken, raw); status != http.StatusOK {
		t.Fatalf("initial update-topology = %d, want 200", status)
	}
	if status := doJSON(t, http.MethodPost, env.opURL("stage"), testOperatorToken, struct{}{}, nil); status != http.StatusOK {
		t.Fatalf("initial stage = %d, want 200", status)
	}
	if status := doJSON(t, http.MethodPost, env.opURL("promote"), testOperatorToken, struct{}{}, nil); status != http.StatusOK {
		t.Fatalf("initial promote = %d, want 200", status)
	}
	ctx := context.Background()
	servedGeneration, err := env.store.CurrentGeneration(ctx, testTenant)
	if err != nil {
		t.Fatal(err)
	}
	servedBundles := make(map[string]controller.SignedBundle, 2)
	for _, nodeID := range []string{"node-1", "node-2"} {
		bundle, err := env.store.GetCurrentBundle(ctx, testTenant, nodeID)
		if err != nil {
			t.Fatalf("GetCurrentBundle(%s): %v", nodeID, err)
		}
		servedBundles[nodeID] = bundle
	}

	// Pin the off-host credential required by a completed probe policy. The invalid blank-host
	// draft below must fail validation before any stage mutation; removing the draft restores a
	// valid deployment.
	publicKey, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	if err := env.store.CompareAndSetOperatorCredential(ctx, testTenant, nil, controller.OperatorCredential{
		Alg:          string(trustlist.AlgEd25519),
		PublicKeyPEM: string(bundlesig.MarshalPublicKeyPEM(publicKey)),
	}); err != nil {
		t.Fatalf("pin operator credential: %v", err)
	}

	topo.Nodes[0].TelemetryProbes = []model.TelemetryProbe{{
		ID: "probe-blank", Type: model.TelemetryProbeICMP, Host: "",
	}}
	raw, err = json.Marshal(topo)
	if err != nil {
		t.Fatal(err)
	}
	if status := doRaw(t, http.MethodPost, env.opURL("update-topology"), testOperatorToken, raw); status != http.StatusOK {
		t.Fatalf("blank-host update-topology = %d, want 200 (drafts remain saveable)", status)
	}

	type codedEnvelope struct {
		Error struct {
			Code    string            `json:"code"`
			Message string            `json:"message"`
			Params  map[string]string `json:"params"`
		} `json:"error"`
	}
	postCoded := func(endpoint string, body any) (int, codedEnvelope) {
		t.Helper()
		requestBody, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		req, err := http.NewRequest(http.MethodPost, env.opURL(endpoint), bytes.NewReader(requestBody))
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
		var envelope codedEnvelope
		if resp.StatusCode != http.StatusOK {
			if err := json.NewDecoder(resp.Body).Decode(&envelope); err != nil {
				t.Fatalf("decode %s coded response: %v", endpoint, err)
			}
		}
		return resp.StatusCode, envelope
	}
	assertBlankHostValidation := func(endpoint string, status int, envelope codedEnvelope) {
		t.Helper()
		if status != http.StatusUnprocessableEntity || envelope.Error.Code != string(apierr.CodeTopologyValidationFailed) {
			t.Fatalf("%s blank host = %d/%q, want 422/%q", endpoint, status, envelope.Error.Code, apierr.CodeTopologyValidationFailed)
		}
		params := envelope.Error.Params
		if params["field"] != "nodes[0].telemetry_probes" || params["validation_code"] != string(validator.CodeNodeTelemetryProbesInvalid) {
			t.Fatalf("%s finding params = %+v", endpoint, params)
		}
		if params["validation_message"] == "" || !strings.Contains(params["validation_param_detail"], `invalid host ""`) {
			t.Fatalf("%s localization params = %+v", endpoint, params)
		}
	}

	status, envelope := postCoded("deploy-preview", topo)
	assertBlankHostValidation("deploy-preview", status, envelope)
	status, envelope = postCoded("stage", struct{}{})
	assertBlankHostValidation("stage", status, envelope)

	if generation, err := env.store.CurrentGeneration(ctx, testTenant); err != nil || generation != servedGeneration {
		t.Fatalf("generation after rejected blank draft = (%d, %v), want (%d, nil)", generation, err, servedGeneration)
	}
	for nodeID, want := range servedBundles {
		got, err := env.store.GetCurrentBundle(ctx, testTenant, nodeID)
		if err != nil {
			t.Fatalf("GetCurrentBundle(%s) after rejection: %v", nodeID, err)
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("served bundle %s changed after rejected blank draft", nodeID)
		}
	}
	if _, err := env.store.PromoteStaged(ctx, testTenant); !errors.Is(err, controller.ErrNoStagedBundle) {
		t.Fatalf("blank-host rejection left a promotable staged set: %v", err)
	}

	// Removing the incomplete draft row restores a valid deployment.
	topo.Nodes[0].TelemetryProbes = nil
	if status := doJSON(t, http.MethodPost, env.opURL("deploy-preview"), testOperatorToken, topo, nil); status != http.StatusOK {
		t.Fatalf("preview after removing blank row = %d, want 200", status)
	}
	raw, err = json.Marshal(topo)
	if err != nil {
		t.Fatal(err)
	}
	if status := doRaw(t, http.MethodPost, env.opURL("update-topology"), testOperatorToken, raw); status != http.StatusOK {
		t.Fatalf("update after removing blank row = %d, want 200", status)
	}
	if status := doJSON(t, http.MethodPost, env.opURL("stage"), testOperatorToken, struct{}{}, nil); status != http.StatusOK {
		t.Fatalf("stage after removing blank row = %d, want 200", status)
	}
}

type deployPreviewListNodesFaultStore struct {
	controller.Store
	err error
}

func (s *deployPreviewListNodesFaultStore) ListNodes(context.Context, controller.TenantID) ([]controller.Node, error) {
	return nil, s.err
}

type stageGetTopologyFaultStore struct {
	controller.Store
	err error
}

func (s *stageGetTopologyFaultStore) GetTopology(context.Context, controller.TenantID) (controller.TopologyRecord, error) {
	return controller.TopologyRecord{}, s.err
}

func TestDeployHandlers_OperationalFaultsRemainInternal(t *testing.T) {
	injected := errors.New("injected storage fault")
	topoJSON, err := json.Marshal(smallTopo())
	if err != nil {
		t.Fatal(err)
	}

	t.Run("deploy preview", func(t *testing.T) {
		h := NewControllerHandler(&deployPreviewListNodesFaultStore{Store: controller.NewMemStore(), err: injected}, testTenant, controller.HashToken(testOperatorToken), DefaultOperatorName, "dev")
		req := httptest.NewRequest(http.MethodPost, "/api/v1/operator/deploy-preview", bytes.NewReader(topoJSON))
		req.Header.Set("Content-Type", "application/json")
		_, apiFailure := h.HandleDeployPreview(context.Background(), testTenant, DefaultOperatorName, httptest.NewRecorder(), req)
		if apiFailure == nil || apiFailure.Code() != apierr.CodeInternal || apiFailure.Status() != http.StatusInternalServerError {
			t.Fatalf("operational preview fault = %#v, want internal/500", apiFailure)
		}
	})

	t.Run("stage", func(t *testing.T) {
		h := NewControllerHandler(&stageGetTopologyFaultStore{Store: controller.NewMemStore(), err: injected}, testTenant, controller.HashToken(testOperatorToken), DefaultOperatorName, "dev")
		req := httptest.NewRequest(http.MethodPost, "/api/v1/operator/stage", nil)
		_, apiFailure := h.HandleStage(context.Background(), testTenant, DefaultOperatorName, httptest.NewRecorder(), req)
		if apiFailure == nil || apiFailure.Code() != apierr.CodeInternal || apiFailure.Status() != http.StatusInternalServerError {
			t.Fatalf("operational stage fault = %#v, want internal/500", apiFailure)
		}
	})
}

// TestHandleStage_ForceBodyAccepted verifies HandleStage parses the optional plan-6 force body without
// error (an empty topology makes the stage a benign no-op; the force LOGIC is proven in regression).
func TestHandleStage_ForceBodyAccepted(t *testing.T) {
	env := newCtlTestEnv(t)
	var resp stageResponseJSON
	if status := doJSON(t, http.MethodPost, env.opURL("stage"), testOperatorToken, map[string]any{"force_all": true}, &resp); status != http.StatusOK {
		t.Fatalf("stage with a force_all body: status %d, want 200", status)
	}
	if status := doJSON(t, http.MethodPost, env.opURL("stage"), testOperatorToken, map[string]any{"force_nodes": []string{"node-1"}}, &resp); status != http.StatusOK {
		t.Fatalf("stage with a force_nodes body: status %d, want 200", status)
	}
	// A stage with NO body is still accepted (force is optional).
	if status := doJSON(t, http.MethodPost, env.opURL("stage"), testOperatorToken, nil, &resp); status != http.StatusOK {
		t.Fatalf("stage with no body: status %d, want 200", status)
	}
}

func TestActiveTelemetryHTTPRequiresKeystone(t *testing.T) {
	env := newCtlTestEnv(t)
	env.enrollNode(t, "node-1")
	env.enrollNode(t, "node-2")

	topo := smallTopo()
	topo.Nodes[0].TelemetryProbes = []model.TelemetryProbe{{
		ID: "dns", Type: model.TelemetryProbeICMP, Host: "resolver.example",
	}}

	type codedEnvelope struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	post := func(url string, body any) (int, string) {
		t.Helper()
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
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
		var envelope codedEnvelope
		if err := json.NewDecoder(resp.Body).Decode(&envelope); err != nil {
			t.Fatalf("decode coded response: %v", err)
		}
		return resp.StatusCode, envelope.Error.Code
	}

	status, code := post(env.opURL("deploy-preview"), topo)
	if status != http.StatusPreconditionFailed || code != string(apierr.CodeTelemetryProbesRequireKeystone) {
		t.Fatalf("deploy-preview = %d/%q, want 412/%q", status, code, apierr.CodeTelemetryProbesRequireKeystone)
	}

	raw, err := json.Marshal(topo)
	if err != nil {
		t.Fatal(err)
	}
	if status := doRaw(t, http.MethodPost, env.opURL("update-topology"), testOperatorToken, raw); status != http.StatusOK {
		t.Fatalf("update-topology: status %d, want 200", status)
	}
	status, code = post(env.opURL("stage"), struct{}{})
	if status != http.StatusPreconditionFailed || code != string(apierr.CodeTelemetryProbesRequireKeystone) {
		t.Fatalf("stage = %d/%q, want 412/%q", status, code, apierr.CodeTelemetryProbesRequireKeystone)
	}
	if status := doJSON(t, http.MethodPost, env.opURL("promote"), testOperatorToken, struct{}{}, nil); status != http.StatusConflict {
		t.Fatalf("promote after refused stage: status %d, want 409", status)
	}
}
