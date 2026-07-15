package controller

import (
	"context"
	"errors"
	"fmt"
	"testing"
)

var errInjectedPromoteCrash = errors.New("injected process crash during promote")

// promoteCrashKV commits one selected record write and then reports an error, modeling a process
// dying after the atomic rename became durable but before PromoteStaged reached its next step. It
// can also fail immediately before the generation commit. The underlying filekv is reopened by the
// test after each fault; no in-memory rollback is involved.
type promoteCrashKV struct {
	kvBackend
	failCollection string
	failOccurrence int
	seenOccurrence int
	failGeneration bool
}

func interruptPromotionAfterFirstCurrentWrite(t *testing.T, tenant TenantID) *FileStore {
	t.Helper()
	ctx := context.Background()
	root := t.TempDir()
	base, err := NewFileStore(root)
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}
	for _, id := range []string{"alpha", "beta"} {
		if err := base.StageBundle(ctx, tenant, SignedBundle{
			NodeID: id, Generation: 1,
			Files: map[string][]byte{"checksums.sha256": []byte("original-" + id)},
		}); err != nil {
			t.Fatalf("StageBundle(%s): %v", id, err)
		}
	}

	faultStore := newStoreCore(&promoteCrashKV{
		kvBackend:      base.filekv,
		failCollection: collCurrent,
		failOccurrence: 1,
	}, nil)
	if _, err := faultStore.PromoteStaged(ctx, tenant); !errors.Is(err, errInjectedPromoteCrash) {
		t.Fatalf("faulted PromoteStaged = %v, want injected crash", err)
	}

	reopened, err := NewFileStore(root)
	if err != nil {
		t.Fatalf("reopen FileStore: %v", err)
	}
	return reopened
}

func (f *promoteCrashKV) put(t TenantID, coll, key string, value []byte) error {
	if err := f.kvBackend.put(t, coll, key, value); err != nil {
		return err
	}
	if coll == f.failCollection {
		f.seenOccurrence++
		if f.seenOccurrence == f.failOccurrence {
			return fmt.Errorf("%w after %s write %d", errInjectedPromoteCrash, coll, f.seenOccurrence)
		}
	}
	return nil
}

func (f *promoteCrashKV) setGeneration(t TenantID, generation int64) error {
	if f.failGeneration {
		f.failGeneration = false
		return fmt.Errorf("%w before generation commit", errInjectedPromoteCrash)
	}
	return f.kvBackend.setGeneration(t, generation)
}

func TestFileStoreInterruptedPromoteNeverServesUncommittedBundle(t *testing.T) {
	tests := []struct {
		name           string
		collection     string
		occurrence     int
		failGeneration bool
		keystone       bool
	}{
		{name: "after first current bundle", collection: collCurrent, occurrence: 1},
		{name: "after first desired generation", collection: collNodes, occurrence: 1},
		{name: "after second current bundle", collection: collCurrent, occurrence: 2},
		{name: "after second desired generation", collection: collNodes, occurrence: 2},
		{name: "before generation commit keystone off", failGeneration: true},
		{name: "after served trust list", collection: collServedTL, occurrence: 1, keystone: true},
		{name: "before generation commit keystone on", failGeneration: true, keystone: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			const tenant = TenantID("promote-crash")
			root := t.TempDir()
			base, err := NewFileStore(root)
			if err != nil {
				t.Fatalf("NewFileStore: %v", err)
			}
			for _, id := range []string{"alpha", "beta"} {
				if err := base.UpsertNode(ctx, tenant, Node{NodeID: id, Status: NodeApproved}); err != nil {
					t.Fatalf("UpsertNode(%s): %v", id, err)
				}
				if err := base.StageBundle(ctx, tenant, SignedBundle{
					NodeID: id, Generation: 1, Files: map[string][]byte{"checksums.sha256": []byte("sum-" + id)},
				}); err != nil {
					t.Fatalf("StageBundle(%s): %v", id, err)
				}
			}
			if tc.keystone {
				if err := base.CompareAndSetOperatorCredential(ctx, tenant, nil, OperatorCredential{
					Alg: "ed25519", PublicKeyPEM: "test-public-key",
				}); err != nil {
					t.Fatalf("pin test keystone: %v", err)
				}
				if err := base.PutSignedTrustList(ctx, tenant, StoredTrustList{
					TrustListJSON: []byte(`{"epoch":1}` + "\n"),
					SignatureJSON: []byte(`{"alg":"ed25519","signature":"test"}`),
					Epoch:         1,
				}); err != nil {
					t.Fatalf("PutSignedTrustList: %v", err)
				}
			}

			faultKV := &promoteCrashKV{
				kvBackend:      base.filekv,
				failCollection: tc.collection,
				failOccurrence: tc.occurrence,
				failGeneration: tc.failGeneration,
			}
			faultStore := newStoreCore(faultKV, nil)
			if _, err := faultStore.PromoteStaged(ctx, tenant); !errors.Is(err, errInjectedPromoteCrash) {
				t.Fatalf("faulted PromoteStaged = %v, want injected crash", err)
			}

			reopened, err := NewFileStore(root)
			if err != nil {
				t.Fatalf("reopen FileStore: %v", err)
			}
			if generation, err := reopened.CurrentGeneration(ctx, tenant); err != nil || generation != 0 {
				t.Fatalf("committed generation after crash = (%d, %v), want 0", generation, err)
			}
			for _, id := range []string{"alpha", "beta"} {
				if sc, err := reopened.GetServedConfig(ctx, tenant, id); err == nil {
					t.Fatalf("GetServedConfig(%s) exposed uncommitted generation %d", id, sc.Bundle.Generation)
				} else if !errors.Is(err, ErrNotFound) && !errors.Is(err, ErrUncommittedPromotion) {
					t.Fatalf("GetServedConfig(%s) = %v, want absent or uncommitted", id, err)
				}
			}
			if served, err := reopened.GetServedTrustList(ctx, tenant); err == nil {
				t.Fatalf("GetServedTrustList exposed uncommitted generation %d", served.PromotedGeneration)
			} else if !errors.Is(err, ErrNotFound) && !errors.Is(err, ErrUncommittedPromotion) {
				t.Fatalf("GetServedTrustList = %v, want absent or uncommitted", err)
			}

			// Staged inputs survive every pre-commit failure, so a clean retry rewrites the full set
			// and publishes it by committing generation.json last.
			if generation, err := reopened.PromoteStaged(ctx, tenant); err != nil || generation != 1 {
				t.Fatalf("retry PromoteStaged = (%d, %v), want generation 1", generation, err)
			}
			for _, id := range []string{"alpha", "beta"} {
				sc, err := reopened.GetServedConfig(ctx, tenant, id)
				if err != nil || sc.Bundle.Generation != 1 {
					t.Fatalf("committed GetServedConfig(%s) = (gen %d, %v), want gen 1", id, sc.Bundle.Generation, err)
				}
				if tc.keystone && !sc.HasTrustList {
					t.Fatalf("committed GetServedConfig(%s) omitted signed trust list", id)
				}
			}
		})
	}
}

func TestInterruptedPromotionBlocksGenerationBumpUntilRetry(t *testing.T) {
	ctx := context.Background()
	const tenant = TenantID("promote-crash-bump-barrier")
	store := interruptPromotionAfterFirstCurrentWrite(t, tenant)

	if _, err := store.BumpGeneration(ctx, tenant); !errors.Is(err, ErrUncommittedPromotion) {
		t.Fatalf("BumpGeneration during interrupted promote = %v, want ErrUncommittedPromotion", err)
	}
	if generation, err := store.CurrentGeneration(ctx, tenant); err != nil || generation != 0 {
		t.Fatalf("blocked bump changed committed generation = (%d, %v), want 0", generation, err)
	}

	// The exact original staged set remains the only recovery authority.
	if generation, err := store.PromoteStaged(ctx, tenant); err != nil || generation != 1 {
		t.Fatalf("retry PromoteStaged = (%d, %v), want generation 1", generation, err)
	}
	if generation, err := store.BumpGeneration(ctx, tenant); err != nil || generation != 2 {
		t.Fatalf("BumpGeneration after recovery = (%d, %v), want generation 2", generation, err)
	}
	for _, id := range []string{"alpha", "beta"} {
		served, err := store.GetServedConfig(ctx, tenant, id)
		if err != nil || served.Bundle.Generation != 1 {
			t.Fatalf("served %s after recovery+bump = (generation %d, %v), want original generation 1", id, served.Bundle.Generation, err)
		}
	}
}

func TestInterruptedPromotionBlocksRestageUntilRetryAndAllowsOlderDeltaBundle(t *testing.T) {
	ctx := context.Background()
	const tenant = TenantID("promote-crash-restage-barrier")
	store := interruptPromotionAfterFirstCurrentWrite(t, tenant)

	stageMutationAttempts := []struct {
		name string
		run  func() error
	}{
		{
			name: "incremental stage",
			run: func() error {
				return store.StageBundle(ctx, tenant, SignedBundle{NodeID: "gamma", Generation: 1})
			},
		},
		{
			name: "exact replacement",
			run: func() error {
				_, err := store.ReplaceStagedSet(ctx, tenant, StagedSet{
					Generation: 1,
					Bundles: []SignedBundle{{
						NodeID: "beta", Generation: 1,
						Files: map[string][]byte{"checksums.sha256": []byte("unrelated-beta")},
					}},
				})
				return err
			},
		},
		{
			name: "prune",
			run: func() error {
				_, err := store.PruneStagedBundles(ctx, tenant, []string{"beta"})
				return err
			},
		},
		{
			name: "trust-list mutation",
			run: func() error {
				return store.PutSignedTrustList(ctx, tenant, StoredTrustList{
					TrustListJSON: []byte(`{"epoch":1}` + "\n"), Epoch: 1,
				})
			},
		},
	}
	for _, attempt := range stageMutationAttempts {
		t.Run(attempt.name, func(t *testing.T) {
			if err := attempt.run(); !errors.Is(err, ErrUncommittedPromotion) {
				t.Fatalf("stage mutation during interrupted promote = %v, want ErrUncommittedPromotion", err)
			}
		})
	}

	if generation, err := store.PromoteStaged(ctx, tenant); err != nil || generation != 1 {
		t.Fatalf("retry original PromoteStaged = (%d, %v), want generation 1", generation, err)
	}
	for _, id := range []string{"alpha", "beta"} {
		bundle, err := store.GetCurrentBundle(ctx, tenant, id)
		if err != nil || string(bundle.Files["checksums.sha256"]) != "original-"+id {
			t.Fatalf("recovered current %s = (%q, %v), want original candidate", id, bundle.Files["checksums.sha256"], err)
		}
	}

	// A normal next deploy may update only beta. Alpha intentionally remains on its older committed
	// generation; the recovery barrier must not confuse that legitimate delta-skip with an orphan.
	if _, err := store.ReplaceStagedSet(ctx, tenant, StagedSet{
		Generation: 2,
		Bundles: []SignedBundle{{
			NodeID: "beta", Generation: 2,
			Files: map[string][]byte{"checksums.sha256": []byte("beta-v2")},
		}},
	}); err != nil {
		t.Fatalf("ReplaceStagedSet after recovery: %v", err)
	}
	if generation, err := store.PromoteStaged(ctx, tenant); err != nil || generation != 2 {
		t.Fatalf("delta PromoteStaged = (%d, %v), want generation 2", generation, err)
	}
	alpha, err := store.GetServedConfig(ctx, tenant, "alpha")
	if err != nil || alpha.Bundle.Generation != 1 {
		t.Fatalf("delta-skipped alpha = (generation %d, %v), want older committed generation 1", alpha.Bundle.Generation, err)
	}
	if generation, err := store.BumpGeneration(ctx, tenant); err != nil || generation != 3 {
		t.Fatalf("BumpGeneration with delta-skipped older bundle = (%d, %v), want generation 3", generation, err)
	}
}

func TestServedConfigAllowsDeltaSkippedOlderBundle(t *testing.T) {
	ctx := context.Background()
	const tenant = TenantID("promote-older-bundle")
	store := NewMemStore()
	for _, id := range []string{"alpha", "beta"} {
		if err := store.StageBundle(ctx, tenant, SignedBundle{NodeID: id, Generation: 1}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := store.PromoteStaged(ctx, tenant); err != nil {
		t.Fatal(err)
	}
	if err := store.StageBundle(ctx, tenant, SignedBundle{NodeID: "beta", Generation: 2}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.PromoteStaged(ctx, tenant); err != nil {
		t.Fatal(err)
	}
	alpha, err := store.GetServedConfig(ctx, tenant, "alpha")
	if err != nil || alpha.Bundle.Generation != 1 {
		t.Fatalf("delta-skipped alpha = (gen %d, %v), want older committed gen 1", alpha.Bundle.Generation, err)
	}
}
