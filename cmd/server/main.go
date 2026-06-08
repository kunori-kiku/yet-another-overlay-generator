package main

import (
	"flag"
	"log"
	"os"

	"github.com/kunorikiku/yet-another-overlay-generator/internal/api"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/controller"
)

// Controller-mode environment gates. When BOTH the state dir and the tenant id are
// set, cmd/server builds the controller dependencies (FileStore) and serves the
// controller on two PLAIN-HTTP ports (operator/panel + agent). When either is unset,
// the server is EXACTLY as before: air-gap HTTP only, no controller routes.
//
// In controller mode the operator token is ALSO required: it authenticates every
// operator route. The server refuses to start in controller mode without it.
//
// Transport is plain HTTP on both ports (plan-4.5); TLS is delegated to a reverse
// proxy (nginx/caddy) and is never forced in-app.
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
)

func main() {
	addr := flag.String("addr", ":8080", "operator/panel + air-gap listen address (plain HTTP)")
	agentAddr := flag.String("agent-addr", "", "controller agent listen address (plain HTTP); default :9090 or YAOG_CONTROLLER_AGENT_ADDR")
	flag.Parse()

	stateDir := os.Getenv(envControllerStateDir)
	tenant := os.Getenv(envTenantID)

	server := api.NewServer()

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
	opToken := os.Getenv(envOperatorToken)
	if opToken == "" {
		// The operator token gates every operator route; starting without it would
		// either lock the operator out or (worse) leave the routes unguarded. Fail loud.
		log.Fatalf("controller: %s is required in controller mode", envOperatorToken)
	}

	store, err := controller.NewFileStore(stateDir)
	if err != nil {
		return err
	}

	ch := api.NewControllerHandler(store, controller.TenantID(tenant), controller.HashToken(opToken), api.DefaultOperatorName)
	ch.SetPathPrefix(os.Getenv(envPathPrefix))
	server.EnableController(ch)

	// Serve both ports concurrently; the first error from either wins. A buffered
	// channel of size 2 ensures the second goroutine's send never blocks on exit.
	errc := make(chan error, 2)
	go func() { errc <- server.ListenAndServe(addr) }()
	go func() { errc <- server.ListenAndServeAgent(agentAddr) }()
	return <-errc
}
