//go:build airgap

// handler_compile_coded_test.go — plan-7 / 1.7: tagged behind //go:build airgap. It drives the
// anonymous /api/compile route, which is registered/linked only under -tags airgap. The default-
// build compile coded-error contract is exercised by the controller compile-preview tests +
// internal/conformance/.

package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestHandleCompile_CodedConstraintEnvelope proves the plan-3.5b end-to-end coded path: a
// compile-time CONSTRAINT failure coded at the source (internal/compiler) flows through the
// writeCodedOr relay and is serialized as the nested {error:{code,message,params}} envelope with
// the source's 422 status — not flattened to a generic 500 nor a legacy bare string.
//
// The topology is valid (passes schema + semantic) but its domain's transit pool is a /30 (one
// pair), while two distinct live links into the public hub each need a transit pair — so the
// SECOND link exhausts the pool inside DerivePeers (CodeTransitPoolExhausted).
func TestHandleCompile_CodedConstraintEnvelope(t *testing.T) {
	server := NewServer()

	req := httptest.NewRequest(http.MethodPost, "/api/compile", bytes.NewReader(transitExhaustedTopologyJSON()))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("expected HTTP 422 for a transit-pool-exhausted compile, got %d; body: %s", rec.Code, rec.Body.String())
	}

	var env struct {
		Error struct {
			Code    string            `json:"code"`
			Message string            `json:"message"`
			Params  map[string]string `json:"params"`
		} `json:"error"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&env); err != nil {
		t.Fatalf("decode error envelope: %v; body: %s", err, rec.Body.String())
	}
	if env.Error.Code != "compile_transit_pool_exhausted" {
		t.Fatalf("envelope code = %q, want compile_transit_pool_exhausted; body: %s", env.Error.Code, rec.Body.String())
	}
	if env.Error.Message == "" {
		t.Errorf("envelope message must carry the English default (CLI/curl + i18n fallback)")
	}
	if env.Error.Params["cidr"] == "" {
		t.Errorf("envelope must carry the {cidr} param for client-side localization; got %v", env.Error.Params)
	}
}

// transitExhaustedTopologyJSON builds a valid star topology (two spokes into one public hub) whose
// domain transit pool is a /30 (a single transit pair) — the second link exhausts it at compile.
func transitExhaustedTopologyJSON() []byte {
	mkNode := func(id, name, host string, public bool) map[string]interface{} {
		node := map[string]interface{}{
			"id": id, "name": name, "hostname": host, "platform": "debian",
			"role": "peer", "domain_id": "domain-1", "listen_port": 51820,
			"capabilities": map[string]interface{}{
				"can_accept_inbound": public, "can_forward": false,
				"can_relay": false, "has_public_ip": public,
			},
		}
		if public {
			node["public_endpoints"] = []interface{}{
				map[string]interface{}{"id": "ep-" + id, "host": "hub.example.com", "port": 51820},
			}
		}
		return node
	}
	mkEdge := func(id, from, to string) map[string]interface{} {
		return map[string]interface{}{
			"id": id, "from_node_id": from, "to_node_id": to, "type": "direct",
			"endpoint_host": "hub.example.com", "transport": "udp", "is_enabled": true,
		}
	}
	topo := map[string]interface{}{
		"project": map[string]interface{}{"id": "transit-exhausted-001", "name": "Transit Exhausted", "version": "0.1.0"},
		"domains": []interface{}{
			map[string]interface{}{
				"id": "domain-1", "name": "small-transit", "cidr": "10.30.0.0/24",
				"allocation_mode": "auto", "routing_mode": "babel", "transit_cidr": "10.20.0.0/30",
			},
		},
		"nodes": []interface{}{
			mkNode("node-hub", "node-hub", "hub.internal", true),
			mkNode("node-a", "node-a", "a.internal", false),
			mkNode("node-b", "node-b", "b.internal", false),
		},
		"edges": []interface{}{
			mkEdge("edge-a", "node-a", "node-hub"),
			mkEdge("edge-b", "node-b", "node-hub"),
		},
	}
	body, _ := json.Marshal(topo)
	return body
}
