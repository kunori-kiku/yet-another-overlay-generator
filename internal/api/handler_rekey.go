package api

// handler_rekey.go holds the operator fleet key-rotation handlers: request a fleet-wide
// rekey (rekey-all) and clear a single node stuck rekey flag (clear-rekey). Both are routed
// through the op() adapter (routes_controller.go), which applies the method guard + structural
// identity() check before the body runs.

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/kunorikiku/yet-another-overlay-generator/internal/apierr"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/controller"
)

// HandleClearRekey clears a node's pending RekeyRequested flag WITHOUT evicting it — the operator's
// escape hatch for a "Roll keys" straggler (a dead/offline node, or a mis-clicked rekey-all) that
// would otherwise keep the panel's rekeying gate stuck and force a revoke. Unlike HandleRevoke it
// does NOT change Status, does NOT clear the API token, and does NOT BumpGeneration (it changes no
// bundle, so there is nothing to wake). Idempotent: a node with no pending rekey returns 200 with
// cleared:false and writes no audit entry. It is best-effort against a racing in-flight /rekey — an
// agent that already saw rekey_requested may still complete its rotation, which is benign (the agent
// holds the new key, so the swap stays consistent); the operator can clear again.
func (h *ControllerHandler) HandleClearRekey(ctx context.Context, tenant controller.TenantID, actor string, w http.ResponseWriter, r *http.Request) (any, *apierr.Error) {
	var req revokeRequestJSON // {node_id} — same shape as revoke
	if err := decodeJSON(w, r, &req); err != nil {
		return nil, codedErr(apierr.CodeReqInvalidBody, err)
	}
	if req.NodeID == "" {
		return nil, apierr.New(apierr.CodeReqFieldRequired).With("field", "node_id")
	}
	// Clear the flag under the tenant op lock (controller.ClearNodeRekey): a durable read-modify-write
	// serialized against a concurrent enrollment.Rekey so this flag flip can never clobber a freshly
	// rotated WireGuard key, and the volatile telemetry overlay is never baked into the record.
	cleared, err := controller.ClearNodeRekey(ctx, h.store, tenant, req.NodeID)
	if err != nil {
		if errors.Is(err, controller.ErrNotFound) {
			return nil, apierr.New(apierr.CodeNodeNotFound).Wrap(err)
		}
		return nil, codedErr(apierr.CodeInternalStorage, err)
	}
	// Idempotent no-op: nothing was pending, so no mutation and no (misleading) audit entry.
	if !cleared {
		return clearRekeyResponseJSON{NodeID: req.NodeID, Cleared: false}, nil
	}
	if _, err := h.store.AppendAudit(ctx, tenant, controller.AuditEntry{
		Timestamp: time.Now(),
		Actor:     "operator:" + actor,
		Action:    "rekey-clear",
		NodeID:    req.NodeID,
	}); err != nil {
		return nil, codedErr(apierr.CodeInternalStorage, err)
	}
	return clearRekeyResponseJSON{NodeID: req.NodeID, Cleared: true}, nil
}

// HandleRekeyAll requests a fleet-wide WireGuard key rotation (operator-only). It
// flags every APPROVED node with RekeyRequested=true via controller.FlagFleetRekey — a
// durable read-modify-write serialized under the tenant op lock so every other field is
// preserved and a concurrent key rotation is never lost; pending/revoked nodes are left
// untouched. After flagging, it calls Store.BumpGeneration to WAKE every parked
// daemon agent: those agents long-poll WaitForGeneration, which fires ONLY on a
// generation advance, so without the bump a flagged agent would never wake to see
// rekey_requested (the deadlock this fixes). The bump changes NO bundle — /config
// (via GetServedConfig) still serves the last promoted bundle — so a woken agent sees the
// rekey signal on /config and skip-applies (rotate+re-register) rather than treating
// the bumped generation as a deploy. Each flagged node's agent then learns of the
// request on its next /config fetch (rekey_requested=true), regenerates its key, and
// re-registers the new PUBLIC key via /rekey (which clears the flag). This is the
// ROUTINE security tier: rolling EXISTING members' keys never adds or removes a
// member, so the operator token authorizes it in v1. Returns {requested:<count>}.
func (h *ControllerHandler) HandleRekeyAll(ctx context.Context, tenant controller.TenantID, actor string, _ http.ResponseWriter, _ *http.Request) (any, *apierr.Error) {
	// Flag every approved node under the tenant op lock (controller.FlagFleetRekey): the whole
	// read-modify-write loop is serialized against a concurrent enrollment.Rekey, so this flag flip
	// cannot lose a freshly rotated WireGuard key, and each node is read durably (no telemetry overlay
	// baked into the persisted record).
	requested, err := controller.FlagFleetRekey(ctx, h.store, tenant)
	if err != nil {
		return nil, codedErr(apierr.CodeInternalStorage, err)
	}
	// WAKE the fleet: bump the generation so parked daemon agents (blocked in
	// WaitForGeneration, which only wakes on an advance) wake, Fetch /config, and see
	// rekey_requested. This bumps the counter ONLY — it changes no bundle, so a woken
	// agent skip-applies on the rekey signal instead of treating it as a deploy. Done
	// even when requested==0 so the bump is unconditional and idempotent (a no-op-flag
	// rekey-all still records the audit entry below).
	if _, err := h.store.BumpGeneration(ctx, tenant); err != nil {
		return nil, codedErr(apierr.CodeInternalStorage, err)
	}
	if _, err := h.store.AppendAudit(ctx, tenant, controller.AuditEntry{
		Timestamp: time.Now(),
		Actor:     "operator:" + actor,
		Action:    "rekey-request",
	}); err != nil {
		return nil, codedErr(apierr.CodeInternalStorage, err)
	}
	return rekeyAllResponseJSON{Requested: requested}, nil
}
