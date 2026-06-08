package agent_test

// controller_client_test.go is the in-process end-to-end test for the agent's
// networked-controller client (plan: agent controller client). It mirrors the
// server-side harness in internal/api/controller_http_test.go but drives the AGENT
// side: a real TLS 1.3 + mTLS httptest server (api.NewControllerHandler over a
// MemStore + ephemeral dev CA) is stood up, and the agent.ControllerClient enrolls,
// polls, fetches, and reports against it.
//
// It covers:
//
//	(1) Enroll over a CERTLESS agent client: the returned CACertPEM byte-matches the
//	    pinned CA, and a client cert is issued.
//	(2) An OPERATOR (api operator cert) stages + promotes a small topology.
//	(3) The agent's mTLS ControllerClient.Poll(0) returns the new generation; Fetch
//	    returns the bundle; agent.VerifyBundle passes over it (unsigned in CI, so
//	    PinnedPubPEM=nil); Report updates the registry (asserted via store.GetNode).
//	(4) Enroll with a DIFFERENT CA pinned -> "controller CA mismatch" refusal.
//	(5) A Fetch/Poll WITHOUT the mTLS cert -> error (401 / handshake refusal).
//
// The bundle apply (install.sh) is NOT executed: a unit test must not run a root
// script. Instead the test asserts the fetched+verified bundle is correct (the same
// gate agent.Run runs before apply). The agent client imports internal/api +
// internal/controller in THIS TEST ONLY; the production controller_client.go does not.

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
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

// ctlEnv bundles the in-process controller server and its dependencies so the agent
// test can reach the store + CA directly (e.g. to assert GetNode and to mint the
// operator cert / enrollment tokens the operator side would mint out-of-band).
type ctlEnv struct {
	srv   *httptest.Server
	store controller.Store
	ca    *controller.DevCA
}

// newCtlEnv stands up the controller over a real TLS 1.3 + mTLS httptest server
// backed by a MemStore and an ephemeral dev CA — the exact production TLS config
// (DevCA.ServerTLSConfig), so the test exercises VerifyClientCertIfGiven for real.
func newCtlEnv(t *testing.T) *ctlEnv {
	t.Helper()
	now := time.Now()

	store := controller.NewMemStore()
	ca, err := controller.NewDevCA(testTenant, now, 24*time.Hour, 12*time.Hour)
	if err != nil {
		t.Fatalf("NewDevCA: %v", err)
	}
	serverCert, err := ca.IssueServerCert("127.0.0.1", now)
	if err != nil {
		t.Fatalf("IssueServerCert: %v", err)
	}

	ch := api.NewControllerHandler(store, ca, testTenant, api.DefaultOperatorName)
	mux := http.NewServeMux()
	ch.Routes(mux)

	srv := httptest.NewUnstartedServer(mux)
	srv.TLS = ca.ServerTLSConfig(serverCert)
	srv.StartTLS()
	t.Cleanup(srv.Close)

	return &ctlEnv{srv: srv, store: store, ca: ca}
}

// genMTLSKeyAndCSR generates a fresh Ed25519 mTLS keypair and a self-signed CSR with
// CN cn (the controller checks the CSR self-signature as proof-of-possession).
func genMTLSKeyAndCSR(t *testing.T, cn string) ([]byte, ed25519.PrivateKey) {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("ed25519.GenerateKey: %v", err)
	}
	csrDER, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{
		Subject: pkix.Name{CommonName: cn},
	}, priv)
	if err != nil {
		t.Fatalf("CreateCertificateRequest(%s): %v", cn, err)
	}
	return csrDER, priv
}

// tlsCertFromPEM assembles a tls.Certificate from an issued client-cert PEM and the
// Ed25519 private key the node generated for its CSR — what an enrolled node / the
// operator presents on subsequent mTLS calls.
func tlsCertFromPEM(t *testing.T, certPEM []byte, priv ed25519.PrivateKey) tls.Certificate {
	t.Helper()
	keyDER, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		t.Fatalf("MarshalPKCS8PrivateKey: %v", err)
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})
	cert, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		t.Fatalf("X509KeyPair: %v", err)
	}
	return cert
}

// operatorCert mints an operator client cert (CN "<tenant>:operator") via the same
// CSR -> IssueClientCert path a node uses, so the operator identity is a real cert
// chaining to the dev CA. The operator drives stage/promote out-of-band of the agent.
func (e *ctlEnv) operatorCert(t *testing.T) tls.Certificate {
	t.Helper()
	cn := string(testTenant) + ":" + api.DefaultOperatorName
	csrDER, priv := genMTLSKeyAndCSR(t, cn)
	certPEM, _, err := e.ca.IssueClientCert(csrDER, api.DefaultOperatorName, time.Now())
	if err != nil {
		t.Fatalf("IssueClientCert(operator): %v", err)
	}
	return tlsCertFromPEM(t, certPEM, priv)
}

// operatorClient returns an mTLS http.Client presenting the operator cert and
// trusting the dev CA — used to drive update-topology/stage/promote.
func (e *ctlEnv) operatorClient(t *testing.T) *http.Client {
	t.Helper()
	return &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{
		RootCAs:      e.ca.CACertPool(),
		Certificates: []tls.Certificate{e.operatorCert(t)},
		MinVersion:   tls.VersionTLS13,
	}}}
}

// mintToken mints + persists a single-use enrollment token for nodeID (the operator
// side of the ceremony) and returns the plaintext the node presents to /enroll.
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

// doRaw performs an mTLS request with a raw body and returns the status code; used to
// drive the operator's update-topology/stage/promote (the agent client never calls
// these — they are the controller's operator-side wiring).
func doRaw(t *testing.T, client *http.Client, method, url string, body []byte) int {
	t.Helper()
	var rdr io.Reader
	if body != nil {
		rdr = bytes.NewReader(body)
	}
	req, err := http.NewRequest(method, url, rdr)
	if err != nil {
		t.Fatalf("NewRequest %s %s: %v", method, url, err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, url, err)
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)
	return resp.StatusCode
}

// stageAndPromote drives the operator side: update-topology -> stage -> promote, and
// returns the promoted generation. The agent never performs these; they are how a
// new configuration becomes available for the agent to poll/fetch.
func (e *ctlEnv) stageAndPromote(t *testing.T) int64 {
	t.Helper()
	op := e.operatorClient(t)
	base := e.srv.URL + "/api/v1/controller/"

	topoJSON, err := json.Marshal(smallTopo())
	if err != nil {
		t.Fatalf("marshal topology: %v", err)
	}
	if status := doRaw(t, op, http.MethodPost, base+"update-topology", topoJSON); status != http.StatusOK {
		t.Fatalf("update-topology: status %d, want 200", status)
	}
	if status := doRaw(t, op, http.MethodPost, base+"stage", []byte("{}")); status != http.StatusOK {
		t.Fatalf("stage: status %d, want 200", status)
	}

	req, err := http.NewRequest(http.MethodPost, base+"promote", bytes.NewReader([]byte("{}")))
	if err != nil {
		t.Fatalf("promote NewRequest: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := op.Do(req)
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
// certless agent client (the shape /enroll requires), mints the token operator-side
// first, and assembles the issued mTLS cert. It asserts the returned CA byte-matches
// the pinned CA. It returns the assembled mTLS tls.Certificate and the EnrollResult.
func (e *ctlEnv) enrollViaAgent(t *testing.T, nodeID string) (tls.Certificate, *agent.EnrollResult, ed25519.PrivateKey) {
	t.Helper()
	caPEM := e.ca.CACertPEM()
	token := e.mintToken(t, nodeID)

	// Node side: generate the mTLS key + CSR (CN "<tenant>:<node>") and a WG pubkey.
	cn := string(testTenant) + ":" + nodeID
	csrDER, priv := genMTLSKeyAndCSR(t, cn)
	wgPriv, err := wgtypes.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("wg GeneratePrivateKey: %v", err)
	}

	// Certless agent client (no cert) — the bootstrap-trust shape for /enroll.
	client, err := agent.NewControllerClient(e.srv.URL, caPEM, nil)
	if err != nil {
		t.Fatalf("NewControllerClient(certless): %v", err)
	}
	res, err := client.Enroll(token, nodeID, csrDER, wgPriv.PublicKey().String())
	if err != nil {
		t.Fatalf("Enroll(%s): %v", nodeID, err)
	}
	if len(res.ClientCertPEM) == 0 || res.Fingerprint == "" {
		t.Fatalf("Enroll(%s): empty cert/fingerprint", nodeID)
	}
	// Bootstrap trust: the returned CA must be the pinned CA, byte for byte.
	if !bytes.Equal(res.CACertPEM, caPEM) {
		t.Fatalf("Enroll(%s): returned CA PEM does not equal the pinned CA PEM", nodeID)
	}

	return tlsCertFromPEM(t, res.ClientCertPEM, priv), res, priv
}

// TestControllerClient_EnrollPollFetchVerifyReport is the full agent-side happy path:
// enroll -> (operator stage+promote) -> poll -> fetch -> verify -> report.
func TestControllerClient_EnrollPollFetchVerifyReport(t *testing.T) {
	env := newCtlEnv(t)

	// (1) Both nodes enroll via the agent's certless Enroll. node-1's cert backs the
	// agent's later mTLS calls; node-2 enrolls so the whole graph compiles.
	node1Cert, _, _ := env.enrollViaAgent(t, "node-1")
	_, _, _ = env.enrollViaAgent(t, "node-2")

	// (2) Operator stages + promotes the topology, making a generation available.
	gen := env.stageAndPromote(t)

	// (3) The agent's mTLS client polls, fetches, verifies, and reports.
	agentClient, err := agent.NewControllerClient(env.srv.URL, env.ca.CACertPEM(), &node1Cert)
	if err != nil {
		t.Fatalf("NewControllerClient(mTLS): %v", err)
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

	// Fetch -> the node-1 bundle (identity from the cert; the arg is diagnostic only).
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

	// Report -> the registry reflects the applied generation. Use SetPendingGeneration
	// + the Reporter shape (the same path agent.Run's auto-report drives), then assert
	// via the store's GetNode.
	agentClient.SetPendingGeneration(gen)
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
	// The server-side 204 long-poll-timeout branch (and the agent's no-advance mapping)
	// is covered by TestControllerClient_PollTimeout204 without the production ~55s wait.
}

// TestControllerClient_EnrollCAMismatch confirms the bootstrap-trust gate: when the
// agent pins a DIFFERENT CA than the controller actually uses, Enroll refuses with a
// "controller CA mismatch" error rather than trusting the response's CA.
//
// To reach the body-level mismatch check (not just a TLS handshake failure), the
// agent must still complete the TLS handshake — so it pins a CA that the SERVER's
// cert chains to. We therefore stand the controller up with a CA whose cert is the
// "pinned" one for the handshake, but make /enroll return a DIFFERENT CA in the body.
// The simplest faithful construction: point the agent at a server that serves a cert
// the agent trusts, while the enroll handler returns another CA. We approximate this
// by pinning the real CA for the transport but asserting the refusal via a stub server
// whose /enroll returns a foreign CA PEM in the body.
func TestControllerClient_EnrollCAMismatch(t *testing.T) {
	// Real CA: used to issue the httptest server's TLS cert AND pinned by the agent, so
	// the TLS handshake succeeds and execution reaches the body-level CA check.
	now := time.Now()
	realCA, err := controller.NewDevCA(testTenant, now, time.Hour, time.Hour)
	if err != nil {
		t.Fatalf("real NewDevCA: %v", err)
	}
	serverCert, err := realCA.IssueServerCert("127.0.0.1", now)
	if err != nil {
		t.Fatalf("IssueServerCert: %v", err)
	}
	// Foreign CA: returned in the /enroll BODY (ca_cert_pem) but never pinned. The agent
	// must reject it because it does not byte-match the pinned (real) CA.
	foreignCA, err := controller.NewDevCA(testTenant, now, time.Hour, time.Hour)
	if err != nil {
		t.Fatalf("foreign NewDevCA: %v", err)
	}

	// Precompute the foreign-signed enroll-response body OUTSIDE the handler. The body
	// carries a real (foreign-signed) client cert + the foreign CA, so only the CA check
	// — not a cert parse error — is what trips the agent's refusal. Building it here (not
	// inside the handler goroutine) keeps t.Fatalf on the test goroutine.
	csrDERStub, _ := genMTLSKeyAndCSR(t, string(testTenant)+":node-1")
	foreignCertPEM, foreignFP, err := foreignCA.IssueClientCert(csrDERStub, "node-1", time.Now())
	if err != nil {
		t.Fatalf("foreign IssueClientCert: %v", err)
	}
	respBody, err := json.Marshal(map[string]string{
		"client_cert_pem": string(foreignCertPEM),
		"ca_cert_pem":     string(foreignCA.CACertPEM()),
		"fingerprint":     foreignFP,
	})
	if err != nil {
		t.Fatalf("marshal stub enroll response: %v", err)
	}

	// Stub /enroll handler: returns the precomputed enroll response whose ca_cert_pem is
	// the FOREIGN CA (a real, parseable cert — just not the pinned one).
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/controller/enroll", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(respBody)
	})
	srv := httptest.NewUnstartedServer(mux)
	srv.TLS = realCA.ServerTLSConfig(serverCert)
	srv.StartTLS()
	t.Cleanup(srv.Close)

	// Agent pins the REAL CA (so TLS succeeds) but the body returns the FOREIGN CA.
	client, err := agent.NewControllerClient(srv.URL, realCA.CACertPEM(), nil)
	if err != nil {
		t.Fatalf("NewControllerClient: %v", err)
	}
	csrDER, _ := genMTLSKeyAndCSR(t, string(testTenant)+":node-1")
	_, err = client.Enroll("any-token", "node-1", csrDER, "wgpub")
	if err == nil {
		t.Fatalf("Enroll with mismatched body CA: got nil error, want a controller CA mismatch refusal")
	}
	if !contains(err.Error(), "CA mismatch") {
		t.Fatalf("Enroll error %q, want it to mention \"CA mismatch\"", err.Error())
	}
}

// TestControllerClient_NoCertRejected confirms that Fetch/Poll without an mTLS cert
// fail: the agent client refuses up front when constructed without a cert (it cannot
// authenticate), and the server's mTLS-gated routes also reject a certless caller
// (401) at the protocol level. Both paths are checked.
func TestControllerClient_NoCertRejected(t *testing.T) {
	env := newCtlEnv(t)

	// Certless agent client: Fetch/Poll must fail (no cert to present).
	certless, err := agent.NewControllerClient(env.srv.URL, env.ca.CACertPEM(), nil)
	if err != nil {
		t.Fatalf("NewControllerClient(certless): %v", err)
	}
	if _, err := certless.Fetch("node-1"); err == nil {
		t.Fatalf("certless Fetch: got nil error, want failure (no mTLS cert)")
	}
	if _, _, err := certless.Poll(0); err == nil {
		t.Fatalf("certless Poll: got nil error, want failure (no mTLS cert)")
	}

	// Server side: a raw certless HTTPS client (trusting the CA) on /config -> 401. This
	// confirms the rejection is enforced by the auth chokepoint, not only by the agent's
	// own up-front guard.
	rawCertless := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{
		RootCAs:    env.ca.CACertPool(),
		MinVersion: tls.VersionTLS13,
	}}}
	resp, err := rawCertless.Get(env.srv.URL + "/api/v1/controller/config")
	if err != nil {
		t.Fatalf("raw certless GET /config: %v", err)
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("raw certless /config: status %d, want 401", resp.StatusCode)
	}
}

// TestControllerClient_PollTimeout204 confirms the agent maps a server-side long-poll
// timeout (HTTP 204) onto the no-advance contract (after, false, nil) so the caller
// re-polls rather than treating it as a change or an error. It uses a tiny stub server
// that always returns 204, so the test does not wait the production ~55s poll deadline
// (which is unreachable from this external test package — pollDeadline is unexported).
func TestControllerClient_PollTimeout204(t *testing.T) {
	now := time.Now()
	ca, err := controller.NewDevCA(testTenant, now, time.Hour, time.Hour)
	if err != nil {
		t.Fatalf("NewDevCA: %v", err)
	}
	serverCert, err := ca.IssueServerCert("127.0.0.1", now)
	if err != nil {
		t.Fatalf("IssueServerCert: %v", err)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/controller/poll", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	srv := httptest.NewUnstartedServer(mux)
	srv.TLS = ca.ServerTLSConfig(serverCert)
	srv.StartTLS()
	t.Cleanup(srv.Close)

	// A client cert is required for Poll's up-front guard; issue a real one chaining to
	// the CA so the mTLS handshake also succeeds against the stub server.
	csrDER, priv := genMTLSKeyAndCSR(t, string(testTenant)+":node-1")
	certPEM, _, err := ca.IssueClientCert(csrDER, "node-1", now)
	if err != nil {
		t.Fatalf("IssueClientCert: %v", err)
	}
	cert := tlsCertFromPEM(t, certPEM, priv)

	client, err := agent.NewControllerClient(srv.URL, ca.CACertPEM(), &cert)
	if err != nil {
		t.Fatalf("NewControllerClient: %v", err)
	}
	gen, changed, err := client.Poll(7)
	if err != nil {
		t.Fatalf("Poll on 204: %v", err)
	}
	if changed {
		t.Fatalf("Poll on 204: changed=true, want false")
	}
	if gen != 7 {
		t.Fatalf("Poll on 204: gen=%d, want the unchanged cursor 7", gen)
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
