package api

// handler_passkey_test.go drives operator passkey login end to end over the in-process
// operator mux: register a login credential, complete the password+passkey (2FA) login,
// reject a replayed challenge, complete a PASSWORDLESS login, and disable the credential
// with a fresh assertion. It builds real WebAuthn assertions (ES256 + EdDSA) in-process
// so the trustlist verifier runs for real.

import (
	"context"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/kunorikiku/yet-another-overlay-generator/internal/controller"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/trustlist"
)

const (
	testRPID   = "panel.example.com"
	testOrigin = "https://panel.example.com"
)

// pkixPEM marshals a public key to a PKIX "PUBLIC KEY" PEM (what the verifier parses).
func pkixPEM(t *testing.T, pub any) string {
	t.Helper()
	der, err := x509.MarshalPKIXPublicKey(pub)
	if err != nil {
		t.Fatalf("marshal pkix: %v", err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der}))
}

// buildAssertion forges a valid UV WebAuthn assertion over `challenge`. Enrollment uses this
// shape; ordinary assertions may be presence-only under the enrollment-scoped UV policy.
func buildAssertion(t *testing.T, alg trustlist.Alg, rpid, origin string, challenge []byte, credID, pubPEM string, priv any) trustlist.SignedTrustList {
	t.Helper()
	return buildAssertionWithFlags(t, alg, rpid, origin, challenge, credID, pubPEM, priv, 0x01|0x04)
}

func buildAssertionWithFlags(t *testing.T, alg trustlist.Alg, rpid, origin string, challenge []byte, credID, pubPEM string, priv any, flags byte) trustlist.SignedTrustList {
	t.Helper()
	rpHash := sha256.Sum256([]byte(rpid))
	authData := make([]byte, 0, 37)
	authData = append(authData, rpHash[:]...)
	authData = append(authData, flags)
	authData = append(authData, 0, 0, 0, 0) // signature counter (0 — synced passkeys do this)
	cData := []byte(fmt.Sprintf(`{"type":"webauthn.get","challenge":%q,"origin":%q}`,
		base64.RawURLEncoding.EncodeToString(challenge), origin))
	cHash := sha256.Sum256(cData)
	signed := append(append([]byte{}, authData...), cHash[:]...)

	var sig []byte
	switch alg {
	case trustlist.AlgWebAuthnEdDSA:
		sig = ed25519.Sign(priv.(ed25519.PrivateKey), signed)
	case trustlist.AlgWebAuthnES256:
		d := sha256.Sum256(signed)
		var err error
		if sig, err = ecdsa.SignASN1(rand.Reader, priv.(*ecdsa.PrivateKey), d[:]); err != nil {
			t.Fatalf("ecdsa sign: %v", err)
		}
	default:
		t.Fatalf("unsupported alg %q", alg)
	}
	return trustlist.SignedTrustList{
		Alg:               alg,
		CredentialID:      credID,
		PublicKey:         pubPEM,
		Signature:         base64.RawURLEncoding.EncodeToString(sig),
		AuthenticatorData: base64.RawURLEncoding.EncodeToString(authData),
		ClientDataJSON:    base64.RawURLEncoding.EncodeToString(cData),
	}
}

// edKeypair / es256Keypair return (publicPEM, privateKey) for the two algorithms.
func edKeypair(t *testing.T) (string, ed25519.PrivateKey) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("ed25519 keygen: %v", err)
	}
	return pkixPEM(t, pub), priv
}

func es256Keypair(t *testing.T) (string, *ecdsa.PrivateKey) {
	t.Helper()
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("ecdsa keygen: %v", err)
	}
	return pkixPEM(t, priv.Public()), priv
}

func beginWebAuthnEnrollment(t *testing.T, srv *httptest.Server, tok, purpose string) []byte {
	t.Helper()
	body, _ := json.Marshal(webAuthnEnrollmentBeginRequestJSON{Purpose: purpose})
	r := authedJSON(t, srv, http.MethodPost, "webauthn/enrollment/begin", tok, string(body))
	defer r.Body.Close()
	if r.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(r.Body)
		t.Fatalf("webauthn/enrollment/begin = %d (%s)", r.StatusCode, b)
	}
	var response webAuthnEnrollmentBeginResponseJSON
	if err := json.NewDecoder(r.Body).Decode(&response); err != nil || response.Challenge == "" {
		t.Fatalf("decode enrollment challenge = (%+v, %v)", response, err)
	}
	challenge, err := base64.RawURLEncoding.DecodeString(response.Challenge)
	if err != nil {
		t.Fatalf("decode enrollment challenge: %v", err)
	}
	return challenge
}

func TestWebAuthnEnrollmentBeginRequiresAuthAndKnownPurpose(t *testing.T) {
	srv, _ := newLoginEnv(t, "")

	unauthBody, _ := json.Marshal(webAuthnEnrollmentBeginRequestJSON{Purpose: webAuthnEnrollmentLogin})
	resp, err := srv.Client().Post(srv.URL+ctlBase+"webauthn/enrollment/begin", "application/json", strings.NewReader(string(unauthBody)))
	if err != nil {
		t.Fatalf("unauthenticated enrollment begin: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauthenticated enrollment begin = %d, want 401", resp.StatusCode)
	}

	_, loginBody := doLogin(t, srv, "admin", "correct-password")
	tok := sessionFrom(t, loginBody)
	badBody, _ := json.Marshal(webAuthnEnrollmentBeginRequestJSON{Purpose: "unknown"})
	bad := authedJSON(t, srv, http.MethodPost, "webauthn/enrollment/begin", tok, string(badBody))
	bad.Body.Close()
	if bad.StatusCode != http.StatusBadRequest {
		t.Fatalf("unknown enrollment purpose = %d, want 400", bad.StatusCode)
	}

	if challenge := beginWebAuthnEnrollment(t, srv, tok, webAuthnEnrollmentLogin); len(challenge) != 32 {
		t.Fatalf("enrollment challenge length = %d, want 32", len(challenge))
	}
}

func TestWebAuthnLoginEnrollmentBeginRejectsBreakGlass(t *testing.T) {
	srv, store := newLoginEnv(t, controller.HashToken("break-glass"))
	// Exercise the dangerous name-collision case: the break-glass actor is named
	// "operator", and a real login account with that same name exists. Authorization
	// must depend on the authentication kind, not merely on the actor string.
	op, err := controller.NewOperator(DefaultOperatorName, "operator-password", time.Now().UTC())
	if err != nil {
		t.Fatalf("NewOperator(%q): %v", DefaultOperatorName, err)
	}
	if err := store.PutOperator(context.Background(), testTenant, op); err != nil {
		t.Fatalf("PutOperator(%q): %v", DefaultOperatorName, err)
	}
	body, _ := json.Marshal(webAuthnEnrollmentBeginRequestJSON{Purpose: webAuthnEnrollmentLogin})
	resp := authedJSON(t, srv, http.MethodPost, "webauthn/enrollment/begin", "break-glass", string(body))
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("break-glass login enrollment begin = %d, want 403", resp.StatusCode)
	}
}

func registerPasskey(t *testing.T, srv *httptest.Server, tok, alg, credID, pubPEM string, priv any) {
	t.Helper()
	proof := buildAssertion(t, trustlist.Alg(alg), testRPID, testOrigin,
		beginWebAuthnEnrollment(t, srv, tok, webAuthnEnrollmentLogin), credID, pubPEM, priv)
	body, _ := json.Marshal(passkeyRegisterRequestJSON{
		Alg: alg, CredentialID: credID, PublicKeyPEM: pubPEM, RPID: testRPID, Origin: testOrigin,
		EnrollmentProof: &proof,
	})
	r := authedJSON(t, srv, http.MethodPost, "passkey/register", tok, string(body))
	defer r.Body.Close()
	if r.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(r.Body)
		t.Fatalf("passkey/register = %d (%s)", r.StatusCode, b)
	}
}

func doLoginPasskey(t *testing.T, srv *httptest.Server, user, pass string, art trustlist.SignedTrustList) *http.Response {
	t.Helper()
	body, _ := json.Marshal(loginRequestJSON{Username: user, Password: pass, Passkey: &art})
	resp, err := srv.Client().Post(srv.URL+ctlBase+"login", "application/json", strings.NewReader(string(body)))
	if err != nil {
		t.Fatalf("login(passkey) POST: %v", err)
	}
	return resp
}

func TestPasskey2FALoginReplayAndPasswordless(t *testing.T) {
	srv, _ := newLoginEnv(t, "")
	_, body := doLogin(t, srv, "admin", "correct-password")
	tok := sessionFrom(t, body)

	pubPEM, priv := edKeypair(t)
	const credID = "login-cred-ed"
	registerPasskey(t, srv, tok, string(trustlist.AlgWebAuthnEdDSA), credID, pubPEM, priv)

	// Status reflects registered.
	r := authedJSON(t, srv, http.MethodGet, "passkey/status", tok, "")
	var st passkeyStatusResponseJSON
	_ = json.NewDecoder(r.Body).Decode(&st)
	r.Body.Close()
	if !st.Registered {
		t.Fatal("passkey/status not registered after register")
	}
	if st.Alg != string(trustlist.AlgWebAuthnEdDSA) || st.CredentialID != credID || st.PublicKeyPEM != pubPEM || st.RPID != testRPID || st.Origin != testOrigin {
		t.Fatalf("passkey/status descriptor = %+v, want the registered public descriptor", st)
	}

	// 2FA leg 1: password alone -> 401 passkey_required + a challenge + allowCredentials.
	resp, lbody := doLogin(t, srv, "admin", "correct-password")
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("password-only login = %d, want 401", resp.StatusCode)
	}
	var pr passkeyRequiredJSON
	if err := json.Unmarshal([]byte(lbody), &pr); err != nil {
		t.Fatalf("unmarshal passkey_required: %v (%s)", err, lbody)
	}
	if !pr.PasskeyRequired || pr.Challenge == "" {
		t.Fatalf("want passkey_required+challenge, got %s", lbody)
	}
	if len(pr.AllowCredentials) != 1 || pr.AllowCredentials[0].ID != credID || pr.AllowCredentials[0].Type != "public-key" {
		t.Fatalf("allowCredentials = %+v, want one public-key %q", pr.AllowCredentials, credID)
	}
	if pr.RPID != testRPID {
		t.Fatalf("rpid = %q, want %q", pr.RPID, testRPID)
	}
	if pr.Alg != string(trustlist.AlgWebAuthnEdDSA) {
		t.Fatalf("passkey_required alg = %q, want %q", pr.Alg, trustlist.AlgWebAuthnEdDSA)
	}

	// 2FA leg 2: sign the challenge -> 200 + session.
	chal, _ := base64.RawURLEncoding.DecodeString(pr.Challenge)
	// Runtime verification is deliberately presence-only: UV was proven once at enrollment.
	art := buildAssertionWithFlags(t, trustlist.AlgWebAuthnEdDSA, testRPID, testOrigin, chal, credID, pubPEM, priv, 0x01)
	resp = doLoginPasskey(t, srv, "admin", "correct-password", art)
	rb, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("password+passkey login = %d (%s)", resp.StatusCode, rb)
	}
	_ = sessionFrom(t, string(rb))

	// Replay: the SAME assertion (its challenge already burned) -> 401, NOT a session.
	resp = doLoginPasskey(t, srv, "admin", "correct-password", art)
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("replayed passkey login = %d, want 401", resp.StatusCode)
	}

	// Passwordless: begin -> challenge (with allowCredentials) -> finish -> 200 + session.
	beginBody, _ := json.Marshal(passkeyLoginBeginRequestJSON{Username: "admin"})
	br, err := srv.Client().Post(srv.URL+ctlBase+"login/passkey/begin", "application/json", strings.NewReader(string(beginBody)))
	if err != nil {
		t.Fatalf("begin POST: %v", err)
	}
	var cr passkeyChallengeResponseJSON
	_ = json.NewDecoder(br.Body).Decode(&cr)
	br.Body.Close()
	if cr.Challenge == "" || len(cr.AllowCredentials) != 1 || cr.AllowCredentials[0].ID != credID {
		t.Fatalf("begin response = %+v", cr)
	}
	cb, _ := base64.RawURLEncoding.DecodeString(cr.Challenge)
	// Ordinary passwordless login remains compatible with an existing UP-only credential.
	art2 := buildAssertionWithFlags(t, trustlist.AlgWebAuthnEdDSA, testRPID, testOrigin, cb, credID, pubPEM, priv, 0x01)
	finBody, _ := json.Marshal(passkeyLoginFinishRequestJSON{Username: "admin", Passkey: &art2})
	fr, err := srv.Client().Post(srv.URL+ctlBase+"login/passkey/finish", "application/json", strings.NewReader(string(finBody)))
	if err != nil {
		t.Fatalf("finish POST: %v", err)
	}
	fb, _ := io.ReadAll(fr.Body)
	fr.Body.Close()
	if fr.StatusCode != http.StatusOK {
		t.Fatalf("passwordless finish = %d (%s)", fr.StatusCode, fb)
	}
	_ = sessionFrom(t, string(fb))
}

// TestPasskeyLoginRejectsForgeries: a wrong-RPID assertion and a tampered signature are
// both rejected, and the ES256 algorithm path works end to end.
func TestPasskeyLoginRejectsForgeries(t *testing.T) {
	srv, _ := newLoginEnv(t, "")
	_, body := doLogin(t, srv, "admin", "correct-password")
	tok := sessionFrom(t, body)

	pubPEM, priv := es256Keypair(t)
	const credID = "login-cred-es"
	registerPasskey(t, srv, tok, string(trustlist.AlgWebAuthnES256), credID, pubPEM, priv)

	getChallenge := func() []byte {
		t.Helper()
		_, lbody := doLogin(t, srv, "admin", "correct-password")
		var pr passkeyRequiredJSON
		if err := json.Unmarshal([]byte(lbody), &pr); err != nil || pr.Challenge == "" {
			t.Fatalf("expected passkey_required challenge, got %s", lbody)
		}
		c, _ := base64.RawURLEncoding.DecodeString(pr.Challenge)
		return c
	}

	// Wrong RPID -> rpIdHash mismatch -> rejected.
	bad := buildAssertion(t, trustlist.AlgWebAuthnES256, "evil.example.com", testOrigin, getChallenge(), credID, pubPEM, priv)
	if resp := doLoginPasskey(t, srv, "admin", "correct-password", bad); resp.StatusCode != http.StatusUnauthorized {
		resp.Body.Close()
		t.Fatalf("wrong-rpid login = %d, want 401", resp.StatusCode)
	}

	// Tampered signature -> bad signature -> rejected.
	tampered := buildAssertion(t, trustlist.AlgWebAuthnES256, testRPID, testOrigin, getChallenge(), credID, pubPEM, priv)
	tampered.Signature = base64.RawURLEncoding.EncodeToString([]byte("not-a-valid-signature"))
	if resp := doLoginPasskey(t, srv, "admin", "correct-password", tampered); resp.StatusCode != http.StatusUnauthorized {
		resp.Body.Close()
		t.Fatalf("tampered-sig login = %d, want 401", resp.StatusCode)
	}

	// A correctly-signed ES256 assertion succeeds.
	good := buildAssertion(t, trustlist.AlgWebAuthnES256, testRPID, testOrigin, getChallenge(), credID, pubPEM, priv)
	resp := doLoginPasskey(t, srv, "admin", "correct-password", good)
	gb, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("valid ES256 login = %d (%s)", resp.StatusCode, gb)
	}
	_ = sessionFrom(t, string(gb))
}

// TestPasskeyLoginBeginRateLimited proves the unauthenticated passwordless begin endpoint
// is rate-limited (it persists a challenge per call for a passkey username, so an ungated
// begin is an unbounded-growth footgun). It must start returning 429 within the cap.
func TestPasskeyLoginBeginRateLimited(t *testing.T) {
	srv, _ := newLoginEnv(t, "")
	got429 := false
	for i := 0; i < maxLoginFailures+3; i++ {
		body, _ := json.Marshal(passkeyLoginBeginRequestJSON{Username: "admin"})
		resp, err := srv.Client().Post(srv.URL+ctlBase+"login/passkey/begin", "application/json", strings.NewReader(string(body)))
		if err != nil {
			t.Fatalf("begin POST: %v", err)
		}
		code := resp.StatusCode
		resp.Body.Close()
		if code == http.StatusTooManyRequests {
			got429 = true
			break
		}
	}
	if !got429 {
		t.Fatalf("begin never returned 429 within %d attempts — endpoint is not rate-limited", maxLoginFailures+3)
	}
}

// TestPasskeyRegisterRequiresOrigin (B3, plan-8 Phase 7): a login passkey registration with
// an EMPTY Origin is rejected at pin time with the coded req_field_required/field=origin,
// mirroring the existing RPID required-check. This makes webauthn.go's `if pin.Origin != ""`
// advisory origin gate authoritative for LOGIN pins (they always carry an Origin now) WITHOUT
// touching the shared VerifyAssertion crypto (the node keystone path keeps an intentional
// empty Origin). A whitespace-only Origin is likewise rejected (TrimSpace).
func TestPasskeyRegisterRequiresOrigin(t *testing.T) {
	srv, _ := newLoginEnv(t, "")
	_, body := doLogin(t, srv, "admin", "correct-password")
	tok := sessionFrom(t, body)

	pubPEM, priv := edKeypair(t)

	register := func(origin string) (int, string, string) {
		t.Helper()
		req := passkeyRegisterRequestJSON{
			Alg:          string(trustlist.AlgWebAuthnEdDSA),
			CredentialID: "login-cred-origin",
			PublicKeyPEM: pubPEM,
			RPID:         testRPID,
			Origin:       origin,
		}
		if strings.TrimSpace(origin) != "" {
			proof := buildAssertion(t, trustlist.AlgWebAuthnEdDSA, testRPID, origin,
				beginWebAuthnEnrollment(t, srv, tok, webAuthnEnrollmentLogin), req.CredentialID, pubPEM, priv)
			req.EnrollmentProof = &proof
		}
		reqBody, _ := json.Marshal(req)
		r := authedJSON(t, srv, http.MethodPost, "passkey/register", tok, string(reqBody))
		defer r.Body.Close()
		var env struct {
			Error struct {
				Code   string            `json:"code"`
				Params map[string]string `json:"params"`
			} `json:"error"`
		}
		_ = json.NewDecoder(r.Body).Decode(&env)
		return r.StatusCode, env.Error.Code, env.Error.Params["field"]
	}

	// Empty Origin -> rejected with the field-scoped coded error.
	if code, errCode, field := register(""); code != http.StatusBadRequest || errCode != "req_field_required" || field != "origin" {
		t.Fatalf("empty-origin register = (%d, %q, field=%q), want (400, req_field_required, origin)", code, errCode, field)
	}
	// Whitespace-only Origin -> also rejected (TrimSpace, like the RPID/credential_id checks).
	if code, errCode, field := register("   "); code != http.StatusBadRequest || errCode != "req_field_required" || field != "origin" {
		t.Fatalf("whitespace-origin register = (%d, %q, field=%q), want (400, req_field_required, origin)", code, errCode, field)
	}
	// A non-empty Origin still registers successfully (the existing happy path).
	if code, _, _ := register(testOrigin); code != http.StatusOK {
		t.Fatalf("non-empty-origin register = %d, want 200", code)
	}
}

// TestPasskeyRegisterRequiresUserVerifiedEnrollmentProof pins the new enrollment boundary: the
// candidate is stored only after it signs a purpose/actor-scoped controller challenge with UV.
// Missing, UP-only, credential-spliced, and replayed proofs all fail without mutating status.
func TestPasskeyRegisterRequiresUserVerifiedEnrollmentProof(t *testing.T) {
	srv, _ := newLoginEnv(t, "")
	_, body := doLogin(t, srv, "admin", "correct-password")
	tok := sessionFrom(t, body)
	pubPEM, priv := edKeypair(t)
	const credID = "login-cred-enrollment-proof"

	base := passkeyRegisterRequestJSON{
		Alg:          string(trustlist.AlgWebAuthnEdDSA),
		CredentialID: credID,
		PublicKeyPEM: pubPEM,
		RPID:         testRPID,
		Origin:       testOrigin,
	}
	post := func(req passkeyRegisterRequestJSON) int {
		t.Helper()
		body, _ := json.Marshal(req)
		resp := authedJSON(t, srv, http.MethodPost, "passkey/register", tok, string(body))
		defer resp.Body.Close()
		return resp.StatusCode
	}
	registered := func() bool {
		t.Helper()
		resp := authedJSON(t, srv, http.MethodGet, "passkey/status", tok, "")
		defer resp.Body.Close()
		var status passkeyStatusResponseJSON
		_ = json.NewDecoder(resp.Body).Decode(&status)
		return status.Registered
	}

	if code := post(base); code != http.StatusBadRequest {
		t.Fatalf("missing enrollment proof = %d, want 400", code)
	}

	// A valid proof over a challenge minted for the other enrollment purpose cannot be
	// consumed here, even though it belongs to the same authenticated actor and candidate.
	wrongPurposeChallenge := beginWebAuthnEnrollment(t, srv, tok, webAuthnEnrollmentKeystone)
	wrongPurpose := buildAssertion(t, trustlist.AlgWebAuthnEdDSA, testRPID, testOrigin,
		wrongPurposeChallenge, credID, pubPEM, priv)
	withWrongPurpose := base
	withWrongPurpose.EnrollmentProof = &wrongPurpose
	if code := post(withWrongPurpose); code != http.StatusBadRequest {
		t.Fatalf("cross-purpose enrollment proof = %d, want 400", code)
	}
	if registered() {
		t.Fatal("cross-purpose enrollment proof mutated passkey status")
	}

	upOnlyChallenge := beginWebAuthnEnrollment(t, srv, tok, webAuthnEnrollmentLogin)
	upOnly := buildAssertionWithFlags(t, trustlist.AlgWebAuthnEdDSA, testRPID, testOrigin,
		upOnlyChallenge, credID, pubPEM, priv, 0x01)
	withUPOnly := base
	withUPOnly.EnrollmentProof = &upOnly
	if code := post(withUPOnly); code != http.StatusBadRequest {
		t.Fatalf("UP-only enrollment proof = %d, want 400", code)
	}
	if registered() {
		t.Fatal("failed UP-only enrollment proof mutated passkey status")
	}

	// The failed UP-only proof did not consume its challenge. Retrying the exact candidate with a
	// valid UV assertion over that same nonce succeeds instead of forcing a new credential/challenge.
	// Do this before beginning another same-subject enrollment, because begin intentionally replaces
	// the actor's previous live challenge to bound abandoned ceremonies.
	good := buildAssertion(t, trustlist.AlgWebAuthnEdDSA, testRPID, testOrigin,
		upOnlyChallenge, credID, pubPEM, priv)
	withGood := base
	withGood.EnrollmentProof = &good
	if code := post(withGood); code != http.StatusOK {
		t.Fatalf("valid UV enrollment proof = %d, want 200", code)
	}
	if !registered() {
		t.Fatal("valid UV enrollment proof did not register passkey")
	}
	if code := post(withGood); code != http.StatusBadRequest {
		t.Fatalf("replayed enrollment proof = %d, want 400", code)
	}

	splicedChallenge := beginWebAuthnEnrollment(t, srv, tok, webAuthnEnrollmentLogin)
	spliced := buildAssertion(t, trustlist.AlgWebAuthnEdDSA, testRPID, testOrigin,
		splicedChallenge, "some-other-credential", pubPEM, priv)
	withSplice := base
	withSplice.EnrollmentProof = &spliced
	if code := post(withSplice); code != http.StatusBadRequest {
		t.Fatalf("credential-spliced enrollment proof = %d, want 400", code)
	}
	if !registered() {
		t.Fatal("failed credential-spliced proof removed the registered passkey")
	}
}

func TestPasskeyEnrollmentProofIsActorScopedAndExpires(t *testing.T) {
	srv, store := newLoginEnv(t, "")
	_, adminBody := doLogin(t, srv, "admin", "correct-password")
	adminToken := sessionFrom(t, adminBody)

	bob, err := controller.NewOperator("bob", "bob-password", time.Now().UTC())
	if err != nil {
		t.Fatalf("NewOperator(bob): %v", err)
	}
	if err := store.PutOperator(context.Background(), testTenant, bob); err != nil {
		t.Fatalf("PutOperator(bob): %v", err)
	}
	_, bobBody := doLogin(t, srv, "bob", "bob-password")
	bobToken := sessionFrom(t, bobBody)

	pubPEM, priv := edKeypair(t)
	const credID = "actor-scoped-credential"
	adminChallenge := beginWebAuthnEnrollment(t, srv, adminToken, webAuthnEnrollmentLogin)
	adminProof := buildAssertion(t, trustlist.AlgWebAuthnEdDSA, testRPID, testOrigin,
		adminChallenge, credID, pubPEM, priv)
	req := passkeyRegisterRequestJSON{
		Alg: string(trustlist.AlgWebAuthnEdDSA), CredentialID: credID, PublicKeyPEM: pubPEM,
		RPID: testRPID, Origin: testOrigin, EnrollmentProof: &adminProof,
	}
	post := func(token string, body passkeyRegisterRequestJSON) int {
		raw, _ := json.Marshal(body)
		resp := authedJSON(t, srv, http.MethodPost, "passkey/register", token, string(raw))
		defer resp.Body.Close()
		return resp.StatusCode
	}
	if status := post(bobToken, req); status != http.StatusBadRequest {
		t.Fatalf("Bob consuming Admin enrollment proof = %d, want 400", status)
	}

	// Seed an already-expired challenge with the exact subject/hash to exercise the finish path's
	// expiry check without sleeping through the production TTL.
	now := time.Now().UTC()
	encoded, expired := controller.NewAssertionChallenge(
		webAuthnEnrollmentSubject("bob", webAuthnEnrollmentLogin), -time.Minute, now,
	)
	if err := store.CreateAssertionChallenge(context.Background(), testTenant, expired, now); err != nil {
		t.Fatalf("seed expired challenge: %v", err)
	}
	challenge, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		t.Fatalf("decode seeded challenge: %v", err)
	}
	expiredProof := buildAssertion(t, trustlist.AlgWebAuthnEdDSA, testRPID, testOrigin,
		challenge, credID, pubPEM, priv)
	req.EnrollmentProof = &expiredProof
	if status := post(bobToken, req); status != http.StatusBadRequest {
		t.Fatalf("expired enrollment proof = %d, want 400", status)
	}
}

// TestPasskeyDisableRequiresAssertion: disable is two-phase — an empty body returns a
// challenge, and only a valid assertion removes the credential.
func TestPasskeyDisableRequiresAssertion(t *testing.T) {
	srv, _ := newLoginEnv(t, "")
	_, body := doLogin(t, srv, "admin", "correct-password")
	tok := sessionFrom(t, body)

	pubPEM, priv := edKeypair(t)
	const credID = "login-cred-disable"
	registerPasskey(t, srv, tok, string(trustlist.AlgWebAuthnEdDSA), credID, pubPEM, priv)

	// Leg 1: empty body -> a challenge to satisfy.
	r := authedJSON(t, srv, http.MethodPost, "passkey/disable", tok, "{}")
	var cr passkeyChallengeResponseJSON
	_ = json.NewDecoder(r.Body).Decode(&cr)
	r.Body.Close()
	if cr.Challenge == "" || len(cr.AllowCredentials) != 1 {
		t.Fatalf("disable leg-1 = %+v, want a challenge", cr)
	}

	// Leg 2: a valid assertion removes the credential.
	cb, _ := base64.RawURLEncoding.DecodeString(cr.Challenge)
	// Passkey removal is an ordinary assertion too: UV is requested client-side when available,
	// but an existing credential that returns only UP must not become impossible to remove.
	art := buildAssertionWithFlags(t, trustlist.AlgWebAuthnEdDSA, testRPID, testOrigin, cb, credID, pubPEM, priv, 0x01)
	disBody, _ := json.Marshal(passkeyDisableRequestJSON{Passkey: &art})
	r = authedJSON(t, srv, http.MethodPost, "passkey/disable", tok, string(disBody))
	var st passkeyStatusResponseJSON
	_ = json.NewDecoder(r.Body).Decode(&st)
	r.Body.Close()
	if st.Registered {
		t.Fatal("passkey still registered after a valid disable")
	}

	// After disable, a plain password login succeeds (no second factor required).
	resp, _ := doLogin(t, srv, "admin", "correct-password")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("password login after disable = %d, want 200", resp.StatusCode)
	}
}
