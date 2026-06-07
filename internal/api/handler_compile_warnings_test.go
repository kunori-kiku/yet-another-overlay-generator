package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestHandleCompile_SurfacesWarnings 验证 /api/compile 在编译成功（200）的同时，
// 把语义校验产生的非致命告警通过 warnings 数组返回（关闭审计阻断项 UX-1）。
//
// 构造一个可成功编译但会触发告警的拓扑：一条 alpha→beta 的可用链路（beta 公网
// 可达且边上带 endpoint_host，避免触发 D50 的「确凿死链」硬错误），外加一个没有任何
// 边的孤立节点——孤立节点检测是稳定的 warning 级校验，不阻断编译。
func TestHandleCompile_SurfacesWarnings(t *testing.T) {
	server := NewServer()

	body := natWarningTopologyJSON()
	req := httptest.NewRequest(http.MethodPost, "/api/compile", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	server.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("期望 200，实际 %d，body: %s", rec.Code, rec.Body.String())
	}

	var resp CompileResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("解码 CompileResponse 失败: %v", err)
	}

	if len(resp.Warnings) == 0 {
		t.Fatalf("期望编译响应携带非空 warnings 数组（孤立节点应产生告警），实际为空")
	}

	// 每条告警都应是 warning 级别，且字段/消息非空，确保前端可直接渲染。
	for i, w := range resp.Warnings {
		if w.Level != "warning" {
			t.Errorf("warnings[%d].level 期望 \"warning\"，实际 %q", i, w.Level)
		}
		if w.Message == "" {
			t.Errorf("warnings[%d].message 不应为空", i)
		}
	}
}

// natWarningTopologyJSON 返回一个可成功编译但会触发孤立节点告警的拓扑：
// alpha→beta 链路完整可用（beta 公网可达、边带 endpoint_host），gamma 无任何边。
func natWarningTopologyJSON() []byte {
	topo := map[string]interface{}{
		"project": map[string]interface{}{
			"id":      "nat-warning-001",
			"name":    "NAT Warning Project",
			"version": "0.1.0",
		},
		"domains": []interface{}{
			map[string]interface{}{
				"id":              "domain-1",
				"name":            "nat-network",
				"cidr":            "10.20.0.0/24",
				"allocation_mode": "auto",
				"routing_mode":    "babel",
			},
		},
		"nodes": []interface{}{
			map[string]interface{}{
				"id":          "node-1",
				"name":        "node-alpha",
				"hostname":    "alpha.internal",
				"platform":    "debian",
				"role":        "peer",
				"domain_id":   "domain-1",
				"listen_port": 51820,
				"capabilities": map[string]interface{}{
					"can_accept_inbound": false,
					"can_forward":        false,
					"can_relay":          false,
					"has_public_ip":      false,
				},
			},
			map[string]interface{}{
				"id":          "node-2",
				"name":        "node-beta",
				"hostname":    "beta.internal",
				"platform":    "ubuntu",
				"role":        "peer",
				"domain_id":   "domain-1",
				"listen_port": 51820,
				"capabilities": map[string]interface{}{
					"can_accept_inbound": true,
					"can_forward":        false,
					"can_relay":          false,
					"has_public_ip":      true,
				},
				"public_endpoints": []interface{}{
					map[string]interface{}{
						"id":   "ep-beta-1",
						"host": "beta.example.com",
						"port": 51820,
					},
				},
			},
			map[string]interface{}{
				"id":          "node-3",
				"name":        "node-gamma",
				"hostname":    "gamma.internal",
				"platform":    "debian",
				"role":        "peer",
				"domain_id":   "domain-1",
				"listen_port": 51820,
				"capabilities": map[string]interface{}{
					"can_accept_inbound": false,
					"can_forward":        false,
					"can_relay":          false,
					"has_public_ip":      false,
				},
			},
		},
		"edges": []interface{}{
			map[string]interface{}{
				"id":            "edge-1",
				"from_node_id":  "node-1",
				"to_node_id":    "node-2",
				"type":          "direct",
				"endpoint_host": "beta.example.com",
				"transport":     "udp",
				"is_enabled":    true,
			},
		},
	}

	body, _ := json.Marshal(topo)
	return body
}
