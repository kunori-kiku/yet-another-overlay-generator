package api

import (
	"fmt"
	"net/http"
)

// Server HTTP API 服务器
type Server struct {
	handler *Handler
	mux     *http.ServeMux
}

// NewServer 创建新的 API 服务器
func NewServer() *Server {
	s := &Server{
		handler: NewHandler(),
		mux:     http.NewServeMux(),
	}
	s.registerRoutes()
	return s
}

func (s *Server) registerRoutes() {
	// CORS 中间件包装
	s.mux.HandleFunc("/api/health", s.cors(s.handler.HandleHealth))
	s.mux.HandleFunc("/api/validate", s.cors(s.handler.HandleValidate))
	s.mux.HandleFunc("/api/compile", s.cors(s.handler.HandleCompile))
	s.mux.HandleFunc("/api/export", s.cors(s.handler.HandleExport))
}

// cors CORS 中间件
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

// Handler 返回 HTTP Handler 用于测试
func (s *Server) Handler() http.Handler {
	return s.mux
}

// ListenAndServe 启动 HTTP 服务
func (s *Server) ListenAndServe(addr string) error {
	fmt.Printf("API 服务启动: http://%s\n", addr)
	fmt.Println("端点:")
	fmt.Println("  GET  /api/health   - 健康检查")
	fmt.Println("  POST /api/validate - 校验拓扑")
	fmt.Println("  POST /api/compile  - 编译拓扑")
	fmt.Println("  POST /api/export   - 导出产物包")
	return http.ListenAndServe(addr, s.mux)
}
