package api

// handler_controller.go is the HTTP surface of the networked controller
// (plan-4.5). It exposes the controller core (Store + enrollment + compile) under
// two audience-named namespaces — operator/panel routes under /api/v1/operator/ and
// agent/node routes under /api/v1/agent/ — with JSON request/response bodies. Authentication and the
// tenant/node identity are handled entirely by the auth chokepoint in
// auth_controller.go: every handler here reads the caller's node from the request
// context (nodeFromCtx) rather than from the request, so a node can only ever act
// as itself. The single exception is /enroll, which is registered WITHOUT the auth
// middleware (it must be reachable before the node has any API token) and is
// instead gated by the single-use enrollment token.
//
// The routes are split across two muxes (served on two plain-HTTP ports):
//   - agent routes (/enroll,/config,/poll,/report,/rekey) → RegisterAgentRoutes.
//   - operator routes (everything else, incl. /rekey-all) → RegisterOperatorRoutes.
//
// Transport is plain HTTP; TLS is delegated to a reverse proxy (plan-4.5). Bearer
// tokens authenticate both kinds of caller (per-node tokens for agents, a single
// operator token for the operator).

import (
	"time"

	"github.com/kunorikiku/yet-another-overlay-generator/internal/apierr"
)

// DefaultOperatorName is the operator's identity stamped into the request context
// (and audit actor) for operator routes. Under single-tenant v1 the operator is
// authenticated by a single shared operator token (YAOG_CONTROLLER_OPERATOR_TOKEN);
// Plan 5 (OIDC/RBAC) replaces this with a real per-operator principal model.
const DefaultOperatorName = "operator"

// defaultPollDeadline bounds a single /poll long-poll on the server side. The
// handler returns 204 when the generation has not advanced within this window so
// the agent re-polls on a fresh connection (and the request does not pin a server
// goroutine indefinitely). It is below typical 60s proxy idle timeouts. A
// ControllerHandler may override it (pollDeadline) — the integration test sets a
// tiny value to exercise the timeout-204 path without waiting ~55s.
const defaultPollDeadline = 55 * time.Second

// controllerMaxBodyBytes caps controller request bodies. Controller payloads
// (enroll request, report, topology JSON) are small; the same MaxBytesReader
// discipline guards against unbounded io.ReadAll (D34). Topology can be larger
// than a report, so this reuses the shared maxRequestBodyBytes topology cap.
const controllerMaxBodyBytes = maxRequestBodyBytes

// errBodyEmpty marks an empty request body where one is required. It is a coded
// *apierr.Error (CodeReqBodyEmpty, 400) so writeCodedOr surfaces it via errors.As with the
// right status; built once at init and only ever read after (never mutated), so sharing is safe.
var errBodyEmpty = apierr.New(apierr.CodeReqBodyEmpty)
