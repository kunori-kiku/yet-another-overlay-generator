package main

import (
	"fmt"
	"os"

	"github.com/kunorikiku/yet-another-overlay-generator/internal/agent"
)

// rekeyMode drives a WireGuard key rotation in response to the controller's rekey flag: fetch
// /config, confirm rekey_requested is set, regenerate a FRESH WG key, register the new public key
// via the REAL (*ControllerClient).Rekey wire (no re-implemented POST), re-fetch + verify the
// post-rekey bundle, and report. Prints REKEY_DONE node=<id> newpub=<short> gen=<n>.
func rekeyMode(client *agent.ControllerClient, f *agentFlags) int {
	// Fetch the current bundle; LastRekeyRequested reflects the /config rekey_requested flag.
	if _, err := client.Fetch(f.nodeID); err != nil {
		fmt.Fprintf(os.Stderr, "e2eagent: rekey: fetch: %v\n", err)
		return 1
	}
	if !client.LastRekeyRequested() {
		fmt.Fprintf(os.Stderr, "e2eagent: rekey: controller did not flag rekey_requested for %s\n", f.nodeID)
		return 1
	}

	// Force a fresh WG key via the dedicated rotate seam (the explicit force-rotate counterpart to
	// EnsureKey's idempotent reuse; its doc describes exactly a controller-requested fleet rekey).
	newPub, err := agent.RegenerateKey(f.keyPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "e2eagent: rekey: regen key: %v\n", err)
		return 1
	}
	// Register the new public key via the real Rekey wire (the client side of HandleRekey).
	if err := client.Rekey(newPub); err != nil {
		fmt.Fprintf(os.Stderr, "e2eagent: rekey: register: %v\n", err)
		return 1
	}

	// Re-fetch + verify the post-rekey bundle (the gate before apply), then report.
	files, err := client.Fetch(f.nodeID)
	if err != nil {
		fmt.Fprintf(os.Stderr, "e2eagent: rekey: re-fetch: %v\n", err)
		return 1
	}
	if _, err := agent.VerifyBundle(files, nil); err != nil {
		fmt.Fprintf(os.Stderr, "e2eagent: rekey: verify: %v\n", err)
		return 1
	}
	if err := report(client, f.nodeID, "e2e-rekeyed", "applied"); err != nil {
		fmt.Fprintf(os.Stderr, "e2eagent: rekey: %v\n", err)
		return 1
	}

	fmt.Printf("REKEY_DONE node=%s newpub=%s gen=%d\n", f.nodeID, shortPub(newPub), client.LastFetchedGeneration())
	return 0
}
