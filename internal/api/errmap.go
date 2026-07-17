package api

// errmap.go is the SINGLE table for "which controller sentinel becomes which apierr code".
// Before it, each handler carried its own errors.Is ladder, so the mapping was 28 scattered
// judgments across 7 files with no auditable overview. mapControllerErr collects the
// CONTEXT-FREE sentinels — the ones that mean the same thing wherever they surface — into
// one switch; codedErr is the generic relay twin of writeCodedOr for the adapter's
// value-returning handlers.

import (
	"errors"
	"strconv"
	"strings"

	"github.com/kunorikiku/yet-another-overlay-generator/internal/apierr"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/compiler"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/controller"
)

// mapControllerErr maps a CONTEXT-FREE controller sentinel to its canonical coded error, or
// returns nil when err is not one of them (the caller then keeps its own handling / relays
// via codedErr). "Context-free" means the sentinel maps to the SAME apierr code regardless
// of which handler saw it, so centralizing it changes no response:
//
//   - ErrNoStagedBundle    → no_staged_bundle (409)
//   - ErrTokenInvalid      → enrollment_token_invalid (401)
//   - ErrTokenConsumed     → enrollment_token_invalid (401)   [same code as ErrTokenInvalid]
//   - ErrNodeRevoked       → enroll_node_revoked (409)
//   - ErrInvalidWGKey      → req_field_invalid {field:wg_public_key} (400)
//   - ErrDuplicateWGKey    → duplicate_wg_key (409)
//   - ErrTopologyChanged   → topology_changed (409)
//
// The cause is wrapped for errors.Is/As + logs; it never reaches the wire (apierr.Error
// serializes only code+message+params — see internal/apierr), so wrapping is response-
// invisible and kept only to preserve the error chain the old inline branches produced.
//
// controller.ErrNotFound is DELIBERATELY ABSENT. It is context-SPECIFIC — the same sentinel
// means different things in different handlers and maps to six different codes (node_not_found,
// config_not_found, no_topology_stored, topology_version_not_found, no_staged_manifest,
// no_pinned_credential), and in several places it is not an error at all but a success/
// control-flow signal (a GetSettings miss falls back to defaults; a GetOperatorCredential
// miss means "keystone off, Pinned:false, 200"; a mid-loop GetNode miss is skipped).
// Collapsing it here would silently rewrite those responses, so every handler keeps its own
// ErrNotFound branch. errmap_test.go documents and pins this exclusion.
func mapControllerErr(err error) *apierr.Error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, controller.ErrNoStagedBundle):
		return apierr.New(apierr.CodeNoStagedBundle).Wrap(err)
	case errors.Is(err, controller.ErrTokenInvalid), errors.Is(err, controller.ErrTokenConsumed):
		return apierr.New(apierr.CodeEnrollmentTokenInvalid).Wrap(err)
	case errors.Is(err, controller.ErrNodeRevoked):
		return apierr.New(apierr.CodeEnrollNodeRevoked).Wrap(err)
	case errors.Is(err, controller.ErrInvalidWGKey):
		return apierr.New(apierr.CodeReqFieldInvalid).With("field", "wg_public_key").Wrap(err)
	case errors.Is(err, controller.ErrDuplicateWGKey):
		return apierr.New(apierr.CodeDuplicateWGKey).Wrap(err)
	case errors.Is(err, controller.ErrTopologyChanged):
		return apierr.New(apierr.CodeTopologyChanged).Wrap(err)
	case errors.Is(err, controller.ErrTelemetryProbesRequireKeystone):
		return apierr.New(apierr.CodeTelemetryProbesRequireKeystone).Wrap(err)
	default:
		var readiness *controller.TelemetryPolicyReadinessError
		if !errors.As(err, &readiness) {
			return nil
		}
		bounded := readiness.NodeIDs
		if len(bounded) > 16 {
			bounded = bounded[:16]
		}
		return apierr.New(apierr.CodeTelemetryPolicyUpgradeRequired).
			With("count", strconv.Itoa(len(readiness.NodeIDs))).
			With("nodes", strings.Join(bounded, ", ")).
			Wrap(err)
	}
}

// mapTopologyValidationErr converts the compiler's structured schema/semantic failure into one
// stable HTTP 422 envelope. Validator codes deliberately remain distinct from apierr codes, so the
// first finding is carried as params for edge localization instead of being promoted into an HTTP
// code. Unknown compile, render, export, and storage failures return nil and retain the handler's
// operational 500 fallback.
func mapTopologyValidationErr(err error) *apierr.Error {
	var validationErr *compiler.TopologyValidationError
	if !errors.As(err, &validationErr) || len(validationErr.Findings) == 0 {
		return nil
	}
	finding := validationErr.Findings[0]
	mapped := apierr.New(apierr.CodeTopologyValidationFailed).
		With("field", finding.Field).
		With("validation_code", finding.Code).
		With("validation_message", finding.Message)
	for key, value := range finding.Params {
		mapped.With("validation_param_"+key, value)
	}
	return mapped.Wrap(err)
}

// codedErr is the return-value twin of writeCodedOr (handler.go): it surfaces err as its own
// *apierr.Error when err is or wraps one, otherwise it buckets err under `fallback` (wrapping
// the cause, which is never serialized). Adapter-based handlers whose fn returns
// (any, *apierr.Error) use it where the hand-rolled body called writeCodedOr(w, fallback,
// err). It does NOT consult mapControllerErr: a relay seam must keep its exact per-handler
// fallback bucket (e.g. CodeInternalStorage vs CodeStageFailed), and sentinel mapping is an
// EXPLICIT step the handler opts into via mapControllerErr, never an implicit relay side
// effect.
func codedErr(fallback apierr.Code, err error) *apierr.Error {
	var ae *apierr.Error
	if errors.As(err, &ae) {
		return ae
	}
	return apierr.New(fallback).Wrap(err)
}
