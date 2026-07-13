package api

// handler_deploy.go holds the operator deploy/stage flow handlers: compile+stage the
// enrolled subgraph (stage), the read-only deploy/compile previews, and promote staged->current.

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/kunorikiku/yet-another-overlay-generator/internal/apierr"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/controller"
)

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
// re-stage (changed vs served) vs skip (unchanged), plus the keystone-full-restage flag — WITHOUT
// staging. It compiles the POSTed CURRENT canvas (what a Deploy pushes+stages), not the stored copy.
// The Deploy dialog calls it on open so the operator sees "N updated, M unchanged" (and any pending
// keystone full-restage) before deploying.
func (h *ControllerHandler) HandleDeployPreview(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeAPIError(w, apierr.New(apierr.CodeMethodNotAllowed).With("method", "POST"))
		return
	}
	tenant, _, ok := identity(r.Context())
	if !ok {
		writeAPIError(w, apierr.New(apierr.CodeInternalIdentityMissing))
		return
	}
	// Preview the POSTed CURRENT canvas (what a Deploy will push+stage), NOT the stored copy — a Deploy
	// pushes the canvas via update-topology then stages, so previewing the stored design would misreport
	// the blast radius with unsaved edits. Public-keys-only + zero-knowledge (CompileSubgraph emits
	// placeholder private keys); the POSTed key fields are never trusted.
	topo, err := readTopology(w, r)
	if err != nil {
		writeCodedOr(w, apierr.CodeReqInvalidBody, err)
		return
	}
	pv, err := controller.DeployPreview(r.Context(), h.store, tenant, topo)
	if err != nil {
		writeCodedOr(w, apierr.CodeInternal, err)
		return
	}
	nodes := make([]deployPreviewNodeJSON, 0, len(pv.Nodes))
	for _, n := range pv.Nodes {
		nodes = append(nodes, deployPreviewNodeJSON{NodeID: n.NodeID, Name: n.Name, Changed: n.Changed})
	}
	writeJSON(w, http.StatusOK, deployPreviewResponseJSON{
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
