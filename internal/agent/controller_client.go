package agent

// controller_client.go is the agent's client for the NETWORKED controller
// (plan-4.5). The mTLS model (plan-4.3b) was withdrawn: the controller is now
// served over PLAIN HTTP and authenticated with a PER-NODE BEARER TOKEN, with
// transport confidentiality delegated to a reverse proxy (nginx/caddy), never
// forced in-app. This client speaks the controller's JSON protocol under the
// agent namespace /api/v1/agent/:
//
//   - Enroll  (POST /enroll, NO auth) turns a single-use enrollment token + the
//     node's WireGuard PUBLIC key into a per-node bearer API token. /enroll is the
//     one route reachable before the node holds a token; the single-use token is the
//     authentication. The returned api_token is the node's credential for every
//     later call.
//   - Fetch   (GET /config, bearer) implements agent.Source: it returns the bundle
//     files for the caller's node (identity is the token, not the request).
//   - Poll    (GET /poll?after=N, bearer) long-polls for a newer generation.
//   - Report  (POST /report, bearer) implements agent.Reporter (best-effort).
//
// The production file imports ONLY net/http + the agent's own types — never
// crypto/tls, crypto/x509, internal/api, or internal/controller. The wire JSON
// shapes are mirrored here as small private structs so the agent stays decoupled
// from the server packages (the test may import them to drive the server, but the
// client does not). Only the field tags are load-bearing: they MUST match the
// server's JSON exactly.

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/kunorikiku/yet-another-overlay-generator/internal/model"
)

// controllerHTTPTimeout bounds non-poll controller requests (enroll/config/report).
// Poll is a long-poll and uses its own, longer client (pollHTTPTimeout) so a normal
// request cannot hang for the full long-poll window.
const controllerHTTPTimeout = 30 * time.Second

// pollHTTPTimeout bounds a single /poll long-poll on the CLIENT side. It must exceed
// the server's poll deadline (defaultPollDeadline ~55s) so the server's 204 timeout
// is observed as a response rather than aborted as a client transport error; the
// margin absorbs scheduling latency.
const pollHTTPTimeout = 90 * time.Second

// controllerBasePath is the URL prefix every agent route lives under. It is joined
// to the configured BaseURL (the controller's scheme://host[:port] + agent secret
// prefix). The agent only ever speaks the AGENT namespace, never the operator one.
const controllerBasePath = "/api/v1/agent/"

// --- wire JSON shapes (mirrors of internal/api/handler_controller.go) ---
//
// These are duplicated here deliberately: the agent client must not import the
// server package. They are kept private to the package; only the field tags are
// load-bearing (they must match the server's JSON exactly).

type enrollRequestWire struct {
	EnrollmentToken string `json:"enrollment_token"`
	NodeID          string `json:"node_id"`
	WGPublicKey     string `json:"wg_public_key"`
}

type enrollResponseWire struct {
	ApiToken string `json:"api_token"`
	NodeID   string `json:"node_id"`
}

type configResponseWire struct {
	Generation int64             `json:"generation"`
	Files      map[string]string `json:"files"`
	// RekeyRequested signals the controller has flagged this node for a WireGuard key
	// rotation (operator clicked "Roll keys"). When set, the agent regenerates its local
	// key and re-registers the new public key via /rekey instead of applying this bundle.
	RekeyRequested bool `json:"rekey_requested"`
}

// rekeyRequestWire is the body the agent POSTs to /rekey to register its freshly
// rotated WireGuard PUBLIC key (the controller clears the rekey flag in response). The
// node identity is the bearer token; only the new public key travels in the body.
type rekeyRequestWire struct {
	WGPublicKey string `json:"wg_public_key"`
}

type pollResponseWire struct {
	Generation int64 `json:"generation"`
}

type reportRequestWire struct {
	AppliedGeneration int64  `json:"applied_generation"`
	Checksum          string `json:"checksum"`
	Health            string `json:"health"`
	// AgentVersion is the agent's build version (cmd/agent main.BuildVersion). Reported so the
	// controller + panel can show each node's running version. omitempty: a legacy agent that
	// does not send it round-trips as "" (the operator view shows "unknown").
	AgentVersion string `json:"agent_version,omitempty"`
	// Conditions is the structured feedback set (plan-1), omitempty so a build/agent that reports
	// none round-trips as an absent field (an old controller ignores it; a new controller stores nil).
	Conditions []model.Condition `json:"conditions,omitempty"`
}

// EnrollResult is what a successful Enroll hands back to the caller (cmd/agent):
// the per-node bearer API token the controller minted. The caller writes the token
// to disk (0600) and uses it to build the bearer ControllerClient for `run`.
type EnrollResult struct {
	// APIToken is the plaintext per-node bearer token. The controller stores only
	// its SHA-256 hash; this plaintext is returned exactly once, at enrollment.
	APIToken string
}

// ControllerClient speaks the controller's plain-HTTP bearer protocol for one node.
// It is constructed with the node's bearer token (empty before enrollment, for the
// /enroll call) and presents it on every authed request. It implements agent.Source
// (Fetch) and agent.Reporter (Report) so it can drop straight into agent.Run.
type ControllerClient struct {
	// baseURL is the controller's scheme://host[:port], trailing slash trimmed.
	baseURL string
	// nodeToken is the per-node bearer token presented on authed calls (config/poll/
	// report). It is empty on the certless-equivalent client used only for Enroll;
	// authed calls guard against an empty token rather than sending a blank header.
	nodeToken string
	// httpClient is used for non-poll requests; pollClient for the long-poll.
	httpClient *http.Client
	pollClient *http.Client
	// lastFetchedGen is the generation of the bundle returned by the most recent Fetch
	// (from the /config response envelope). Report sends it as the applied generation
	// ONLY when the apply succeeded, so a failed apply never claims the new generation.
	lastFetchedGen int64
	// lastRekeyRequested records the rekey_requested flag from the most recent Fetch's
	// /config envelope. The daemon loop reads it via LastRekeyRequested() AFTER a Fetch:
	// when set, the agent rotates its WG key and re-registers (Rekey) instead of applying
	// the now-stale bundle, then awaits the operator's redeploy.
	lastRekeyRequested bool
	// priorGen is the last-applied generation watermark for the current cycle (the
	// loop's --after). On a FAILED apply, Report sends this unchanged instead of the
	// fetched generation, so the controller registry never shows a generation the node
	// did not actually apply.
	priorGen int64
	// AgentVersion is the agent's build version (set by cmd/agent from its main.BuildVersion).
	// postReport sends it on every /report so the controller + panel show the running version
	// per node. Empty when unset (a dev build / legacy caller); reported as-is.
	AgentVersion string
}

// SetPriorGeneration records the last-applied generation watermark for this cycle (the
// loop's --after). On a FAILED apply, Report sends this unchanged instead of the fetched
// generation, so the controller registry never falsely advances on a failed cycle.
func (c *ControllerClient) SetPriorGeneration(gen int64) {
	c.priorGen = gen
}

// LastFetchedGeneration returns the generation of the bundle returned by the most recent
// successful Fetch (the /config envelope's generation). The daemon loop reads it AFTER a
// successful apply to advance its resume cursor to the generation actually fetched and
// applied — not the one the poll merely observed — so the watermark cannot lag under a
// poll->fetch race (a promote landing between Poll returning gen N and Fetch returning gen
// N+1). It is zero before the first Fetch.
func (c *ControllerClient) LastFetchedGeneration() int64 {
	return c.lastFetchedGen
}

// LastRekeyRequested reports whether the most recent Fetch's /config envelope carried
// rekey_requested=true (the operator flagged this node for a WireGuard key rotation). The
// daemon loop reads it after a Fetch to decide whether to rotate+re-register the local key
// (via RegenerateKey + Rekey) instead of applying the now-stale bundle. It is false before
// the first Fetch and resets to whatever the latest envelope reports.
func (c *ControllerClient) LastRekeyRequested() bool {
	return c.lastRekeyRequested
}

// NewControllerClient builds a ControllerClient for baseURL over plain net/http. The
// nodeToken is the per-node bearer credential presented on config/poll/report; pass
// "" to build the pre-enrollment client used only for Enroll (which is unauthenticated
// by design). There is no TLS/CA/cert material: transport confidentiality is the
// reverse proxy's responsibility (plan-4.5). It returns an error only to keep the
// two-value constructor contract stable across the mTLS->bearer migration.
func NewControllerClient(baseURL, nodeToken string) (*ControllerClient, error) {
	return &ControllerClient{
		baseURL:    strings.TrimRight(baseURL, "/"),
		nodeToken:  nodeToken,
		httpClient: &http.Client{Timeout: controllerHTTPTimeout},
		pollClient: &http.Client{Timeout: pollHTTPTimeout},
	}, nil
}

// url joins the controller base URL, the controller route prefix, and a route name
// (plus optional raw query). route is a trusted constant from this package, so it
// is not escaped.
func (c *ControllerClient) url(route string) string {
	return c.baseURL + controllerBasePath + route
}

// authedRequest builds an *http.Request for an authed controller call, attaching the
// node bearer token. It refuses up front when the client holds no token, so a
// misconfigured (token-less) client fails loudly instead of issuing an unauthenticated
// request that the server would reject with an opaque 401.
func (c *ControllerClient) authedRequest(method, url string, body io.Reader) (*http.Request, error) {
	if c.nodeToken == "" {
		return nil, fmt.Errorf("agent: controller call requires a node API token (enroll first)")
	}
	req, err := http.NewRequest(method, url, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.nodeToken)
	return req, nil
}

// Enroll runs the unauthenticated enrollment ceremony: it POSTs the single-use
// enrollment token + node id + WG public key to /enroll and, on 200, returns the
// minted per-node bearer API token. Enroll must be called on a token-less client
// (NewControllerClient with nodeToken ""): /enroll is the one route reachable before
// the node holds a token, and it is gated by the single-use enrollment token itself,
// not by a bearer credential.
func (c *ControllerClient) Enroll(enrollmentToken, nodeID, wgPub string) (*EnrollResult, error) {
	reqBody, err := json.Marshal(enrollRequestWire{
		EnrollmentToken: enrollmentToken,
		NodeID:          nodeID,
		WGPublicKey:     wgPub,
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
	if strings.TrimSpace(er.ApiToken) == "" {
		return nil, fmt.Errorf("agent: enroll response has empty api token")
	}

	return &EnrollResult{APIToken: er.ApiToken}, nil
}

// Rekey registers a freshly rotated WireGuard PUBLIC key with the controller over the
// bearer-authed POST /rekey. The node identity is the bearer token; only the new public
// key travels in the body. On 2xx the controller has stored the new public key and
// cleared this node's rekey flag — the agent then awaits the operator's redeploy so the
// rest of the fleet learns the new key. It rejects an empty wgPub up front (the server
// would 400) and surfaces any non-2xx as an error.
func (c *ControllerClient) Rekey(wgPub string) error {
	if strings.TrimSpace(wgPub) == "" {
		return fmt.Errorf("agent: rekey requires a non-empty WG public key")
	}
	reqBody, err := json.Marshal(rekeyRequestWire{WGPublicKey: wgPub})
	if err != nil {
		return fmt.Errorf("agent: marshal rekey request: %w", err)
	}
	req, err := c.authedRequest(http.MethodPost, c.url("rekey"), bytes.NewReader(reqBody))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("agent: rekey POST: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("agent: rekey: status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return nil
}

// Fetch implements agent.Source over the controller's GET /config (bearer). The
// node identity is implied by the bearer token, so the nodeID argument is not sent;
// it is only used as a diagnostic context here. It decodes configResponseWire and
// base64-decodes each file into the path->content map agent.Run expects.
func (c *ControllerClient) Fetch(nodeID string) (map[string][]byte, error) {
	req, err := c.authedRequest(http.MethodGet, c.url("config"), nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("agent: config GET: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if resp.StatusCode == http.StatusNotFound {
		// No bundle promoted for this node yet — e.g. the node is enrolled but was not
		// in the current deploy's enrolled subgraph (render-what's-ready). The run loop
		// treats this as a transient no-op (keep-last-good) and keeps polling; it is not
		// a corruption or auth failure.
		return nil, fmt.Errorf("agent: no bundle promoted for node %q yet", nodeID)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("agent: config for node %q: status %d: %s", nodeID, resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var cr configResponseWire
	if err := json.Unmarshal(body, &cr); err != nil {
		return nil, fmt.Errorf("agent: decode config response: %w", err)
	}
	// Record the bundle's own generation so Report (on success) tells the controller the
	// generation actually applied — not merely the one the loop polled.
	c.lastFetchedGen = cr.Generation
	// Record the rekey signal from this envelope so the loop can branch (rotate+register
	// vs apply) via LastRekeyRequested() after the Fetch returns.
	c.lastRekeyRequested = cr.RekeyRequested
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
	reqURL := fmt.Sprintf("%s?after=%d", c.url("poll"), after)
	req, err := c.authedRequest(http.MethodGet, reqURL, nil)
	if err != nil {
		return after, false, err
	}
	resp, err := c.pollClient.Do(req)
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

// Report implements agent.Reporter over the controller's POST /report (bearer). The
// payload is the agent's persisted State JSON (the same bytes the DirSource/HTTP
// path reports); this maps it onto the controller's reportRequestWire shape. Like
// the existing HTTPSource.Report it is best-effort: a transport or non-2xx failure
// is returned to the caller, which logs it but does NOT fail an applied bundle.
func (c *ControllerClient) Report(nodeID string, payload []byte) error {
	// Translate the agent State JSON into the controller's report shape. The applied
	// generation is the bundle actually applied (lastFetchedGen) ONLY when the apply
	// succeeded; on any non-"ok" result we report the unchanged prior watermark, so a
	// failed apply never tells the controller the node advanced. The health line always
	// reflects the real outcome so the registry shows the check-in either way.
	var st State
	if err := json.Unmarshal(payload, &st); err != nil {
		return fmt.Errorf("agent: report: parse state payload: %w", err)
	}
	gen := c.priorGen
	if st.LastResult == LastResultOK {
		gen = c.lastFetchedGen
	}
	return c.postReport(gen, st.LastChecksum, st.Health, st.Conditions)
}

// postReport POSTs a reportRequestWire over the bearer-authed client. It is the
// single report transport shared by the Reporter interface (Report) and any explicit
// caller. It is best-effort: a transport or non-2xx error is returned to the caller
// (which logs it) but must not fail an otherwise-applied bundle.
func (c *ControllerClient) postReport(gen int64, checksum, health string, conditions []model.Condition) error {
	reqBody, err := json.Marshal(reportRequestWire{
		AppliedGeneration: gen,
		Checksum:          checksum,
		Health:            health,
		AgentVersion:      c.AgentVersion,
		Conditions:        conditions,
	})
	if err != nil {
		return fmt.Errorf("agent: marshal report request: %w", err)
	}
	req, err := c.authedRequest(http.MethodPost, c.url("report"), bytes.NewReader(reqBody))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.httpClient.Do(req)
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
