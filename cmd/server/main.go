package main

import (
	"context"
	"flag"
	"log"
	"os"
	"strings"

	"github.com/kunorikiku/yet-another-overlay-generator/internal/api"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/controller"
)

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
	// envPathPrefix is an optional secret path segment the controller routes mount
	// under (both ports), e.g. "s3cr3t" -> "/s3cr3t/api/v1/controller/...". Empty =
	// the bare paths. Defense-in-depth obscurity, not a security boundary.
	envPathPrefix = "YAOG_CONTROLLER_PATH_PREFIX"
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

func main() {
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

	ch := api.NewControllerHandler(store, controller.TenantID(tenant), opTokenHash, api.DefaultOperatorName)
	ch.SetPathPrefix(os.Getenv(envPathPrefix))
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

	// Serve both ports concurrently; the first error from either wins. A buffered
	// channel of size 2 ensures the second goroutine's send never blocks on exit.
	errc := make(chan error, 2)
	go func() { errc <- server.ListenAndServe(addr) }()
	go func() { errc <- server.ListenAndServeAgent(agentAddr) }()
	return <-errc
}
