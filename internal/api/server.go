package api

import (
	"fmt"
	"log"
	"net/http"
	"runtime/debug"
	"time"

	"github.com/kunorikiku/yet-another-overlay-generator/internal/apierr"
)

// Server HTTP API
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
}

// NewServer  API
func NewServer() *Server {
	s := &Server{
		handler:  NewHandler(),
		mux:      http.NewServeMux(),
		agentMux: http.NewServeMux(),
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
	// 中间件链（外层先执行）：panic 恢复 -> CORS -> 业务处理。
	// recoverPanics 包在最外层，确保 CORS 阶段或业务处理中的 panic 都会被捕获
	// 并转换为 500 JSON 响应，而不是中断连接（D60）。
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

// cors CORS
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

// headerTrackingResponseWriter 包装 http.ResponseWriter，记录是否已经写出过响应头。
// panic 恢复时据此避免在业务处理已写出响应头之后再次 WriteHeader（会触发
// "superfluous WriteHeader call" 并破坏已有响应）。
type headerTrackingResponseWriter struct {
	http.ResponseWriter
	wroteHeader bool
}

func (w *headerTrackingResponseWriter) WriteHeader(status int) {
	w.wroteHeader = true
	w.ResponseWriter.WriteHeader(status)
}

func (w *headerTrackingResponseWriter) Write(b []byte) (int, error) {
	// 隐式写出 200 时也视为已写头，与标准库 ResponseWriter 行为一致。
	w.wroteHeader = true
	return w.ResponseWriter.Write(b)
}

// recoverPanics 捕获被包裹处理器中的 panic，记录堆栈，并在尚未写出响应头时
// 返回 500 JSON 错误体（{"error": ...}）。这样分配器等深层代码触发的 panic
// （例如 IPv6 CIDR 进入仅支持 IPv4 的分配器）会变成干净的 5xx，而不是被中断的连接（D60）。
func (s *Server) recoverPanics(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		tracked := &headerTrackingResponseWriter{ResponseWriter: w}

		defer func() {
			if rec := recover(); rec != nil {
				log.Printf("panic recovered in %s %s: %v\n%s", r.Method, r.URL.Path, rec, debug.Stack())

				// 仅当尚未写出任何响应头时才写 500，避免重复 WriteHeader。
				if !tracked.wroteHeader {
					writeAPIError(tracked, apierr.New(apierr.CodeInternalPanic))
				}
			}
		}()

		next(tracked, r)
	}
}

// Handler returns the operator/panel mux (air-gap routes + operator controller
// routes). Exposed for tests that drive it via httptest.
func (s *Server) Handler() http.Handler {
	return s.mux
}

// AgentHandler returns the agent mux (agent controller routes). It serves nothing
// until EnableController is called. Exposed for tests that drive it via httptest.
func (s *Server) AgentHandler() http.Handler {
	return s.agentMux
}

// ListenAndServe 启动 HTTP 服务。
// 使用配置了读/写/空闲超时的 *http.Server，而非裸 http.ListenAndServe，
// 以抵御 Slowloris / 慢速请求体类 DoS（D33）。
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
		Handler:           s.mux,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      120 * time.Second,
		IdleTimeout:       120 * time.Second,
		MaxHeaderBytes:    1 << 20,
	}
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
		Handler:           s.agentMux,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		// WriteTimeout must exceed the /poll long-poll deadline (~55s) so a waiting
		// poll is answered rather than killed by the write deadline; 90s leaves margin.
		WriteTimeout:   90 * time.Second,
		IdleTimeout:    120 * time.Second,
		MaxHeaderBytes: 1 << 20,
	}
	return srv.ListenAndServe()
}
