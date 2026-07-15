package api

// handler_topology.go holds the operator topology-store handlers: store a new version
// (update-topology), read the current or a retained version, and list retained versions.
// They are routed through the op()/opRaw() adapter (routes_controller.go), which applies the
// method guard + structural identity() check before the body runs. HandleTopology uses opRaw
// because it writes the stored bytes VERBATIM (its own Content-Type), not a JSON-marshaled value.

import (
	"context"
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
func (h *ControllerHandler) HandleUpdateTopology(ctx context.Context, tenant controller.TenantID, actor string, w http.ResponseWriter, r *http.Request) (any, *apierr.Error) {
	body, err := readControllerBody(w, r)
	if err != nil {
		return nil, codedErr(apierr.CodeReqInvalidBody, err)
	}
	// Custody gate: unmarshal into the model (not a substring match, which would
	// false-positive on names/notes) and refuse any private key material. Bodies are
	// always panel-produced model.Topology, so an unmarshal failure is equally a 400 —
	// storing bytes we cannot custody-check would reopen the hole this gate closes.
	var topo model.Topology
	if err := json.Unmarshal(body, &topo); err != nil {
		return nil, apierr.New(apierr.CodeReqInvalidBody).Wrap(err)
	}
	for _, n := range topo.Nodes {
		if n.WireGuardPrivateKey != "" {
			return nil, apierr.New(apierr.CodeCustodyPrivateKey)
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
		return nil, codedErr(apierr.CodeInternalStorage, err)
	}

	rec, err := h.store.PutTopology(ctx, tenant, canonical)
	if err != nil {
		return nil, codedErr(apierr.CodeInternalStorage, err)
	}
	// Post-commit audit is best-effort: the version is already stored, and converting
	// an audit-write hiccup into a 500 would tell the operator the action failed when
	// it committed (the retry would mint a duplicate version). Same convention as the
	// settings/login audits.
	_, _ = h.store.AppendAudit(ctx, tenant, controller.AuditEntry{
		Timestamp: time.Now(),
		Actor:     "operator:" + actor,
		Action:    "update-topology",
	})
	return map[string]int64{"version": rec.Version}, nil
}

// HandleTopology returns stored topology JSON (operator-only). With no query it
// returns the CURRENT record; `?version=N` returns one retained history version
// (plan-2, D7 — the recovery substrate for a bad overwrite). The stored bytes are
// public-keys-only and returned verbatim. 404 before the first update-topology, or
// for an unknown/pruned version. Routed through opRaw: it writes the stored bytes with
// its OWN "application/json; charset=utf-8" Content-Type, which writeJSON cannot reproduce.
func (h *ControllerHandler) HandleTopology(ctx context.Context, tenant controller.TenantID, _ string, w http.ResponseWriter, r *http.Request) *apierr.Error {
	var rec controller.TopologyRecord
	var err error
	if vq := r.URL.Query().Get("version"); vq != "" {
		version, perr := strconv.ParseInt(vq, 10, 64)
		if perr != nil || version <= 0 {
			return apierr.New(apierr.CodeReqFieldInvalid).With("field", "version")
		}
		rec, err = h.store.GetTopologyVersion(ctx, tenant, version)
		if err != nil {
			if errors.Is(err, controller.ErrNotFound) {
				return apierr.New(apierr.CodeTopologyVersionNotFound)
			}
			return codedErr(apierr.CodeInternalStorage, err)
		}
	} else {
		rec, err = h.store.GetTopology(ctx, tenant)
		if err != nil {
			if errors.Is(err, controller.ErrNotFound) {
				return apierr.New(apierr.CodeNoTopologyStored)
			}
			return codedErr(apierr.CodeInternalStorage, err)
		}
	}
	// The stored JSON is returned verbatim (it is already valid JSON, validated at
	// update-topology time).
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(rec.JSON)
	return nil
}

// HandleTopologyVersions lists the retained topology versions, newest first
// (operator-only; metadata only — fetch a payload via GET /topology?version=N).
func (h *ControllerHandler) HandleTopologyVersions(ctx context.Context, tenant controller.TenantID, _ string, _ http.ResponseWriter, _ *http.Request) (any, *apierr.Error) {
	infos, err := h.store.ListTopologyVersions(ctx, tenant)
	if err != nil {
		return nil, codedErr(apierr.CodeInternalStorage, err)
	}
	out := make([]topologyVersionJSON, len(infos))
	for i, v := range infos {
		out[i] = topologyVersionJSON{Version: v.Version, UpdatedAt: v.UpdatedAt, Bytes: v.Bytes}
	}
	return topologyVersionsResponseJSON{Versions: out, Limit: controller.TopologyHistoryLimit}, nil
}
