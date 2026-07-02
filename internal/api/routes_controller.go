package api

import (
	"context"
	"errors"
	"log"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/kunorikiku/yet-another-overlay-generator/internal/controller"
)

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
	// enrollLimiter throttles failed POST /enroll attempts (per source IP). /enroll is
	// the one agent route with NO bearer token — its only credential is a single-use
	// enrollment token — so it is the brute-force surface for guessing live tokens. A
	// SEPARATE limiter from loginLimiter keeps the two surfaces independent (enroll spray
	// must not lock out operator logins from the same IP, and vice versa) and uses the higher
	// maxEnrollFailures cap. It refunds on a successful enroll (succeed), so a SEQUENTIAL bulk
	// enroll never accumulates; the higher cap also absorbs a PARALLEL bootstrap of many nodes
	// behind one NAT (whose concurrent enrolls all reserve a slot before any refunds) without
	// false 429s, while still throttling a token-guessing sprayer.
	enrollLimiter *loginLimiter
	// nodeLimiter is a per-node (bearer-identity) REQUEST-rate limiter for the agent mux
	// (/config, /poll, /report, /telemetry, /rekey), used without succeed() as a fixed-window cap.
	// It bounds an authenticated node's request rate so one abusive/compromised node cannot DoS the
	// controller (e.g. a /telemetry flood forcing fsync'd, lock-contended writes). Keyed by node id.
	nodeLimiter *loginLimiter
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
	// trustedProxies is the set of reverse-proxy CIDRs/IPs whose X-Forwarded-For is trusted for
	// rate-limit keying (YAOG_TRUSTED_PROXIES). Empty (default) trusts nobody — clientIP uses the
	// direct peer and ignores forwarding headers. Set via SetTrustedProxies.
	trustedProxies []net.IPNet
	// secureCookie controls the Secure attribute on the session/CSRF cookies
	// (YAOG_SECURE_COOKIE, default true). Set false ONLY for local non-TLS development;
	// production MUST keep it true (the deployment fronts the controller with TLS).
	secureCookie bool
	// releaseClient is the egress-guarded HTTP client the assisted release-pin fetch
	// (HandleReleasePins) uses: a bounded timeout, a redirect cap that refuses non-http(s)
	// hops, and a dial-time private-IP reject (newReleasePinClient). A struct field so a
	// test can inject a permissive client pointed at a loopback test server — the production
	// guard rejects loopback by design, so the happy path cannot be exercised through it.
	releaseClient *http.Client
	// githubAPIBase is the GitHub REST API origin the asset-DISCOVERY fetch (HandleReleaseAssets)
	// hits DIRECTLY — "https://api.github.com" in production. Discovery does NOT route through the
	// gh-proxy: the proxy's shared API identity is globally rate-limited (a 403 for everyone), and
	// api.github.com is broadly reachable + the listing is non-custody metadata (the SHA pin is
	// fetched separately, still through the proxy). A struct field so a test can point it at a
	// loopback stub (with a permissive releaseClient) — the production egress guard still applies.
	githubAPIBase string
	// version is the controller's build version (cmd/server main.BuildVersion, stamped at release
	// link time via -ldflags -X; "dev" for a non-release build). Threaded in at construction
	// (immutable, never a runtime setter) and surfaced ONLY on the AUTHENTICATED operator /session
	// response — never on an anonymous surface beyond /api/health. The panel reads it to display the
	// controller version and (plan-8) to default the agent-rollout target + reject a target newer
	// than the controller. Empty is normalized to "dev" by the constructor.
	version string
}

// NewControllerHandler builds a ControllerHandler. operatorTokenHash is the hex
// SHA-256 of the operator's bearer token (callers hash the plaintext via
// controller.HashToken; the plaintext never reaches the handler). operatorName
// defaults to DefaultOperatorName when empty; the poll deadline defaults to
// defaultPollDeadline. buildVersion is the controller build version surfaced on the
// operator session (normalized to "dev" when empty).
func NewControllerHandler(store controller.Store, tenant controller.TenantID, operatorTokenHash, operatorName, buildVersion string) *ControllerHandler {
	if operatorName == "" {
		operatorName = DefaultOperatorName
	}
	if buildVersion == "" {
		buildVersion = "dev"
	}
	return &ControllerHandler{
		store:             store,
		tenant:            tenant,
		operatorTokenHash: operatorTokenHash,
		operatorName:      operatorName,
		version:           buildVersion,
		loginLimiter:      newLoginLimiter(),
		enrollLimiter:     newLimiter(maxEnrollFailures, loginWindow),
		nodeLimiter:       newLimiter(maxNodeRequestsPerWindow, nodeRateWindow),
		sessionTTL:        controller.DefaultSessionTTL,
		pollDeadline:      defaultPollDeadline,
		// Secure cookies by default: a non-TLS deployment must opt out explicitly
		// (YAOG_SECURE_COOKIE=false) for local development.
		secureCookie: true,
		// The assisted release-pin fetch egresses to github.com / the gh-proxy, so it gets
		// an egress-guarded client (SSRF private-IP reject + redirect/timeout caps).
		releaseClient: newReleasePinClient(),
		// Asset discovery hits the GitHub REST API directly (not via the gh-proxy).
		githubAPIBase: defaultGithubAPIBase,
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

// SetTrustedProxies configures the CIDRs/IPs of reverse proxies whose X-Forwarded-For header is
// trusted for rate-limit keying (clientIP). Empty (the default) trusts NOBODY — forwarding headers are
// ignored and rate-limits key on the direct peer. Each entry is a CIDR (e.g. 10.0.0.0/8) or a bare IP
// (normalized to a /32 or /128); invalid entries are skipped. NEVER configure 0.0.0.0/0 — it would
// trust a spoofed X-Forwarded-For from any client.
func (h *ControllerHandler) SetTrustedProxies(entries []string) {
	out := make([]net.IPNet, 0, len(entries))
	for _, e := range entries {
		e = strings.TrimSpace(e)
		if e == "" {
			continue
		}
		if _, ipnet, err := net.ParseCIDR(e); err == nil {
			out = append(out, *ipnet)
			continue
		}
		if ip := net.ParseIP(e); ip != nil {
			bits := 32
			if ip.To4() == nil {
				bits = 128
			}
			out = append(out, net.IPNet{IP: ip, Mask: net.CIDRMask(bits, bits)})
		}
	}
	h.trustedProxies = out
}

// SetSecureCookie sets the Secure attribute on the session/CSRF cookies. Production
// keeps it true; set false only for local non-TLS development.
func (h *ControllerHandler) SetSecureCookie(secure bool) {
	h.secureCookie = secure
}

// WarnInsecureControllerPosture emits a single startup WARNING when the controller is
// running in the dev-only TOFU posture: keystone OFF (no operator credential pinned, so a
// fetched bootstrap/bundle is trust-on-first-use with no off-host signature anchoring it)
// AND no TLS hint (secureCookie explicitly disabled via YAOG_SECURE_COOKIE=false, the
// signal that the deployment is NOT fronted by a TLS-terminating proxy). In that combination
// a network MITM can substitute the bootstrap/config the agent fetches, because nothing —
// neither transport TLS nor an off-host keystone signature — binds it. This is a DOCUMENTed
// rc.1 boundary, NOT a refusal: keystone-OFF + non-TLS is a supported dev mode and refusing
// it in code is deferred bootstrap-TOFU work (rc.2/GA). The rc.1 release notes carry the hard
// production requirement (keystone OR TLS-or-pinned-pubkey). Called from EnableController.
//
// keystone-OFF is determined by a single ErrNotFound from the store; any OTHER store error is
// logged-and-skipped (the warning is advisory, never a startup blocker). secureCookie left at
// its default (true) means the operator declared a TLS front, so no warning fires even with
// keystone OFF.
func (h *ControllerHandler) WarnInsecureControllerPosture(ctx context.Context) {
	if h.secureCookie {
		return // TLS hint present (the default / explicit production posture) — not the dev MITM window.
	}
	if _, err := h.store.GetOperatorCredential(ctx, h.tenant); err == nil {
		return // keystone ON: the off-host signature anchors the bundle regardless of transport.
	} else if !errors.Is(err, controller.ErrNotFound) {
		return // a non-ErrNotFound store fault: skip the advisory warning, do not block startup.
	}
	log.Printf("controller: WARNING: insecure dev posture — keystone is OFF (no operator credential pinned) " +
		"AND YAOG_SECURE_COOKIE=false (no TLS front). A network MITM can substitute the bootstrap script and " +
		"config the agent fetches; nothing anchors them. This is DEV-ONLY. Production REQUIRES a pinned keystone " +
		"OR a TLS-terminating/pinned-pubkey front (see the rc.1 release notes / docs/spec/security).")
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
	// /telemetry is the LIVE health heartbeat (beta9-smoke-hardening plan-1): per-node bearer auth like
	// /report, but observability-only — it updates conditions + last_seen and never touches deploy custody.
	mux.HandleFunc(base+"telemetry", h.requireNode(h.HandleTelemetry))
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
	// Manual-node bundle download (mixed-controller-local-mode plan-3): a MANUAL (agent-less) node has
	// no agent to pull /config, so the operator downloads its promoted, off-host-signed bundle as a ZIP
	// (?node=<id>) and installs it by hand. Manual-only; the bundle carries the key placeholder, never
	// real key material.
	mux.HandleFunc(base+"manual-node-bundle", op(h.HandleManualNodeBundle))
	mux.HandleFunc(base+"revoke", op(h.HandleRevoke))
	mux.HandleFunc(base+"audit", op(h.HandleAudit))
	mux.HandleFunc(base+"topology", op(h.HandleTopology))
	// Topology version history (plan-2, D7): list retained versions; a specific
	// version's payload is served by GET /topology?version=N above.
	mux.HandleFunc(base+"topology/versions", op(h.HandleTopologyVersions))
	mux.HandleFunc(base+"enrollment-token", op(h.HandleEnrollmentToken))
	mux.HandleFunc(base+"rekey-all", op(h.HandleRekeyAll))
	mux.HandleFunc(base+"clear-rekey", op(h.HandleClearRekey))
	// Bootstrap settings (plan-5.2): public agent URL, GitHub proxy, agent release URL.
	mux.HandleFunc(base+"settings", op(h.HandleSettings))
	// Assisted release-pin fetch (controller-panel-rollout-ui plan-1): fetch the per-asset
	// .sha256 sidecars for agent/mimic release assets through the gh-proxy and return Artifact
	// pins for operator REVIEW. A convenience only — trust stays the keystone-signed
	// artifacts.json the agent verifies against (see release_pins.go custody note).
	mux.HandleFunc(base+"release-pins", op(h.HandleReleasePins))
	// Assisted release-ASSET discovery (beta9-smoke-hardening plan-4): list a GitHub release's
	// .deb asset names (hitting the GitHub REST API directly, not the gh-proxy) so the mimic catalog
	// offers a pick-from checklist instead of hand-typed filenames. Convenience only — the SHA-256
	// pin is still fetched + saved separately (see release_assets.go custody note).
	mux.HandleFunc(base+"release-assets", op(h.HandleReleaseAssets))
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
