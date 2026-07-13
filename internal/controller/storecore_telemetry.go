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
}

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

// cloneMetrics returns a copy of a metrics map (the json.RawMessage values are immutable, so a per-key
// copy is sufficient isolation); nil-safe.
func cloneMetrics(in map[string]json.RawMessage) map[string]json.RawMessage {
	if in == nil {
		return nil
	}
	out := make(map[string]json.RawMessage, len(in))
	for k, v := range in {
		out[k] = v
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
	c.telemetryMu.Lock()
	defer c.telemetryMu.Unlock()
	ent := c.telemetryEntryLocked(t, nodeID)
	if !ent.lastSeenSet || observedAt.After(ent.lastSeen) {
		ent.lastSeen = observedAt
		ent.lastSeenSet = true
	}
	if !ent.telemetrySet || !observedAt.Before(ent.writtenAt) { // observedAt >= writtenAt
		ent.conditions = stampConditions(conditions, observedAt)
		ent.telemetry = metrics
		ent.telemetrySet = true
		ent.writtenAt = observedAt
		if agentVersion != "" {
			ent.agentVersion = agentVersion
		}
		// Retain a resource-history sample (in-memory append; a background flusher persists it off the
		// heartbeat path). Gated on the same monotonic freshness. history has its OWN mutex; lock order
		// telemetryMu → history.mu is consistent (nothing takes telemetryMu while holding history.mu).
		if s, ok := resourceSampleFromMetrics(metrics, observedAt); ok {
			c.history.append(t, nodeID, s)
		}
	}
	return nil
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
