package api

// static.go serves the built frontend (the panel SPA) so a single binary/container can
// serve the panel AND the API on the operator/panel port. It is OPT-IN: cmd/server
// mounts it only when YAOG_WEB_DIR is set (e.g. the Docker image). When unset, the
// server behaves exactly as before (API only; the panel is served by Vite in dev or a
// reverse proxy in the release bundle).

import (
	"net/http"
	"path/filepath"
	"strings"
)

// spaHandler serves a single-page app from dir: a real file under dir is served with
// the correct content type (via http.FileServer, which is path-traversal safe through
// http.Dir), and ANY other non-/api path falls back to index.html so client-side
// routes (e.g. /deploy) resolve. /api paths never receive index.html (defensive — the
// /api/* mux patterns are more specific and already take precedence).
func spaHandler(dir string) http.HandlerFunc {
	fileServer := http.FileServer(http.Dir(dir))
	index := filepath.Join(dir, "index.html")
	return func(w http.ResponseWriter, r *http.Request) {
		// Never serve the SPA for an API path. Cover both the bare "/api/" surface and
		// the controller namespace under an optional secret path prefix — the Contains
		// check is prefix-agnostic, so it covers the operator prefix
		// (YAOG_OPERATOR_PATH_PREFIX) on this mux and would equally cover the agent
		// prefix (YAOG_AGENT_PATH_PREFIX) if these paths ever hit this handler. An
		// unregistered/typo'd API path 404s instead of falling through to index.html
		// (which would mask routing mistakes).
		if strings.HasPrefix(r.URL.Path, "/api/") || strings.Contains(r.URL.Path, "/api/v1/controller/") {
			http.NotFound(w, r)
			return
		}
		// Serve a real file if one exists at the request path (http.Dir.Open rejects
		// "..", so this is traversal-safe); otherwise fall back to the SPA index.
		if f, err := http.Dir(dir).Open(r.URL.Path); err == nil {
			st, statErr := f.Stat()
			_ = f.Close()
			if statErr == nil && !st.IsDir() {
				fileServer.ServeHTTP(w, r)
				return
			}
		}
		http.ServeFile(w, r, index)
	}
}

// EnableStatic mounts the built panel SPA from dir at "/" on the operator/panel mux, so
// one process serves the panel + API. The /api/* routes are registered with more
// specific patterns and take precedence, so the API is never shadowed. Call it once,
// before ListenAndServe; cmd/server gates it on YAOG_WEB_DIR.
func (s *Server) EnableStatic(dir string) {
	s.mux.HandleFunc("/", s.recoverPanics(spaHandler(dir)))
}
