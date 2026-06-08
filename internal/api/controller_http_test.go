package api

// controller_http_test.go is the in-process integration test for the networked
// controller HTTP surface (plan-4.3b). It exercises the real TLS 1.3 + mTLS stack
// end-to-end with the ephemeral dev CA and a MemStore — no external process, no
// network beyond loopback — covering:
//
//	(1) ENROLL over a certless client (RootCAs trusts the dev CA, no client cert):
//	    operator pre-creates a single-use token, the node generates an Ed25519 mTLS
//	    key + CSR (CN "<tenant>:node-1"), POSTs /enroll, and gets back a client cert.
//	(2) an mTLS node client built from that issued cert.
//	(3) GET /config → 404 before any promote.
//	(4) an OPERATOR mTLS client (CN "<tenant>:operator", issued via IssueClientCert)
//	    drives /update-topology → /stage → /promote.
//	(5) the node client: GET /config → 200 with the bundle; /poll?after=0 → the new
//	    generation; a /poll already at the current generation (the handler's poll
//	    deadline is shrunk for the test) and no promote → 204; POST /report → ok, and
//	    GetNode shows the applied generation.
//	(6) AUTH: a certless client on /config → 401; the node cert on /stage → 403.
//
// Real x509/ed25519 throughout; stdlib only.

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
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

// ctlTestEnv bundles the in-process controller server and its dependencies so the
// individual phases can reach the store and CA directly (e.g. to assert GetNode).
type ctlTestEnv struct {
	srv   *httptest.Server
	store controller.Store
	ca    *controller.DevCA
}

// newCtlTestEnv stands up the controller over a real TLS 1.3 + mTLS httptest server
// backed by a MemStore and an ephemeral dev CA. The server's TLS config is the very
// config the production path builds (DevCA.ServerTLSConfig), so the test exercises
// VerifyClientCertIfGiven for real.
func newCtlTestEnv(t *testing.T) *ctlTestEnv {
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

	ch := NewControllerHandler(store, ca, testTenant, DefaultOperatorName)
	// Shrink the server-side /poll long-poll deadline so the timeout-204 path returns
	// promptly instead of waiting the production ~55s. The server (not the client) is
	// what produces the 204, so this is the right knob: a client-side context deadline
	// would instead surface as a transport error, never a 204 response.
	ch.pollDeadline = 250 * time.Millisecond
	mux := http.NewServeMux()
	ch.Routes(mux)

	srv := httptest.NewUnstartedServer(mux)
	srv.TLS = ca.ServerTLSConfig(serverCert)
	srv.StartTLS()
	t.Cleanup(srv.Close)

	return &ctlTestEnv{srv: srv, store: store, ca: ca}
}

// certlessClient returns an HTTPS client that trusts the dev CA but presents NO
// client cert — the shape /enroll must accept under VerifyClientCertIfGiven.
func (e *ctlTestEnv) certlessClient() *http.Client {
	return &http.Client{
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{
				RootCAs:    e.ca.CACertPool(),
				MinVersion: tls.VersionTLS13,
			},
		},
	}
}

// mtlsClient returns an HTTPS client that trusts the dev CA AND presents the given
// client cert — the shape every mTLS route requires.
func (e *ctlTestEnv) mtlsClient(cert tls.Certificate) *http.Client {
	return &http.Client{
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{
				RootCAs:      e.ca.CACertPool(),
				Certificates: []tls.Certificate{cert},
				MinVersion:   tls.VersionTLS13,
			},
		},
	}
}

// genMTLSKeyAndCSR generates a fresh Ed25519 mTLS keypair and a self-signed CSR with
// CN cn. It returns the CSR DER and the private key (the node keeps the key; only the
// CSR is sent to the controller — the controller never sees the private key).
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

// clientCertFromPEM assembles a tls.Certificate from an issued client-cert PEM and
// the private key the node generated for the CSR. This is what an enrolled node /
// the operator presents on subsequent mTLS calls.
func clientCertFromPEM(t *testing.T, certPEM []byte, priv ed25519.PrivateKey) tls.Certificate {
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

// issueOperatorCert mints an operator client cert (CN "<tenant>:operator") by
// driving the same CSR → IssueClientCert path a node uses, so the operator identity
// is a real cert chaining to the dev CA.
func (e *ctlTestEnv) issueOperatorCert(t *testing.T) tls.Certificate {
	t.Helper()
	cn := string(testTenant) + ":" + DefaultOperatorName
	csrDER, priv := genMTLSKeyAndCSR(t, cn)
	certPEM, _, err := e.ca.IssueClientCert(csrDER, DefaultOperatorName, time.Now())
	if err != nil {
		t.Fatalf("IssueClientCert(operator): %v", err)
	}
	return clientCertFromPEM(t, certPEM, priv)
}

// doJSON performs a request with an optional JSON body and decodes a JSON response
// into out (when out != nil). It returns the status code.
func doJSON(t *testing.T, client *http.Client, method, url string, body any, out any) int {
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
	resp, err := client.Do(req)
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

// enrollNode runs the full /enroll ceremony for nodeID over a certless client: the
// operator mints a token (and persists it), the node generates a key+CSR and a WG
// public key, POSTs /enroll, and the resulting client cert is assembled. It returns
// the assembled mTLS client cert.
func (e *ctlTestEnv) enrollNode(t *testing.T, nodeID string) tls.Certificate {
	t.Helper()
	ctx := context.Background()

	// Operator side: mint + persist a single-use token scoped to nodeID.
	plaintext, tok := controller.NewEnrollmentToken(nodeID, time.Hour, time.Now())
	if err := e.store.CreateEnrollmentToken(ctx, testTenant, tok); err != nil {
		t.Fatalf("CreateEnrollmentToken(%s): %v", nodeID, err)
	}

	// Node side: generate the mTLS key + CSR (CN "<tenant>:<node>") and a WG pubkey.
	cn := string(testTenant) + ":" + nodeID
	csrDER, priv := genMTLSKeyAndCSR(t, cn)
	wgPriv, err := wgtypes.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("wg GeneratePrivateKey: %v", err)
	}

	var resp enrollResponseJSON
	status := doJSON(t, e.certlessClient(), http.MethodPost, e.srv.URL+"/api/v1/controller/enroll", enrollRequestJSON{
		Token:       plaintext,
		NodeID:      nodeID,
		CSRDER:      base64.StdEncoding.EncodeToString(csrDER),
		WGPublicKey: wgPriv.PublicKey().String(),
	}, &resp)
	if status != http.StatusOK {
		t.Fatalf("enroll %s: status %d, want 200", nodeID, status)
	}
	if resp.ClientCertPEM == "" || resp.Fingerprint == "" {
		t.Fatalf("enroll %s: empty cert/fingerprint in response", nodeID)
	}

	return clientCertFromPEM(t, []byte(resp.ClientCertPEM), priv)
}

// TestControllerHTTP_EnrollMTLSConfigPollReport is the full happy-path + auth
// integration test described in the file header.
func TestControllerHTTP_EnrollMTLSConfigPollReport(t *testing.T) {
	env := newCtlTestEnv(t)
	base := env.srv.URL + "/api/v1/controller/"

	// (1)+(2) Enroll both nodes over certless clients; build their mTLS clients.
	node1Cert := env.enrollNode(t, "node-1")
	node2Cert := env.enrollNode(t, "node-2")
	node1 := env.mtlsClient(node1Cert)
	node2 := env.mtlsClient(node2Cert)

	// (3) GET /config before any promote → 404.
	if status := doJSON(t, node1, http.MethodGet, base+"config", nil, nil); status != http.StatusNotFound {
		t.Fatalf("config before promote: status %d, want 404", status)
	}

	// (4) Operator drives update-topology → stage → promote.
	op := env.mtlsClient(env.issueOperatorCert(t))

	topoJSON, err := json.Marshal(smallTopo())
	if err != nil {
		t.Fatalf("marshal topology: %v", err)
	}
	// update-topology takes the raw topology JSON as the body.
	if status := doRaw(t, op, http.MethodPost, base+"update-topology", topoJSON); status != http.StatusOK {
		t.Fatalf("update-topology: status %d, want 200", status)
	}

	var stage stageResponseJSON
	if status := doJSON(t, op, http.MethodPost, base+"stage", struct{}{}, &stage); status != http.StatusOK {
		t.Fatalf("stage: status %d, want 200", status)
	}
	if len(stage.Staged) != 2 {
		t.Fatalf("stage: staged %v, want both node-1 and node-2", stage.Staged)
	}

	var promote generationResponseJSON
	if status := doJSON(t, op, http.MethodPost, base+"promote", struct{}{}, &promote); status != http.StatusOK {
		t.Fatalf("promote: status %d, want 200", status)
	}
	if promote.Generation < 1 {
		t.Fatalf("promote: generation %d, want >= 1", promote.Generation)
	}

	// (5) Node fetches its config → 200 with a non-empty bundle.
	var cfg configResponseJSON
	if status := doJSON(t, node1, http.MethodGet, base+"config", nil, &cfg); status != http.StatusOK {
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
	if status := doJSON(t, node1, http.MethodGet, base+"poll?after=0", nil, &poll); status != http.StatusOK {
		t.Fatalf("poll after=0: status %d, want 200", status)
	}
	if poll.Generation != promote.Generation {
		t.Fatalf("poll generation %d, want %d", poll.Generation, promote.Generation)
	}

	// A /poll already AT the current generation with no further promote must time out
	// on the server's (test-shrunk) deadline → 204. This drives the timeout branch of
	// WaitForGeneration; the server returns 204 so the agent re-polls.
	if status := doJSON(t, node1, http.MethodGet, base+"poll?after="+itoa(promote.Generation), nil, nil); status != http.StatusNoContent {
		t.Fatalf("poll timeout: status %d, want 204", status)
	}

	// POST /report → ok; GetNode reflects the applied generation.
	if status := doJSON(t, node1, http.MethodPost, base+"report", reportRequestJSON{
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
	// A certless client on /config → 401 (no client cert).
	if status := doJSON(t, env.certlessClient(), http.MethodGet, base+"config", nil, nil); status != http.StatusUnauthorized {
		t.Fatalf("certless config: status %d, want 401", status)
	}
	// A NODE cert on an operator-only route (/stage) → 403.
	if status := doJSON(t, node2, http.MethodPost, base+"stage", struct{}{}, nil); status != http.StatusForbidden {
		t.Fatalf("node cert on /stage: status %d, want 403", status)
	}
}

// doRaw performs a request with a raw (non-JSON-marshaled) body and returns the
// status code. Used for /update-topology, which stores the topology bytes verbatim.
func doRaw(t *testing.T, client *http.Client, method, url string, body []byte) int {
	t.Helper()
	req, err := http.NewRequest(method, url, bytes.NewReader(body))
	if err != nil {
		t.Fatalf("NewRequest %s %s: %v", method, url, err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, url, err)
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)
	return resp.StatusCode
}

// itoa is a tiny int64→string helper for building the poll cursor query without
// pulling strconv into the test's top-level imports (kept local for clarity).
func itoa(n int64) string {
	return fmt.Sprintf("%d", n)
}

// TestControllerHTTP_ReservedOperatorName confirms a node can never enroll AS the
// operator: /enroll rejects NodeID == the operator identity (before any token work),
// so no node-enrollment path can mint a cert carrying operator privileges.
func TestControllerHTTP_ReservedOperatorName(t *testing.T) {
	env := newCtlTestEnv(t)
	csrDER, _ := genMTLSKeyAndCSR(t, string(testTenant)+":"+DefaultOperatorName)
	status := doJSON(t, env.certlessClient(), http.MethodPost, env.srv.URL+"/api/v1/controller/enroll", enrollRequestJSON{
		Token:       "rejected-before-token-use",
		NodeID:      DefaultOperatorName,
		CSRDER:      base64.StdEncoding.EncodeToString(csrDER),
		WGPublicKey: "unused",
	}, nil)
	if status != http.StatusForbidden {
		t.Fatalf("enroll as %q: status %d, want 403 (reserved operator name)", DefaultOperatorName, status)
	}
}

// TestControllerHTTP_NodeActsOnlyAsItself confirms /config returns the CALLER's own
// bundle (derived from the verified cert) and that two different nodes get two
// different bundles — there is no request field by which node A could obtain node B's
// config.
func TestControllerHTTP_NodeActsOnlyAsItself(t *testing.T) {
	env := newCtlTestEnv(t)
	base := env.srv.URL + "/api/v1/controller/"
	node1 := env.mtlsClient(env.enrollNode(t, "node-1"))
	node2 := env.mtlsClient(env.enrollNode(t, "node-2"))

	op := env.mtlsClient(env.issueOperatorCert(t))
	topoJSON, err := json.Marshal(smallTopo())
	if err != nil {
		t.Fatalf("marshal topology: %v", err)
	}
	if status := doRaw(t, op, http.MethodPost, base+"update-topology", topoJSON); status != http.StatusOK {
		t.Fatalf("update-topology: status %d, want 200", status)
	}
	if status := doJSON(t, op, http.MethodPost, base+"stage", struct{}{}, &stageResponseJSON{}); status != http.StatusOK {
		t.Fatalf("stage: status %d, want 200", status)
	}
	if status := doJSON(t, op, http.MethodPost, base+"promote", struct{}{}, &generationResponseJSON{}); status != http.StatusOK {
		t.Fatalf("promote: status %d, want 200", status)
	}

	var cfg1, cfg2 configResponseJSON
	if status := doJSON(t, node1, http.MethodGet, base+"config", nil, &cfg1); status != http.StatusOK {
		t.Fatalf("node-1 config: status %d, want 200", status)
	}
	if status := doJSON(t, node2, http.MethodGet, base+"config", nil, &cfg2); status != http.StatusOK {
		t.Fatalf("node-2 config: status %d, want 200", status)
	}
	// Each node received ITS OWN bundle: the router (node-1) and peer (node-2) install
	// scripts differ. If /config ignored the cert and served one shared bundle, these
	// would be identical.
	if cfg1.Files["install.sh"] == "" || cfg2.Files["install.sh"] == "" {
		t.Fatalf("a config bundle is missing install.sh (node-1 keys %v, node-2 keys %v)", keysOfMap(cfg1.Files), keysOfMap(cfg2.Files))
	}
	if cfg1.Files["install.sh"] == cfg2.Files["install.sh"] {
		t.Fatalf("node-1 and node-2 received identical install.sh; /config must return the caller's own node bundle")
	}
}

// TestControllerHTTP_ForeignClientCertRejected confirms the mTLS layer rejects a client
// cert that does NOT chain to the controller's dev CA: presenting a cert from a
// different CA fails the handshake, so authenticate() never even sees it.
func TestControllerHTTP_ForeignClientCertRejected(t *testing.T) {
	env := newCtlTestEnv(t)
	foreignCA, err := controller.NewDevCA(testTenant, time.Now(), time.Hour, time.Hour)
	if err != nil {
		t.Fatalf("foreign NewDevCA: %v", err)
	}
	csrDER, priv := genMTLSKeyAndCSR(t, string(testTenant)+":node-1")
	certPEM, _, err := foreignCA.IssueClientCert(csrDER, "node-1", time.Now())
	if err != nil {
		t.Fatalf("foreign IssueClientCert: %v", err)
	}
	foreignCert := clientCertFromPEM(t, certPEM, priv)

	// Trust the REAL server CA (so the server is accepted) but present a FOREIGN client
	// cert the server's ClientCAs does not include.
	client := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{
		RootCAs:      env.ca.CACertPool(),
		Certificates: []tls.Certificate{foreignCert},
		MinVersion:   tls.VersionTLS13,
	}}}
	req, err := http.NewRequest(http.MethodGet, env.srv.URL+"/api/v1/controller/config", nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	if resp, err := client.Do(req); err == nil {
		resp.Body.Close()
		t.Fatalf("foreign client cert accepted (status %d); the mTLS handshake must reject it", resp.StatusCode)
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
