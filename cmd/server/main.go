package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/kunorikiku/yet-another-overlay-generator/internal/api"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/controller"
)

// controllerShutdownGrace bounds how long graceful shutdown waits for in-flight requests
// to finish after a SIGTERM/SIGINT before forcing exit. It comfortably exceeds a normal
// operator request; in-flight /poll long-polls are cancelled immediately by Shutdown, so
// the window is spent only on genuinely active work. Kept at/under a typical orchestrator
// termination grace (k8s default 30s) so we drain rather than get SIGKILLed mid-drain.
const controllerShutdownGrace = 25 * time.Second

// Controller-mode environment gates. When BOTH the state dir and the tenant id are
// set, cmd/server builds the controller dependencies (FileStore) and serves the
// controller on two PLAIN-HTTP ports (operator/panel + agent). When either is unset,
// the server is EXACTLY as before: air-gap HTTP only, no controller routes.
//
// In controller mode the operator token is OPTIONAL break-glass (plan-5.2): operator
// routes are authenticated PRIMARILY by password-login sessions (operators created via
// `yaog-server create-operator`), and the token, when set, is accepted alongside them
// as a recovery credential. The server starts even with neither configured (it logs a
// warning); an operator account can be created against the same store afterwards.
//
// Transport is plain HTTP on both ports (plan-4.5); TLS is delegated to a reverse
// proxy (nginx/caddy) and is never forced in-app. POST /login carries a plaintext
// password, so a TLS-terminating proxy in front of the controller is REQUIRED, not
// advisory.
const (
	// envControllerStateDir is the FileStore root for controller state. Its presence
	// (together with the tenant id) turns on controller mode.
	envControllerStateDir = "YAOG_CONTROLLER_STATE_DIR"
	// envTenantID is the single-tenant id (single-tenant v1). Pinned into the auth
	// chokepoint and used to scope every Store operation.
	envTenantID = "YAOG_TENANT_ID"
	// envOperatorToken is the operator's bearer token (plaintext). It is hashed
	// (controller.HashToken) before it reaches the handler; the plaintext is never
	// stored. REQUIRED in controller mode.
	envOperatorToken = "YAOG_CONTROLLER_OPERATOR_TOKEN"
	// envAgentAddr overrides the default agent-port listen address.
	envAgentAddr = "YAOG_CONTROLLER_AGENT_ADDR"
	// envOperatorPathPrefix is an optional secret path segment the OPERATOR/panel
	// routes (the :8080 mux) mount under, e.g. "s3cr3t" ->
	// "/s3cr3t/api/v1/operator/...". Empty = the bare paths. Defense-in-depth
	// obscurity, not a security boundary. Independent from the agent prefix so a
	// path-based proxy can route each audience to its own port on one hostname.
	envOperatorPathPrefix = "YAOG_OPERATOR_PATH_PREFIX"
	// envAgentPathPrefix is the same for the AGENT routes (the :9090 mux). The
	// bootstrap installer bakes this prefix into the agent's controller base URL.
	envAgentPathPrefix = "YAOG_AGENT_PATH_PREFIX"
	// envWebDir, when set, is the directory of the built frontend (the panel SPA) to
	// serve on the operator/panel port alongside the API. The Docker image sets it to
	// the embedded dist; unset = API only (Vite/dev or a reverse proxy serves the panel).
	envWebDir = "YAOG_WEB_DIR"
	// envPanelOrigin is a comma-separated allowlist of browser origins permitted to make
	// CREDENTIALED (session-cookie) cross-origin requests to the operator routes
	// (panel-appshell P5). Empty = same-origin only for the cookie path; the Bearer path
	// still works. e.g. "https://panel.example.com,https://ops.example.com".
	envPanelOrigin = "YAOG_PANEL_ORIGIN"
	// envSecureCookie toggles the Secure attribute on the session/CSRF cookies. Default
	// true; set "false"/"0"/"no" ONLY for local non-TLS development.
	envSecureCookie = "YAOG_SECURE_COOKIE"
)

// BuildVersion is the server's build version, overwritten at release link time via
// -ldflags "-X main.BuildVersion=<tag>" (see RELEASING.md). A non-release build reports "dev".
var BuildVersion = "dev"

func main() {
	// Subcommand: `yaog-server version` prints the build version and exits (before flag parsing).
	if len(os.Args) > 1 && (os.Args[1] == "version" || os.Args[1] == "--version" || os.Args[1] == "-v") {
		fmt.Println(BuildVersion)
		return
	}
	// Subcommand: `yaog-server create-operator ...` bootstraps an operator login
	// account into the controller FileStore, then exits. It must be intercepted before
	// the default flag set parses the serve flags.
	if len(os.Args) > 1 && os.Args[1] == "create-operator" {
		if err := runCreateOperator(os.Args[2:]); err != nil {
			log.Fatalf("create-operator: %v", err)
		}
		return
	}

	addr := flag.String("addr", ":8080", "operator/panel + air-gap listen address (plain HTTP)")
	agentAddr := flag.String("agent-addr", "", "controller agent listen address (plain HTTP); default :9090 or YAOG_CONTROLLER_AGENT_ADDR")
	flag.Parse()

	stateDir := os.Getenv(envControllerStateDir)
	tenant := os.Getenv(envTenantID)

	server := api.NewServer()

	// Optionally serve the built panel SPA from YAOG_WEB_DIR on the operator/panel port
	// (the Docker image sets this so one container serves panel + API). Applies in both
	// air-gap and controller mode; the /api/* routes take precedence over the SPA "/".
	if webDir := os.Getenv(envWebDir); webDir != "" {
		server.EnableStatic(webDir)
	}

	// Air-gap mode (the default): controller env not configured → serve exactly as
	// before. The mux and the HTTP serve path are untouched.
	if stateDir == "" || tenant == "" {
		if err := server.ListenAndServe(*addr); err != nil {
			log.Fatalf("server: %v", err)
		}
		return
	}

	// Controller mode: resolve the agent address (flag > env > default).
	resolvedAgentAddr := *agentAddr
	if resolvedAgentAddr == "" {
		resolvedAgentAddr = os.Getenv(envAgentAddr)
	}
	if resolvedAgentAddr == "" {
		resolvedAgentAddr = ":9090"
	}

	if err := serveController(server, *addr, resolvedAgentAddr, stateDir, tenant); err != nil {
		log.Fatalf("controller: %v", err)
	}
}

// serveController builds the controller dependencies (FileStore), arms the
// controller routes across the server's two muxes, and serves the operator/panel
// port and the agent port concurrently as plain HTTP. It is only reached under the
// controller env gate. It returns the first serve error from either port.
func serveController(server *api.Server, addr, agentAddr, stateDir, tenant string) error {
	// Fail LOUD on the removed env (D3 clean break): silently ignoring a stale
	// YAOG_CONTROLLER_PATH_PREFIX would mount everything at the bare paths while the
	// whole enrolled fleet keeps polling the old prefixed URLs — a fleet-wide 404 with
	// nothing in the log naming the cause. Refusing to start names the fix instead.
	if os.Getenv("YAOG_CONTROLLER_PATH_PREFIX") != "" {
		return errors.New("YAOG_CONTROLLER_PATH_PREFIX was removed: set YAOG_OPERATOR_PATH_PREFIX (operator/panel API, :8080) and YAOG_AGENT_PATH_PREFIX (agent API, :9090) instead, then update your proxy rules per audience")
	}

	// The operator token is now OPTIONAL: it is the BREAK-GLASS credential, accepted
	// alongside password-login sessions (plan-5.2). When unset, only operator accounts
	// (created via `yaog-server create-operator`) can authenticate operator routes.
	opToken := os.Getenv(envOperatorToken)
	opTokenHash := ""
	if opToken != "" {
		opTokenHash = controller.HashToken(opToken)
	}

	store, err := controller.NewFileStore(stateDir)
	if err != nil {
		return err
	}

	// Surface a clear startup warning if NEITHER a break-glass token NOR any operator
	// account exists: operator routes would be inaccessible until one is created. The
	// server still starts (create-operator can run against the same store), so this is
	// a warning, not a fatal.
	if opTokenHash == "" {
		if ops, err := store.ListOperators(context.Background(), controller.TenantID(tenant)); err == nil && len(ops) == 0 {
			log.Printf("controller: WARNING: no operator credentials configured — set %s or run 'yaog-server create-operator'; operator routes are inaccessible until an operator account exists",
				envOperatorToken)
		}
	}

	// B3 (plan-8 Phase 7): NEW login passkeys now require a non-empty Origin (the verifier's
	// advisory origin gate is authoritative for login pins). Existing pins saved BEFORE this
	// requirement may have an empty Origin; we do NOT enforce on them (that would lock out an
	// operator mid-upgrade), but we surface a one-line warning per such operator so the owner
	// knows to re-register the passkey to gain origin binding. Best-effort; a list error is
	// non-fatal here (the no-operator warning above already covers the read).
	if ops, err := store.ListOperators(context.Background(), controller.TenantID(tenant)); err == nil {
		for _, op := range ops {
			if op.LoginCredential != nil && strings.TrimSpace(op.LoginCredential.Origin) == "" {
				log.Printf("controller: WARNING: operator %q has a legacy login passkey with no Origin — origin binding is not enforced for it; re-register the passkey to gain origin binding",
					op.Username)
			}
		}
	}

	// Legacy operator-credential binding (plan-8 review #2): the forward-only validate-at-pin
	// gate (validateOperatorCredentialBinding) only covers credentials pinned AFTER it existed.
	// An operator credential pinned BEFORE this fix may carry a legacy RPID/Origin containing
	// whitespace or a shell metacharacter, which then word-splits through the unquoted ${OP_FLAGS}
	// in the rendered bootstrap script — a flag-injection vector for an already-authenticated
	// operator. We surface it as an ADVISORY startup WARNING (mirroring the B3 legacy login-pin
	// precedent above) so the owner re-pins the credential; we do NOT refuse to start or clear the
	// pin, which would lock the operator's keystone out mid-upgrade. Documented as a residual in
	// docs/spec/security/security.md (S11). Best-effort; a not-found/read error is non-fatal here.
	if cred, err := store.GetOperatorCredential(context.Background(), controller.TenantID(tenant)); err == nil {
		if field := api.UnsafeOperatorCredentialBindingField(cred); field != "" {
			log.Printf("controller: WARNING: tenant %q has a legacy operator credential whose %s contains whitespace or a shell metacharacter — re-pin the operator credential to remove it; until then the bootstrap script's unquoted OP_FLAGS expansion of that field is a flag-injection risk for an authenticated operator",
				tenant, field)
		}
	}

	ch := api.NewControllerHandler(store, controller.TenantID(tenant), opTokenHash, api.DefaultOperatorName)
	ch.SetOperatorPathPrefix(os.Getenv(envOperatorPathPrefix))
	ch.SetAgentPathPrefix(os.Getenv(envAgentPathPrefix))
	// Credentialed-CORS allowlist for cross-origin panel hosting (cookie auth). Empty =
	// same-origin only for the cookie path (the Bearer path still works).
	if origins := os.Getenv(envPanelOrigin); strings.TrimSpace(origins) != "" {
		ch.SetPanelOrigins(strings.Split(origins, ","))
	}
	// Secure cookies default ON; an explicit false/0/no opts out for non-TLS dev.
	if v := strings.ToLower(strings.TrimSpace(os.Getenv(envSecureCookie))); v != "" {
		ch.SetSecureCookie(!(v == "false" || v == "0" || v == "no"))
	}
	server.EnableController(ch)

	// Name both mounted base paths at startup so a proxy/tunnel misroute (operator
	// traffic at the agent port or a prefix mismatch) is diagnosable from the container
	// log in seconds. The prefixes are secrets-by-obscurity only — this log stays inside
	// the operator's own container, so naming them here is acceptable and deliberate.
	log.Printf("controller: operator routes at %s (addr %s); agent routes at %s (addr %s)",
		ch.OperatorBasePath(), addr, ch.AgentBasePath(), agentAddr)

	// Register the shutdown signal handler BEFORE spawning the serve goroutines: a SIGTERM
	// delivered during the startup window must be captured (and drained) rather than falling
	// through to the kernel's default terminate-immediately disposition.
	sigc := make(chan os.Signal, 1)
	signal.Notify(sigc, syscall.SIGINT, syscall.SIGTERM)

	// Serve both ports concurrently; the first error from either wins. A buffered
	// channel of size 2 ensures the second goroutine's send never blocks on exit.
	errc := make(chan error, 2)
	go func() { errc <- server.ListenAndServe(addr) }()
	go func() { errc <- server.ListenAndServeAgent(agentAddr) }()

	// Graceful shutdown: on SIGTERM/SIGINT (a `docker stop`, a k8s pod termination, or
	// Ctrl-C), drain BOTH listeners within a bounded grace window before exiting, so a
	// restart does not sever an in-flight operator request or agent report mid-write.
	// Long-polls return at once (Shutdown cancels their context), so a polling fleet does
	// not stretch every restart to the full grace. A genuine listener error (e.g. address
	// already in use) still wins immediately via errc.
	select {
	case err := <-errc:
		return err
	case sig := <-sigc:
		log.Printf("controller: received %s, draining listeners (grace %s)…", sig, controllerShutdownGrace)
		ctx, cancel := context.WithTimeout(context.Background(), controllerShutdownGrace)
		defer cancel()
		if err := server.Shutdown(ctx); err != nil {
			// The grace window elapsed with connections still active: the listeners are
			// closed regardless, so exit cleanly — affected agents simply re-poll/re-report.
			log.Printf("controller: shutdown grace elapsed with connections still active: %v", err)
		}
		log.Printf("controller: listeners drained, exiting")
		return nil
	}
}
