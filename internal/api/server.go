package api

import (
	"fmt"
	"log"
	"net/http"
	"runtime/debug"
	"time"
)

// Server HTTP API 
type Server struct {
	handler *Handler
	mux     *http.ServeMux
}

// NewServer  API 
func NewServer() *Server {
	s := &Server{
		handler: NewHandler(),
		mux:     http.NewServeMux(),
	}
	s.registerRoutes()
	return s
}

func (s *Server) registerRoutes() {
	// 中间件链（外层先执行）：panic 恢复 -> CORS -> 业务处理。
	// recoverPanics 包在最外层，确保 CORS 阶段或业务处理中的 panic 都会被捕获
	// 并转换为 500 JSON 响应，而不是中断连接（D60）。
	wrap := func(h http.HandlerFunc) http.HandlerFunc {
		return s.recoverPanics(s.cors(h))
	}

	s.mux.HandleFunc("/api/health", wrap(s.handler.HandleHealth))
	s.mux.HandleFunc("/api/validate", wrap(s.handler.HandleValidate))
	s.mux.HandleFunc("/api/compile", wrap(s.handler.HandleCompile))
	s.mux.HandleFunc("/api/export", wrap(s.handler.HandleExport))
	s.mux.HandleFunc("/api/deploy-script", wrap(s.handler.HandleDeployScript))
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
					writeError(tracked, http.StatusInternalServerError, "服务器内部错误")
				}
			}
		}()

		next(tracked, r)
	}
}

// Handler  HTTP Handler
func (s *Server) Handler() http.Handler {
	return s.mux
}

// ListenAndServe 启动 HTTP 服务。
// 使用配置了读/写/空闲超时的 *http.Server，而非裸 http.ListenAndServe，
// 以抵御 Slowloris / 慢速请求体类 DoS（D33）。
func (s *Server) ListenAndServe(addr string) error {
	fmt.Printf("API 服务地址: http://%s\n", addr)
	fmt.Println("可用接口:")
	fmt.Println("  GET  /api/health   - 健康检查")
	fmt.Println("  POST /api/validate - 校验拓扑")
	fmt.Println("  POST /api/compile  - 编译拓扑")
	fmt.Println("  POST /api/export   - 导出产物 ZIP")
	fmt.Println("  POST /api/deploy-script - 下载部署脚本")

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
