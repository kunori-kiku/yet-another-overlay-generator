package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestRequestBodySizeCap_Returns413 验证超过 4 MiB 上限的 POST 请求体
// 被 http.MaxBytesReader 拒绝，并由处理器映射为 413 Payload Too Large（D34）。
//
// 请求体是合法 JSON 的前缀（以一个巨大的字符串字段填充至超限），确保触发的是
// 大小上限而非 JSON 解析错误——读取阶段在解析之前就会因超限而失败。
func TestRequestBodySizeCap_Returns413(t *testing.T) {
	server := NewServer()

	// 构造一个略大于 maxRequestBodyBytes 的请求体。
	oversized := bytes.Repeat([]byte("a"), int(maxRequestBodyBytes)+1024)
	req := httptest.NewRequest(http.MethodPost, "/api/validate", bytes.NewReader(oversized))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	server.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("期望 413，实际 %d，body: %s", rec.Code, rec.Body.String())
	}

	// 错误响应必须是 {"error":{code,message,params}} 形式，供前端展示/本地化。
	var resp apiError
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("解码错误响应失败: %v", err)
	}
	if resp.Error.Message == "" {
		t.Errorf("413 响应应包含非空的 error.message 字段")
	}
}

// TestRecoverPanics_Returns500JSON 直接测试 recoverPanics 中间件：
// 一个故意 panic 的 http.HandlerFunc 经中间件包裹后，应返回 500 且响应体为
// {"error": ...} JSON，而不是中断连接（D60）。
// TestRecovered_MuxPanicReturns500JSON pins B1: recovered() — the top-level wrapper applied
// to BOTH the operator and agent muxes (not just the air-gap routes) — converts a handler
// panic into a coded 500 JSON instead of a torn connection. The operator/agent routes had no
// per-route recovery before this, so a panic in a fleet/agent handler degraded the
// controller in exactly the mode rc.1 gates on.
func TestRecovered_MuxPanicReturns500JSON(t *testing.T) {
	server := NewServer()

	mux := http.NewServeMux()
	mux.HandleFunc("/boom", func(w http.ResponseWriter, r *http.Request) {
		panic("deliberate panic on a controller mux route")
	})
	h := server.recovered(mux)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/boom", nil))

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("panic on a mux route: status %d, want 500", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}
	var resp apiError
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode error body: %v", err)
	}
	if resp.Error.Message == "" {
		t.Errorf("recovered 500 must carry a non-empty error.message")
	}
}

func TestRecoverPanics_Returns500JSON(t *testing.T) {
	server := NewServer()

	panicking := func(w http.ResponseWriter, r *http.Request) {
		panic("deliberate panic for recovery test")
	}

	wrapped := server.recoverPanics(panicking)

	req := httptest.NewRequest(http.MethodPost, "/api/compile", nil)
	rec := httptest.NewRecorder()

	// 不应向上抛出 panic；中间件须捕获并转换为 500。
	wrapped(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("期望 500，实际 %d", rec.Code)
	}

	ct := rec.Header().Get("Content-Type")
	if ct != "application/json" {
		t.Errorf("期望 Content-Type=application/json，实际 %q", ct)
	}

	var resp apiError
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("解码错误响应失败: %v", err)
	}
	if resp.Error.Message == "" {
		t.Errorf("500 响应应包含非空的 error.message 字段")
	}
}

// TestRecoverPanics_PassesThroughNonPanicking 验证非 panic 的处理器在被
// recoverPanics 包裹后行为不变：状态码与响应体均原样透传。
func TestRecoverPanics_PassesThroughNonPanicking(t *testing.T) {
	server := NewServer()

	ok := func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	}

	wrapped := server.recoverPanics(ok)

	req := httptest.NewRequest(http.MethodGet, "/api/health", nil)
	rec := httptest.NewRecorder()

	wrapped(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("期望 200，实际 %d", rec.Code)
	}

	var resp map[string]string
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("解码响应失败: %v", err)
	}
	if resp["status"] != "ok" {
		t.Errorf("期望 status=ok，实际 %q", resp["status"])
	}
}
