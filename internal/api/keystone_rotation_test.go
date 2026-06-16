package api

// keystone_rotation_test.go covers the controller-side keystone ROTATION DISCIPLINE on the
// operator-credential resource: GET reports server-authoritative status; POST does first-pin
// (TOFU) / idempotent re-pin / ack-gated rotation, with the redeploy-required signal. It uses a
// software Ed25519 signer for the off-host key, like the other keystone HTTP tests.

import (
	"crypto/ed25519"
	"net/http"
	"testing"

	"github.com/kunorikiku/yet-another-overlay-generator/internal/apierr"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/trustlist"
)

// postCred POSTs an operator-credential pin for pub and returns the HTTP status + parsed result.
func postCred(t *testing.T, env *ctlTestEnv, pub ed25519.PublicKey, rotate bool) (int, operatorCredentialPinResultJSON) {
	t.Helper()
	var res operatorCredentialPinResultJSON
	status := doJSON(t, http.MethodPost, env.opURL("operator-credential"), testOperatorToken,
		operatorCredentialRequestJSON{Alg: string(trustlist.AlgEd25519), PublicKeyPEM: ed25519PinPEM(t, pub), Rotate: rotate}, &res)
	return status, res
}

func TestKeystoneStatus_OffUntilPinned(t *testing.T) {
	env := newCtlTestEnv(t)
	if st := keystoneStatus(t, env); st.Pinned {
		t.Fatalf("keystone OFF: want pinned=false, got %+v", st)
	}
	pub, _, _ := ed25519.GenerateKey(nil)
	if status, res := postCred(t, env, pub, false); status != http.StatusOK || !res.OK || res.Rotated {
		t.Fatalf("first pin: status %d res %+v, want 200 ok && !rotated", status, res)
	}
	st := keystoneStatus(t, env)
	if !st.Pinned || st.Alg != string(trustlist.AlgEd25519) || st.Fingerprint == "" {
		t.Fatalf("after first pin: want pinned with alg+fingerprint, got %+v", st)
	}
	if st.RedeployRequired {
		t.Fatalf("after first pin (nothing deployed): redeploy_required must be false, got %+v", st)
	}
}

func TestKeystoneRepin_IdempotentSameKey(t *testing.T) {
	env := newCtlTestEnv(t)
	pub, _, _ := ed25519.GenerateKey(nil)
	postCred(t, env, pub, false)
	fp1 := keystoneStatus(t, env).Fingerprint

	// Re-pinning the SAME key without rotate is idempotent — no ack needed, no fleet impact.
	status, res := postCred(t, env, pub, false)
	if status != http.StatusOK || !res.OK || res.Rotated || !res.Unchanged {
		t.Fatalf("idempotent re-pin: status %d res %+v, want 200 ok unchanged !rotated", status, res)
	}
	if fp2 := keystoneStatus(t, env).Fingerprint; fp2 != fp1 {
		t.Fatalf("idempotent re-pin changed the fingerprint: %s -> %s", fp1, fp2)
	}
}

func TestKeystoneRotation_RefusedWithoutAck(t *testing.T) {
	env := newCtlTestEnv(t)
	pubA, _, _ := ed25519.GenerateKey(nil)
	pubB, _, _ := ed25519.GenerateKey(nil)
	postCred(t, env, pubA, false)
	fpA := keystoneStatus(t, env).Fingerprint

	// A different key WITHOUT rotate:true is refused 409, and the stored credential is UNCHANGED.
	status, _ := postCred(t, env, pubB, false)
	if status != http.StatusConflict {
		t.Fatalf("unacked rotation: status %d, want 409", status)
	}
	if got := keystoneStatus(t, env).Fingerprint; got != fpA {
		t.Fatalf("a refused rotation must NOT mutate the pinned credential: %s -> %s", fpA, got)
	}

	// With rotate:true it succeeds and the fingerprint advances to B.
	status, res := postCred(t, env, pubB, true)
	if status != http.StatusOK || !res.OK || !res.Rotated {
		t.Fatalf("acked rotation: status %d res %+v, want 200 ok rotated", status, res)
	}
	st := keystoneStatus(t, env)
	if st.Fingerprint == fpA || st.Fingerprint == "" {
		t.Fatalf("acked rotation must advance the fingerprint away from A, got %+v", st)
	}
}

// TestKeystoneRotation_RedeployRequiredSignal asserts the operator-facing redeploy_required signal:
// after a real A-signed deploy, rotating to B (with no fresh deploy) reports redeploy_required, and
// the rotate RESPONSE carries the same signal — the fix for the formerly-silent stranded fleet.
func TestKeystoneRotation_RedeployRequiredSignal(t *testing.T) {
	env := newCtlTestEnv(t)
	pubA, privA, _ := ed25519.GenerateKey(nil)
	pubB, _, _ := ed25519.GenerateKey(nil)

	postCred(t, env, pubA, false)
	env.enrollNode(t, "node-1")
	env.enrollNode(t, "node-2")
	deploy(t, env, trustlist.NewEd25519Signer(privA))
	if keystoneStatus(t, env).RedeployRequired {
		t.Fatal("a freshly-deployed A fleet must not require a redeploy")
	}

	// Rotate to B without redeploying: the served bundle is still A-signed -> redeploy required.
	status, res := postCred(t, env, pubB, true)
	if status != http.StatusOK || !res.Rotated || !res.RedeployRequired {
		t.Fatalf("rotate after deploy: status %d res %+v, want 200 rotated redeploy_required", status, res)
	}
	if !keystoneStatus(t, env).RedeployRequired {
		t.Fatal("GET status must report redeploy_required after a rotate-without-redeploy")
	}
}

// TestKeystoneStatus_RequiresOperatorAuth confirms GET /operator-credential is operator-gated by
// the op() wrapper (an unauthenticated caller is 401; a node-scoped token is 403).
func TestKeystoneStatus_RequiresOperatorAuth(t *testing.T) {
	env := newCtlTestEnv(t)
	nodeTok := env.enrollNode(t, "node-1")
	if status := doJSON(t, http.MethodGet, env.opURL("operator-credential"), "", nil, nil); status != http.StatusUnauthorized {
		t.Fatalf("GET operator-credential no-auth: status %d, want 401", status)
	}
	if status := doJSON(t, http.MethodGet, env.opURL("operator-credential"), nodeTok, nil, nil); status != http.StatusForbidden {
		t.Fatalf("GET operator-credential node-token: status %d, want 403", status)
	}
}

// guard against an accidental drift of the apierr code/status used by the rotation gate.
func TestKeystoneRotationCode(t *testing.T) {
	if apierr.New(apierr.CodeKeystoneRotationRequiresAck).Status() != http.StatusConflict {
		t.Fatal("CodeKeystoneRotationRequiresAck must be a 409")
	}
}
