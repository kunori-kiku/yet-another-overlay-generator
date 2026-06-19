// Command e2eserver is a TEST-ONLY full-stack bring-up for the Playwright E2E layer
// (plan-13 / milestone 3.1). It is NOT a release artifact: .github/workflows/release.yml
// builds explicit targets only (./cmd/server, ./cmd/compiler, ./cmd/agent), so this main
// is excluded from shipped binaries by construction.
//
// It boots the REAL internal/api server (same handlers/routes/auth as cmd/server) in one
// of two modes, reusing the production seams — never a reimplementation:
//
//	--mode controller   mirrors cmd/server's serveController: a FileStore-backed
//	                    ControllerHandler with a seeded operator account (the shared
//	                    controller.SeedOperator write path) + one pre-minted enrollment
//	                    token, EnableController, and the built panel served from the same
//	                    origin (EnableStatic). Serves the operator/panel mux and the agent
//	                    mux on two ports.
//	--mode airgap       mirrors cmd/server's air-gap boot: NewServer + EnableStatic, NO
//	                    EnableController (so Server.operatorAuth stays nil and the air-gap
//	                    compute routes — /api/{validate,compile,export,deploy-script} — are
//	                    reachable UNauthenticated via gateAirgap's passthrough). Serves one
//	                    port. This proves the local-mode panel's compute round-trip.
//
// BUILD TAG: this binary MUST be built with `-tags airgap`. The four air-gap compute
// routes live behind //go:build airgap (plan-7 / 1.7): only the -tags airgap build of
// internal/api links + registers them, so a default build's --mode airgap boot would 404
// /api/compile. The binary itself references only exported api.Server methods present in
// BOTH builds, so it compiles either way; the tag is what makes the air-gap boot real.
//
// Why two boots from ONE binary instead of env-tweaking cmd/server: EnableController
// arms Server.operatorAuth UNCONDITIONALLY (server.go), which gates the air-gap compute
// routes (401 without auth). A single controller boot therefore cannot also serve the
// UNauthenticated /api/compile the local-mode panel sends. The two boots make that split
// observable (DoD #5: air-gap boot 200 unauth, controller boot 401 unauth).
//
// Ports default to 127.0.0.1:0 (loopback, OS-assigned) for hermetic parallel-safe runs.
// Each boot binds its listener(s) FIRST, then prints exactly one machine-readable line so
// the Playwright globalSetup can parse the resolved ports (and the controller's enrollment
// token) without a fixed-port assumption:
//
//	E2E_READY mode=<mode> panel=<host:port> [agent=<host:port>] [enroll=<token>]
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"time"

	"github.com/kunorikiku/yet-another-overlay-generator/internal/api"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/controller"
)

// enrollTokenTTL bounds the pre-minted enrollment token. The harness enrolls within
// seconds of boot; an hour is comfortable slack and matches controller_client_test.go.
const enrollTokenTTL = time.Hour

func main() {
	mode := flag.String("mode", "controller", "boot mode: controller | airgap")
	stateDir := flag.String("state-dir", "", "controller FileStore root (controller mode; required)")
	tenant := flag.String("tenant", "e2e", "tenant id (controller mode)")
	operatorUser := flag.String("operator-user", "e2e-operator", "operator account username to seed (controller mode)")
	operatorPass := flag.String("operator-pass", "e2e-operator-pass", "operator account password to seed (controller mode)")
	operatorToken := flag.String("operator-token", "", "optional break-glass operator bearer token (controller mode); empty = password login only")
	enrollNode := flag.String("enroll-node", "node-1", "node id to pre-mint a single-use enrollment token for (controller mode)")
	webDir := flag.String("web-dir", "", "directory of the built panel SPA to serve at / (EnableStatic); empty = API only")
	addr := flag.String("addr", "127.0.0.1:0", "operator/panel (+ air-gap) listen address; :0 = OS-assigned")
	agentAddr := flag.String("agent-addr", "127.0.0.1:0", "agent listen address (controller mode); :0 = OS-assigned")
	secureCookie := flag.Bool("secure-cookie", false, "set the Secure attribute on session/CSRF cookies (false for plain-HTTP test)")
	flag.Parse()

	switch *mode {
	case "airgap":
		if err := serveAirgap(*webDir, *addr); err != nil {
			log.Fatalf("e2eserver: airgap: %v", err)
		}
	case "controller":
		if err := serveController(controllerConfig{
			stateDir:      *stateDir,
			tenant:        *tenant,
			operatorUser:  *operatorUser,
			operatorPass:  *operatorPass,
			operatorToken: *operatorToken,
			enrollNode:    *enrollNode,
			webDir:        *webDir,
			addr:          *addr,
			agentAddr:     *agentAddr,
			secureCookie:  *secureCookie,
		}); err != nil {
			log.Fatalf("e2eserver: controller: %v", err)
		}
	default:
		log.Fatalf("e2eserver: unknown --mode %q (want controller | airgap)", *mode)
	}
}

// serveAirgap boots the air-gap server: NewServer (which, under -tags airgap, registers
// the four anonymous compute routes) + the optional panel SPA, served on one port with
// Server.operatorAuth nil so gateAirgap is a passthrough. It binds first, prints READY,
// then serves (blocking).
func serveAirgap(webDir, addr string) error {
	server := api.NewServer()
	if webDir != "" {
		server.EnableStatic(webDir)
	}

	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("bind panel listener: %w", err)
	}
	fmt.Printf("E2E_READY mode=airgap panel=%s\n", ln.Addr().String())
	return http.Serve(ln, server.Handler())
}

// controllerConfig groups the controller-boot inputs (kept as a struct so main's flag
// plumbing stays readable).
type controllerConfig struct {
	stateDir      string
	tenant        string
	operatorUser  string
	operatorPass  string
	operatorToken string
	enrollNode    string
	webDir        string
	addr          string
	agentAddr     string
	secureCookie  bool
}

// serveController boots the controller exactly as cmd/server's serveController does
// (FileStore -> ControllerHandler -> EnableController), additionally seeding an operator
// account and one enrollment token so the E2E specs have a login credential and an
// enrollable node out of the box. It binds both listeners FIRST (so the OS-assigned :0
// ports are resolved), prints the READY line carrying both ports + the enrollment token,
// then serves both muxes (agent mux in a goroutine, operator/panel mux blocking).
func serveController(cfg controllerConfig) error {
	if cfg.stateDir == "" {
		return fmt.Errorf("--state-dir is required in controller mode")
	}
	ctx := context.Background()
	tid := controller.TenantID(cfg.tenant)

	store, err := controller.NewFileStore(cfg.stateDir)
	if err != nil {
		return fmt.Errorf("new filestore: %w", err)
	}

	// Seed the operator login account via the SHARED write path (Phase 3) — byte-identical
	// to `yaog-server create-operator`, so the panel logs in against a real account.
	if err := controller.SeedOperator(ctx, store, tid, cfg.operatorUser, cfg.operatorPass, time.Now().UTC()); err != nil {
		return fmt.Errorf("seed operator: %w", err)
	}

	// Optional break-glass token; empty hash = password-login only (the canary's path).
	opTokenHash := ""
	if cfg.operatorToken != "" {
		opTokenHash = controller.HashToken(cfg.operatorToken)
	}

	ch := api.NewControllerHandler(store, tid, opTokenHash, api.DefaultOperatorName)
	ch.SetSecureCookie(cfg.secureCookie)

	// Pre-mint ONE single-use enrollment token for the configured node, straight to the
	// store (the operator-side of the ceremony) — the same effect as the operator's
	// /enrollment-token route, mirroring controller_client_test.go's mintToken, and
	// INDEPENDENT of the handler's 7-day TTL clamp since the direct mint never passes
	// through the handler.
	enrollPlaintext, tok := controller.NewEnrollmentToken(cfg.enrollNode, enrollTokenTTL, time.Now())
	if err := store.CreateEnrollmentToken(ctx, tid, tok); err != nil {
		return fmt.Errorf("create enrollment token: %w", err)
	}

	server := api.NewServer()
	if cfg.webDir != "" {
		server.EnableStatic(cfg.webDir)
	}
	server.EnableController(ch)

	// Bind both listeners before printing READY so the :0 ports are concrete.
	panelLn, err := net.Listen("tcp", cfg.addr)
	if err != nil {
		return fmt.Errorf("bind panel listener: %w", err)
	}
	agentLn, err := net.Listen("tcp", cfg.agentAddr)
	if err != nil {
		return fmt.Errorf("bind agent listener: %w", err)
	}

	fmt.Printf("E2E_READY mode=controller panel=%s agent=%s enroll=%s\n",
		panelLn.Addr().String(), agentLn.Addr().String(), enrollPlaintext)

	// Serve the agent mux concurrently; the operator/panel mux blocks. A serve error on
	// either is fatal to the process (globalSetup then fails loudly on the lost server).
	errc := make(chan error, 2)
	go func() { errc <- http.Serve(agentLn, server.AgentHandler()) }()
	go func() { errc <- http.Serve(panelLn, server.Handler()) }()
	return <-errc
}
