package api

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHandleHealth(t *testing.T) {
	server := NewServer()

	req := httptest.NewRequest(http.MethodGet, "/api/health", nil)
	rec := httptest.NewRecorder()

	server.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("期望状态码 200, 得到 %d", rec.Code)
	}

	var resp HealthResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("解析响应失败: %v", err)
	}

	if resp.Status != "ok" {
		t.Errorf("期望 status=ok, 得到 %s", resp.Status)
	}
}

func TestHandleHealth_WrongMethod(t *testing.T) {
	server := NewServer()

	req := httptest.NewRequest(http.MethodPost, "/api/health", nil)
	rec := httptest.NewRecorder()

	server.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("期望状态码 405, 得到 %d", rec.Code)
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
		t.Errorf("期望状态码 200, 得到 %d, body: %s", rec.Code, rec.Body.String())
	}

	var resp ValidateResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("解析响应失败: %v", err)
	}

	if !resp.Valid {
		t.Errorf("合法拓扑应通过校验, errors: %v", resp.Errors)
	}
}

func TestHandleValidate_InvalidTopology(t *testing.T) {
	server := NewServer()

	topo := map[string]interface{}{
		"project": map[string]interface{}{
			"id":   "", // 空 ID
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
		t.Errorf("期望状态码 200, 得到 %d", rec.Code)
	}

	var resp ValidateResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("解析响应失败: %v", err)
	}

	if resp.Valid {
		t.Errorf("非法拓扑不应通过校验")
	}

	if len(resp.Errors) == 0 {
		t.Errorf("应有校验错误")
	}
}

func TestHandleValidate_EmptyBody(t *testing.T) {
	server := NewServer()

	req := httptest.NewRequest(http.MethodPost, "/api/validate", nil)
	rec := httptest.NewRecorder()

	server.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("期望状态码 400, 得到 %d", rec.Code)
	}
}

func TestHandleValidate_InvalidJSON(t *testing.T) {
	server := NewServer()

	req := httptest.NewRequest(http.MethodPost, "/api/validate", bytes.NewReader([]byte("not json")))
	rec := httptest.NewRecorder()

	server.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("期望状态码 400, 得到 %d", rec.Code)
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
		t.Errorf("期望状态码 200, 得到 %d, body: %s", rec.Code, rec.Body.String())
	}

	var resp CompileResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("解析响应失败: %v", err)
	}

	// 检查 IP 已分配
	for _, node := range resp.Topology.Nodes {
		if node.OverlayIP == "" {
			t.Errorf("节点 %s 没有分配 IP", node.Name)
		}
	}

	// 检查 WireGuard 配置已生成
	if len(resp.WireGuardConfigs) == 0 {
		t.Errorf("应有 WireGuard 配置")
	}

	// 检查 Babel 配置已生成
	if len(resp.BabelConfigs) == 0 {
		t.Errorf("应有 Babel 配置")
	}

	// 检查安装脚本已生成
	if len(resp.InstallScripts) == 0 {
		t.Errorf("应有安装脚本")
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
		t.Errorf("期望状态码 422, 得到 %d", rec.Code)
	}
}

func TestHandleExport_ReturnsZip(t *testing.T) {
	server := NewServer()

	body := validTopologyJSON()
	req := httptest.NewRequest(http.MethodPost, "/api/export", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	server.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("期望状态码 200, 得到 %d, body: %s", rec.Code, rec.Body.String())
	}

	// 检查 Content-Type
	ct := rec.Header().Get("Content-Type")
	if ct != "application/zip" {
		t.Errorf("期望 Content-Type=application/zip, 得到 %s", ct)
	}

	// 检查 Content-Disposition
	cd := rec.Header().Get("Content-Disposition")
	if cd == "" {
		t.Errorf("应有 Content-Disposition header")
	}

	// 验证是有效的 zip
	zipReader, err := zip.NewReader(bytes.NewReader(rec.Body.Bytes()), int64(rec.Body.Len()))
	if err != nil {
		t.Fatalf("返回的不是有效的 zip 文件: %v", err)
	}

	// 检查 zip 内有文件
	if len(zipReader.File) == 0 {
		t.Errorf("zip 文件为空")
	}

	// 检查包含 wg0.conf 文件
	hasWgConf := false
	for _, f := range zipReader.File {
		if f.Name == "node-alpha/wireguard/wg0.conf" {
			hasWgConf = true
			break
		}
	}
	if !hasWgConf {
		t.Errorf("zip 应包含 node-alpha/wireguard/wg0.conf")
		t.Log("zip 文件列表:")
		for _, f := range zipReader.File {
			t.Logf("  %s", f.Name)
		}
	}
}

func TestCORS_Preflight(t *testing.T) {
	server := NewServer()

	req := httptest.NewRequest(http.MethodOptions, "/api/compile", nil)
	rec := httptest.NewRecorder()

	server.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Errorf("CORS preflight 期望状态码 204, 得到 %d", rec.Code)
	}

	if rec.Header().Get("Access-Control-Allow-Origin") != "*" {
		t.Errorf("应有 Access-Control-Allow-Origin: *")
	}
}

func TestCORS_Headers(t *testing.T) {
	server := NewServer()

	req := httptest.NewRequest(http.MethodGet, "/api/health", nil)
	rec := httptest.NewRecorder()

	server.Handler().ServeHTTP(rec, req)

	if rec.Header().Get("Access-Control-Allow-Origin") != "*" {
		t.Errorf("响应应有 CORS header")
	}
}

// --- 辅助函数 ---

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

// 确保使用了 io 包（zip 读取需要）
var _ = io.EOF
