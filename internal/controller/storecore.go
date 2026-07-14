package controller

// storecore.go — the ONE behavioral core (plan-8 keystone). Every custody/allocation rule the
// controller has is authored HERE, once, over the thin kvBackend port (kv.go): promote generation-
// scoping, the served-vs-staged keystone predicate + atomic served snapshot, monotonic anti-rollback,
// the TOTP watermark CAS, enrollment-token single-use burn, node API-token rotation, and — in the
// sibling storecore_telemetry.go — the volatile telemetry overlay. MemStore and FileStore are thin
// wrappers around a *storeCore over a memkv / filekv backend, so the impl used in every test is the
// impl that ships. The Store interface (store.go) is unchanged; a backend holds NO business rule.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/kunorikiku/yet-another-overlay-generator/internal/runtimecontract"
)

// storeCore is the store-agnostic behavioral core. It owns the telemetry overlay (its own second
// lock, see storecore_telemetry.go) and the bounded resource-history; every custody rule executes
// over c.kv under c.kv.withLock so a multi-record read-modify-write is atomic.
type storeCore struct {
	kv      kvBackend
	history *telemetryHistory

	// telemetryMu guards the volatile telemetry overlay (a SEPARATE lock from the backend's store
	// lock so a heartbeat never contends on the custody path). Lock order is ALWAYS backend-lock →
	// telemetryMu (GetNode/ListNodes/SetAppliedGeneration take the backend lock, then telemetryMu);
	// the heartbeat paths take ONLY telemetryMu (after a lock-free existence probe), so the two can
	// never deadlock. See storecore_telemetry.go.
	telemetryMu sync.Mutex
	telemetry   map[TenantID]map[string]*volatileTelemetry
}

// Compile-time assertion that *storeCore satisfies the whole Store interface (so MemStore/FileStore,
// which embed it, do too).
var _ Store = (*storeCore)(nil)

// newStoreCore wires the core to a backend + its resource-history buffer.
func newStoreCore(kv kvBackend, history *telemetryHistory) *storeCore {
	return &storeCore{kv: kv, history: history}
}

// apiTokenIndex is the value stored in collAPITokens: the reverse index from a node API token's
// hash to the owning NodeID (filekv persists it as apitokens/<hash>.json).
type apiTokenIndex struct {
	NodeID string `json:"node_id"`
}

// generationFile is the on-disk shape of a filekv generation counter; the core marshals it so the
// counter round-trips identically through either backend.
type generationFile struct {
	Generation int64 `json:"generation"`
}

// --- record (un)marshal helpers (caller holds withLock) ---------------------

// loadJSON reads collection/key and unmarshals into v, mapping a backend miss to the PUBLIC
// ErrNotFound (the ~4 methods that want ErrTokenInvalid / ErrChallengeInvalid translate it). Caller
// holds withLock.
func (c *storeCore) loadJSON(t TenantID, coll, key string, v any) error {
	b, err := c.kv.get(t, coll, key)
	if errors.Is(err, errKVNotFound) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if err := json.Unmarshal(b, v); err != nil {
		return fmt.Errorf("controller: parse %s/%s: %w", coll, key, err)
	}
	return nil
}

// saveJSON marshals v (pretty-printed, matching filekv's historical on-disk format byte-for-byte)
// and durably writes it to collection/key. Caller holds withLock.
func (c *storeCore) saveJSON(t TenantID, coll, key string, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return fmt.Errorf("controller: marshal %s/%s: %w", coll, key, err)
	}
	return c.kv.put(t, coll, key, b)
}

// =============================== Registry ==================================

// UpsertNode creates or updates a node registry record (matched by NodeID).
func (c *storeCore) UpsertNode(ctx context.Context, t TenantID, n Node) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return c.kv.withLock(func() error {
		return c.saveJSON(t, collNodes, n.NodeID, n)
	})
}

// GetNode returns the node with the live telemetry overlay merged, or ErrNotFound. This is the READ
// path for fleet views. A read-modify-write custody caller that persists the result must use
// GetNodeRecord (below), so the volatile overlay is never baked into the durable record.
func (c *storeCore) GetNode(ctx context.Context, t TenantID, nodeID string) (Node, error) {
	return c.getNode(ctx, t, nodeID, true)
}

// GetNodeRecord returns the DURABLE node record WITHOUT the telemetry overlay, or ErrNotFound. It is
// the read for a read-modify-write writeback (rekey flag, revoke, key rotation): reading durable truth
// means the subsequent UpsertNode persists exactly the custody fields — never a possibly-dead node's
// stale "healthy" live conditions/metrics/last-seen/agent-version baked in from the volatile overlay.
func (c *storeCore) GetNodeRecord(ctx context.Context, t TenantID, nodeID string) (Node, error) {
	return c.getNode(ctx, t, nodeID, false)
}

// getNode is the shared reader for GetNode/GetNodeRecord: it loads the durable record and, when
// withOverlay is set, merges the volatile telemetry overlay under telemetryMu, nested in the backend
// lock (matching the mu → telemetryMu order) so the (durable record, overlay) pair is a single
// snapshot. withOverlay=false returns the durable record verbatim (the RMW-writeback read).
func (c *storeCore) getNode(ctx context.Context, t TenantID, nodeID string, withOverlay bool) (Node, error) {
	if err := ctx.Err(); err != nil {
		return Node{}, err
	}
	var n Node
	err := c.kv.withLock(func() error {
		if err := c.loadJSON(t, collNodes, nodeID, &n); err != nil {
			return err
		}
		if withOverlay {
			c.applyTelemetryOverlay(t, &n)
		}
		return nil
	})
	if err != nil {
		return Node{}, err
	}
	return n, nil
}

// ListNodes returns all nodes for the tenant (stable order by NodeID), each with the live overlay
// merged.
func (c *storeCore) ListNodes(ctx context.Context, t TenantID) ([]Node, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	var out []Node
	err := c.kv.withLock(func() error {
		recs, err := c.kv.list(t, collNodes)
		if err != nil {
			return err
		}
		out = make([]Node, 0, len(recs))
		for _, r := range recs {
			var n Node
			if err := json.Unmarshal(r.val, &n); err != nil {
				return fmt.Errorf("controller: parse %s/%s: %w", collNodes, r.key, err)
			}
			c.applyTelemetryOverlay(t, &n)
			out = append(out, n)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// SetAppliedGeneration records what an agent reported applying (the applied generation, the manifest
// checksum, the free-form health string, the reported build version — "" leaves the stored version
// unchanged — and the server-stamped conditions set; a nil/empty slice clears it). It then refreshes
// the telemetry overlay so the just-written report is not shadowed by an older heartbeat on the next
// read (report↔heartbeat coherence, monotonic). Returns ErrNotFound if the node does not exist.
func (c *storeCore) SetAppliedGeneration(ctx context.Context, t TenantID, nodeID string, gen int64, checksum, health, agentVersion string, conditions []runtimecontract.Condition, observedAt time.Time) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return c.kv.withLock(func() error {
		var n Node
		if err := c.loadJSON(t, collNodes, nodeID, &n); err != nil {
			return err
		}
		n.AppliedGeneration = gen
		n.LastChecksum = checksum
		n.LastHealth = health
		if agentVersion != "" {
			n.LastAgentVersion = agentVersion
		}
		n.Conditions = stampConditions(conditions, observedAt)
		if err := c.saveJSON(t, collNodes, nodeID, n); err != nil {
			return err
		}
		c.refreshTelemetryOverlayFromReport(t, nodeID, n.Conditions, agentVersion, observedAt)
		return nil
	})
}

// =============================== Topology ==================================

// PutTopology stores a new topology version and returns the stored record with its assigned Version.
// The history file is written BEFORE topology.json flips, so a crash between the two leaves a harmless
// orphan (invisible to List/Get), never a current record missing from history.
func (c *storeCore) PutTopology(ctx context.Context, t TenantID, jsonBytes []byte) (TopologyRecord, error) {
	if err := ctx.Err(); err != nil {
		return TopologyRecord{}, err
	}
	var rec TopologyRecord
	err := c.kv.withLock(func() error {
		var prev TopologyRecord
		var prevVersion int64
		switch err := c.loadJSON(t, collTopology, "", &prev); {
		case err == nil:
			prevVersion = prev.Version
		case errors.Is(err, ErrNotFound):
			// no prior topology
		default:
			return err
		}
		rec = TopologyRecord{
			Version:   prevVersion + 1,
			JSON:      append([]byte(nil), jsonBytes...),
			UpdatedAt: time.Now().UTC(),
		}
		// Upgrade backfill: a deployment whose current topology predates the history feature has no
		// history file for it; write it lazily so the displaced version stays recoverable. Use get
		// (lock-free, in-lock) rather than the self-locking exists.
		if prevVersion > 0 {
			if _, err := c.kv.get(t, collTopoHistory, strconv.FormatInt(prevVersion, 10)); errors.Is(err, errKVNotFound) {
				if err := c.saveJSON(t, collTopoHistory, strconv.FormatInt(prevVersion, 10), prev); err != nil {
					return err
				}
			} else if err != nil {
				return err
			}
		}
		if err := c.saveJSON(t, collTopoHistory, strconv.FormatInt(rec.Version, 10), rec); err != nil {
			return err
		}
		if err := c.saveJSON(t, collTopology, "", rec); err != nil {
			return err
		}
		// Prune beyond the retention bound (best-effort — a leftover is re-pruned next put).
		if cutoff := rec.Version - TopologyHistoryLimit; cutoff > 0 {
			recs, err := c.kv.list(t, collTopoHistory)
			if err == nil {
				for _, r := range recs {
					if v, perr := strconv.ParseInt(r.key, 10, 64); perr == nil && v <= cutoff {
						_ = c.kv.del(t, collTopoHistory, r.key)
					}
				}
			}
		}
		return nil
	})
	if err != nil {
		return TopologyRecord{}, err
	}
	return rec, nil
}

// GetTopology returns the current topology, or ErrNotFound.
func (c *storeCore) GetTopology(ctx context.Context, t TenantID) (TopologyRecord, error) {
	if err := ctx.Err(); err != nil {
		return TopologyRecord{}, err
	}
	var rec TopologyRecord
	err := c.kv.withLock(func() error {
		return c.loadJSON(t, collTopology, "", &rec)
	})
	if err != nil {
		return TopologyRecord{}, err
	}
	return rec, nil
}

// ListTopologyVersions returns the retained versions, newest first. A crash orphan (history version >
// the committed current) is invisible; a corrupt entry is skipped (never bricks the recovery list);
// the current record always appears even when its history file is missing (upgrade shape).
func (c *storeCore) ListTopologyVersions(ctx context.Context, t TenantID) ([]TopologyVersionInfo, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	var out []TopologyVersionInfo
	err := c.kv.withLock(func() error {
		var cur TopologyRecord
		switch err := c.loadJSON(t, collTopology, "", &cur); {
		case err == nil:
		case errors.Is(err, ErrNotFound):
			out = []TopologyVersionInfo{}
			return nil
		default:
			return err
		}
		recs, err := c.kv.list(t, collTopoHistory)
		if err != nil {
			return err
		}
		out = make([]TopologyVersionInfo, 0, len(recs)+1)
		sawCurrent := false
		for _, r := range recs {
			v, perr := strconv.ParseInt(r.key, 10, 64)
			if perr != nil || v <= 0 || v > cur.Version {
				continue // foreign name, or a crash orphan newer than the committed record
			}
			var histRec TopologyRecord
			if json.Unmarshal(r.val, &histRec) != nil || histRec.Version != v {
				continue // unreadable/corrupt entry: skip, never brick the recovery list
			}
			if v == cur.Version {
				sawCurrent = true
			}
			out = append(out, TopologyVersionInfo{Version: histRec.Version, UpdatedAt: histRec.UpdatedAt, Bytes: len(histRec.JSON)})
		}
		if !sawCurrent {
			out = append(out, TopologyVersionInfo{Version: cur.Version, UpdatedAt: cur.UpdatedAt, Bytes: len(cur.JSON)})
		}
		sort.Slice(out, func(i, j int) bool { return out[i].Version > out[j].Version })
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// GetTopologyVersion returns one retained version, or ErrNotFound (unknown, pruned, or a crash orphan
// that never committed). The current record's version is always servable.
func (c *storeCore) GetTopologyVersion(ctx context.Context, t TenantID, version int64) (TopologyRecord, error) {
	if err := ctx.Err(); err != nil {
		return TopologyRecord{}, err
	}
	if version <= 0 {
		return TopologyRecord{}, ErrNotFound
	}
	var rec TopologyRecord
	err := c.kv.withLock(func() error {
		var cur TopologyRecord
		if err := c.loadJSON(t, collTopology, "", &cur); err != nil {
			return err // ErrNotFound when nothing ever committed
		}
		if version > cur.Version {
			return ErrNotFound // crash orphan / never existed
		}
		if version == cur.Version {
			rec = cur // the committed current record is authoritative for its version
			return nil
		}
		return c.loadJSON(t, collTopoHistory, strconv.FormatInt(version, 10), &rec)
	})
	if err != nil {
		return TopologyRecord{}, err
	}
	return rec, nil
}

// ========================= Bundles + generation ============================

// StageBundle stores a node's bundle as the staged (not-yet-current) version, replacing any prior.
func (c *storeCore) StageBundle(ctx context.Context, t TenantID, b SignedBundle) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return c.kv.withLock(func() error {
		b.IsStaged = true
		b.IsCurrent = false
		return c.saveJSON(t, collStaged, b.NodeID, b)
	})
}

// PruneStagedBundles deletes staged bundles whose NodeID is not in keep and returns the purged node
// IDs (stable order). Current bundles are never touched. A per-record delete failure is recorded but
// does not abort the loop — every actual removal is reported even if a later one fails.
func (c *storeCore) PruneStagedBundles(ctx context.Context, t TenantID, keep []string) ([]string, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	keepSet := make(map[string]bool, len(keep))
	for _, id := range keep {
		keepSet[id] = true
	}
	var purged []string
	var firstErr error
	err := c.kv.withLock(func() error {
		recs, err := c.kv.list(t, collStaged)
		if err != nil {
			return err
		}
		for _, r := range recs {
			if keepSet[r.key] {
				continue
			}
			if err := c.kv.del(t, collStaged, r.key); err != nil {
				if firstErr == nil {
					firstErr = fmt.Errorf("controller: prune staged bundle %s: %w", r.key, err)
				}
				continue
			}
			purged = append(purged, r.key)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Strings(purged)
	return purged, firstErr
}

// PromoteStaged atomically flips the staged bundles whose provisional Generation equals the generation
// being promoted (current+1) to current, bumps each promoted node's DesiredGeneration (only if a
// registry record exists — never creates one), copies a SIGNED staged trust-list into the served slot,
// and commits the new generation LAST (so the counter never runs ahead of the bundle/manifest pair),
// waking WaitForGeneration waiters. A stale provisional (invalidated by an interleaved bump/promote)
// stays staged. Returns ErrNoStagedBundle when nothing matches, changing NOTHING.
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

		recs, err := c.kv.list(t, collStaged)
		if err != nil {
			return err
		}
		var staged []SignedBundle // list is stable by key(=NodeID), so this is deterministic
		for _, r := range recs {
			var b SignedBundle
			if err := json.Unmarshal(r.val, &b); err != nil {
				return fmt.Errorf("controller: parse %s/%s: %w", collStaged, r.key, err)
			}
			if b.Generation != newGen {
				continue // stale provisional generation — not part of the stage being promoted
			}
			staged = append(staged, b)
		}
		if len(staged) == 0 {
			return ErrNoStagedBundle
		}

		for _, b := range staged {
			b.IsStaged = false
			b.IsCurrent = true
			b.Generation = newGen
			if err := c.saveJSON(t, collCurrent, b.NodeID, b); err != nil {
				return err
			}
			if err := c.kv.del(t, collStaged, b.NodeID); err != nil {
				return err
			}
			// Bump the promoted node's DesiredGeneration if a registry record exists.
			var n Node
			switch err := c.loadJSON(t, collNodes, b.NodeID, &n); {
			case err == nil:
				n.DesiredGeneration = newGen
				if err := c.saveJSON(t, collNodes, b.NodeID, n); err != nil {
					return err
				}
			case errors.Is(err, ErrNotFound):
				// no registry record for this node; nothing to update (never create one)
			default:
				return err
			}
		}

		// Served-slot promote gate: copy the staged trust-list into the served slot ONLY when it is
		// signed; an unsigned/absent staged manifest leaves the served slot intact.
		var stagedTL StoredTrustList
		switch err := c.loadJSON(t, collStagedTL, "", &stagedTL); {
		case err == nil:
			if len(stagedTL.SignatureJSON) > 0 {
				if err := c.saveJSON(t, collServedTL, "", stagedTL); err != nil {
					return err
				}
			}
		case errors.Is(err, ErrNotFound):
			// nothing staged; leave the served slot as-is
		default:
			return err
		}

		// Commit the generation LAST (and wake waiters) — the crash-atomicity invariant, documented
		// here because the ordering is load-bearing. Every bundle flip (collCurrent), each promoted
		// node's DesiredGeneration bump, and the served trust-list copy (collServedTL) are written
		// ABOVE, BEFORE this line. The generation counter is the single commit point the fleet keys
		// off: WaitForGeneration wakes parked agents ONLY on an advance, and it advances ONLY here. So
		// a process crash BEFORE this setGeneration leaves the prior generation as the served config —
		// the counter never runs ahead of a half-written promote, so no agent is woken to a torn
		// generation — and a re-run of PromoteStaged re-drives the flip. In-process the whole body runs
		// under one kv lock, so a concurrent GetServedConfig reader can never observe a torn
		// (old-bundle, new-manifest) pair. (A transient on-disk torn pair after a FileStore crash is
		// fail-closed at the agent's offline digest binding and self-repairing — see ServedConfig. This
		// is a DOC of the existing ordering, deliberately NOT a new atomic-snapshot / promote-in-progress
		// marker: generation-tagging the served slot would be wrong, per ServedConfig.)
		return c.kv.setGeneration(t, newGen)
	})
	if err != nil {
		return 0, err
	}
	return newGen, nil
}

// GetCurrentBundle returns the node's current (promoted) bundle, or ErrNotFound.
func (c *storeCore) GetCurrentBundle(ctx context.Context, t TenantID, nodeID string) (SignedBundle, error) {
	if err := ctx.Err(); err != nil {
		return SignedBundle{}, err
	}
	var b SignedBundle
	err := c.kv.withLock(func() error {
		return c.loadJSON(t, collCurrent, nodeID, &b)
	})
	if err != nil {
		return SignedBundle{}, err
	}
	return b, nil
}

// CurrentGeneration returns the tenant's current generation (0 if none promoted).
func (c *storeCore) CurrentGeneration(ctx context.Context, t TenantID) (int64, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	var gen int64
	err := c.kv.withLock(func() error {
		var e error
		gen, e = c.kv.generation(t)
		return e
	})
	return gen, err
}

// BumpGeneration advances the counter by one WITHOUT touching any bundle (a WAKE, not a deploy) and
// wakes any WaitForGeneration waiters.
func (c *storeCore) BumpGeneration(ctx context.Context, t TenantID) (int64, error) {
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
		return c.kv.setGeneration(t, newGen)
	})
	if err != nil {
		return 0, err
	}
	return newGen, nil
}

// WaitForGeneration blocks until the tenant generation is strictly greater than afterGen (returning
// it), or returns ctx.Err() if ctx is done first. The fast path is here; the backend-specific wake
// (memkv sync.Cond / filekv 200ms poll) is awaitGenerationChange.
func (c *storeCore) WaitForGeneration(ctx context.Context, t TenantID, afterGen int64) (int64, error) {
	// Fast path: already satisfied? (Available data is delivered ahead of cancellation.)
	var gen int64
	if err := c.kv.withLock(func() error {
		var e error
		gen, e = c.kv.generation(t)
		return e
	}); err != nil {
		return 0, err
	}
	if gen > afterGen {
		return gen, nil
	}
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	return c.kv.awaitGenerationChange(ctx, t, afterGen)
}

// =========================== Enrollment tokens =============================

// CreateEnrollmentToken stores a single-use, node-scoped, TTL token keyed by its hash (a later
// create with the same hash overwrites, matching the registry upsert semantics).
func (c *storeCore) CreateEnrollmentToken(ctx context.Context, t TenantID, tok EnrollmentToken) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return c.kv.withLock(func() error {
		return c.saveJSON(t, collTokens, tok.TokenHash, tok)
	})
}

// ConsumeEnrollmentToken atomically validates and burns a token: ErrTokenInvalid if absent, scoped to
// a different node, or expired; ErrTokenConsumed if already burned; else set ConsumedAt=now + persist.
func (c *storeCore) ConsumeEnrollmentToken(ctx context.Context, t TenantID, tokenHash, nodeID string, now time.Time) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return c.kv.withLock(func() error {
		var tok EnrollmentToken
		switch err := c.loadJSON(t, collTokens, tokenHash, &tok); {
		case err == nil:
		case errors.Is(err, ErrNotFound):
			return ErrTokenInvalid
		default:
			return err
		}
		if tok.NodeID != nodeID || !now.Before(tok.ExpiresAt) {
			return ErrTokenInvalid
		}
		if tok.ConsumedAt != nil {
			return ErrTokenConsumed
		}
		consumed := now
		tok.ConsumedAt = &consumed
		return c.saveJSON(t, collTokens, tokenHash, tok)
	})
}

// PurgeEnrollmentTokensForNode deletes every token scoped to nodeID, returning the count removed
// (absent tokens → 0, nil). An unreadable/garbage token record is skipped (best-effort GC).
func (c *storeCore) PurgeEnrollmentTokensForNode(ctx context.Context, t TenantID, nodeID string) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	n := 0
	err := c.kv.withLock(func() error {
		recs, err := c.kv.list(t, collTokens)
		if err != nil {
			return err
		}
		for _, r := range recs {
			var tok EnrollmentToken
			if json.Unmarshal(r.val, &tok) != nil {
				continue // skip unreadable/garbage; best-effort purge
			}
			if tok.NodeID == nodeID {
				if err := c.kv.del(t, collTokens, r.key); err != nil {
					return err
				}
				n++
			}
		}
		return nil
	})
	if err != nil {
		return n, err
	}
	return n, nil
}

// ====================== Passkey login challenges ===========================

// CreateLoginChallenge stores a single-use, operator-scoped, TTL login challenge keyed by its hash.
func (c *storeCore) CreateLoginChallenge(ctx context.Context, t TenantID, lc LoginChallenge) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return c.kv.withLock(func() error {
		return c.saveJSON(t, collLoginChal, lc.ChallengeHash, lc)
	})
}

// ConsumeLoginChallenge atomically validates and burns a login challenge by DELETING it:
// ErrChallengeInvalid if absent, expired, or scoped to a different operator; else delete + nil. An
// expired record is deleted (lazy GC); a wrong-operator record is left intact.
func (c *storeCore) ConsumeLoginChallenge(ctx context.Context, t TenantID, challengeHash, operator string, now time.Time) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return c.kv.withLock(func() error {
		var lc LoginChallenge
		switch err := c.loadJSON(t, collLoginChal, challengeHash, &lc); {
		case err == nil:
		case errors.Is(err, ErrNotFound):
			return ErrChallengeInvalid
		default:
			return err
		}
		if !now.Before(lc.ExpiresAt) {
			if err := c.kv.del(t, collLoginChal, challengeHash); err != nil { // expired: lazy GC
				return err
			}
			return ErrChallengeInvalid
		}
		if lc.Operator != operator {
			return ErrChallengeInvalid // not the caller's challenge to burn
		}
		return c.kv.del(t, collLoginChal, challengeHash) // success: single-use consume
	})
}

// ========================== Node API tokens ================================

// IssueNodeAPIToken stamps tokenHash onto the node and writes the reverse index; rotation is
// self-cleaning (the prior index entry is dropped first). ErrNotFound if no node record exists.
func (c *storeCore) IssueNodeAPIToken(ctx context.Context, t TenantID, nodeID, tokenHash string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return c.kv.withLock(func() error {
		var n Node
		if err := c.loadJSON(t, collNodes, nodeID, &n); err != nil {
			return err // ErrNotFound when no node record
		}
		if n.APITokenHash != "" && n.APITokenHash != tokenHash {
			if err := c.kv.del(t, collAPITokens, n.APITokenHash); err != nil {
				return err
			}
		}
		n.APITokenHash = tokenHash
		if err := c.saveJSON(t, collNodes, nodeID, n); err != nil {
			return err
		}
		return c.saveJSON(t, collAPITokens, tokenHash, apiTokenIndex{NodeID: nodeID})
	})
}

// LookupNodeByAPIToken resolves a presented hash to its Node via the reverse index, self-consistently:
// ErrTokenInvalid unless the index resolves to a live node whose own APITokenHash still equals the
// hash AND whose Status is NodeApproved (rejects an unmapped hash, a vanished node, a stale/orphaned
// index, and any non-approved node).
func (c *storeCore) LookupNodeByAPIToken(ctx context.Context, t TenantID, tokenHash string) (Node, error) {
	if err := ctx.Err(); err != nil {
		return Node{}, err
	}
	var n Node
	err := c.kv.withLock(func() error {
		var idx apiTokenIndex
		switch err := c.loadJSON(t, collAPITokens, tokenHash, &idx); {
		case err == nil:
		case errors.Is(err, ErrNotFound):
			return ErrTokenInvalid
		default:
			return err
		}
		switch err := c.loadJSON(t, collNodes, idx.NodeID, &n); {
		case err == nil:
		case errors.Is(err, ErrNotFound):
			return ErrTokenInvalid
		default:
			return err
		}
		if n.APITokenHash != tokenHash || n.Status != NodeApproved {
			return ErrTokenInvalid
		}
		return nil
	})
	if err != nil {
		return Node{}, err
	}
	return n, nil
}

// RevokeNodeAPIToken clears the node's APITokenHash and deletes the reverse index entry. Idempotent:
// a missing node or a node with no issued token is a no-op success.
func (c *storeCore) RevokeNodeAPIToken(ctx context.Context, t TenantID, nodeID string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return c.kv.withLock(func() error {
		var n Node
		switch err := c.loadJSON(t, collNodes, nodeID, &n); {
		case err == nil:
		case errors.Is(err, ErrNotFound):
			return nil // idempotent: no node, nothing to revoke
		default:
			return err
		}
		if n.APITokenHash == "" {
			return nil // idempotent: no issued token
		}
		oldHash := n.APITokenHash
		n.APITokenHash = ""
		if err := c.saveJSON(t, collNodes, nodeID, n); err != nil {
			return err
		}
		return c.kv.del(t, collAPITokens, oldHash)
	})
}

// ===================== Keystone: operator credential + trust-list ==========

// SetOperatorCredential pins (or replaces) the tenant's off-host operator signing credential. Pinning
// one turns KEYSTONE ON.
func (c *storeCore) SetOperatorCredential(ctx context.Context, t TenantID, cred OperatorCredential) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return c.kv.withLock(func() error {
		return c.saveJSON(t, collOperatorCred, "", cred)
	})
}

// GetOperatorCredential returns the pinned operator credential, or ErrNotFound (keystone OFF).
func (c *storeCore) GetOperatorCredential(ctx context.Context, t TenantID) (OperatorCredential, error) {
	if err := ctx.Err(); err != nil {
		return OperatorCredential{}, err
	}
	var cred OperatorCredential
	err := c.kv.withLock(func() error {
		return c.loadJSON(t, collOperatorCred, "", &cred)
	})
	if err != nil {
		return OperatorCredential{}, err
	}
	return cred, nil
}

// PutSignedTrustList stores (replacing any prior) the STAGED membership trust-list. It is NOT what
// /config serves; staging it must never disturb the served slot.
func (c *storeCore) PutSignedTrustList(ctx context.Context, t TenantID, sl StoredTrustList) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return c.kv.withLock(func() error {
		return c.saveJSON(t, collStagedTL, "", sl)
	})
}

// GetCurrentSignedTrustList returns the STAGED trust-list, or ErrNotFound when none is staged.
func (c *storeCore) GetCurrentSignedTrustList(ctx context.Context, t TenantID) (StoredTrustList, error) {
	if err := ctx.Err(); err != nil {
		return StoredTrustList{}, err
	}
	var sl StoredTrustList
	err := c.kv.withLock(func() error {
		return c.loadJSON(t, collStagedTL, "", &sl)
	})
	if err != nil {
		return StoredTrustList{}, err
	}
	return sl, nil
}

// GetServedTrustList returns the SERVED (last-promoted) trust-list, or ErrNotFound when nothing has
// been promoted under a keystone.
func (c *storeCore) GetServedTrustList(ctx context.Context, t TenantID) (StoredTrustList, error) {
	if err := ctx.Err(); err != nil {
		return StoredTrustList{}, err
	}
	var sl StoredTrustList
	err := c.kv.withLock(func() error {
		return c.loadJSON(t, collServedTL, "", &sl)
	})
	if err != nil {
		return StoredTrustList{}, err
	}
	return sl, nil
}

// GetServedConfig is the ATOMIC snapshot /config serves a node — its current bundle, whether the
// keystone is ON, and (when ON and signed) the served trust-list — all read under ONE lock so a
// concurrent PromoteStaged can never expose a torn (old-bundle, new-manifest) pair. ErrNotFound when
// the node has no current bundle.
func (c *storeCore) GetServedConfig(ctx context.Context, t TenantID, nodeID string) (ServedConfig, error) {
	if err := ctx.Err(); err != nil {
		return ServedConfig{}, err
	}
	var sc ServedConfig
	err := c.kv.withLock(func() error {
		var b SignedBundle
		if err := c.loadJSON(t, collCurrent, nodeID, &b); err != nil {
			return err // ErrNotFound when no current bundle
		}
		sc = ServedConfig{Bundle: b}
		// Keystone ON iff a pinned operator credential exists.
		var cred OperatorCredential
		switch err := c.loadJSON(t, collOperatorCred, "", &cred); {
		case err == nil:
			sc.KeystoneOn = true
		case errors.Is(err, ErrNotFound):
			// keystone OFF
		default:
			return err
		}
		if sc.KeystoneOn {
			var sl StoredTrustList
			switch err := c.loadJSON(t, collServedTL, "", &sl); {
			case err == nil:
				if len(sl.SignatureJSON) > 0 {
					sc.TrustList = sl
					sc.HasTrustList = true
				}
			case errors.Is(err, ErrNotFound):
				// nothing promoted under the keystone yet — fail-closed at /config
			default:
				return err
			}
		}
		return nil
	})
	if err != nil {
		return ServedConfig{}, err
	}
	return sc, nil
}

// ===================== Operators + sessions (login) ========================

// PutOperator creates or replaces an operator account (matched by Username).
func (c *storeCore) PutOperator(ctx context.Context, t TenantID, op Operator) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return c.kv.withLock(func() error {
		return c.saveJSON(t, collOperators, op.Username, op)
	})
}

// GetOperator returns the operator account, or ErrNotFound.
func (c *storeCore) GetOperator(ctx context.Context, t TenantID, username string) (Operator, error) {
	if err := ctx.Err(); err != nil {
		return Operator{}, err
	}
	var op Operator
	err := c.kv.withLock(func() error {
		return c.loadJSON(t, collOperators, username, &op)
	})
	if err != nil {
		return Operator{}, err
	}
	return op, nil
}

// ListOperators returns all operator accounts (stable order by Username).
func (c *storeCore) ListOperators(ctx context.Context, t TenantID) ([]Operator, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	var out []Operator
	err := c.kv.withLock(func() error {
		recs, err := c.kv.list(t, collOperators)
		if err != nil {
			return err
		}
		out = make([]Operator, 0, len(recs))
		for _, r := range recs {
			var op Operator
			if err := json.Unmarshal(r.val, &op); err != nil {
				return fmt.Errorf("controller: parse %s/%s: %w", collOperators, r.key, err)
			}
			out = append(out, op)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// DeleteOperator removes an operator account. Idempotent (a missing account is a no-op success);
// sessions are not cascaded (they expire on their own TTL).
func (c *storeCore) DeleteOperator(ctx context.Context, t TenantID, username string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return c.kv.withLock(func() error {
		return c.kv.del(t, collOperators, username)
	})
}

// AdvanceTOTPStep atomically advances the operator's replay watermark to step iff step is strictly
// greater than the stored value (the whole check-and-set under one lock, closing the login TOCTOU).
// Returns advanced=false without writing when the step was already consumed; ErrNotFound if absent.
func (c *storeCore) AdvanceTOTPStep(ctx context.Context, t TenantID, username string, step int64) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	advanced := false
	err := c.kv.withLock(func() error {
		var op Operator
		if err := c.loadJSON(t, collOperators, username, &op); err != nil {
			return err // ErrNotFound if absent
		}
		if step <= op.TOTPLastUsedStep {
			return nil // already consumed (replay / concurrent reuse) — no write, advanced stays false
		}
		op.TOTPLastUsedStep = step
		if err := c.saveJSON(t, collOperators, username, op); err != nil {
			return err
		}
		advanced = true
		return nil
	})
	if err != nil {
		return false, err
	}
	return advanced, nil
}

// CreateSession stores a minted operator session, keyed by its TokenHash.
func (c *storeCore) CreateSession(ctx context.Context, t TenantID, sess Session) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return c.kv.withLock(func() error {
		return c.saveJSON(t, collSessions, sess.TokenHash, sess)
	})
}

// LookupSession resolves a session token's hash to its Session, returning ErrTokenInvalid if absent
// OR expired (an expired session encountered here is lazily deleted).
func (c *storeCore) LookupSession(ctx context.Context, t TenantID, tokenHash string, now time.Time) (Session, error) {
	if err := ctx.Err(); err != nil {
		return Session{}, err
	}
	var sess Session
	err := c.kv.withLock(func() error {
		switch err := c.loadJSON(t, collSessions, tokenHash, &sess); {
		case err == nil:
		case errors.Is(err, ErrNotFound):
			return ErrTokenInvalid
		default:
			return err
		}
		if !now.Before(sess.ExpiresAt) {
			if err := c.kv.del(t, collSessions, tokenHash); err != nil {
				return err
			}
			sess = Session{}
			return ErrTokenInvalid
		}
		return nil
	})
	if err != nil {
		return Session{}, err
	}
	return sess, nil
}

// DeleteSession removes a session (logout / revoke). Idempotent.
func (c *storeCore) DeleteSession(ctx context.Context, t TenantID, tokenHash string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return c.kv.withLock(func() error {
		return c.kv.del(t, collSessions, tokenHash)
	})
}

// ===================== Controller settings + signing anchor ================

// GetSettings returns the tenant's saved settings, or ErrNotFound when none has been saved. It keeps
// the resource-history cap cache in sync (no disk on the append path).
func (c *storeCore) GetSettings(ctx context.Context, t TenantID) (ControllerSettings, error) {
	if err := ctx.Err(); err != nil {
		return ControllerSettings{}, err
	}
	var cs ControllerSettings
	err := c.kv.withLock(func() error {
		return c.loadJSON(t, collSettings, "", &cs)
	})
	if err != nil {
		return ControllerSettings{}, err
	}
	c.history.setCap(t, cs.EffectiveHistoryCap())
	return cs, nil
}

// PutSettings stores (replacing) the tenant's settings and tracks the operator's cap.
func (c *storeCore) PutSettings(ctx context.Context, t TenantID, cs ControllerSettings) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := c.kv.withLock(func() error {
		return c.saveJSON(t, collSettings, "", cs)
	}); err != nil {
		return err
	}
	c.history.setCap(t, cs.EffectiveHistoryCap())
	return nil
}

// GetSigningAnchor returns the tenant's pinned bundle-signing public key, or ErrNotFound when none.
func (c *storeCore) GetSigningAnchor(ctx context.Context, t TenantID) (SigningAnchor, error) {
	if err := ctx.Err(); err != nil {
		return SigningAnchor{}, err
	}
	var a SigningAnchor
	err := c.kv.withLock(func() error {
		return c.loadJSON(t, collSigningAnchor, "", &a)
	})
	if err != nil {
		return SigningAnchor{}, err
	}
	return a, nil
}

// PutSigningAnchor pins (replacing any prior) the tenant's signing public key.
func (c *storeCore) PutSigningAnchor(ctx context.Context, t TenantID, a SigningAnchor) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return c.kv.withLock(func() error {
		return c.saveJSON(t, collSigningAnchor, "", a)
	})
}

// ================================ Audit ====================================

// AppendAudit appends a hash-chained entry (delegated to the backend's append-only log; the chain
// crypto is shared via chainAudit).
func (c *storeCore) AppendAudit(ctx context.Context, t TenantID, e AuditEntry) (AuditEntry, error) {
	if err := ctx.Err(); err != nil {
		return AuditEntry{}, err
	}
	return c.kv.appendAudit(t, e)
}

// ListAudit returns the tenant's audit entries in Seq order.
func (c *storeCore) ListAudit(ctx context.Context, t TenantID) ([]AuditEntry, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return c.kv.listAudit(t)
}
