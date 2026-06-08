package main

import (
	"flag"
	"log"
	"os"
	"time"

	"github.com/kunorikiku/yet-another-overlay-generator/internal/api"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/controller"
)

// Controller-mode environment gates. When BOTH are set, cmd/server builds the
// controller dependencies (FileStore + ephemeral DevCA) and serves the controller
// over TLS 1.3 + mTLS. When either is unset, the server is EXACTLY as before:
// air-gap HTTP only, no controller routes, no TLS.
const (
	// envControllerStateDir is the FileStore root for controller state. Its presence
	// (together with the tenant id) turns on controller mode.
	envControllerStateDir = "YAOG_CONTROLLER_STATE_DIR"
	// envTenantID is the single-tenant id (single-tenant v1). Pinned into the auth
	// chokepoint and used to scope every Store operation.
	envTenantID = "YAOG_TENANT_ID"
	// envControllerHost names the controller for its TLS server cert SANs. Optional;
	// "localhost" + 127.0.0.1 are always included regardless.
	envControllerHost = "YAOG_CONTROLLER_HOST"
)

// Ephemeral DevCA lifetimes (controller mode). The CA private key is in-memory only
// and discarded on restart (see enrollment.go), so a restart forces re-enrollment;
// the TTLs are long enough that a single run never expires mid-operation.
const (
	caTTL         = 365 * 24 * time.Hour
	clientCertTTL = 90 * 24 * time.Hour
)

func main() {
	addr := flag.String("addr", ":8080", "")
	flag.Parse()

	stateDir := os.Getenv(envControllerStateDir)
	tenant := os.Getenv(envTenantID)

	server := api.NewServer()

	// Air-gap mode (the default): controller env not configured → serve exactly as
	// before. The mux and the HTTP serve path are untouched.
	if stateDir == "" || tenant == "" {
		if err := server.ListenAndServe(*addr); err != nil {
			log.Fatalf(": %v", err)
		}
		return
	}

	// Controller mode: build the dependencies and serve over TLS + mTLS.
	if err := serveController(server, *addr, stateDir, tenant); err != nil {
		log.Fatalf("controller: %v", err)
	}
}

// serveController builds the controller dependencies (FileStore, ephemeral DevCA,
// server cert + mTLS config), arms the controller routes on the server, and serves
// over TLS. It is only reached under the controller env gate.
func serveController(server *api.Server, addr, stateDir, tenant string) error {
	now := time.Now()

	store, err := controller.NewFileStore(stateDir)
	if err != nil {
		return err
	}

	ca, err := controller.NewDevCA(controller.TenantID(tenant), now, caTTL, clientCertTTL)
	if err != nil {
		return err
	}

	serverCert, err := ca.IssueServerCert(os.Getenv(envControllerHost), now)
	if err != nil {
		return err
	}
	tlsConfig := ca.ServerTLSConfig(serverCert)

	ch := api.NewControllerHandler(store, ca, controller.TenantID(tenant), api.DefaultOperatorName)
	server.EnableController(ch, tlsConfig)

	return server.ListenAndServeTLS(addr)
}
