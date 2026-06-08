package agent

// controller_client.go is the agent's client for the NETWORKED controller
// (plan-4.3b), closing the single-tenant loop opened by the configured-source
// agent (plan-1b). Where DirSource/HTTPSource pull a plain bundle tree, this
// client speaks the controller's mTLS JSON protocol under /api/v1/controller/:
//
//   - Enroll  (POST /enroll, certless TLS) turns a single-use token + CSR into an
//     issued mTLS client cert. The agent is configured OUT-OF-BAND with the
//     controller's CA cert PEM (--controller-ca) and pins it: the Enroll TLS trusts
//     only that CA, and the response's ca_cert_pem MUST byte-equal the pinned CA or
//     enrollment is refused. This is the bootstrap-trust anchor for every later call.
//   - Fetch   (GET /config, mTLS) implements agent.Source: it returns the bundle
//     files for the caller's node (identity is the cert, not the request).
//   - Poll    (GET /poll?after=N, mTLS) long-polls for a newer generation.
//   - Report  (POST /report, mTLS) implements agent.Reporter (best-effort).
//
// The production file imports ONLY net/http + crypto/tls + the agent's own types —
// never internal/api or internal/controller. The wire JSON shapes are mirrored here
// as small private structs so the agent stays decoupled from the server packages
// (the test may import them to drive the server, but the client does not).

import (
	"bytes"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// controllerHTTPTimeout bounds non-poll controller requests (enroll/config/report).
// Poll is a long-poll and uses its own, longer client (pollHTTPTimeout) so a normal
// request cannot hang for the full long-poll window.
const controllerHTTPTimeout = 30 * time.Second

// pollHTTPTimeout bounds a single /poll long-poll on the CLIENT side. It must exceed
// the server's poll deadline (defaultPollDeadline ~55s) so the server's 204 timeout
// is observed as a response rather than aborted as a client transport error; the
// margin absorbs handshake + scheduling latency.
const pollHTTPTimeout = 90 * time.Second

// controllerBasePath is the URL prefix every controller route lives under. It is
// joined to the configured BaseURL (which is the controller's scheme://host[:port]).
const controllerBasePath = "/api/v1/controller/"

// --- wire JSON shapes (mirrors of internal/api/handler_controller.go) ---
//
// These are duplicated here deliberately: the agent client must not import the
// server package. They are kept private to the package; only the field tags are
// load-bearing (they must match the server's JSON exactly).

type enrollRequestWire struct {
	Token       string `json:"token"`
	NodeID      string `json:"node_id"`
	CSRDER      string `json:"csr_der"` // base64(DER) of the node's mTLS CSR
	WGPublicKey string `json:"wg_public_key"`
}

type enrollResponseWire struct {
	ClientCertPEM string `json:"client_cert_pem"`
	CACertPEM     string `json:"ca_cert_pem"`
	Fingerprint   string `json:"fingerprint"`
}

type configResponseWire struct {
	Generation int64             `json:"generation"`
	Files      map[string]string `json:"files"`
}

type pollResponseWire struct {
	Generation int64 `json:"generation"`
}

type reportRequestWire struct {
	AppliedGeneration int64  `json:"applied_generation"`
	Checksum          string `json:"checksum"`
	Health            string `json:"health"`
}

// EnrollResult is what a successful Enroll hands back to the caller (cmd/agent):
// the issued client-cert PEM, the CA PEM (already verified to equal the pinned CA),
// and the issued cert's fingerprint. The caller writes the cert + its mTLS private
// key to disk and uses them to build the mTLS ControllerClient for `run`.
type EnrollResult struct {
	// ClientCertPEM is the issued mTLS client certificate, PEM-encoded.
	ClientCertPEM []byte
	// CACertPEM is the controller CA, PEM-encoded. It is guaranteed to byte-equal
	// the pinned CA (Enroll refuses otherwise), so it is safe to persist as-is.
	CACertPEM []byte
	// Fingerprint is hex(SHA-256(certDER)) of the issued client cert.
	Fingerprint string
}

// ControllerClient speaks the controller's mTLS JSON protocol for one node. It is
// constructed with the pinned CA PEM (the bootstrap trust anchor) and, after
// enrollment, the issued client cert. It implements agent.Source (Fetch) and
// agent.Reporter (Report) so it can drop straight into agent.Run.
type ControllerClient struct {
	// baseURL is the controller's scheme://host[:port], trailing slash trimmed.
	baseURL string
	// caPEM is the pinned controller CA, configured out-of-band. RootCAs trusts only
	// this; Enroll additionally requires the response CA to byte-equal it.
	caPEM []byte
	// clientCert is the issued mTLS client cert, or nil before enrollment (Enroll
	// runs certless). When set, every request presents it for mTLS.
	clientCert *tls.Certificate
	// httpClient is used for non-poll requests; pollClient for the long-poll.
	httpClient *http.Client
	pollClient *http.Client
	// pendingGeneration is the controller generation the loop is about to apply. The
	// agent State models anti-rollback as a manifest compiled_at STRING and carries no
	// int64 generation, but the controller's /report wants the numeric generation. The
	// controller-mode loop calls SetPendingGeneration(gen) right before agent.Run, so
	// the auto-Report that Run fires (Run reports because this client is a Reporter)
	// carries the correct applied generation. It defaults to 0 (an honest "unknown
	// generation" check-in) for any Report not preceded by SetPendingGeneration.
	pendingGeneration int64
}

// SetPendingGeneration records the controller generation the loop is about to apply
// so the next Report (auto-fired by agent.Run) reports that generation. See the
// pendingGeneration field doc for why the generation cannot be derived from State.
func (c *ControllerClient) SetPendingGeneration(gen int64) {
	c.pendingGeneration = gen
}

// NewControllerClient builds a ControllerClient for baseURL, pinning caPEM as the
// sole TLS root. When clientCert is non-nil the client presents it (mTLS, for
// config/poll/report); when nil the client is certless (for Enroll). It returns an
// error if caPEM does not parse into at least one certificate, so a misconfigured
// --controller-ca fails loudly at construction rather than as an opaque handshake
// error later.
func NewControllerClient(baseURL string, caPEM []byte, clientCert *tls.Certificate) (*ControllerClient, error) {
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(caPEM) {
		return nil, fmt.Errorf("agent: controller CA PEM contains no usable certificate")
	}
	tlsCfg := &tls.Config{
		RootCAs:    pool,
		MinVersion: tls.VersionTLS13,
	}
	if clientCert != nil {
		tlsCfg.Certificates = []tls.Certificate{*clientCert}
	}
	// Each client gets its own Transport with a clone of the TLS config so the two
	// timeouts do not share mutable state.
	newTransport := func() *http.Transport {
		return &http.Transport{TLSClientConfig: tlsCfg.Clone()}
	}
	return &ControllerClient{
		baseURL:    strings.TrimRight(baseURL, "/"),
		caPEM:      caPEM,
		clientCert: clientCert,
		httpClient: &http.Client{Timeout: controllerHTTPTimeout, Transport: newTransport()},
		pollClient: &http.Client{Timeout: pollHTTPTimeout, Transport: newTransport()},
	}, nil
}

// url joins the controller base URL, the controller route prefix, and a route name
// (plus optional raw query). route is a trusted constant from this package, so it
// is not escaped.
func (c *ControllerClient) url(route string) string {
	return c.baseURL + controllerBasePath + route
}

// Enroll runs the certless enrollment ceremony: it POSTs the token + CSR (+ WG
// public key) to /enroll and, on 200, decodes the issued cert.
//
// Bootstrap trust is enforced HERE: the response's ca_cert_pem MUST byte-equal the
// caPEM the agent was configured with out-of-band. A controller that returns a
// DIFFERENT CA — even a validly-served one — is refused ("controller CA mismatch"),
// because the agent's whole trust chain for every later mTLS call rests on the
// pinned CA. (The handshake already trusts only the pinned CA, so a TLS-level
// substitution cannot occur; this check additionally rejects a controller that
// hands back an inconsistent CA in the body.)
//
// Enroll must be called on a certless client (NewControllerClient with nil
// clientCert): /enroll is the one route reachable before the node holds a cert.
func (c *ControllerClient) Enroll(token, nodeID string, csrDER []byte, wgPub string) (*EnrollResult, error) {
	reqBody, err := json.Marshal(enrollRequestWire{
		Token:       token,
		NodeID:      nodeID,
		CSRDER:      base64.StdEncoding.EncodeToString(csrDER),
		WGPublicKey: wgPub,
	})
	if err != nil {
		return nil, fmt.Errorf("agent: marshal enroll request: %w", err)
	}

	resp, err := c.httpClient.Post(c.url("enroll"), "application/json", bytes.NewReader(reqBody))
	if err != nil {
		return nil, fmt.Errorf("agent: enroll POST: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("agent: enroll: status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var er enrollResponseWire
	if err := json.Unmarshal(body, &er); err != nil {
		return nil, fmt.Errorf("agent: decode enroll response: %w", err)
	}
	if strings.TrimSpace(er.ClientCertPEM) == "" {
		return nil, fmt.Errorf("agent: enroll response has empty client cert")
	}

	// Bootstrap-trust gate: the returned CA must be the pinned CA, byte for byte.
	// Compare the DECODED certificate bytes, not the raw PEM strings, so cosmetic PEM
	// differences (line wrapping, trailing newline) do not cause a spurious mismatch
	// while still rejecting a genuinely different CA.
	if !sameCertPEM(c.caPEM, []byte(er.CACertPEM)) {
		return nil, fmt.Errorf("agent: controller CA mismatch: enroll response CA does not equal the pinned --controller-ca")
	}

	return &EnrollResult{
		ClientCertPEM: []byte(er.ClientCertPEM),
		CACertPEM:     []byte(er.CACertPEM),
		Fingerprint:   er.Fingerprint,
	}, nil
}

// Fetch implements agent.Source over the controller's GET /config (mTLS). The
// node identity is implied by the client cert, so the nodeID argument is not sent;
// it is only used as a diagnostic context here. It decodes configResponseJSON and
// base64-decodes each file into the path->content map agent.Run expects.
func (c *ControllerClient) Fetch(nodeID string) (map[string][]byte, error) {
	if c.clientCert == nil {
		return nil, fmt.Errorf("agent: Fetch requires an mTLS client cert (enroll first)")
	}
	resp, err := c.httpClient.Get(c.url("config"))
	if err != nil {
		return nil, fmt.Errorf("agent: config GET: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("agent: config for node %q: status %d: %s", nodeID, resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var cr configResponseWire
	if err := json.Unmarshal(body, &cr); err != nil {
		return nil, fmt.Errorf("agent: decode config response: %w", err)
	}
	files := make(map[string][]byte, len(cr.Files))
	for path, b64 := range cr.Files {
		raw, err := base64.StdEncoding.DecodeString(b64)
		if err != nil {
			return nil, fmt.Errorf("agent: config file %q: bad base64: %w", path, err)
		}
		files[path] = raw
	}
	return files, nil
}

// Poll long-polls the controller for a generation strictly greater than after. It
// returns (gen, true, nil) when the controller reports an advance (HTTP 200), or
// (after, false, nil) when the long-poll times out server-side (HTTP 204) so the
// caller re-polls. Any other status or transport failure is an error. It uses the
// long-poll-aware client so a single call may block up to pollHTTPTimeout.
func (c *ControllerClient) Poll(after int64) (gen int64, changed bool, err error) {
	if c.clientCert == nil {
		return after, false, fmt.Errorf("agent: Poll requires an mTLS client cert (enroll first)")
	}
	reqURL := fmt.Sprintf("%s?after=%d", c.url("poll"), after)
	resp, err := c.pollClient.Get(reqURL)
	if err != nil {
		return after, false, fmt.Errorf("agent: poll GET: %w", err)
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusNoContent:
		// Server-side long-poll timeout: no advance within the window. Re-poll.
		_, _ = io.Copy(io.Discard, resp.Body)
		return after, false, nil
	case http.StatusOK:
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		var pr pollResponseWire
		if err := json.Unmarshal(body, &pr); err != nil {
			return after, false, fmt.Errorf("agent: decode poll response: %w", err)
		}
		return pr.Generation, true, nil
	default:
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		return after, false, fmt.Errorf("agent: poll: status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
}

// Report implements agent.Reporter over the controller's POST /report (mTLS). The
// payload is the agent's persisted State JSON (the same bytes the DirSource/HTTP
// path reports); this maps it onto the controller's reportRequestJSON shape. Like
// the existing HTTPSource.Report it is best-effort: a transport or non-2xx failure
// is returned to the caller, which logs it but does NOT fail an applied bundle.
func (c *ControllerClient) Report(nodeID string, payload []byte) error {
	if c.clientCert == nil {
		return fmt.Errorf("agent: Report requires an mTLS client cert (enroll first)")
	}
	// Translate the agent State JSON into the controller's report shape. A State
	// without controller-relevant fields (e.g. a fetch failure before a manifest was
	// read) still reports its health so the controller registry reflects the check-in.
	// The numeric generation comes from pendingGeneration (set by the loop), not from
	// State, which has no int64 generation.
	var st State
	if err := json.Unmarshal(payload, &st); err != nil {
		return fmt.Errorf("agent: report: parse state payload: %w", err)
	}
	return c.postReport(c.pendingGeneration, st.LastChecksum, st.Health)
}

// postReport POSTs a reportRequestJSON over mTLS. It is the single report transport
// shared by the Reporter interface (Report) and any explicit caller. It is
// best-effort: a transport or non-2xx error is returned to the caller (which logs
// it) but must not fail an otherwise-applied bundle.
func (c *ControllerClient) postReport(gen int64, checksum, health string) error {
	reqBody, err := json.Marshal(reportRequestWire{
		AppliedGeneration: gen,
		Checksum:          checksum,
		Health:            health,
	})
	if err != nil {
		return fmt.Errorf("agent: marshal report request: %w", err)
	}
	resp, err := c.httpClient.Post(c.url("report"), "application/json", bytes.NewReader(reqBody))
	if err != nil {
		return fmt.Errorf("agent: report POST: %w", err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("agent: report: status %d", resp.StatusCode)
	}
	return nil
}

// sameCertPEM reports whether two CA PEM blobs encode the same certificate. It
// compares the parsed certificate DER (via the first cert in each pool) rather than
// the raw PEM text so harmless encoding differences do not read as a CA mismatch,
// while a genuinely different certificate is still rejected. A blob that fails to
// parse is treated as not-equal (fail closed).
func sameCertPEM(a, b []byte) bool {
	ca, errA := firstCertDER(a)
	cb, errB := firstCertDER(b)
	if errA != nil || errB != nil {
		return false
	}
	return bytes.Equal(ca, cb)
}

// firstCertDER decodes the first CERTIFICATE block from a PEM blob and returns its
// DER bytes. It validates the bytes parse as an x509 certificate so a malformed
// block cannot masquerade as a match.
func firstCertDER(pemBytes []byte) ([]byte, error) {
	rest := pemBytes
	for {
		var block *pem.Block
		block, rest = pem.Decode(rest)
		if block == nil {
			return nil, fmt.Errorf("agent: no CERTIFICATE block in CA PEM")
		}
		if block.Type != "CERTIFICATE" {
			continue
		}
		cert, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			return nil, fmt.Errorf("agent: parse CA certificate: %w", err)
		}
		return cert.Raw, nil
	}
}
