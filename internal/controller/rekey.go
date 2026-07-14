package controller

// rekey.go — the operator fleet-rekey control-plane ops (FlagFleetRekey / ClearNodeRekey). Both are
// registry read-modify-writes on the RekeyRequested flag, and both run under the per-tenant op lock so
// a concurrent enrollment.Rekey (which also holds it, and writes the freshly rotated WireGuard public
// key) cannot be clobbered by a stale read-then-write — the lost-update the api handlers had while
// doing GetNode→UpsertNode with NO lock. They read DURABLY (GetNodeRecord) so the volatile telemetry
// overlay is never baked into the persisted record either. The WAKE (BumpGeneration) and the audit
// entry stay in the api handler: they are not custody-critical and the audit needs the operator id.

import (
	"context"
	"errors"
	"fmt"
)

// FlagFleetRekey sets RekeyRequested=true on every APPROVED node, under the tenant op lock, and returns
// the count flagged. Holding the lock across the whole loop is what closes the lost-update: without it,
// a per-node GetNode→UpsertNode could read a node, and a concurrent enrollment.Rekey could commit a new
// WGPublicKey, before this writeback lands and reverts the key to the stale value it read. A node that
// vanished between the list and the durable read is skipped (a concurrent revoke+delete).
func FlagFleetRekey(ctx context.Context, store Store, t TenantID) (int, error) {
	defer lockTenantOps(t)()
	nodes, err := store.ListNodes(ctx, t)
	if err != nil {
		return 0, fmt.Errorf("controller: listing nodes to rekey: %w", err)
	}
	requested := 0
	for _, n := range nodes {
		if n.Status != NodeApproved {
			continue
		}
		// Re-read DURABLY inside the lock: GetNodeRecord keeps the telemetry overlay out of the
		// writeback, and the enclosing lock keeps a concurrent key rotation from being lost.
		node, err := store.GetNodeRecord(ctx, t, n.NodeID)
		if err != nil {
			if errors.Is(err, ErrNotFound) {
				continue // vanished between the list and the read
			}
			return 0, fmt.Errorf("controller: reading node to rekey: %w", err)
		}
		node.RekeyRequested = true
		if err := store.UpsertNode(ctx, t, node); err != nil {
			return 0, fmt.Errorf("controller: flagging node rekey: %w", err)
		}
		requested++
	}
	return requested, nil
}

// ClearNodeRekey clears one node's RekeyRequested flag WITHOUT evicting it, under the tenant op lock
// (same lost-update reasoning as FlagFleetRekey). It returns cleared=false with NO write when the flag
// was already clear (idempotent — the caller then writes no audit entry), or ErrNotFound when the node
// is absent (mapped by the caller). The durable read keeps the volatile telemetry overlay out of the
// persisted record.
func ClearNodeRekey(ctx context.Context, store Store, t TenantID, nodeID string) (bool, error) {
	defer lockTenantOps(t)()
	node, err := store.GetNodeRecord(ctx, t, nodeID)
	if err != nil {
		return false, err // ErrNotFound mapped by the caller
	}
	if !node.RekeyRequested {
		return false, nil // idempotent no-op: nothing pending, no write, no (misleading) audit
	}
	node.RekeyRequested = false
	if err := store.UpsertNode(ctx, t, node); err != nil {
		return false, fmt.Errorf("controller: clearing node rekey: %w", err)
	}
	return true, nil
}
