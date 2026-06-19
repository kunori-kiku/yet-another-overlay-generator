package main

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/kunorikiku/yet-another-overlay-generator/internal/agent"
)

// checkinMode (the default) drives the post-enroll wire: in --mock it reports an immediate
// enrolled check-in; otherwise it polls for a promoted generation and, on one, fetches + verifies
// the bundle (the same gate agent.Run runs before apply — install.sh is NOT executed) and reports
// it applied, else reports an enrolled check-in. Prints the machine-readable spec line.
func checkinMode(client *agent.ControllerClient, f *agentFlags) int {
	gen, err := checkIn(client, f.nodeID, f.mock)
	if err != nil {
		fmt.Fprintf(os.Stderr, "e2eagent: %v\n", err)
		return 1
	}
	label := "real"
	if f.mock {
		label = "mock"
	}
	fmt.Printf("E2E_AGENT node=%s reported_generation=%d mode=%s\n", f.nodeID, gen, label)
	return 0
}

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
		// No generation promoted yet (timed-out long-poll). Report an enrolled check-in.
		if err := report(client, nodeID, "", "enrolled (no generation yet)"); err != nil {
			return 0, err
		}
		return 0, nil
	}

	files, err := client.Fetch(nodeID)
	if err != nil {
		return 0, fmt.Errorf("fetch generation %d: %w", gen, err)
	}
	// VerifyBundle over the fetched bundle — unsigned in CI (PinnedPubPEM=nil), the same gate
	// agent.Run runs before apply (controller_client_test.go). install.sh never runs.
	if _, err := agent.VerifyBundle(files, nil); err != nil {
		return 0, fmt.Errorf("verify generation %d: %w", gen, err)
	}
	if err := report(client, nodeID, "e2e-verified", "applied"); err != nil {
		return 0, err
	}
	return client.LastFetchedGeneration(), nil
}

// report POSTs an agent State as a /report check-in (LastResult "ok" so the applied generation
// tracks the last fetched generation).
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
