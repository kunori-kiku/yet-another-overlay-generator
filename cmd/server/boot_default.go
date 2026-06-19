//go:build !airgap

package main

import (
	"errors"

	"github.com/kunorikiku/yet-another-overlay-generator/internal/api"
)

// boot_default.go — the DEFAULT (controller-only) build's air-gap boot disposition.
//
// plan-7 / 1.7 (LOCKED build-tag mechanism, NOT a delete): the default binary is a CONTROLLER.
// The four anonymous air-gap compute routes (/api/validate, /api/compile, /api/export,
// /api/deploy-script) and their handlers live behind //go:build airgap, so they are not linked
// here. When the controller env is unset (state dir and/or tenant id), there is no controller to
// serve and no air-gap compute surface to fall back to — so we FAIL LOUD (mirroring the loud-fail
// pattern in serveController) rather than stand up a do-nothing panel/health-only listener that
// would mask the misconfiguration. The error names the fix.

// serveAirgap fails loud in the default build: the controller env (YAOG_CONTROLLER_STATE_DIR +
// YAOG_TENANT_ID) is required to run the default binary, which is controller-only. For offline
// topology compilation use one of the air-gap paths instead: the -tags airgap local-design server
// build, the standalone static-local-design site (in-browser TS compiler, no backend), or the
// cmd/compiler CLI. The *api.Server argument is accepted to match the -tags airgap signature; it
// is unused here because nothing is served.
func serveAirgap(_ *api.Server, _ string) error {
	return errors.New("controller env not configured: set " + envControllerStateDir + " and " +
		envTenantID + " to run the controller. This is the controller-only build; it links no " +
		"air-gap compute routes. For offline topology compilation use the -tags airgap " +
		"local-design server build, the standalone static-local-design site (in-browser " +
		"compiler, no backend), or the cmd/compiler CLI")
}
