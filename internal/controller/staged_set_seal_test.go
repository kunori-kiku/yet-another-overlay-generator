package controller

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func sealTestBundle(nodeID string, generation int64, content string) SignedBundle {
	return SignedBundle{
		NodeID:     nodeID,
		Generation: generation,
		Files:      map[string][]byte{"checksums.sha256": []byte(content)},
		IsStaged:   true,
	}
}

func storeKV(t *testing.T, store Store) kvBackend {
	t.Helper()
	switch s := store.(type) {
	case *MemStore:
		return s.storeCore.kv
	case *FileStore:
		return s.storeCore.kv
	default:
		t.Fatalf("unsupported store type %T", store)
		return nil
	}
}

func putRawStoreJSON(t *testing.T, store Store, tenant TenantID, coll, key string, value any) {
	t.Helper()
	raw, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		t.Fatalf("marshal raw store value: %v", err)
	}
	kv := storeKV(t, store)
	if err := kv.withLock(func() error { return kv.put(tenant, coll, key, raw) }); err != nil {
		t.Fatalf("put raw %s/%s: %v", coll, key, err)
	}
}

type observedAtomicStageStore struct {
	Store
	stageBundleCalls int
	pruneCalls       int
	replaceCalls     int
}

func (s *observedAtomicStageStore) StageBundle(context.Context, TenantID, SignedBundle) error {
	s.stageBundleCalls++
	return errors.New("incremental StageBundle must not be called by CompileAndStage")
}

func (s *observedAtomicStageStore) PruneStagedBundles(context.Context, TenantID, []string) ([]string, error) {
	s.pruneCalls++
	return nil, errors.New("incremental PruneStagedBundles must not be called by CompileAndStage")
}

func (s *observedAtomicStageStore) ReplaceStagedSet(ctx context.Context, tenant TenantID, set StagedSet) ([]string, error) {
	s.replaceCalls++
	return s.Store.ReplaceStagedSet(ctx, tenant, set)
}

// TestCompileAndStage_PublishesOneCompleteSet pins the structural fix: compilation/export gathers
// every candidate in memory and crosses the persistence boundary exactly once. Reintroducing the old
// per-node StageBundle + later prune sequence fails this test immediately.
func TestCompileAndStage_PublishesOneCompleteSet(t *testing.T) {
	base := NewMemStore()
	ctx := putNoClientTopo(t, base)
	approveNode(t, ctx, base, tenant, "node-router", genWGPubKey(t))
	approveNode(t, ctx, base, tenant, "node-peer", genWGPubKey(t))
	store := &observedAtomicStageStore{Store: base}

	result, err := CompileAndStage(ctx, store, tenant, time.Now())
	if err != nil {
		t.Fatalf("CompileAndStage: %v", err)
	}
	if len(result.Staged) != 2 {
		t.Fatalf("staged = %v, want both ready nodes", result.Staged)
	}
	if store.replaceCalls != 1 || store.stageBundleCalls != 0 || store.pruneCalls != 0 {
		t.Fatalf("stage persistence calls: replace=%d StageBundle=%d prune=%d, want 1/0/0",
			store.replaceCalls, store.stageBundleCalls, store.pruneCalls)
	}
}

// TestReplaceStagedSet_EmptyMigratesPreSealManifest covers an in-place rc upgrade. Older FileStore
// state can contain signed_trustlist.json without staged-set.json/history. An unchanged/empty stage
// must retain that epoch for status and future monotonicity, but only behind a Historical zero-node
// seal that PromoteStaged refuses.
func TestReplaceStagedSet_EmptyMigratesPreSealManifest(t *testing.T) {
	for _, impl := range storeImpls() {
		impl := impl
		t.Run(impl.name, func(t *testing.T) {
			ctx := context.Background()
			store := impl.factory(t)
			const tn = TenantID("pre-seal-upgrade")
			legacy := StoredTrustList{
				TrustListJSON: []byte(`{"schema_version":1,"tenant":"pre-seal-upgrade","epoch":9,"members":[]}` + "\n"),
				SignatureJSON: []byte(`{"alg":"ed25519","signature":"legacy"}`),
				Epoch:         9,
			}
			putRawStoreJSON(t, store, tn, collStagedTL, "", legacy) // deliberately no seal/history

			if _, err := store.ReplaceStagedSet(ctx, tn, StagedSet{}); err != nil {
				t.Fatalf("empty replacement migration: %v", err)
			}
			got, err := store.GetCurrentSignedTrustList(ctx, tn)
			if err != nil || got.Epoch != 9 || string(got.SignatureJSON) != string(legacy.SignatureJSON) {
				t.Fatalf("historical manifest after migration = (%+v, %v), want epoch 9 + original signature", got, err)
			}
			last, err := store.GetLastStagedTrustList(ctx, tn)
			if err != nil || last.Epoch != 9 {
				t.Fatalf("epoch history after migration = (%+v, %v), want epoch 9", last, err)
			}
			if _, err := store.PromoteStaged(ctx, tn); !errors.Is(err, ErrNoStagedBundle) {
				t.Fatalf("historical upgrade marker promoted: %v", err)
			}
		})
	}
}

func TestBumpGeneration_PreservesOnlyHistoricalManifest(t *testing.T) {
	for _, impl := range storeImpls() {
		impl := impl
		t.Run(impl.name, func(t *testing.T) {
			ctx := context.Background()
			store := impl.factory(t)
			const tn = TenantID("bump-historical-manifest")
			manifest := StoredTrustList{
				TrustListJSON: []byte(`{"schema_version":1,"tenant":"bump-historical-manifest","epoch":3,"members":[]}` + "\n"),
				SignatureJSON: []byte(`{"alg":"ed25519","signature":"signed"}`),
				Epoch:         3,
			}
			if _, err := store.ReplaceStagedSet(ctx, tn, StagedSet{
				Generation: 1,
				Bundles:    []SignedBundle{sealTestBundle("alpha", 1, "alpha-v1")},
				TrustList:  &manifest,
			}); err != nil {
				t.Fatalf("ReplaceStagedSet: %v", err)
			}
			if _, err := store.PromoteStaged(ctx, tn); err != nil {
				t.Fatalf("PromoteStaged: %v", err)
			}
			if gen, err := store.BumpGeneration(ctx, tn); err != nil || gen != 2 {
				t.Fatalf("BumpGeneration = (%d, %v), want (2, nil)", gen, err)
			}
			got, err := store.GetCurrentSignedTrustList(ctx, tn)
			if err != nil || got.Epoch != manifest.Epoch {
				t.Fatalf("visible trust-list after bump = (%+v, %v), want historical epoch %d", got, err, manifest.Epoch)
			}
			if _, err := store.PromoteStaged(ctx, tn); !errors.Is(err, ErrNoStagedBundle) {
				t.Fatalf("historical manifest promoted after bump: %v", err)
			}
		})
	}
}

// TestReplaceStagedSet_SealRejectsLooseBundleAndManifestMutation proves promotion is authorized by
// the exact seal, not merely by whatever JSON records happen to be present. It runs over both
// backends: an extra same-generation bundle and a manifest byte substitution each fail closed; a
// clean replacement prunes/overwrites the loose records and recovers normally.
func TestReplaceStagedSet_SealRejectsLooseBundleAndManifestMutation(t *testing.T) {
	for _, impl := range storeImpls() {
		impl := impl
		t.Run(impl.name, func(t *testing.T) {
			ctx := context.Background()
			store := impl.factory(t)
			const tn = TenantID("sealed-exact-set")
			manifest := StoredTrustList{
				TrustListJSON: []byte(`{"schema_version":1,"tenant":"sealed-exact-set","epoch":4,"members":[]}` + "\n"),
				SignatureJSON: []byte(`{"alg":"ed25519","signature":"signed"}`),
				Epoch:         4,
			}
			candidate := StagedSet{
				Generation: 1,
				Bundles: []SignedBundle{
					sealTestBundle("alpha", 1, "alpha-v1"),
					sealTestBundle("beta", 1, "beta-v1"),
				},
				TrustList: &manifest,
			}
			if _, err := store.ReplaceStagedSet(ctx, tn, candidate); err != nil {
				t.Fatalf("ReplaceStagedSet: %v", err)
			}

			// A loose same-generation record is not in the seal and must never hitchhike into promote.
			putRawStoreJSON(t, store, tn, collStaged, "rogue", sealTestBundle("rogue", 1, "rogue"))
			if _, err := store.PromoteStaged(ctx, tn); !errors.Is(err, ErrIncompleteStagedSet) || !errors.Is(err, ErrNoStagedBundle) {
				t.Fatalf("promote with loose bundle = %v, want ErrIncompleteStagedSet + ErrNoStagedBundle", err)
			}
			if _, err := store.GetCurrentBundle(ctx, tn, "alpha"); !errors.Is(err, ErrNotFound) {
				t.Fatalf("partial exact-set refusal changed current bundle: %v", err)
			}

			// A clean replacement is the recovery operation and prunes the loose record.
			purged, err := store.ReplaceStagedSet(ctx, tn, candidate)
			if err != nil {
				t.Fatalf("clean ReplaceStagedSet after loose record: %v", err)
			}
			if len(purged) != 1 || purged[0] != "rogue" {
				t.Fatalf("recovery purged = %v, want [rogue]", purged)
			}

			// The seal binds canonical manifest bytes + epoch. A substituted record is inert even when
			// it carries a non-empty signature.
			mutated := manifest
			mutated.TrustListJSON = []byte(`{"schema_version":1,"tenant":"evil","epoch":4,"members":[]}` + "\n")
			putRawStoreJSON(t, store, tn, collStagedTL, "", mutated)
			if _, err := store.PromoteStaged(ctx, tn); !errors.Is(err, ErrIncompleteStagedSet) || !errors.Is(err, ErrNoStagedBundle) {
				t.Fatalf("promote with substituted manifest = %v, want ErrIncompleteStagedSet + ErrNoStagedBundle", err)
			}

			if _, err := store.ReplaceStagedSet(ctx, tn, candidate); err != nil {
				t.Fatalf("clean ReplaceStagedSet after manifest substitution: %v", err)
			}
			if gen, err := store.PromoteStaged(ctx, tn); err != nil || gen != 1 {
				t.Fatalf("PromoteStaged after recovery = (%d, %v), want (1, nil)", gen, err)
			}
			for _, id := range []string{"alpha", "beta"} {
				b, err := store.GetCurrentBundle(ctx, tn, id)
				if err != nil || b.Generation != 1 {
					t.Fatalf("current %s after recovery = (%+v, %v), want generation 1", id, b, err)
				}
			}
			if _, err := store.GetCurrentBundle(ctx, tn, "rogue"); !errors.Is(err, ErrNotFound) {
				t.Fatalf("loose rogue bundle was promoted: %v", err)
			}
		})
	}
}

// failSecondStagedPutKV simulates a process/storage failure after one new bundle landed but before
// the remainder, manifest, and final seal. It wraps the real filekv so the partial files are then
// reopened by an ordinary FileStore, exactly like a controller restart.
type failSecondStagedPutKV struct {
	kvBackend
	stagedPuts int
}

func (f *failSecondStagedPutKV) put(t TenantID, coll, key string, val []byte) error {
	if coll == collStaged {
		f.stagedPuts++
		if f.stagedPuts == 2 {
			return fmt.Errorf("injected staged write failure for %s", key)
		}
	}
	return f.kvBackend.put(t, coll, key, val)
}

// TestFileStore_PartialReplaceHasNoSealAfterReopen is the crash-focused regression for the original
// CompileAndStage loop bug. One candidate bundle overwrites disk, the second write fails, and the old
// extra bundle/manifest remain loose. After a fresh FileStore reopen, none can promote because the
// seal invalidation was durable. A clean restage replaces/prunes the partial state and promotes.
func TestFileStore_PartialReplaceHasNoSealAfterReopen(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	const tn = TenantID("partial-stage-reopen")
	store, err := NewFileStore(root)
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}
	oldManifest := StoredTrustList{TrustListJSON: []byte(`{"epoch":1}` + "\n"), Epoch: 1}
	if _, err := store.ReplaceStagedSet(ctx, tn, StagedSet{
		Generation: 1,
		Bundles: []SignedBundle{
			sealTestBundle("alpha", 1, "alpha-old"),
			sealTestBundle("obsolete", 1, "obsolete-old"),
		},
		TrustList: &oldManifest,
	}); err != nil {
		t.Fatalf("seed ReplaceStagedSet: %v", err)
	}

	failingKV := &failSecondStagedPutKV{kvBackend: store.filekv}
	failingCore := newStoreCore(failingKV, newTelemetryHistory("", DefaultTelemetryHistoryCap, nil))
	newManifest := StoredTrustList{
		TrustListJSON: []byte(`{"schema_version":1,"tenant":"partial-stage-reopen","epoch":2,"members":[]}` + "\n"),
		SignatureJSON: []byte(`{"alg":"ed25519","signature":"new"}`),
		Epoch:         2,
	}
	_, replaceErr := failingCore.ReplaceStagedSet(ctx, tn, StagedSet{
		Generation: 1,
		Bundles: []SignedBundle{
			sealTestBundle("alpha", 1, "alpha-new"),
			sealTestBundle("beta", 1, "beta-new"),
		},
		TrustList: &newManifest,
	})
	if replaceErr == nil {
		t.Fatal("injected partial ReplaceStagedSet = nil, want error")
	}

	sealPath := filepath.Join(root, string(tn), "staged-set.json")
	if _, err := os.Stat(sealPath); !os.IsNotExist(err) {
		t.Fatalf("partial replace left a staged-set seal (stat err %v)", err)
	}

	reopened, err := NewFileStore(root)
	if err != nil {
		t.Fatalf("reopen FileStore: %v", err)
	}
	if _, err := reopened.GetCurrentSignedTrustList(ctx, tn); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unsealed leftover manifest is visible after reopen: %v", err)
	}
	if _, err := reopened.PromoteStaged(ctx, tn); !errors.Is(err, ErrIncompleteStagedSet) || !errors.Is(err, ErrNoStagedBundle) {
		t.Fatalf("partial stage promoted after reopen: %v", err)
	}
	if _, err := reopened.GetCurrentBundle(ctx, tn, "alpha"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("partial alpha became current after reopen: %v", err)
	}

	purged, err := reopened.ReplaceStagedSet(ctx, tn, StagedSet{
		Generation: 1,
		Bundles: []SignedBundle{
			sealTestBundle("alpha", 1, "alpha-new"),
			sealTestBundle("beta", 1, "beta-new"),
		},
		TrustList: &newManifest,
	})
	if err != nil {
		t.Fatalf("clean restage after reopen: %v", err)
	}
	if len(purged) != 1 || purged[0] != "obsolete" {
		t.Fatalf("clean restage purged = %v, want [obsolete]", purged)
	}
	if gen, err := reopened.PromoteStaged(ctx, tn); err != nil || gen != 1 {
		t.Fatalf("promote after clean restage = (%d, %v), want (1, nil)", gen, err)
	}
	alpha, err := reopened.GetCurrentBundle(ctx, tn, "alpha")
	if err != nil || string(alpha.Files["checksums.sha256"]) != "alpha-new" {
		t.Fatalf("alpha after recovery = (%+v, %v), want alpha-new", alpha, err)
	}
	if _, err := reopened.GetCurrentBundle(ctx, tn, "obsolete"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("obsolete partial record promoted after recovery: %v", err)
	}
}
