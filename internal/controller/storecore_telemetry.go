package controller

// storecore_telemetry.go — the VOLATILE telemetry overlay, authored ONCE in the core (plan-8). It was
// FileStore's deliberate DoS-hardening (a 30s heartbeat must never fsync the whole node record); the
// collapse moves it into storeCore so MemStore ADOPTS the identical volatile + monotonic + clone-on-read
// semantics — strictly safer than MemStore's old durable RecordTelemetry (which had no monotonic gate
// and aliased the stored maps). The overlay merges over the durable node record on GetNode/ListNodes
// ONLY, never on any served/custody path, and touches only the four OBSERVABILITY fields
// (LastSeen/Conditions/Telemetry/LastAgentVersion) — never AppliedGeneration/LastChecksum/LastHealth/
// DesiredGeneration/keys, which stay on the durable write path.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/kunorikiku/yet-another-overlay-generator/internal/runtimecontract"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/telemetrymetric"
)

// volatileTelemetry is the in-memory-only overlay of a node's observability fields, written by the
// heartbeat paths WITHOUT a durable rewrite and merged over the durable record on read. The *Set flags
// distinguish "never overlaid" from a written zero value; writtenAt is the server observedAt of the
// last conditions/metrics/version write and gates a monotonic last-writer-wins so a /report's fresh
// conditions are never permanently shadowed by an older heartbeat (and vice-versa).
type volatileTelemetry struct {
	writtenAt    time.Time
	conditions   []NodeCondition
	telemetry    map[string]json.RawMessage
	agentVersion string
	telemetrySet bool
	lastSeen     time.Time
	lastSeenSet  bool
	// sequenceCursors bounds exact-retry suppression to the most recently observed agent boots.
	// It is intentionally volatile: making it durable would add a write/fsync to every heartbeat.
	sequenceCursors map[string]telemetrySequenceCursor
	// retiredBoots remembers a second bounded tier of evicted cursor identities. It lets a genuinely
	// new restart win by receipt inside the clock-skew window without mistaking a delayed evicted boot
	// for that restart. Values are last receipt times used only for deterministic bounded eviction.
	retiredBoots map[string]time.Time
	// activeBootID prevents a delayed sample from a known retired agent process from replacing live
	// state produced by its successor. activeSampledAt helps identify a previously unseen stale boot.
	activeBootID    string
	activeSampledAt time.Time
}

type telemetrySequenceCursor struct {
	highestSequence uint64
	lastSampledAt   time.Time
	lastReceivedAt  time.Time
}

// Keep the controller's per-node retry state strictly bounded even if a compromised authenticated
// node invents a new boot id for every request. Four covers a normal restart plus delayed in-flight
// requests without turning telemetry into an unbounded memory surface.
const (
	maxTelemetrySequenceCursors = 4
	maxTelemetryRetiredBoots    = 8
	maxTelemetryReplayAge       = 24 * time.Hour
	maxTelemetryFutureSkew      = 5 * time.Minute
	telemetryBootStaleSlack     = 5 * time.Minute
)

// telemetryEntryLocked returns the overlay entry for (t, nodeID), lazily creating the maps and entry.
// The caller MUST hold telemetryMu.
func (c *storeCore) telemetryEntryLocked(t TenantID, nodeID string) *volatileTelemetry {
	if c.telemetry == nil {
		c.telemetry = make(map[TenantID]map[string]*volatileTelemetry)
	}
	m := c.telemetry[t]
	if m == nil {
		m = make(map[string]*volatileTelemetry)
		c.telemetry[t] = m
	}
	ent := m[nodeID]
	if ent == nil {
		ent = &volatileTelemetry{}
		m[nodeID] = ent
	}
	return ent
}

// applyTelemetryOverlay merges the volatile overlay OVER a durable node record, deep-copying the
// slice/map so a returned Node can never alias (and later mutate) the shared overlay. Custody fields
// are never touched. Takes telemetryMu; the caller holds the backend lock (order backend → telemetryMu).
func (c *storeCore) applyTelemetryOverlay(t TenantID, n *Node) {
	c.telemetryMu.Lock()
	defer c.telemetryMu.Unlock()
	m := c.telemetry[t]
	if m == nil {
		return
	}
	ent := m[n.NodeID]
	if ent == nil {
		return
	}
	if ent.lastSeenSet {
		n.LastSeen = ent.lastSeen
	}
	if ent.telemetrySet {
		n.Conditions = cloneNodeConditions(ent.conditions)
		n.Telemetry = cloneMetrics(ent.telemetry)
		if ent.agentVersion != "" {
			n.LastAgentVersion = ent.agentVersion
		}
	}
}

// refreshTelemetryOverlayFromReport keeps the overlay coherent with a just-persisted /report: the
// report's fresher conditions (+ agent version, + last-seen) win over an older heartbeat, gated by the
// same monotonic writtenAt so a concurrent newer heartbeat is not regressed. It does NOT touch the
// overlay's metrics — /report carries none, so the last heartbeat's metrics persist. Takes telemetryMu.
func (c *storeCore) refreshTelemetryOverlayFromReport(t TenantID, nodeID string, conditions []NodeCondition, agentVersion string, observedAt time.Time) {
	c.telemetryMu.Lock()
	defer c.telemetryMu.Unlock()
	ent := c.telemetryEntryLocked(t, nodeID)
	if !ent.telemetrySet || !observedAt.Before(ent.writtenAt) { // observedAt >= writtenAt
		ent.conditions = cloneNodeConditions(conditions)
		ent.telemetrySet = true
		ent.writtenAt = observedAt
		if agentVersion != "" {
			ent.agentVersion = agentVersion
		}
	}
	if !ent.lastSeenSet || observedAt.After(ent.lastSeen) {
		ent.lastSeen = observedAt
		ent.lastSeenSet = true
	}
}

// cloneNodeConditions returns a deep copy of a stamped-conditions slice (nil-safe).
func cloneNodeConditions(in []NodeCondition) []NodeCondition {
	if in == nil {
		return nil
	}
	out := make([]NodeCondition, len(in))
	copy(out, in)
	return out
}

// cloneMetrics returns a deep copy of a metrics map, including each json.RawMessage byte slice.
// RawMessage is a []byte rather than an immutable value: callers may reuse or mutate request buffers,
// and readers may mutate a returned Node in place. Neither direction may alias the shared overlay.
func cloneMetrics(in map[string]json.RawMessage) map[string]json.RawMessage {
	if in == nil {
		return nil
	}
	out := make(map[string]json.RawMessage, len(in))
	for k, v := range in {
		out[k] = append(json.RawMessage(nil), v...)
	}
	return out
}

// cloneLiveMetrics applies the known-key latest/live-surface policy while taking byte ownership.
// Unknown keys remain visible deliberately so rolling a newer agent before its controller does not
// erase forward-compatible telemetry. History projection always receives the full admitted map before
// this filter is applied; in particular probe_samples is retained but not echoed through Fleet.
func cloneLiveMetrics(in map[string]json.RawMessage) map[string]json.RawMessage {
	if in == nil {
		return nil
	}
	out := make(map[string]json.RawMessage, len(in))
	for key, value := range in {
		if !telemetrymetric.VisibleOnLiveSurface(key) {
			continue
		}
		out[key] = append(json.RawMessage(nil), value...)
	}
	return out
}

// RecordTelemetry writes a LIVE health heartbeat to the in-memory overlay ONLY — conditions + metrics +
// last-seen (+ agent version when non-empty). It is a strict subset of SetAppliedGeneration (never
// touches AppliedGeneration/LastChecksum/LastHealth/DesiredGeneration) AND performs NO durable rewrite:
// a 30s heartbeat must not fsync the whole node record (the DoS vector). Node existence is confirmed
// with the metadata-only kv.exists (a corrupt-but-present record no longer fails a heartbeat). A
// monotonic writtenAt guard drops a stale write so a concurrent /report's fresher conditions win.
func (c *storeCore) RecordTelemetry(ctx context.Context, t TenantID, nodeID string, conditions []runtimecontract.Condition, metrics map[string]json.RawMessage, agentVersion string, observedAt time.Time) error {
	if err := c.telemetryNodeExists(ctx, t, nodeID); err != nil {
		return err
	}
	c.telemetryMu.Lock()
	defer c.telemetryMu.Unlock()
	ent := c.telemetryEntryLocked(t, nodeID)
	// An accepted unsequenced heartbeat that changes the capability advertisement is a live
	// replacement boundary. Retire the previously active boot before replacing its map so a delayed
	// higher-sequence replay from that old process can still contribute history/liveness but cannot
	// restore executable-policy readiness. A header-stripped v2 heartbeat carries the same canonical
	// capability body and deliberately keeps ownership, preserving safe proxy degradation.
	_, currentCapabilities := ent.telemetry[telemetrymetric.AgentCapabilitiesKey]
	_, incomingCapabilities := metrics[telemetrymetric.AgentCapabilitiesKey]
	capabilityReplacement := !incomingCapabilities ||
		(currentCapabilities && !sameAgentCapabilityAdvertisement(ent.telemetry, metrics))
	if ent.telemetrySet && !observedAt.Before(ent.writtenAt) && ent.activeBootID != "" &&
		capabilityReplacement {
		previousAt := ent.activeSampledAt
		if previous, ok := ent.sequenceCursors[ent.activeBootID]; ok && !previous.lastReceivedAt.IsZero() {
			previousAt = previous.lastReceivedAt
		}
		rememberRetiredTelemetryBoot(ent, ent.activeBootID, previousAt)
		ent.activeBootID = ""
		ent.activeSampledAt = time.Time{}
	}
	c.recordTelemetryLocked(t, nodeID, ent, conditions, metrics, agentVersion, observedAt, observedAt, 0)
	return nil
}

func sameAgentCapabilityAdvertisement(current, next map[string]json.RawMessage) bool {
	currentRaw, currentOK := current[telemetrymetric.AgentCapabilitiesKey]
	nextRaw, nextOK := next[telemetrymetric.AgentCapabilitiesKey]
	return currentOK == nextOK && (!currentOK || bytes.Equal(currentRaw, nextRaw))
}

// RecordTelemetrySequenced acknowledges and records one reliable telemetry sample. The cursor key is
// (node identity from bearer auth, agent boot id); sequence values are monotonic within that boot. An
// already-acknowledged sequence proves the node reached us now (so LastSeen may advance) but must not
// overwrite newer live metrics or append duplicate history. receivedAt, not the node clock, remains
// authoritative for LastSeen and condition ObservedAt. Bounded sampledAt timestamps history so replay
// reconstructs the sampling timeline and helps arbitrate stale or ambiguous cross-boot live-overlay
// ownership; it never establishes liveness.
func (c *storeCore) RecordTelemetrySequenced(ctx context.Context, t TenantID, nodeID string, conditions []runtimecontract.Condition, metrics map[string]json.RawMessage, agentVersion, bootID string, sequence uint64, sampledAt time.Time, interval time.Duration, receivedAt time.Time) (TelemetryReceipt, error) {
	sampledAt = sampledAt.UTC()
	receivedAt = receivedAt.UTC()
	if interval < 0 {
		interval = 0
	}
	receipt := TelemetryReceipt{
		AcknowledgedSequence: sequence,
		SampledAt:            sampledAt,
		ReceivedAt:           receivedAt,
	}
	if strings.TrimSpace(bootID) == "" {
		return TelemetryReceipt{}, fmt.Errorf("controller: telemetry boot id is empty")
	}
	if sequence == 0 {
		return TelemetryReceipt{}, fmt.Errorf("controller: telemetry sequence must be positive")
	}
	if sampledAt.IsZero() || receivedAt.IsZero() {
		return TelemetryReceipt{}, fmt.Errorf("controller: telemetry timestamps must be non-zero")
	}
	if err := c.telemetryNodeExists(ctx, t, nodeID); err != nil {
		return TelemetryReceipt{}, err
	}

	c.telemetryMu.Lock()
	defer c.telemetryMu.Unlock()
	ent := c.telemetryEntryLocked(t, nodeID)
	if ent.sequenceCursors == nil {
		ent.sequenceCursors = make(map[string]telemetrySequenceCursor)
	}
	if cursor, ok := ent.sequenceCursors[bootID]; ok && sequence <= cursor.highestSequence {
		receipt.Duplicate = true
		if receivedAt.After(cursor.lastReceivedAt) {
			cursor.lastReceivedAt = receivedAt
			ent.sequenceCursors[bootID] = cursor
		}
		// A retry is still a freshly authenticated request, so it is valid liveness evidence. Do not
		// touch conditions, metrics, agent version, or history: those belong to the original sample.
		if !ent.lastSeenSet || receivedAt.After(ent.lastSeen) {
			ent.lastSeen = receivedAt
			ent.lastSeenSet = true
		}
		return receipt, nil
	}

	cursor, seen := ent.sequenceCursors[bootID]
	historyAt, sampledAtTrusted := normalizeTelemetrySampleTime(sampledAt, receivedAt)
	_, retiredBoot := ent.retiredBoots[bootID]
	live := !retiredBoot && (ent.activeBootID == "" || ent.activeBootID == bootID)
	crossBoot := ent.activeBootID != "" && ent.activeBootID != bootID
	ambiguousCrossBoot := crossBoot && (!sampledAtTrusted ||
		(!ent.activeSampledAt.IsZero() && !sampledAt.After(ent.activeSampledAt)))
	if crossBoot {
		switch {
		case retiredBoot:
			live = false
		case seen:
			// A cursor can become "retired" only because a delayed, previously evicted boot landed
			// inside the restart-skew window. Let the known current boot reclaim the live overlay once
			// it supplies a newer trusted sample.
			live = sampledAtTrusted && (ent.activeSampledAt.IsZero() || sampledAt.After(ent.activeSampledAt))
		case sampledAtTrusted && !ent.activeSampledAt.IsZero() && sampledAt.Before(ent.activeSampledAt.Add(-telemetryBootStaleSlack)):
			live = false
		default:
			// A previously unseen boot inside the bounded skew window is a restart. Receipt order is
			// authoritative for live state; the agent clock remains advisory history metadata.
			live = true
		}
	}
	if ambiguousCrossBoot && ent.telemetrySet {
		// Capability readiness fails closed at every ambiguous restart boundary, even if the sample
		// is too old to replace the rest of the live overlay. This prevents a downgraded new process
		// (or a delayed evicted process) from inheriting another boot's executable-support claim.
		delete(ent.telemetry, telemetrymetric.AgentCapabilitiesKey)
	}
	if seen && !cursor.lastSampledAt.IsZero() && sampledAt.Before(cursor.lastSampledAt) {
		historyAt = receivedAt
	}
	if live && !ent.activeSampledAt.IsZero() && historyAt.Before(ent.activeSampledAt) {
		historyAt = receivedAt
	}

	if !seen && len(ent.sequenceCursors) >= maxTelemetrySequenceCursors {
		retiredID, retiredAt := evictOldestTelemetryCursor(ent.sequenceCursors, ent.activeBootID)
		rememberRetiredTelemetryBoot(ent, retiredID, retiredAt)
	}
	cursor.highestSequence = sequence
	if cursor.lastSampledAt.IsZero() || sampledAt.After(cursor.lastSampledAt) {
		cursor.lastSampledAt = sampledAt
	}
	if receivedAt.After(cursor.lastReceivedAt) {
		cursor.lastReceivedAt = receivedAt
	}
	ent.sequenceCursors[bootID] = cursor

	if !ent.lastSeenSet || receivedAt.After(ent.lastSeen) {
		ent.lastSeen = receivedAt
		ent.lastSeenSet = true
	}
	if live {
		if crossBoot {
			previousBootID := ent.activeBootID
			previousAt := ent.activeSampledAt
			if previous, ok := ent.sequenceCursors[previousBootID]; ok && !previous.lastReceivedAt.IsZero() {
				previousAt = previous.lastReceivedAt
			}
			// A boot generation that has been superseded must never reauthorize itself merely by
			// presenting a later advisory wall-clock sample. Remember it independently of cursor
			// eviction so receipt-ordered restart semantics remain monotonic.
			rememberRetiredTelemetryBoot(ent, previousBootID, previousAt)
		}
		ent.activeBootID = bootID
		if ent.activeSampledAt.IsZero() || historyAt.After(ent.activeSampledAt) {
			ent.activeSampledAt = historyAt
		}
		liveMetrics := metrics
		if ambiguousCrossBoot {
			liveMetrics = cloneMetrics(metrics)
			delete(liveMetrics, telemetrymetric.AgentCapabilitiesKey)
		}
		c.recordTelemetryLocked(t, nodeID, ent, conditions, liveMetrics, agentVersion, receivedAt, historyAt, interval)
	} else {
		c.appendTelemetryHistoryLocked(t, nodeID, metrics, historyAt, interval)
	}
	return receipt, nil
}

func (c *storeCore) telemetryNodeExists(ctx context.Context, t TenantID, nodeID string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	// Metadata-only existence probe OUTSIDE the custody lock (self-synchronizing; see kv.go).
	ok, err := c.kv.exists(t, collNodes, nodeID)
	if err != nil {
		return err
	}
	if !ok {
		return ErrNotFound
	}
	return nil
}

func normalizeTelemetrySampleTime(sampledAt, receivedAt time.Time) (time.Time, bool) {
	if sampledAt.Before(receivedAt.Add(-maxTelemetryReplayAge)) || sampledAt.After(receivedAt.Add(maxTelemetryFutureSkew)) {
		return receivedAt, false
	}
	return sampledAt, true
}

// recordTelemetryLocked applies an accepted sample. observedAt is controller-authoritative receipt
// time for the live overlay; historyAt is the distinct observation time (equal for legacy callers,
// sampled_at for sequenced callers). The caller holds telemetryMu.
func (c *storeCore) recordTelemetryLocked(t TenantID, nodeID string, ent *volatileTelemetry, conditions []runtimecontract.Condition, metrics map[string]json.RawMessage, agentVersion string, observedAt, historyAt time.Time, interval time.Duration) {
	if !ent.lastSeenSet || observedAt.After(ent.lastSeen) {
		ent.lastSeen = observedAt
		ent.lastSeenSet = true
	}
	// History is observation-time data, not latest-state data. Append every accepted, non-duplicate
	// sample exactly once even when a concurrent newer /report owns the live overlay. Keeping this
	// outside writtenAt prevents an acknowledged reliable sample from being permanently lost.
	c.appendTelemetryHistoryLocked(t, nodeID, metrics, historyAt, interval)
	if !ent.telemetrySet || !observedAt.Before(ent.writtenAt) { // observedAt >= writtenAt
		ent.conditions = stampConditions(conditions, observedAt)
		ent.telemetry = cloneLiveMetrics(metrics)
		ent.telemetrySet = true
		ent.writtenAt = observedAt
		if agentVersion != "" {
			ent.agentVersion = agentVersion
		}
	}
}

func (c *storeCore) appendTelemetryHistoryLocked(t TenantID, nodeID string, metrics map[string]json.RawMessage, historyAt time.Time, interval time.Duration) {
	c.history.appendMetrics(t, nodeID, metrics, historyAt, interval)
}

func evictOldestTelemetryCursor(cursors map[string]telemetrySequenceCursor, protectedBootID string) (string, time.Time) {
	var oldestID string
	var oldestAt time.Time
	for bootID, cursor := range cursors {
		if bootID == protectedBootID && len(cursors) > 1 {
			continue
		}
		if oldestID == "" || cursor.lastReceivedAt.Before(oldestAt) ||
			(cursor.lastReceivedAt.Equal(oldestAt) && bootID < oldestID) {
			oldestID = bootID
			oldestAt = cursor.lastReceivedAt
		}
	}
	if oldestID != "" {
		delete(cursors, oldestID)
	}
	return oldestID, oldestAt
}

func rememberRetiredTelemetryBoot(ent *volatileTelemetry, bootID string, at time.Time) {
	if ent == nil || bootID == "" {
		return
	}
	if ent.retiredBoots == nil {
		ent.retiredBoots = make(map[string]time.Time)
	}
	if _, exists := ent.retiredBoots[bootID]; !exists && len(ent.retiredBoots) >= maxTelemetryRetiredBoots {
		var oldestID string
		var oldestAt time.Time
		for candidateID, candidateAt := range ent.retiredBoots {
			if oldestID == "" || candidateAt.Before(oldestAt) ||
				(candidateAt.Equal(oldestAt) && candidateID < oldestID) {
				oldestID = candidateID
				oldestAt = candidateAt
			}
		}
		delete(ent.retiredBoots, oldestID)
	}
	ent.retiredBoots[bootID] = at
}

// TouchLastSeen records that the agent for nodeID checked in — to the in-memory overlay ONLY (no
// durable rewrite; same DoS reasoning as RecordTelemetry). Existence is confirmed with the
// metadata-only kv.exists; LastSeen advances monotonically. Returns ErrNotFound if the node is absent.
func (c *storeCore) TouchLastSeen(ctx context.Context, t TenantID, nodeID string, at time.Time) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	ok, err := c.kv.exists(t, collNodes, nodeID)
	if err != nil {
		return err
	}
	if !ok {
		return ErrNotFound
	}
	c.telemetryMu.Lock()
	defer c.telemetryMu.Unlock()
	ent := c.telemetryEntryLocked(t, nodeID)
	if !ent.lastSeenSet || at.After(ent.lastSeen) {
		ent.lastSeen = at
		ent.lastSeenSet = true
	}
	return nil
}

// QueryTelemetryHistory returns the node's retained resource-history samples within [from, to]
// (inclusive), sorted by time and bounded by the operator's per-node cap. Observability only.
func (c *storeCore) QueryTelemetryHistory(ctx context.Context, t TenantID, nodeID string, from, to time.Time) ([]ResourceSample, error) {
	return c.history.queryContext(ctx, t, nodeID, from, to)
}

// QueryTelemetryProbeHistory returns completed typed ICMP/TCP/URL attempts within [from, to], sorted and
// exact-deduplicated across reliable retries, overlapping probe_samples windows, the rc.9 latest-result
// fallback, and disk/inflight visibility overlap. Retention and tenant custody are shared with resource
// history; no live metric or destination crosses tenant boundaries.
func (c *storeCore) QueryTelemetryProbeHistory(ctx context.Context, t TenantID, nodeID string, from, to time.Time) ([]ProbeHistorySample, error) {
	return c.history.queryProbesContext(ctx, t, nodeID, from, to)
}

// QueryTelemetryHistorySnapshot returns the legacy resource and all-active-probe projections from one
// coherent merge of durable, in-flight, and buffered history. Device history remains exact-only and
// therefore requires the filtered method below.
func (c *storeCore) QueryTelemetryHistorySnapshot(ctx context.Context, t TenantID, nodeID string, from, to time.Time) (TelemetryHistorySnapshot, error) {
	return c.history.querySnapshotContext(ctx, t, nodeID, from, to)
}

// QueryTelemetryHistorySnapshotFiltered is the selector-pushdown form used by the API for resources,
// optional legacy all-probes or one exact probe, and at most one exact device series. No broad device
// mode exists, so a browser cannot materialize every retained disk/GPU series in one query.
func (c *storeCore) QueryTelemetryHistorySnapshotFiltered(ctx context.Context, t TenantID, nodeID string, from, to time.Time, options TelemetryHistoryQueryOptions) (TelemetryHistorySnapshot, error) {
	return c.history.querySnapshotFilteredContext(ctx, t, nodeID, from, to, options)
}
