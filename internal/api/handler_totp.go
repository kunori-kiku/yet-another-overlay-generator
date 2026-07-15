package api

// handler_totp.go is the operator TOTP-2FA management surface (plan-5.2): enroll,
// confirm, disable, and status. All routes are operator-authenticated and act on the
// CURRENTLY LOGGED-IN operator's account (resolved from the request identity the op()
// adapter established). They are routed through the op() adapter (routes_controller.go),
// which applies the method guard + structural identity() check before the body runs.
//
// TOTP gates the panel login only — it is never a keystone signing mechanism (it is
// symmetric and time-based, not an asymmetric content-bound signature). See totp.go and
// docs/spec/controller/operator-auth.md.

import (
	"context"
	"errors"
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

// currentOperatorAccount resolves the logged-in operator's stored account from the identity
// the op() adapter already established (tenant + operator name). It returns a coded
// CodeTotpRequiresLogin error when the caller is not a real operator account — e.g.
// authenticated via the break-glass token, which has no account and so cannot manage 2FA /
// passkeys. Shared by the TOTP and passkey-management handlers.
func (h *ControllerHandler) currentOperatorAccount(ctx context.Context, tenant controller.TenantID, name string) (controller.Operator, *apierr.Error) {
	if kind, ok := operatorAuthKindFromCtx(ctx); !ok || kind != operatorAuthSession {
		return controller.Operator{}, apierr.New(apierr.CodeTotpRequiresLogin)
	}
	op, err := h.store.GetOperator(ctx, tenant, name)
	if err != nil {
		return controller.Operator{}, apierr.New(apierr.CodeTotpRequiresLogin).Wrap(err)
	}
	return op, nil
}

// HandleTOTPStatus (GET) reports whether the current operator has 2FA enrolled.
func (h *ControllerHandler) HandleTOTPStatus(ctx context.Context, tenant controller.TenantID, actor string, _ http.ResponseWriter, _ *http.Request) (any, *apierr.Error) {
	op, aerr := h.currentOperatorAccount(ctx, tenant, actor)
	if aerr != nil {
		return nil, aerr
	}
	return totpStatusResponseJSON{Enabled: op.TOTPEnabled()}, nil
}

// HandleTOTPEnroll (POST) mints a fresh TOTP secret + otpauth URI for the operator to
// add to an authenticator app. The secret is NOT saved here — the operator proves they
// can generate codes via /totp/confirm before it is activated.
func (h *ControllerHandler) HandleTOTPEnroll(ctx context.Context, tenant controller.TenantID, actor string, _ http.ResponseWriter, _ *http.Request) (any, *apierr.Error) {
	op, aerr := h.currentOperatorAccount(ctx, tenant, actor)
	if aerr != nil {
		return nil, aerr
	}
	secret := controller.GenerateTOTPSecret()
	uri := controller.TOTPProvisioningURI(secret, op.Username, "YAOG ("+string(tenant)+")")
	return totpEnrollResponseJSON{Secret: secret, URI: uri}, nil
}

// HandleTOTPConfirm (POST) activates 2FA: it verifies a code against the just-issued
// secret (proving the authenticator is set up) and then persists the secret. Only on a
// valid code is TOTP turned on.
func (h *ControllerHandler) HandleTOTPConfirm(ctx context.Context, tenant controller.TenantID, actor string, w http.ResponseWriter, r *http.Request) (any, *apierr.Error) {
	op, aerr := h.currentOperatorAccount(ctx, tenant, actor)
	if aerr != nil {
		return nil, aerr
	}
	var req totpConfirmRequestJSON
	if err := decodeJSON(w, r, &req); err != nil {
		return nil, codedErr(apierr.CodeReqInvalidBody, err)
	}
	if strings.TrimSpace(req.Secret) == "" {
		return nil, apierr.New(apierr.CodeReqFieldRequired).With("field", "secret")
	}
	now := time.Now().UTC()
	totpOK, step := controller.VerifyTOTP(req.Secret, req.Code, now, 0)
	if !totpOK {
		return nil, apierr.New(apierr.CodeTotpInvalidCode)
	}
	expected := controller.TOTPState{Secret: op.TOTPSecret, LastUsedStep: op.TOTPLastUsedStep}
	next := controller.TOTPState{Secret: req.Secret, LastUsedStep: step}
	if err := h.store.CompareAndSetTOTPState(ctx, tenant, op.Username, expected, next, now); err != nil {
		if errors.Is(err, controller.ErrTOTPStateChanged) {
			return nil, apierr.New(apierr.CodeTOTPStateChanged)
		}
		return nil, codedErr(apierr.CodeInternalStorage, err)
	}
	_, _ = h.store.AppendAudit(ctx, tenant, controller.AuditEntry{
		Timestamp: now, Actor: "operator:" + op.Username, Action: "totp-enabled",
	})
	return totpStatusResponseJSON{Enabled: true}, nil
}

// HandleTOTPDisable (POST) turns 2FA off, requiring a current code so a hijacked session
// cannot trivially disable the second factor. Idempotent if already disabled.
func (h *ControllerHandler) HandleTOTPDisable(ctx context.Context, tenant controller.TenantID, actor string, w http.ResponseWriter, r *http.Request) (any, *apierr.Error) {
	op, aerr := h.currentOperatorAccount(ctx, tenant, actor)
	if aerr != nil {
		return nil, aerr
	}
	if !op.TOTPEnabled() {
		return totpStatusResponseJSON{Enabled: false}, nil
	}
	var req totpDisableRequestJSON
	if err := decodeJSON(w, r, &req); err != nil {
		return nil, codedErr(apierr.CodeReqInvalidBody, err)
	}
	now := time.Now().UTC()
	if totpOK, _ := controller.VerifyTOTP(op.TOTPSecret, req.Code, now, op.TOTPLastUsedStep); !totpOK {
		return nil, apierr.New(apierr.CodeTotpInvalidCode)
	}
	expected := controller.TOTPState{Secret: op.TOTPSecret, LastUsedStep: op.TOTPLastUsedStep}
	if err := h.store.CompareAndSetTOTPState(ctx, tenant, op.Username, expected, controller.TOTPState{}, now); err != nil {
		if errors.Is(err, controller.ErrTOTPStateChanged) {
			return nil, apierr.New(apierr.CodeTOTPStateChanged)
		}
		return nil, codedErr(apierr.CodeInternalStorage, err)
	}
	_, _ = h.store.AppendAudit(ctx, tenant, controller.AuditEntry{
		Timestamp: now, Actor: "operator:" + op.Username, Action: "totp-disabled",
	})
	return totpStatusResponseJSON{Enabled: false}, nil
}
