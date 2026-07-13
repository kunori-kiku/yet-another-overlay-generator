package api

// handler_audit.go holds the operator audit-chain handler. It is routed through the op()
// adapter (routes_controller.go), which applies the method guard + structural identity()
// check before the body runs.

import (
	"context"
	"net/http"

	"github.com/kunorikiku/yet-another-overlay-generator/internal/apierr"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/controller"
)

// HandleAudit returns the tenant's audit chain plus whether it verifies intact
// (operator-only). verified is true when VerifyAuditChain finds no break.
func (h *ControllerHandler) HandleAudit(ctx context.Context, tenant controller.TenantID, _ string, _ http.ResponseWriter, _ *http.Request) (any, *apierr.Error) {
	entries, err := h.store.ListAudit(ctx, tenant)
	if err != nil {
		return nil, codedErr(apierr.CodeInternalStorage, err)
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
	return auditResponseJSON{
		Entries:  out,
		Verified: controller.VerifyAuditChain(entries) == -1,
	}, nil
}
