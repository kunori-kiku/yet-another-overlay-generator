package controller

// storecore.go — the ONE behavioral core (plan-8 keystone). Every custody/allocation rule the
// controller has is authored HERE, once, over the thin kvBackend port (kv.go): promote generation-
// scoping, the served-vs-staged keystone predicate + atomic served snapshot, monotonic anti-rollback,
// the TOTP watermark CAS, enrollment-token single-use burn, node API-token rotation, and — in the
// sibling storecore_telemetry.go — the volatile telemetry overlay. MemStore and FileStore are thin
// wrappers around a *storeCore over a memkv / filekv backend, so the impl used in every test is the
// impl that ships. Store is the public contract; a backend holds NO business rule.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
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
// Stage/replace/promote live in storecore_stage.go. Keeping the durable staged-set seal machinery
// together makes its invalidate-components-seal-last ordering reviewable as one unit.

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
		if err := c.requireCompletedPromotionLocked(t, cur); err != nil {
			return err
		}
		newGen = cur + 1
		// A wake invalidates every bundle compiled for the old current+1. Remove the authority
		// marker before advancing the counter; loose staged records remain inert and are replaced by
		// the next clean stage. Preserve the last trust-list only behind a Historical marker so
		// status/epoch compatibility survives rekey-all without making it promotable.
		var history StoredTrustList
		hasHistory := false
		if err := c.loadJSON(t, collStagedTLHist, "", &history); err == nil {
			hasHistory = true
		} else if !errors.Is(err, ErrNotFound) {
			return err
		} else if err := c.loadJSON(t, collStagedTL, "", &history); err == nil {
			hasHistory = true // pre-seal upgrade fallback
		} else if !errors.Is(err, ErrNotFound) {
			return err
		}
		if err := c.kv.del(t, collStagedSeal, ""); err != nil {
			return err
		}
		if hasHistory {
			if err := c.saveJSON(t, collStagedTLHist, "", history); err != nil {
				return err
			}
			if err := c.saveJSON(t, collStagedTL, "", history); err != nil {
				return err
			}
			if err := c.saveJSON(t, collStagedSeal, "", stagedSetSeal{
				Generation:      newGen,
				Historical:      true,
				HasTrustList:    true,
				TrustListSHA256: trustListSHA256(history.TrustListJSON),
				TrustListEpoch:  history.Epoch,
			}); err != nil {
				return err
			}
		} else if err := c.kv.del(t, collStagedTL, ""); err != nil {
			return err
		}
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

// ====================== WebAuthn assertion challenges ======================

// createAssertionChallenge stores a challenge after removing expired records and, when
// replaceSubject is true, any still-live record for the same subject. Caller does not hold a lock.
func (c *storeCore) createAssertionChallenge(ctx context.Context, t TenantID, challenge AssertionChallenge, now time.Time, replaceSubject bool) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return c.kv.withLock(func() error {
		recs, err := c.kv.list(t, collLoginChal)
		if err != nil {
			return err
		}
		for _, r := range recs {
			var prior AssertionChallenge
			if err := json.Unmarshal(r.val, &prior); err != nil {
				continue // preserve unreadable records; a storage audit should diagnose them
			}
			if !now.Before(prior.ExpiresAt) || replaceSubject && prior.Subject == challenge.Subject {
				if err := c.kv.del(t, collLoginChal, r.key); err != nil {
					return err
				}
			}
		}
		return c.saveJSON(t, collLoginChal, challenge.ChallengeHash, challenge)
	})
}

// CreateAssertionChallenge stores a single-use challenge and garbage-collects expired records.
func (c *storeCore) CreateAssertionChallenge(ctx context.Context, t TenantID, challenge AssertionChallenge, now time.Time) error {
	return c.createAssertionChallenge(ctx, t, challenge, now, false)
}

// ReplaceAssertionChallengeForSubject bounds browser enrollment to one live challenge per
// purpose+actor subject while leaving ordinary login's concurrent challenges unchanged.
func (c *storeCore) ReplaceAssertionChallengeForSubject(ctx context.Context, t TenantID, challenge AssertionChallenge, now time.Time) error {
	return c.createAssertionChallenge(ctx, t, challenge, now, true)
}

// ConsumeAssertionChallenge atomically validates and burns a challenge by DELETING it:
// ErrChallengeInvalid if absent, expired, or scoped to a different subject; else delete + nil. An
// expired record is deleted (lazy GC); a wrong-subject record is left intact.
func (c *storeCore) ConsumeAssertionChallenge(ctx context.Context, t TenantID, challengeHash, subject string, now time.Time) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return c.kv.withLock(func() error {
		var challenge AssertionChallenge
		switch err := c.loadJSON(t, collLoginChal, challengeHash, &challenge); {
		case err == nil:
		case errors.Is(err, ErrNotFound):
			return ErrChallengeInvalid
		default:
			return err
		}
		if !now.Before(challenge.ExpiresAt) {
			if err := c.kv.del(t, collLoginChal, challengeHash); err != nil { // expired: lazy GC
				return err
			}
			return ErrChallengeInvalid
		}
		if challenge.Subject != subject {
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

// CompareAndSetOperatorCredential conditionally replaces the tenant keystone in one store-lock
// scope. Exact struct equality is intentional: OperatorCredential contains only strings, and the
// caller supplies the precise snapshot it classified. Semantic key equality belongs above this
// storage primitive (SameKeystoneCredential); this method only detects intervening state changes.
func (c *storeCore) CompareAndSetOperatorCredential(ctx context.Context, t TenantID, expected *OperatorCredential, next OperatorCredential) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return c.kv.withLock(func() error {
		var current OperatorCredential
		err := c.loadJSON(t, collOperatorCred, "", &current)
		if expected == nil {
			if err == nil {
				return ErrOperatorCredentialChanged
			}
			if !errors.Is(err, ErrNotFound) {
				return err
			}
		} else {
			if err != nil {
				if errors.Is(err, ErrNotFound) {
					return ErrOperatorCredentialChanged
				}
				return err
			}
			if current != *expected {
				return ErrOperatorCredentialChanged
			}
			if current == next {
				return nil // compare-only idempotent path: detect races without rewriting
			}
		}
		return c.saveJSON(t, collOperatorCred, "", next)
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

// CreatePendingKeystoneTransition creates the durable audit/CAS recovery marker without ever
// clobbering another unresolved event. The marker contains no private material; nevertheless it
// follows the same 0600 durable-record path as the credential.
func (c *storeCore) CreatePendingKeystoneTransition(ctx context.Context, t TenantID, pending PendingKeystoneTransition) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if pending.Audit.EventID == "" || pending.Audit.Timestamp.IsZero() {
		return errors.New("controller: pending keystone transition requires an audit event id and timestamp")
	}
	return c.kv.withLock(func() error {
		var current PendingKeystoneTransition
		if err := c.loadJSON(t, collKeystoneTransition, "", &current); err == nil {
			if current.Audit.EventID == pending.Audit.EventID && reflect.DeepEqual(current, pending) {
				return nil
			}
			return fmt.Errorf("%w: unresolved event %q would be replaced by %q", ErrPendingKeystoneTransitionConflict, current.Audit.EventID, pending.Audit.EventID)
		} else if !errors.Is(err, ErrNotFound) {
			return err
		}
		return c.saveJSON(t, collKeystoneTransition, "", pending)
	})
}

// GetPendingKeystoneTransition returns the pending marker, or ErrNotFound.
func (c *storeCore) GetPendingKeystoneTransition(ctx context.Context, t TenantID) (PendingKeystoneTransition, error) {
	if err := ctx.Err(); err != nil {
		return PendingKeystoneTransition{}, err
	}
	var pending PendingKeystoneTransition
	err := c.kv.withLock(func() error {
		return c.loadJSON(t, collKeystoneTransition, "", &pending)
	})
	if err != nil {
		return PendingKeystoneTransition{}, err
	}
	return pending, nil
}

// DeletePendingKeystoneTransition idempotently clears the reconciled marker with eventID. A stale
// cleanup can never erase a newer unresolved transition.
func (c *storeCore) DeletePendingKeystoneTransition(ctx context.Context, t TenantID, eventID string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if eventID == "" {
		return errors.New("controller: pending keystone transition deletion requires an event id")
	}
	return c.kv.withLock(func() error {
		var current PendingKeystoneTransition
		if err := c.loadJSON(t, collKeystoneTransition, "", &current); err != nil {
			if errors.Is(err, ErrNotFound) {
				return nil
			}
			return err
		}
		if current.Audit.EventID != eventID {
			return fmt.Errorf("%w: cleanup event %q does not match unresolved event %q", ErrPendingKeystoneTransitionConflict, eventID, current.Audit.EventID)
		}
		return c.kv.del(t, collKeystoneTransition, "")
	})
}

// PutSignedTrustList retains the manifest-first/signature-fill Store API. A canonical-byte/epoch
// change invalidates the candidate seal before writing the manifest and re-seals the exact
// next-generation bundle set afterward. Updating only SignatureJSON over the same sealed bytes is a
// single-record atomic write and deliberately leaves the seal unchanged.
func (c *storeCore) PutSignedTrustList(ctx context.Context, t TenantID, sl StoredTrustList) error {
	if err := ctx.Err(); err != nil {
		return err
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

		// The signature installation path is allowed to mutate only SignatureJSON over the same
		// bytes/epoch named by the existing seal. No component invalidation is needed for that
		// atomic one-file replacement.
		if seal, sealErr := c.loadSealLocked(t); sealErr == nil && (seal.Historical || seal.Generation == next) && seal.HasTrustList &&
			seal.TrustListEpoch == sl.Epoch && seal.TrustListSHA256 == trustListSHA256(sl.TrustListJSON) {
			if err := c.saveJSON(t, collStagedTLHist, "", sl); err != nil {
				return err
			}
			return c.saveJSON(t, collStagedTL, "", sl)
		}

		if err := c.kv.del(t, collStagedSeal, ""); err != nil {
			return err
		}
		if err := c.saveJSON(t, collStagedTL, "", sl); err != nil {
			return err
		}
		if err := c.saveJSON(t, collStagedTLHist, "", sl); err != nil {
			return err
		}
		// allowEmpty preserves legacy manifest-first callers: the manifest is fetchable/signable,
		// but PromoteStaged still refuses until at least one exact bundle is sealed with it.
		return c.saveSealForGenerationLocked(t, next, &sl, true)
	})
}

// GetCurrentSignedTrustList returns only a trust-list that matches a staged-set seal (a pending
// candidate or its explicit non-promotable historical marker). Loose or partially-written manifest
// bytes are intentionally invisible after a failed/crashed stage.
func (c *storeCore) GetCurrentSignedTrustList(ctx context.Context, t TenantID) (StoredTrustList, error) {
	if err := ctx.Err(); err != nil {
		return StoredTrustList{}, err
	}
	var sl StoredTrustList
	err := c.kv.withLock(func() error {
		seal, err := c.loadSealLocked(t)
		if err != nil {
			return err
		}
		sealed, err := c.loadSealedTrustListLocked(t, seal)
		if err != nil {
			return err
		}
		if sealed == nil {
			return ErrNotFound
		}
		sl = *sealed
		return nil
	})
	if err != nil {
		return StoredTrustList{}, err
	}
	return sl, nil
}

// GetLastStagedTrustList is the epoch-history read for CompileAndStage. It never participates in
// promotion. The fallback order lazily supports pre-seal FileStore deployments: old active staged
// data first, then the last served manifest when no dedicated history record exists yet.
func (c *storeCore) GetLastStagedTrustList(ctx context.Context, t TenantID) (StoredTrustList, error) {
	if err := ctx.Err(); err != nil {
		return StoredTrustList{}, err
	}
	var sl StoredTrustList
	err := c.kv.withLock(func() error {
		if err := c.loadJSON(t, collStagedTLHist, "", &sl); err == nil {
			return nil
		} else if !errors.Is(err, ErrNotFound) {
			return err
		}
		if err := c.loadJSON(t, collStagedTL, "", &sl); err == nil {
			return nil
		} else if !errors.Is(err, ErrNotFound) {
			return err
		}
		return c.loadJSON(t, collServedTL, "", &sl)
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
		if err := c.loadJSON(t, collServedTL, "", &sl); err != nil {
			return err
		}
		committed, err := c.kv.generation(t)
		if err != nil {
			return err
		}
		if sl.PromotedGeneration > committed {
			return fmt.Errorf("%w: served trust-list generation %d is ahead of committed generation %d", ErrUncommittedPromotion, sl.PromotedGeneration, committed)
		}
		return nil
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
		committed, err := c.kv.generation(t)
		if err != nil {
			return err
		}
		// PromoteStaged writes current bundles before generation.json. A process crash can
		// therefore leave a subset of next-generation bundle files on disk. Never serve one
		// until the tenant-wide commit marker catches up; an older bundle remains legitimate
		// because delta-skipped nodes intentionally retain earlier generations.
		if b.Generation > committed {
			return fmt.Errorf("%w: bundle generation %d is ahead of committed generation %d", ErrUncommittedPromotion, b.Generation, committed)
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
				if sl.PromotedGeneration > committed {
					return fmt.Errorf("%w: served trust-list generation %d is ahead of committed generation %d", ErrUncommittedPromotion, sl.PromotedGeneration, committed)
				}
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

func sameLoginCredential(a, b *LoginCredential) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

// CompareAndSetLoginCredential is a field-scoped operator update. It preserves every concurrent
// account field outside LoginCredential and rejects a stale expected credential rather than
// letting a delayed registration/disable ceremony overwrite newer passkey state.
func (c *storeCore) CompareAndSetLoginCredential(ctx context.Context, t TenantID, username string, expected, next *LoginCredential, now time.Time) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return c.kv.withLock(func() error {
		var op Operator
		if err := c.loadJSON(t, collOperators, username, &op); err != nil {
			return err
		}
		if !sameLoginCredential(op.LoginCredential, expected) {
			return ErrLoginCredentialChanged
		}
		if next == nil {
			op.LoginCredential = nil
		} else {
			copy := *next
			op.LoginCredential = &copy
		}
		op.UpdatedAt = now
		return c.saveJSON(t, collOperators, username, op)
	})
}

// CompareAndSetTOTPState is the TOTP counterpart to CompareAndSetLoginCredential: update only the
// TOTP configuration/replay fields and preserve all unrelated account state. Exact expected-state
// comparison ensures a delayed confirm/disable cannot overwrite a newer TOTP choice or replay step.
func (c *storeCore) CompareAndSetTOTPState(ctx context.Context, t TenantID, username string, expected, next TOTPState, now time.Time) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return c.kv.withLock(func() error {
		var op Operator
		if err := c.loadJSON(t, collOperators, username, &op); err != nil {
			return err
		}
		current := TOTPState{Secret: op.TOTPSecret, LastUsedStep: op.TOTPLastUsedStep}
		if current != expected {
			return ErrTOTPStateChanged
		}
		op.TOTPSecret = next.Secret
		op.TOTPLastUsedStep = next.LastUsedStep
		op.UpdatedAt = now
		return c.saveJSON(t, collOperators, username, op)
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
