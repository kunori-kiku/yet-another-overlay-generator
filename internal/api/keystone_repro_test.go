package api

// keystone_repro_test.go is the headless, end-to-end regression guard for the production
// incident the operator hit: after rotating the off-host keystone credential the agent loops
// on "trust-list signature verification failed" and re-enrolling the node does not fix it.
//
// It drives the REAL operator + agent HTTP surfaces (the shared ctlTestEnv) with a SOFTWARE
// Ed25519 signer standing in for the browser passkey, then feeds the bytes the controller
// actually SERVES at /config through the REAL agent.VerifyMembership — i.e. exactly the offline
// check a node runs. No browser, no WebAuthn, no root, no WireGuard: the membership gate is
// reached before any apply, so the trust decision is observable in isolation.
//
// The sub-tests pin the post-fix behavior across the two coupled axes:
//
//	A (baseline)            pin A, deploy signed-by-A, node pinned-A     -> applies
//	B (rotate + redeploy)   rotate to B (acked), redeploy signed-by-B    -> node-A REFUSES,
//	                        node-B ACCEPTS the SAME served bundle         (node must be
//	                        re-provisioned with B out of band — fail-closed, by design)
//	C (rotate, NO redeploy) rotate to B (acked) but do NOT redeploy      -> GET status reports
//	                        redeploy_required=true while /config keeps    serving (so the node is
//	                        not blinded); a fresh sign+promote clears it   and node-B then accepts
//
// The controller fix makes a CHANGED credential a deliberate, acked operation, so the rotation
// calls here must pass rotate:true (an unacked rotation is exercised by keystone_rotation_test.go).

import (
	"crypto/ed25519"
	"encoding/base64"
	"net/http"
	"testing"

	"github.com/kunorikiku/yet-another-overlay-generator/internal/agent"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/trustlist"
)

// pinKeystone pins the off-host operator credential to pub. rotate acknowledges a deliberate
// replacement of a DIFFERENT already-pinned credential (the post-fix controller refuses a
// changed credential without it); it is ignored on a first pin or an idempotent re-pin.
func pinKeystone(t *testing.T, env *ctlTestEnv, pub ed25519.PublicKey, rotate bool) {
	t.Helper()
	// Delegates to postCred (keystone_rotation_test.go) so the request body shape is defined once.
	if status, _ := postCred(t, env, pub, rotate); status != http.StatusOK {
		t.Fatalf("operator-credential pin (rotate=%v): status %d, want 200", rotate, status)
	}
}

// keystoneStatus GETs the server-authoritative keystone status.
func keystoneStatus(t *testing.T, env *ctlTestEnv) operatorCredentialStatusJSON {
	t.Helper()
	var st operatorCredentialStatusJSON
	if status := doJSON(t, http.MethodGet, env.opURL("operator-credential"), testOperatorToken, nil, &st); status != http.StatusOK {
		t.Fatalf("GET operator-credential: status %d, want 200", status)
	}
	return st
}

// deploy runs the full operator deploy with the keystone ON: stage -> off-host sign with
// `signer` -> promote. It asserts each step's status so a regression in the deploy path is
// caught here rather than surfacing as a confusing membership failure later.
func deploy(t *testing.T, env *ctlTestEnv, signer *trustlist.Ed25519Signer) {
	t.Helper()
	env.stageOnly(t)
	signStaged(t, env, signer)
	if status := doJSON(t, http.MethodPost, env.opURL("promote"), testOperatorToken, struct{}{}, nil); status != http.StatusOK {
		t.Fatalf("promote: status %d, want 200", status)
	}
}

// fetchServedBundle GETs /config for nodeToken and decodes the base64 file map into the
// raw bytes the agent verifies (map[string][]byte), exactly as the agent's HTTPSource does.
func fetchServedBundle(t *testing.T, env *ctlTestEnv, nodeToken string) map[string][]byte {
	t.Helper()
	var cfg configResponseJSON
	if status := doJSON(t, http.MethodGet, env.agentURL("config"), nodeToken, nil, &cfg); status != http.StatusOK {
		t.Fatalf("config: status %d, want 200", status)
	}
	files := make(map[string][]byte, len(cfg.Files))
	for name, b64 := range cfg.Files {
		raw, err := base64.StdEncoding.DecodeString(b64)
		if err != nil {
			t.Fatalf("decode served %q: %v", name, err)
		}
		files[name] = raw
	}
	return files
}

// verifyAsNode runs the REAL agent membership gate against a served bundle with the node
// pinned to `pinned`, mirroring agent.Run's VerifyMembership call (prevEpoch 0 = fresh node).
func verifyAsNode(t *testing.T, files map[string][]byte, nodeID string, pinned ed25519.PublicKey) error {
	t.Helper()
	_, err := agent.VerifyMembership(files, agent.MembershipConfig{
		NodeID:          nodeID,
		OperatorCredPEM: []byte(ed25519PinPEM(t, pinned)),
		OperatorCredAlg: string(trustlist.AlgEd25519),
	}, 0)
	return err
}

// TestKeystoneRotation_Repro pins the post-fix behavior across the rotation axes.
func TestKeystoneRotation_Repro(t *testing.T) {
	// Keystone A and keystone B: two independent off-host signers. A is the original
	// pinned anchor; B is the "new key" the operator rotates to.
	pubA, privA, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("genkey A: %v", err)
	}
	pubB, privB, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("genkey B: %v", err)
	}
	signerA := trustlist.NewEd25519Signer(privA)
	signerB := trustlist.NewEd25519Signer(privB)

	t.Run("A_baseline_node_pinnedA_applies", func(t *testing.T) {
		env := newCtlTestEnv(t)
		pinKeystone(t, env, pubA, false)
		nodeTok := env.enrollNode(t, "node-1")
		env.enrollNode(t, "node-2")
		deploy(t, env, signerA)

		files := fetchServedBundle(t, env, nodeTok)
		if err := verifyAsNode(t, files, "node-1", pubA); err != nil {
			t.Fatalf("baseline: node pinned-A must accept an A-signed bundle, got: %v", err)
		}
		if st := keystoneStatus(t, env); !st.Pinned || st.RedeployRequired {
			t.Fatalf("baseline status: want pinned && !redeploy_required, got %+v", st)
		}
	})

	t.Run("B_rotate_and_redeploy", func(t *testing.T) {
		env := newCtlTestEnv(t)
		pinKeystone(t, env, pubA, false)
		nodeTok := env.enrollNode(t, "node-1")
		env.enrollNode(t, "node-2")
		deploy(t, env, signerA)

		// Operator rotates the keystone to B IN THE CONTROLLER (acked) and re-deploys, now
		// signing with B. The promote gate verifies against the freshly-pinned B, so a
		// B-signed bundle is what gets served.
		pinKeystone(t, env, pubB, true)
		deploy(t, env, signerB)

		files := fetchServedBundle(t, env, nodeTok)

		// The node is STILL pinned to A (its /etc/wireguard/operator-cred.pem was never
		// refreshed). It MUST refuse, fail-closed — this is the keystone doing its job, not a
		// bug; the remedy is out-of-band re-provisioning, never auto-trust.
		if err := verifyAsNode(t, files, "node-1", pubA); err == nil {
			t.Fatalf("rotation: a node still pinned to A must REFUSE the B-signed bundle, but it accepted it")
		} else {
			t.Logf("EXPECTED refusal (node pinned-A, bundle signed-B): %v", err)
		}

		// Re-provisioning the SAME node with B (the out-of-band re-bootstrap / reprovision-keystone)
		// makes the SAME served bundle verify — the served bundle was correct all along.
		if err := verifyAsNode(t, files, "node-1", pubB); err != nil {
			t.Fatalf("rotation: after re-provisioning the node with B, the B-signed bundle must verify, got: %v", err)
		}
		// After a fresh signed deploy under B, the fleet is consistent: no redeploy required.
		if st := keystoneStatus(t, env); !st.Pinned || st.RedeployRequired {
			t.Fatalf("after rotate+redeploy: want pinned && !redeploy_required, got %+v", st)
		}
	})

	t.Run("C_rotate_without_redeploy_signals_then_clears", func(t *testing.T) {
		env := newCtlTestEnv(t)
		pinKeystone(t, env, pubA, false)
		nodeTok := env.enrollNode(t, "node-1")
		env.enrollNode(t, "node-2")
		deploy(t, env, signerA)

		// Operator rotates the keystone to B (acked) but does NOT re-stage/sign/promote.
		pinKeystone(t, env, pubB, true)

		// The controller now SIGNALS the stranded state to the operator (the fix for the silent
		// trap) — but /config keeps SERVING the stale bundle so the node still reaches
		// VerifyMembership and emits its actionable error (a /config 409 would blind it).
		if st := keystoneStatus(t, env); !st.Pinned || !st.RedeployRequired {
			t.Fatalf("rotate-without-redeploy: want pinned && redeploy_required, got %+v", st)
		}
		files := fetchServedBundle(t, env, nodeTok) // still served (200), not blinded
		if err := verifyAsNode(t, files, "node-1", pubB); err == nil {
			t.Fatalf("rotate-without-redeploy: a B-provisioned node must REFUSE the still-A-signed served bundle, but it accepted it")
		} else {
			t.Logf("EXPECTED refusal (node pinned-B, served bundle still signed-A): %v", err)
		}

		// Mid-deploy window: re-staging clears the served signature (staged-but-unsigned), which
		// must read as NOT-required (a deploy is in flight), not as a spurious strand signal.
		env.stageOnly(t)
		if st := keystoneStatus(t, env); st.RedeployRequired {
			t.Fatalf("staged-but-unsigned window must NOT report redeploy_required, got %+v", st)
		}

		// Completing the fresh signed deploy under B clears the signal and the B-node then accepts.
		signStaged(t, env, signerB)
		if status := doJSON(t, http.MethodPost, env.opURL("promote"), testOperatorToken, struct{}{}, nil); status != http.StatusOK {
			t.Fatalf("promote after re-sign: status %d, want 200", status)
		}
		if st := keystoneStatus(t, env); st.RedeployRequired {
			t.Fatalf("after the fresh B deploy: redeploy_required must clear, got %+v", st)
		}
		files = fetchServedBundle(t, env, nodeTok)
		if err := verifyAsNode(t, files, "node-1", pubB); err != nil {
			t.Fatalf("after the fresh B deploy: node-B must accept, got: %v", err)
		}
	})
}
