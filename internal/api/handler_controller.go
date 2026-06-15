package api

// handler_controller.go is the HTTP surface of the networked controller
// (plan-4.5). It exposes the controller core (Store + enrollment + compile) under
// two audience-named namespaces — operator/panel routes under /api/v1/operator/ and
// agent/node routes under /api/v1/agent/ — with JSON request/response bodies. Authentication and the
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

	"github.com/kunorikiku/yet-another-overlay-generator/internal/apierr"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/controller"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/model"
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
	// (e.g. "/s3cr3t" -> "/s3cr3t/api/v1/operator/..." and "/s3cr3t/api/v1/agent/...").
	// Empty = the bare "/api/v1/{operator,agent}/..." paths. They are defense-in-depth
	// obscurity (hiding the
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

// isReservedNodeID reports whether id is a reserved identity that may not be used for a node —
// today only the operator's own name (the operator authenticates out-of-band, never via the
// node-enrollment path). Centralized so enrollment and token-mint share one rule and a future
// reserved set (e.g. system nodes) extends in a single place. See HandleEnroll / HandleEnrollmentToken.
func (h *ControllerHandler) isReservedNodeID(id string) bool {
	return id == h.operatorName
}

// RegisterAgentRoutes registers the agent-facing controller routes on mux (served
// on the agent port), under AgentBasePath() (the optional agent secret prefix + the
// fixed /api/v1/agent/). /enroll is registered WITHOUT auth (reachable before the
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
	// Read-only, server-authoritative compile preview (the controller "Compile" button):
	// renders the enrolled subgraph WITHOUT staging/persisting, returning configs + skipped.
	mux.HandleFunc(base+"compile-preview", op(h.HandleCompilePreview))
	mux.HandleFunc(base+"promote", op(h.HandlePromote))
	mux.HandleFunc(base+"nodes", op(h.HandleNodes))
	mux.HandleFunc(base+"revoke", op(h.HandleRevoke))
	mux.HandleFunc(base+"audit", op(h.HandleAudit))
	mux.HandleFunc(base+"topology", op(h.HandleTopology))
	// Topology version history (plan-2, D7): list retained versions; a specific
	// version's payload is served by GET /topology?version=N above.
	mux.HandleFunc(base+"topology/versions", op(h.HandleTopologyVersions))
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
// operator secret prefix followed by the fixed "/api/v1/operator/". Exported so
// cmd/server can name the mounted base path in its startup log. The audience name in
// the path (operator vs agent) keeps the two surfaces distinct so a path-routing
// proxy can split them without the secret prefix doing double duty.
func (h *ControllerHandler) OperatorBasePath() string {
	return h.operatorPrefix + "/api/v1/operator/"
}

// AgentBasePath is the route prefix for the agent endpoints: the optional agent
// secret prefix followed by the fixed "/api/v1/agent/". Exported so cmd/server can
// name the mounted base path in its startup log. Distinct from the operator path so
// the public agent surface (enroll/config/poll/report/bootstrap) is unambiguous and
// can be exposed publicly while the operator panel stays behind a VPN.
func (h *ControllerHandler) AgentBasePath() string {
	return h.agentPrefix + "/api/v1/agent/"
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

// topologyVersionJSON is the wire form of one retained topology version's
// metadata (no payload — GET /topology?version=N serves the bytes).
type topologyVersionJSON struct {
	Version   int64     `json:"version"`
	UpdatedAt time.Time `json:"updated_at"`
	Bytes     int       `json:"bytes"`
}

// topologyVersionsResponseJSON is the wire form of the version list, newest
// first, plus the server's retention bound (so the panel can label the list).
type topologyVersionsResponseJSON struct {
	Versions []topologyVersionJSON `json:"versions"`
	Limit    int                   `json:"limit"`
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
	// Warning is a non-blocking advisory (plan-6): set when the node-id has no
	// matching node in the stored design, so the operator learns the token will mint
	// fine but the node will be SKIPPED at stage until it is added to the design.
	// Empty when the node-id is present (or no design is stored yet).
	Warning string `json:"warning,omitempty"`
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
		writeAPIError(w, apierr.New(apierr.CodeMethodNotAllowed).With("method", "POST"))
		return
	}
	var req enrollRequestJSON
	if err := decodeJSON(w, r, &req); err != nil {
		writeCodedOr(w, apierr.CodeReqInvalidBody, err)
		return
	}
	// Reserve the operator identity: a node must never enroll AS the operator. Doing
	// so would mint a node whose identity collides with the operator name stamped on
	// operator routes. The operator is authenticated by its own out-of-band token,
	// never through this node-enrollment path.
	if h.isReservedNodeID(req.NodeID) {
		writeAPIError(w, apierr.New(apierr.CodeNodeIDReserved))
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
			writeAPIError(w, apierr.New(apierr.CodeEnrollmentTokenInvalid).Wrap(err))
			return
		}
		// Duplicate WG pubkey under another node-id (plan-6): a conflict the operator
		// must resolve (revoke the other node or reuse its id), not a malformed request.
		if errors.Is(err, controller.ErrDuplicateWGKey) {
			writeAPIError(w, apierr.New(apierr.CodeDuplicateWGKey).Wrap(err))
			return
		}
		writeCodedOr(w, apierr.CodeReqInvalidBody, err)
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
		writeAPIError(w, apierr.New(apierr.CodeMethodNotAllowed).With("method", "GET"))
		return
	}
	tenant, node, ok := identity(r.Context())
	if !ok {
		writeAPIError(w, apierr.New(apierr.CodeInternalIdentityMissing))
		return
	}

	bundle, err := h.store.GetCurrentBundle(r.Context(), tenant, node)
	if err != nil {
		if errors.Is(err, controller.ErrNotFound) {
			writeAPIError(w, apierr.New(apierr.CodeConfigNotFound).Wrap(err))
			return
		}
		writeCodedOr(w, apierr.CodeInternalStorage, err)
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
			writeAPIError(w, apierr.New(apierr.CodeKeystoneNoSignedManifest))
			return
		}
		files["trustlist.json"] = base64.StdEncoding.EncodeToString(stored.TrustListJSON)
		files["trustlist.sig"] = base64.StdEncoding.EncodeToString(stored.SignatureJSON)
	} else if !errors.Is(err, controller.ErrNotFound) {
		writeCodedOr(w, apierr.CodeInternalStorage, err)
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
		writeAPIError(w, apierr.New(apierr.CodeMethodNotAllowed).With("method", "GET"))
		return
	}
	tenant, node, ok := identity(r.Context())
	if !ok {
		writeAPIError(w, apierr.New(apierr.CodeInternalIdentityMissing))
		return
	}
	after, err := parseAfter(r.URL.Query().Get("after"))
	if err != nil {
		writeCodedOr(w, apierr.CodeReqInvalidBody, err)
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
		writeCodedOr(w, apierr.CodeInternalStorage, err)
		return
	}
	writeJSON(w, http.StatusOK, pollResponseJSON{Generation: gen})
}

// HandleReport records an agent's apply outcome for ITSELF: SetAppliedGeneration +
// TouchLastSeen + an audit entry. The node is the bearer token's node; the report
// body carries only the applied generation, checksum, and a health string.
func (h *ControllerHandler) HandleReport(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeAPIError(w, apierr.New(apierr.CodeMethodNotAllowed).With("method", "POST"))
		return
	}
	tenant, node, ok := identity(r.Context())
	if !ok {
		writeAPIError(w, apierr.New(apierr.CodeInternalIdentityMissing))
		return
	}
	var req reportRequestJSON
	if err := decodeJSON(w, r, &req); err != nil {
		writeCodedOr(w, apierr.CodeReqInvalidBody, err)
		return
	}

	now := time.Now()
	if err := h.store.SetAppliedGeneration(r.Context(), tenant, node, req.AppliedGeneration, req.Checksum, req.Health); err != nil {
		if errors.Is(err, controller.ErrNotFound) {
			writeAPIError(w, apierr.New(apierr.CodeNodeNotFound).Wrap(err))
			return
		}
		writeCodedOr(w, apierr.CodeInternalStorage, err)
		return
	}
	_ = h.store.TouchLastSeen(r.Context(), tenant, node, now)
	if _, err := h.store.AppendAudit(r.Context(), tenant, controller.AuditEntry{
		Timestamp: now,
		Actor:     "agent:" + node,
		Action:    "report",
		NodeID:    node,
	}); err != nil {
		writeCodedOr(w, apierr.CodeInternalStorage, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// HandleUpdateTopology stores a new topology version (operator-only). The body is
// the public-keys-only topology JSON (the stage step compiles/validates it). The
// key-custody principle is ENFORCED at this API write boundary, not just asserted:
// a payload carrying any non-empty wireguard_private_key is refused with 400 (D4,
// fail-closed — the panel strips client-side; a key reaching this handler means a
// custody bug upstream and must blow up loudly, never be stored). The tenant is the
// configured one.
func (h *ControllerHandler) HandleUpdateTopology(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeAPIError(w, apierr.New(apierr.CodeMethodNotAllowed).With("method", "POST"))
		return
	}
	tenant, operator, ok := identity(r.Context())
	if !ok {
		writeAPIError(w, apierr.New(apierr.CodeInternalIdentityMissing))
		return
	}
	body, err := readControllerBody(w, r)
	if err != nil {
		writeCodedOr(w, apierr.CodeReqInvalidBody, err)
		return
	}
	// Custody gate: unmarshal into the model (not a substring match, which would
	// false-positive on names/notes) and refuse any private key material. Bodies are
	// always panel-produced model.Topology, so an unmarshal failure is equally a 400 —
	// storing bytes we cannot custody-check would reopen the hole this gate closes.
	// A *json.SyntaxError keeps the plain "not valid JSON" message (the old json.Valid
	// pre-check, now folded into this single parse).
	var topo model.Topology
	if err := json.Unmarshal(body, &topo); err != nil {
		var syn *json.SyntaxError
		if errors.As(err, &syn) {
			writeAPIError(w, apierr.New(apierr.CodeReqInvalidBody).Wrap(err))
			return
		}
		writeAPIError(w, apierr.New(apierr.CodeReqInvalidBody).Wrap(err))
		return
	}
	for _, n := range topo.Nodes {
		if n.WireGuardPrivateKey != "" {
			writeAPIError(w, apierr.New(apierr.CodeCustodyPrivateKey))
			return
		}
	}
	// Store the CANONICAL re-marshaled form, not the raw bytes: the gate above checks
	// the parsed view, and raw bytes could smuggle key material past it via duplicate
	// JSON keys (last-key-wins parsing) or fields outside the model. Canonicalizing
	// makes stored-bytes == checked-view by construction. The wire contract for this
	// endpoint is exactly model.Topology, so unknown fields are not data, they are a
	// bug — and they are dropped here rather than persisted unchecked.
	canonical, err := json.Marshal(topo)
	if err != nil {
		writeCodedOr(w, apierr.CodeInternalStorage, err)
		return
	}

	rec, err := h.store.PutTopology(r.Context(), tenant, canonical)
	if err != nil {
		writeCodedOr(w, apierr.CodeInternalStorage, err)
		return
	}
	// Post-commit audit is best-effort: the version is already stored, and converting
	// an audit-write hiccup into a 500 would tell the operator the action failed when
	// it committed (the retry would mint a duplicate version). Same convention as the
	// settings/login audits.
	_, _ = h.store.AppendAudit(r.Context(), tenant, controller.AuditEntry{
		Timestamp: time.Now(),
		Actor:     "operator:" + operator,
		Action:    "update-topology",
	})
	writeJSON(w, http.StatusOK, map[string]int64{"version": rec.Version})
}

// HandleStage compiles the enrolled subgraph of the stored topology into per-node
// bundles staged at the next generation (operator-only). It returns the StageResult.
func (h *ControllerHandler) HandleStage(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeAPIError(w, apierr.New(apierr.CodeMethodNotAllowed).With("method", "POST"))
		return
	}
	tenant, _, ok := identity(r.Context())
	if !ok {
		writeAPIError(w, apierr.New(apierr.CodeInternalIdentityMissing))
		return
	}
	result, err := controller.CompileAndStage(r.Context(), h.store, tenant, time.Now())
	if err != nil {
		// CompileAndStage wraps source-coded errors (%w), so writeCodedOr surfaces each at its
		// OWN status — compile constraints stay 422, but a keygen error (e.g. an AgentHeld node
		// with no registered public key) surfaces its native 400 and an export I/O failure its
		// 500. This is intentionally MORE precise than the old blanket 422; CodeStageFailed (422)
		// is only the fallback for an un-coded stage error. (See TestWriteCodedOr_* in handler_test.)
		writeCodedOr(w, apierr.CodeStageFailed, err)
		return
	}
	writeJSON(w, http.StatusOK, stageResponseJSON{
		Staged:            result.Staged,
		SkippedUnenrolled: result.SkippedUnenrolled,
		Generation:        result.Generation,
	})
}

// compilePreviewResponseJSON is the read-only compile-preview wire shape. It promotes the
// same fields as the air-gap CompileResponse (so the panel reuses CompilePreview/EdgeEditor
// verbatim) and adds skipped_unenrolled — the node IDs present in the topology but dropped
// from the render because they are not yet enrolled. The embedded *CompileResponse is nil
// when nothing is enrolled, so its fields are absent and only skipped_unenrolled is sent.
type compilePreviewResponseJSON struct {
	*CompileResponse
	SkippedUnenrolled []string `json:"skipped_unenrolled"`
}

// HandleCompilePreview compiles the enrolled subgraph of the POSTed current design and returns
// the rendered configs + the skipped (unenrolled) node IDs — WITHOUT staging, persisting pins,
// exporting bundles, or writing the audit log (operator-only). It is the read-only, server-
// authoritative compile the panel's "Compile" button drives in controller mode: the operator
// sees the server-computed allocation (ports, transit IPs, link-locals) and the full wg/babel/
// sysctl text BEFORE deploying, then adjusts the NAT-relevant fields and saves.
//
// Zero-knowledge: it drives controller.CompileSubgraph, whose render.GenerateKeys runs in
// AgentHeld custody — every [Interface] PrivateKey is PRIVATEKEY_PLACEHOLDER, never real key
// material — so the rendered text is safe to return to an authenticated operator. It MUST NOT
// reuse the air-gap HandleCompile (render.AirGap reconstructs real private keys).
func (h *ControllerHandler) HandleCompilePreview(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeAPIError(w, apierr.New(apierr.CodeMethodNotAllowed).With("method", "POST"))
		return
	}
	tenant, _, ok := identity(r.Context())
	if !ok {
		writeAPIError(w, apierr.New(apierr.CodeInternalIdentityMissing))
		return
	}

	// Compile the POSTed CURRENT design (the canvas the operator is editing) — NOT the stored
	// copy — so the operator can compile before saving ("Compile → adjust the NAT ip:port →
	// Save"). The body is public-keys-only (the panel strips private keys); enrollment and
	// public keys come from the registry via CompileSubgraph → enrolledSubgraph, so the POSTed
	// key fields are never trusted (and GenerateKeys(AgentHeld) emits placeholder private keys).
	topo, err := readTopology(w, r)
	if err != nil {
		writeCodedOr(w, apierr.CodeReqInvalidBody, err)
		return
	}

	nodes, err := h.store.ListNodes(r.Context(), tenant)
	if err != nil {
		writeCodedOr(w, apierr.CodeInternal, err)
		return
	}

	// The COMPILE HALF only — enrolled subgraph → AgentHeld keys → compile → render — with no
	// persistAllocations / Export / StageBundle / Prune / manifest / audit. That absence of
	// side effects is exactly what distinguishes a preview from a deploy.
	result, _, skipped, err := controller.CompileSubgraph(topo, nodes)
	if err != nil {
		// CompileSubgraph wraps source-coded errors (%w); writeCodedOr surfaces each at its own
		// status (compile constraints 422, keygen 400, etc.), CodeCompileFailed the fallback.
		writeCodedOr(w, apierr.CodeCompileFailed, err)
		return
	}
	if result == nil {
		// Nothing enrolled yet: report the skipped set so the panel can say "no node enrolled".
		writeJSON(w, http.StatusOK, compilePreviewResponseJSON{SkippedUnenrolled: skipped})
		return
	}
	writeJSON(w, http.StatusOK, compilePreviewResponseJSON{
		CompileResponse: &CompileResponse{
			Topology:         result.Topology,
			WireGuardConfigs: result.WireGuardConfigs,
			BabelConfigs:     result.BabelConfigs,
			SysctlConfigs:    result.SysctlConfigs,
			InstallScripts:   result.InstallScripts,
			DeployScripts:    result.DeployScripts,
			Warnings:         result.Warnings,
			Manifest:         result.Manifest,
		},
		SkippedUnenrolled: skipped,
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
		writeAPIError(w, apierr.New(apierr.CodeMethodNotAllowed).With("method", "POST"))
		return
	}
	tenant, operator, ok := identity(r.Context())
	if !ok {
		writeAPIError(w, apierr.New(apierr.CodeInternalIdentityMissing))
		return
	}
	gen, err := controller.PromoteStaged(r.Context(), h.store, tenant)
	if err != nil {
		if errors.Is(err, controller.ErrNoStagedBundle) {
			writeAPIError(w, apierr.New(apierr.CodeNoStagedBundle).Wrap(err))
			return
		}
		// The keystone gate (missing/unsigned/invalid manifest) is an operator-actionable
		// precondition failure, not an internal error: surface its message at 422.
		writeCodedOr(w, apierr.CodeStageFailed, err)
		return
	}
	// Audit the flip: promote is the action that changes what the fleet RUNS, so its
	// absence from the audit log was a real observability gap (plan-1). Best-effort:
	// the generation has ALREADY flipped fleet-wide — a 500 here would report a live
	// deploy as failed, and the operator's retry would 409 on the consumed stage.
	_, _ = h.store.AppendAudit(r.Context(), tenant, controller.AuditEntry{
		Timestamp: time.Now(),
		Actor:     "operator:" + operator,
		Action:    "promote",
	})
	writeJSON(w, http.StatusOK, generationResponseJSON{Generation: gen})
}

// HandleNodes lists the fleet registry for the operator panel (operator-only). It
// returns a []nodeJSON view that carries fleet state but NO key material.
func (h *ControllerHandler) HandleNodes(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeAPIError(w, apierr.New(apierr.CodeMethodNotAllowed).With("method", "GET"))
		return
	}
	tenant, _, ok := identity(r.Context())
	if !ok {
		writeAPIError(w, apierr.New(apierr.CodeInternalIdentityMissing))
		return
	}
	nodes, err := h.store.ListNodes(r.Context(), tenant)
	if err != nil {
		writeCodedOr(w, apierr.CodeInternalStorage, err)
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
		writeAPIError(w, apierr.New(apierr.CodeMethodNotAllowed).With("method", "POST"))
		return
	}
	tenant, operator, ok := identity(r.Context())
	if !ok {
		writeAPIError(w, apierr.New(apierr.CodeInternalIdentityMissing))
		return
	}
	var req revokeRequestJSON
	if err := decodeJSON(w, r, &req); err != nil {
		writeCodedOr(w, apierr.CodeReqInvalidBody, err)
		return
	}
	if req.NodeID == "" {
		writeAPIError(w, apierr.New(apierr.CodeReqFieldRequired).With("field", "node_id"))
		return
	}

	// Load the existing record so we can preserve every field while flipping Status;
	// an unknown node is a 404 (there is nothing to revoke).
	node, err := h.store.GetNode(r.Context(), tenant, req.NodeID)
	if err != nil {
		if errors.Is(err, controller.ErrNotFound) {
			writeAPIError(w, apierr.New(apierr.CodeNodeNotFound).Wrap(err))
			return
		}
		writeCodedOr(w, apierr.CodeInternalStorage, err)
		return
	}

	// Flip to revoked, preserving all other fields. Also clear any pending rekey flag:
	// a revoked node will never re-register, so a left-over RekeyRequested would keep the
	// panel's "rotating" gate stuck forever (a revoked node is excluded from the deploy
	// subgraph anyway). UpsertNode matches by NodeID.
	node.Status = controller.NodeRevoked
	node.RekeyRequested = false
	if err := h.store.UpsertNode(r.Context(), tenant, node); err != nil {
		writeCodedOr(w, apierr.CodeInternalStorage, err)
		return
	}
	// Clear the API token + reverse index so the bearer credential stops resolving
	// immediately (idempotent: a no-op success if the node had no token).
	if err := h.store.RevokeNodeAPIToken(r.Context(), tenant, req.NodeID); err != nil {
		writeCodedOr(w, apierr.CodeInternalStorage, err)
		return
	}
	if _, err := h.store.AppendAudit(r.Context(), tenant, controller.AuditEntry{
		Timestamp: time.Now(),
		Actor:     "operator:" + operator,
		Action:    "revoke",
		NodeID:    req.NodeID,
	}); err != nil {
		writeCodedOr(w, apierr.CodeInternalStorage, err)
		return
	}
	writeJSON(w, http.StatusOK, revokeResponseJSON{NodeID: req.NodeID, Revoked: true})
}

// HandleAudit returns the tenant's audit chain plus whether it verifies intact
// (operator-only). verified is true when VerifyAuditChain finds no break.
func (h *ControllerHandler) HandleAudit(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeAPIError(w, apierr.New(apierr.CodeMethodNotAllowed).With("method", "GET"))
		return
	}
	tenant, _, ok := identity(r.Context())
	if !ok {
		writeAPIError(w, apierr.New(apierr.CodeInternalIdentityMissing))
		return
	}
	entries, err := h.store.ListAudit(r.Context(), tenant)
	if err != nil {
		writeCodedOr(w, apierr.CodeInternalStorage, err)
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

// HandleTopology returns stored topology JSON (operator-only). With no query it
// returns the CURRENT record; `?version=N` returns one retained history version
// (plan-2, D7 — the recovery substrate for a bad overwrite). The stored bytes are
// public-keys-only and returned verbatim. 404 before the first update-topology, or
// for an unknown/pruned version.
func (h *ControllerHandler) HandleTopology(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeAPIError(w, apierr.New(apierr.CodeMethodNotAllowed).With("method", "GET"))
		return
	}
	tenant, _, ok := identity(r.Context())
	if !ok {
		writeAPIError(w, apierr.New(apierr.CodeInternalIdentityMissing))
		return
	}

	var rec controller.TopologyRecord
	var err error
	if vq := r.URL.Query().Get("version"); vq != "" {
		version, perr := strconv.ParseInt(vq, 10, 64)
		if perr != nil || version <= 0 {
			writeAPIError(w, apierr.New(apierr.CodeReqFieldInvalid).With("field", "version"))
			return
		}
		rec, err = h.store.GetTopologyVersion(r.Context(), tenant, version)
		if err != nil {
			if errors.Is(err, controller.ErrNotFound) {
				writeAPIError(w, apierr.New(apierr.CodeTopologyVersionNotFound))
				return
			}
			writeCodedOr(w, apierr.CodeInternalStorage, err)
			return
		}
	} else {
		rec, err = h.store.GetTopology(r.Context(), tenant)
		if err != nil {
			if errors.Is(err, controller.ErrNotFound) {
				writeAPIError(w, apierr.New(apierr.CodeNoTopologyStored))
				return
			}
			writeCodedOr(w, apierr.CodeInternalStorage, err)
			return
		}
	}
	// The stored JSON is returned verbatim (it is already valid JSON, validated at
	// update-topology time).
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(rec.JSON)
}

// HandleTopologyVersions lists the retained topology versions, newest first
// (operator-only; metadata only — fetch a payload via GET /topology?version=N).
func (h *ControllerHandler) HandleTopologyVersions(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeAPIError(w, apierr.New(apierr.CodeMethodNotAllowed).With("method", "GET"))
		return
	}
	tenant, _, ok := identity(r.Context())
	if !ok {
		writeAPIError(w, apierr.New(apierr.CodeInternalIdentityMissing))
		return
	}
	infos, err := h.store.ListTopologyVersions(r.Context(), tenant)
	if err != nil {
		writeCodedOr(w, apierr.CodeInternalStorage, err)
		return
	}
	out := make([]topologyVersionJSON, len(infos))
	for i, v := range infos {
		out[i] = topologyVersionJSON{Version: v.Version, UpdatedAt: v.UpdatedAt, Bytes: v.Bytes}
	}
	writeJSON(w, http.StatusOK, topologyVersionsResponseJSON{Versions: out, Limit: controller.TopologyHistoryLimit})
}

// HandleEnrollmentToken mints a single-use, node-scoped enrollment token
// (operator-only) and returns its plaintext ONCE. The controller stores only the
// token hash (CreateEnrollmentToken), so the plaintext cannot be recovered later.
func (h *ControllerHandler) HandleEnrollmentToken(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeAPIError(w, apierr.New(apierr.CodeMethodNotAllowed).With("method", "POST"))
		return
	}
	tenant, _, ok := identity(r.Context())
	if !ok {
		writeAPIError(w, apierr.New(apierr.CodeInternalIdentityMissing))
		return
	}
	var req enrollmentTokenRequestJSON
	if err := decodeJSON(w, r, &req); err != nil {
		writeCodedOr(w, apierr.CodeReqInvalidBody, err)
		return
	}
	if req.NodeID == "" {
		writeAPIError(w, apierr.New(apierr.CodeReqFieldRequired).With("field", "node_id"))
		return
	}
	// A node must never be granted an enrollment token AS the operator (the operator
	// identity is reserved; enrolling under it is rejected at /enroll, but reject the
	// token mint too for a clear, early error).
	if h.isReservedNodeID(req.NodeID) {
		writeAPIError(w, apierr.New(apierr.CodeNodeIDReserved))
		return
	}
	if req.TTLSeconds <= 0 {
		writeAPIError(w, apierr.New(apierr.CodeReqFieldInvalid).With("field", "ttl_seconds"))
		return
	}

	now := time.Now()
	plaintext, tok := controller.NewEnrollmentToken(req.NodeID, time.Duration(req.TTLSeconds)*time.Second, now)
	if err := h.store.CreateEnrollmentToken(r.Context(), tenant, tok); err != nil {
		writeCodedOr(w, apierr.CodeInternalStorage, err)
		return
	}
	if _, err := h.store.AppendAudit(r.Context(), tenant, controller.AuditEntry{
		Timestamp: now,
		Actor:     "operator:" + h.operatorName,
		Action:    "enrollment-token",
		NodeID:    req.NodeID,
	}); err != nil {
		writeCodedOr(w, apierr.CodeInternalStorage, err)
		return
	}
	// Design-membership advisory (plan-6, warn-not-block): if the stored design has
	// no node with this id, the token mints fine but the node will be skipped at
	// stage until it is added — surface that so an operator who fat-fingers an id
	// learns about it now. The warning is set ONLY when GetTopology succeeds AND the
	// id is absent. Any read failure yields NO warning by design: ErrNotFound means
	// no design is stored yet (pre-minting before designing is normal), and a
	// transient store error must not produce a false alarm — the advisory fails safe
	// to silent, never blocks the mint.
	resp := enrollmentTokenResponseJSON{Token: plaintext}
	if rec, err := h.store.GetTopology(r.Context(), tenant); err == nil && !topologyHasNode(rec.JSON, req.NodeID) {
		resp.Warning = "node-id not present in the stored design; it will be skipped at stage until added"
	}
	writeJSON(w, http.StatusOK, resp)
}

// topologyHasNode reports whether the stored topology JSON contains a node with the
// given id. A parse failure is treated as "present" (no false alarm on a topology we
// cannot read — the membership check is an advisory, not a gate).
func topologyHasNode(topoJSON []byte, nodeID string) bool {
	var topo model.Topology
	if err := json.Unmarshal(topoJSON, &topo); err != nil {
		return true
	}
	for _, n := range topo.Nodes {
		if n.ID == nodeID {
			return true
		}
	}
	return false
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
		writeAPIError(w, apierr.New(apierr.CodeMethodNotAllowed).With("method", "POST"))
		return
	}
	tenant, operator, ok := identity(r.Context())
	if !ok {
		writeAPIError(w, apierr.New(apierr.CodeInternalIdentityMissing))
		return
	}
	nodes, err := h.store.ListNodes(r.Context(), tenant)
	if err != nil {
		writeCodedOr(w, apierr.CodeInternalStorage, err)
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
			writeCodedOr(w, apierr.CodeInternalStorage, err)
			return
		}
		node.RekeyRequested = true
		if err := h.store.UpsertNode(r.Context(), tenant, node); err != nil {
			writeCodedOr(w, apierr.CodeInternalStorage, err)
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
		writeCodedOr(w, apierr.CodeInternalStorage, err)
		return
	}
	if _, err := h.store.AppendAudit(r.Context(), tenant, controller.AuditEntry{
		Timestamp: time.Now(),
		Actor:     "operator:" + operator,
		Action:    "rekey-request",
	}); err != nil {
		writeCodedOr(w, apierr.CodeInternalStorage, err)
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
		writeAPIError(w, apierr.New(apierr.CodeMethodNotAllowed).With("method", "POST"))
		return
	}
	tenant, node, ok := identity(r.Context())
	if !ok {
		writeAPIError(w, apierr.New(apierr.CodeInternalIdentityMissing))
		return
	}
	var req rekeyRequestJSON
	if err := decodeJSON(w, r, &req); err != nil {
		writeCodedOr(w, apierr.CodeReqInvalidBody, err)
		return
	}
	if req.WGPublicKey == "" {
		writeAPIError(w, apierr.New(apierr.CodeReqFieldRequired).With("field", "wg_public_key"))
		return
	}

	// controller.Rekey swaps the key + clears the flag under the per-tenant op lock
	// and enforces the SAME identity invariant as enroll (plan-6 review: the rekey
	// write path must not be able to create a duplicate the enroll dedupe forbids).
	if err := controller.Rekey(r.Context(), h.store, tenant, node, req.WGPublicKey, time.Now()); err != nil {
		if errors.Is(err, controller.ErrNotFound) {
			writeAPIError(w, apierr.New(apierr.CodeNodeNotFound).Wrap(err))
			return
		}
		if errors.Is(err, controller.ErrDuplicateWGKey) {
			writeAPIError(w, apierr.New(apierr.CodeDuplicateWGKey).Wrap(err))
			return
		}
		writeCodedOr(w, apierr.CodeInternalStorage, err)
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
		writeAPIError(w, apierr.New(apierr.CodeMethodNotAllowed).With("method", "POST"))
		return
	}
	tenant, operator, ok := identity(r.Context())
	if !ok {
		writeAPIError(w, apierr.New(apierr.CodeInternalIdentityMissing))
		return
	}
	var req operatorCredentialRequestJSON
	if err := decodeJSON(w, r, &req); err != nil {
		writeCodedOr(w, apierr.CodeReqInvalidBody, err)
		return
	}
	// Validate the PEM parses for the declared algorithm before pinning it.
	switch trustlist.Alg(req.Alg) {
	case trustlist.AlgEd25519, trustlist.AlgWebAuthnEdDSA:
		if _, err := trustlist.ParseEd25519PinPEM([]byte(req.PublicKeyPEM)); err != nil {
			writeAPIError(w, apierr.New(apierr.CodeReqFieldInvalid).With("field", "public_key_pem").Wrap(err))
			return
		}
	case trustlist.AlgWebAuthnES256:
		if _, err := trustlist.ParseES256Pin([]byte(req.PublicKeyPEM)); err != nil {
			writeAPIError(w, apierr.New(apierr.CodeReqFieldInvalid).With("field", "public_key_pem").Wrap(err))
			return
		}
	default:
		writeAPIError(w, apierr.New(apierr.CodeReqUnsupportedAlg).With("alg", req.Alg))
		return
	}

	if err := h.store.SetOperatorCredential(r.Context(), tenant, controller.OperatorCredential{
		Alg:          req.Alg,
		CredentialID: req.CredentialID,
		PublicKeyPEM: req.PublicKeyPEM,
		RPID:         req.RPID,
		Origin:       req.Origin,
	}); err != nil {
		writeCodedOr(w, apierr.CodeInternalStorage, err)
		return
	}
	if _, err := h.store.AppendAudit(r.Context(), tenant, controller.AuditEntry{
		Timestamp: time.Now(),
		Actor:     "operator:" + operator,
		Action:    "pin-operator-credential",
	}); err != nil {
		writeCodedOr(w, apierr.CodeInternalStorage, err)
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
		writeAPIError(w, apierr.New(apierr.CodeMethodNotAllowed).With("method", "GET"))
		return
	}
	tenant, _, ok := identity(r.Context())
	if !ok {
		writeAPIError(w, apierr.New(apierr.CodeInternalIdentityMissing))
		return
	}
	canonical, epoch, err := h.stagedManifest(r.Context(), tenant)
	if err != nil {
		if errors.Is(err, controller.ErrNotFound) {
			writeAPIError(w, apierr.New(apierr.CodeNoStagedManifest))
			return
		}
		writeCodedOr(w, apierr.CodeInternalStorage, err)
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
		writeAPIError(w, apierr.New(apierr.CodeMethodNotAllowed).With("method", "POST"))
		return
	}
	tenant, operator, ok := identity(r.Context())
	if !ok {
		writeAPIError(w, apierr.New(apierr.CodeInternalIdentityMissing))
		return
	}
	var req trustListSignatureRequestJSON
	if err := decodeJSON(w, r, &req); err != nil {
		writeCodedOr(w, apierr.CodeReqInvalidBody, err)
		return
	}

	// A signature is meaningless without a pinned credential to verify it against.
	cred, err := h.store.GetOperatorCredential(r.Context(), tenant)
	if err != nil {
		if errors.Is(err, controller.ErrNotFound) {
			writeAPIError(w, apierr.New(apierr.CodeNoPinnedCredential))
			return
		}
		writeCodedOr(w, apierr.CodeInternalStorage, err)
		return
	}

	// (a) Re-derive the staged manifest canonical bytes from the store.
	canonical, epoch, err := h.stagedManifest(r.Context(), tenant)
	if err != nil {
		if errors.Is(err, controller.ErrNotFound) {
			writeAPIError(w, apierr.New(apierr.CodeNoStagedManifest))
			return
		}
		writeCodedOr(w, apierr.CodeInternalStorage, err)
		return
	}

	// (b) Substitution guard: the operator must have signed EXACTLY these bytes.
	submitted, err := base64.StdEncoding.DecodeString(req.TrustListJSON)
	if err != nil {
		writeAPIError(w, apierr.New(apierr.CodeReqFieldInvalid).With("field", "trustlist_json"))
		return
	}
	if !bytes.Equal(submitted, canonical) {
		writeAPIError(w, apierr.New(apierr.CodeStagedManifestMismatch))
		return
	}

	// Parse the staged manifest so trustlist.Verify checks the signature over its exact
	// canonical bytes (Verify re-canonicalizes the parsed value internally).
	var manifest trustlist.TrustList
	if err := json.Unmarshal(canonical, &manifest); err != nil {
		writeAPIError(w, apierr.New(apierr.CodeInternalStorage).Wrap(err))
		return
	}

	// (c) Verify the off-host signature against the PINNED credential.
	pin, err := pinFromCredential(cred)
	if err != nil {
		writeAPIError(w, apierr.New(apierr.CodeInternalStorage).Wrap(err))
		return
	}
	if err := trustlist.Verify(manifest, req.Signed, pin); err != nil {
		writeAPIError(w, apierr.New(apierr.CodeManifestSignatureInvalid).Wrap(err))
		return
	}

	// (d) Store the signature onto the staged manifest record (canonical bytes + epoch
	// unchanged) and audit it.
	signedJSON, err := json.Marshal(req.Signed)
	if err != nil {
		writeCodedOr(w, apierr.CodeInternalStorage, err)
		return
	}
	if err := h.store.PutSignedTrustList(r.Context(), tenant, controller.StoredTrustList{
		TrustListJSON: canonical,
		SignatureJSON: signedJSON,
		Epoch:         epoch,
	}); err != nil {
		writeCodedOr(w, apierr.CodeInternalStorage, err)
		return
	}
	if _, err := h.store.AppendAudit(r.Context(), tenant, controller.AuditEntry{
		Timestamp: time.Now(),
		Actor:     "operator:" + operator,
		Action:    "sign-trustlist",
	}); err != nil {
		writeCodedOr(w, apierr.CodeInternalStorage, err)
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

// errBodyEmpty marks an empty request body where one is required. It is a coded
// *apierr.Error (CodeReqBodyEmpty, 400) so writeCodedOr surfaces it via errors.As with the
// right status; built once at init and only ever read after (never mutated), so sharing is safe.
var errBodyEmpty = apierr.New(apierr.CodeReqBodyEmpty)

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
