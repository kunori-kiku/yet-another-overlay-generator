package controller

// promote_scope_test.go — subject-scoped tests for promote scoping + staged-bundle
// purge (controller-server-authority-redesign plan-3): a stale staged bundle (one
// whose provisional generation was invalidated, or whose node left the design)
// can no longer go live on a later promote.
//
// Lifecycle: subject-scoped — retire at subject close.

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/kunorikiku/yet-another-overlay-generator/internal/model"
)

// stagedAt stages a minimal bundle for nodeID at the given provisional generation.
func stagedAt(t *testing.T, ctx context.Context, s Store, tnt TenantID, nodeID string, gen int64) {
	t.Helper()
	if err := s.StageBundle(ctx, tnt, SignedBundle{
		NodeID:     nodeID,
		Generation: gen,
		Files:      map[string][]byte{"install.sh": []byte("#!/bin/sh\n"), "manifest.json": []byte("{}")},
		IsStaged:   true,
	}); err != nil {
		t.Fatalf("StageBundle(%s@%d): %v", nodeID, gen, err)
	}
}

// TestPromoteScope_StaleGenerationNotFlipped: promote flips ONLY bundles staged at
// the generation being promoted (current+1); a bundle carrying a stale provisional
// generation stays staged and never goes live.
func TestPromoteScope_StaleGenerationNotFlipped(t *testing.T) {
	for _, impl := range storeImpls() {
		impl := impl
		t.Run(impl.name, func(t *testing.T) {
			ctx := context.Background()
			s := impl.factory(t)

			// Fresh tenant: current generation 0, so the promotable stage is gen 1.
			stagedAt(t, ctx, s, tenant, "node-live", 1)
			stagedAt(t, ctx, s, tenant, "node-stale", 99) // stale provisional generation

			gen, err := s.PromoteStaged(ctx, tenant)
			if err != nil {
				t.Fatalf("PromoteStaged: %v", err)
			}
			if gen != 1 {
				t.Fatalf("promoted generation = %d, want 1", gen)
			}
			if _, err := s.GetCurrentBundle(ctx, tenant, "node-live"); err != nil {
				t.Errorf("node-live current bundle missing after promote: %v", err)
			}
			if _, err := s.GetCurrentBundle(ctx, tenant, "node-stale"); !errors.Is(err, ErrNotFound) {
				t.Errorf("node-stale went LIVE from a stale staged generation (err=%v, want ErrNotFound)", err)
			}
		})
	}
}

// TestPromoteScope_BumpInvalidatesStage: a BumpGeneration (rekey-all wake) between
// stage and promote invalidates the staged set — its bundles were compiled against
// pre-bump state (old public keys), so promoting them would deploy dead configs.
// The promote refuses with ErrNoStagedBundle; the operator re-stages.
func TestPromoteScope_BumpInvalidatesStage(t *testing.T) {
	for _, impl := range storeImpls() {
		impl := impl
		t.Run(impl.name, func(t *testing.T) {
			ctx := context.Background()
			s := impl.factory(t)

			stagedAt(t, ctx, s, tenant, "node-1", 1) // provisional: gen 0 + 1
			if _, err := s.BumpGeneration(ctx, tenant); err != nil {
				t.Fatalf("BumpGeneration: %v", err)
			}
			// Promote would now be gen 2; the staged bundle carries 1 → refused.
			if _, err := s.PromoteStaged(ctx, tenant); !errors.Is(err, ErrNoStagedBundle) {
				t.Fatalf("PromoteStaged after bump: err = %v, want ErrNoStagedBundle", err)
			}
			if _, err := s.GetCurrentBundle(ctx, tenant, "node-1"); !errors.Is(err, ErrNotFound) {
				t.Errorf("pre-bump staged bundle went live (err=%v, want ErrNotFound)", err)
			}
		})
	}
}

// TestPruneStagedBundles: the stage-side purge deletes staged bundles outside the
// keep set (returning them in stable order) and never touches current bundles.
func TestPruneStagedBundles(t *testing.T) {
	for _, impl := range storeImpls() {
		impl := impl
		t.Run(impl.name, func(t *testing.T) {
			ctx := context.Background()
			s := impl.factory(t)

			// node-promoted becomes CURRENT (must survive any purge).
			stagedAt(t, ctx, s, tenant, "node-promoted", 1)
			if _, err := s.PromoteStaged(ctx, tenant); err != nil {
				t.Fatalf("PromoteStaged: %v", err)
			}

			// Fresh stage set keeps node-keep; node-gone-b and node-gone-a are stale.
			stagedAt(t, ctx, s, tenant, "node-keep", 2)
			stagedAt(t, ctx, s, tenant, "node-gone-b", 2)
			stagedAt(t, ctx, s, tenant, "node-gone-a", 2)

			purged, err := s.PruneStagedBundles(ctx, tenant, []string{"node-keep"})
			if err != nil {
				t.Fatalf("PruneStagedBundles: %v", err)
			}
			if len(purged) != 2 || purged[0] != "node-gone-a" || purged[1] != "node-gone-b" {
				t.Fatalf("purged = %v, want [node-gone-a node-gone-b] (stable order)", purged)
			}

			// The kept staged bundle still promotes; the purged ones never appear.
			gen, err := s.PromoteStaged(ctx, tenant)
			if err != nil {
				t.Fatalf("PromoteStaged after prune: %v", err)
			}
			if gen != 2 {
				t.Fatalf("promoted generation = %d, want 2", gen)
			}
			if _, err := s.GetCurrentBundle(ctx, tenant, "node-keep"); err != nil {
				t.Errorf("node-keep current bundle missing: %v", err)
			}
			for _, gone := range []string{"node-gone-a", "node-gone-b"} {
				if _, err := s.GetCurrentBundle(ctx, tenant, gone); !errors.Is(err, ErrNotFound) {
					t.Errorf("%s went live after purge (err=%v, want ErrNotFound)", gone, err)
				}
			}
			// The promoted current bundle from before the purge is untouched.
			if _, err := s.GetCurrentBundle(ctx, tenant, "node-promoted"); err != nil {
				t.Errorf("purge touched a CURRENT bundle (node-promoted): %v", err)
			}

			// Pruning with nothing staged is a calm no-op.
			purged, err = s.PruneStagedBundles(ctx, tenant, nil)
			if err != nil || len(purged) != 0 {
				t.Errorf("PruneStagedBundles(empty) = %v, %v; want none, nil", purged, err)
			}
		})
	}
}

// TestCompileAndStage_EmptyStagePurges: the review-confirmed kill shot — the
// operator stages a fleet, then retracts the design (no enrolled node remains)
// and re-stages to "clear" it. The empty stage MUST purge the previous stage's
// bundles; without the purge they keep their promotable provisional generation
// and the next promote would run install.sh as root fleet-wide with the
// retracted design.
func TestCompileAndStage_EmptyStagePurges(t *testing.T) {
	store := NewMemStore()
	tnt := TenantID("empty-stage-purge")
	ctx := putStageTopo(t, store, tnt)

	approveNode(t, ctx, store, tnt, "node-router", genWGPubKey(t))
	approveNode(t, ctx, store, tnt, "node-peer", genWGPubKey(t))

	res1, err := CompileAndStage(ctx, store, tnt, time.Now())
	if err != nil || len(res1.Staged) != 2 {
		t.Fatalf("first stage = %+v, %v; want 2 staged", res1, err)
	}

	// Retract the design: replace the topology with only the never-enrolled
	// client, so the enrolled subgraph is empty.
	retracted := stageTestTopo()
	retracted.Nodes = retracted.Nodes[2:3] // node-client only
	retracted.Edges = nil
	raw, err := json.Marshal(retracted)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.PutTopology(ctx, tnt, raw); err != nil {
		t.Fatalf("PutTopology(retracted): %v", err)
	}

	res2, err := CompileAndStage(ctx, store, tnt, time.Now())
	if err != nil {
		t.Fatalf("empty stage: %v", err)
	}
	if len(res2.Staged) != 0 {
		t.Fatalf("empty stage Staged = %v, want none", res2.Staged)
	}

	// The previous stage's bundles are gone: promote must refuse, and neither
	// node may go live.
	if _, err := store.PromoteStaged(ctx, tnt); !errors.Is(err, ErrNoStagedBundle) {
		t.Fatalf("PromoteStaged after empty stage: err = %v, want ErrNoStagedBundle (retracted bundles must not flip live)", err)
	}
	for _, id := range []string{"node-router", "node-peer"} {
		if _, err := store.GetCurrentBundle(ctx, tnt, id); !errors.Is(err, ErrNotFound) {
			t.Errorf("%s went live after a retract + empty stage (err=%v)", id, err)
		}
	}

	// The purge and the empty stage are both audited.
	entries, err := store.ListAudit(ctx, tnt)
	if err != nil {
		t.Fatal(err)
	}
	purgeCount, emptyCount := 0, 0
	for _, e := range entries {
		switch e.Action {
		case "purge-staged":
			purgeCount++
		case "stage-empty":
			emptyCount++
		}
	}
	if purgeCount != 2 || emptyCount != 1 {
		t.Errorf("audit: purge-staged=%d (want 2), stage-empty=%d (want 1)", purgeCount, emptyCount)
	}
}

// TestCompileAndStage_PurgesRemovedNode: the end-to-end audit scenario — a node is
// staged, then removed from the design; the NEXT stage purges its stale staged
// bundle (with an attributable audit entry) so the following promote cannot ship it.
func TestCompileAndStage_PurgesRemovedNode(t *testing.T) {
	store := NewMemStore()
	tnt := TenantID("purge-on-stage")
	ctx := putStageTopo(t, store, tnt)

	approveNode(t, ctx, store, tnt, "node-router", genWGPubKey(t))
	approveNode(t, ctx, store, tnt, "node-peer", genWGPubKey(t))

	// First stage: router + peer staged (client unenrolled).
	res1, err := CompileAndStage(ctx, store, tnt, time.Now())
	if err != nil {
		t.Fatalf("first CompileAndStage: %v", err)
	}
	if !containsStr(res1.Staged, "node-peer") {
		t.Fatalf("first stage Staged = %v, want node-peer included", res1.Staged)
	}

	// The operator removes node-peer (and its edge) from the design WITHOUT promoting.
	reduced := stageTestTopo()
	reduced.Nodes = []model.Node{reduced.Nodes[0], reduced.Nodes[2]} // router + client
	reduced.Edges = []model.Edge{reduced.Edges[1]}                   // client→router only
	raw, err := json.Marshal(reduced)
	if err != nil {
		t.Fatalf("marshal reduced topology: %v", err)
	}
	if _, err := store.PutTopology(ctx, tnt, raw); err != nil {
		t.Fatalf("PutTopology(reduced): %v", err)
	}

	// Second stage: only the router stages; node-peer's stale staged bundle is purged.
	res2, err := CompileAndStage(ctx, store, tnt, time.Now())
	if err != nil {
		t.Fatalf("second CompileAndStage: %v", err)
	}
	if containsStr(res2.Staged, "node-peer") {
		t.Fatalf("second stage Staged = %v, must not contain the removed node-peer", res2.Staged)
	}

	// Promote: the removed node must NOT go live from its first-stage leftover.
	if _, err := store.PromoteStaged(ctx, tnt); err != nil {
		t.Fatalf("PromoteStaged: %v", err)
	}
	if _, err := store.GetCurrentBundle(ctx, tnt, "node-peer"); !errors.Is(err, ErrNotFound) {
		t.Errorf("removed node-peer went live from a stale staged bundle (err=%v)", err)
	}

	// The purge is attributable in the audit log.
	entries, err := store.ListAudit(ctx, tnt)
	if err != nil {
		t.Fatalf("ListAudit: %v", err)
	}
	found := false
	for _, e := range entries {
		if e.Action == "purge-staged" && e.NodeID == "node-peer" {
			found = true
		}
	}
	if !found {
		t.Errorf("no purge-staged audit entry for node-peer (entries: %+v)", entries)
	}
}
