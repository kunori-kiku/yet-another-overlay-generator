//go:build airgap

package api

import "net/http"

// airgap_routes.go — the -tags airgap build's air-gap route registration + operator-auth gate.
//
// plan-7 / 1.7 (LOCKED build-tag mechanism, NOT a delete): under -tags airgap the four anonymous
// compute routes are RETAINED as the local-design oracle and the boot target for plan-13's
// --mode airgap E2E and plan-21's -tags airgap DAST. This file holds the //go:build airgap
// overrides of the two hooks the un-tagged server core calls (registerExtraRoutes / armAirgapAuth,
// no-ops in airgap_stubs.go), plus gateAirgap. The DEFAULT (controller-only) build links none of
// this, so no unauthenticated path reaches the keygen/allocator/compiler pipeline in the shipped
// controller.

// registerExtraRoutes registers the four air-gap compute routes on s.mux under -tags airgap. Each
// is wrapped by compute (panic recovery -> CORS -> operator-auth gate -> handler) so a 401/403
// from the gate still carries CORS headers (the panel can read it). /api/health is registered
// ungated in registerRoutes and is NOT re-registered here — it stays a public liveness probe in
// both builds.
func (s *Server) registerExtraRoutes() {
	// compute wraps an air-gap compute route with the controller-mode operator-auth gate
	// (gateAirgap) INSIDE the panic/cors chain, so a 401/403 from the gate still gets CORS
	// headers. In a pure air-gap deployment (no EnableController) gateAirgap is a passthrough.
	compute := func(h http.HandlerFunc) http.HandlerFunc {
		return s.recoverPanics(s.cors(s.gateAirgap(h)))
	}

	s.mux.HandleFunc("/api/validate", compute(s.handler.HandleValidate))
	s.mux.HandleFunc("/api/compile", compute(s.handler.HandleCompile))
	s.mux.HandleFunc("/api/export", compute(s.handler.HandleExport))
	s.mux.HandleFunc("/api/deploy-script", compute(s.handler.HandleDeployScript))
}

// armAirgapAuth stores the controller's operator-auth middleware so gateAirgap can require
// operator auth on the air-gap compute routes IN CONTROLLER MODE (plan-12 / T6): they must not be
// an unauthenticated compute/key-gen oracle on the operator port. gateAirgap reads s.operatorAuth
// at request time because EnableController runs after registerRoutes.
func (s *Server) armAirgapAuth(ch *ControllerHandler) {
	s.operatorAuth = ch.operatorAuth
}

// gateAirgap wraps an air-gap compute handler so it requires operator auth IN CONTROLLER MODE
// (s.operatorAuth armed by armAirgapAuth) and is a passthrough in pure air-gap mode (s.operatorAuth
// nil), exactly as before. Read at request time because EnableController runs after registerRoutes.
// /api/health is intentionally NOT wrapped (it stays a public liveness probe in both modes).
func (s *Server) gateAirgap(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if s.operatorAuth != nil {
			s.operatorAuth(h)(w, r)
			return
		}
		h(w, r)
	}
}
