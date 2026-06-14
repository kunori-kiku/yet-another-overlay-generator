package api

// handler_totp.go is the operator TOTP-2FA management surface (plan-5.2): enroll,
// confirm, disable, and status. All routes are operator-authenticated and act on the
// CURRENTLY LOGGED-IN operator's account (resolved from the request identity).
//
// TOTP gates the panel login only — it is never a keystone signing mechanism (it is
// symmetric and time-based, not an asymmetric content-bound signature). See totp.go and
// docs/spec/controller/operator-auth.md.

import (
	"net/http"
	"strings"
	"time"

	"github.com/kunorikiku/yet-another-overlay-generator/internal/apierr"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/controller"
)

type totpEnrollResponseJSON struct {
	Secret string `json:"secret"`      // base32 shared secret (also shown to the operator)
	URI    string `json:"otpauth_uri"` // otpauth:// for QR/import
}

type totpConfirmRequestJSON struct {
	Secret string `json:"secret"` // the secret from /totp/enroll, echoed back
	Code   string `json:"code"`   // a current code proving the operator can generate them
}

type totpDisableRequestJSON struct {
	Code string `json:"code"` // a current code, required to turn 2FA off
}

type totpStatusResponseJSON struct {
	Enabled bool `json:"enabled"`
}

// currentOperator resolves the logged-in operator's account from the request identity.
// It returns ok=false (after writing an error) when the caller is not a real operator
// account — e.g. authenticated via the break-glass token, which has no account and so
// cannot manage 2FA.
func (h *ControllerHandler) currentOperator(w http.ResponseWriter, r *http.Request) (controller.Operator, controller.TenantID, bool) {
	tenant, name, ok := identity(r.Context())
	if !ok {
		writeAPIError(w, apierr.New(apierr.CodeInternalIdentityMissing))
		return controller.Operator{}, "", false
	}
	op, err := h.store.GetOperator(r.Context(), tenant, name)
	if err != nil {
		writeAPIError(w, apierr.New(apierr.CodeTotpRequiresLogin).Wrap(err))
		return controller.Operator{}, "", false
	}
	return op, tenant, true
}

// HandleTOTPStatus (GET) reports whether the current operator has 2FA enrolled.
func (h *ControllerHandler) HandleTOTPStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeAPIError(w, apierr.New(apierr.CodeMethodNotAllowed).With("method", "GET"))
		return
	}
	op, _, ok := h.currentOperator(w, r)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, totpStatusResponseJSON{Enabled: op.TOTPEnabled()})
}

// HandleTOTPEnroll (POST) mints a fresh TOTP secret + otpauth URI for the operator to
// add to an authenticator app. The secret is NOT saved here — the operator proves they
// can generate codes via /totp/confirm before it is activated.
func (h *ControllerHandler) HandleTOTPEnroll(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeAPIError(w, apierr.New(apierr.CodeMethodNotAllowed).With("method", "POST"))
		return
	}
	op, tenant, ok := h.currentOperator(w, r)
	if !ok {
		return
	}
	secret := controller.GenerateTOTPSecret()
	uri := controller.TOTPProvisioningURI(secret, op.Username, "YAOG ("+string(tenant)+")")
	writeJSON(w, http.StatusOK, totpEnrollResponseJSON{Secret: secret, URI: uri})
}

// HandleTOTPConfirm (POST) activates 2FA: it verifies a code against the just-issued
// secret (proving the authenticator is set up) and then persists the secret. Only on a
// valid code is TOTP turned on.
func (h *ControllerHandler) HandleTOTPConfirm(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeAPIError(w, apierr.New(apierr.CodeMethodNotAllowed).With("method", "POST"))
		return
	}
	op, tenant, ok := h.currentOperator(w, r)
	if !ok {
		return
	}
	var req totpConfirmRequestJSON
	if err := decodeJSON(w, r, &req); err != nil {
		writeCodedOr(w, apierr.CodeReqInvalidBody, err)
		return
	}
	if strings.TrimSpace(req.Secret) == "" {
		writeAPIError(w, apierr.New(apierr.CodeReqFieldRequired).With("field", "secret"))
		return
	}
	now := time.Now().UTC()
	totpOK, step := controller.VerifyTOTP(req.Secret, req.Code, now, 0)
	if !totpOK {
		writeAPIError(w, apierr.New(apierr.CodeTotpInvalidCode))
		return
	}
	op.TOTPSecret = req.Secret
	op.TOTPLastUsedStep = step
	op.UpdatedAt = now
	if err := h.store.PutOperator(r.Context(), tenant, op); err != nil {
		writeCodedOr(w, apierr.CodeInternalStorage, err)
		return
	}
	_, _ = h.store.AppendAudit(r.Context(), tenant, controller.AuditEntry{
		Timestamp: now, Actor: "operator:" + op.Username, Action: "totp-enabled",
	})
	writeJSON(w, http.StatusOK, totpStatusResponseJSON{Enabled: true})
}

// HandleTOTPDisable (POST) turns 2FA off, requiring a current code so a hijacked session
// cannot trivially disable the second factor. Idempotent if already disabled.
func (h *ControllerHandler) HandleTOTPDisable(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeAPIError(w, apierr.New(apierr.CodeMethodNotAllowed).With("method", "POST"))
		return
	}
	op, tenant, ok := h.currentOperator(w, r)
	if !ok {
		return
	}
	if !op.TOTPEnabled() {
		writeJSON(w, http.StatusOK, totpStatusResponseJSON{Enabled: false})
		return
	}
	var req totpDisableRequestJSON
	if err := decodeJSON(w, r, &req); err != nil {
		writeCodedOr(w, apierr.CodeReqInvalidBody, err)
		return
	}
	now := time.Now().UTC()
	if totpOK, _ := controller.VerifyTOTP(op.TOTPSecret, req.Code, now, op.TOTPLastUsedStep); !totpOK {
		writeAPIError(w, apierr.New(apierr.CodeTotpInvalidCode))
		return
	}
	op.TOTPSecret = ""
	op.TOTPLastUsedStep = 0
	op.UpdatedAt = now
	if err := h.store.PutOperator(r.Context(), tenant, op); err != nil {
		writeCodedOr(w, apierr.CodeInternalStorage, err)
		return
	}
	_, _ = h.store.AppendAudit(r.Context(), tenant, controller.AuditEntry{
		Timestamp: now, Actor: "operator:" + op.Username, Action: "totp-disabled",
	})
	writeJSON(w, http.StatusOK, totpStatusResponseJSON{Enabled: false})
}
