package api

import (
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestSpaHandler: real files are served verbatim; root and unknown (non-/api) routes
// fall back to index.html; /api paths are never served the SPA.
func TestSpaHandler(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "index.html"), []byte("<html>INDEX</html>"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "assets"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "assets", "app.js"), []byte("APPJS"), 0644); err != nil {
		t.Fatal(err)
	}
	h := spaHandler(dir)

	get := func(path string) (int, string) {
		t.Helper()
		r := httptest.NewRequest("GET", path, nil)
		w := httptest.NewRecorder()
		h(w, r)
		return w.Code, w.Body.String()
	}

	if code, body := get("/assets/app.js"); code != 200 || body != "APPJS" {
		t.Errorf("real asset: code=%d body=%q, want 200 APPJS", code, body)
	}
	if code, body := get("/"); code != 200 || !strings.Contains(body, "INDEX") {
		t.Errorf("root -> index: code=%d body=%q", code, body)
	}
	if code, body := get("/deploy"); code != 200 || !strings.Contains(body, "INDEX") {
		t.Errorf("SPA route -> index fallback: code=%d body=%q", code, body)
	}
	if code, _ := get("/api/anything"); code != 404 {
		t.Errorf("/api path: code=%d, want 404 (never the SPA)", code)
	}
	if code, _ := get("/s3cr3t/api/v1/operator/missing"); code != 404 {
		t.Errorf("prefixed controller path: code=%d, want 404 (never the SPA)", code)
	}
}
