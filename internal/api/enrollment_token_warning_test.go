package api

// enrollment_token_warning_test.go — subject-scoped test for the token-mint
// design-membership advisory (controller-server-authority-redesign plan-6): minting
// an enrollment token for a node-id absent from the stored design returns a 200 with
// a non-blocking `warning`; an id present in the design (or no design stored yet) has
// no warning. Retire at subject close.

import (
	"encoding/json"
	"net/http"
	"testing"
)

func TestEnrollmentTokenWarning(t *testing.T) {
	env := newCtlTestEnv(t)

	mint := func(nodeID string) (int, enrollmentTokenResponseJSON) {
		var resp enrollmentTokenResponseJSON
		st := doJSON(t, http.MethodPost, env.opURL("enrollment-token"), testOperatorToken,
			enrollmentTokenRequestJSON{NodeID: nodeID, TTLSeconds: 3600}, &resp)
		return st, resp
	}

	// No stored design yet: pre-minting is normal, so NO warning.
	st, resp := mint("node-1")
	if st != http.StatusOK {
		t.Fatalf("mint (no design) = %d, want 200", st)
	}
	if resp.Token == "" {
		t.Fatalf("mint (no design): empty token")
	}
	if resp.Warning != "" {
		t.Errorf("mint (no design) warning = %q, want empty (pre-minting is normal)", resp.Warning)
	}

	// Store a design with node-1 and node-2 (smallTopo's ids).
	topoJSON, _ := json.Marshal(smallTopo())
	if st := doRaw(t, http.MethodPost, env.opURL("update-topology"), testOperatorToken, topoJSON); st != http.StatusOK {
		t.Fatalf("update-topology = %d, want 200", st)
	}

	// node-1 is in the design → no warning.
	st, resp = mint("node-1")
	if st != http.StatusOK || resp.Warning != "" {
		t.Errorf("mint (node-1 in design) = %d warning=%q, want 200 + no warning", st, resp.Warning)
	}

	// node-99 is NOT in the design → warning, but still a usable token (warn-not-block).
	st, resp = mint("node-99")
	if st != http.StatusOK {
		t.Fatalf("mint (node-99 absent) = %d, want 200 (warn-not-block)", st)
	}
	if resp.Token == "" {
		t.Errorf("mint (node-99 absent): token must still be minted (warn-not-block)")
	}
	if resp.Warning == "" {
		t.Errorf("mint (node-99 absent): want a design-membership warning, got none")
	}
}
