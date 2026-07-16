package controller

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/kunorikiku/yet-another-overlay-generator/internal/model"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/probepolicy"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/telemetrycap"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/telemetrymetric"
)

func agentCapabilitiesJSON(t *testing.T, capabilities ...string) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(telemetrymetric.AgentCapabilitiesMetric{Capabilities: capabilities})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestReadiness_UsesReadyManagedLatestCapabilitiesWithoutVersionInference(t *testing.T) {
	topo := stageTestTopo()
	for i := range topo.Nodes {
		topo.Nodes[i].TelemetryDevices = &model.TelemetryDevicePolicy{Mode: string(probepolicy.DeviceModeAllEligibleV1)}
	}
	topo.Nodes = append(topo.Nodes, model.Node{
		ID: "node-manual", Name: "manual", Role: "peer", DomainID: "domain-1",
		DeploymentMode: model.DeploymentManual, WireGuardPublicKey: genWGPubKey(t),
	})
	capabilities := []string{
		telemetrycap.DeviceV1,
		telemetrycap.PolicyV2,
	}
	nodes := []Node{
		{
			NodeID: "node-router", Status: NodeApproved, WGPublicKey: genWGPubKey(t),
			Telemetry: map[string]json.RawMessage{
				telemetrymetric.AgentCapabilitiesKey: agentCapabilitiesJSON(t, capabilities...),
			},
		},
		{
			NodeID: "node-peer", Status: NodeApproved, WGPublicKey: genWGPubKey(t),
			LastAgentVersion: "v999.0.0",
			Telemetry: map[string]json.RawMessage{
				// Unsorted evidence is malformed and must not become readiness merely because
				// a version string looks newer.
				telemetrymetric.AgentCapabilitiesKey: agentCapabilitiesJSON(t,
					telemetrycap.PolicyV2,
					telemetrycap.DeviceV1,
				),
			},
		},
		{NodeID: "node-client", Status: NodePending, LastAgentVersion: "v999.0.0"},
	}

	_, _, err := PrepareTelemetryPolicyDeployment(topo, nodes, TelemetryPolicyDeployNormal)
	var readiness *TelemetryPolicyReadinessError
	if !errors.As(err, &readiness) {
		t.Fatalf("PrepareTelemetryPolicyDeployment error = %v, want readiness error", err)
	}
	if want := []string{"node-peer"}; !reflect.DeepEqual(readiness.NodeIDs, want) {
		t.Fatalf("blocked nodes = %v, want only ready managed node %v", readiness.NodeIDs, want)
	}
	for _, node := range topo.Nodes[:3] {
		if node.TelemetryDevices == nil {
			t.Fatalf("normal readiness check mutated successor-bearing source node %s", node.ID)
		}
	}
	if topo.Nodes[3].TelemetryDevices != nil {
		t.Fatal("normal readiness check added device policy to the manual source node")
	}

	nodes[1].Telemetry = map[string]json.RawMessage{
		telemetrymetric.AgentCapabilitiesKey: agentCapabilitiesJSON(t, capabilities...),
	}
	projected, omitted, err := PrepareTelemetryPolicyDeployment(topo, nodes, TelemetryPolicyDeployNormal)
	if err != nil {
		t.Fatalf("ready deployment: %v", err)
	}
	if len(omitted) != 0 || projected == topo {
		t.Fatalf("normal deployment = projected:%p source:%p omitted:%v", projected, topo, omitted)
	}
	projected.Nodes[0].TelemetryDevices.Mode = "mutated-copy"
	if topo.Nodes[0].TelemetryDevices.Mode != string(probepolicy.DeviceModeAllEligibleV1) {
		t.Fatal("projected topology aliases the saved successor draft")
	}
}

func TestReadiness_URLRequiresFeatureCapability(t *testing.T) {
	topo := stageTestTopo()
	topo.Nodes[0].TelemetryProbes = []model.TelemetryProbe{{
		ID: "health", Type: model.TelemetryProbeURL, URL: "https://service.internal/ready",
	}}
	nodes := []Node{{
		NodeID: "node-router", Status: NodeApproved, WGPublicKey: genWGPubKey(t),
		Telemetry: map[string]json.RawMessage{
			telemetrymetric.AgentCapabilitiesKey: agentCapabilitiesJSON(t, telemetrycap.PolicyV2),
		},
	}}
	_, _, err := PrepareTelemetryPolicyDeployment(topo, nodes, TelemetryPolicyDeployNormal)
	var readiness *TelemetryPolicyReadinessError
	if !errors.As(err, &readiness) || !reflect.DeepEqual(readiness.NodeIDs, []string{"node-router"}) {
		t.Fatalf("generic-v2-only readiness = %v / %+v, want node-router blocked", err, readiness)
	}
	nodes[0].Telemetry[telemetrymetric.AgentCapabilitiesKey] = agentCapabilitiesJSON(t,
		telemetrycap.PolicyV2, telemetrycap.URLV1,
	)
	if _, _, err := PrepareTelemetryPolicyDeployment(topo, nodes, TelemetryPolicyDeployNormal); err != nil {
		t.Fatalf("URL-capable agent remained blocked: %v", err)
	}
}

func TestUpgradeAgentsFirst_ProjectsOnlySuccessorFieldsAndReportsSortedIDs(t *testing.T) {
	topo := stageTestTopo()
	topo.Nodes[0].TelemetryProbes = []model.TelemetryProbe{
		{ID: "legacy", Type: model.TelemetryProbeICMP, Host: "legacy.example"},
		{ID: "web", Type: model.TelemetryProbeURL, URL: "https://service.internal/"},
	}
	topo.Nodes[0].TelemetryDevices = &model.TelemetryDevicePolicy{Mode: string(probepolicy.DeviceModeAllEligibleV1)}
	topo.Nodes[2].TelemetryDevices = &model.TelemetryDevicePolicy{Mode: string(probepolicy.DeviceModeAllEligibleV1)}

	projected, omitted, err := PrepareTelemetryPolicyDeployment(topo, nil, TelemetryPolicyDeployUpgradeAgentsFirst)
	if err != nil {
		t.Fatalf("upgrade-first projection: %v", err)
	}
	if want := []string{"node-client", "node-router"}; !reflect.DeepEqual(omitted, want) {
		t.Fatalf("omitted node IDs = %v, want %v", omitted, want)
	}
	for _, node := range projected.Nodes {
		if node.TelemetryDevices != nil {
			t.Fatalf("projected node %s retained successor-only device policy", node.ID)
		}
	}
	if len(projected.Nodes[0].TelemetryProbes) != 1 || projected.Nodes[0].TelemetryProbes[0].ID != "legacy" {
		t.Fatalf("upgrade-first projection removed v1-compatible probes: %+v", projected.Nodes[0].TelemetryProbes)
	}
	if topo.Nodes[0].TelemetryDevices == nil || topo.Nodes[2].TelemetryDevices == nil || len(topo.Nodes[0].TelemetryProbes) != 2 {
		t.Fatal("upgrade-first projection erased the saved successor draft")
	}
	if _, _, err := PrepareTelemetryPolicyDeployment(topo, nil, "future-mode"); err == nil {
		t.Fatal("unsupported telemetry deployment mode was accepted")
	}
}

func TestUpgradeAgentsFirst_PreviewAndStageKeepSavedDraftOutOfLegacyBundle(t *testing.T) {
	ctx := context.Background()
	store := NewMemStore()
	tenant := TenantID("telemetry-upgrade-first")
	topo := stageTestTopo()
	topo.Nodes[0].TelemetryDevices = &model.TelemetryDevicePolicy{Mode: string(probepolicy.DeviceModeAllEligibleV1)}
	raw, err := json.Marshal(topo)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.PutTopology(ctx, tenant, raw); err != nil {
		t.Fatal(err)
	}
	for _, nodeID := range []string{"node-router", "node-peer", "node-client"} {
		approveNode(t, ctx, store, tenant, nodeID, genWGPubKey(t))
	}

	if _, err := DeployPreview(ctx, store, tenant, topo); err == nil {
		t.Fatal("normal preview ignored missing successor capability evidence")
	} else {
		var readiness *TelemetryPolicyReadinessError
		if !errors.As(err, &readiness) || !reflect.DeepEqual(readiness.NodeIDs, []string{"node-router"}) {
			t.Fatalf("normal preview error = %v, readiness = %+v", err, readiness)
		}
	}
	preview, err := DeployPreview(ctx, store, tenant, topo, TelemetryPolicyDeployUpgradeAgentsFirst)
	if err != nil {
		t.Fatalf("upgrade-first preview: %v", err)
	}
	if !reflect.DeepEqual(preview.TelemetryPolicyOmittedNodeIDs, []string{"node-router"}) || topo.Nodes[0].TelemetryDevices == nil {
		t.Fatalf("upgrade-first preview = %+v; source draft = %+v", preview, topo.Nodes[0].TelemetryDevices)
	}

	staged, err := CompileAndStage(ctx, store, tenant, time.Unix(1_000, 0).UTC(),
		WithTelemetryPolicyDeployMode(TelemetryPolicyDeployUpgradeAgentsFirst))
	if err != nil {
		t.Fatalf("upgrade-first stage: %v", err)
	}
	if !reflect.DeepEqual(staged.TelemetryPolicyOmittedNodeIDs, []string{"node-router"}) {
		t.Fatalf("stage omitted nodes = %v", staged.TelemetryPolicyOmittedNodeIDs)
	}
	if _, err := PromoteStaged(ctx, store, tenant); err != nil {
		t.Fatalf("promote upgrade-first bundle: %v", err)
	}
	bundle, err := store.GetCurrentBundle(ctx, tenant, "node-router")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := bundle.Files[probepolicy.FileName]; ok {
		t.Fatal("device-only successor draft unexpectedly produced telemetry.json")
	}
	if _, ok := bundle.Files[probepolicy.SuccessorFileName]; ok {
		t.Fatal("upgrade-first bundle unexpectedly contains successor telemetry policy")
	}
	record, err := store.GetTopology(ctx, tenant)
	if err != nil {
		t.Fatal(err)
	}
	var saved model.Topology
	if err := json.Unmarshal(record.JSON, &saved); err != nil {
		t.Fatal(err)
	}
	if saved.Nodes[0].TelemetryDevices == nil || saved.Nodes[0].TelemetryDevices.Mode != string(probepolicy.DeviceModeAllEligibleV1) {
		t.Fatalf("stage erased saved successor draft: %+v", saved.Nodes[0].TelemetryDevices)
	}
	if saved.AllocSchemaVersion != model.CurrentAllocSchemaVersion {
		t.Fatalf("saved allocation schema = %d, want %d", saved.AllocSchemaVersion, model.CurrentAllocSchemaVersion)
	}
	if len(saved.Edges) == 0 || saved.Edges[0].PinnedFromPort == 0 || saved.Edges[0].PinnedToPort == 0 ||
		saved.Edges[0].PinnedFromTransitIP == "" || saved.Edges[0].PinnedToTransitIP == "" {
		t.Fatalf("phase-one allocation pins were not persisted into the full successor draft: %+v", saved.Edges)
	}
}
