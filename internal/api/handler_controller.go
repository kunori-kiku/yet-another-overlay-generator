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
//   - agent routes (/enroll,/config,/poll,/report,/rekey) → RegisterAgentRoutes.
//   - operator routes (everything else, incl. /rekey-all) → RegisterOperatorRoutes.
//
// Transport is plain HTTP; TLS is delegated to a reverse proxy (plan-4.5). Bearer
// tokens authenticate both kinds of caller (per-node tokens for agents, a single
// operator token for the operator).

import (
	"bytes"
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
	"github.com/kunorikiku/yet-another-overlay-generator/internal/trustlist"
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
	store  controller.Store
	tenant controller.TenantID
	// operatorTokenHash is the hex SHA-256 of the optional BREAK-GLASS operator token
	// (YAOG_CONTROLLER_OPERATOR_TOKEN). Empty disables it: with no break-glass token,
	// only password-login sessions authenticate operator routes. When set it is a
	// standing admin credential, accepted alongside sessions (a recovery path).
	operatorTokenHash string
	operatorName      string
	// loginLimiter throttles failed POST /login attempts (per username + source IP).
	loginLimiter *loginLimiter
	// sessionTTL is the lifetime of a session minted at /login.
	sessionTTL time.Duration
	// pollDeadline bounds a single /poll long-poll (defaultPollDeadline when zero).
	pollDeadline time.Duration
	// operatorPrefix and agentPrefix are optional secret path segments the OPERATOR
	// routes (panel port) and AGENT routes (agent port) mount under, independently
	// (e.g. "/s3cr3t" -> "/s3cr3t/api/v1/controller/..."). Empty = the bare
	// "/api/v1/controller/..." paths. They are defense-in-depth obscurity (hiding the
	// surface from drive-by scanners, CDN-friendly), NOT a security boundary — the
	// boundary is the bearer tokens + the off-host signed trust-list. Two independent
	// prefixes (YAOG_OPERATOR_PATH_PREFIX / YAOG_AGENT_PATH_PREFIX) let a path-based
	// proxy on one hostname route each audience to its own port without the
	// shared-prefix ambiguity that misrouted operator logins to the agent port.
	// Normalized by the setters to "" or "/<seg>" (single leading slash, no trailing).
	operatorPrefix string
	agentPrefix    string
	// panelOriginAllowlist is the set of exact browser origins (scheme://host[:port])
	// permitted to make CREDENTIALED (cookie-bearing) cross-origin requests to the
	// operator routes (YAOG_PANEL_ORIGIN). For a matching Origin, cors() reflects it +
	// Access-Control-Allow-Credentials: true (never "*" with credentials). Empty =
	// same-origin only for the cookie path (the Bearer path still works via the "*"
	// non-credentialed fallback). Set via SetPanelOrigins.
	panelOriginAllowlist []string
	// secureCookie controls the Secure attribute on the session/CSRF cookies
	// (YAOG_SECURE_COOKIE, default true). Set false ONLY for local non-TLS development;
	// production MUST keep it true (the deployment fronts the controller with TLS).
	secureCookie bool
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
		loginLimiter:      newLoginLimiter(),
		sessionTTL:        controller.DefaultSessionTTL,
		pollDeadline:      defaultPollDeadline,
		// Secure cookies by default: a non-TLS deployment must opt out explicitly
		// (YAOG_SECURE_COOKIE=false) for local development.
		secureCookie: true,
	}
}

// SetPanelOrigins sets the credentialed-CORS allowlist of browser origins permitted to
// make cookie-bearing cross-origin requests to the operator routes. Each entry is an
// exact origin (scheme://host[:port]); empty/blank entries are dropped. Call before
// RegisterOperatorRoutes.
func (h *ControllerHandler) SetPanelOrigins(origins []string) {
	out := make([]string, 0, len(origins))
	for _, o := range origins {
		if o = strings.TrimSpace(o); o != "" {
			out = append(out, strings.TrimRight(o, "/"))
		}
	}
	h.panelOriginAllowlist = out
}

// SetSecureCookie sets the Secure attribute on the session/CSRF cookies. Production
// keeps it true; set false only for local non-TLS development.
func (h *ControllerHandler) SetSecureCookie(secure bool) {
	h.secureCookie = secure
}

// originAllowed reports whether origin is in the credentialed-CORS allowlist (exact
// match, trailing slash ignored).
func (h *ControllerHandler) originAllowed(origin string) bool {
	origin = strings.TrimRight(origin, "/")
	for _, o := range h.panelOriginAllowlist {
		if o == origin {
			return true
		}
	}
	return false
}

// RegisterAgentRoutes registers the agent-facing controller routes on mux (served
// on the agent port), under AgentBasePath() (the optional agent secret prefix + the
// fixed /api/v1/controller/). /enroll is registered WITHOUT auth (reachable before the
// node has an API token); /config,/poll,/report,/rekey go through requireNode (per-node
// bearer token). recoverPanics/cors are NOT applied here — controller requests are
// machine-to-machine JSON, with no browser CORS concern.
func (h *ControllerHandler) RegisterAgentRoutes(mux *http.ServeMux) {
	base := h.AgentBasePath()
	mux.HandleFunc(base+"enroll", h.HandleEnroll)
	mux.HandleFunc(base+"config", h.requireNode(h.HandleConfig))
	mux.HandleFunc(base+"poll", h.requireNode(h.HandlePoll))
	mux.HandleFunc(base+"report", h.requireNode(h.HandleReport))
	mux.HandleFunc(base+"rekey", h.requireNode(h.HandleRekey))
	// Bootstrap (plan-5.2): the one-shot install script, served WITHOUT auth (it is
	// generic; the single-use enrollment token is a flag the operator supplies).
	mux.HandleFunc(base+"bootstrap", h.HandleBootstrap)
}

// RegisterOperatorRoutes registers the operator-facing controller routes on mux
// (served on the operator/panel port), under OperatorBasePath(). Each is wrapped with
// cors() so the browser panel — served from a possibly different origin and pointed at
// a configurable controller URL — can call it, and its CORS preflight (which carries no
// Authorization header) is answered before operatorAuth. All routes go through
// operatorAuth (the shared operator bearer token).
func (h *ControllerHandler) RegisterOperatorRoutes(mux *http.ServeMux) {
	base := h.OperatorBasePath()
	op := func(next http.HandlerFunc) http.HandlerFunc { return h.cors(h.operatorAuth(next)) }
	// Operator login (plan-5.2): /login is UNAUTHENTICATED (reachable before the
	// operator has a session) — cors-wrapped but NOT operatorAuth; it verifies a
	// password and mints a session. /logout is authenticated and revokes the
	// presented session.
	mux.HandleFunc(base+"login", h.cors(h.HandleLogin))
	mux.HandleFunc(base+"logout", op(h.HandleLogout))
	// Session probe (panel-appshell P5): the panel calls GET /session on mount to derive
	// login state from the httpOnly cookie after a refresh (no token read in JS).
	mux.HandleFunc(base+"session", op(h.HandleSession))
	// Passwordless passkey login (plan-5.2): UNAUTHENTICATED (reachable before a session),
	// cors-wrapped but NOT operatorAuth. begin issues a single-use random challenge for a
	// username; finish verifies the WebAuthn assertion and mints a session (no password).
	mux.HandleFunc(base+"login/passkey/begin", h.cors(h.HandlePasskeyLoginBegin))
	mux.HandleFunc(base+"login/passkey/finish", h.cors(h.HandlePasskeyLoginFinish))
	// TOTP login 2FA (plan-5.2): manage the current operator's optional second factor.
	mux.HandleFunc(base+"totp/status", op(h.HandleTOTPStatus))
	mux.HandleFunc(base+"totp/enroll", op(h.HandleTOTPEnroll))
	mux.HandleFunc(base+"totp/confirm", op(h.HandleTOTPConfirm))
	mux.HandleFunc(base+"totp/disable", op(h.HandleTOTPDisable))
	// Passkey login management (plan-5.2): register/disable the current operator's login
	// passkey (the password+passkey 2FA factor and the passwordless credential).
	mux.HandleFunc(base+"passkey/status", op(h.HandlePasskeyStatus))
	mux.HandleFunc(base+"passkey/register", op(h.HandlePasskeyRegister))
	mux.HandleFunc(base+"passkey/disable", op(h.HandlePasskeyDisable))
	mux.HandleFunc(base+"update-topology", op(h.HandleUpdateTopology))
	mux.HandleFunc(base+"stage", op(h.HandleStage))
	mux.HandleFunc(base+"promote", op(h.HandlePromote))
	mux.HandleFunc(base+"nodes", op(h.HandleNodes))
	mux.HandleFunc(base+"revoke", op(h.HandleRevoke))
	mux.HandleFunc(base+"audit", op(h.HandleAudit))
	mux.HandleFunc(base+"topology", op(h.HandleTopology))
	mux.HandleFunc(base+"enrollment-token", op(h.HandleEnrollmentToken))
	mux.HandleFunc(base+"rekey-all", op(h.HandleRekeyAll))
	// Bootstrap settings (plan-5.2): public agent URL, GitHub proxy, agent release URL.
	mux.HandleFunc(base+"settings", op(h.HandleSettings))
	// Keystone (plan-5.1b): pin the off-host operator credential, fetch the canonical
	// trust-list bytes to sign, and submit the off-host signature.
	mux.HandleFunc(base+"operator-credential", op(h.HandleOperatorCredential))
	mux.HandleFunc(base+"trustlist", op(h.HandleTrustList))
	mux.HandleFunc(base+"trustlist-signature", op(h.HandleTrustListSignature))
}

// normalizePathPrefix normalizes a configured secret path prefix to "" (no prefix)
// or "/<trimmed>" (single leading slash, no trailing slash).
func normalizePathPrefix(prefix string) string {
	if p := strings.Trim(strings.TrimSpace(prefix), "/"); p != "" {
		return "/" + p
	}
	return ""
}

// SetOperatorPathPrefix sets the optional secret path prefix the OPERATOR routes
// mount under (YAOG_OPERATOR_PATH_PREFIX). Call it before RegisterOperatorRoutes.
func (h *ControllerHandler) SetOperatorPathPrefix(prefix string) {
	h.operatorPrefix = normalizePathPrefix(prefix)
}

// SetAgentPathPrefix sets the optional secret path prefix the AGENT routes mount
// under (YAOG_AGENT_PATH_PREFIX). Call it before RegisterAgentRoutes.
func (h *ControllerHandler) SetAgentPathPrefix(prefix string) {
	h.agentPrefix = normalizePathPrefix(prefix)
}

// OperatorBasePath is the route prefix for the operator endpoints: the optional
// operator secret prefix followed by the fixed "/api/v1/controller/". Exported so
// cmd/server can name the mounted base path in its startup log.
func (h *ControllerHandler) OperatorBasePath() string {
	return h.operatorPrefix + "/api/v1/controller/"
}

// AgentBasePath is the route prefix for the agent endpoints: the optional agent
// secret prefix followed by the fixed "/api/v1/controller/". Exported so cmd/server
// can name the mounted base path in its startup log.
func (h *ControllerHandler) AgentBasePath() string {
	return h.agentPrefix + "/api/v1/controller/"
}

// cors answers a browser CORS preflight (OPTIONS, which carries no auth) and stamps the
// headers the operator panel needs onto every response.
//
// Two modes. (1) When the request Origin is in the credentialed allowlist
// (YAOG_PANEL_ORIGIN), reflect that EXACT origin + Access-Control-Allow-Credentials:
// true + Vary: Origin, so the browser sends/stores the session cookie cross-origin. A
// wildcard "*" is NEVER emitted together with credentials (the browser would reject it,
// and it would be unsafe). (2) Otherwise fall back to the historical permissive
// non-credentialed "*" for the Bearer-token path (no cookies attached). Same-origin
// panels (the Docker default) need no allowlist — the browser applies no CORS and the
// cookie flows normally.
func (h *ControllerHandler) cors(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type, "+csrfHeaderName)
		w.Header().Set("Access-Control-Max-Age", "600") // cache the preflight; the panel re-polls often
		// The Allow-Origin value depends on the request Origin in BOTH branches, so advertise
		// Vary: Origin unconditionally to keep a shared cache from cross-serving origins.
		w.Header().Add("Vary", "Origin")
		if origin := r.Header.Get("Origin"); origin != "" && h.originAllowed(origin) {
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Set("Access-Control-Allow-Credentials", "true")
		} else {
			w.Header().Set("Access-Control-Allow-Origin", "*")
		}
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
	// RekeyRequested signals the agent that the operator has requested a fleet-wide
	// key rotation: on the next fetch the agent regenerates its WireGuard key,
	// re-registers the new PUBLIC key via POST /rekey, and waits for the operator's
	// redeploy rather than applying this (now stale) bundle.
	RekeyRequested bool `json:"rekey_requested"`
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
	// RekeyRequested is true while the node is pending a key rotation (the operator
	// requested one and the agent has not yet re-registered its new public key). The
	// panel renders a "rekeying" badge from this flag. No key material is exposed.
	RekeyRequested bool `json:"rekey_requested"`
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

// rekeyAllResponseJSON is the operator-facing result of a fleet-wide key-rotation
// request: the count of APPROVED nodes flagged for rotation.
type rekeyAllResponseJSON struct {
	Requested int `json:"requested"`
}

// rekeyRequestJSON is the agent's re-registration of its rotated WireGuard PUBLIC
// key (never a private key). The node is the bearer token's node, never the body.
type rekeyRequestJSON struct {
	WGPublicKey string `json:"wg_public_key"`
}

// rekeyResponseJSON confirms an agent rekey re-registration.
type rekeyResponseJSON struct {
	OK bool `json:"ok"`
}

// operatorCredentialRequestJSON is the operator's request to pin the off-host signing
// credential (the keystone trust anchor). public_key_pem is the PKIX ("PUBLIC KEY")
// PEM; alg selects how it is parsed (ed25519 / webauthn-es256 / webauthn-eddsa);
// rpid/origin are the WebAuthn relying-party binding values (empty for raw Ed25519).
type operatorCredentialRequestJSON struct {
	Alg          string `json:"alg"`
	CredentialID string `json:"credential_id"`
	PublicKeyPEM string `json:"public_key_pem"`
	RPID         string `json:"rpid"`
	Origin       string `json:"origin"`
}

// trustListResponseJSON returns the canonical bytes the operator must sign
// (base64-encoded) plus the membership epoch those bytes carry. The panel signs
// challenge = SHA256(decoded trustlist_json).
type trustListResponseJSON struct {
	TrustListJSON string `json:"trustlist_json"`
	Epoch         int64  `json:"epoch"`
}

// trustListSignatureRequestJSON is the operator's submission of a signed trust-list:
// the base64 of the canonical bytes the operator actually signed (substitution guard)
// plus the trustlist.SignedTrustList detached-signature artifact.
type trustListSignatureRequestJSON struct {
	TrustListJSON string                    `json:"trustlist_json"`
	Signed        trustlist.SignedTrustList `json:"signed"`
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

	// Read the caller's registry record so the response can carry its
	// RekeyRequested flag (the agent reacts to it by regenerating + re-registering
	// its WireGuard key). A failed read must not deny the node its config — the flag
	// then defaults to false and the agent re-learns it on a later fetch.
	rekeyRequested := false
	if n, err := h.store.GetNode(r.Context(), tenant, node); err == nil {
		rekeyRequested = n.RekeyRequested
	}

	files := make(map[string]string, len(bundle.Files)+2)
	for path, content := range bundle.Files {
		files[path] = base64.StdEncoding.EncodeToString(content)
	}

	// KEYSTONE: when an operator credential is pinned, APPEND the off-host-signed
	// membership manifest (trustlist.json) and its detached signature (trustlist.sig)
	// to the SERVED file map — NOT to the bundle's checksums.sha256. The manifest binds
	// each node's checksums.sha256 digest, so it cannot live inside that very checksum
	// set; the agent verifies it against its pinned credential and asserts this node's
	// member.BundleSHA256 matches hex(sha256(checksums.sha256)). A promote cannot occur
	// without a valid signed manifest (PromoteStaged gate), so a promoted bundle always
	// has one to serve; we still fail closed if it is somehow absent.
	if _, err := h.store.GetOperatorCredential(r.Context(), tenant); err == nil {
		stored, err := h.store.GetCurrentSignedTrustList(r.Context(), tenant)
		if err != nil || len(stored.SignatureJSON) == 0 {
			writeError(w, http.StatusInternalServerError, "keystone is enabled but no signed membership manifest is available to serve")
			return
		}
		files["trustlist.json"] = base64.StdEncoding.EncodeToString(stored.TrustListJSON)
		files["trustlist.sig"] = base64.StdEncoding.EncodeToString(stored.SignatureJSON)
	} else if !errors.Is(err, controller.ErrNotFound) {
		writeError(w, http.StatusInternalServerError, "failed to load operator credential")
		return
	}

	writeJSON(w, http.StatusOK, configResponseJSON{
		Generation:     bundle.Generation,
		Files:          files,
		RekeyRequested: rekeyRequested,
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
//
// It drives controller.PromoteStaged, which enforces the KEYSTONE gate: when an
// operator credential is pinned (keystone ON), the promote is refused unless a valid
// off-host signature exists over the staged membership manifest. A missing/unsigned/
// invalid manifest is a 422 (the deploy cannot go live without the off-host proof); an
// empty staged set is a 409.
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
	gen, err := controller.PromoteStaged(r.Context(), h.store, tenant)
	if err != nil {
		if errors.Is(err, controller.ErrNoStagedBundle) {
			writeError(w, http.StatusConflict, err.Error())
			return
		}
		// The keystone gate (missing/unsigned/invalid manifest) is an operator-actionable
		// precondition failure, not an internal error: surface its message at 422.
		writeError(w, http.StatusUnprocessableEntity, err.Error())
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
			RekeyRequested:    n.RekeyRequested,
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

	// Flip to revoked, preserving all other fields. Also clear any pending rekey flag:
	// a revoked node will never re-register, so a left-over RekeyRequested would keep the
	// panel's "rotating" gate stuck forever (a revoked node is excluded from the deploy
	// subgraph anyway). UpsertNode matches by NodeID.
	node.Status = controller.NodeRevoked
	node.RekeyRequested = false
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

// HandleRekeyAll requests a fleet-wide WireGuard key rotation (operator-only). It
// flags every APPROVED node with RekeyRequested=true (read-modify-write via
// GetNode/UpsertNode so every other field is preserved); pending/revoked nodes are
// left untouched. After flagging, it calls Store.BumpGeneration to WAKE every parked
// daemon agent: those agents long-poll WaitForGeneration, which fires ONLY on a
// generation advance, so without the bump a flagged agent would never wake to see
// rekey_requested (the deadlock this fixes). The bump changes NO bundle —
// GetCurrentBundle still returns the last promoted bundle — so a woken agent sees the
// rekey signal on /config and skip-applies (rotate+re-register) rather than treating
// the bumped generation as a deploy. Each flagged node's agent then learns of the
// request on its next /config fetch (rekey_requested=true), regenerates its key, and
// re-registers the new PUBLIC key via /rekey (which clears the flag). This is the
// ROUTINE security tier: rolling EXISTING members' keys never adds or removes a
// member, so the operator token authorizes it in v1. Returns {requested:<count>}.
func (h *ControllerHandler) HandleRekeyAll(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "only POST is supported")
		return
	}
	tenant, operator, ok := identity(r.Context())
	if !ok {
		writeError(w, http.StatusInternalServerError, "missing authenticated identity")
		return
	}
	nodes, err := h.store.ListNodes(r.Context(), tenant)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list nodes")
		return
	}
	requested := 0
	for _, n := range nodes {
		if n.Status != controller.NodeApproved {
			continue
		}
		// Re-read under the same shape as /revoke so a concurrent mutation does not
		// clobber a field; flip the flag while preserving everything else.
		node, err := h.store.GetNode(r.Context(), tenant, n.NodeID)
		if err != nil {
			if errors.Is(err, controller.ErrNotFound) {
				// The node vanished between the list and the read; skip it.
				continue
			}
			writeError(w, http.StatusInternalServerError, "failed to load node")
			return
		}
		node.RekeyRequested = true
		if err := h.store.UpsertNode(r.Context(), tenant, node); err != nil {
			writeError(w, http.StatusInternalServerError, "failed to flag node for rekey")
			return
		}
		requested++
	}
	// WAKE the fleet: bump the generation so parked daemon agents (blocked in
	// WaitForGeneration, which only wakes on an advance) wake, Fetch /config, and see
	// rekey_requested. This bumps the counter ONLY — it changes no bundle, so a woken
	// agent skip-applies on the rekey signal instead of treating it as a deploy. Done
	// even when requested==0 so the bump is unconditional and idempotent (a no-op-flag
	// rekey-all still records the audit entry below).
	if _, err := h.store.BumpGeneration(r.Context(), tenant); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to wake agents for rekey")
		return
	}
	if _, err := h.store.AppendAudit(r.Context(), tenant, controller.AuditEntry{
		Timestamp: time.Now(),
		Actor:     "operator:" + operator,
		Action:    "rekey-request",
	}); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to append audit")
		return
	}
	writeJSON(w, http.StatusOK, rekeyAllResponseJSON{Requested: requested})
}

// HandleRekey re-registers the CALLER's rotated WireGuard PUBLIC key (the node from
// the bearer token, never the request body). It stamps the new public key onto the
// node record and clears RekeyRequested, all via GetNode/UpsertNode so every other
// field is preserved. It is the agent's response to a rekey_requested=true /config:
// the controller never sees a private key (zero-knowledge custody). An empty
// wg_public_key is a 400. Returns {ok:true}.
func (h *ControllerHandler) HandleRekey(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "only POST is supported")
		return
	}
	tenant, node, ok := identity(r.Context())
	if !ok {
		writeError(w, http.StatusInternalServerError, "missing authenticated identity")
		return
	}
	var req rekeyRequestJSON
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if req.WGPublicKey == "" {
		writeError(w, http.StatusBadRequest, "wg_public_key is required")
		return
	}

	// Load the existing record so we preserve every field while swapping the public
	// key and clearing the flag. UpsertNode matches by NodeID.
	rec, err := h.store.GetNode(r.Context(), tenant, node)
	if err != nil {
		if errors.Is(err, controller.ErrNotFound) {
			writeError(w, http.StatusNotFound, "node not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to load node")
		return
	}
	rec.WGPublicKey = req.WGPublicKey
	rec.RekeyRequested = false
	if err := h.store.UpsertNode(r.Context(), tenant, rec); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to record rekey")
		return
	}
	if _, err := h.store.AppendAudit(r.Context(), tenant, controller.AuditEntry{
		Timestamp: time.Now(),
		Actor:     "agent:" + node,
		Action:    "rekey",
		NodeID:    node,
	}); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to append audit")
		return
	}
	writeJSON(w, http.StatusOK, rekeyResponseJSON{OK: true})
}

// --- keystone (off-host membership manifest) ---

// stagedManifest returns the tenant's STAGED membership manifest (the to-be-signed
// canonical bytes CompileAndStage stored) and its epoch. The manifest binds, per member,
// the node's bundle digest (BundleSHA256 = hex(sha256(checksums.sha256))), so the
// off-host signature covers what RUNS — install.sh + every config — not merely the
// member list. The manifest is built at STAGE time (not projected from the live
// registry), so GET /trustlist and POST /trustlist-signature both operate over the exact
// bytes a node will be served. ErrNotFound surfaces when nothing has been staged yet.
func (h *ControllerHandler) stagedManifest(ctx context.Context, t controller.TenantID) (canonical []byte, epoch int64, err error) {
	stored, err := h.store.GetCurrentSignedTrustList(ctx, t)
	if err != nil {
		return nil, 0, err
	}
	return stored.TrustListJSON, stored.Epoch, nil
}

// pinFromParts builds the trustlist.PinnedCredential the verifier checks against from a
// credential's raw fields, parsing the PEM by algorithm and carrying through the WebAuthn
// RPID/Origin binding values. It is shared by the keystone membership credential
// (pinFromCredential) and the per-operator passkey LOGIN credential
// (pinFromLoginCredential, handler_passkey.go) — the same WebAuthn verification, two
// callers.
func pinFromParts(alg, credentialID, publicKeyPEM, rpid, origin string) (trustlist.PinnedCredential, error) {
	pin := trustlist.PinnedCredential{
		Alg:          trustlist.Alg(alg),
		CredentialID: credentialID,
		RPID:         rpid,
		Origin:       origin,
	}
	switch trustlist.Alg(alg) {
	case trustlist.AlgEd25519, trustlist.AlgWebAuthnEdDSA:
		pub, err := trustlist.ParseEd25519PinPEM([]byte(publicKeyPEM))
		if err != nil {
			return trustlist.PinnedCredential{}, err
		}
		pin.Ed25519Pub = pub
	case trustlist.AlgWebAuthnES256:
		pub, err := trustlist.ParseES256Pin([]byte(publicKeyPEM))
		if err != nil {
			return trustlist.PinnedCredential{}, err
		}
		pin.ES256Pub = pub
	default:
		return trustlist.PinnedCredential{}, errors.New("unsupported operator credential algorithm")
	}
	return pin, nil
}

// pinFromCredential builds the trustlist.PinnedCredential the verifier checks against
// from a stored keystone OperatorCredential.
func pinFromCredential(c controller.OperatorCredential) (trustlist.PinnedCredential, error) {
	return pinFromParts(c.Alg, c.CredentialID, c.PublicKeyPEM, c.RPID, c.Origin)
}

// HandleOperatorCredential pins the off-host operator signing credential (operator-only),
// turning KEYSTONE ON for the tenant. The public_key_pem MUST parse for the declared
// alg (so a malformed pin is rejected here, not at verify time). On success it stores
// the credential and records a "pin-operator-credential" audit entry.
func (h *ControllerHandler) HandleOperatorCredential(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "only POST is supported")
		return
	}
	tenant, operator, ok := identity(r.Context())
	if !ok {
		writeError(w, http.StatusInternalServerError, "missing authenticated identity")
		return
	}
	var req operatorCredentialRequestJSON
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	// Validate the PEM parses for the declared algorithm before pinning it.
	switch trustlist.Alg(req.Alg) {
	case trustlist.AlgEd25519, trustlist.AlgWebAuthnEdDSA:
		if _, err := trustlist.ParseEd25519PinPEM([]byte(req.PublicKeyPEM)); err != nil {
			writeError(w, http.StatusBadRequest, "invalid public_key_pem for alg: "+err.Error())
			return
		}
	case trustlist.AlgWebAuthnES256:
		if _, err := trustlist.ParseES256Pin([]byte(req.PublicKeyPEM)); err != nil {
			writeError(w, http.StatusBadRequest, "invalid public_key_pem for alg: "+err.Error())
			return
		}
	default:
		writeError(w, http.StatusBadRequest, "unsupported alg")
		return
	}

	if err := h.store.SetOperatorCredential(r.Context(), tenant, controller.OperatorCredential{
		Alg:          req.Alg,
		CredentialID: req.CredentialID,
		PublicKeyPEM: req.PublicKeyPEM,
		RPID:         req.RPID,
		Origin:       req.Origin,
	}); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to pin operator credential")
		return
	}
	if _, err := h.store.AppendAudit(r.Context(), tenant, controller.AuditEntry{
		Timestamp: time.Now(),
		Actor:     "operator:" + operator,
		Action:    "pin-operator-credential",
	}); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to append audit")
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// HandleTrustList returns the STAGED membership manifest's canonical bytes (base64) plus
// its epoch (operator-only). These are EXACTLY the bytes that get signed and verified —
// the panel signs challenge = SHA256(decoded bytes). Each member carries its bundle
// digest, so the off-host signature covers what RUNS (install.sh + every config), not
// only the member list. 404 when nothing has been staged yet (stage a deploy first).
func (h *ControllerHandler) HandleTrustList(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "only GET is supported")
		return
	}
	tenant, _, ok := identity(r.Context())
	if !ok {
		writeError(w, http.StatusInternalServerError, "missing authenticated identity")
		return
	}
	canonical, epoch, err := h.stagedManifest(r.Context(), tenant)
	if err != nil {
		if errors.Is(err, controller.ErrNotFound) {
			writeError(w, http.StatusNotFound, "no staged membership manifest; stage a deploy before signing")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to load staged manifest")
		return
	}
	writeJSON(w, http.StatusOK, trustListResponseJSON{
		TrustListJSON: base64.StdEncoding.EncodeToString(canonical),
		Epoch:         epoch,
	})
}

// HandleTrustListSignature accepts the operator's off-host signature over the staged
// membership manifest (operator-only). It (a) re-derives the staged manifest canonical
// bytes server-side from the store; (b) rejects a submitted trustlist_json that does not
// byte-equal them (409 substitution guard — the operator must sign exactly what was
// staged); (c) builds the pinned credential from the stored OperatorCredential and
// verifies the signature with trustlist.Verify (400 on any verification failure); (d)
// stores the signature onto the staged manifest record (keeping its canonical bytes +
// epoch), records a "sign-trustlist" audit entry, and returns 200. A 412 is returned
// when no operator credential is pinned; a 404 when nothing has been staged.
func (h *ControllerHandler) HandleTrustListSignature(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "only POST is supported")
		return
	}
	tenant, operator, ok := identity(r.Context())
	if !ok {
		writeError(w, http.StatusInternalServerError, "missing authenticated identity")
		return
	}
	var req trustListSignatureRequestJSON
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	// A signature is meaningless without a pinned credential to verify it against.
	cred, err := h.store.GetOperatorCredential(r.Context(), tenant)
	if err != nil {
		if errors.Is(err, controller.ErrNotFound) {
			writeError(w, http.StatusPreconditionFailed, "no operator credential is pinned; pin one before signing")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to load operator credential")
		return
	}

	// (a) Re-derive the staged manifest canonical bytes from the store.
	canonical, epoch, err := h.stagedManifest(r.Context(), tenant)
	if err != nil {
		if errors.Is(err, controller.ErrNotFound) {
			writeError(w, http.StatusNotFound, "no staged membership manifest; stage a deploy before signing")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to load staged manifest")
		return
	}

	// (b) Substitution guard: the operator must have signed EXACTLY these bytes.
	submitted, err := base64.StdEncoding.DecodeString(req.TrustListJSON)
	if err != nil {
		writeError(w, http.StatusBadRequest, "trustlist_json is not valid base64")
		return
	}
	if !bytes.Equal(submitted, canonical) {
		writeError(w, http.StatusConflict, "submitted manifest does not match the current staged manifest; re-fetch and re-sign")
		return
	}

	// Parse the staged manifest so trustlist.Verify checks the signature over its exact
	// canonical bytes (Verify re-canonicalizes the parsed value internally).
	var manifest trustlist.TrustList
	if err := json.Unmarshal(canonical, &manifest); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to parse staged manifest: "+err.Error())
		return
	}

	// (c) Verify the off-host signature against the PINNED credential.
	pin, err := pinFromCredential(cred)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load pinned credential: "+err.Error())
		return
	}
	if err := trustlist.Verify(manifest, req.Signed, pin); err != nil {
		writeError(w, http.StatusBadRequest, "manifest signature verification failed: "+err.Error())
		return
	}

	// (d) Store the signature onto the staged manifest record (canonical bytes + epoch
	// unchanged) and audit it.
	signedJSON, err := json.Marshal(req.Signed)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to marshal signature")
		return
	}
	if err := h.store.PutSignedTrustList(r.Context(), tenant, controller.StoredTrustList{
		TrustListJSON: canonical,
		SignatureJSON: signedJSON,
		Epoch:         epoch,
	}); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to store signed manifest")
		return
	}
	if _, err := h.store.AppendAudit(r.Context(), tenant, controller.AuditEntry{
		Timestamp: time.Now(),
		Actor:     "operator:" + operator,
		Action:    "sign-trustlist",
	}); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to append audit")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "epoch": epoch})
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
