package api

// handler_controller.go is the HTTP surface of the networked controller
// (plan-4.5). It exposes the controller core (Store + enrollment + compile) under
// /api/v1/controller/, with JSON request/response bodies. Authentication and the
// tenant/node identity are handled entirely by the auth chokepoint in
// auth_controller.go: every handler here reads the caller's node from the request
// context (nodeFromCtx) rather than from the request, so a node can only ever act
// as itself. The single exception is /enroll, which is registered WITHOUT the auth
// middleware (it must be reachable before the node has any API token) and is
// instead gated by the single-use enrollment token.
//
// The routes are split across two muxes (served on two plain-HTTP ports):
//   - agent routes (/enroll,/config,/poll,/report) → RegisterAgentRoutes.
//   - operator routes (everything else) → RegisterOperatorRoutes.
//
// Transport is plain HTTP; TLS is delegated to a reverse proxy (plan-4.5). Bearer
// tokens authenticate both kinds of caller (per-node tokens for agents, a single
// operator token for the operator).

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/kunorikiku/yet-another-overlay-generator/internal/controller"
)

// DefaultOperatorName is the operator's identity stamped into the request context
// (and audit actor) for operator routes. Under single-tenant v1 the operator is
// authenticated by a single shared operator token (YAOG_CONTROLLER_OPERATOR_TOKEN);
// Plan 5 (OIDC/RBAC) replaces this with a real per-operator principal model.
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
// tenant-scoped Store, the configured tenant (single-tenant v1, pinned from
// YAOG_TENANT_ID), the hex SHA-256 of the operator's bearer token (never the
// plaintext), and the operator identity stamped onto operator-route requests.
type ControllerHandler struct {
	store             controller.Store
	tenant            controller.TenantID
	operatorTokenHash string
	operatorName      string
	// pollDeadline bounds a single /poll long-poll (defaultPollDeadline when zero).
	pollDeadline time.Duration
	// pathPrefix is an optional secret path segment the controller routes mount under
	// (e.g. "/s3cr3t" -> "/s3cr3t/api/v1/controller/..."). Empty = the bare
	// "/api/v1/controller/..." paths. This is defense-in-depth obscurity (it hides the
	// surface from drive-by scanners and is CDN-friendly), NOT a security boundary —
	// the boundary is the bearer tokens + the off-host signed trust-list. Normalized by
	// SetPathPrefix to "" or "/<seg>" (single leading slash, no trailing slash).
	pathPrefix string
}

// NewControllerHandler builds a ControllerHandler. operatorTokenHash is the hex
// SHA-256 of the operator's bearer token (callers hash the plaintext via
// controller.HashToken; the plaintext never reaches the handler). operatorName
// defaults to DefaultOperatorName when empty; the poll deadline defaults to
// defaultPollDeadline.
func NewControllerHandler(store controller.Store, tenant controller.TenantID, operatorTokenHash, operatorName string) *ControllerHandler {
	if operatorName == "" {
		operatorName = DefaultOperatorName
	}
	return &ControllerHandler{
		store:             store,
		tenant:            tenant,
		operatorTokenHash: operatorTokenHash,
		operatorName:      operatorName,
		pollDeadline:      defaultPollDeadline,
	}
}

// RegisterAgentRoutes registers the agent-facing controller routes on mux (served
// on the agent port), under basePath() (the optional secret prefix + the fixed
// /api/v1/controller/). /enroll is registered WITHOUT auth (reachable before the node
// has an API token); /config,/poll,/report go through requireNode (per-node bearer
// token). recoverPanics/cors are NOT applied here — controller requests are
// machine-to-machine JSON, with no browser CORS concern.
func (h *ControllerHandler) RegisterAgentRoutes(mux *http.ServeMux) {
	base := h.basePath()
	mux.HandleFunc(base+"enroll", h.HandleEnroll)
	mux.HandleFunc(base+"config", h.requireNode(h.HandleConfig))
	mux.HandleFunc(base+"poll", h.requireNode(h.HandlePoll))
	mux.HandleFunc(base+"report", h.requireNode(h.HandleReport))
}

// RegisterOperatorRoutes registers the operator-facing controller routes on mux
// (served on the operator/panel port), under basePath(). Each is wrapped with cors()
// so the browser panel — served from a possibly different origin and pointed at a
// configurable controller URL — can call it, and its CORS preflight (which carries no
// Authorization header) is answered before operatorAuth. All routes go through
// operatorAuth (the shared operator bearer token).
func (h *ControllerHandler) RegisterOperatorRoutes(mux *http.ServeMux) {
	base := h.basePath()
	op := func(next http.HandlerFunc) http.HandlerFunc { return h.cors(h.operatorAuth(next)) }
	mux.HandleFunc(base+"update-topology", op(h.HandleUpdateTopology))
	mux.HandleFunc(base+"stage", op(h.HandleStage))
	mux.HandleFunc(base+"promote", op(h.HandlePromote))
	mux.HandleFunc(base+"nodes", op(h.HandleNodes))
	mux.HandleFunc(base+"revoke", op(h.HandleRevoke))
	mux.HandleFunc(base+"audit", op(h.HandleAudit))
	mux.HandleFunc(base+"topology", op(h.HandleTopology))
	mux.HandleFunc(base+"enrollment-token", op(h.HandleEnrollmentToken))
}

// SetPathPrefix sets the optional secret path prefix the controller routes mount under.
// It normalizes to "" (no prefix) or "/<trimmed>" (single leading slash, no trailing
// slash). Call it before RegisterAgentRoutes/RegisterOperatorRoutes.
func (h *ControllerHandler) SetPathPrefix(prefix string) {
	if p := strings.Trim(strings.TrimSpace(prefix), "/"); p != "" {
		h.pathPrefix = "/" + p
	} else {
		h.pathPrefix = ""
	}
}

// basePath is the route prefix for all controller endpoints: the optional secret path
// prefix followed by the fixed "/api/v1/controller/".
func (h *ControllerHandler) basePath() string {
	return h.pathPrefix + "/api/v1/controller/"
}

// cors answers a browser CORS preflight (OPTIONS, which carries no auth) and stamps the
// headers the operator panel needs onto every response. Allow-Origin is "*" because the
// panel authenticates with a Bearer token (not cookies), so no credentialed-origin
// pinning is required; a deployment fronting the controller with a proxy may tighten it.
func (h *ControllerHandler) cors(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type")
		w.Header().Set("Access-Control-Max-Age", "600") // cache the preflight; the panel re-polls often
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next(w, r)
	}
}

// --- JSON request/response types ---

// enrollRequestJSON is the wire form of an enrollment request: the single-use
// enrollment token, the claimed node id, and the node's WireGuard PUBLIC key
// (never a private key).
type enrollRequestJSON struct {
	Token       string `json:"enrollment_token"`
	NodeID      string `json:"node_id"`
	WGPublicKey string `json:"wg_public_key"`
}

// enrollResponseJSON is the wire form of a successful enrollment: the node's issued
// per-node bearer token (returned ONCE, never stored in plaintext) and its node id.
type enrollResponseJSON struct {
	ApiToken string `json:"api_token"`
	NodeID   string `json:"node_id"`
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

// nodeJSON is the operator-facing view of one registry node. It deliberately
// exposes NO key material (neither the WG public key bytes nor the API token hash):
// only a boolean that a public key is on file. The operator panel lists fleet state
// without ever seeing secrets.
type nodeJSON struct {
	NodeID            string    `json:"node_id"`
	Status            string    `json:"status"`
	HasWGPublicKey    bool      `json:"has_wg_public_key"`
	DesiredGeneration int64     `json:"desired_generation"`
	AppliedGeneration int64     `json:"applied_generation"`
	LastChecksum      string    `json:"last_checksum"`
	LastHealth        string    `json:"last_health"`
	LastSeen          time.Time `json:"last_seen"`
	EnrolledAt        time.Time `json:"enrolled_at"`
}

// revokeRequestJSON is the operator's request to revoke (evict) a node: the target
// node id. Revocation flips the node to NodeRevoked and clears its API token so the
// node's bearer credential stops resolving immediately.
type revokeRequestJSON struct {
	NodeID string `json:"node_id"`
}

// revokeResponseJSON confirms a revoke: the node id and a revoked flag (always true
// on success, so a caller can branch without re-reading the registry).
type revokeResponseJSON struct {
	NodeID  string `json:"node_id"`
	Revoked bool   `json:"revoked"`
}

// auditEntryJSON is the operator-facing wire form of one audit entry. It is an
// explicit snake_case DTO (controller.AuditEntry has no json tags, so it would
// otherwise serialize as PascalCase) that exposes only the operator-relevant fields —
// the chain internals (PrevHash/Hash) are NOT leaked; their integrity is conveyed by
// auditResponseJSON.Verified.
type auditEntryJSON struct {
	Seq       int64     `json:"seq"`
	Timestamp time.Time `json:"timestamp"`
	Actor     string    `json:"actor"`
	Action    string    `json:"action"`
	NodeID    string    `json:"node_id"`
}

// auditResponseJSON is the operator-facing view of the audit chain: the entries in
// Seq order plus whether the hash chain verifies intact.
type auditResponseJSON struct {
	Entries  []auditEntryJSON `json:"entries"`
	Verified bool             `json:"verified"`
}

// enrollmentTokenRequestJSON is the operator's request to mint a single-use
// enrollment token for a node, with a TTL in seconds.
type enrollmentTokenRequestJSON struct {
	NodeID     string `json:"node_id"`
	TTLSeconds int64  `json:"ttl_seconds"`
}

// enrollmentTokenResponseJSON returns the freshly minted plaintext enrollment
// token ONCE. The controller stores only its hash, so this is the only chance to
// capture the plaintext.
type enrollmentTokenResponseJSON struct {
	Token string `json:"token"`
}

// --- handlers ---

// HandleEnroll runs the node-enrollment ceremony. It requires NO bearer token (it
// is the route a node calls before it has one); the single-use enrollment token is
// the authentication. The tenant is the configured one (single-tenant v1) — never
// taken from the request body. On success it returns the node's per-node API token
// ONCE.
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
	// Reserve the operator identity: a node must never enroll AS the operator. Doing
	// so would mint a node whose identity collides with the operator name stamped on
	// operator routes. The operator is authenticated by its own out-of-band token,
	// never through this node-enrollment path.
	if req.NodeID == h.operatorName {
		writeError(w, http.StatusForbidden, "node id is reserved")
		return
	}

	result, err := controller.Enroll(r.Context(), h.store, h.tenant, controller.EnrollRequest{
		Token:       req.Token,
		NodeID:      req.NodeID,
		WGPublicKey: req.WGPublicKey,
	}, time.Now())
	if err != nil {
		// Token errors are an authorization failure (bad/expired/consumed token);
		// everything else here is a malformed request. We map token errors to 401 and
		// the rest to 400 so a caller can distinguish "your token is no good" from a
		// malformed request.
		if errors.Is(err, controller.ErrTokenInvalid) || errors.Is(err, controller.ErrTokenConsumed) {
			writeError(w, http.StatusUnauthorized, err.Error())
			return
		}
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, enrollResponseJSON{
		ApiToken: result.APIToken,
		NodeID:   result.NodeID,
	})
}

// HandleConfig returns the CALLER's current bundle (the node taken from the bearer
// token, never the request). 404 before the first promote for that node. It also
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
// agent re-polls. The node identity comes from the bearer token (TouchLastSeen the
// caller).
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
// TouchLastSeen + an audit entry. The node is the bearer token's node; the report
// body carries only the applied generation, checksum, and a health string.
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
	if err := h.store.SetAppliedGeneration(r.Context(), tenant, node, req.AppliedGeneration, req.Checksum, req.Health); err != nil {
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

// HandleNodes lists the fleet registry for the operator panel (operator-only). It
// returns a []nodeJSON view that carries fleet state but NO key material.
func (h *ControllerHandler) HandleNodes(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "only GET is supported")
		return
	}
	tenant, _, ok := identity(r.Context())
	if !ok {
		writeError(w, http.StatusInternalServerError, "missing authenticated identity")
		return
	}
	nodes, err := h.store.ListNodes(r.Context(), tenant)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list nodes")
		return
	}
	out := make([]nodeJSON, 0, len(nodes))
	for _, n := range nodes {
		out = append(out, nodeJSON{
			NodeID:            n.NodeID,
			Status:            string(n.Status),
			HasWGPublicKey:    n.WGPublicKey != "",
			DesiredGeneration: n.DesiredGeneration,
			AppliedGeneration: n.AppliedGeneration,
			LastChecksum:      n.LastChecksum,
			LastHealth:        n.LastHealth,
			LastSeen:          n.LastSeen,
			EnrolledAt:        n.EnrolledAt,
		})
	}
	writeJSON(w, http.StatusOK, out)
}

// HandleRevoke evicts a node from the fleet (operator-only). It flips the node's
// Status to NodeRevoked (preserving every other field) AND clears its API token via
// RevokeNodeAPIToken, so the node's bearer credential stops resolving immediately
// (LookupNodeByAPIToken no longer maps it to an approved node). It is the operator
// counterpart to enrollment: 404 when the node is unknown, otherwise it records a
// "revoke" audit entry and returns {node_id, revoked:true}.
func (h *ControllerHandler) HandleRevoke(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "only POST is supported")
		return
	}
	tenant, operator, ok := identity(r.Context())
	if !ok {
		writeError(w, http.StatusInternalServerError, "missing authenticated identity")
		return
	}
	var req revokeRequestJSON
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if req.NodeID == "" {
		writeError(w, http.StatusBadRequest, "node_id is required")
		return
	}

	// Load the existing record so we can preserve every field while flipping Status;
	// an unknown node is a 404 (there is nothing to revoke).
	node, err := h.store.GetNode(r.Context(), tenant, req.NodeID)
	if err != nil {
		if errors.Is(err, controller.ErrNotFound) {
			writeError(w, http.StatusNotFound, "node not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to load node")
		return
	}

	// Flip to revoked, preserving all other fields. UpsertNode matches by NodeID.
	node.Status = controller.NodeRevoked
	if err := h.store.UpsertNode(r.Context(), tenant, node); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to revoke node")
		return
	}
	// Clear the API token + reverse index so the bearer credential stops resolving
	// immediately (idempotent: a no-op success if the node had no token).
	if err := h.store.RevokeNodeAPIToken(r.Context(), tenant, req.NodeID); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to revoke node token")
		return
	}
	if _, err := h.store.AppendAudit(r.Context(), tenant, controller.AuditEntry{
		Timestamp: time.Now(),
		Actor:     "operator:" + operator,
		Action:    "revoke",
		NodeID:    req.NodeID,
	}); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to append audit")
		return
	}
	writeJSON(w, http.StatusOK, revokeResponseJSON{NodeID: req.NodeID, Revoked: true})
}

// HandleAudit returns the tenant's audit chain plus whether it verifies intact
// (operator-only). verified is true when VerifyAuditChain finds no break.
func (h *ControllerHandler) HandleAudit(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "only GET is supported")
		return
	}
	tenant, _, ok := identity(r.Context())
	if !ok {
		writeError(w, http.StatusInternalServerError, "missing authenticated identity")
		return
	}
	entries, err := h.store.ListAudit(r.Context(), tenant)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list audit")
		return
	}
	out := make([]auditEntryJSON, len(entries))
	for i, e := range entries {
		out[i] = auditEntryJSON{
			Seq:       e.Seq,
			Timestamp: e.Timestamp,
			Actor:     e.Actor,
			Action:    e.Action,
			NodeID:    e.NodeID,
		}
	}
	writeJSON(w, http.StatusOK, auditResponseJSON{
		Entries:  out,
		Verified: controller.VerifyAuditChain(entries) == -1,
	})
}

// HandleTopology returns the currently stored topology JSON (operator-only). The
// stored bytes are public-keys-only and are returned verbatim. 404 before the first
// update-topology.
func (h *ControllerHandler) HandleTopology(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "only GET is supported")
		return
	}
	tenant, _, ok := identity(r.Context())
	if !ok {
		writeError(w, http.StatusInternalServerError, "missing authenticated identity")
		return
	}
	rec, err := h.store.GetTopology(r.Context(), tenant)
	if err != nil {
		if errors.Is(err, controller.ErrNotFound) {
			writeError(w, http.StatusNotFound, "no topology stored yet")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to load topology")
		return
	}
	// The stored JSON is returned verbatim (it is already valid JSON, validated at
	// update-topology time).
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(rec.JSON)
}

// HandleEnrollmentToken mints a single-use, node-scoped enrollment token
// (operator-only) and returns its plaintext ONCE. The controller stores only the
// token hash (CreateEnrollmentToken), so the plaintext cannot be recovered later.
func (h *ControllerHandler) HandleEnrollmentToken(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "only POST is supported")
		return
	}
	tenant, _, ok := identity(r.Context())
	if !ok {
		writeError(w, http.StatusInternalServerError, "missing authenticated identity")
		return
	}
	var req enrollmentTokenRequestJSON
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if req.NodeID == "" {
		writeError(w, http.StatusBadRequest, "node_id is required")
		return
	}
	// A node must never be granted an enrollment token AS the operator (the operator
	// identity is reserved; enrolling under it is rejected at /enroll, but reject the
	// token mint too for a clear, early error).
	if req.NodeID == h.operatorName {
		writeError(w, http.StatusForbidden, "node id is reserved")
		return
	}
	if req.TTLSeconds <= 0 {
		writeError(w, http.StatusBadRequest, "ttl_seconds must be positive")
		return
	}

	now := time.Now()
	plaintext, tok := controller.NewEnrollmentToken(req.NodeID, time.Duration(req.TTLSeconds)*time.Second, now)
	if err := h.store.CreateEnrollmentToken(r.Context(), tenant, tok); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create enrollment token")
		return
	}
	if _, err := h.store.AppendAudit(r.Context(), tenant, controller.AuditEntry{
		Timestamp: now,
		Actor:     "operator:" + h.operatorName,
		Action:    "enrollment-token",
		NodeID:    req.NodeID,
	}); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to append audit")
		return
	}
	writeJSON(w, http.StatusOK, enrollmentTokenResponseJSON{Token: plaintext})
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
// generation). A non-numeric, negative, or out-of-range value is a 400 — strconv
// rejects overflow, so a huge all-digit value cannot silently wrap to a negative
// generation (which would make WaitForGeneration return immediately).
func parseAfter(s string) (int64, error) {
	if s == "" {
		return 0, nil
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil || n < 0 {
		return 0, errors.New("after must be a non-negative integer")
	}
	return n, nil
}
