package api

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHandleHealth(t *testing.T) {
	server := NewServer()

	req := httptest.NewRequest(http.MethodGet, "/api/health", nil)
	rec := httptest.NewRecorder()

	server.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf(" 200,  %d", rec.Code)
	}

	var resp HealthResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf(": %v", err)
	}

	if resp.Status != "ok" {
		t.Errorf(" status=ok,  %s", resp.Status)
	}
}

func TestHandleHealth_WrongMethod(t *testing.T) {
	server := NewServer()

	req := httptest.NewRequest(http.MethodPost, "/api/health", nil)
	rec := httptest.NewRecorder()

	server.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf(" 405,  %d", rec.Code)
	}
}

func TestHandleValidate_ValidTopology(t *testing.T) {
	server := NewServer()

	body := validTopologyJSON()
	req := httptest.NewRequest(http.MethodPost, "/api/validate", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	server.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf(" 200,  %d, body: %s", rec.Code, rec.Body.String())
	}

	var resp ValidateResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf(": %v", err)
	}

	if !resp.Valid {
		t.Errorf(", errors: %v", resp.Errors)
	}
}

func TestHandleValidate_InvalidTopology(t *testing.T) {
	server := NewServer()

	topo := map[string]interface{}{
		"project": map[string]interface{}{
			"id":   "", //  ID
			"name": "Test",
		},
		"domains": []interface{}{},
		"nodes":   []interface{}{},
		"edges":   []interface{}{},
	}
	body, _ := json.Marshal(topo)

	req := httptest.NewRequest(http.MethodPost, "/api/validate", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	server.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf(" 200,  %d", rec.Code)
	}

	var resp ValidateResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf(": %v", err)
	}

	if resp.Valid {
		t.Errorf("")
	}

	if len(resp.Errors) == 0 {
		t.Errorf("")
	}
}

func TestHandleValidate_EmptyBody(t *testing.T) {
	server := NewServer()

	req := httptest.NewRequest(http.MethodPost, "/api/validate", nil)
	rec := httptest.NewRecorder()

	server.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf(" 400,  %d", rec.Code)
	}
}

func TestHandleValidate_InvalidJSON(t *testing.T) {
	server := NewServer()

	req := httptest.NewRequest(http.MethodPost, "/api/validate", bytes.NewReader([]byte("not json")))
	rec := httptest.NewRecorder()

	server.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf(" 400,  %d", rec.Code)
	}
}

func TestHandleCompile_ValidTopology(t *testing.T) {
	server := NewServer()

	body := validTopologyJSON()
	req := httptest.NewRequest(http.MethodPost, "/api/compile", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	server.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf(" 200,  %d, body: %s", rec.Code, rec.Body.String())
	}

	var resp CompileResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf(": %v", err)
	}

	//  IP
	for _, node := range resp.Topology.Nodes {
		if node.OverlayIP == "" {
			t.Errorf(" %s  IP", node.Name)
		}
	}

	//  WireGuard
	if len(resp.WireGuardConfigs) == 0 {
		t.Errorf(" WireGuard ")
	}

	//  Babel
	if len(resp.BabelConfigs) == 0 {
		t.Errorf(" Babel ")
	}

	//
	if len(resp.InstallScripts) == 0 {
		t.Errorf("")
	}
}

func TestHandleCompile_InvalidTopology(t *testing.T) {
	server := NewServer()

	topo := map[string]interface{}{
		"project": map[string]interface{}{"id": "", "name": ""},
		"domains": []interface{}{},
		"nodes":   []interface{}{},
		"edges":   []interface{}{},
	}
	body, _ := json.Marshal(topo)

	req := httptest.NewRequest(http.MethodPost, "/api/compile", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	server.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusUnprocessableEntity {
		t.Errorf(" 422,  %d", rec.Code)
	}
}

func TestHandleExport_ReturnsZipWithNodeInstallers(t *testing.T) {
	server := NewServer()

	body := validTopologyJSON()
	req := httptest.NewRequest(http.MethodPost, "/api/export", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	server.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf(" 200,  %d, body: %s", rec.Code, rec.Body.String())
	}

	//  Content-Type
	ct := rec.Header().Get("Content-Type")
	if ct != "application/zip" {
		t.Errorf(" Content-Type=application/zip,  %s", ct)
	}

	//  Content-Disposition
	cd := rec.Header().Get("Content-Disposition")
	if cd == "" {
		t.Errorf(" Content-Disposition header")
	}

	zipReader, err := zip.NewReader(bytes.NewReader(rec.Body.Bytes()), int64(rec.Body.Len()))
	if err != nil {
		t.Fatalf(" failed to read zip stream: %v", err)
	}

	entries := map[string]*zip.File{}
	for _, f := range zipReader.File {
		entries[f.Name] = f
	}

	if _, ok := entries["node-alpha.tar.gz"]; ok {
		t.Fatalf("zip should not include node-alpha.tar.gz; only node-alpha.install.sh is expected")
	}

	alphaInstaller, ok := entries["node-alpha.install.sh"]
	if !ok {
		t.Fatalf("zip missing node-alpha.install.sh")
	}

	rc, err := alphaInstaller.Open()
	if err != nil {
		t.Fatalf("failed to open node-alpha.install.sh in zip: %v", err)
	}
	defer rc.Close()

	installerBytes, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("failed to read node-alpha.install.sh bytes: %v", err)
	}

	installerText := string(installerBytes)
	payloadMarker := "__PAYLOAD_BELOW__\n"
	idx := strings.Index(installerText, payloadMarker)
	if idx < 0 {
		t.Fatalf("node-alpha.install.sh missing payload marker")
	}

	payloadBase64 := strings.TrimSpace(installerText[idx+len(payloadMarker):])
	tarBytes, err := base64.StdEncoding.DecodeString(payloadBase64)
	if err != nil {
		t.Fatalf("failed to decode embedded payload from node-alpha.install.sh: %v", err)
	}

	gzReader, err := gzip.NewReader(bytes.NewReader(tarBytes))
	if err != nil {
		t.Fatalf("failed to read gzip stream in node-alpha.tar.gz: %v", err)
	}
	defer gzReader.Close()

	tarReader := tar.NewReader(gzReader)
	hasWgConf := false
	for {
		hdr, err := tarReader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("failed to read tar entry from node-alpha.tar.gz: %v", err)
		}
		if hdr.Name == "wireguard/wg0.conf" {
			hasWgConf = true
			break
		}
	}

	if !hasWgConf {
		t.Errorf("node-alpha.tar.gz missing wireguard/wg0.conf")
	}
}

func TestCORS_Preflight(t *testing.T) {
	server := NewServer()

	req := httptest.NewRequest(http.MethodOptions, "/api/compile", nil)
	rec := httptest.NewRecorder()

	server.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Errorf("CORS preflight  204,  %d", rec.Code)
	}

	if rec.Header().Get("Access-Control-Allow-Origin") != "*" {
		t.Errorf(" Access-Control-Allow-Origin: *")
	}
}

func TestCORS_Headers(t *testing.T) {
	server := NewServer()

	req := httptest.NewRequest(http.MethodGet, "/api/health", nil)
	rec := httptest.NewRecorder()

	server.Handler().ServeHTTP(rec, req)

	if rec.Header().Get("Access-Control-Allow-Origin") != "*" {
		t.Errorf(" CORS header")
	}
}

// ---  ---

func validTopologyJSON() []byte {
	topo := map[string]interface{}{
		"project": map[string]interface{}{
			"id":      "test-001",
			"name":    "Test Project",
			"version": "0.1.0",
		},
		"domains": []interface{}{
			map[string]interface{}{
				"id":              "domain-1",
				"name":            "test-network",
				"cidr":            "10.10.0.0/24",
				"allocation_mode": "auto",
				"routing_mode":    "babel",
			},
		},
		"nodes": []interface{}{
			map[string]interface{}{
				"id":          "node-1",
				"name":        "node-alpha",
				"hostname":    "alpha.example.com",
				"platform":    "debian",
				"role":        "router",
				"domain_id":   "domain-1",
				"listen_port": 51820,
				"capabilities": map[string]interface{}{
					"can_accept_inbound": true,
					"can_forward":        true,
					"can_relay":          false,
					"has_public_ip":      true,
				},
			},
			map[string]interface{}{
				"id":          "node-2",
				"name":        "node-beta",
				"hostname":    "beta.example.com",
				"platform":    "ubuntu",
				"role":        "router",
				"domain_id":   "domain-1",
				"listen_port": 51820,
				"capabilities": map[string]interface{}{
					"can_accept_inbound": true,
					"can_forward":        true,
					"can_relay":          false,
					"has_public_ip":      true,
				},
			},
		},
		"edges": []interface{}{
			map[string]interface{}{
				"id":            "edge-1",
				"from_node_id":  "node-1",
				"to_node_id":    "node-2",
				"type":          "direct",
				"endpoint_host": "203.0.113.2",
				"endpoint_port": 51820,
				"transport":     "udp",
				"is_enabled":    true,
			},
			map[string]interface{}{
				"id":            "edge-2",
				"from_node_id":  "node-2",
				"to_node_id":    "node-1",
				"type":          "direct",
				"endpoint_host": "203.0.113.1",
				"endpoint_port": 51820,
				"transport":     "udp",
				"is_enabled":    true,
			},
		},
	}

	body, _ := json.Marshal(topo)
	return body
}

// Keep io imported for compatibility checks.
var _ = io.EOF
