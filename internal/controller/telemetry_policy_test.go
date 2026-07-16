package controller

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/kunorikiku/yet-another-overlay-generator/internal/bundlesig"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/model"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/trustlist"
)

func activeProbeStageTopology() *model.Topology {
	topo := stageTestTopo()
	topo.Nodes[0].TelemetryProbes = []model.TelemetryProbe{
		{ID: "dns", Type: model.TelemetryProbeICMP, Host: "service.example"},
		{ID: "tls", Type: model.TelemetryProbeTCP, Host: "192.0.2.20", Port: 443},
	}
	return topo
}

func setupActiveProbeController(t *testing.T, tenant TenantID) (context.Context, Store, *model.Topology) {
	t.Helper()
	ctx := context.Background()
	store := NewMemStore()
	topo := activeProbeStageTopology()
	raw, err := json.Marshal(topo)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.PutTopology(ctx, tenant, raw); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"node-router", "node-peer", "node-client"} {
		approveNode(t, ctx, store, tenant, id, genWGPubKey(t))
	}
	return ctx, store, topo
}

func pinActiveProbeKeystone(t *testing.T, ctx context.Context, store Store, tenant TenantID) {
	t.Helper()
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.CompareAndSetOperatorCredential(ctx, tenant, nil, OperatorCredential{
		Alg: string(trustlist.AlgEd25519), PublicKeyPEM: string(bundlesig.MarshalPublicKeyPEM(pub)),
	}); err != nil {
		t.Fatal(err)
	}
}

func TestActiveTelemetryControllerGate_RefusesBeforeStageMutation(t *testing.T) {
	tenant := TenantID("active-probe-no-keystone")
	ctx, store, _ := setupActiveProbeController(t, tenant)
	before, err := store.GetTopology(ctx, tenant)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := CompileAndStage(ctx, store, tenant, time.Now().UTC()); !errors.Is(err, ErrTelemetryProbesRequireKeystone) {
		t.Fatalf("CompileAndStage = %v, want ErrTelemetryProbesRequireKeystone", err)
	}
	after, err := store.GetTopology(ctx, tenant)
	if err != nil {
		t.Fatal(err)
	}
	if string(after.JSON) != string(before.JSON) || after.Version != before.Version {
		t.Fatal("keystone refusal mutated topology/allocation state")
	}
	if _, err := store.PromoteStaged(ctx, tenant); !errors.Is(err, ErrNoStagedBundle) {
		t.Fatalf("stage refusal left promotable state: %v", err)
	}
	audit, err := store.ListAudit(ctx, tenant)
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range audit {
		if event.Action == "stage" {
			t.Fatalf("stage refusal appended stage audit: %+v", audit)
		}
	}
}

func TestActiveTelemetryControllerGate_DeployPreviewAndKeystoneAllow(t *testing.T) {
	tenant := TenantID("active-probe-preview")
	ctx, store, topo := setupActiveProbeController(t, tenant)
	if _, err := DeployPreview(ctx, store, tenant, topo); !errors.Is(err, ErrTelemetryProbesRequireKeystone) {
		t.Fatalf("DeployPreview without keystone = %v, want sentinel", err)
	}

	pinActiveProbeKeystone(t, ctx, store, tenant)
	preview, err := DeployPreview(ctx, store, tenant, topo)
	if err != nil {
		t.Fatalf("DeployPreview with keystone: %v", err)
	}
	if len(preview.Nodes) != 3 {
		t.Fatalf("DeployPreview nodes = %+v, want three ready nodes", preview.Nodes)
	}
	result, err := CompileAndStage(ctx, store, tenant, time.Now().UTC())
	if err != nil {
		t.Fatalf("CompileAndStage with keystone: %v", err)
	}
	if len(result.Staged) != 3 {
		t.Fatalf("staged = %v, want three nodes", result.Staged)
	}
}
