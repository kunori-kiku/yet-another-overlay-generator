package api

import (
	"errors"
	"fmt"
	"net/http"
	"testing"

	"github.com/kunorikiku/yet-another-overlay-generator/internal/apierr"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/controller"
)

// TestMapControllerErr_Table is the perpetual guard on the central sentinel→code table
// (errmap.go): every context-free controller sentinel maps to EXACTLY one apierr code + HTTP
// status, and the wrapped cause is preserved for errors.Is. If a handler's inline ladder and
// this table ever disagree, the coded-error envelope the FE i18n catalog joins on would drift —
// so the mapping is pinned here. Each sentinel is tested both bare and %w-wrapped (the store /
// controller layers wrap with %w), proving the mapper matches THROUGH a wrap.
func TestMapControllerErr_Table(t *testing.T) {
	cases := []struct {
		name   string
		err    error
		code   apierr.Code
		status int
		field  string // expected params["field"]; "" when the code carries none
	}{
		{"no_staged_bundle", controller.ErrNoStagedBundle, apierr.CodeNoStagedBundle, http.StatusConflict, ""},
		{"token_invalid", controller.ErrTokenInvalid, apierr.CodeEnrollmentTokenInvalid, http.StatusUnauthorized, ""},
		{"token_consumed", controller.ErrTokenConsumed, apierr.CodeEnrollmentTokenInvalid, http.StatusUnauthorized, ""},
		{"node_revoked", controller.ErrNodeRevoked, apierr.CodeEnrollNodeRevoked, http.StatusConflict, ""},
		{"invalid_wg_key", controller.ErrInvalidWGKey, apierr.CodeReqFieldInvalid, http.StatusBadRequest, "wg_public_key"},
		{"duplicate_wg_key", controller.ErrDuplicateWGKey, apierr.CodeDuplicateWGKey, http.StatusConflict, ""},
		{"telemetry_probes_require_keystone", controller.ErrTelemetryProbesRequireKeystone, apierr.CodeTelemetryProbesRequireKeystone, http.StatusPreconditionFailed, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			for _, in := range []error{tc.err, fmt.Errorf("controller: %w", tc.err)} {
				ae := mapControllerErr(in)
				if ae == nil {
					t.Fatalf("mapControllerErr(%v) = nil, want coded %s", in, tc.code)
				}
				if ae.Code() != tc.code {
					t.Errorf("code = %q, want %q", ae.Code(), tc.code)
				}
				if ae.Status() != tc.status {
					t.Errorf("status = %d, want %d", ae.Status(), tc.status)
				}
				if tc.field != "" && ae.Params()["field"] != tc.field {
					t.Errorf("params[field] = %q, want %q", ae.Params()["field"], tc.field)
				}
				// The cause is wrapped for errors.Is/As (never serialized).
				if !errors.Is(ae, tc.err) {
					t.Errorf("mapped error does not wrap the sentinel (errors.Is failed)")
				}
			}
		})
	}
}

// TestMapControllerErr_ErrNotFoundExcluded pins the DELIBERATE exclusion of
// controller.ErrNotFound from the central table. It is context-specific — the same sentinel
// maps to six different codes across handlers (node_not_found, config_not_found,
// no_topology_stored, topology_version_not_found, no_staged_manifest, no_pinned_credential)
// and is a success/control-flow signal in several places — so the mapper MUST return nil for
// it and every handler keeps its own ErrNotFound branch. Collapsing it here would silently
// rewrite those responses.
func TestMapControllerErr_ErrNotFoundExcluded(t *testing.T) {
	if ae := mapControllerErr(controller.ErrNotFound); ae != nil {
		t.Fatalf("mapControllerErr(ErrNotFound) = %s, want nil (context-specific, handled per-handler)", ae.Code())
	}
	if ae := mapControllerErr(fmt.Errorf("wrap: %w", controller.ErrNotFound)); ae != nil {
		t.Fatalf("mapControllerErr(wrapped ErrNotFound) = %s, want nil", ae.Code())
	}
}

// TestMapControllerErr_UnknownAndNil: a nil error and an unrecognized error both return nil so
// the caller falls through to its own fallback bucket (codedErr), unchanged.
func TestMapControllerErr_UnknownAndNil(t *testing.T) {
	if ae := mapControllerErr(nil); ae != nil {
		t.Errorf("mapControllerErr(nil) = %s, want nil", ae.Code())
	}
	if ae := mapControllerErr(errors.New("some unrelated failure")); ae != nil {
		t.Errorf("mapControllerErr(unknown) = %s, want nil", ae.Code())
	}
}

// TestCodedErr_RelaySemantics pins codedErr (the return-value twin of writeCodedOr): a
// source-coded error surfaces at its OWN code+status; an un-coded error is bucketed under the
// fallback. It must NOT consult mapControllerErr (a relay keeps its exact per-handler bucket).
func TestCodedErr_RelaySemantics(t *testing.T) {
	// A wrapped source code wins over the fallback bucket.
	src := apierr.New(apierr.CodeKeygenMissingPubkey).With("node", "edge-1")
	ae := codedErr(apierr.CodeStageFailed, fmt.Errorf("stage: %w", src))
	if ae.Code() != apierr.CodeKeygenMissingPubkey || ae.Status() != http.StatusBadRequest {
		t.Errorf("wrapped source: code=%q status=%d, want keygen_missing_pubkey/400", ae.Code(), ae.Status())
	}
	// An un-coded error falls back to the bucket.
	ae = codedErr(apierr.CodeStageFailed, errors.New("disk gone"))
	if ae.Code() != apierr.CodeStageFailed || ae.Status() != http.StatusUnprocessableEntity {
		t.Errorf("un-coded: code=%q status=%d, want stage_failed/422", ae.Code(), ae.Status())
	}
	// A bare controller sentinel is NOT sentinel-mapped by codedErr — it buckets under the
	// fallback (sentinel mapping is an explicit mapControllerErr step, not a relay side effect).
	ae = codedErr(apierr.CodeInternalStorage, controller.ErrDuplicateWGKey)
	if ae.Code() != apierr.CodeInternalStorage {
		t.Errorf("codedErr sentinel-mapped a relay (code=%q); it must keep the fallback bucket", ae.Code())
	}
}
