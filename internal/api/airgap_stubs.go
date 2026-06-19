//go:build !airgap

package api

// airgap_stubs.go — the DEFAULT (controller-only) build's no-op hooks for the air-gap surface.
//
// plan-7 / 1.7: the four anonymous air-gap compute routes (/api/validate, /api/compile,
// /api/export, /api/deploy-script), their handlers, the three ZIP helpers, gateAirgap, and the
// Server.operatorAuth read/arming all live behind //go:build airgap (airgap_routes.go +
// handler_airgap.go). The un-tagged server core calls these surfaces ONLY through two hooks —
// registerRoutes() calls registerExtraRoutes(), and EnableController() calls armAirgapAuth() —
// so the default build links no anonymous compute path at all. Here those hooks are no-ops; the
// //go:build airgap overrides in airgap_routes.go do the real work. This is the LOCKED build-tag
// mechanism, not a delete: -tags airgap RETAINS the full surface as the local-design oracle and
// the boot target for plan-13's --mode airgap E2E and plan-21's -tags airgap DAST.

// registerExtraRoutes registers no additional routes in the default build. The controller-only
// surface is /api/health (registered in registerRoutes) plus, when EnableController is called,
// the operator/agent controller routes (registered by ControllerHandler on the two muxes).
func (s *Server) registerExtraRoutes() {}

// armAirgapAuth is a no-op in the default build: there are no air-gap compute routes to gate, so
// Server.operatorAuth is never read or written here. The DISTINCT controller operator-route auth
// (ControllerHandler.operatorAuth) is wired by EnableController in both builds and is unaffected.
func (s *Server) armAirgapAuth(_ *ControllerHandler) {}
