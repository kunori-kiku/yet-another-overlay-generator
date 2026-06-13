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
	"strings"
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
	return newCtlTestEnvWith(t, nil)
}

// newCtlTestEnvWith is newCtlTestEnv with a configuration hook applied to the handler
// BEFORE route registration (e.g. the prefix setters). It is the SINGLE controller
// test-env constructor in this package — sibling test files must extend it via the
// hook rather than re-implementing the scaffold, so a change to handler construction
// is made exactly once.
func newCtlTestEnvWith(t *testing.T, configure func(*ControllerHandler)) *ctlTestEnv {
	t.Helper()

	store := controller.NewMemStore()
	ch := NewControllerHandler(store, testTenant, controller.HashToken(testOperatorToken), DefaultOperatorName)
	// Shrink the server-side /poll long-poll deadline so the timeout-204 path returns
	// promptly instead of waiting the production ~55s. The server (not the client) is
	// what produces the 204, so this is the right knob.
	ch.pollDeadline = 250 * time.Millisecond
	if configure != nil {
		configure(ch)
	}

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
// the canonical re-marshaled form of the posted topology.
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

// agentURL builds an agent-mux URL for the given agent-namespace path suffix.
func (e *ctlTestEnv) agentURL(suffix string) string {
	return e.agentSrv.URL + "/api/v1/agent/" + suffix
}

// opURL builds an operator-mux URL for the given operator-namespace path suffix.
func (e *ctlTestEnv) opURL(suffix string) string {
	return e.opSrv.URL + "/api/v1/operator/" + suffix
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

	// A full deploy leaves a complete audit trail: update-topology, stage, and promote
	// must each have appended an entry (plan-1 closed the update-topology/promote gaps).
	auditEntries, err := env.store.ListAudit(context.Background(), testTenant)
	if err != nil {
		t.Fatalf("ListAudit: %v", err)
	}
	wantActions := map[string]bool{"update-topology": false, "stage": false, "promote": false}
	for _, e := range auditEntries {
		if _, ok := wantActions[e.Action]; ok {
			wantActions[e.Action] = true
		}
	}
	for action, seen := range wantActions {
		if !seen {
			t.Errorf("full deploy left no %q audit entry (entries: %d)", action, len(auditEntries))
		}
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
	if status := doJSON(t, http.MethodGet, ts.URL+"/api/v1/operator/nodes", "", nil, nil); status != http.StatusUnauthorized {
		t.Fatalf("operator /nodes without token: status %d, want 401", status)
	}
}

// promoteSmallTopo drives the operator update-topology → stage → promote sequence
// for smallTopo over the operator mux, so the enrolled nodes have a current bundle
// to fetch on /config. It fails the test on any non-200 step.
func (e *ctlTestEnv) promoteSmallTopo(t *testing.T) {
	t.Helper()
	topoJSON, err := json.Marshal(smallTopo())
	if err != nil {
		t.Fatalf("marshal topology: %v", err)
	}
	if status := doRaw(t, http.MethodPost, e.opURL("update-topology"), testOperatorToken, topoJSON); status != http.StatusOK {
		t.Fatalf("update-topology: status %d, want 200", status)
	}
	if status := doJSON(t, http.MethodPost, e.opURL("stage"), testOperatorToken, struct{}{}, &stageResponseJSON{}); status != http.StatusOK {
		t.Fatalf("stage: status %d, want 200", status)
	}
	if status := doJSON(t, http.MethodPost, e.opURL("promote"), testOperatorToken, struct{}{}, &generationResponseJSON{}); status != http.StatusOK {
		t.Fatalf("promote: status %d, want 200", status)
	}
}

// TestControllerHTTP_Revoke confirms the operator /revoke route immediately voids a
// node's bearer credential: the node enrolls and its token works on /config, the
// operator POSTs /revoke {node_id}, and the SAME token now 401s on /config (the
// revoke flipped Status to NodeRevoked AND cleared the API token, so the bearer no
// longer resolves to an approved node). Revoking an unknown node is a 404.
func TestControllerHTTP_Revoke(t *testing.T) {
	env := newCtlTestEnv(t)
	node1Token := env.enrollNode(t, "node-1")
	env.promoteSmallTopo(t)

	// The freshly enrolled, approved node's token works on /config.
	if status := doJSON(t, http.MethodGet, env.agentURL("config"), node1Token, nil, nil); status != http.StatusOK {
		t.Fatalf("config before revoke: status %d, want 200", status)
	}

	// Revoking an unknown node → 404 (nothing to revoke).
	if status := doJSON(t, http.MethodPost, env.opURL("revoke"), testOperatorToken, revokeRequestJSON{NodeID: "ghost"}, nil); status != http.StatusNotFound {
		t.Fatalf("revoke unknown node: status %d, want 404", status)
	}

	// Operator revokes node-1.
	var rev revokeResponseJSON
	if status := doJSON(t, http.MethodPost, env.opURL("revoke"), testOperatorToken, revokeRequestJSON{NodeID: "node-1"}, &rev); status != http.StatusOK {
		t.Fatalf("revoke node-1: status %d, want 200", status)
	}
	if rev.NodeID != "node-1" || !rev.Revoked {
		t.Fatalf("revoke node-1: response %+v, want {node_id:node-1 revoked:true}", rev)
	}

	// The SAME token now 401s on /config: the bearer credential stops resolving.
	if status := doJSON(t, http.MethodGet, env.agentURL("config"), node1Token, nil, nil); status != http.StatusUnauthorized {
		t.Fatalf("config after revoke: status %d, want 401 (revoked token must stop resolving)", status)
	}

	// The registry reflects the revoked status and the cleared token hash.
	node, err := env.store.GetNode(context.Background(), testTenant, "node-1")
	if err != nil {
		t.Fatalf("GetNode(node-1): %v", err)
	}
	if node.Status != controller.NodeRevoked {
		t.Fatalf("GetNode status %q, want %q", node.Status, controller.NodeRevoked)
	}
	if node.APITokenHash != "" {
		t.Fatalf("GetNode APITokenHash %q, want empty after revoke", node.APITokenHash)
	}
}

// TestControllerHTTP_ReEnrollRotation confirms re-enrolling a node rotates its bearer
// token AND invalidates the old one at the lookup chokepoint: node-1 enrolls (token1
// works), the operator mints a fresh enrollment token and node-1 enrolls again
// (token2), after which token1 401s and token2 works on /config. This is the
// re-enroll-leaves-old-token bug: a stale token must never authorize.
func TestControllerHTTP_ReEnrollRotation(t *testing.T) {
	env := newCtlTestEnv(t)
	token1 := env.enrollNode(t, "node-1")
	env.promoteSmallTopo(t)

	// token1 works on /config.
	if status := doJSON(t, http.MethodGet, env.agentURL("config"), token1, nil, nil); status != http.StatusOK {
		t.Fatalf("config with token1: status %d, want 200", status)
	}

	// Operator mints a fresh enrollment token; node-1 re-enrolls and gets token2.
	token2 := env.enrollNode(t, "node-1")
	if token2 == token1 {
		t.Fatalf("re-enroll returned the same api_token; expected a rotated token")
	}

	// token1 (the OLD token) now 401s — rotation invalidated it.
	if status := doJSON(t, http.MethodGet, env.agentURL("config"), token1, nil, nil); status != http.StatusUnauthorized {
		t.Fatalf("config with old token1 after re-enroll: status %d, want 401 (stale token must not authorize)", status)
	}

	// token2 (the NEW token) works.
	if status := doJSON(t, http.MethodGet, env.agentURL("config"), token2, nil, nil); status != http.StatusOK {
		t.Fatalf("config with new token2: status %d, want 200", status)
	}
}

// TestControllerHTTP_HealthSurfaced confirms a node's reported health is persisted and
// surfaced to the operator: the node POSTs /report with a health string, and the
// operator GET /nodes view shows it in last_health.
func TestControllerHTTP_HealthSurfaced(t *testing.T) {
	env := newCtlTestEnv(t)
	node1Token := env.enrollNode(t, "node-1")
	env.promoteSmallTopo(t)

	const wantHealth = "degraded: babel reconverging"
	if status := doJSON(t, http.MethodPost, env.agentURL("report"), node1Token, reportRequestJSON{
		AppliedGeneration: 1,
		Checksum:          "cafebabe",
		Health:            wantHealth,
	}, nil); status != http.StatusOK {
		t.Fatalf("report: status %d, want 200", status)
	}

	var nodes []nodeJSON
	if status := doJSON(t, http.MethodGet, env.opURL("nodes"), testOperatorToken, nil, &nodes); status != http.StatusOK {
		t.Fatalf("nodes: status %d, want 200", status)
	}
	var found bool
	for _, n := range nodes {
		if n.NodeID == "node-1" {
			found = true
			if n.LastHealth != wantHealth {
				t.Fatalf("node-1 last_health %q, want %q", n.LastHealth, wantHealth)
			}
		}
	}
	if !found {
		t.Fatalf("nodes view did not include node-1")
	}
}

// TestControllerHTTP_RekeyFlow exercises the fleet-wide key-rotation flow end-to-end
// over the two muxes: the operator POSTs /rekey-all (flagging every APPROVED node),
// the operator GET /nodes view and the agent GET /config response both surface
// rekey_requested=true, the agent POSTs /rekey with its NEW WireGuard public key, and
// afterward /nodes shows rekey_requested=false while the registry holds the NEW public
// key (zero-knowledge: only the public key is registered). It also pins the two
// rejection paths: an empty wg_public_key is a 400, and a node token cannot drive the
// operator-only /rekey-all (403).
func TestControllerHTTP_RekeyFlow(t *testing.T) {
	env := newCtlTestEnv(t)
	node1Token := env.enrollNode(t, "node-1")
	node2Token := env.enrollNode(t, "node-2")
	// A current bundle must exist so /config returns 200 (it 404s before any promote);
	// the rekey_requested flag rides on that 200 response.
	env.promoteSmallTopo(t)

	// Register a PENDING node (slot created, never enrolled): /rekey-all must SKIP it
	// (it flags only NodeApproved nodes), so the requested count is the approved count
	// and the pending node's rekey_requested stays false (the skip-path assertion).
	if err := env.store.UpsertNode(context.Background(), testTenant, controller.Node{
		NodeID: "node-pending", Status: controller.NodePending,
	}); err != nil {
		t.Fatalf("UpsertNode(node-pending): %v", err)
	}

	// Capture node-1's original public key so we can prove the rekey changed it.
	origNode, err := env.store.GetNode(context.Background(), testTenant, "node-1")
	if err != nil {
		t.Fatalf("GetNode(node-1) before rekey: %v", err)
	}
	origPub := origNode.WGPublicKey
	if origPub == "" {
		t.Fatalf("node-1 has no WG public key before rekey")
	}

	// Capture the current generation so we can assert /rekey-all advances it (the WAKE:
	// a bumped generation rouses parked daemon agents from WaitForGeneration).
	genBefore, err := env.store.CurrentGeneration(context.Background(), testTenant)
	if err != nil {
		t.Fatalf("CurrentGeneration before rekey-all: %v", err)
	}

	// A node token must NOT drive the operator-only /rekey-all -> 403.
	if status := doJSON(t, http.MethodPost, env.opURL("rekey-all"), node1Token, struct{}{}, nil); status != http.StatusForbidden {
		t.Fatalf("rekey-all with node token: status %d, want 403", status)
	}

	// Operator requests a fleet-wide rekey: both APPROVED nodes are flagged; the pending
	// node is skipped, so requested == the approved count (2), not 3.
	var rekeyAll rekeyAllResponseJSON
	if status := doJSON(t, http.MethodPost, env.opURL("rekey-all"), testOperatorToken, struct{}{}, &rekeyAll); status != http.StatusOK {
		t.Fatalf("rekey-all: status %d, want 200", status)
	}
	if rekeyAll.Requested != 2 {
		t.Fatalf("rekey-all requested = %d, want 2 (both approved nodes; pending node skipped)", rekeyAll.Requested)
	}

	// The WAKE: /rekey-all bumped the generation so parked daemon agents wake (BLOCKER-1).
	genAfter, err := env.store.CurrentGeneration(context.Background(), testTenant)
	if err != nil {
		t.Fatalf("CurrentGeneration after rekey-all: %v", err)
	}
	if genAfter != genBefore+1 {
		t.Fatalf("CurrentGeneration after rekey-all = %d, want %d (rekey-all must bump the generation to wake parked agents)", genAfter, genBefore+1)
	}

	// The pending node was NOT flagged (skip-path): its rekey_requested stays false.
	pending, err := env.store.GetNode(context.Background(), testTenant, "node-pending")
	if err != nil {
		t.Fatalf("GetNode(node-pending) after rekey-all: %v", err)
	}
	if pending.RekeyRequested {
		t.Fatalf("node-pending rekey_requested = true after rekey-all, want false (rekey-all must skip non-approved nodes)")
	}

	// GET /nodes shows rekey_requested=true for both APPROVED nodes; the pending node
	// stays false (skip-path).
	var nodes []nodeJSON
	if status := doJSON(t, http.MethodGet, env.opURL("nodes"), testOperatorToken, nil, &nodes); status != http.StatusOK {
		t.Fatalf("nodes after rekey-all: status %d, want 200", status)
	}
	for _, n := range nodes {
		switch n.NodeID {
		case "node-pending":
			if n.RekeyRequested {
				t.Fatalf("node-pending rekey_requested = true after rekey-all, want false (non-approved nodes are skipped)")
			}
		default:
			if !n.RekeyRequested {
				t.Fatalf("node %s rekey_requested = false after rekey-all, want true", n.NodeID)
			}
		}
	}

	// GET /config (agent) shows rekey_requested=true for the caller node.
	var cfg configResponseJSON
	if status := doJSON(t, http.MethodGet, env.agentURL("config"), node1Token, nil, &cfg); status != http.StatusOK {
		t.Fatalf("config after rekey-all: status %d, want 200", status)
	}
	if !cfg.RekeyRequested {
		t.Fatalf("config rekey_requested = false after rekey-all, want true")
	}

	// An empty wg_public_key is a 400.
	if status := doJSON(t, http.MethodPost, env.agentURL("rekey"), node1Token, rekeyRequestJSON{WGPublicKey: ""}, nil); status != http.StatusBadRequest {
		t.Fatalf("rekey with empty key: status %d, want 400", status)
	}

	// The agent regenerates its WG key and re-registers the NEW public key.
	newPriv, err := wgtypes.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("wg GeneratePrivateKey: %v", err)
	}
	newPub := newPriv.PublicKey().String()
	if newPub == origPub {
		t.Fatalf("generated key equals original; cannot prove rotation")
	}
	var rekeyResp rekeyResponseJSON
	if status := doJSON(t, http.MethodPost, env.agentURL("rekey"), node1Token, rekeyRequestJSON{WGPublicKey: newPub}, &rekeyResp); status != http.StatusOK {
		t.Fatalf("rekey: status %d, want 200", status)
	}
	if !rekeyResp.OK {
		t.Fatalf("rekey response ok = false, want true")
	}

	// The registry now holds the NEW public key and the flag is cleared for node-1.
	after, err := env.store.GetNode(context.Background(), testTenant, "node-1")
	if err != nil {
		t.Fatalf("GetNode(node-1) after rekey: %v", err)
	}
	if after.WGPublicKey != newPub {
		t.Fatalf("node-1 WGPublicKey = %q, want the rotated key %q", after.WGPublicKey, newPub)
	}
	if after.RekeyRequested {
		t.Fatalf("node-1 RekeyRequested still set after /rekey, want cleared")
	}

	// GET /nodes confirms node-1 cleared its flag (and still reports a key on file),
	// while node-2 — which never re-registered — is still flagged.
	if status := doJSON(t, http.MethodGet, env.opURL("nodes"), testOperatorToken, nil, &nodes); status != http.StatusOK {
		t.Fatalf("nodes after rekey: status %d, want 200", status)
	}
	for _, n := range nodes {
		switch n.NodeID {
		case "node-1":
			if n.RekeyRequested {
				t.Fatalf("node-1 rekey_requested = true after /rekey, want false")
			}
			if !n.HasWGPublicKey {
				t.Fatalf("node-1 reports no WG public key after /rekey")
			}
		case "node-2":
			if !n.RekeyRequested {
				t.Fatalf("node-2 rekey_requested = false; only node-1 re-registered")
			}
		}
	}

	// node-2's bearer token is unaffected by node-1's rekey; it can still re-register.
	// node-2 rotates to its OWN fresh key — reusing node-1's just-registered key would
	// (correctly) be refused by the WG-pubkey dedupe (plan-6: one approved key ↔ one id).
	node2Priv, err := wgtypes.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate node-2 rekey key: %v", err)
	}
	if status := doJSON(t, http.MethodPost, env.agentURL("rekey"), node2Token, rekeyRequestJSON{WGPublicKey: node2Priv.PublicKey().String()}, nil); status != http.StatusOK {
		t.Fatalf("node-2 rekey: status %d, want 200", status)
	}

	// GET /audit records the flow: a fleet-wide rekey-request (actor operator:*, empty
	// node_id) from /rekey-all, and a per-node rekey (actor agent:node-1, node_id
	// node-1) from node-1's /rekey re-registration — with the chain verified intact.
	var audit struct {
		Entries []struct {
			Actor  string `json:"actor"`
			Action string `json:"action"`
			NodeID string `json:"node_id"`
		} `json:"entries"`
		Verified bool `json:"verified"`
	}
	if status := doJSON(t, http.MethodGet, env.opURL("audit"), testOperatorToken, nil, &audit); status != http.StatusOK {
		t.Fatalf("audit after rekey flow: status %d, want 200", status)
	}
	if !audit.Verified {
		t.Fatalf("audit verified = false after rekey flow, want true")
	}
	var sawRekeyRequest, sawRekey bool
	for _, e := range audit.Entries {
		switch e.Action {
		case "rekey-request":
			if !strings.HasPrefix(e.Actor, "operator:") {
				t.Fatalf("rekey-request actor = %q, want operator:* prefix", e.Actor)
			}
			sawRekeyRequest = true
		case "rekey":
			if e.Actor == "agent:node-1" && e.NodeID == "node-1" {
				sawRekey = true
			}
		}
	}
	if !sawRekeyRequest {
		t.Fatalf("audit missing a rekey-request (actor operator:*) entry: %+v", audit.Entries)
	}
	if !sawRekey {
		t.Fatalf("audit missing a rekey entry (actor agent:node-1, node_id node-1): %+v", audit.Entries)
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

// TestControllerHTTP_CORS confirms operator routes answer a browser CORS preflight
// (OPTIONS, which carries no Authorization) with 204 + the headers the panel needs,
// and stamp Allow-Origin onto real responses, so a cross-origin panel can call them.
func TestControllerHTTP_CORS(t *testing.T) {
	env := newCtlTestEnv(t)

	// Preflight: OPTIONS must be answered before operatorAuth (it has no bearer token).
	req, err := http.NewRequest(http.MethodOptions, env.opURL("nodes"), nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.Header.Set("Origin", "http://panel.example")
	req.Header.Set("Access-Control-Request-Method", "GET")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("OPTIONS /nodes: %v", err)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("preflight status %d, want 204", resp.StatusCode)
	}
	if got := resp.Header.Get("Access-Control-Allow-Origin"); got != "*" {
		t.Errorf("preflight Allow-Origin = %q, want *", got)
	}
	if ah := resp.Header.Get("Access-Control-Allow-Headers"); !strings.Contains(ah, "Authorization") {
		t.Errorf("preflight Allow-Headers = %q, want to include Authorization", ah)
	}

	// A real authenticated GET also carries the CORS origin header.
	req2, _ := http.NewRequest(http.MethodGet, env.opURL("nodes"), nil)
	req2.Header.Set("Authorization", "Bearer "+testOperatorToken)
	resp2, err := http.DefaultClient.Do(req2)
	if err != nil {
		t.Fatalf("GET /nodes: %v", err)
	}
	io.Copy(io.Discard, resp2.Body)
	resp2.Body.Close()
	if resp2.StatusCode != http.StatusOK {
		t.Fatalf("GET /nodes status %d, want 200", resp2.StatusCode)
	}
	if got := resp2.Header.Get("Access-Control-Allow-Origin"); got != "*" {
		t.Errorf("GET Allow-Origin = %q, want *", got)
	}
}

// TestControllerHTTP_AuditWireShape locks the /audit JSON contract: entries must
// serialize in snake_case (the operator DTO) so the browser panel can read them. An
// enroll appends an "enroll" entry; GET /audit must return it with snake_case keys and
// verified=true. (Regression guard: controller.AuditEntry has no json tags, so without
// the DTO it would serialize PascalCase and the panel's audit table would render blank.)
func TestControllerHTTP_AuditWireShape(t *testing.T) {
	env := newCtlTestEnv(t)
	env.enrollNode(t, "node-1")

	var resp struct {
		Entries []struct {
			Seq       int64  `json:"seq"`
			Timestamp string `json:"timestamp"`
			Actor     string `json:"actor"`
			Action    string `json:"action"`
			NodeID    string `json:"node_id"`
		} `json:"entries"`
		Verified bool `json:"verified"`
	}
	if st := doJSON(t, http.MethodGet, env.opURL("audit"), testOperatorToken, nil, &resp); st != http.StatusOK {
		t.Fatalf("GET /audit status %d, want 200", st)
	}
	if !resp.Verified {
		t.Errorf("audit verified = false, want true")
	}
	found := false
	for _, e := range resp.Entries {
		if e.Action == "enroll" && e.NodeID == "node-1" {
			found = true
			if e.Timestamp == "" || e.Actor == "" {
				t.Errorf("enroll entry missing timestamp/actor (snake_case mapping broken): %+v", e)
			}
		}
	}
	if !found {
		t.Errorf("no snake_case enroll audit entry for node-1 in %+v", resp.Entries)
	}
}

// TestControllerHTTP_RevokeClearsRekey is a regression guard: revoking a node that was
// flagged for rekey must clear RekeyRequested, else the panel's "rotating" gate (which
// counts rekey_requested nodes) would stay stuck forever on a node that can never
// re-register.
func TestControllerHTTP_RevokeClearsRekey(t *testing.T) {
	env := newCtlTestEnv(t)
	env.enrollNode(t, "node-1")
	env.promoteSmallTopo(t)

	// Flag the fleet for rekey, then revoke node-1 before it re-registers.
	var ra struct {
		Requested int `json:"requested"`
	}
	if st := doJSON(t, http.MethodPost, env.opURL("rekey-all"), testOperatorToken, struct{}{}, &ra); st != http.StatusOK {
		t.Fatalf("rekey-all: status %d, want 200", st)
	}
	if st := doJSON(t, http.MethodPost, env.opURL("revoke"), testOperatorToken, map[string]string{"node_id": "node-1"}, nil); st != http.StatusOK {
		t.Fatalf("revoke: status %d, want 200", st)
	}

	var nodes []struct {
		NodeID         string `json:"node_id"`
		Status         string `json:"status"`
		RekeyRequested bool   `json:"rekey_requested"`
	}
	if st := doJSON(t, http.MethodGet, env.opURL("nodes"), testOperatorToken, nil, &nodes); st != http.StatusOK {
		t.Fatalf("nodes: status %d, want 200", st)
	}
	for _, n := range nodes {
		if n.NodeID == "node-1" {
			if n.Status != "revoked" {
				t.Errorf("node-1 status = %q, want revoked", n.Status)
			}
			if n.RekeyRequested {
				t.Errorf("node-1 rekey_requested still true after revoke (would stick the Deploy gate)")
			}
		}
	}
}
