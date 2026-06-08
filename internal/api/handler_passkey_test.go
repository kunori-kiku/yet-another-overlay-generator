package api

// handler_passkey_test.go drives operator passkey login end to end over the in-process
// operator mux: register a login credential, complete the password+passkey (2FA) login,
// reject a replayed challenge, complete a PASSWORDLESS login, and disable the credential
// with a fresh assertion. It builds real WebAuthn assertions (ES256 + EdDSA) in-process
// so the trustlist verifier runs for real.

import (
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

// buildAssertion forges a valid WebAuthn assertion over `challenge` (the RAW nonce
// bytes) signed by priv: authenticatorData = sha256(rpid)||UP-flag||counter(0), and the
// signature covers authData || sha256(clientDataJSON), exactly as the verifier checks.
func buildAssertion(t *testing.T, alg trustlist.Alg, rpid, origin string, challenge []byte, credID, pubPEM string, priv any) trustlist.SignedTrustList {
	t.Helper()
	rpHash := sha256.Sum256([]byte(rpid))
	authData := make([]byte, 0, 37)
	authData = append(authData, rpHash[:]...)
	authData = append(authData, 0x01)       // flags: User-Present
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

func registerPasskey(t *testing.T, srv *httptest.Server, tok, alg, credID, pubPEM string) {
	t.Helper()
	body, _ := json.Marshal(passkeyRegisterRequestJSON{
		Alg: alg, CredentialID: credID, PublicKeyPEM: pubPEM, RPID: testRPID, Origin: testOrigin,
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
	registerPasskey(t, srv, tok, string(trustlist.AlgWebAuthnEdDSA), credID, pubPEM)

	// Status reflects registered.
	r := authedJSON(t, srv, http.MethodGet, "passkey/status", tok, "")
	var st passkeyStatusResponseJSON
	_ = json.NewDecoder(r.Body).Decode(&st)
	r.Body.Close()
	if !st.Registered {
		t.Fatal("passkey/status not registered after register")
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
	art := buildAssertion(t, trustlist.AlgWebAuthnEdDSA, testRPID, testOrigin, chal, credID, pubPEM, priv)
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
	art2 := buildAssertion(t, trustlist.AlgWebAuthnEdDSA, testRPID, testOrigin, cb, credID, pubPEM, priv)
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
	registerPasskey(t, srv, tok, string(trustlist.AlgWebAuthnES256), credID, pubPEM)

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

// TestPasskeyDisableRequiresAssertion: disable is two-phase — an empty body returns a
// challenge, and only a valid assertion removes the credential.
func TestPasskeyDisableRequiresAssertion(t *testing.T) {
	srv, _ := newLoginEnv(t, "")
	_, body := doLogin(t, srv, "admin", "correct-password")
	tok := sessionFrom(t, body)

	pubPEM, priv := edKeypair(t)
	const credID = "login-cred-disable"
	registerPasskey(t, srv, tok, string(trustlist.AlgWebAuthnEdDSA), credID, pubPEM)

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
	art := buildAssertion(t, trustlist.AlgWebAuthnEdDSA, testRPID, testOrigin, cb, credID, pubPEM, priv)
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
