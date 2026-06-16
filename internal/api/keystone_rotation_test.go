package api

// keystone_rotation_test.go covers the controller-side keystone ROTATION DISCIPLINE on the
// operator-credential resource: GET reports server-authoritative status; POST does first-pin
// (TOFU) / idempotent re-pin / ack-gated rotation, with the redeploy-required signal. It uses a
// software Ed25519 signer for the off-host key, like the other keystone HTTP tests.

import (
	"context"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"net/http"
	"testing"

	"github.com/kunorikiku/yet-another-overlay-generator/internal/apierr"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/trustlist"
)

// keystoneAuditActions returns the audit-log action strings recorded for the test tenant, so a
// test can assert the exact pin/rotate accountability entries (and their absence on an idempotent
// re-pin) — the accountability invariant the fix relies on.
func keystoneAuditActions(t *testing.T, env *ctlTestEnv) []string {
	t.Helper()
	entries, err := env.store.ListAudit(context.Background(), testTenant)
	if err != nil {
		t.Fatalf("ListAudit: %v", err)
	}
	actions := make([]string, 0, len(entries))
	for _, e := range entries {
		actions = append(actions, e.Action)
	}
	return actions
}

// countAction counts how many times action appears in actions.
func countAction(actions []string, action string) int {
	n := 0
	for _, a := range actions {
		if a == action {
			n++
		}
	}
	return n
}

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

// TestKeystoneAudit pins the accountability invariant: a first pin records exactly one
// pin-operator-credential, an idempotent re-pin records NOTHING new, and an acked rotation records
// exactly one rotate-operator-credential (and no extra pin entry). Mirrors signing_anchor_test's
// hasAudit discipline.
func TestKeystoneAudit(t *testing.T) {
	env := newCtlTestEnv(t)
	pubA, _, _ := ed25519.GenerateKey(rand.Reader)
	pubB, _, _ := ed25519.GenerateKey(rand.Reader)

	postCred(t, env, pubA, false) // first pin
	if got := countAction(keystoneAuditActions(t, env), "pin-operator-credential"); got != 1 {
		t.Fatalf("first pin: pin-operator-credential count = %d, want 1", got)
	}

	postCred(t, env, pubA, false) // idempotent re-pin: NO new audit
	a := keystoneAuditActions(t, env)
	if got := countAction(a, "pin-operator-credential"); got != 1 {
		t.Fatalf("idempotent re-pin must add no audit; pin count = %d, want 1", got)
	}
	if got := countAction(a, "rotate-operator-credential"); got != 0 {
		t.Fatalf("idempotent re-pin must not record a rotate; got %d", got)
	}

	postCred(t, env, pubB, true) // acked rotation
	a = keystoneAuditActions(t, env)
	if got := countAction(a, "rotate-operator-credential"); got != 1 {
		t.Fatalf("acked rotation: rotate-operator-credential count = %d, want 1", got)
	}
	if got := countAction(a, "pin-operator-credential"); got != 1 {
		t.Fatalf("a rotation must NOT also record a pin-operator-credential; pin count = %d, want 1", got)
	}
}

// es256PinPEM builds a fresh ES256 (P-256) PKIX public-key PEM — the production keystone uses a
// WebAuthn ES256 passkey, so the rotation gate must be exercised through the WebAuthn alg too.
func es256PinPEM(t *testing.T) string {
	t.Helper()
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("ecdsa gen: %v", err)
	}
	der, err := x509.MarshalPKIXPublicKey(&priv.PublicKey)
	if err != nil {
		t.Fatalf("marshal pkix: %v", err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der}))
}

// TestKeystoneRotation_WebAuthnRebinding drives the rotation gate end-to-end through the PRODUCTION
// alg (webauthn-es256): pin a key with an rpid+origin+credential_id, then a re-pin that changes the
// binding (same key, different rpid) WITHOUT rotate is refused 409 and leaves the store unchanged;
// WITH rotate it succeeds. It also asserts the public identifiers round-trip through GET status.
func TestKeystoneRotation_WebAuthnRebinding(t *testing.T) {
	env := newCtlTestEnv(t)
	pemES := es256PinPEM(t)
	base := operatorCredentialRequestJSON{
		Alg: string(trustlist.AlgWebAuthnES256), PublicKeyPEM: pemES,
		RPID: "rp.example", Origin: "https://rp.example", CredentialID: "cred-1",
	}
	if status := doJSON(t, http.MethodPost, env.opURL("operator-credential"), testOperatorToken, base, nil); status != http.StatusOK {
		t.Fatalf("first webauthn pin: status %d, want 200", status)
	}
	// GET status round-trips the public identifiers (the panel's authoritative source).
	st := keystoneStatus(t, env)
	if st.Alg != base.Alg || st.RPID != base.RPID || st.Origin != base.Origin || st.CredentialID != base.CredentialID || st.Fingerprint == "" {
		t.Fatalf("webauthn status round-trip mismatch: got %+v, want fields of %+v", st, base)
	}

	// Same key, CHANGED rpid, no rotate -> 409, store unchanged.
	rebind := base
	rebind.RPID = "evil.example"
	if status := doJSON(t, http.MethodPost, env.opURL("operator-credential"), testOperatorToken, rebind, nil); status != http.StatusConflict {
		t.Fatalf("webauthn rebinding without rotate: status %d, want 409", status)
	}
	if got := keystoneStatus(t, env); got.RPID != base.RPID {
		t.Fatalf("a refused rebinding must NOT mutate the stored rpid: got %q, want %q", got.RPID, base.RPID)
	}

	// With rotate it succeeds and the new rpid is stored.
	rebind.Rotate = true
	var res operatorCredentialPinResultJSON
	if status := doJSON(t, http.MethodPost, env.opURL("operator-credential"), testOperatorToken, rebind, &res); status != http.StatusOK || !res.Rotated {
		t.Fatalf("webauthn rebinding with rotate: status %d res %+v, want 200 rotated", status, res)
	}
	if got := keystoneStatus(t, env); got.RPID != "evil.example" {
		t.Fatalf("acked rebinding must store the new rpid, got %q", got.RPID)
	}
}
