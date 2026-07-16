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
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/kunorikiku/yet-another-overlay-generator/internal/runtimecontract"
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
	c.recordTelemetryLocked(t, nodeID, ent, conditions, metrics, agentVersion, observedAt, observedAt, 0)
	return nil
}

// RecordTelemetrySequenced acknowledges and records one reliable telemetry sample. The cursor key is
// (node identity from bearer auth, agent boot id); sequence values are monotonic within that boot. An
// already-acknowledged sequence proves the node reached us now (so LastSeen may advance) but must not
// overwrite newer live metrics or append duplicate history. receivedAt, not the node clock, remains
// authoritative for LastSeen and condition ObservedAt. sampledAt is used only for the observation's
// history timestamp so a bounded replay reconstructs the sampling timeline instead of an arrival burst.
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
	live := ent.activeBootID == "" || ent.activeBootID == bootID
	if ent.activeBootID != "" && ent.activeBootID != bootID {
		switch {
		case seen:
			// A cursor can become "retired" only because a delayed, previously evicted
			// boot landed inside the restart-skew window. Let the known current boot
			// reclaim the live overlay once it supplies a newer trusted sample; otherwise
			// one delayed request could strand the node on stale live state indefinitely.
			live = sampledAtTrusted && (ent.activeSampledAt.IsZero() || sampledAt.After(ent.activeSampledAt))
		case sampledAtTrusted && !ent.activeSampledAt.IsZero() && sampledAt.Before(ent.activeSampledAt.Add(-telemetryBootStaleSlack)):
			live = false
		default:
			live = true
		}
	}
	if seen && !cursor.lastSampledAt.IsZero() && sampledAt.Before(cursor.lastSampledAt) {
		historyAt = receivedAt
	}
	if live && !ent.activeSampledAt.IsZero() && historyAt.Before(ent.activeSampledAt) {
		historyAt = receivedAt
	}

	if !seen && len(ent.sequenceCursors) >= maxTelemetrySequenceCursors {
		evictOldestTelemetryCursor(ent.sequenceCursors, ent.activeBootID)
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
		ent.activeBootID = bootID
		if ent.activeSampledAt.IsZero() || historyAt.After(ent.activeSampledAt) {
			ent.activeSampledAt = historyAt
		}
		c.recordTelemetryLocked(t, nodeID, ent, conditions, metrics, agentVersion, receivedAt, historyAt, interval)
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
	if !ent.telemetrySet || !observedAt.Before(ent.writtenAt) { // observedAt >= writtenAt
		ent.conditions = stampConditions(conditions, observedAt)
		ent.telemetry = cloneMetrics(metrics)
		ent.telemetrySet = true
		ent.writtenAt = observedAt
		if agentVersion != "" {
			ent.agentVersion = agentVersion
		}
		// Retain a resource-history sample (in-memory append; a background flusher persists it off the
		// heartbeat path). Gated on the same monotonic freshness. history has its OWN mutex; lock order
		// telemetryMu → history.mu is consistent (nothing takes telemetryMu while holding history.mu).
		c.appendTelemetryHistoryLocked(t, nodeID, metrics, historyAt, interval)
	}
}

func (c *storeCore) appendTelemetryHistoryLocked(t TenantID, nodeID string, metrics map[string]json.RawMessage, historyAt time.Time, interval time.Duration) {
	if s, ok := resourceSampleFromMetrics(metrics, historyAt, interval); ok {
		c.history.append(t, nodeID, s)
	}
}

func evictOldestTelemetryCursor(cursors map[string]telemetrySequenceCursor, protectedBootID string) {
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
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return c.history.query(t, nodeID, from, to)
}
