package main

import (
	"fmt"
	"os"

	"github.com/kunorikiku/yet-another-overlay-generator/internal/agent"
)

// reprovisionMode is the keystone-rotation node half: assert the served bundle's membership
// REFUSES under the currently-pinned OLD credential, then ReprovisionKeystone the pin to the NEW
// credential (validate-before-atomic-write, fail-closed), then assert it ADOPTS. Reuses the
// exported internal/agent verbatim — no re-implemented crypto. Prints
// REPROVISION node=<id> refuse-before=ok adopt-after=ok.
func reprovisionMode(client *agent.ControllerClient, f *agentFlags) int {
	if f.credOut == "" || f.credAlg == "" || f.newCredPEM == "" {
		fmt.Fprintln(os.Stderr, "e2eagent: reprovision: --operator-cred, --operator-cred-alg, --new-cred-pem are required")
		return 2
	}
	oldPEM, err := os.ReadFile(f.credOut)
	if err != nil {
		fmt.Fprintf(os.Stderr, "e2eagent: reprovision: read OLD pin %s: %v\n", f.credOut, err)
		return 1
	}
	newPEM, err := os.ReadFile(f.newCredPEM)
	if err != nil {
		fmt.Fprintf(os.Stderr, "e2eagent: reprovision: read NEW pem %s: %v\n", f.newCredPEM, err)
		return 1
	}

	membership := func(pem []byte) agent.MembershipConfig {
		return agent.MembershipConfig{
			NodeID:          f.nodeID,
			OperatorCredPEM: pem,
			OperatorCredAlg: f.credAlg,
			OperatorRPID:    f.operatorRPID,
			OperatorOrigin:  f.operatorOrigin,
		}
	}

	// Fetch the served (NEW-signed) bundle.
	files, err := client.Fetch(f.nodeID)
	if err != nil {
		fmt.Fprintf(os.Stderr, "e2eagent: reprovision: fetch: %v\n", err)
		return 1
	}
	// (a) refuse-before: VerifyMembership under the OLD pin MUST fail — the bundle is signed under
	// the NEW credential, which the node has not yet adopted.
	if _, err := agent.VerifyMembership(files, membership(oldPEM), 0); err == nil {
		fmt.Fprintln(os.Stderr, "e2eagent: reprovision: VerifyMembership unexpectedly ACCEPTED under the OLD credential (refuse-before failed)")
		return 1
	}
	// (b) adopt the NEW pin (atomic rewrite, fail-closed).
	if err := agent.ReprovisionKeystone(f.credOut, f.credAlg, newPEM); err != nil {
		fmt.Fprintf(os.Stderr, "e2eagent: reprovision: %v\n", err)
		return 1
	}
	// (c) adopt-after: re-fetch + VerifyMembership under the NEW pin MUST pass.
	files, err = client.Fetch(f.nodeID)
	if err != nil {
		fmt.Fprintf(os.Stderr, "e2eagent: reprovision: re-fetch: %v\n", err)
		return 1
	}
	if _, err := agent.VerifyMembership(files, membership(newPEM), 0); err != nil {
		fmt.Fprintf(os.Stderr, "e2eagent: reprovision: VerifyMembership REFUSED under the NEW credential (adopt-after failed): %v\n", err)
		return 1
	}

	fmt.Printf("REPROVISION node=%s refuse-before=ok adopt-after=ok\n", f.nodeID)
	return 0
}
