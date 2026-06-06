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
// 构造一个可成功编译但会触发 NAT 告警的拓扑：两个均位于 NAT 之后（无公网 IP、
// 角色为 peer）的节点，之间有一条启用的 direct 边且未设置 endpoint_host。
// 这正是 validator.validateNATReachability 中 AddWarning 的触发条件——双 NAT 的
// direct 链路以及目标节点无公网可达地址——但不会产生任何硬性语义错误，因此编译成功。
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
		t.Fatalf("期望编译响应携带非空 warnings 数组（双 NAT direct 链路应产生 NAT 告警），实际为空")
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

// natWarningTopologyJSON 返回一个可成功编译但会触发 NAT 告警的拓扑。
// 两个 peer 节点均无公网 IP（NAT 之后），之间一条启用的 direct 边且无 endpoint_host。
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
					"can_accept_inbound": false,
					"can_forward":        false,
					"can_relay":          false,
					"has_public_ip":      false,
				},
			},
		},
		"edges": []interface{}{
			map[string]interface{}{
				"id":           "edge-1",
				"from_node_id": "node-1",
				"to_node_id":   "node-2",
				"type":         "direct",
				"transport":    "udp",
				"is_enabled":   true,
			},
		},
	}

	body, _ := json.Marshal(topo)
	return body
}
