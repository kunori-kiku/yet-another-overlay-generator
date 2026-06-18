package api

// beta8_blockers_test.go — HTTP-level guards for two beta.8 enrollment fixes:
//   - S6: a minted enrollment token's TTL is capped server-side (an over-cap TTL is 400).
//   - S5: revoking a node purges its outstanding enrollment tokens, so a still-held token
//     can no longer enroll (resurrect) the revoked node.
// The controller-package tests cover the Enroll NodeRevoked guard and the purge store method
// directly; these pin the wiring at the HTTP boundary.

import (
	"net/http"
	"testing"

	"golang.zx2c4.com/wireguard/wgctrl/wgtypes"
)

// TestControllerHTTP_EnrollmentTokenTTLCapped: an over-cap TTL is rejected (400); a
// within-cap TTL still mints (S6).
func TestControllerHTTP_EnrollmentTokenTTLCapped(t *testing.T) {
	env := newCtlTestEnv(t)

	// 8 days exceeds the 7-day server-side cap.
	status := doJSON(t, http.MethodPost, env.opURL("enrollment-token"), testOperatorToken, enrollmentTokenRequestJSON{
		NodeID:     "node-1",
		TTLSeconds: 8 * 24 * 60 * 60,
	}, nil)
	if status != http.StatusBadRequest {
		t.Fatalf("over-cap TTL: status %d, want 400", status)
	}

	var resp enrollmentTokenResponseJSON
	status = doJSON(t, http.MethodPost, env.opURL("enrollment-token"), testOperatorToken, enrollmentTokenRequestJSON{
		NodeID:     "node-1",
		TTLSeconds: 3600,
	}, &resp)
	if status != http.StatusOK || resp.Token == "" {
		t.Fatalf("within-cap TTL: status %d, empty-token=%v, want 200 + token", status, resp.Token == "")
	}
}

// TestControllerHTTP_RevokeBlocksTokenResurrection: after a node is revoked, an
// outstanding enrollment token minted for it can no longer enroll it (S5 purge; the
// NodeRevoked guard is the belt-and-braces backstop). The attempt must NOT succeed.
func TestControllerHTTP_RevokeBlocksTokenResurrection(t *testing.T) {
	env := newCtlTestEnv(t)

	// Enroll node-1, then mint a SECOND, still-outstanding token for it (the leak/vector).
	env.enrollNode(t, "node-1")
	leakTok := env.mintEnrollmentToken(t, "node-1")

	if status := doJSON(t, http.MethodPost, env.opURL("revoke"), testOperatorToken, revokeRequestJSON{NodeID: "node-1"}, nil); status != http.StatusOK {
		t.Fatalf("revoke: status %d, want 200", status)
	}

	wgPriv, err := wgtypes.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("GeneratePrivateKey: %v", err)
	}
	// The outstanding token was purged on revoke → invalid-token 401. (If any token path
	// survived it would hit the NodeRevoked guard → 409.) Either way it must NOT be 200.
	status := doJSON(t, http.MethodPost, env.agentURL("enroll"), "", enrollRequestJSON{
		Token:       leakTok,
		NodeID:      "node-1",
		WGPublicKey: wgPriv.PublicKey().String(),
	}, nil)
	if status == http.StatusOK {
		t.Fatalf("revoked node re-enrolled with an outstanding token (200) — resurrection not blocked")
	}
	if status != http.StatusUnauthorized && status != http.StatusConflict {
		t.Fatalf("resurrection attempt: status %d, want 401 (token purged) or 409 (revoked guard)", status)
	}
}
