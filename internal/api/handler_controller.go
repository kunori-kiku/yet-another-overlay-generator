package api

// handler_controller.go is the HTTP surface of the networked controller
// (plan-4.3b). It exposes the controller core (Store + enrollment + compile) under
// /api/v1/controller/, with JSON request/response bodies. Authentication and the
// tenant/node identity are handled entirely by the auth chokepoint in
// auth_controller.go: every handler here reads the caller's node from the request
// context (nodeFromCtx) rather than from the request, so a node can only ever act
// as itself. The single exception is /enroll, which is registered WITHOUT the auth
// middleware (it must be reachable before the node has any client cert) and is
// instead gated by the single-use enrollment token + CSR proof-of-possession.

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/kunorikiku/yet-another-overlay-generator/internal/controller"
)

// DefaultOperatorName is the node component of the operator's client-cert CN
// ("<tenant>:operator"). Operator-only routes require a cert with this identity.
// It is a constant under single-tenant v1; Plan 5 (OIDC/RBAC) replaces this with a
// real principal model.
const DefaultOperatorName = "operator"

// defaultPollDeadline bounds a single /poll long-poll on the server side. The
// handler returns 204 when the generation has not advanced within this window so
// the agent re-polls on a fresh connection (and the request does not pin a server
// goroutine indefinitely). It is below typical 60s proxy idle timeouts. A
// ControllerHandler may override it (pollDeadline) — the integration test sets a
// tiny value to exercise the timeout-204 path without waiting ~55s.
const defaultPollDeadline = 55 * time.Second

// controllerMaxBodyBytes caps controller request bodies. Controller payloads
// (enroll request, report, topology JSON) are small; the same MaxBytesReader
// discipline as the air-gap handlers guards against unbounded io.ReadAll (D34).
// Topology can be larger than a report, so this reuses the air-gap topology cap.
const controllerMaxBodyBytes = maxRequestBodyBytes

// ControllerHandler holds the dependencies shared by every controller route: the
// tenant-scoped Store, the ephemeral DevCA (issuer for enrollment + the TLS server
// cert), the configured tenant (single-tenant v1, pinned from YAOG_TENANT_ID), and
// the operator identity that operator-only routes require.
type ControllerHandler struct {
	store        controller.Store
	ca           *controller.DevCA
	tenant       controller.TenantID
	operatorName string
	// pollDeadline bounds a single /poll long-poll (defaultPollDeadline when zero).
	pollDeadline time.Duration
}

// NewControllerHandler builds a ControllerHandler. operatorName defaults to
// DefaultOperatorName when empty; the poll deadline defaults to defaultPollDeadline.
func NewControllerHandler(store controller.Store, ca *controller.DevCA, tenant controller.TenantID, operatorName string) *ControllerHandler {
	if operatorName == "" {
		operatorName = DefaultOperatorName
	}
	return &ControllerHandler{
		store:        store,
		ca:           ca,
		tenant:       tenant,
		operatorName: operatorName,
		pollDeadline: defaultPollDeadline,
	}
}

// Routes registers the controller routes on mux. /enroll is registered WITHOUT the
// auth middleware (reachable certless); agent routes go through requireNode;
// operator routes through requireOperator. recoverPanics/cors are NOT applied here
// — controller requests are machine-to-machine JSON over mTLS, with no browser CORS
// concern; panic recovery for these routes is a documented follow-up if needed.
// Keeping registration here (not in Server.registerRoutes) keeps the air-gap mux
// byte-identical when controller mode is off.
func (h *ControllerHandler) Routes(mux *http.ServeMux) {
	const base = "/api/v1/controller/"
	mux.HandleFunc(base+"enroll", h.HandleEnroll)
	mux.HandleFunc(base+"config", h.requireNode(h.HandleConfig))
	mux.HandleFunc(base+"poll", h.requireNode(h.HandlePoll))
	mux.HandleFunc(base+"report", h.requireNode(h.HandleReport))
	mux.HandleFunc(base+"update-topology", h.requireOperator(h.HandleUpdateTopology))
	mux.HandleFunc(base+"stage", h.requireOperator(h.HandleStage))
	mux.HandleFunc(base+"promote", h.requireOperator(h.HandlePromote))
}

// --- JSON request/response types ---

// enrollRequestJSON is the wire form of an enrollment request. The CSR and CA-bound
// material are DER bytes carried base64 (JSON cannot hold raw bytes).
type enrollRequestJSON struct {
	Token       string `json:"token"`
	NodeID      string `json:"node_id"`
	CSRDER      string `json:"csr_der"` // base64(DER) of the node's mTLS CSR
	WGPublicKey string `json:"wg_public_key"`
}

// enrollResponseJSON is the wire form of a successful enrollment: PEM strings + the
// issued cert fingerprint.
type enrollResponseJSON struct {
	ClientCertPEM string `json:"client_cert_pem"`
	CACertPEM     string `json:"ca_cert_pem"`
	Fingerprint   string `json:"fingerprint"`
}

// configResponseJSON is the wire form of a node's current bundle: the generation
// plus the bundle files keyed by bundle-relative path, each value base64.
type configResponseJSON struct {
	Generation int64             `json:"generation"`
	Files      map[string]string `json:"files"`
}

// pollResponseJSON is the wire form of a /poll hit: the generation that is now
// current (strictly greater than the caller's ?after=). A timeout returns 204 with
// no body instead.
type pollResponseJSON struct {
	Generation int64 `json:"generation"`
}

// reportRequestJSON is the wire form of an agent apply report.
type reportRequestJSON struct {
	AppliedGeneration int64  `json:"applied_generation"`
	Checksum          string `json:"checksum"`
	Health            string `json:"health"`
}

// stageResponseJSON is the wire form of a stage result.
type stageResponseJSON struct {
	Staged            []string `json:"staged"`
	SkippedUnenrolled []string `json:"skipped_unenrolled"`
	Generation        int64    `json:"generation"`
}

// generationResponseJSON is the wire form of a promote result.
type generationResponseJSON struct {
	Generation int64 `json:"generation"`
}

// --- handlers ---

// HandleEnroll runs the node-enrollment ceremony. It requires NO client cert (it is
// the route a node calls before it has one); the single-use token + CSR PoP from
// plan-4.2 are the authentication. The tenant is the configured one (single-tenant
// v1) — never taken from the request body.
func (h *ControllerHandler) HandleEnroll(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "only POST is supported")
		return
	}
	var req enrollRequestJSON
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	csrDER, err := base64.StdEncoding.DecodeString(req.CSRDER)
	if err != nil {
		writeError(w, http.StatusBadRequest, "csr_der must be base64-encoded DER")
		return
	}

	result, err := controller.Enroll(r.Context(), h.store, h.ca, h.tenant, controller.EnrollRequest{
		Token:       req.Token,
		NodeID:      req.NodeID,
		CSRDER:      csrDER,
		WGPublicKey: req.WGPublicKey,
	}, time.Now())
	if err != nil {
		// Token errors are an authorization failure (bad/expired/consumed token);
		// everything else here is a malformed request (bad CSR, CN mismatch). We map
		// token errors to 401 and the rest to 400 so a caller can distinguish "your
		// token is no good" from "your CSR is wrong".
		if errors.Is(err, controller.ErrTokenInvalid) || errors.Is(err, controller.ErrTokenConsumed) {
			writeError(w, http.StatusUnauthorized, err.Error())
			return
		}
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, enrollResponseJSON{
		ClientCertPEM: string(result.ClientCertPEM),
		CACertPEM:     string(result.CACertPEM),
		Fingerprint:   result.Fingerprint,
	})
}

// HandleConfig returns the CALLER's current bundle (the node taken from the verified
// cert, never the request). 404 before the first promote for that node. It also
// TouchLastSeen-s the node so the registry reflects the check-in.
func (h *ControllerHandler) HandleConfig(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "only GET is supported")
		return
	}
	tenant, node, ok := identity(r.Context())
	if !ok {
		writeError(w, http.StatusInternalServerError, "missing authenticated identity")
		return
	}

	bundle, err := h.store.GetCurrentBundle(r.Context(), tenant, node)
	if err != nil {
		if errors.Is(err, controller.ErrNotFound) {
			writeError(w, http.StatusNotFound, "no configuration available for this node yet")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to load configuration")
		return
	}

	// Best-effort check-in stamp; a failed touch must not deny the node its config.
	_ = h.store.TouchLastSeen(r.Context(), tenant, node, time.Now())

	files := make(map[string]string, len(bundle.Files))
	for path, content := range bundle.Files {
		files[path] = base64.StdEncoding.EncodeToString(content)
	}
	writeJSON(w, http.StatusOK, configResponseJSON{
		Generation: bundle.Generation,
		Files:      files,
	})
}

// HandlePoll long-polls for a generation strictly greater than ?after=N. It blocks
// on Store.WaitForGeneration under a ~55s server deadline derived from the request
// context. On advance it returns {generation}; on the deadline it returns 204 so the
// agent re-polls. The node identity comes from the cert (TouchLastSeen the caller).
func (h *ControllerHandler) HandlePoll(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "only GET is supported")
		return
	}
	tenant, node, ok := identity(r.Context())
	if !ok {
		writeError(w, http.StatusInternalServerError, "missing authenticated identity")
		return
	}
	after, err := parseAfter(r.URL.Query().Get("after"))
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	// Best-effort check-in stamp on each poll.
	_ = h.store.TouchLastSeen(r.Context(), tenant, node, time.Now())

	deadline := h.pollDeadline
	if deadline <= 0 {
		deadline = defaultPollDeadline
	}
	ctx, cancel := context.WithTimeout(r.Context(), deadline)
	defer cancel()

	gen, err := h.store.WaitForGeneration(ctx, tenant, after)
	if err != nil {
		// Deadline/cancellation → no advance within the window → 204, re-poll.
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		writeError(w, http.StatusInternalServerError, "poll failed")
		return
	}
	writeJSON(w, http.StatusOK, pollResponseJSON{Generation: gen})
}

// HandleReport records an agent's apply outcome for ITSELF: SetAppliedGeneration +
// TouchLastSeen + an audit entry. The node is the cert's node; the report body
// carries only the applied generation, checksum, and a health string.
func (h *ControllerHandler) HandleReport(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "only POST is supported")
		return
	}
	tenant, node, ok := identity(r.Context())
	if !ok {
		writeError(w, http.StatusInternalServerError, "missing authenticated identity")
		return
	}
	var req reportRequestJSON
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	now := time.Now()
	if err := h.store.SetAppliedGeneration(r.Context(), tenant, node, req.AppliedGeneration, req.Checksum); err != nil {
		if errors.Is(err, controller.ErrNotFound) {
			writeError(w, http.StatusNotFound, "node not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to record applied generation")
		return
	}
	_ = h.store.TouchLastSeen(r.Context(), tenant, node, now)
	if _, err := h.store.AppendAudit(r.Context(), tenant, controller.AuditEntry{
		Timestamp: now,
		Actor:     "agent:" + node,
		Action:    "report",
		NodeID:    node,
	}); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to append audit")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// HandleUpdateTopology stores a new topology version (operator-only). The body is
// the public-keys-only topology JSON; it is stored verbatim via PutTopology (the
// stage step compiles/validates it). The tenant is the configured one.
func (h *ControllerHandler) HandleUpdateTopology(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "only POST is supported")
		return
	}
	tenant, _, ok := identity(r.Context())
	if !ok {
		writeError(w, http.StatusInternalServerError, "missing authenticated identity")
		return
	}
	body, err := readControllerBody(w, r)
	if err != nil {
		writeError(w, statusForBodyErr(err), err.Error())
		return
	}
	// Reject a non-JSON body up front so a malformed topology fails here rather than
	// surfacing as a confusing compile error at /stage.
	if !json.Valid(body) {
		writeError(w, http.StatusBadRequest, "topology body is not valid JSON")
		return
	}

	rec, err := h.store.PutTopology(r.Context(), tenant, body)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to store topology")
		return
	}
	writeJSON(w, http.StatusOK, map[string]int64{"version": rec.Version})
}

// HandleStage compiles the enrolled subgraph of the stored topology into per-node
// bundles staged at the next generation (operator-only). It returns the StageResult.
func (h *ControllerHandler) HandleStage(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "only POST is supported")
		return
	}
	tenant, _, ok := identity(r.Context())
	if !ok {
		writeError(w, http.StatusInternalServerError, "missing authenticated identity")
		return
	}
	result, err := controller.CompileAndStage(r.Context(), h.store, tenant, time.Now())
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, stageResponseJSON{
		Staged:            result.Staged,
		SkippedUnenrolled: result.SkippedUnenrolled,
		Generation:        result.Generation,
	})
}

// HandlePromote flips the staged bundles to current and bumps the generation
// (operator-only), waking any /poll waiters. Returns the new generation.
func (h *ControllerHandler) HandlePromote(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "only POST is supported")
		return
	}
	tenant, _, ok := identity(r.Context())
	if !ok {
		writeError(w, http.StatusInternalServerError, "missing authenticated identity")
		return
	}
	gen, err := h.store.PromoteStaged(r.Context(), tenant)
	if err != nil {
		if errors.Is(err, controller.ErrNoStagedBundle) {
			writeError(w, http.StatusConflict, err.Error())
			return
		}
		writeError(w, http.StatusInternalServerError, "promote failed")
		return
	}
	writeJSON(w, http.StatusOK, generationResponseJSON{Generation: gen})
}

// --- helpers ---

// identity pulls the tenant+node the auth middleware pinned onto the context. The
// boolean is false if either is missing (a handler reached without the middleware,
// which is an internal error).
func identity(ctx context.Context) (controller.TenantID, string, bool) {
	tenant, okT := tenantFromCtx(ctx)
	node, okN := nodeFromCtx(ctx)
	if !okT || !okN {
		return "", "", false
	}
	return tenant, node, true
}

// decodeJSON reads a size-capped JSON body into v. It rejects unknown fields so a
// typo'd key is a 400 rather than a silently-ignored field.
func decodeJSON(w http.ResponseWriter, r *http.Request, v any) error {
	defer r.Body.Close()
	r.Body = http.MaxBytesReader(w, r.Body, controllerMaxBodyBytes)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return err
	}
	return nil
}

// readControllerBody reads a size-capped raw body (for endpoints that store the
// bytes verbatim, e.g. update-topology). It returns errBodyTooLarge on overflow so
// the caller can map it to 413.
func readControllerBody(w http.ResponseWriter, r *http.Request) ([]byte, error) {
	defer r.Body.Close()
	r.Body = http.MaxBytesReader(w, r.Body, controllerMaxBodyBytes)
	body, err := io.ReadAll(r.Body)
	if err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			return nil, errBodyTooLarge
		}
		return nil, err
	}
	if len(body) == 0 {
		return nil, errBodyEmpty
	}
	return body, nil
}

// errBodyEmpty marks an empty request body where one is required.
var errBodyEmpty = errors.New("request body is empty")

// statusForBodyErr maps a readControllerBody error to an HTTP status: 413 for
// oversize, 400 otherwise.
func statusForBodyErr(err error) int {
	if isBodyTooLarge(err) {
		return http.StatusRequestEntityTooLarge
	}
	return http.StatusBadRequest
}

// parseAfter parses the /poll ?after= cursor. An empty value means 0 (poll for any
// generation). A non-numeric or negative value is a 400.
func parseAfter(s string) (int64, error) {
	if s == "" {
		return 0, nil
	}
	var n int64
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0, errors.New("after must be a non-negative integer")
		}
		n = n*10 + int64(c-'0')
	}
	return n, nil
}
