package api

// handler_rekey.go holds the operator fleet key-rotation handlers: request a fleet-wide
// rekey (rekey-all) and clear a single node stuck rekey flag (clear-rekey).

import (
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
func (h *ControllerHandler) HandleClearRekey(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeAPIError(w, apierr.New(apierr.CodeMethodNotAllowed).With("method", "POST"))
		return
	}
	tenant, operator, ok := identity(r.Context())
	if !ok {
		writeAPIError(w, apierr.New(apierr.CodeInternalIdentityMissing))
		return
	}
	var req revokeRequestJSON // {node_id} — same shape as revoke
	if err := decodeJSON(w, r, &req); err != nil {
		writeCodedOr(w, apierr.CodeReqInvalidBody, err)
		return
	}
	if req.NodeID == "" {
		writeAPIError(w, apierr.New(apierr.CodeReqFieldRequired).With("field", "node_id"))
		return
	}
	node, err := h.store.GetNode(r.Context(), tenant, req.NodeID)
	if err != nil {
		if errors.Is(err, controller.ErrNotFound) {
			writeAPIError(w, apierr.New(apierr.CodeNodeNotFound).Wrap(err))
			return
		}
		// Reserved for a persistent Store; MemStore only ever returns ErrNotFound from GetNode.
		writeCodedOr(w, apierr.CodeInternalStorage, err)
		return
	}
	// Idempotent no-op: nothing pending, so no mutation and no (misleading) audit entry.
	if !node.RekeyRequested {
		writeJSON(w, http.StatusOK, clearRekeyResponseJSON{NodeID: req.NodeID, Cleared: false})
		return
	}
	// Clear ONLY the flag, preserving every other field (mirrors the revoke path's preserve-and-set).
	node.RekeyRequested = false
	if err := h.store.UpsertNode(r.Context(), tenant, node); err != nil {
		writeCodedOr(w, apierr.CodeInternalStorage, err)
		return
	}
	if _, err := h.store.AppendAudit(r.Context(), tenant, controller.AuditEntry{
		Timestamp: time.Now(),
		Actor:     "operator:" + operator,
		Action:    "rekey-clear",
		NodeID:    req.NodeID,
	}); err != nil {
		writeCodedOr(w, apierr.CodeInternalStorage, err)
		return
	}
	writeJSON(w, http.StatusOK, clearRekeyResponseJSON{NodeID: req.NodeID, Cleared: true})
}

// HandleRekeyAll requests a fleet-wide WireGuard key rotation (operator-only). It
// flags every APPROVED node with RekeyRequested=true (read-modify-write via
// GetNode/UpsertNode so every other field is preserved); pending/revoked nodes are
// left untouched. After flagging, it calls Store.BumpGeneration to WAKE every parked
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
func (h *ControllerHandler) HandleRekeyAll(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeAPIError(w, apierr.New(apierr.CodeMethodNotAllowed).With("method", "POST"))
		return
	}
	tenant, operator, ok := identity(r.Context())
	if !ok {
		writeAPIError(w, apierr.New(apierr.CodeInternalIdentityMissing))
		return
	}
	nodes, err := h.store.ListNodes(r.Context(), tenant)
	if err != nil {
		writeCodedOr(w, apierr.CodeInternalStorage, err)
		return
	}
	requested := 0
	for _, n := range nodes {
		if n.Status != controller.NodeApproved {
			continue
		}
		// Re-read under the same shape as /revoke so a concurrent mutation does not
		// clobber a field; flip the flag while preserving everything else.
		node, err := h.store.GetNode(r.Context(), tenant, n.NodeID)
		if err != nil {
			if errors.Is(err, controller.ErrNotFound) {
				// The node vanished between the list and the read; skip it.
				continue
			}
			writeCodedOr(w, apierr.CodeInternalStorage, err)
			return
		}
		node.RekeyRequested = true
		if err := h.store.UpsertNode(r.Context(), tenant, node); err != nil {
			writeCodedOr(w, apierr.CodeInternalStorage, err)
			return
		}
		requested++
	}
	// WAKE the fleet: bump the generation so parked daemon agents (blocked in
	// WaitForGeneration, which only wakes on an advance) wake, Fetch /config, and see
	// rekey_requested. This bumps the counter ONLY — it changes no bundle, so a woken
	// agent skip-applies on the rekey signal instead of treating it as a deploy. Done
	// even when requested==0 so the bump is unconditional and idempotent (a no-op-flag
	// rekey-all still records the audit entry below).
	if _, err := h.store.BumpGeneration(r.Context(), tenant); err != nil {
		writeCodedOr(w, apierr.CodeInternalStorage, err)
		return
	}
	if _, err := h.store.AppendAudit(r.Context(), tenant, controller.AuditEntry{
		Timestamp: time.Now(),
		Actor:     "operator:" + operator,
		Action:    "rekey-request",
	}); err != nil {
		writeCodedOr(w, apierr.CodeInternalStorage, err)
		return
	}
	writeJSON(w, http.StatusOK, rekeyAllResponseJSON{Requested: requested})
}
