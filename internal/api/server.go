package api

import (
	"context"
	"fmt"
	"log"
	"net"
	"net/http"
	"runtime/debug"
	"sync"
	"time"

	"github.com/kunorikiku/yet-another-overlay-generator/internal/apierr"
)

// Server is the HTTP API server. It owns the operator/panel mux and the optional
// agent mux, the controller-mode auth middleware, and the live *http.Server handles
// for graceful shutdown.
type Server struct {
	handler *Handler
	mux     *http.ServeMux

	// agentMux serves the agent-facing controller routes on a SEPARATE port from the
	// operator/panel mux. It is nil in air-gap mode (the default); only
	// EnableController populates it. Splitting the agent and operator surfaces onto
	// two muxes/ports lets a deployment expose the agent port to the fleet while
	// keeping the operator port behind a tighter network boundary. Both are plain
	// HTTP — TLS is delegated to a reverse proxy (plan-4.5).
	agentMux *http.ServeMux

	// operatorAuth is the operator-auth middleware, set by EnableController in controller
	// mode and nil in air-gap mode (plan-12 / T6). The air-gap compute routes (validate/
	// compile/export/deploy-script) read it AT REQUEST TIME via gateAirgap: in a controller
	// deployment they are then gated behind operator auth (closing the unauthenticated
	// compute / key-gen oracle on the operator port); in air-gap mode they stay open exactly
	// as before. Stored as a field (not wired at registerRoutes time) because EnableController
	// runs AFTER registerRoutes.
	operatorAuth func(http.HandlerFunc) http.HandlerFunc

	// srvMu guards the live *http.Server handles, which ListenAndServe[Agent] publish
	// once they start so Shutdown can drain them.
	srvMu    sync.Mutex
	httpSrv  *http.Server
	agentSrv *http.Server
	// baseCtx is the parent context handed to every in-flight request (via the servers'
	// BaseContext). Shutdown cancels it FIRST so long-poll handlers — which select on
	// their request context and answer 204 on cancellation — return immediately instead
	// of pinning the drain open for their full deadline. A polling fleet therefore does
	// not make every restart wait the whole grace window.
	baseCtx    context.Context
	baseCancel context.CancelFunc
}

// NewServer constructs a Server with both muxes initialized and the air-gap routes
// registered. The agent mux serves nothing until EnableController is called.
func NewServer() *Server {
	baseCtx, baseCancel := context.WithCancel(context.Background())
	s := &Server{
		handler:    NewHandler(),
		mux:        http.NewServeMux(),
		agentMux:   http.NewServeMux(),
		baseCtx:    baseCtx,
		baseCancel: baseCancel,
	}
	s.registerRoutes()
	return s
}

// EnableController registers the networked controller routes across this server's
// two muxes: the operator routes go on s.mux (the operator/panel port) and the
// agent routes go on s.agentMux (the agent port). It is the single opt-in seam for
// controller mode: when it is NOT called, the air-gap routes on s.mux are exactly as
// before and s.agentMux serves nothing. cmd/server calls this only under the
// controller env gate.
//
// Both ports are served as PLAIN HTTP (plan-4.5); confidentiality is delegated to a
// reverse proxy's TLS. Authentication is per-node bearer tokens (agent) and a shared
// operator token (operator), enforced by the auth chokepoint in auth_controller.go.
// The controller routes live under /api/v1/operator/ (operator mux) and
// /api/v1/agent/ (agent mux) and never collide with the air-gap /api/ routes on s.mux.
func (s *Server) EnableController(ch *ControllerHandler) {
	ch.RegisterOperatorRoutes(s.mux)
	ch.RegisterAgentRoutes(s.agentMux)
	// plan-12 / T6: in a controller deployment the air-gap compute routes on s.mux must not be
	// an unauthenticated compute/key-gen oracle on the operator port. Arm the operator-auth gate;
	// gateAirgap (wrapping those routes since registerRoutes) reads it at request time.
	s.operatorAuth = ch.operatorAuth
	// plan-8 Phase 5.2 (TOFU-MITM DOCUMENT): surface a single startup warning when the controller
	// is in the dev-only no-anchor posture (keystone OFF + no TLS hint), where a network MITM can
	// substitute the fetched bootstrap/config. Advisory only — refusing the posture in code is
	// deferred bootstrap-TOFU work (rc.2/GA); the rc.1 production requirement lives in the release
	// notes + docs/spec/security.
	ch.WarnInsecureControllerPosture(context.Background())
}

// gateAirgap wraps an air-gap compute handler so it requires operator auth IN CONTROLLER MODE
// (s.operatorAuth armed by EnableController) and is a passthrough in air-gap mode (s.operatorAuth
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

func (s *Server) registerRoutes() {
	// Middleware chain (outermost runs first): panic recovery -> CORS -> business handler.
	// recoverPanics wraps the outermost layer so a panic during either the CORS stage or the
	// business handler is caught and converted into a 500 JSON response instead of tearing the
	// connection down (D60).
	wrap := func(h http.HandlerFunc) http.HandlerFunc {
		return s.recoverPanics(s.cors(h))
	}

	// compute wraps an air-gap compute route with the controller-mode operator-auth gate
	// (gateAirgap) INSIDE the panic/cors chain, so a 401/403 from the gate still gets CORS
	// headers (the panel can read it). Health is exempt — public liveness probe in both modes.
	compute := func(h http.HandlerFunc) http.HandlerFunc {
		return s.recoverPanics(s.cors(s.gateAirgap(h)))
	}

	s.mux.HandleFunc("/api/health", wrap(s.handler.HandleHealth))
	s.mux.HandleFunc("/api/validate", compute(s.handler.HandleValidate))
	s.mux.HandleFunc("/api/compile", compute(s.handler.HandleCompile))
	s.mux.HandleFunc("/api/export", compute(s.handler.HandleExport))
	s.mux.HandleFunc("/api/deploy-script", compute(s.handler.HandleDeployScript))
}

// cors wraps next with permissive CORS headers for the air-gap compute routes and
// short-circuits preflight OPTIONS requests with 204 No Content.
func (s *Server) cors(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")

		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}

		next(w, r)
	}
}

// headerTrackingResponseWriter wraps an http.ResponseWriter and records whether response
// headers have already been written. Panic recovery uses this to avoid calling WriteHeader
// again after the business handler has already written headers (which would trigger a
// "superfluous WriteHeader call" and corrupt the existing response).
type headerTrackingResponseWriter struct {
	http.ResponseWriter
	wroteHeader bool
}

func (w *headerTrackingResponseWriter) WriteHeader(status int) {
	w.wroteHeader = true
	w.ResponseWriter.WriteHeader(status)
}

func (w *headerTrackingResponseWriter) Write(b []byte) (int, error) {
	// An implicit 200 on first Write also counts as headers written, matching the standard
	// library ResponseWriter behavior.
	w.wroteHeader = true
	return w.ResponseWriter.Write(b)
}

// recoverPanics catches a panic in the wrapped handler, logs the stack trace, and — if no
// response headers have been written yet — returns a 500 JSON error body ({"error": ...}).
// This way a panic raised by deep code such as the allocator (e.g. an IPv6 CIDR reaching the
// IPv4-only allocator) becomes a clean 5xx instead of a torn connection (D60).
func (s *Server) recoverPanics(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		tracked := &headerTrackingResponseWriter{ResponseWriter: w}

		defer func() {
			if rec := recover(); rec != nil {
				log.Printf("panic recovered in %s %s: %v\n%s", r.Method, r.URL.Path, rec, debug.Stack())

				// Only write 500 if no response headers have been written yet, to avoid a
				// duplicate WriteHeader call.
				if !tracked.wroteHeader {
					writeAPIError(tracked, apierr.New(apierr.CodeInternalPanic))
				}
			}
		}()

		next(tracked, r)
	}
}

// recovered wraps a mux with TOP-LEVEL panic recovery so EVERY route on it — the
// operator and agent controller routes included, not just the air-gap compute routes —
// converts a handler panic into a coded 500 instead of a torn connection (B1). The
// air-gap routes additionally wrap recoverPanics per-route; the inner recover fires
// first, leaving this outer wrap as a harmless backstop for them.
func (s *Server) recovered(mux *http.ServeMux) http.Handler {
	return http.HandlerFunc(s.recoverPanics(mux.ServeHTTP))
}

// Handler returns the operator/panel mux (air-gap routes + operator controller routes),
// wrapped in top-level panic recovery. Exposed for tests that drive it via httptest.
func (s *Server) Handler() http.Handler {
	return s.recovered(s.mux)
}

// AgentHandler returns the agent mux (agent controller routes), wrapped in top-level
// panic recovery. It serves nothing until EnableController is called. Exposed for tests
// that drive it via httptest.
func (s *Server) AgentHandler() http.Handler {
	return s.recovered(s.agentMux)
}

// ListenAndServe starts the HTTP service. It uses an *http.Server configured with
// read/write/idle timeouts rather than the bare http.ListenAndServe, to defend against
// Slowloris / slow-request-body class DoS (D33).
//
// This serves s.mux: the air-gap routes plus, when controller mode is on, the
// operator/panel controller routes. It is plain HTTP.
func (s *Server) ListenAndServe(addr string) error {
	fmt.Printf("API server listening on: http://%s\n", addr)
	fmt.Println("available endpoints:")
	fmt.Println("  GET  /api/health   - health check")
	fmt.Println("  POST /api/validate - validate topology")
	fmt.Println("  POST /api/compile  - compile topology")
	fmt.Println("  POST /api/export   - export artifacts ZIP")
	fmt.Println("  POST /api/deploy-script - download deploy script")

	srv := &http.Server{
		Addr:              addr,
		Handler:           s.recovered(s.mux),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      120 * time.Second,
		IdleTimeout:       120 * time.Second,
		MaxHeaderBytes:    1 << 20,
		BaseContext:       func(net.Listener) context.Context { return s.baseCtx },
	}
	s.srvMu.Lock()
	s.httpSrv = srv
	s.srvMu.Unlock()
	return srv.ListenAndServe()
}

// ListenAndServeAgent serves the agent-facing controller routes (s.agentMux) on a
// separate port as PLAIN HTTP. It uses the same Slowloris timeouts as the air-gap
// path (D33) but a longer WriteTimeout to accommodate the /poll long-poll (~55s)
// without the server tearing the connection down mid-wait (90s leaves margin). TLS
// is delegated to a reverse proxy (plan-4.5).
func (s *Server) ListenAndServeAgent(addr string) error {
	fmt.Printf("Controller agent service (HTTP): http://%s\n", addr)
	fmt.Println("Agent endpoints (under /api/v1/agent/):")
	fmt.Println("  POST /enroll          - node enrollment (no auth)")
	fmt.Println("  GET  /config          - fetch current bundle (bearer)")
	fmt.Println("  GET  /poll?after=N     - long-poll for a new generation (bearer)")
	fmt.Println("  POST /report          - report applied generation (bearer)")

	srv := &http.Server{
		Addr:              addr,
		Handler:           s.recovered(s.agentMux),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		// WriteTimeout must exceed the /poll long-poll deadline (~55s) so a waiting
		// poll is answered rather than killed by the write deadline; 90s leaves margin.
		WriteTimeout:   90 * time.Second,
		IdleTimeout:    120 * time.Second,
		MaxHeaderBytes: 1 << 20,
		BaseContext:    func(net.Listener) context.Context { return s.baseCtx },
	}
	s.srvMu.Lock()
	s.agentSrv = srv
	s.srvMu.Unlock()
	return srv.ListenAndServe()
}

// Shutdown gracefully drains both listeners (operator/panel + agent): it first cancels
// the shared base context so in-flight long-polls return immediately, then waits for the
// remaining in-flight requests on each server to finish, bounded by ctx's deadline. A
// listener that never started (nil handle, e.g. air-gap mode where only ListenAndServe
// ran) is skipped. After Shutdown, the corresponding ListenAndServe[Agent] returns
// http.ErrServerClosed, which the caller treats as a clean stop. It returns the first
// non-nil per-server shutdown error (typically context.DeadlineExceeded if the grace
// window elapsed with connections still active).
func (s *Server) Shutdown(ctx context.Context) error {
	s.srvMu.Lock()
	httpSrv, agentSrv := s.httpSrv, s.agentSrv
	s.srvMu.Unlock()

	// Cancel in-flight request contexts before draining so long-polls unblock now.
	if s.baseCancel != nil {
		s.baseCancel()
	}

	var firstErr error
	if httpSrv != nil {
		if err := httpSrv.Shutdown(ctx); err != nil {
			firstErr = err
		}
	}
	if agentSrv != nil {
		if err := agentSrv.Shutdown(ctx); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}
