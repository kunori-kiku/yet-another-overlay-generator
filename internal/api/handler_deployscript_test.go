package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestHandleDeployScript_BashIncludesPerInterfaceTeardown verifies that the
// /api/deploy-script?format=sh endpoint runs the full compilation pipeline, which
// populates PeerMap with each per-peer tunnel's interface name so that the generated
// teardown block contains per-interface removal steps (wg-quick down wg-<remote>).
// This is a regression test for audit blocker D36: the old implementation rendered the
// deploy script with a nil PeerMap, so the teardown block was missing all per-peer
// removal steps.
func TestHandleDeployScript_BashIncludesPerInterfaceTeardown(t *testing.T) {
	server := NewServer()

	body := deployTopologyWithSSHJSON()
	req := httptest.NewRequest(http.MethodPost, "/api/deploy-script?format=sh", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	server.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d, body: %s", rec.Code, rec.Body.String())
	}

	ct := rec.Header().Get("Content-Type")
	if ct != "text/x-shellscript; charset=utf-8" {
		t.Errorf("want Content-Type=text/x-shellscript; charset=utf-8, got %s", ct)
	}

	cd := rec.Header().Get("Content-Disposition")
	if !strings.Contains(cd, "deploy-all.sh") {
		t.Errorf("want Content-Disposition to contain deploy-all.sh, got %s", cd)
	}

	script := rec.Body.String()

	// In the per-peer architecture, node-beta and node-alpha are each other's peers:
	// node-beta's interface name is derived from the remote node-alpha -> wg-node-alpha;
	// node-alpha's interface name is derived from the remote node-beta -> wg-node-beta.
	// These two per-interface teardown lines only appear when PeerMap is correctly
	// populated; the old nil-PeerMap implementation produced no wg-node-* lines (only an
	// ID-agnostic fallback loop).
	teardownAlpha := "wg-quick down wg-node-alpha"
	teardownBeta := "wg-quick down wg-node-beta"
	if !strings.Contains(script, teardownAlpha) {
		t.Errorf("deploy script missing per-interface teardown line for node-beta's peer %q (PeerMap not populated)", teardownAlpha)
	}
	if !strings.Contains(script, teardownBeta) {
		t.Errorf("deploy script missing per-interface teardown line for node-alpha's peer %q (PeerMap not populated)", teardownBeta)
	}
}

// TestHandleDeployScript_InvalidTopologyReturns422 verifies that an invalid topology (an
// edge referencing a non-existent node) makes the compilation pipeline fail during
// semantic validation, so the endpoint returns 422, consistent with HandleCompile.
func TestHandleDeployScript_InvalidTopologyReturns422(t *testing.T) {
	server := NewServer()

	body := deployTopologyDanglingEdgeJSON()
	req := httptest.NewRequest(http.MethodPost, "/api/deploy-script?format=sh", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	server.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("want 422, got %d, body: %s", rec.Code, rec.Body.String())
	}

	var resp apiError
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("failed to parse error response: %v", err)
	}
	if resp.Error.Message == "" {
		t.Errorf("want 422 response body to contain a non-empty error.message field")
	}
}

// deployTopologyWithSSHJSON returns a two-router topology with SSH details.
// The two nodes are each other's peers and both have SSH configured, ensuring the deploy
// script renders teardown/deploy blocks for each node, with the blocks containing
// per-peer interface removal lines.
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

// deployTopologyDanglingEdgeJSON returns an invalid topology: edge-1 references the
// non-existent node node-missing. This error is caught by validateEdgeNodeRefs during
// semantic validation, making the compilation pipeline return an error (422).
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
