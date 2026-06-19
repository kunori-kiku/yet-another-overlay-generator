//go:build airgap

// handler_compile_warnings_test.go — plan-7 / 1.7: tagged behind //go:build airgap. It drives the
// anonymous /api/compile route (registered/linked only under -tags airgap). HandleCompilePreview
// surfaces the same warnings array on the controller path; conformance covers the warning contract
// in the default build.

package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestHandleCompile_SurfacesWarnings verifies that /api/compile, on a successful compile
// (200), also returns the non-fatal warnings produced by semantic validation via the
// warnings array (closing audit blocker UX-1).
//
// It builds a topology that compiles successfully but triggers a warning: a usable
// alpha->beta link (beta is publicly reachable and the edge carries endpoint_host,
// avoiding the D50 "definite dead link" hard error), plus an isolated node with no edges
// at all -- isolated-node detection is a stable warning-level check that does not block
// compilation.
func TestHandleCompile_SurfacesWarnings(t *testing.T) {
	server := NewServer()

	body := natWarningTopologyJSON()
	req := httptest.NewRequest(http.MethodPost, "/api/compile", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	server.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d, body: %s", rec.Code, rec.Body.String())
	}

	var resp CompileResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("failed to decode CompileResponse: %v", err)
	}

	if len(resp.Warnings) == 0 {
		t.Fatalf("want compile response to carry a non-empty warnings array (isolated node should produce a warning), got empty")
	}

	// Every warning should be warning-level, with non-empty fields/message, so the frontend
	// can render it directly.
	for i, w := range resp.Warnings {
		if w.Level != "warning" {
			t.Errorf("warnings[%d].level want \"warning\", got %q", i, w.Level)
		}
		if w.Message == "" {
			t.Errorf("warnings[%d].message should not be empty", i)
		}
	}
}

// natWarningTopologyJSON returns a topology that compiles successfully but triggers an
// isolated-node warning: the alpha->beta link is fully usable (beta is publicly reachable
// and the edge carries endpoint_host), while gamma has no edges.
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
