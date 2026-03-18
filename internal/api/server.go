package api

import (
	"fmt"
	"net/http"
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
	// CORS 
	s.mux.HandleFunc("/api/health", s.cors(s.handler.HandleHealth))
	s.mux.HandleFunc("/api/validate", s.cors(s.handler.HandleValidate))
	s.mux.HandleFunc("/api/compile", s.cors(s.handler.HandleCompile))
	s.mux.HandleFunc("/api/export", s.cors(s.handler.HandleExport))
	s.mux.HandleFunc("/api/deploy-script", s.cors(s.handler.HandleDeployScript))
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

// Handler  HTTP Handler 
func (s *Server) Handler() http.Handler {
	return s.mux
}

// ListenAndServe  HTTP 
func (s *Server) ListenAndServe(addr string) error {
	fmt.Printf("API : http://%s\n", addr)
	fmt.Println(":")
	fmt.Println("  GET  /api/health   - ")
	fmt.Println("  POST /api/validate - ")
	fmt.Println("  POST /api/compile  - ")
	fmt.Println("  POST /api/export   - ")
	fmt.Println("  POST /api/deploy-script - 下载部署脚本")
	return http.ListenAndServe(addr, s.mux)
}
