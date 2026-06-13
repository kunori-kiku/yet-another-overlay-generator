package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestHandleDeployScript_BashIncludesPerInterfaceTeardown 验证 /api/deploy-script?format=sh
// 端点运行完整编译流水线，从而把每条 per-peer 隧道的接口名填入 PeerMap，使生成的卸载块
// 含逐接口拆除步骤（wg-quick down wg-<remote>）。这是审计阻断项 D36 的回归测试：旧实现
// 以 nil PeerMap 渲染部署脚本，卸载块完全缺失 per-peer 拆除步骤。
func TestHandleDeployScript_BashIncludesPerInterfaceTeardown(t *testing.T) {
	server := NewServer()

	body := deployTopologyWithSSHJSON()
	req := httptest.NewRequest(http.MethodPost, "/api/deploy-script?format=sh", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	server.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("期望 200，得到 %d，body: %s", rec.Code, rec.Body.String())
	}

	ct := rec.Header().Get("Content-Type")
	if ct != "text/x-shellscript; charset=utf-8" {
		t.Errorf("期望 Content-Type=text/x-shellscript; charset=utf-8，得到 %s", ct)
	}

	cd := rec.Header().Get("Content-Disposition")
	if !strings.Contains(cd, "deploy-all.sh") {
		t.Errorf("期望 Content-Disposition 含 deploy-all.sh，得到 %s", cd)
	}

	script := rec.Body.String()

	// per-peer 架构下，节点 node-beta 与 node-alpha 互为对端：
	// node-beta 的接口名取自远端 node-alpha → wg-node-alpha；
	// node-alpha 的接口名取自远端 node-beta → wg-node-beta。
	// 这两条逐接口拆除行只有在 PeerMap 被正确填充时才会出现；旧的 nil PeerMap 实现
	// 不会产生任何 wg-node-* 行（仅有按 ID 无关的兜底循环）。
	teardownAlpha := "wg-quick down wg-node-alpha"
	teardownBeta := "wg-quick down wg-node-beta"
	if !strings.Contains(script, teardownAlpha) {
		t.Errorf("部署脚本缺少 node-beta 对端的逐接口拆除行 %q（PeerMap 未填充）", teardownAlpha)
	}
	if !strings.Contains(script, teardownBeta) {
		t.Errorf("部署脚本缺少 node-alpha 对端的逐接口拆除行 %q（PeerMap 未填充）", teardownBeta)
	}
}

// TestHandleDeployScript_InvalidTopologyReturns422 验证无效拓扑（边引用了不存在的节点）
// 会让编译流水线在语义校验阶段失败，端点返回 422，与 HandleCompile 的行为一致。
func TestHandleDeployScript_InvalidTopologyReturns422(t *testing.T) {
	server := NewServer()

	body := deployTopologyDanglingEdgeJSON()
	req := httptest.NewRequest(http.MethodPost, "/api/deploy-script?format=sh", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	server.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("期望 422，得到 %d，body: %s", rec.Code, rec.Body.String())
	}

	var resp apiError
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("解析错误响应失败: %v", err)
	}
	if resp.Error.Message == "" {
		t.Errorf("期望 422 响应体含非空 error.message 字段")
	}
}

// deployTopologyWithSSHJSON 返回一个含 SSH 详情的双 router 拓扑。
// 两个节点互为对端并都配置了 SSH，确保部署脚本会为每个节点渲染卸载/部署块，
// 块内含 per-peer 接口的逐一拆除行。
func deployTopologyWithSSHJSON() []byte {
	topo := map[string]interface{}{
		"project": map[string]interface{}{
			"id":      "deploy-test-001",
			"name":    "Deploy Test Project",
			"version": "0.1.0",
		},
		"domains": []interface{}{
			map[string]interface{}{
				"id":              "domain-1",
				"name":            "test-network",
				"cidr":            "10.20.0.0/24",
				"allocation_mode": "auto",
				"routing_mode":    "babel",
			},
		},
		"nodes": []interface{}{
			map[string]interface{}{
				"id":           "node-1",
				"name":         "node-alpha",
				"hostname":     "alpha.example.com",
				"platform":     "debian",
				"role":         "router",
				"domain_id":    "domain-1",
				"listen_port":  51820,
				"ssh_host":     "203.0.113.1",
				"ssh_user":     "root",
				"ssh_port":     22,
				"ssh_key_path": "/home/op/.ssh/id_ed25519",
				"capabilities": map[string]interface{}{
					"can_accept_inbound": true,
					"can_forward":        true,
					"can_relay":          false,
					"has_public_ip":      true,
				},
			},
			map[string]interface{}{
				"id":           "node-2",
				"name":         "node-beta",
				"hostname":     "beta.example.com",
				"platform":     "ubuntu",
				"role":         "router",
				"domain_id":    "domain-1",
				"listen_port":  51820,
				"ssh_host":     "203.0.113.2",
				"ssh_user":     "root",
				"ssh_port":     22,
				"ssh_key_path": "/home/op/.ssh/id_ed25519",
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

	out, _ := json.Marshal(topo)
	return out
}

// deployTopologyDanglingEdgeJSON 返回一个无效拓扑：edge-1 引用了不存在的节点 node-missing。
// 该错误在语义校验阶段被 validateEdgeNodeRefs 捕获，使编译流水线返回错误（422）。
func deployTopologyDanglingEdgeJSON() []byte {
	topo := map[string]interface{}{
		"project": map[string]interface{}{
			"id":      "deploy-test-002",
			"name":    "Deploy Invalid Project",
			"version": "0.1.0",
		},
		"domains": []interface{}{
			map[string]interface{}{
				"id":              "domain-1",
				"name":            "test-network",
				"cidr":            "10.20.0.0/24",
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
		},
		"edges": []interface{}{
			map[string]interface{}{
				"id":           "edge-1",
				"from_node_id": "node-1",
				"to_node_id":   "node-missing",
				"type":         "direct",
				"transport":    "udp",
				"is_enabled":   true,
			},
		},
	}

	out, _ := json.Marshal(topo)
	return out
}
