package api

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"

	"github.com/kunorikiku/yet-another-overlay-generator/internal/apierr"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/controller"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/model"
)

// handler_manual_node.go serves the per-node bundle DOWNLOAD for a MANUAL (deployment_mode=manual,
// hand-deployed, agent-less) node in a controller topology (mixed-controller-local-mode plan-3). A
// managed node's agent pulls its config from GET /config; a manual node has no agent, so the operator
// downloads the same SERVED (promoted, off-host-signed) bundle here and installs it by hand (the kit,
// plan-4, splices the private key over the placeholder). The bundle carries the PRIVATEKEY_PLACEHOLDER,
// never real key material, so it is safe to hand to an authenticated operator — zero-knowledge holds.

// HandleManualNodeBundle returns a manual node's promoted bundle as a downloadable ZIP. Operator-only
// (registered under the operator mux). The node id is the `node` query param. It is restricted to
// manual nodes: a managed node's bundle is delivered to its agent via /config, not here.
func (h *ControllerHandler) HandleManualNodeBundle(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeAPIError(w, apierr.New(apierr.CodeMethodNotAllowed).With("method", "GET"))
		return
	}
	tenant, _, ok := identity(r.Context())
	if !ok {
		writeAPIError(w, apierr.New(apierr.CodeInternalIdentityMissing))
		return
	}
	nodeID := r.URL.Query().Get("node")
	if nodeID == "" {
		writeAPIError(w, apierr.New(apierr.CodeReqInvalidBody).With("detail", "missing required query parameter: node"))
		return
	}

	// Manual-only: confirm the requested node is a manual node in the stored topology. A non-manual
	// or absent node is a 404 (there is no downloadable manual bundle for it) — managed nodes pull via
	// /config. This also keeps the endpoint from being a generic any-node bundle reader.
	manual, err := h.nodeIsManual(r, tenant, nodeID)
	if err != nil {
		writeCodedOr(w, apierr.CodeInternalStorage, err)
		return
	}
	if !manual {
		writeAPIError(w, apierr.New(apierr.CodeConfigNotFound).With("detail", "no manual node with that id (a managed node's config is pulled by its agent)"))
		return
	}

	// Serve the SAME atomic snapshot the agent /config path serves: the node's promoted bundle plus,
	// when the keystone is on, the off-host-signed membership trust-list. A manual node that has not
	// been staged+promoted yet has no served config → 404.
	sc, err := h.store.GetServedConfig(r.Context(), tenant, nodeID)
	if err != nil {
		if errors.Is(err, controller.ErrNotFound) {
			writeAPIError(w, apierr.New(apierr.CodeConfigNotFound).Wrap(err))
			return
		}
		writeCodedOr(w, apierr.CodeInternalStorage, err)
		return
	}

	files := make(map[string][]byte, len(sc.Bundle.Files)+2)
	for path, content := range sc.Bundle.Files {
		files[path] = content
	}
	if sc.KeystoneOn {
		// Fail closed if the served snapshot somehow lacks the signed manifest (a promote cannot
		// occur without one, so this should be unreachable) — never hand out an unattested bundle.
		if !sc.HasTrustList {
			writeAPIError(w, apierr.New(apierr.CodeKeystoneNoSignedManifest))
			return
		}
		files["trustlist.json"] = sc.TrustList.TrustListJSON
		files["trustlist.sig"] = sc.TrustList.SignatureJSON
	}

	buf, err := zipBundleFiles(files)
	if err != nil {
		writeCodedOr(w, apierr.CodeInternalStorage, err)
		return
	}
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", nodeID+"-bundle.zip"))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(buf.Bytes())
}

// nodeIsManual reports whether the stored topology carries a node with id nodeID whose deployment_mode
// is manual. A missing topology / unknown node ⇒ false (not an error).
func (h *ControllerHandler) nodeIsManual(r *http.Request, tenant controller.TenantID, nodeID string) (bool, error) {
	rec, err := h.store.GetTopology(r.Context(), tenant)
	if err != nil {
		if errors.Is(err, controller.ErrNotFound) {
			return false, nil
		}
		return false, err
	}
	var topo model.Topology
	if err := json.Unmarshal(rec.JSON, &topo); err != nil {
		return false, err
	}
	for i := range topo.Nodes {
		if topo.Nodes[i].ID == nodeID {
			return topo.Nodes[i].IsManual(), nil
		}
	}
	return false, nil
}

// zipBundleFiles packs a node's bundle file map into a deterministic ZIP (names sorted) for download.
func zipBundleFiles(files map[string][]byte) (*bytes.Buffer, error) {
	buf := &bytes.Buffer{}
	zw := zip.NewWriter(buf)
	names := make([]string, 0, len(files))
	for n := range files {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		f, err := zw.Create(n)
		if err != nil {
			return nil, err
		}
		if _, err := f.Write(files[n]); err != nil {
			return nil, err
		}
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return buf, nil
}
