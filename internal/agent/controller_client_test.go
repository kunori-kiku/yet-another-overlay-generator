package agent_test

// controller_client_test.go is the in-process end-to-end test for the agent's
// networked-controller client (plan-4.5: per-node bearer tokens + plain HTTP + two
// ports). It mirrors the server-side harness in internal/api/controller_http_test.go
// but drives the AGENT side: a real (PLAIN HTTP) httptest server runs the REAL
// api.NewControllerHandler over a MemStore, with an operator bearer token, and the
// agent.ControllerClient enrolls, polls, fetches, and reports against it.
//
// The mTLS model is gone: there is no TLS/CA/cert here. /enroll is unauthenticated
// (gated by the single-use enrollment token); every other call presents the per-node
// bearer token the enroll response minted. The agent serves agent routes on one mux
// (RegisterAgentRoutes) and the operator drives stage/promote on the other
// (RegisterOperatorRoutes) over the operator bearer token.
//
// It covers:
//
//	(1) Enroll over a TOKEN-LESS agent client (the /enroll shape): the response
//	    carries a per-node bearer api_token.
//	(2) The OPERATOR (operator bearer token) stages + promotes a small topology.
//	(3) The agent's bearer ControllerClient.Poll(0) returns the new generation; Fetch
//	    returns the bundle; agent.VerifyBundle passes over it (unsigned in CI, so
//	    PinnedPubPEM=nil); Report updates the registry (asserted via store.GetNode).
//	(4) A Fetch/Poll with a BAD or EMPTY node token -> 401 (agent up-front guard for
//	    empty; server 401 for a wrong token).
//
// The bundle apply (install.sh) is NOT executed: a unit test must not run a root
// script. Instead the test asserts the fetched+verified bundle is correct (the same
// gate agent.Run runs before apply). The agent client imports internal/api +
// internal/controller in THIS TEST ONLY; the production controller_client.go does not.

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/kunorikiku/yet-another-overlay-generator/internal/agent"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/api"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/controller"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/model"
	"golang.zx2c4.com/wireguard/wgctrl/wgtypes"
)

const testTenant = controller.TenantID("acme")

// operatorPlaintext is the operator bearer token the test mints; the handler is
// constructed with its hash (HashToken), and the operator presents the plaintext as
// "Authorization: Bearer <operatorPlaintext>" on operator routes.
const operatorPlaintext = "op-secret-token-for-tests"

// ctlEnv bundles the in-process controller server and its dependencies so the agent
// test can reach the store directly (e.g. to assert GetNode and to mint the
// enrollment tokens the operator side would mint out-of-band).
type ctlEnv struct {
	agentSrv *httptest.Server
	opSrv    *httptest.Server
	store    controller.Store
}

// newCtlEnv stands up the controller over two PLAIN HTTP httptest servers backed by a
// single MemStore: one carrying the agent routes (RegisterAgentRoutes) and one the
// operator routes (RegisterOperatorRoutes). This mirrors the production two-port split
// (agent port vs operator/panel port) while keeping both reachable from the test.
func newCtlEnv(t *testing.T) *ctlEnv {
	t.Helper()

	store := controller.NewMemStore()
	ch := api.NewControllerHandler(store, testTenant, controller.HashToken(operatorPlaintext), api.DefaultOperatorName)

	agentMux := http.NewServeMux()
	ch.RegisterAgentRoutes(agentMux)
	agentSrv := httptest.NewServer(agentMux)
	t.Cleanup(agentSrv.Close)

	opMux := http.NewServeMux()
	ch.RegisterOperatorRoutes(opMux)
	opSrv := httptest.NewServer(opMux)
	t.Cleanup(opSrv.Close)

	return &ctlEnv{agentSrv: agentSrv, opSrv: opSrv, store: store}
}

// mintToken mints + persists a single-use enrollment token for nodeID (the operator
// side of the ceremony) and returns the plaintext the node presents to /enroll. It
// goes straight to the store, the same effect as the operator's /enrollment-token
// route, but avoids depending on that route's response shape for the happy path.
func (e *ctlEnv) mintToken(t *testing.T, nodeID string) string {
	t.Helper()
	plaintext, tok := controller.NewEnrollmentToken(nodeID, time.Hour, time.Now())
	if err := e.store.CreateEnrollmentToken(context.Background(), testTenant, tok); err != nil {
		t.Fatalf("CreateEnrollmentToken(%s): %v", nodeID, err)
	}
	return plaintext
}

// smallTopo is a minimal router+peer topology; both nodes enroll so the whole graph
// compiles (mirrors the controller HTTP test's fixture).
func smallTopo() *model.Topology {
	return &model.Topology{
		Project: model.Project{ID: "agent-ctl-001", Name: "Agent Controller Client Test"},
		Domains: []model.Domain{{
			ID: "domain-1", Name: "net", CIDR: "10.61.0.0/24",
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

// doOperator performs an operator request (bearer = operatorPlaintext) with a raw body
// and returns the status code; used to drive update-topology/stage. The agent client
// never calls these — they are the controller's operator-side wiring.
func doOperator(t *testing.T, method, url string, body []byte) int {
	t.Helper()
	var rdr io.Reader
	if body != nil {
		rdr = bytes.NewReader(body)
	}
	req, err := http.NewRequest(method, url, rdr)
	if err != nil {
		t.Fatalf("NewRequest %s %s: %v", method, url, err)
	}
	req.Header.Set("Authorization", "Bearer "+operatorPlaintext)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, url, err)
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)
	return resp.StatusCode
}

// doOperatorJSON performs an operator request (bearer = operatorPlaintext) and returns
// the response body, failing the test on any non-200 status. It is used where the test
// needs the operator route's response (e.g. /rekey-all's {requested}), unlike doOperator
// which returns only the status code.
func (e *ctlEnv) doOperatorJSON(t *testing.T, method, url string, body []byte) []byte {
	t.Helper()
	var rdr io.Reader
	if body != nil {
		rdr = bytes.NewReader(body)
	}
	req, err := http.NewRequest(method, url, rdr)
	if err != nil {
		t.Fatalf("NewRequest %s %s: %v", method, url, err)
	}
	req.Header.Set("Authorization", "Bearer "+operatorPlaintext)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, url, err)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("%s %s: status %d, want 200: %s", method, url, resp.StatusCode, string(respBody))
	}
	return respBody
}

// stageAndPromote drives the operator side over the operator HTTP routes:
// update-topology -> stage -> promote, and returns the promoted generation. The agent
// never performs these; they are how a new configuration becomes available to poll.
func (e *ctlEnv) stageAndPromote(t *testing.T) int64 {
	t.Helper()
	base := e.opSrv.URL + "/api/v1/controller/"

	topoJSON, err := json.Marshal(smallTopo())
	if err != nil {
		t.Fatalf("marshal topology: %v", err)
	}
	if status := doOperator(t, http.MethodPost, base+"update-topology", topoJSON); status != http.StatusOK {
		t.Fatalf("update-topology: status %d, want 200", status)
	}
	if status := doOperator(t, http.MethodPost, base+"stage", []byte("{}")); status != http.StatusOK {
		t.Fatalf("stage: status %d, want 200", status)
	}

	req, err := http.NewRequest(http.MethodPost, base+"promote", bytes.NewReader([]byte("{}")))
	if err != nil {
		t.Fatalf("promote NewRequest: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+operatorPlaintext)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("promote: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("promote: status %d, want 200", resp.StatusCode)
	}
	var promote struct {
		Generation int64 `json:"generation"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&promote); err != nil {
		t.Fatalf("decode promote response: %v", err)
	}
	if promote.Generation < 1 {
		t.Fatalf("promote generation %d, want >= 1", promote.Generation)
	}
	return promote.Generation
}

// enrollViaAgent runs the agent's OWN Enroll against the live controller over a
// token-less agent client (the shape /enroll requires), minting the enrollment token
// operator-side first. It returns the per-node bearer token the enroll response minted.
func (e *ctlEnv) enrollViaAgent(t *testing.T, nodeID string) string {
	t.Helper()
	token := e.mintToken(t, nodeID)

	wgPriv, err := wgtypes.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("wg GeneratePrivateKey: %v", err)
	}

	// Token-less agent client (no bearer) — the bootstrap shape for /enroll.
	client, err := agent.NewControllerClient(e.agentSrv.URL, "")
	if err != nil {
		t.Fatalf("NewControllerClient(token-less): %v", err)
	}
	res, err := client.Enroll(token, nodeID, wgPriv.PublicKey().String())
	if err != nil {
		t.Fatalf("Enroll(%s): %v", nodeID, err)
	}
	if res.APIToken == "" {
		t.Fatalf("Enroll(%s): empty api token", nodeID)
	}
	return res.APIToken
}

// TestControllerClient_EnrollPollFetchVerifyReport is the full agent-side happy path:
// enroll -> (operator stage+promote) -> poll -> fetch -> verify -> report.
func TestControllerClient_EnrollPollFetchVerifyReport(t *testing.T) {
	env := newCtlEnv(t)

	// (1) Both nodes enroll via the agent's token-less Enroll. node-1's token backs the
	// agent's later authed calls; node-2 enrolls so the whole graph compiles.
	node1Token := env.enrollViaAgent(t, "node-1")
	_ = env.enrollViaAgent(t, "node-2")

	// (2) Operator stages + promotes the topology, making a generation available.
	gen := env.stageAndPromote(t)

	// (3) The agent's bearer client polls, fetches, verifies, and reports.
	agentClient, err := agent.NewControllerClient(env.agentSrv.URL, node1Token)
	if err != nil {
		t.Fatalf("NewControllerClient(bearer): %v", err)
	}

	// Poll(0) -> the promoted generation immediately (current > 0).
	gotGen, changed, err := agentClient.Poll(0)
	if err != nil {
		t.Fatalf("Poll(0): %v", err)
	}
	if !changed {
		t.Fatalf("Poll(0): changed=false, want true (a generation is promoted)")
	}
	if gotGen != gen {
		t.Fatalf("Poll(0): generation %d, want %d", gotGen, gen)
	}

	// Fetch -> the node-1 bundle (identity from the token; the arg is diagnostic only).
	files, err := agentClient.Fetch("node-1")
	if err != nil {
		t.Fatalf("Fetch(node-1): %v", err)
	}
	if len(files) == 0 {
		t.Fatalf("Fetch(node-1): empty bundle")
	}
	if _, ok := files["install.sh"]; !ok {
		t.Fatalf("Fetch(node-1): bundle missing install.sh (keys: %v)", keysOf(files))
	}
	if _, ok := files["checksums.sha256"]; !ok {
		t.Fatalf("Fetch(node-1): bundle missing checksums.sha256 (keys: %v)", keysOf(files))
	}

	// Fetch records the bundle's own generation; LastFetchedGeneration exposes it so the
	// daemon loop can advance its resume cursor to the generation actually fetched/applied
	// (not merely the one polled), closing the poll->fetch race. It must equal the promoted
	// generation here (no concurrent promote raced in this single-threaded test).
	if got := agentClient.LastFetchedGeneration(); got != gen {
		t.Fatalf("LastFetchedGeneration after Fetch = %d, want %d (the fetched bundle's generation)", got, gen)
	}

	// VerifyBundle passes over the fetched bundle. CI bundles are unsigned
	// (YAOG_BUNDLE_SIGNING_KEY unset), so pin nothing. This is the SAME gate agent.Run
	// runs before apply — asserting it passes is the unit-test stand-in for "would apply"
	// (we do NOT execute the root install.sh here).
	vr, err := agent.VerifyBundle(files, nil)
	if err != nil {
		t.Fatalf("VerifyBundle over fetched bundle: %v", err)
	}
	if vr.FileCount == 0 {
		t.Fatalf("VerifyBundle: FileCount=0, want > 0")
	}

	// Report -> the registry reflects the applied generation. The Fetch above recorded
	// the bundle's own generation; an "ok" State makes Report send THAT as the applied
	// generation (the same path agent.Run's auto-report drives). Assert via GetNode.
	statePayload, err := json.Marshal(agent.State{
		NodeID:       "node-1",
		LastChecksum: "deadbeef",
		LastResult:   "ok",
		Health:       "applied",
	})
	if err != nil {
		t.Fatalf("marshal state payload: %v", err)
	}
	if err := agentClient.Report("node-1", statePayload); err != nil {
		t.Fatalf("Report(node-1): %v", err)
	}
	node, err := env.store.GetNode(context.Background(), testTenant, "node-1")
	if err != nil {
		t.Fatalf("GetNode(node-1): %v", err)
	}
	if node.AppliedGeneration != gen {
		t.Fatalf("GetNode applied generation %d, want %d", node.AppliedGeneration, gen)
	}
	if node.LastChecksum != "deadbeef" {
		t.Fatalf("GetNode checksum %q, want deadbeef", node.LastChecksum)
	}
}

// TestControllerClient_BadOrEmptyToken confirms that authed calls fail without a valid
// per-node bearer token: an EMPTY token is rejected by the agent's own up-front guard
// (it cannot present a credential), and a WRONG token is rejected by the server with a
// 401 at the auth chokepoint. Both paths are checked.
func TestControllerClient_BadOrEmptyToken(t *testing.T) {
	env := newCtlEnv(t)
	// A real node must exist so a 401 is a token rejection, not an empty-fleet artifact.
	_ = env.enrollViaAgent(t, "node-1")

	// Empty token: Fetch/Poll fail up front (no credential to present).
	empty, err := agent.NewControllerClient(env.agentSrv.URL, "")
	if err != nil {
		t.Fatalf("NewControllerClient(empty): %v", err)
	}
	if _, err := empty.Fetch("node-1"); err == nil {
		t.Fatalf("empty-token Fetch: got nil error, want failure (no bearer token)")
	}
	if _, _, err := empty.Poll(0); err == nil {
		t.Fatalf("empty-token Poll: got nil error, want failure (no bearer token)")
	}

	// Wrong token: the server rejects it at the auth chokepoint. The agent surfaces the
	// 401 as an error from Fetch/Poll.
	bad, err := agent.NewControllerClient(env.agentSrv.URL, "definitely-not-a-real-token")
	if err != nil {
		t.Fatalf("NewControllerClient(bad): %v", err)
	}
	if _, err := bad.Fetch("node-1"); err == nil {
		t.Fatalf("bad-token Fetch: got nil error, want a 401 failure")
	}
	if !contains(errStr(bad.Poll(0)), "401") {
		t.Fatalf("bad-token Poll: error %q, want it to mention 401", errStr(bad.Poll(0)))
	}

	// Server side: a raw plain-HTTP GET with a bogus bearer on /config -> 401. This
	// confirms the rejection is enforced by the auth chokepoint, not only by the agent's
	// own guard.
	req, err := http.NewRequest(http.MethodGet, env.agentSrv.URL+"/api/v1/controller/config", nil)
	if err != nil {
		t.Fatalf("NewRequest /config: %v", err)
	}
	req.Header.Set("Authorization", "Bearer definitely-not-a-real-token")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("raw bad-token GET /config: %v", err)
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("raw bad-token /config: status %d, want 401", resp.StatusCode)
	}
}

// TestControllerClient_RekeyFlow is the agent side of fleet-wide key rotation (plan-4.6):
// enroll -> (operator stage+promote so /config returns a bundle) -> operator POST
// /rekey-all flags the node -> the agent's Fetch surfaces rekey_requested via
// LastRekeyRequested() -> the agent rotates its key and re-registers the NEW public key
// via Rekey, which clears the flag (asserted via the store). It drives the REAL controller
// handler, so the agent wire tags (rekey_requested, wg_public_key) are verified end to end.
func TestControllerClient_RekeyFlow(t *testing.T) {
	env := newCtlEnv(t)

	// Both nodes enroll (enrolled == NodeApproved, which /rekey-all flags); the whole
	// graph then compiles on promote.
	node1Token := env.enrollViaAgent(t, "node-1")
	_ = env.enrollViaAgent(t, "node-2")

	// A generation must be promoted so /config returns 200 (it 404s before the first
	// promote) and the agent can read the rekey flag off the envelope.
	env.stageAndPromote(t)

	agentClient, err := agent.NewControllerClient(env.agentSrv.URL, node1Token)
	if err != nil {
		t.Fatalf("NewControllerClient(bearer): %v", err)
	}

	// Before the operator rolls keys, a Fetch must report rekey NOT requested.
	if _, err := agentClient.Fetch("node-1"); err != nil {
		t.Fatalf("pre-rekey Fetch: %v", err)
	}
	if agentClient.LastRekeyRequested() {
		t.Fatalf("LastRekeyRequested before /rekey-all = true, want false")
	}

	// Operator rolls keys fleet-wide; the response counts the flagged (approved) nodes.
	body := env.doOperatorJSON(t, http.MethodPost, env.opSrv.URL+"/api/v1/controller/rekey-all", []byte("{}"))
	var rekeyAll struct {
		Requested int `json:"requested"`
	}
	if err := json.Unmarshal(body, &rekeyAll); err != nil {
		t.Fatalf("decode rekey-all response: %v", err)
	}
	if rekeyAll.Requested < 1 {
		t.Fatalf("rekey-all requested=%d, want >= 1 (node-1 is approved)", rekeyAll.Requested)
	}

	// The agent learns of the request on its next Fetch (the /config envelope now carries
	// rekey_requested=true).
	if _, err := agentClient.Fetch("node-1"); err != nil {
		t.Fatalf("post-rekey Fetch: %v", err)
	}
	if !agentClient.LastRekeyRequested() {
		t.Fatalf("LastRekeyRequested after /rekey-all = false, want true")
	}

	// Capture the node's current public key so we can assert the rotation changed it.
	before, err := env.store.GetNode(context.Background(), testTenant, "node-1")
	if err != nil {
		t.Fatalf("GetNode(node-1) before rekey: %v", err)
	}

	// The agent rotates its LOCAL key and registers the NEW public key. We mint a fresh
	// key here to stand in for RegenerateKey's output (the keygen path is unit-tested
	// separately; this test exercises the client/server rekey wire).
	newPriv, err := wgtypes.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("wg GeneratePrivateKey: %v", err)
	}
	newPub := newPriv.PublicKey().String()
	if newPub == before.WGPublicKey {
		t.Fatalf("test setup: new key equals old key (no rotation to assert)")
	}
	if err := agentClient.Rekey(newPub); err != nil {
		t.Fatalf("Rekey(newPub): %v", err)
	}

	// The store now holds the NEW public key and the rekey flag is cleared.
	after, err := env.store.GetNode(context.Background(), testTenant, "node-1")
	if err != nil {
		t.Fatalf("GetNode(node-1) after rekey: %v", err)
	}
	if after.WGPublicKey != newPub {
		t.Fatalf("after rekey: stored pubkey %q, want %q", after.WGPublicKey, newPub)
	}
	if after.RekeyRequested {
		t.Fatalf("after rekey: RekeyRequested still true, want cleared")
	}

	// A follow-up Fetch confirms the cleared flag is reflected on the wire too.
	if _, err := agentClient.Fetch("node-1"); err != nil {
		t.Fatalf("post-clear Fetch: %v", err)
	}
	if agentClient.LastRekeyRequested() {
		t.Fatalf("LastRekeyRequested after rekey cleared = true, want false")
	}
}

// TestControllerClient_RekeyRejectsEmptyPubkey confirms the agent's own up-front guard:
// Rekey with a blank public key fails before any request is issued (the server would
// otherwise 400). This keeps a misconfigured agent from clobbering its registered key
// with an empty value.
func TestControllerClient_RekeyRejectsEmptyPubkey(t *testing.T) {
	c, err := agent.NewControllerClient("http://example.invalid", "tok")
	if err != nil {
		t.Fatalf("NewControllerClient: %v", err)
	}
	if err := c.Rekey("   "); err == nil {
		t.Fatalf("Rekey(empty): got nil error, want failure (no public key)")
	}
}

// TestControllerClient_LastRekeyRequestedFalseBeforeFetch confirms the getter the daemon
// loop branches on is false on a freshly-constructed client (before any Fetch), so the
// rekey branch never fires off a stale signal.
func TestControllerClient_LastRekeyRequestedFalseBeforeFetch(t *testing.T) {
	c, err := agent.NewControllerClient("http://example.invalid", "tok")
	if err != nil {
		t.Fatalf("NewControllerClient: %v", err)
	}
	if c.LastRekeyRequested() {
		t.Fatalf("LastRekeyRequested before Fetch = true, want false")
	}
}

// TestControllerClient_LastFetchedGenerationZeroBeforeFetch confirms the getter the
// daemon loop reads is zero on a freshly-constructed client (before any Fetch), so the
// resume-cursor advance never picks up a stale generation from a prior client instance.
func TestControllerClient_LastFetchedGenerationZeroBeforeFetch(t *testing.T) {
	c, err := agent.NewControllerClient("http://example.invalid", "tok")
	if err != nil {
		t.Fatalf("NewControllerClient: %v", err)
	}
	if got := c.LastFetchedGeneration(); got != 0 {
		t.Fatalf("LastFetchedGeneration before Fetch = %d, want 0", got)
	}
}

// keysOf returns a map's keys for diagnostic messages.
func keysOf(m map[string][]byte) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// contains reports whether s contains substr (kept local to avoid a strings import
// solely for one assertion).
func contains(s, substr string) bool {
	return bytes.Contains([]byte(s), []byte(substr))
}

// errStr renders a (gen, changed, err) Poll result's error as a string ("" when nil),
// so an assertion can inspect the message without juggling the other return values.
func errStr(_ int64, _ bool, err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
