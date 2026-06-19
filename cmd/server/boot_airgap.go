//go:build airgap

package main

import (
	"github.com/kunorikiku/yet-another-overlay-generator/internal/api"
)

// boot_airgap.go — the -tags airgap build's air-gap boot disposition.
//
// plan-7 / 1.7 (LOCKED build-tag mechanism, NOT a delete): under -tags airgap the binary RETAINS
// the four anonymous air-gap compute routes (registered by registerExtraRoutes in
// internal/api/airgap_routes.go) and boots the air-gap server when the controller env is unset.
// This is the local-design oracle and the --mode airgap boot target for plan-13's E2E and
// plan-21's -tags airgap DAST.

// serveAirgap boots the air-gap server: it serves s.mux (the four /api/{validate,compile,export,
// deploy-script} compute routes + /api/health, plus the panel SPA when YAOG_WEB_DIR is set) on
// addr as plain HTTP, exactly as before the build-tag split. It returns the serve error.
func serveAirgap(server *api.Server, addr string) error {
	return server.ListenAndServe(addr)
}
