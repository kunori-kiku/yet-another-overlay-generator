package api

// handler_topology.go holds the operator topology-store handlers: store a new version
// (update-topology), read the current or a retained version, and list retained versions.

import (
	"encoding/json"
	"errors"
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
