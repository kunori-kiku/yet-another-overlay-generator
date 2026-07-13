package api

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/kunorikiku/yet-another-overlay-generator/internal/apierr"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/controller"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/model"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/normalize"
)

// HandleUpdateTopology stores a new topology version (operator-only). The body is
// the public-keys-only topology JSON (the stage step compiles/validates it). The
// key-custody principle is ENFORCED at this API write boundary, not just asserted:
// a payload carrying any non-empty wireguard_private_key is refused with 400 (D4,
// fail-closed — the panel strips client-side; a key reaching this handler means a
// custody bug upstream and must blow up loudly, never be stored). The tenant is the
// configured one.
func (h *ControllerHandler) HandleUpdateTopology(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeAPIError(w, apierr.New(apierr.CodeMethodNotAllowed).With("method", "POST"))
		return
	}
	tenant, operator, ok := identity(r.Context())
	if !ok {
		writeAPIError(w, apierr.New(apierr.CodeInternalIdentityMissing))
		return
	}
	body, err := readControllerBody(w, r)
	if err != nil {
		writeCodedOr(w, apierr.CodeReqInvalidBody, err)
		return
	}
	// Custody gate: unmarshal into the model (not a substring match, which would
	// false-positive on names/notes) and refuse any private key material. Bodies are
	// always panel-produced model.Topology, so an unmarshal failure is equally a 400 —
	// storing bytes we cannot custody-check would reopen the hole this gate closes.
	// A *json.SyntaxError keeps the plain "not valid JSON" message (the old json.Valid
	// pre-check, now folded into this single parse).
	var topo model.Topology
	if err := json.Unmarshal(body, &topo); err != nil {
		var syn *json.SyntaxError
		if errors.As(err, &syn) {
			writeAPIError(w, apierr.New(apierr.CodeReqInvalidBody).Wrap(err))
			return
		}
		writeAPIError(w, apierr.New(apierr.CodeReqInvalidBody).Wrap(err))
		return
	}
	for _, n := range topo.Nodes {
		if n.WireGuardPrivateKey != "" {
			writeAPIError(w, apierr.New(apierr.CodeCustodyPrivateKey))
			return
		}
	}
	// Heal colliding allocation pins on the write path: an incremental-enrollment compile could once
	// persist a transit IP / port / link-local onto two different links (the "pin occupied by two
	// different links" validate error). Stripping the colliding edge's pins here means every saved or
	// imported design is stored collision-free and re-allocates cleanly on the next stage; the
	// allocator's out-of-subgraph reservation prevents NEW instances, so this is a one-way convergence.
	normalize.HealCollidingPins(&topo)
	// Store the CANONICAL re-marshaled form, not the raw bytes: the gate above checks
	// the parsed view, and raw bytes could smuggle key material past it via duplicate
	// JSON keys (last-key-wins parsing) or fields outside the model. Canonicalizing
	// makes stored-bytes == checked-view by construction. The wire contract for this
	// endpoint is exactly model.Topology, so unknown fields are not data, they are a
	// bug — and they are dropped here rather than persisted unchecked.
	canonical, err := json.Marshal(topo)
	if err != nil {
		writeCodedOr(w, apierr.CodeInternalStorage, err)
		return
	}

	rec, err := h.store.PutTopology(r.Context(), tenant, canonical)
	if err != nil {
		writeCodedOr(w, apierr.CodeInternalStorage, err)
		return
	}
	// Post-commit audit is best-effort: the version is already stored, and converting
	// an audit-write hiccup into a 500 would tell the operator the action failed when
	// it committed (the retry would mint a duplicate version). Same convention as the
	// settings/login audits.
	_, _ = h.store.AppendAudit(r.Context(), tenant, controller.AuditEntry{
		Timestamp: time.Now(),
		Actor:     "operator:" + operator,
		Action:    "update-topology",
	})
	writeJSON(w, http.StatusOK, map[string]int64{"version": rec.Version})
}

// HandleStage compiles the enrolled subgraph of the stored topology into per-node
// bundles staged at the next generation (operator-only). It returns the StageResult.
func (h *ControllerHandler) HandleStage(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeAPIError(w, apierr.New(apierr.CodeMethodNotAllowed).With("method", "POST"))
		return
	}
	tenant, _, ok := identity(r.Context())
	if !ok {
		writeAPIError(w, apierr.New(apierr.CodeInternalIdentityMissing))
		return
	}
	// Optional force override (plan-6): an empty body = no force; force_all re-stages every node,
	// force_nodes re-stages named nodes even when unchanged — the drift/rescue escape hatch for the
	// delta-skip. A non-empty malformed body is a 400.
	var req stageRequestJSON
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && !errors.Is(err, io.EOF) {
		writeCodedOr(w, apierr.CodeReqInvalidBody, err)
		return
	}
	var opts []controller.StageOption
	if req.ForceAll {
		opts = append(opts, controller.WithForceAll())
	}
	if len(req.ForceNodes) > 0 {
		opts = append(opts, controller.WithForceNodes(req.ForceNodes...))
	}
	result, err := controller.CompileAndStage(r.Context(), h.store, tenant, time.Now(), opts...)
	if err != nil {
		// CompileAndStage wraps source-coded errors (%w), so writeCodedOr surfaces each at its
		// OWN status — compile constraints stay 422, but a keygen error (e.g. an AgentHeld node
		// with no registered public key) surfaces its native 400 and an export I/O failure its
		// 500. This is intentionally MORE precise than the old blanket 422; CodeStageFailed (422)
		// is only the fallback for an un-coded stage error. (See TestWriteCodedOr_* in handler_test.)
		writeCodedOr(w, apierr.CodeStageFailed, err)
		return
	}
	writeJSON(w, http.StatusOK, stageResponseJSON{
		Staged:            result.Staged,
		Unchanged:         result.UnchangedNodeIDs,
		SkippedUnenrolled: result.SkippedUnenrolled,
		Generation:        result.Generation,
	})
}

// HandleDeployPreview is the plan-6 read-only dry-run: it reports which enrolled nodes a Deploy WOULD
// re-stage (changed vs served) vs skip (unchanged), plus the keystone-full-restage flag and the topology
// version it compiled — WITHOUT staging. The Deploy dialog calls it on open so the operator sees "N
// updated, M unchanged" (and any pending keystone full-restage) before deploying.
func (h *ControllerHandler) HandleDeployPreview(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeAPIError(w, apierr.New(apierr.CodeMethodNotAllowed).With("method", "GET"))
		return
	}
	tenant, _, ok := identity(r.Context())
	if !ok {
		writeAPIError(w, apierr.New(apierr.CodeInternalIdentityMissing))
		return
	}
	pv, err := controller.DeployPreview(r.Context(), h.store, tenant)
	if err != nil {
		writeCodedOr(w, apierr.CodeInternal, err)
		return
	}
	nodes := make([]deployPreviewNodeJSON, 0, len(pv.Nodes))
	for _, n := range pv.Nodes {
		nodes = append(nodes, deployPreviewNodeJSON{NodeID: n.NodeID, Name: n.Name, Changed: n.Changed})
	}
	writeJSON(w, http.StatusOK, deployPreviewResponseJSON{
		TopologyVersion:     pv.TopologyVersion,
		KeystoneFullRestage: pv.KeystoneFullRestage,
		Nodes:               nodes,
		SkippedUnenrolled:   pv.SkippedUnenrolled,
	})
}

// HandleCompilePreview compiles the enrolled subgraph of the POSTed current design and returns
// the rendered configs + the skipped (unenrolled) node IDs — WITHOUT staging, persisting pins,
// exporting bundles, or writing the audit log (operator-only). It is the read-only, server-
// authoritative compile the panel's "Compile" button drives in controller mode: the operator
// sees the server-computed allocation (ports, transit IPs, link-locals) and the full wg/babel/
// sysctl text BEFORE deploying, then adjusts the NAT-relevant fields and saves.
//
// Zero-knowledge: it drives controller.CompileSubgraph, whose render.GenerateKeys runs in
// AgentHeld custody — every [Interface] PrivateKey is PRIVATEKEY_PLACEHOLDER, never real key
// material — so the rendered text is safe to return to an authenticated operator. It MUST NOT
// reuse the air-gap HandleCompile (render.AirGap reconstructs real private keys).
func (h *ControllerHandler) HandleCompilePreview(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeAPIError(w, apierr.New(apierr.CodeMethodNotAllowed).With("method", "POST"))
		return
	}
	tenant, _, ok := identity(r.Context())
	if !ok {
		writeAPIError(w, apierr.New(apierr.CodeInternalIdentityMissing))
		return
	}

	// Compile the POSTed CURRENT design (the canvas the operator is editing) — NOT the stored
	// copy — so the operator can compile before saving ("Compile → adjust the NAT ip:port →
	// Save"). The body is public-keys-only (the panel strips private keys); enrollment and
	// public keys come from the registry via CompileSubgraph → enrolledSubgraph, so the POSTed
	// key fields are never trusted (and GenerateKeys(AgentHeld) emits placeholder private keys).
	topo, err := readTopology(w, r)
	if err != nil {
		writeCodedOr(w, apierr.CodeReqInvalidBody, err)
		return
	}

	nodes, err := h.store.ListNodes(r.Context(), tenant)
	if err != nil {
		writeCodedOr(w, apierr.CodeInternal, err)
		return
	}

	// Settings → FetchSettings so the preview reflects the configured mimic catalog (the previewed
	// install.sh matches what a deploy would render). No catalog ⇒ no artifacts.json (D4). An absent
	// settings record is normal → fall back to defaults (zero cs + WithDefaults), never fail.
	cs, err := h.store.GetSettings(r.Context(), tenant)
	if err != nil && !errors.Is(err, controller.ErrNotFound) {
		writeCodedOr(w, apierr.CodeInternalStorage, err)
		return
	}

	// The COMPILE HALF only — enrolled subgraph → AgentHeld keys → compile → render — with no
	// persistAllocations / Export / StageBundle / Prune / manifest / audit. That absence of
	// side effects is exactly what distinguishes a preview from a deploy.
	pfs := controller.BuildFetchSettings(cs.WithDefaults())
	pfs.AgentRolloutNodeIDs = controller.AgentRolloutNodeIDs(cs, nodes)
	result, _, skipped, err := controller.CompileSubgraph(r.Context(), topo, nodes, pfs)
	if err != nil {
		// CompileSubgraph wraps source-coded errors (%w); writeCodedOr surfaces each at its own
		// status (compile constraints 422, keygen 400, etc.), CodeCompileFailed the fallback.
		writeCodedOr(w, apierr.CodeCompileFailed, err)
		return
	}
	if result == nil {
		// Nothing enrolled yet: report the skipped set so the panel can say "no node enrolled".
		writeJSON(w, http.StatusOK, compilePreviewResponseJSON{SkippedUnenrolled: skipped})
		return
	}
	writeJSON(w, http.StatusOK, compilePreviewResponseJSON{
		CompileResponse: &CompileResponse{
			Topology:         result.Topology,
			WireGuardConfigs: result.WireGuardConfigs,
			BabelConfigs:     result.BabelConfigs,
			SysctlConfigs:    result.SysctlConfigs,
			InstallScripts:   result.InstallScripts,
			DeployScripts:    result.DeployScripts,
			Warnings:         result.Warnings,
			Manifest:         result.Manifest,
		},
		SkippedUnenrolled: skipped,
	})
}

// HandlePromote flips the staged bundles to current and bumps the generation
// (operator-only), waking any /poll waiters. Returns the new generation.
//
// It drives controller.PromoteStaged, which enforces the KEYSTONE gate: when an
// operator credential is pinned (keystone ON), the promote is refused unless a valid
// off-host signature exists over the staged membership manifest. A missing/unsigned/
// invalid manifest is a 422 (the deploy cannot go live without the off-host proof); an
// empty staged set is a 409.
func (h *ControllerHandler) HandlePromote(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeAPIError(w, apierr.New(apierr.CodeMethodNotAllowed).With("method", "POST"))
		return
	}
	tenant, operator, ok := identity(r.Context())
	if !ok {
		writeAPIError(w, apierr.New(apierr.CodeInternalIdentityMissing))
		return
	}
	gen, err := controller.PromoteStaged(r.Context(), h.store, tenant)
	if err != nil {
		if errors.Is(err, controller.ErrNoStagedBundle) {
			writeAPIError(w, apierr.New(apierr.CodeNoStagedBundle).Wrap(err))
			return
		}
		// The keystone gate (missing/unsigned/invalid manifest) is an operator-actionable
		// precondition failure, not an internal error: surface its message at 422.
		writeCodedOr(w, apierr.CodeStageFailed, err)
		return
	}
	// Audit the flip: promote is the action that changes what the fleet RUNS, so its
	// absence from the audit log was a real observability gap (plan-1). Best-effort:
	// the generation has ALREADY flipped fleet-wide — a 500 here would report a live
	// deploy as failed, and the operator's retry would 409 on the consumed stage.
	_, _ = h.store.AppendAudit(r.Context(), tenant, controller.AuditEntry{
		Timestamp: time.Now(),
		Actor:     "operator:" + operator,
		Action:    "promote",
	})
	writeJSON(w, http.StatusOK, generationResponseJSON{Generation: gen})
}

// mapConditions projects the stored controller.NodeCondition slice onto the operator wire view
// (plan-2). nil/empty in => nil out (omitempty drops the field). It reads the embedded model.Condition
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
func (h *ControllerHandler) HandleNodes(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeAPIError(w, apierr.New(apierr.CodeMethodNotAllowed).With("method", "GET"))
		return
	}
	tenant, _, ok := identity(r.Context())
	if !ok {
		writeAPIError(w, apierr.New(apierr.CodeInternalIdentityMissing))
		return
	}
	nodes, err := h.store.ListNodes(r.Context(), tenant)
	if err != nil {
		writeCodedOr(w, apierr.CodeInternalStorage, err)
		return
	}
	// Compute agent self-update rollout membership ONCE for the whole list (one settings load +
	// one membership pass): the per-node in_rollout flag the panel's update-status chip reads.
	// An absent settings record (most fleets never configure a rollout) is a benign no-op — the
	// zero ControllerSettings yields an empty rollout set (every node not-targeted).
	cs, err := h.store.GetSettings(r.Context(), tenant)
	if err != nil && !errors.Is(err, controller.ErrNotFound) {
		writeCodedOr(w, apierr.CodeInternalStorage, err)
		return
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
	writeJSON(w, http.StatusOK, out)
}

// HandleRevoke evicts a node from the fleet (operator-only). It flips the node's
// Status to NodeRevoked (preserving every other field) AND clears its API token via
// RevokeNodeAPIToken, so the node's bearer credential stops resolving immediately
// (LookupNodeByAPIToken no longer maps it to an approved node). It is the operator
// counterpart to enrollment: 404 when the node is unknown, otherwise it records a
// "revoke" audit entry and returns {node_id, revoked:true}.
func (h *ControllerHandler) HandleRevoke(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeAPIError(w, apierr.New(apierr.CodeMethodNotAllowed).With("method", "POST"))
		return
	}
	tenant, operator, ok := identity(r.Context())
	if !ok {
		writeAPIError(w, apierr.New(apierr.CodeInternalIdentityMissing))
		return
	}
	var req revokeRequestJSON
	if err := decodeJSON(w, r, &req); err != nil {
		writeCodedOr(w, apierr.CodeReqInvalidBody, err)
		return
	}
	if req.NodeID == "" {
		writeAPIError(w, apierr.New(apierr.CodeReqFieldRequired).With("field", "node_id"))
		return
	}

	// Load the existing record so we can preserve every field while flipping Status;
	// an unknown node is a 404 (there is nothing to revoke).
	node, err := h.store.GetNode(r.Context(), tenant, req.NodeID)
	if err != nil {
		if errors.Is(err, controller.ErrNotFound) {
			writeAPIError(w, apierr.New(apierr.CodeNodeNotFound).Wrap(err))
			return
		}
		writeCodedOr(w, apierr.CodeInternalStorage, err)
		return
	}

	// Flip to revoked, preserving all other fields. Also clear any pending rekey flag:
	// a revoked node will never re-register, so a left-over RekeyRequested would keep the
	// panel's "rotating" gate stuck forever (a revoked node is excluded from the deploy
	// subgraph anyway). UpsertNode matches by NodeID.
	node.Status = controller.NodeRevoked
	node.RekeyRequested = false
	if err := h.store.UpsertNode(r.Context(), tenant, node); err != nil {
		writeCodedOr(w, apierr.CodeInternalStorage, err)
		return
	}
	// Clear the API token + reverse index so the bearer credential stops resolving
	// immediately (idempotent: a no-op success if the node had no token).
	if err := h.store.RevokeNodeAPIToken(r.Context(), tenant, req.NodeID); err != nil {
		writeCodedOr(w, apierr.CodeInternalStorage, err)
		return
	}
	// Invalidate any outstanding enrollment tokens for this node so a still-valid token
	// cannot resurrect the revoked node (S5; defense in depth with the Enroll
	// NodeRevoked guard). Idempotent: a node with no outstanding tokens purges zero.
	if _, err := h.store.PurgeEnrollmentTokensForNode(r.Context(), tenant, req.NodeID); err != nil {
		writeCodedOr(w, apierr.CodeInternalStorage, err)
		return
	}
	if _, err := h.store.AppendAudit(r.Context(), tenant, controller.AuditEntry{
		Timestamp: time.Now(),
		Actor:     "operator:" + operator,
		Action:    "revoke",
		NodeID:    req.NodeID,
	}); err != nil {
		writeCodedOr(w, apierr.CodeInternalStorage, err)
		return
	}
	writeJSON(w, http.StatusOK, revokeResponseJSON{NodeID: req.NodeID, Revoked: true})
}

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

// HandleAudit returns the tenant's audit chain plus whether it verifies intact
// (operator-only). verified is true when VerifyAuditChain finds no break.
func (h *ControllerHandler) HandleAudit(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeAPIError(w, apierr.New(apierr.CodeMethodNotAllowed).With("method", "GET"))
		return
	}
	tenant, _, ok := identity(r.Context())
	if !ok {
		writeAPIError(w, apierr.New(apierr.CodeInternalIdentityMissing))
		return
	}
	entries, err := h.store.ListAudit(r.Context(), tenant)
	if err != nil {
		writeCodedOr(w, apierr.CodeInternalStorage, err)
		return
	}
	out := make([]auditEntryJSON, len(entries))
	for i, e := range entries {
		out[i] = auditEntryJSON{
			Seq:       e.Seq,
			Timestamp: e.Timestamp,
			Actor:     e.Actor,
			Action:    e.Action,
			NodeID:    e.NodeID,
		}
	}
	writeJSON(w, http.StatusOK, auditResponseJSON{
		Entries:  out,
		Verified: controller.VerifyAuditChain(entries) == -1,
	})
}

// HandleTopology returns stored topology JSON (operator-only). With no query it
// returns the CURRENT record; `?version=N` returns one retained history version
// (plan-2, D7 — the recovery substrate for a bad overwrite). The stored bytes are
// public-keys-only and returned verbatim. 404 before the first update-topology, or
// for an unknown/pruned version.
func (h *ControllerHandler) HandleTopology(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeAPIError(w, apierr.New(apierr.CodeMethodNotAllowed).With("method", "GET"))
		return
	}
	tenant, _, ok := identity(r.Context())
	if !ok {
		writeAPIError(w, apierr.New(apierr.CodeInternalIdentityMissing))
		return
	}

	var rec controller.TopologyRecord
	var err error
	if vq := r.URL.Query().Get("version"); vq != "" {
		version, perr := strconv.ParseInt(vq, 10, 64)
		if perr != nil || version <= 0 {
			writeAPIError(w, apierr.New(apierr.CodeReqFieldInvalid).With("field", "version"))
			return
		}
		rec, err = h.store.GetTopologyVersion(r.Context(), tenant, version)
		if err != nil {
			if errors.Is(err, controller.ErrNotFound) {
				writeAPIError(w, apierr.New(apierr.CodeTopologyVersionNotFound))
				return
			}
			writeCodedOr(w, apierr.CodeInternalStorage, err)
			return
		}
	} else {
		rec, err = h.store.GetTopology(r.Context(), tenant)
		if err != nil {
			if errors.Is(err, controller.ErrNotFound) {
				writeAPIError(w, apierr.New(apierr.CodeNoTopologyStored))
				return
			}
			writeCodedOr(w, apierr.CodeInternalStorage, err)
			return
		}
	}
	// The stored JSON is returned verbatim (it is already valid JSON, validated at
	// update-topology time).
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(rec.JSON)
}

// HandleTopologyVersions lists the retained topology versions, newest first
// (operator-only; metadata only — fetch a payload via GET /topology?version=N).
func (h *ControllerHandler) HandleTopologyVersions(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeAPIError(w, apierr.New(apierr.CodeMethodNotAllowed).With("method", "GET"))
		return
	}
	tenant, _, ok := identity(r.Context())
	if !ok {
		writeAPIError(w, apierr.New(apierr.CodeInternalIdentityMissing))
		return
	}
	infos, err := h.store.ListTopologyVersions(r.Context(), tenant)
	if err != nil {
		writeCodedOr(w, apierr.CodeInternalStorage, err)
		return
	}
	out := make([]topologyVersionJSON, len(infos))
	for i, v := range infos {
		out[i] = topologyVersionJSON{Version: v.Version, UpdatedAt: v.UpdatedAt, Bytes: v.Bytes}
	}
	writeJSON(w, http.StatusOK, topologyVersionsResponseJSON{Versions: out, Limit: controller.TopologyHistoryLimit})
}

// HandleEnrollmentToken mints a single-use, node-scoped enrollment token
// (operator-only) and returns its plaintext ONCE. The controller stores only the
// token hash (CreateEnrollmentToken), so the plaintext cannot be recovered later.
func (h *ControllerHandler) HandleEnrollmentToken(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeAPIError(w, apierr.New(apierr.CodeMethodNotAllowed).With("method", "POST"))
		return
	}
	tenant, _, ok := identity(r.Context())
	if !ok {
		writeAPIError(w, apierr.New(apierr.CodeInternalIdentityMissing))
		return
	}
	var req enrollmentTokenRequestJSON
	if err := decodeJSON(w, r, &req); err != nil {
		writeCodedOr(w, apierr.CodeReqInvalidBody, err)
		return
	}
	if req.NodeID == "" {
		writeAPIError(w, apierr.New(apierr.CodeReqFieldRequired).With("field", "node_id"))
		return
	}
	// A node must never be granted an enrollment token AS the operator (the operator
	// identity is reserved; enrolling under it is rejected at /enroll, but reject the
	// token mint too for a clear, early error).
	if h.isReservedNodeID(req.NodeID) {
		writeAPIError(w, apierr.New(apierr.CodeNodeIDReserved))
		return
	}
	// Bound the TTL server-side: an enrollment token is a one-shot node bring-up
	// credential, not a standing capability. Without an upper cap an operator could mint
	// a year-long token that, combined with re-enroll, is a long-lived node-takeover /
	// resurrection vector (S6).
	const maxEnrollmentTokenTTLSeconds = 7 * 24 * 60 * 60 // 7 days
	if req.TTLSeconds <= 0 || req.TTLSeconds > maxEnrollmentTokenTTLSeconds {
		writeAPIError(w, apierr.New(apierr.CodeReqFieldInvalid).With("field", "ttl_seconds"))
		return
	}

	now := time.Now()
	plaintext, tok := controller.NewEnrollmentToken(req.NodeID, time.Duration(req.TTLSeconds)*time.Second, now)
	if err := h.store.CreateEnrollmentToken(r.Context(), tenant, tok); err != nil {
		writeCodedOr(w, apierr.CodeInternalStorage, err)
		return
	}
	if _, err := h.store.AppendAudit(r.Context(), tenant, controller.AuditEntry{
		Timestamp: now,
		Actor:     "operator:" + h.operatorName,
		Action:    "enrollment-token",
		NodeID:    req.NodeID,
	}); err != nil {
		writeCodedOr(w, apierr.CodeInternalStorage, err)
		return
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
	if rec, err := h.store.GetTopology(r.Context(), tenant); err == nil && !topologyHasNode(rec.JSON, req.NodeID) {
		resp.Warning = "node-id not present in the stored design; it will be skipped at stage until added"
	}
	writeJSON(w, http.StatusOK, resp)
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
