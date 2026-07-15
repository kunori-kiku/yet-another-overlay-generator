package api

// handler_controller.go holds the small definitions shared by the networked
// controller HTTP surface. Route registration lives in routes_controller.go; DTOs
// live in wire_controller.go; behavior is split across the sibling handler_*.go
// files. The two audience namespaces are served by separate plain-HTTP muxes:
// operator/panel routes under /api/v1/operator/ and agent/node routes under
// /api/v1/agent/ (each optionally prefixed independently).
//
// Protected agent routes derive their tenant/node from a per-node bearer through
// requireNode. Protected operator routes derive a named session identity, or the
// configured break-glass actor, through operatorAuth. Pre-auth exceptions are
// explicit in route registration: agent enrollment/bootstrap and operator
// password/passkey login. Production delegates transport confidentiality to a
// TLS-terminating reverse proxy.

import (
	"time"

	"github.com/kunorikiku/yet-another-overlay-generator/internal/apierr"
)

// DefaultOperatorName is the fallback display/audit identity for the optional
// break-glass operator credential when no explicit actor name is configured. Named
// login sessions carry their account username instead; auth kind remains separately
// pinned in context even if that username equals this fallback.
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
