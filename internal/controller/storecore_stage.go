package controller

// storecore_stage.go owns the staged-set transaction protocol. FileStore cannot atomically rename a
// directory containing several bundle records, so a small durable seal is the commit marker:
//
//   1. delete (and durably fsync) the old seal;
//   2. write/prune every candidate bundle and the optional keystone manifest;
//   3. write the exact generation/node-set/manifest digest seal LAST.
//
// Promotion trusts neither loose files nor an old manifest: it requires that the next generation,
// exact staged node set, and manifest bytes/epoch all match the seal. A crash anywhere before step 3
// can leave partial JSON records, but they are inert. A clean ReplaceStagedSet overwrites/prunes them
// and publishes a new seal. Promote itself keeps staged inputs until generation.json commits, so a
// crash before that final promote commit remains retryable.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
)

type stagedSetSeal struct {
	Generation      int64    `json:"generation"`
	NodeIDs         []string `json:"node_ids"`
	Historical      bool     `json:"historical,omitempty"`
	HasTrustList    bool     `json:"has_trustlist"`
	TrustListSHA256 string   `json:"trustlist_sha256,omitempty"`
	TrustListEpoch  int64    `json:"trustlist_epoch,omitempty"`
}

func trustListSHA256(canonical []byte) string {
	sum := sha256.Sum256(canonical)
	return hex.EncodeToString(sum[:])
}

func incompleteStagedSet(reason string) error {
	return fmt.Errorf("%w: %w: %s", ErrNoStagedBundle, ErrIncompleteStagedSet, reason)
}

func sameStringSet(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// requireCompletedPromotionLocked prevents a later stage candidate or generation-only wake from
// accidentally committing live-state files left by an interrupted PromoteStaged. Promote writes
// current bundles (and, with a keystone, the served trust-list) before generation.json, which is the
// tenant-wide commit point. Until that counter catches up, the ONLY safe mutating recovery is to retry
// the exact sealed promotion: replacing its staged inputs or merely bumping the counter would let the
// orphaned current files hitchhike on an unrelated commit.
//
// Older current bundles are intentional delta-skip state and remain valid: only records strictly ahead
// of the committed generation block. Caller must hold c.kv.withLock.
func (c *storeCore) requireCompletedPromotionLocked(t TenantID, committed int64) error {
	recs, err := c.kv.list(t, collCurrent)
	if err != nil {
		return err
	}
	for _, r := range recs {
		var b SignedBundle
		if err := json.Unmarshal(r.val, &b); err != nil {
			return fmt.Errorf("controller: parse %s/%s while checking promotion recovery: %w", collCurrent, r.key, err)
		}
		if b.NodeID != r.key {
			return fmt.Errorf("controller: current bundle key %q contains node id %q", r.key, b.NodeID)
		}
		if b.Generation > committed {
			return fmt.Errorf("%w: current bundle %q generation %d is ahead of committed generation %d; retry the interrupted promotion before staging or bumping generation", ErrUncommittedPromotion, r.key, b.Generation, committed)
		}
	}

	var served StoredTrustList
	if err := c.loadJSON(t, collServedTL, "", &served); err == nil {
		if served.PromotedGeneration > committed {
			return fmt.Errorf("%w: served trust-list generation %d is ahead of committed generation %d; retry the interrupted promotion before staging or bumping generation", ErrUncommittedPromotion, served.PromotedGeneration, committed)
		}
	} else if !errors.Is(err, ErrNotFound) {
		return err
	}
	return nil
}

// stagedBundlesForGenerationLocked returns the exact staged records for generation, ordered by
// node ID. It rejects a record whose payload identity differs from its storage key.
func (c *storeCore) stagedBundlesForGenerationLocked(t TenantID, generation int64) ([]SignedBundle, []string, error) {
	recs, err := c.kv.list(t, collStaged)
	if err != nil {
		return nil, nil, err
	}
	bundles := make([]SignedBundle, 0, len(recs))
	ids := make([]string, 0, len(recs))
	for _, r := range recs {
		var b SignedBundle
		if err := json.Unmarshal(r.val, &b); err != nil {
			return nil, nil, fmt.Errorf("controller: parse %s/%s: %w", collStaged, r.key, err)
		}
		if b.NodeID != r.key {
			return nil, nil, fmt.Errorf("controller: staged bundle key %q contains node id %q", r.key, b.NodeID)
		}
		if b.Generation != generation {
			continue
		}
		bundles = append(bundles, b)
		ids = append(ids, r.key)
	}
	return bundles, ids, nil
}

func (c *storeCore) loadSealLocked(t TenantID) (stagedSetSeal, error) {
	var seal stagedSetSeal
	if err := c.loadJSON(t, collStagedSeal, "", &seal); err != nil {
		if !errors.Is(err, ErrNotFound) {
			return stagedSetSeal{}, incompleteStagedSet("cannot read staged-set seal: " + err.Error())
		}
		return stagedSetSeal{}, err
	}
	if seal.Generation <= 0 {
		return stagedSetSeal{}, incompleteStagedSet("seal has a non-positive generation")
	}
	if !sort.StringsAreSorted(seal.NodeIDs) {
		return stagedSetSeal{}, incompleteStagedSet("seal node ids are not canonical")
	}
	if seal.Historical && (len(seal.NodeIDs) != 0 || !seal.HasTrustList) {
		return stagedSetSeal{}, incompleteStagedSet("historical seal must contain only a trust-list")
	}
	for i, id := range seal.NodeIDs {
		if id == "" || i > 0 && seal.NodeIDs[i-1] == id {
			return stagedSetSeal{}, incompleteStagedSet("seal contains an empty or duplicate node id")
		}
	}
	if seal.HasTrustList == (seal.TrustListSHA256 == "") {
		return stagedSetSeal{}, incompleteStagedSet("seal trust-list fields are inconsistent")
	}
	return seal, nil
}

func (c *storeCore) loadSealedTrustListLocked(t TenantID, seal stagedSetSeal) (*StoredTrustList, error) {
	if !seal.HasTrustList {
		return nil, nil
	}
	var sl StoredTrustList
	if err := c.loadJSON(t, collStagedTL, "", &sl); err != nil {
		if errors.Is(err, ErrNotFound) {
			return nil, incompleteStagedSet("sealed trust-list record is absent")
		}
		return nil, incompleteStagedSet("cannot read sealed trust-list: " + err.Error())
	}
	if sl.Epoch != seal.TrustListEpoch || trustListSHA256(sl.TrustListJSON) != seal.TrustListSHA256 {
		return nil, incompleteStagedSet("staged trust-list bytes or epoch do not match the seal")
	}
	return &sl, nil
}

// saveSealForGenerationLocked derives the node IDs back from storage rather than trusting the
// caller's slice, then publishes the seal last. allowEmpty is used only by PutSignedTrustList's
// legacy manifest-first API; an empty seal can expose a manifest for signing but can never promote.
func (c *storeCore) saveSealForGenerationLocked(t TenantID, generation int64, sl *StoredTrustList, allowEmpty bool) error {
	_, ids, err := c.stagedBundlesForGenerationLocked(t, generation)
	if err != nil {
		return err
	}
	if len(ids) == 0 && sl == nil {
		return nil
	}
	if len(ids) == 0 && !allowEmpty {
		return nil
	}
	seal := stagedSetSeal{Generation: generation, NodeIDs: ids}
	if sl != nil {
		seal.HasTrustList = true
		seal.TrustListSHA256 = trustListSHA256(sl.TrustListJSON)
		seal.TrustListEpoch = sl.Epoch
	}
	return c.saveJSON(t, collStagedSeal, "", seal)
}

// pendingSealedTrustListLocked returns the manifest associated with an already-complete candidate
// for nextGeneration. Direct StageBundle/PruneStagedBundles use it to retain their historical
// incremental API without ever borrowing the manifest from an already-promoted generation.
func (c *storeCore) pendingSealedTrustListLocked(t TenantID, nextGeneration int64) *StoredTrustList {
	seal, err := c.loadSealLocked(t)
	if err != nil || seal.Historical || seal.Generation != nextGeneration || !seal.HasTrustList {
		return nil
	}
	sl, err := c.loadSealedTrustListLocked(t, seal)
	if err != nil {
		return nil
	}
	return sl
}

// StageBundle retains the focused/legacy incremental store API. Production CompileAndStage uses
// ReplaceStagedSet so a whole candidate is published in one seal-last operation.
func (c *storeCore) StageBundle(ctx context.Context, t TenantID, b SignedBundle) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if b.NodeID == "" || b.Generation <= 0 {
		return errors.New("controller: staged bundle requires a node id and positive generation")
	}
	return c.kv.withLock(func() error {
		cur, err := c.kv.generation(t)
		if err != nil {
			return err
		}
		if err := c.requireCompletedPromotionLocked(t, cur); err != nil {
			return err
		}
		next := cur + 1
		manifest := c.pendingSealedTrustListLocked(t, next)
		if err := c.kv.del(t, collStagedSeal, ""); err != nil {
			return err
		}
		b.IsStaged = true
		b.IsCurrent = false
		if err := c.saveJSON(t, collStaged, b.NodeID, b); err != nil {
			return err
		}
		return c.saveSealForGenerationLocked(t, next, manifest, false)
	})
}

// ReplaceStagedSet publishes an exact all-or-nothing candidate. The backend lock prevents
// in-process observers from seeing the component writes; the seal ordering makes a process crash
// fail closed after reopen.
func (c *storeCore) ReplaceStagedSet(ctx context.Context, t TenantID, set StagedSet) ([]string, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if len(set.Bundles) > 0 && set.Generation <= 0 {
		return nil, errors.New("controller: a non-empty staged set requires a positive generation")
	}
	if len(set.Bundles) == 0 && set.TrustList != nil {
		return nil, errors.New("controller: cannot stage a trust-list without any changed bundle")
	}
	bundles := append([]SignedBundle(nil), set.Bundles...)
	sort.Slice(bundles, func(i, j int) bool { return bundles[i].NodeID < bundles[j].NodeID })
	keep := make(map[string]bool, len(bundles))
	for i := range bundles {
		b := &bundles[i]
		if b.NodeID == "" || b.Generation != set.Generation {
			return nil, fmt.Errorf("controller: staged bundle %q has generation %d, want %d", b.NodeID, b.Generation, set.Generation)
		}
		if keep[b.NodeID] {
			return nil, fmt.Errorf("controller: duplicate staged bundle for node %q", b.NodeID)
		}
		keep[b.NodeID] = true
		b.IsStaged = true
		b.IsCurrent = false
	}

	var purged []string
	err := c.kv.withLock(func() error {
		cur, err := c.kv.generation(t)
		if err != nil {
			return err
		}
		if err := c.requireCompletedPromotionLocked(t, cur); err != nil {
			return err
		}
		if len(bundles) > 0 && set.Generation != cur+1 {
			return fmt.Errorf("controller: staged set generation %d is stale; current generation is %d", set.Generation, cur)
		}

		// Commit invalidation is FIRST and durable on FileStore. Any later error leaves loose records
		// but no authority to promote them.
		if err := c.kv.del(t, collStagedSeal, ""); err != nil {
			return err
		}
		for _, b := range bundles {
			if err := c.saveJSON(t, collStaged, b.NodeID, b); err != nil {
				return fmt.Errorf("controller: staging bundle for node %s: %w", b.NodeID, err)
			}
		}
		recs, err := c.kv.list(t, collStaged)
		if err != nil {
			return err
		}
		for _, r := range recs {
			if keep[r.key] {
				continue
			}
			if err := c.kv.del(t, collStaged, r.key); err != nil {
				return fmt.Errorf("controller: prune staged bundle %s: %w", r.key, err)
			}
			purged = append(purged, r.key)
		}

		if set.TrustList == nil {
			// Upgrade/back-compat migration: pre-seal FileStore versions retained the last manifest
			// only in signed_trustlist.json. Before an empty cleanup removes that active record, copy
			// it into the non-authoritative epoch/status history slot. It will be restored below only
			// behind a Historical seal, never as a promotable candidate.
			if len(bundles) == 0 {
				var prior StoredTrustList
				if err := c.loadJSON(t, collStagedTL, "", &prior); err == nil {
					if err := c.saveJSON(t, collStagedTLHist, "", prior); err != nil {
						return err
					}
				} else if !errors.Is(err, ErrNotFound) {
					return err
				}
			}
			if err := c.kv.del(t, collStagedTL, ""); err != nil {
				return err
			}
		} else {
			if err := c.saveJSON(t, collStagedTL, "", *set.TrustList); err != nil {
				return fmt.Errorf("controller: storing staged manifest: %w", err)
			}
			// Separate history preserves the anti-rollback epoch even when a later empty stage clears
			// all active records. History is never consulted by promotion or served to an agent.
			if err := c.saveJSON(t, collStagedTLHist, "", *set.TrustList); err != nil {
				return fmt.Errorf("controller: storing staged manifest history: %w", err)
			}
		}

		if len(bundles) == 0 {
			// Preserve the last manifest as an explicitly NON-PROMOTABLE historical view. Existing
			// clients expect GET /trustlist to keep showing the last epoch after an unchanged stage,
			// while Historical prevents a later direct StageBundle from borrowing these bytes.
			var history StoredTrustList
			if err := c.loadJSON(t, collStagedTLHist, "", &history); err == nil {
				if err := c.saveJSON(t, collStagedTL, "", history); err != nil {
					return err
				}
				historyGen := cur
				if historyGen <= 0 {
					historyGen = 1
				}
				return c.saveJSON(t, collStagedSeal, "", stagedSetSeal{
					Generation:      historyGen,
					Historical:      true,
					HasTrustList:    true,
					TrustListSHA256: trustListSHA256(history.TrustListJSON),
					TrustListEpoch:  history.Epoch,
				})
			} else if !errors.Is(err, ErrNotFound) {
				return err
			}
			return nil // no manifest history: active set is simply empty
		}
		if err := c.saveSealForGenerationLocked(t, set.Generation, set.TrustList, false); err != nil {
			return fmt.Errorf("controller: sealing staged set: %w", err)
		}
		// Defense in depth: assert the derived on-disk set equals the validated caller set.
		seal, err := c.loadSealLocked(t)
		if err != nil {
			return err
		}
		expected := make([]string, len(bundles))
		for i := range bundles {
			expected[i] = bundles[i].NodeID
		}
		if !sameStringSet(seal.NodeIDs, expected) {
			_ = c.kv.del(t, collStagedSeal, "")
			return incompleteStagedSet("sealed node set differs from the replacement candidate")
		}
		return nil
	})
	sort.Strings(purged)
	return purged, err
}

// PruneStagedBundles keeps its legacy incremental behavior while participating in the same seal
// protocol: invalidate before deletes, then derive a fresh seal for the remaining next-generation
// records. A partial delete error leaves no seal.
func (c *storeCore) PruneStagedBundles(ctx context.Context, t TenantID, keepIDs []string) ([]string, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	keep := make(map[string]bool, len(keepIDs))
	for _, id := range keepIDs {
		keep[id] = true
	}
	var purged []string
	err := c.kv.withLock(func() error {
		cur, err := c.kv.generation(t)
		if err != nil {
			return err
		}
		if err := c.requireCompletedPromotionLocked(t, cur); err != nil {
			return err
		}
		next := cur + 1
		manifest := c.pendingSealedTrustListLocked(t, next)
		if err := c.kv.del(t, collStagedSeal, ""); err != nil {
			return err
		}
		recs, err := c.kv.list(t, collStaged)
		if err != nil {
			return err
		}
		for _, r := range recs {
			if keep[r.key] {
				continue
			}
			if err := c.kv.del(t, collStaged, r.key); err != nil {
				return fmt.Errorf("controller: prune staged bundle %s: %w", r.key, err)
			}
			purged = append(purged, r.key)
		}
		return c.saveSealForGenerationLocked(t, next, manifest, false)
	})
	sort.Strings(purged)
	return purged, err
}

// PromoteStaged requires the exact durable seal before flipping anything. Staged inputs are retained
// until generation.json commits, so a pre-commit crash can re-drive every current/desired/served write.
func (c *storeCore) PromoteStaged(ctx context.Context, t TenantID) (int64, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	var newGen int64
	err := c.kv.withLock(func() error {
		cur, err := c.kv.generation(t)
		if err != nil {
			return err
		}
		newGen = cur + 1

		seal, err := c.loadSealLocked(t)
		if errors.Is(err, ErrNotFound) {
			recs, listErr := c.kv.list(t, collStaged)
			if listErr != nil {
				return listErr
			}
			if len(recs) > 0 {
				return incompleteStagedSet("staged bundle records exist without a durable seal")
			}
			return ErrNoStagedBundle
		}
		if err != nil {
			return err
		}
		if seal.Generation != newGen {
			return ErrNoStagedBundle
		}
		if seal.Historical || len(seal.NodeIDs) == 0 {
			return ErrNoStagedBundle
		}
		staged, ids, err := c.stagedBundlesForGenerationLocked(t, newGen)
		if err != nil {
			return incompleteStagedSet(err.Error())
		}
		if !sameStringSet(ids, seal.NodeIDs) {
			return incompleteStagedSet("staged bundle records do not exactly match the sealed node set")
		}
		stagedTL, err := c.loadSealedTrustListLocked(t, seal)
		if err != nil {
			return err
		}

		for _, b := range staged {
			b.IsStaged = false
			b.IsCurrent = true
			if err := c.saveJSON(t, collCurrent, b.NodeID, b); err != nil {
				return err
			}
			var n Node
			switch err := c.loadJSON(t, collNodes, b.NodeID, &n); {
			case err == nil:
				n.DesiredGeneration = newGen
				if err := c.saveJSON(t, collNodes, b.NodeID, n); err != nil {
					return err
				}
			case errors.Is(err, ErrNotFound):
				// Store-level tests may stage before registry creation; never create a phantom node.
			default:
				return err
			}
		}

		if stagedTL != nil && len(stagedTL.SignatureJSON) > 0 {
			// Tag the served slot with the generation commit it belongs to. FileStore writes the
			// slot before generation.json, so GetServedConfig/GetServedTrustList can reject it
			// after a crash until that final commit marker lands. Zero remains the legacy value.
			stagedTL.PromotedGeneration = newGen
			if err := c.saveJSON(t, collServedTL, "", *stagedTL); err != nil {
				return err
			}
		}

		// The generation is the promote commit point and remains LAST among live-state writes.
		if err := c.kv.setGeneration(t, newGen); err != nil {
			return err
		}

		// Post-commit cleanup is deliberately best-effort. A crash/error here leaves only stale
		// records whose seal generation no longer equals current+1, so they cannot promote again.
		for _, id := range ids {
			_ = c.kv.del(t, collStaged, id)
		}
		if stagedTL == nil {
			_ = c.kv.del(t, collStagedSeal, "")
			_ = c.kv.del(t, collStagedTL, "")
		} else {
			// Keep the last manifest readable for status/epoch compatibility, but replace the
			// candidate seal with an explicit historical marker that can never authorize promote or
			// be inherited by incremental StageBundle.
			_ = c.saveJSON(t, collStagedTLHist, "", *stagedTL)
			_ = c.saveJSON(t, collStagedSeal, "", stagedSetSeal{
				Generation:      newGen,
				Historical:      true,
				HasTrustList:    true,
				TrustListSHA256: trustListSHA256(stagedTL.TrustListJSON),
				TrustListEpoch:  stagedTL.Epoch,
			})
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	return newGen, nil
}
