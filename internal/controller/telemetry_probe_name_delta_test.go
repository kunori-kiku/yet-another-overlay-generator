package controller

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/kunorikiku/yet-another-overlay-generator/internal/bundlesig"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/model"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/probepolicy"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/trustlist"
)

func TestTelemetryProbeNameOnlyChangeDoesNotRestageBundles(t *testing.T) {
	const testTenant = TenantID("telemetry-probe-name-delta")

	ctx := context.Background()
	store := NewMemStore()
	topo := noClientTopo()
	topo.Nodes[0].TelemetryProbes = []model.TelemetryProbe{{
		ID:              "resolver",
		Name:            "Primary resolver",
		Type:            model.TelemetryProbeICMP,
		Host:            "resolver.example",
		IntervalSeconds: 30,
	}}
	raw, err := json.Marshal(topo)
	if err != nil {
		t.Fatalf("marshal initial topology: %v", err)
	}
	if _, err := store.PutTopology(ctx, testTenant, raw); err != nil {
		t.Fatalf("PutTopology(initial): %v", err)
	}

	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	if err := store.CompareAndSetOperatorCredential(ctx, testTenant, nil, OperatorCredential{
		Alg:          string(trustlist.AlgEd25519),
		PublicKeyPEM: string(bundlesig.MarshalPublicKeyPEM(pub)),
	}); err != nil {
		t.Fatalf("CompareAndSetOperatorCredential: %v", err)
	}
	for _, nodeID := range []string{"node-router", "node-peer"} {
		approveNode(t, ctx, store, testTenant, nodeID, genWGPubKey(t))
	}

	first, err := CompileAndStage(ctx, store, testTenant, time.Unix(1_000, 0).UTC())
	if err != nil {
		t.Fatalf("CompileAndStage(initial): %v", err)
	}
	if len(first.Staged) != 2 || first.Generation != 1 {
		t.Fatalf("initial stage = %+v, want both nodes at generation 1", first)
	}

	stagedTrust, err := store.GetCurrentSignedTrustList(ctx, testTenant)
	if err != nil {
		t.Fatalf("GetCurrentSignedTrustList: %v", err)
	}
	var manifest trustlist.TrustList
	if err := json.Unmarshal(stagedTrust.TrustListJSON, &manifest); err != nil {
		t.Fatalf("unmarshal staged trust list: %v", err)
	}
	signed, err := trustlist.NewEd25519Signer(priv).Sign(manifest)
	if err != nil {
		t.Fatalf("sign staged trust list: %v", err)
	}
	signatureJSON, err := json.Marshal(signed)
	if err != nil {
		t.Fatalf("marshal staged trust-list signature: %v", err)
	}
	if err := store.PutSignedTrustList(ctx, testTenant, StoredTrustList{
		TrustListJSON: stagedTrust.TrustListJSON,
		SignatureJSON: signatureJSON,
		Epoch:         stagedTrust.Epoch,
	}); err != nil {
		t.Fatalf("PutSignedTrustList: %v", err)
	}
	if generation, err := PromoteStaged(ctx, store, testTenant); err != nil {
		t.Fatalf("PromoteStaged(initial): %v", err)
	} else if generation != 1 {
		t.Fatalf("PromoteStaged(initial) generation = %d, want 1", generation)
	}

	beforeGeneration, err := store.CurrentGeneration(ctx, testTenant)
	if err != nil {
		t.Fatalf("CurrentGeneration(before rename): %v", err)
	}
	beforeBundles := make(map[string]SignedBundle, 2)
	var beforeTelemetryJSON []byte
	for _, nodeID := range []string{"node-router", "node-peer"} {
		bundle, err := store.GetCurrentBundle(ctx, testTenant, nodeID)
		if err != nil {
			t.Fatalf("GetCurrentBundle(%s, before rename): %v", nodeID, err)
		}
		beforeBundles[nodeID] = bundle
		if nodeID == "node-router" {
			beforeTelemetryJSON = append([]byte(nil), bundle.Files[probepolicy.FileName]...)
		}
	}
	wantTelemetryJSON := []byte(`{"version":1,"probes":[{"id":"resolver","type":"icmp","host":"resolver.example","interval_seconds":30}]}`)
	if !bytes.Equal(beforeTelemetryJSON, wantTelemetryJSON) {
		t.Fatalf("initial telemetry.json = %s, want exact v1 projection %s", beforeTelemetryJSON, wantTelemetryJSON)
	}

	record, err := store.GetTopology(ctx, testTenant)
	if err != nil {
		t.Fatalf("GetTopology(after initial promote): %v", err)
	}
	var renamed model.Topology
	if err := json.Unmarshal(record.JSON, &renamed); err != nil {
		t.Fatalf("unmarshal stored topology: %v", err)
	}
	if len(renamed.Nodes[0].TelemetryProbes) != 1 {
		t.Fatalf("stored probes = %+v, want one", renamed.Nodes[0].TelemetryProbes)
	}
	renamed.Nodes[0].TelemetryProbes[0].Name = "Office resolver"
	raw, err = json.Marshal(&renamed)
	if err != nil {
		t.Fatalf("marshal renamed topology: %v", err)
	}
	if _, err := store.PutTopology(ctx, testTenant, raw); err != nil {
		t.Fatalf("PutTopology(name-only change): %v", err)
	}

	preview, err := DeployPreview(ctx, store, testTenant, &renamed)
	if err != nil {
		t.Fatalf("DeployPreview(name-only change): %v", err)
	}
	if len(preview.Nodes) != 2 {
		t.Fatalf("preview nodes = %+v, want two", preview.Nodes)
	}
	for _, node := range preview.Nodes {
		if node.Changed {
			t.Errorf("preview marked %s changed after a display-only probe rename", node.NodeID)
		}
	}

	second, err := CompileAndStage(ctx, store, testTenant, time.Unix(2_000, 0).UTC())
	if err != nil {
		t.Fatalf("CompileAndStage(name-only change): %v", err)
	}
	if len(second.Staged) != 0 {
		t.Fatalf("name-only change staged executable bundles: %v", second.Staged)
	}
	if len(second.UnchangedNodeIDs) != 2 ||
		!containsStr(second.UnchangedNodeIDs, "node-router") ||
		!containsStr(second.UnchangedNodeIDs, "node-peer") {
		t.Fatalf("unchanged nodes = %v, want router and peer", second.UnchangedNodeIDs)
	}
	if second.Generation != beforeGeneration {
		t.Fatalf("stage result generation = %d, want unchanged %d", second.Generation, beforeGeneration)
	}
	afterGeneration, err := store.CurrentGeneration(ctx, testTenant)
	if err != nil {
		t.Fatalf("CurrentGeneration(after rename): %v", err)
	}
	if afterGeneration != beforeGeneration {
		t.Fatalf("current generation advanced %d -> %d for a display-only probe rename", beforeGeneration, afterGeneration)
	}
	if _, err := store.PromoteStaged(ctx, testTenant); !errors.Is(err, ErrNoStagedBundle) {
		t.Fatalf("name-only change left a promotable staged bundle: %v", err)
	}

	for nodeID, before := range beforeBundles {
		after, err := store.GetCurrentBundle(ctx, testTenant, nodeID)
		if err != nil {
			t.Fatalf("GetCurrentBundle(%s, after rename): %v", nodeID, err)
		}
		if after.Generation != before.Generation || !reflect.DeepEqual(after.Files, before.Files) {
			t.Errorf("current executable bundle for %s changed after a probe rename", nodeID)
		}
		if nodeID == "node-router" && !bytes.Equal(after.Files[probepolicy.FileName], beforeTelemetryJSON) {
			t.Errorf("telemetry.json changed after a display-only rename:\nbefore %s\nafter  %s", beforeTelemetryJSON, after.Files[probepolicy.FileName])
		}
	}

	afterRecord, err := store.GetTopology(ctx, testTenant)
	if err != nil {
		t.Fatalf("GetTopology(after name-only stage): %v", err)
	}
	var saved model.Topology
	if err := json.Unmarshal(afterRecord.JSON, &saved); err != nil {
		t.Fatalf("unmarshal saved renamed topology: %v", err)
	}
	if got := saved.Nodes[0].TelemetryProbes[0].Name; got != "Office resolver" {
		t.Fatalf("saved display name = %q, want %q", got, "Office resolver")
	}
}
