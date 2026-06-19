// Command e2eagent is a TEST-ONLY node-agent fixture for the Playwright E2E layer
// (plan-13 / 3.1, extended by plan-15 / 3.3). It is NOT a release artifact: it is built ONLY
// for the gate-e2e Playwright job (into .e2e-bin/, never uploaded) — the release publish matrix
// builds explicit targets only (server / compiler / agent / server-airgap) — so it is excluded
// from shipped binaries by construction.
//
// It reuses the REAL internal/agent client seams (exactly as
// internal/agent/controller_client_test.go drives them) so an E2E spec can walk a node through
// the controller-mode lifecycle. It NEVER runs the bundle's root install.sh (no root, no real
// WireGuard) — every mode stops at fetch+verify (the same gate agent.Run runs before apply).
//
// main.go is a THIN DISPATCHER: it parses the common flags + --mode, enrolls-or-reuses a bearer
// client (dial), and calls the mode func in its sibling file. The modes:
//
//	--mode checkin (default)  enroll -> [poll -> fetch -> VerifyBundle] -> report. --mock skips
//	                          poll/fetch for a fast visible check-in. (checkin.go)
//	--mode rekey              fetch /config; if rekey_requested, regenerate the WG key, register
//	                          it via (*ControllerClient).Rekey, re-fetch+verify, report. (rekey.go)
//	--mode reprovision        keystone-rotation node half: assert VerifyMembership REFUSES the
//	                          served bundle under the OLD pinned credential, ReprovisionKeystone to
//	                          the NEW credential, assert it ADOPTS. (reprovision.go)
//
// Bearer reuse: enroll writes the per-node bearer token to --bearer-file (when given); a later
// invocation (rekey/reprovision) with the same --bearer-file reuses it and skips enroll (the
// single-use enrollment token is consumed once). Mirrors the real agent's token persistence.
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/kunorikiku/yet-another-overlay-generator/internal/agent"
)

func main() {
	os.Exit(run())
}

// agentFlags is the parsed common + mode-specific flag set, threaded to the mode funcs so each
// sibling file stays a pure function of its inputs (no global flag reads).
type agentFlags struct {
	controllerURL string
	nodeID        string
	token         string
	keyPath       string
	bearerFile    string
	agentVersion  string
	mode          string
	mock          bool
	// reprovision-mode inputs (keystone rotation):
	credOut        string // the pinned-credential PEM file the node reads (rewritten on reprovision)
	credAlg        string // ed25519 | webauthn-es256 | webauthn-eddsa
	newCredPEM     string // path to the NEW (rotated) operator-credential public-key PEM
	operatorRPID   string // WebAuthn relying-party id (WebAuthn algs)
	operatorOrigin string // WebAuthn origin (WebAuthn algs; advisory on a node)
}

func run() int {
	var f agentFlags
	flag.StringVar(&f.controllerURL, "controller", "", "controller AGENT base URL: http://host:port (required)")
	flag.StringVar(&f.nodeID, "node-id", "", "node identity to enroll/act as (required)")
	flag.StringVar(&f.token, "token", "", "single-use enrollment token (required unless --bearer-file already holds a token)")
	flag.StringVar(&f.keyPath, "key", "", "WG private-key path (mode 0600); default: an ephemeral temp file per node")
	flag.StringVar(&f.bearerFile, "bearer-file", "", "persist/reuse the per-node bearer token here across invocations (enroll writes, later modes read)")
	flag.StringVar(&f.agentVersion, "agent-version", "e2e-dev", "build version to report on check-in (Node.LastAgentVersion)")
	flag.StringVar(&f.mode, "mode", "checkin", "checkin | rekey | reprovision")
	flag.BoolVar(&f.mock, "mock", false, "checkin mode: skip poll/fetch/verify (fast visible check-in)")
	flag.StringVar(&f.credOut, "operator-cred", "", "reprovision: path to the pinned operator-credential PEM the node reads (OLD pin in, rewritten to NEW)")
	flag.StringVar(&f.credAlg, "operator-cred-alg", "", "reprovision: ed25519 | webauthn-es256 | webauthn-eddsa")
	flag.StringVar(&f.newCredPEM, "new-cred-pem", "", "reprovision: path to the NEW (rotated) operator-credential public-key PEM")
	flag.StringVar(&f.operatorRPID, "operator-rpid", "", "reprovision: WebAuthn relying-party id (WebAuthn algs)")
	flag.StringVar(&f.operatorOrigin, "operator-origin", "", "reprovision: WebAuthn origin (WebAuthn algs)")
	flag.Parse()

	if f.controllerURL == "" || f.nodeID == "" {
		fmt.Fprintln(os.Stderr, "e2eagent: --controller and --node-id are required")
		return 2
	}
	if f.keyPath == "" {
		f.keyPath = filepath.Join(os.TempDir(), "yaog-e2eagent-"+f.nodeID+".key")
	}

	client, err := dial(&f)
	if err != nil {
		fmt.Fprintf(os.Stderr, "e2eagent: %v\n", err)
		return 1
	}

	switch f.mode {
	case "checkin":
		return checkinMode(client, &f)
	case "rekey":
		return rekeyMode(client, &f)
	case "reprovision":
		return reprovisionMode(client, &f)
	default:
		fmt.Fprintf(os.Stderr, "e2eagent: unknown --mode %q (checkin | rekey | reprovision)\n", f.mode)
		return 2
	}
}

// dial returns a bearer ControllerClient for the node: it reuses a persisted bearer token from
// --bearer-file when present, otherwise enrolls with the single-use --token (ensuring the WG key
// first) and persists the issued bearer to --bearer-file (when given). The enrollment token is
// single-use, so only the FIRST invocation per node enrolls; later modes reuse the bearer.
func dial(f *agentFlags) (*agent.ControllerClient, error) {
	if f.bearerFile != "" {
		if b, err := os.ReadFile(f.bearerFile); err == nil && strings.TrimSpace(string(b)) != "" {
			client, err := agent.NewControllerClient(f.controllerURL, strings.TrimSpace(string(b)))
			if err != nil {
				return nil, fmt.Errorf("bearer client from %s: %w", f.bearerFile, err)
			}
			client.AgentVersion = f.agentVersion
			return client, nil
		}
	}

	if f.token == "" {
		return nil, fmt.Errorf("--token is required to enroll (no reusable bearer in --bearer-file)")
	}
	// Ensure the WG key (rootless temp path) and read its public key — the real agent's seam.
	wgPub, _, err := agent.EnsureKey(f.keyPath)
	if err != nil {
		return nil, fmt.Errorf("ensure key: %w", err)
	}
	// Enroll over a token-less client; the single-use token is the credential.
	enrollClient, err := agent.NewControllerClient(f.controllerURL, "")
	if err != nil {
		return nil, fmt.Errorf("new client: %w", err)
	}
	res, err := enrollClient.Enroll(f.token, f.nodeID, wgPub)
	if err != nil {
		return nil, fmt.Errorf("enroll: %w", err)
	}
	if f.bearerFile != "" {
		if err := os.WriteFile(f.bearerFile, []byte(res.APIToken), 0o600); err != nil {
			return nil, fmt.Errorf("persist bearer to %s: %w", f.bearerFile, err)
		}
	}
	client, err := agent.NewControllerClient(f.controllerURL, res.APIToken)
	if err != nil {
		return nil, fmt.Errorf("new bearer client: %w", err)
	}
	client.AgentVersion = f.agentVersion
	return client, nil
}

// shortPub abbreviates a base64 WG public key for a one-line machine-readable print.
func shortPub(pub string) string {
	if len(pub) <= 12 {
		return pub
	}
	return pub[:12]
}
