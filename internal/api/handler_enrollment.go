package api

// handler_enrollment.go holds the operator fleet-registry handlers: mint an enrollment
// token, list nodes, and revoke a node (plus the mapConditions/topologyHasNode helpers used only here).
// All three are routed through the op() adapter (routes_controller.go), which applies the method
// guard + structural identity() check before the body runs.

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/kunorikiku/yet-another-overlay-generator/internal/apierr"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/controller"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/model"
)

// mapConditions projects the stored controller.NodeCondition slice onto the operator wire view
// (plan-2). nil/empty in => nil out (omitempty drops the field). It reads the embedded runtimecontract.Condition
// fields plus the wrapper's server-stamped ObservedAt, copying verbatim — the curation/length-cap
// already happened at ingest (handler_agent), so this is pure projection, no re-classification.
func mapConditions(cs []controller.NodeCondition) []conditionJSON {
	if len(cs) == 0 {
		return nil
	}
	out := make([]conditionJSON, 0, len(cs))
	for _, c := range cs {
		out = append(out, conditionJSON{
			Type:       c.Type,
			Status:     c.Status,
			Reason:     c.Reason,
			Message:    c.Message,
			Since:      c.Since,
			ObservedAt: c.ObservedAt,
		})
	}
	return out
}

// HandleNodes lists the fleet registry for the operator panel (operator-only). It
// returns a []nodeJSON view that carries fleet state but NO key material.
func (h *ControllerHandler) HandleNodes(ctx context.Context, tenant controller.TenantID, _ string, _ http.ResponseWriter, _ *http.Request) (any, *apierr.Error) {
	nodes, err := h.store.ListNodes(ctx, tenant)
	if err != nil {
		return nil, codedErr(apierr.CodeInternalStorage, err)
	}
	// Compute agent self-update rollout membership ONCE for the whole list (one settings load +
	// one membership pass): the per-node in_rollout flag the panel's update-status chip reads.
	// An absent settings record (most fleets never configure a rollout) is a benign no-op — the
	// zero ControllerSettings yields an empty rollout set (every node not-targeted).
	cs, err := h.store.GetSettings(ctx, tenant)
	if err != nil && !errors.Is(err, controller.ErrNotFound) {
		return nil, codedErr(apierr.CodeInternalStorage, err)
	}
	rollout := controller.AgentRolloutNodeIDs(cs, nodes)
	out := make([]nodeJSON, 0, len(nodes))
	for _, n := range nodes {
		out = append(out, nodeJSON{
			NodeID:            n.NodeID,
			Status:            string(n.Status),
			HasWGPublicKey:    n.WGPublicKey != "",
			DesiredGeneration: n.DesiredGeneration,
			AppliedGeneration: n.AppliedGeneration,
			LastChecksum:      n.LastChecksum,
			LastHealth:        n.LastHealth,
			AgentVersion:      n.LastAgentVersion,
			LastSeen:          n.LastSeen,
			EnrolledAt:        n.EnrolledAt,
			RekeyRequested:    n.RekeyRequested,
			InRollout:         rollout[n.NodeID],
			Conditions:        mapConditions(n.Conditions),
			Telemetry:         n.Telemetry,
		})
	}
	return out, nil
}

// HandleRevoke evicts a node from the fleet (operator-only). It flips the node's
// Status to NodeRevoked (preserving every other field) AND clears its API token via
// RevokeNodeAPIToken, so the node's bearer credential stops resolving immediately
// (LookupNodeByAPIToken no longer maps it to an approved node). It is the operator
// counterpart to enrollment: 404 when the node is unknown, otherwise it records a
// "revoke" audit entry and returns {node_id, revoked:true}.
func (h *ControllerHandler) HandleRevoke(ctx context.Context, tenant controller.TenantID, actor string, w http.ResponseWriter, r *http.Request) (any, *apierr.Error) {
	var req revokeRequestJSON
	if err := decodeJSON(w, r, &req); err != nil {
		return nil, codedErr(apierr.CodeReqInvalidBody, err)
	}
	if req.NodeID == "" {
		return nil, apierr.New(apierr.CodeReqFieldRequired).With("field", "node_id")
	}

	// Load the existing record so we can preserve every field while flipping Status;
	// an unknown node is a 404 (there is nothing to revoke).
	node, err := h.store.GetNode(ctx, tenant, req.NodeID)
	if err != nil {
		if errors.Is(err, controller.ErrNotFound) {
			return nil, apierr.New(apierr.CodeNodeNotFound).Wrap(err)
		}
		return nil, codedErr(apierr.CodeInternalStorage, err)
	}

	// Flip to revoked, preserving all other fields. Also clear any pending rekey flag:
	// a revoked node will never re-register, so a left-over RekeyRequested would keep the
	// panel's "rotating" gate stuck forever (a revoked node is excluded from the deploy
	// subgraph anyway). UpsertNode matches by NodeID.
	node.Status = controller.NodeRevoked
	node.RekeyRequested = false
	if err := h.store.UpsertNode(ctx, tenant, node); err != nil {
		return nil, codedErr(apierr.CodeInternalStorage, err)
	}
	// Clear the API token + reverse index so the bearer credential stops resolving
	// immediately (idempotent: a no-op success if the node had no token).
	if err := h.store.RevokeNodeAPIToken(ctx, tenant, req.NodeID); err != nil {
		return nil, codedErr(apierr.CodeInternalStorage, err)
	}
	// Invalidate any outstanding enrollment tokens for this node so a still-valid token
	// cannot resurrect the revoked node (S5; defense in depth with the Enroll
	// NodeRevoked guard). Idempotent: a node with no outstanding tokens purges zero.
	if _, err := h.store.PurgeEnrollmentTokensForNode(ctx, tenant, req.NodeID); err != nil {
		return nil, codedErr(apierr.CodeInternalStorage, err)
	}
	if _, err := h.store.AppendAudit(ctx, tenant, controller.AuditEntry{
		Timestamp: time.Now(),
		Actor:     "operator:" + actor,
		Action:    "revoke",
		NodeID:    req.NodeID,
	}); err != nil {
		return nil, codedErr(apierr.CodeInternalStorage, err)
	}
	return revokeResponseJSON{NodeID: req.NodeID, Revoked: true}, nil
}

// HandleEnrollmentToken mints a single-use, node-scoped enrollment token
// (operator-only) and returns its plaintext ONCE. The controller stores only the
// token hash (CreateEnrollmentToken), so the plaintext cannot be recovered later.
func (h *ControllerHandler) HandleEnrollmentToken(ctx context.Context, tenant controller.TenantID, _ string, w http.ResponseWriter, r *http.Request) (any, *apierr.Error) {
	var req enrollmentTokenRequestJSON
	if err := decodeJSON(w, r, &req); err != nil {
		return nil, codedErr(apierr.CodeReqInvalidBody, err)
	}
	if req.NodeID == "" {
		return nil, apierr.New(apierr.CodeReqFieldRequired).With("field", "node_id")
	}
	// A node must never be granted an enrollment token AS the operator (the operator
	// identity is reserved; enrolling under it is rejected at /enroll, but reject the
	// token mint too for a clear, early error).
	if h.isReservedNodeID(req.NodeID) {
		return nil, apierr.New(apierr.CodeNodeIDReserved)
	}
	// Bound the TTL server-side: an enrollment token is a one-shot node bring-up
	// credential, not a standing capability. Without an upper cap an operator could mint
	// a year-long token that, combined with re-enroll, is a long-lived node-takeover /
	// resurrection vector (S6).
	const maxEnrollmentTokenTTLSeconds = 7 * 24 * 60 * 60 // 7 days
	if req.TTLSeconds <= 0 || req.TTLSeconds > maxEnrollmentTokenTTLSeconds {
		return nil, apierr.New(apierr.CodeReqFieldInvalid).With("field", "ttl_seconds")
	}

	now := time.Now()
	plaintext, tok := controller.NewEnrollmentToken(req.NodeID, time.Duration(req.TTLSeconds)*time.Second, now)
	if err := h.store.CreateEnrollmentToken(ctx, tenant, tok); err != nil {
		return nil, codedErr(apierr.CodeInternalStorage, err)
	}
	if _, err := h.store.AppendAudit(ctx, tenant, controller.AuditEntry{
		Timestamp: now,
		Actor:     "operator:" + h.operatorName,
		Action:    "enrollment-token",
		NodeID:    req.NodeID,
	}); err != nil {
		return nil, codedErr(apierr.CodeInternalStorage, err)
	}
	// Design-membership advisory (plan-6, warn-not-block): if the stored design has
	// no node with this id, the token mints fine but the node will be skipped at
	// stage until it is added — surface that so an operator who fat-fingers an id
	// learns about it now. The warning is set ONLY when GetTopology succeeds AND the
	// id is absent. Any read failure yields NO warning by design: ErrNotFound means
	// no design is stored yet (pre-minting before designing is normal), and a
	// transient store error must not produce a false alarm — the advisory fails safe
	// to silent, never blocks the mint.
	resp := enrollmentTokenResponseJSON{Token: plaintext}
	if rec, err := h.store.GetTopology(ctx, tenant); err == nil && !topologyHasNode(rec.JSON, req.NodeID) {
		resp.Warning = "node-id not present in the stored design; it will be skipped at stage until added"
	}
	return resp, nil
}

// topologyHasNode reports whether the stored topology JSON contains a node with the
// given id. A parse failure is treated as "present" (no false alarm on a topology we
// cannot read — the membership check is an advisory, not a gate).
func topologyHasNode(topoJSON []byte, nodeID string) bool {
	var topo model.Topology
	if err := json.Unmarshal(topoJSON, &topo); err != nil {
		return true
	}
	for _, n := range topo.Nodes {
		if n.ID == nodeID {
			return true
		}
	}
	return false
}
