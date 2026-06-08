package api

// handler_passkey.go is the operator PASSKEY-login surface (plan-5.2): registering and
// disabling a per-operator WebAuthn login credential, and the PASSWORDLESS login flow
// (begin/finish). The password+passkey 2FA leg lives in handler_login.go and reuses the
// shared helpers here (verifyLoginAssertion / writePasskeyChallenge).
//
// A login passkey is a SIBLING to the keystone operator-credential, NOT the same key:
//   - keystone  — ONE per tenant, signs a CONTENT-BOUND manifest (challenge = SHA256 of
//     the canonical membership). It is the network trust anchor (a node verifies it).
//   - login     — per operator, proves possession at panel login against a server-issued
//     RANDOM single-use challenge. It only gates the panel; no node ever sees it.
// Both are WebAuthn assertions verified by the same trustlist core (VerifyAssertion);
// only the challenge source differs (manifest hash vs random nonce).

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/kunorikiku/yet-another-overlay-generator/internal/controller"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/trustlist"
)

// loginChallengeTTL bounds how long an issued passkey login challenge is valid. It is
// short — a login ceremony completes in seconds — so a captured-but-unused challenge
// (and the assertion over it) expires quickly; single-use consumption is the primary
// anti-replay, the TTL is defense in depth.
const loginChallengeTTL = 5 * time.Minute

// allowCredentialJSON is one entry of a WebAuthn allowCredentials list: it tells the
// browser which registered credential to assert with. Type is always "public-key".
type allowCredentialJSON struct {
	Type string `json:"type"`
	ID   string `json:"id"` // base64url(rawId) of the registered login credential
}

// passkeyRequiredJSON is the 401 returned by POST /login when the password is correct but
// the operator has a login passkey: it carries a fresh random challenge + allowCredentials
// + rpid for the panel to run navigator.credentials.get and resubmit with the assertion.
// It is the passkey analogue of totpRequiredJSON.
type passkeyRequiredJSON struct {
	Error            string                `json:"error"`
	PasskeyRequired  bool                  `json:"passkey_required"`
	Challenge        string                `json:"challenge"`
	AllowCredentials []allowCredentialJSON `json:"allow_credentials"`
	RPID             string                `json:"rpid"`
}

// passkeyChallengeResponseJSON is the 200 carrying a fresh login challenge for a
// ceremony that is NOT a /login 401 — passwordless begin and the disable re-auth leg.
type passkeyChallengeResponseJSON struct {
	Challenge        string                `json:"challenge"`
	AllowCredentials []allowCredentialJSON `json:"allow_credentials"`
	RPID             string                `json:"rpid"`
}

type passkeyStatusResponseJSON struct {
	Registered bool `json:"registered"`
}

type passkeyRegisterRequestJSON struct {
	Alg          string `json:"alg"`            // webauthn-es256 | webauthn-eddsa
	CredentialID string `json:"credential_id"`  // base64url(rawId)
	PublicKeyPEM string `json:"public_key_pem"` // PKIX "PUBLIC KEY" PEM
	RPID         string `json:"rpid"`           // sha256(rpid) == authenticatorData[0:32]
	Origin       string `json:"origin"`         // advisory
}

type passkeyDisableRequestJSON struct {
	// Passkey is nil on the first (challenge-request) leg and carries the re-auth
	// assertion on the second leg.
	Passkey *trustlist.SignedTrustList `json:"passkey,omitempty"`
}

type passkeyLoginBeginRequestJSON struct {
	Username string `json:"username"`
}

type passkeyLoginFinishRequestJSON struct {
	Username string                     `json:"username"`
	Passkey  *trustlist.SignedTrustList `json:"passkey"`
}

// pinFromLoginCredential builds the verifier's pinned credential from an operator's
// stored LOGIN passkey (always a WebAuthn algorithm).
func pinFromLoginCredential(lc controller.LoginCredential) (trustlist.PinnedCredential, error) {
	return pinFromParts(lc.Alg, lc.CredentialID, lc.PublicKeyPEM, lc.RPID, lc.Origin)
}

// allowCredentialsFor returns the single-entry allowCredentials list naming an operator's
// registered login credential (so the browser asserts with THAT key), or an empty list
// when none is registered.
func allowCredentialsFor(lc *controller.LoginCredential) []allowCredentialJSON {
	if lc == nil {
		return []allowCredentialJSON{}
	}
	return []allowCredentialJSON{{Type: "public-key", ID: lc.CredentialID}}
}

// issueLoginChallenge mints a single-use random login challenge for operator, persists it
// (hash only), and returns the plaintext (base64url) for the browser to sign over.
func (h *ControllerHandler) issueLoginChallenge(ctx context.Context, operator string, now time.Time) (string, error) {
	challenge, lc := controller.NewLoginChallenge(operator, loginChallengeTTL, now)
	if err := h.store.CreateLoginChallenge(ctx, h.tenant, lc); err != nil {
		return "", err
	}
	return challenge, nil
}

// writePasskeyChallenge issues a fresh challenge and writes the 401 passkey_required
// response (the /login 2FA leg). On a store failure it writes a 500. The caller must
// have verified op.PasskeyEnabled() (op.LoginCredential is non-nil here).
func (h *ControllerHandler) writePasskeyChallenge(w http.ResponseWriter, ctx context.Context, op controller.Operator, now time.Time) {
	challenge, err := h.issueLoginChallenge(ctx, op.Username, now)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to issue passkey challenge")
		return
	}
	writeJSON(w, http.StatusUnauthorized, passkeyRequiredJSON{
		Error:            "passkey assertion required",
		PasskeyRequired:  true,
		Challenge:        challenge,
		AllowCredentials: allowCredentialsFor(op.LoginCredential),
		RPID:             op.LoginCredential.RPID,
	})
}

// verifyLoginAssertion is the shared passkey-assertion check for every login/re-auth
// path. It (1) extracts the challenge the assertion was signed over from its
// clientDataJSON, (2) ATOMICALLY consumes the matching single-use challenge record — it
// must be one WE issued, scoped to THIS operator, unexpired, and unconsumed — and (3)
// verifies the WebAuthn signature against the operator's pinned login credential over
// those exact challenge bytes. Returns nil only when all three hold.
//
// Consuming the challenge is the anti-replay: the random nonce is single-use, so a
// captured assertion cannot be replayed (its challenge is already burned), and two
// concurrent logins cannot both consume the same challenge. The challenge bytes passed to
// VerifyAssertion are recovered from clientDataJSON, so VerifyAssertion's own
// challenge-equality check is satisfied by construction; the load-bearing guarantees are
// the store-consume (we issued it, once) and the signature (the operator's key signed it).
func (h *ControllerHandler) verifyLoginAssertion(ctx context.Context, tenant controller.TenantID, op controller.Operator, art trustlist.SignedTrustList, now time.Time) error {
	if op.LoginCredential == nil {
		return errors.New("no login passkey registered")
	}
	challengeStr, err := trustlist.AssertionChallenge(art)
	if err != nil {
		return err
	}
	challengeBytes, err := base64.RawURLEncoding.DecodeString(challengeStr)
	if err != nil {
		return fmt.Errorf("decode assertion challenge: %w", err)
	}
	// Atomically burn the single-use challenge (must be ours, this operator, unexpired).
	if err := h.store.ConsumeLoginChallenge(ctx, tenant, controller.HashToken(challengeStr), op.Username, now); err != nil {
		return err
	}
	pin, err := pinFromLoginCredential(*op.LoginCredential)
	if err != nil {
		return err
	}
	return trustlist.VerifyAssertion(art, pin, challengeBytes)
}

// --- passkey management (operator-authed) ---

// HandlePasskeyStatus (GET) reports whether the current operator has a login passkey.
func (h *ControllerHandler) HandlePasskeyStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "only GET is supported")
		return
	}
	op, _, ok := h.currentOperator(w, r)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, passkeyStatusResponseJSON{Registered: op.PasskeyEnabled()})
}

// HandlePasskeyRegister (POST) registers (or replaces) the current operator's login
// passkey. The credential MUST be a WebAuthn algorithm (a raw Ed25519 has no assertion to
// verify at login) and its PEM must parse, with a non-empty rpid (an empty rpid disables
// relying-party binding — the verifier rejects it). Only the PUBLIC half is stored.
func (h *ControllerHandler) HandlePasskeyRegister(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "only POST is supported")
		return
	}
	op, tenant, ok := h.currentOperator(w, r)
	if !ok {
		return
	}
	var req passkeyRegisterRequestJSON
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	switch trustlist.Alg(req.Alg) {
	case trustlist.AlgWebAuthnES256:
		if _, err := trustlist.ParseES256Pin([]byte(req.PublicKeyPEM)); err != nil {
			writeError(w, http.StatusBadRequest, "invalid public_key_pem for alg: "+err.Error())
			return
		}
	case trustlist.AlgWebAuthnEdDSA:
		if _, err := trustlist.ParseEd25519PinPEM([]byte(req.PublicKeyPEM)); err != nil {
			writeError(w, http.StatusBadRequest, "invalid public_key_pem for alg: "+err.Error())
			return
		}
	default:
		writeError(w, http.StatusBadRequest, "unsupported alg (a login passkey must be webauthn-es256 or webauthn-eddsa)")
		return
	}
	if strings.TrimSpace(req.CredentialID) == "" {
		writeError(w, http.StatusBadRequest, "credential_id is required")
		return
	}
	if strings.TrimSpace(req.RPID) == "" {
		writeError(w, http.StatusBadRequest, "rpid is required (it binds the assertion's relying party)")
		return
	}
	now := time.Now().UTC()
	op.LoginCredential = &controller.LoginCredential{
		Alg:          req.Alg,
		CredentialID: req.CredentialID,
		PublicKeyPEM: req.PublicKeyPEM,
		RPID:         req.RPID,
		Origin:       req.Origin,
	}
	op.UpdatedAt = now
	if err := h.store.PutOperator(r.Context(), tenant, op); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to register passkey")
		return
	}
	_, _ = h.store.AppendAudit(r.Context(), tenant, controller.AuditEntry{
		Timestamp: now, Actor: "operator:" + op.Username, Action: "passkey-registered",
	})
	writeJSON(w, http.StatusOK, passkeyStatusResponseJSON{Registered: true})
}

// HandlePasskeyDisable (POST) removes the current operator's login passkey, requiring a
// fresh assertion so a hijacked session cannot strip the factor without the authenticator
// (mirrors TOTP-disable-requires-a-code). Two legs: an empty body issues a challenge
// (200 with challenge+allowCredentials); a body carrying the assertion verifies it and
// removes the credential. Idempotent when no passkey is registered.
func (h *ControllerHandler) HandlePasskeyDisable(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "only POST is supported")
		return
	}
	op, tenant, ok := h.currentOperator(w, r)
	if !ok {
		return
	}
	if op.LoginCredential == nil {
		writeJSON(w, http.StatusOK, passkeyStatusResponseJSON{Registered: false})
		return
	}
	var req passkeyDisableRequestJSON
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	now := time.Now().UTC()
	if req.Passkey == nil {
		// Leg 1: issue a re-auth challenge for the registered credential.
		challenge, err := h.issueLoginChallenge(r.Context(), op.Username, now)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to issue passkey challenge")
			return
		}
		writeJSON(w, http.StatusOK, passkeyChallengeResponseJSON{
			Challenge:        challenge,
			AllowCredentials: allowCredentialsFor(op.LoginCredential),
			RPID:             op.LoginCredential.RPID,
		})
		return
	}
	// Leg 2: verify the fresh assertion, then remove the credential.
	if err := h.verifyLoginAssertion(r.Context(), tenant, op, *req.Passkey, now); err != nil {
		writeError(w, http.StatusBadRequest, "passkey verification failed")
		return
	}
	op.LoginCredential = nil
	op.UpdatedAt = now
	if err := h.store.PutOperator(r.Context(), tenant, op); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to disable passkey")
		return
	}
	_, _ = h.store.AppendAudit(r.Context(), tenant, controller.AuditEntry{
		Timestamp: now, Actor: "operator:" + op.Username, Action: "passkey-disabled",
	})
	writeJSON(w, http.StatusOK, passkeyStatusResponseJSON{Registered: false})
}

// --- passwordless passkey login (unauthenticated) ---

// HandlePasskeyLoginBegin (POST, UNAUTH) issues a fresh login challenge for a username.
// When the operator exists and has a registered passkey it stores a single-use challenge
// scoped to them and returns it with allowCredentials naming the credential; otherwise it
// returns a DECOY challenge with empty allowCredentials (so the response shape is uniform
// and the ensuing finish fails uniformly). Passwordless login inherently reveals
// passkey-registration for a username via the empty-vs-present allowCredentials; that is a
// low-value signal and the actual authentication is rate-limited at finish.
func (h *ControllerHandler) HandlePasskeyLoginBegin(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "only POST is supported")
		return
	}
	var req passkeyLoginBeginRequestJSON
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if strings.TrimSpace(req.Username) == "" {
		writeError(w, http.StatusBadRequest, "username is required")
		return
	}
	now := time.Now().UTC()
	op, err := h.store.GetOperator(r.Context(), h.tenant, req.Username)
	if err == nil && op.LoginCredential != nil {
		challenge, err := h.issueLoginChallenge(r.Context(), op.Username, now)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to issue passkey challenge")
			return
		}
		writeJSON(w, http.StatusOK, passkeyChallengeResponseJSON{
			Challenge:        challenge,
			AllowCredentials: allowCredentialsFor(op.LoginCredential),
			RPID:             op.LoginCredential.RPID,
		})
		return
	}
	// Decoy: a random (UNSTORED) challenge with empty allowCredentials. The finish will
	// fail (no operator / no passkey / unconsumable challenge) with a uniform 401.
	decoy, _ := controller.NewLoginChallenge(req.Username, loginChallengeTTL, now)
	writeJSON(w, http.StatusOK, passkeyChallengeResponseJSON{
		Challenge:        decoy,
		AllowCredentials: []allowCredentialJSON{},
		RPID:             "",
	})
}

// HandlePasskeyLoginFinish (POST, UNAUTH) completes a passwordless passkey login: it
// verifies the WebAuthn assertion (consuming the single-use challenge) against the
// operator's registered login passkey and mints a session — NO password. It is
// rate-limited per username + source IP (the same limiter as password /login, so a
// locked-out account is locked across both paths). Every failure is a uniform 401.
func (h *ControllerHandler) HandlePasskeyLoginFinish(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "only POST is supported")
		return
	}
	var req passkeyLoginFinishRequestJSON
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	now := time.Now().UTC()
	userKey := "user:" + req.Username
	ipKey := "ip:" + clientIP(r)
	allowed, justLocked, retry := h.loginLimiter.registerAttempt(now, userKey, ipKey)
	if !allowed {
		w.Header().Set("Retry-After", strconv.Itoa(int(retry.Seconds())+1))
		writeError(w, http.StatusTooManyRequests, "too many login attempts; try again later")
		return
	}
	op, err := h.store.GetOperator(r.Context(), h.tenant, req.Username)
	if err != nil || op.LoginCredential == nil || req.Passkey == nil {
		h.auditLockout(r.Context(), now, req.Username, justLocked)
		writeError(w, http.StatusUnauthorized, "passkey login failed")
		return
	}
	if err := h.verifyLoginAssertion(r.Context(), h.tenant, op, *req.Passkey, now); err != nil {
		h.auditLockout(r.Context(), now, req.Username, justLocked)
		writeError(w, http.StatusUnauthorized, "passkey login failed")
		return
	}
	h.mintSessionResponse(w, r.Context(), op, now, userKey, ipKey)
}
