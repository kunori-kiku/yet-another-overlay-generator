package api

// controller_http_test.go is the in-process integration test for the networked
// controller HTTP surface (plan-4.5). It exercises the bearer-token + plain-HTTP +
// two-mux model end-to-end with a MemStore — no external process, no network beyond
// loopback, no TLS — covering:
//
//	(1) ENROLL over the AGENT mux with NO auth: the operator mints a single-use
//	    enrollment token (via the operator-token /enrollment-token route), the node
//	    POSTs /enroll with that token + its WG public key, and gets back a per-node
//	    api_token.
//	(2) the node uses its api_token as a Bearer credential on the agent routes.
//	(3) GET /config → 404 before any promote.
//	(4) the OPERATOR (authenticated by the shared operator token) drives
//	    /update-topology → /stage → /promote on the OPERATOR mux.
//	(5) the node: GET /config → 200 with the bundle; /poll?after=0 → the new
//	    generation; /poll already at the current generation (the handler's poll
//	    deadline is shrunk for the test) and no promote → 204; POST /report → ok, and
//	    GetNode shows the applied generation.
//	(6) AUTH: an absent/wrong bearer on an agent route → 401/403; a node token on an
//	    operator route → 403; an absent token on an operator route → 401.
//	(7) NODE-ACTS-AS-ITSELF: two enrolled nodes get DIFFERENT /config bundles.
//
// Plain HTTP throughout (httptest.NewServer); stdlib only.

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/kunorikiku/yet-another-overlay-generator/internal/controller"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/model"
	"golang.zx2c4.com/wireguard/wgctrl/wgtypes"
)

const testTenant = controller.TenantID("acme")

// testOperatorToken is the plaintext operator bearer token the test handler is built
// with. The handler stores only its hash; the test presents the plaintext.
const testOperatorToken = "op-secret-token-for-tests"

// ctlTestEnv bundles the in-process controller servers (two plain-HTTP httptest
// servers for the two muxes) and the shared store so phases can reach the store
// directly (e.g. to assert GetNode).
type ctlTestEnv struct {
	opSrv    *httptest.Server // operator/panel mux
	agentSrv *httptest.Server // agent mux
	store    controller.Store
}

// newCtlTestEnv stands up the controller over two plain-HTTP httptest servers (one
// per mux) backed by a MemStore. The handler is built with the operator token's
// hash, exactly as the production path does.
func newCtlTestEnv(t *testing.T) *ctlTestEnv {
	t.Helper()

	store := controller.NewMemStore()
	ch := NewControllerHandler(store, testTenant, controller.HashToken(testOperatorToken), DefaultOperatorName)
	// Shrink the server-side /poll long-poll deadline so the timeout-204 path returns
	// promptly instead of waiting the production ~55s. The server (not the client) is
	// what produces the 204, so this is the right knob.
	ch.pollDeadline = 250 * time.Millisecond

	opMux := http.NewServeMux()
	ch.RegisterOperatorRoutes(opMux)
	agentMux := http.NewServeMux()
	ch.RegisterAgentRoutes(agentMux)

	opSrv := httptest.NewServer(opMux)
	agentSrv := httptest.NewServer(agentMux)
	t.Cleanup(opSrv.Close)
	t.Cleanup(agentSrv.Close)

	return &ctlTestEnv{opSrv: opSrv, agentSrv: agentSrv, store: store}
}

// doJSON performs a request with an optional Bearer token and optional JSON body,
// decoding a JSON response into out (when out != nil and status is 200). It returns
// the status code. token == "" omits the Authorization header.
func doJSON(t *testing.T, method, url, token string, body any, out any) int {
	t.Helper()
	var rdr io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal body: %v", err)
		}
		rdr = bytes.NewReader(raw)
	}
	req, err := http.NewRequest(method, url, rdr)
	if err != nil {
		t.Fatalf("NewRequest %s %s: %v", method, url, err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, url, err)
	}
	defer resp.Body.Close()
	if out != nil && resp.StatusCode == http.StatusOK {
		if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
			t.Fatalf("decode %s %s response: %v", method, url, err)
		}
	} else {
		io.Copy(io.Discard, resp.Body)
	}
	return resp.StatusCode
}

// doRaw performs a request with a raw (non-JSON-marshaled) body and an optional
// Bearer token, returning the status code. Used for /update-topology, which stores
// the topology bytes verbatim.
func doRaw(t *testing.T, method, url, token string, body []byte) int {
	t.Helper()
	req, err := http.NewRequest(method, url, bytes.NewReader(body))
	if err != nil {
		t.Fatalf("NewRequest %s %s: %v", method, url, err)
	}
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, url, err)
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)
	return resp.StatusCode
}

// smallTopo is a minimal single-node-pair topology: a public router and a peer that
// dials it. Both nodes enroll, so the whole graph compiles.
func smallTopo() *model.Topology {
	return &model.Topology{
		Project: model.Project{ID: "ctrl-http-001", Name: "Controller HTTP Test"},
		Domains: []model.Domain{{
			ID: "domain-1", Name: "net", CIDR: "10.60.0.0/24",
			AllocationMode: "auto", RoutingMode: "babel",
		}},
		Nodes: []model.Node{
			{
				ID: "node-1", Name: "router", Hostname: "router.example.com",
				Role: "router", DomainID: "domain-1", ListenPort: 51820,
				Capabilities: model.NodeCapabilities{CanAcceptInbound: true, CanForward: true, HasPublicIP: true},
			},
			{
				ID: "node-2", Name: "peer",
				Role: "peer", DomainID: "domain-1", ListenPort: 51820,
				Capabilities: model.NodeCapabilities{CanAcceptInbound: false, CanForward: false, HasPublicIP: false},
			},
		},
		Edges: []model.Edge{
			{ID: "e-1", FromNodeID: "node-2", ToNodeID: "node-1", Type: "public-endpoint", EndpointHost: "198.51.100.1", EndpointPort: 0, Transport: "udp", IsEnabled: true},
		},
	}
}

// agentURL builds an agent-mux URL for the given controller path suffix.
func (e *ctlTestEnv) agentURL(suffix string) string {
	return e.agentSrv.URL + "/api/v1/controller/" + suffix
}

// opURL builds an operator-mux URL for the given controller path suffix.
func (e *ctlTestEnv) opURL(suffix string) string {
	return e.opSrv.URL + "/api/v1/controller/" + suffix
}

// mintEnrollmentToken drives the operator-token /enrollment-token route to mint a
// single-use token for nodeID and returns the plaintext.
func (e *ctlTestEnv) mintEnrollmentToken(t *testing.T, nodeID string) string {
	t.Helper()
	var resp enrollmentTokenResponseJSON
	status := doJSON(t, http.MethodPost, e.opURL("enrollment-token"), testOperatorToken, enrollmentTokenRequestJSON{
		NodeID:     nodeID,
		TTLSeconds: 3600,
	}, &resp)
	if status != http.StatusOK {
		t.Fatalf("enrollment-token(%s): status %d, want 200", nodeID, status)
	}
	if resp.Token == "" {
		t.Fatalf("enrollment-token(%s): empty token", nodeID)
	}
	return resp.Token
}

// enrollNode runs the full /enroll ceremony for nodeID over the agent mux with NO
// auth: the operator mints a token, the node generates a WG public key, POSTs
// /enroll, and the per-node api_token is returned. It returns that api_token.
func (e *ctlTestEnv) enrollNode(t *testing.T, nodeID string) string {
	t.Helper()
	enrollTok := e.mintEnrollmentToken(t, nodeID)

	wgPriv, err := wgtypes.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("wg GeneratePrivateKey: %v", err)
	}

	var resp enrollResponseJSON
	status := doJSON(t, http.MethodPost, e.agentURL("enroll"), "", enrollRequestJSON{
		Token:       enrollTok,
		NodeID:      nodeID,
		WGPublicKey: wgPriv.PublicKey().String(),
	}, &resp)
	if status != http.StatusOK {
		t.Fatalf("enroll %s: status %d, want 200", nodeID, status)
	}
	if resp.ApiToken == "" {
		t.Fatalf("enroll %s: empty api_token in response", nodeID)
	}
	if resp.NodeID != nodeID {
		t.Fatalf("enroll %s: response node_id %q, want %q", nodeID, resp.NodeID, nodeID)
	}
	return resp.ApiToken
}

// TestControllerHTTP_EnrollConfigPollReport is the full happy-path + auth
// integration test described in the file header.
func TestControllerHTTP_EnrollConfigPollReport(t *testing.T) {
	env := newCtlTestEnv(t)

	// (1)+(2) Enroll both nodes over the agent mux (no auth); capture their tokens.
	node1Token := env.enrollNode(t, "node-1")
	node2Token := env.enrollNode(t, "node-2")

	// (3) GET /config before any promote → 404.
	if status := doJSON(t, http.MethodGet, env.agentURL("config"), node1Token, nil, nil); status != http.StatusNotFound {
		t.Fatalf("config before promote: status %d, want 404", status)
	}

	// (4) Operator (operator token) drives update-topology → stage → promote.
	topoJSON, err := json.Marshal(smallTopo())
	if err != nil {
		t.Fatalf("marshal topology: %v", err)
	}
	if status := doRaw(t, http.MethodPost, env.opURL("update-topology"), testOperatorToken, topoJSON); status != http.StatusOK {
		t.Fatalf("update-topology: status %d, want 200", status)
	}

	var stage stageResponseJSON
	if status := doJSON(t, http.MethodPost, env.opURL("stage"), testOperatorToken, struct{}{}, &stage); status != http.StatusOK {
		t.Fatalf("stage: status %d, want 200", status)
	}
	if len(stage.Staged) != 2 {
		t.Fatalf("stage: staged %v, want both node-1 and node-2", stage.Staged)
	}

	var promote generationResponseJSON
	if status := doJSON(t, http.MethodPost, env.opURL("promote"), testOperatorToken, struct{}{}, &promote); status != http.StatusOK {
		t.Fatalf("promote: status %d, want 200", status)
	}
	if promote.Generation < 1 {
		t.Fatalf("promote: generation %d, want >= 1", promote.Generation)
	}

	// (5) Node fetches its config → 200 with a non-empty bundle.
	var cfg configResponseJSON
	if status := doJSON(t, http.MethodGet, env.agentURL("config"), node1Token, nil, &cfg); status != http.StatusOK {
		t.Fatalf("config after promote: status %d, want 200", status)
	}
	if cfg.Generation != promote.Generation {
		t.Fatalf("config generation %d, want %d", cfg.Generation, promote.Generation)
	}
	if len(cfg.Files) == 0 {
		t.Fatalf("config: empty bundle files")
	}
	// The files are base64; confirm at least one decodes to non-empty content.
	sawContent := false
	for path, b64 := range cfg.Files {
		raw, err := base64.StdEncoding.DecodeString(b64)
		if err != nil {
			t.Fatalf("config file %s: bad base64: %v", path, err)
		}
		if len(raw) > 0 {
			sawContent = true
		}
	}
	if !sawContent {
		t.Fatalf("config: all bundle files empty")
	}

	// /poll?after=0 → returns the current generation immediately (already promoted).
	var poll pollResponseJSON
	if status := doJSON(t, http.MethodGet, env.agentURL("poll?after=0"), node1Token, nil, &poll); status != http.StatusOK {
		t.Fatalf("poll after=0: status %d, want 200", status)
	}
	if poll.Generation != promote.Generation {
		t.Fatalf("poll generation %d, want %d", poll.Generation, promote.Generation)
	}

	// A /poll already AT the current generation with no further promote must time out
	// on the server's (test-shrunk) deadline → 204. This drives the timeout branch of
	// WaitForGeneration; the server returns 204 so the agent re-polls.
	if status := doJSON(t, http.MethodGet, env.agentURL("poll?after="+itoa(promote.Generation)), node1Token, nil, nil); status != http.StatusNoContent {
		t.Fatalf("poll timeout: status %d, want 204", status)
	}

	// POST /report → ok; GetNode reflects the applied generation.
	if status := doJSON(t, http.MethodPost, env.agentURL("report"), node1Token, reportRequestJSON{
		AppliedGeneration: promote.Generation,
		Checksum:          "deadbeef",
		Health:            "ok",
	}, nil); status != http.StatusOK {
		t.Fatalf("report: status %d, want 200", status)
	}
	node, err := env.store.GetNode(context.Background(), testTenant, "node-1")
	if err != nil {
		t.Fatalf("GetNode(node-1): %v", err)
	}
	if node.AppliedGeneration != promote.Generation {
		t.Fatalf("GetNode applied generation %d, want %d", node.AppliedGeneration, promote.Generation)
	}
	if node.LastChecksum != "deadbeef" {
		t.Fatalf("GetNode checksum %q, want deadbeef", node.LastChecksum)
	}

	// (6) AUTH rejections.
	// No bearer on an agent route → 401.
	if status := doJSON(t, http.MethodGet, env.agentURL("config"), "", nil, nil); status != http.StatusUnauthorized {
		t.Fatalf("no-token config: status %d, want 401", status)
	}
	// A garbage bearer on an agent route → 401 (resolves to no node).
	if status := doJSON(t, http.MethodGet, env.agentURL("config"), "not-a-real-token", nil, nil); status != http.StatusUnauthorized {
		t.Fatalf("bad-token config: status %d, want 401", status)
	}
	// A NODE token on an operator-only route (/stage) → 403 (wrong, but present).
	if status := doJSON(t, http.MethodPost, env.opURL("stage"), node2Token, struct{}{}, nil); status != http.StatusForbidden {
		t.Fatalf("node token on /stage: status %d, want 403", status)
	}
	// No token on an operator-only route → 401.
	if status := doJSON(t, http.MethodPost, env.opURL("stage"), "", struct{}{}, nil); status != http.StatusUnauthorized {
		t.Fatalf("no token on /stage: status %d, want 401", status)
	}

	// Operator read routes work with the operator token.
	var nodes []nodeJSON
	if status := doJSON(t, http.MethodGet, env.opURL("nodes"), testOperatorToken, nil, &nodes); status != http.StatusOK {
		t.Fatalf("nodes: status %d, want 200", status)
	}
	if len(nodes) != 2 {
		t.Fatalf("nodes: got %d, want 2", len(nodes))
	}
	for _, n := range nodes {
		if !n.HasWGPublicKey {
			t.Fatalf("nodes: %s reports no WG public key", n.NodeID)
		}
	}
}

// itoa is a tiny int64→string helper for building the poll cursor query without
// pulling strconv into the test's top-level imports (kept local for clarity).
func itoa(n int64) string {
	return fmt.Sprintf("%d", n)
}

// TestControllerHTTP_ReservedOperatorName confirms a node can never enroll AS the
// operator: /enroll rejects NodeID == the operator identity (before any token work),
// so no node-enrollment path can mint a node colliding with the operator identity.
func TestControllerHTTP_ReservedOperatorName(t *testing.T) {
	env := newCtlTestEnv(t)
	status := doJSON(t, http.MethodPost, env.agentURL("enroll"), "", enrollRequestJSON{
		Token:       "rejected-before-token-use",
		NodeID:      DefaultOperatorName,
		WGPublicKey: "unused",
	}, nil)
	if status != http.StatusForbidden {
		t.Fatalf("enroll as %q: status %d, want 403 (reserved operator name)", DefaultOperatorName, status)
	}
}

// TestControllerHTTP_NodeActsOnlyAsItself confirms /config returns the CALLER's own
// bundle (derived from its bearer token) and that two different nodes get two
// different bundles — there is no request field by which node A could obtain node B's
// config.
func TestControllerHTTP_NodeActsOnlyAsItself(t *testing.T) {
	env := newCtlTestEnv(t)
	node1Token := env.enrollNode(t, "node-1")
	node2Token := env.enrollNode(t, "node-2")

	topoJSON, err := json.Marshal(smallTopo())
	if err != nil {
		t.Fatalf("marshal topology: %v", err)
	}
	if status := doRaw(t, http.MethodPost, env.opURL("update-topology"), testOperatorToken, topoJSON); status != http.StatusOK {
		t.Fatalf("update-topology: status %d, want 200", status)
	}
	if status := doJSON(t, http.MethodPost, env.opURL("stage"), testOperatorToken, struct{}{}, &stageResponseJSON{}); status != http.StatusOK {
		t.Fatalf("stage: status %d, want 200", status)
	}
	if status := doJSON(t, http.MethodPost, env.opURL("promote"), testOperatorToken, struct{}{}, &generationResponseJSON{}); status != http.StatusOK {
		t.Fatalf("promote: status %d, want 200", status)
	}

	var cfg1, cfg2 configResponseJSON
	if status := doJSON(t, http.MethodGet, env.agentURL("config"), node1Token, nil, &cfg1); status != http.StatusOK {
		t.Fatalf("node-1 config: status %d, want 200", status)
	}
	if status := doJSON(t, http.MethodGet, env.agentURL("config"), node2Token, nil, &cfg2); status != http.StatusOK {
		t.Fatalf("node-2 config: status %d, want 200", status)
	}
	// Each node received ITS OWN bundle: the router (node-1) and peer (node-2) install
	// scripts differ. If /config ignored the token and served one shared bundle, these
	// would be identical.
	if cfg1.Files["install.sh"] == "" || cfg2.Files["install.sh"] == "" {
		t.Fatalf("a config bundle is missing install.sh (node-1 keys %v, node-2 keys %v)", keysOfMap(cfg1.Files), keysOfMap(cfg2.Files))
	}
	if cfg1.Files["install.sh"] == cfg2.Files["install.sh"] {
		t.Fatalf("node-1 and node-2 received identical install.sh; /config must return the caller's own node bundle")
	}
}

// TestControllerHTTP_AirGapOpen confirms the air-gap routes stay OPEN and
// UNAUTHENTICATED even when controller mode is enabled: they live on the operator
// mux (s.mux) and are never gated by the operator token. Here we drive the full
// Server (not a bare mux) so the air-gap routes are registered, enable the
// controller, and confirm /api/health responds with no Authorization header.
func TestControllerHTTP_AirGapOpen(t *testing.T) {
	srv := NewServer()
	ch := NewControllerHandler(controller.NewMemStore(), testTenant, controller.HashToken(testOperatorToken), DefaultOperatorName)
	srv.EnableController(ch)

	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)

	// Air-gap health: no token, must be 200.
	if status := doJSON(t, http.MethodGet, ts.URL+"/api/health", "", nil, nil); status != http.StatusOK {
		t.Fatalf("air-gap /api/health: status %d, want 200 (must stay open)", status)
	}
	// The operator route on the SAME mux still requires the operator token (401 absent).
	if status := doJSON(t, http.MethodGet, ts.URL+"/api/v1/controller/nodes", "", nil, nil); status != http.StatusUnauthorized {
		t.Fatalf("operator /nodes without token: status %d, want 401", status)
	}
}

// keysOfMap returns a map's keys for diagnostic messages.
func keysOfMap(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
