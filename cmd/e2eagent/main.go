// Command e2eagent is a TEST-ONLY node-agent fixture for the Playwright E2E layer
// (plan-13 / milestone 3.1). It is NOT a release artifact: .github/workflows/release.yml
// builds explicit targets only (./cmd/server, ./cmd/compiler, ./cmd/agent), so this main
// is excluded from shipped binaries by construction.
//
// It reuses the REAL internal/agent client seams — exactly as
// internal/agent/controller_client_test.go drives them — to enroll a node against a
// live (e2eserver) controller and check in, so an E2E spec can assert a node appears in
// the Fleet registry. It NEVER runs the bundle's root install.sh (no root, no real
// WireGuard) — it stops at fetch+verify+report, the same gate agent.Run runs before
// apply (matching controller_client_test.go:27-30).
//
// Two behaviors:
//
//	default (real)  EnsureKey -> Enroll -> Poll -> (on a promoted generation) Fetch +
//	                VerifyBundle(unsigned) -> Report the applied generation. Exercises the
//	                full agent<->controller wire — the top rc.1 risk surface. Expects the
//	                operator to have deployed a generation first (plans 14+); without one,
//	                Poll times out and it reports an enrolled check-in (applied gen 0).
//	--mock          EnsureKey -> Enroll -> Report a check-in (no poll/fetch/verify), for
//	                specs (e.g. plan-13's controller-fleet canary) that only need a node
//	                to become visible in the registry quickly and deterministically.
//
// On success it prints one machine-readable line for spec assertions and exits 0:
//
//	E2E_AGENT node=<id> reported_generation=<n> mode=<real|mock>
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/kunorikiku/yet-another-overlay-generator/internal/agent"
)

func main() {
	os.Exit(run())
}

func run() int {
	controllerURL := flag.String("controller", "", "controller AGENT base URL: http://host:port (required)")
	nodeID := flag.String("node-id", "", "node identity to enroll as (must match the enrollment token's scope) (required)")
	token := flag.String("token", "", "single-use enrollment token from the e2eserver READY line (required)")
	keyPath := flag.String("key", "", "WG private-key path (mode 0600); default: an ephemeral temp file per node")
	agentVersion := flag.String("agent-version", "e2e-dev", "build version to report on check-in (Node.LastAgentVersion)")
	mock := flag.Bool("mock", false, "skip poll/fetch/verify: just enroll + report a check-in (fast visible check-in)")
	flag.Parse()

	switch {
	case *controllerURL == "":
		fmt.Fprintln(os.Stderr, "e2eagent: --controller is required")
		return 2
	case *nodeID == "":
		fmt.Fprintln(os.Stderr, "e2eagent: --node-id is required")
		return 2
	case *token == "":
		fmt.Fprintln(os.Stderr, "e2eagent: --token is required")
		return 2
	}

	key := *keyPath
	if key == "" {
		key = filepath.Join(os.TempDir(), "yaog-e2eagent-"+*nodeID+".key")
	}

	// (1) Ensure the WG key (rootless temp path) and read its public key — the same seam
	// the real agent uses (cmd/agent EnsureKey).
	wgPub, _, err := agent.EnsureKey(key)
	if err != nil {
		fmt.Fprintf(os.Stderr, "e2eagent: ensure key: %v\n", err)
		return 1
	}

	// (2) Enroll over a token-less client (the /enroll shape) — the single-use token is
	// the credential; the response mints the per-node bearer token.
	enrollClient, err := agent.NewControllerClient(*controllerURL, "")
	if err != nil {
		fmt.Fprintf(os.Stderr, "e2eagent: new client: %v\n", err)
		return 1
	}
	enrollRes, err := enrollClient.Enroll(*token, *nodeID, wgPub)
	if err != nil {
		fmt.Fprintf(os.Stderr, "e2eagent: enroll: %v\n", err)
		return 1
	}

	// (3) Bearer client for the authed calls; report this fixture's version on check-in.
	client, err := agent.NewControllerClient(*controllerURL, enrollRes.APIToken)
	if err != nil {
		fmt.Fprintf(os.Stderr, "e2eagent: new bearer client: %v\n", err)
		return 1
	}
	client.AgentVersion = *agentVersion

	reportedGen, err := checkIn(client, *nodeID, *mock)
	if err != nil {
		fmt.Fprintf(os.Stderr, "e2eagent: %v\n", err)
		return 1
	}

	modeLabel := "real"
	if *mock {
		modeLabel = "mock"
	}
	fmt.Printf("E2E_AGENT node=%s reported_generation=%d mode=%s\n", *nodeID, reportedGen, modeLabel)
	return 0
}

// checkIn drives the post-enroll wire and returns the applied generation reported to the
// controller. In --mock it reports an immediate enrolled check-in. In real mode it polls
// for a promoted generation; on one it fetches + verifies the bundle (the same gate
// agent.Run runs before apply — install.sh is NOT executed) and reports it applied;
// without one it reports an enrolled check-in (applied generation 0).
func checkIn(client *agent.ControllerClient, nodeID string, mock bool) (int64, error) {
	if mock {
		if err := report(client, nodeID, "", "enrolled (mock check-in)"); err != nil {
			return 0, err
		}
		return client.LastFetchedGeneration(), nil
	}

	gen, changed, err := client.Poll(0)
	if err != nil {
		return 0, fmt.Errorf("poll: %w", err)
	}
	if !changed {
		// No generation promoted yet (timed-out long-poll). Report an enrolled check-in;
		// a later deploy (plans 14+) makes the full fetch path below fire.
		if err := report(client, nodeID, "", "enrolled (no generation yet)"); err != nil {
			return 0, err
		}
		return 0, nil
	}

	files, err := client.Fetch(nodeID)
	if err != nil {
		return 0, fmt.Errorf("fetch generation %d: %w", gen, err)
	}
	// VerifyBundle over the fetched bundle — unsigned in CI (PinnedPubPEM=nil), the same
	// gate agent.Run runs before apply (controller_client_test.go). install.sh never runs.
	if _, err := agent.VerifyBundle(files, nil); err != nil {
		return 0, fmt.Errorf("verify generation %d: %w", gen, err)
	}
	// Report the FETCHED generation applied (LastResult "ok" => Report sends
	// LastFetchedGeneration). The checksum is a fixture marker (the registry stores it
	// verbatim; the wire + applied-generation are what this fixture exercises), matching
	// the "deadbeef" literal convention in controller_client_test.go.
	if err := report(client, nodeID, "e2e-verified", "applied"); err != nil {
		return 0, err
	}
	return client.LastFetchedGeneration(), nil
}

// report POSTs an agent State as a /report check-in (LastResult "ok" so the applied
// generation tracks the last fetched generation).
func report(client *agent.ControllerClient, nodeID, checksum, health string) error {
	payload, err := json.Marshal(agent.State{
		NodeID:       nodeID,
		LastChecksum: checksum,
		LastResult:   agent.LastResultOK,
		Health:       health,
	})
	if err != nil {
		return fmt.Errorf("marshal state: %w", err)
	}
	if err := client.Report(nodeID, payload); err != nil {
		return fmt.Errorf("report: %w", err)
	}
	return nil
}
