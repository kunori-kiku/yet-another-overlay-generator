package api

// handler_audit.go holds the operator audit-chain handler.

import (
	"net/http"

	"github.com/kunorikiku/yet-another-overlay-generator/internal/apierr"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/controller"
)

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
