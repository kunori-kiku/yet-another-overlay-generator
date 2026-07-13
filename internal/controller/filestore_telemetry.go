package controller

// filestore_telemetry.go — FileStore's volatile telemetry overlay: the second-lock
// (telemetryMu) in-memory observability merge that keeps high-frequency heartbeats off the
// durable custody write path (deliberate DoS-hardening). Split from filestore.go (plan-2);
// no logic change.

import (
	"context"
	"encoding/json"
	"os"
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

// telemetryEntryLocked returns the volatile overlay entry for (t, nodeID), lazily creating the maps
// and the entry. The caller MUST hold telemetryMu.
func (fs *FileStore) telemetryEntryLocked(t TenantID, nodeID string) *volatileTelemetry {
	if fs.telemetry == nil {
		fs.telemetry = make(map[TenantID]map[string]*volatileTelemetry)
	}
	m := fs.telemetry[t]
	if m == nil {
		m = make(map[string]*volatileTelemetry)
		fs.telemetry[t] = m
	}
	ent := m[nodeID]
	if ent == nil {
		ent = &volatileTelemetry{}
		m[nodeID] = ent
	}
	return ent
}

// applyTelemetryOverlay merges the volatile telemetry overlay OVER a durable node record, deep-copying
// the slice/map so a returned Node can never alias (and later mutate) the shared overlay. Custody
// fields are never touched. Takes telemetryMu; the caller holds mu (lock order mu -> telemetryMu).
func (fs *FileStore) applyTelemetryOverlay(t TenantID, n *Node) {
	fs.telemetryMu.Lock()
	defer fs.telemetryMu.Unlock()
	m := fs.telemetry[t]
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
func (fs *FileStore) refreshTelemetryOverlayFromReport(t TenantID, nodeID string, conditions []NodeCondition, agentVersion string, observedAt time.Time) {
	fs.telemetryMu.Lock()
	defer fs.telemetryMu.Unlock()
	ent := fs.telemetryEntryLocked(t, nodeID)
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

// cloneMetrics returns a copy of a metrics map (values are immutable json.RawMessage the callers never
// mutate in place, so a per-key copy is sufficient isolation); nil-safe.
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
// touches AppliedGeneration/LastChecksum/LastHealth/DesiredGeneration) AND, unlike the pre-overlay
// version, performs NO durable rewrite: a 30s heartbeat must not fsync the whole node record (the DoS
// vector). Node existence is confirmed with a metadata-only os.Stat (a corrupt-but-present record no
// longer fails a heartbeat, which is strictly better — telemetry never writes the file). A monotonic
// writtenAt guard drops a stale write so a concurrent /report's fresher conditions win.
func (fs *FileStore) RecordTelemetry(ctx context.Context, t TenantID, nodeID string, conditions []runtimecontract.Condition, metrics map[string]json.RawMessage, agentVersion string, observedAt time.Time) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	dir, err := fs.tenantDir(t)
	if err != nil {
		return err
	}
	p, err := fs.nodePath(dir, nodeID)
	if err != nil {
		return err
	}
	if _, statErr := os.Stat(p); statErr != nil {
		if os.IsNotExist(statErr) {
			return ErrNotFound
		}
		return statErr
	}
	fs.telemetryMu.Lock()
	defer fs.telemetryMu.Unlock()
	ent := fs.telemetryEntryLocked(t, nodeID)
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
		// plan-2: retain a resource-history sample (in-memory append; a background flusher persists it
		// off the heartbeat path). Gated on the same monotonic freshness as the overlay, so history holds
		// only in-order samples. history has its OWN mutex; lock order telemetryMu -> history.mu is
		// consistent (nothing takes telemetryMu while holding history.mu), so this cannot deadlock.
		if s, ok := resourceSampleFromMetrics(metrics, observedAt); ok {
			fs.history.append(t, nodeID, s)
		}
	}
	return nil
}

// TouchLastSeen records that the agent for nodeID checked in at the given time — to the in-memory
// overlay ONLY (no durable rewrite; same DoS reasoning as RecordTelemetry). Existence is confirmed with
// a metadata-only os.Stat; LastSeen advances monotonically.
func (fs *FileStore) TouchLastSeen(ctx context.Context, t TenantID, nodeID string, at time.Time) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	dir, err := fs.tenantDir(t)
	if err != nil {
		return err
	}
	p, err := fs.nodePath(dir, nodeID)
	if err != nil {
		return err
	}
	if _, statErr := os.Stat(p); statErr != nil {
		if os.IsNotExist(statErr) {
			return ErrNotFound
		}
		return statErr
	}
	fs.telemetryMu.Lock()
	defer fs.telemetryMu.Unlock()
	ent := fs.telemetryEntryLocked(t, nodeID)
	if !ent.lastSeenSet || at.After(ent.lastSeen) {
		ent.lastSeen = at
		ent.lastSeenSet = true
	}
	return nil
}
