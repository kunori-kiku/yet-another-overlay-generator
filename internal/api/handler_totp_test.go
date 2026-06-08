package api

// handler_totp_test.go drives the TOTP-2FA enroll/confirm/status flow and the
// second-factor enforcement at login, over the in-process operator mux.

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/kunorikiku/yet-another-overlay-generator/internal/controller"
)

// authedJSON does an authenticated request with an optional JSON body.
func authedJSON(t *testing.T, srv *httptest.Server, method, path, bearer, body string) *http.Response {
	t.Helper()
	var r io.Reader
	if body != "" {
		r = strings.NewReader(body)
	}
	req, _ := http.NewRequest(method, srv.URL+ctlBase+path, r)
	req.Header.Set("Authorization", "Bearer "+bearer)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	return resp
}

func TestTOTPEnrollAndLoginEnforcement(t *testing.T) {
	srv, _ := newLoginEnv(t, "") // operator admin/correct-password, no break-glass
	_, body := doLogin(t, srv, "admin", "correct-password")
	tok := sessionFrom(t, body)

	// Enroll -> a fresh secret (not yet active).
	r := authedJSON(t, srv, http.MethodPost, "totp/enroll", tok, "")
	if r.StatusCode != http.StatusOK {
		t.Fatalf("totp/enroll = %d", r.StatusCode)
	}
	var en totpEnrollResponseJSON
	_ = json.NewDecoder(r.Body).Decode(&en)
	r.Body.Close()
	if en.Secret == "" || !strings.HasPrefix(en.URI, "otpauth://") {
		t.Fatalf("enroll response = %+v", en)
	}

	now := time.Now().UTC()
	confirmCode, _ := controller.TOTPCode(en.Secret, now)

	// Confirm with a valid code activates 2FA.
	r = authedJSON(t, srv, http.MethodPost, "totp/confirm", tok,
		`{"secret":"`+en.Secret+`","code":"`+confirmCode+`"}`)
	if r.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(r.Body)
		t.Fatalf("totp/confirm = %d (%s)", r.StatusCode, b)
	}
	r.Body.Close()

	// Status reflects enabled.
	r = authedJSON(t, srv, http.MethodGet, "totp/status", tok, "")
	var st totpStatusResponseJSON
	_ = json.NewDecoder(r.Body).Decode(&st)
	r.Body.Close()
	if !st.Enabled {
		t.Fatal("status not enabled after confirm")
	}

	// Login WITHOUT a code now requires the second factor.
	resp, lbody := doLogin(t, srv, "admin", "correct-password")
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("login without code = %d, want 401", resp.StatusCode)
	}
	if !strings.Contains(lbody, "totp_required") {
		t.Errorf("401 body should signal totp_required: %s", lbody)
	}

	// Login WITH a fresh code (a step after the one the confirm consumed) succeeds.
	loginCode, _ := controller.TOTPCode(en.Secret, now.Add(30*time.Second))
	resp = doLoginTOTP(t, srv, "admin", "correct-password", loginCode)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("login with code = %d, want 200", resp.StatusCode)
	}

	// Replay: the SAME code is now rejected (the watermark advanced).
	resp = doLoginTOTP(t, srv, "admin", "correct-password", loginCode)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("replayed code login = %d, want 401", resp.StatusCode)
	}
}

// doLoginTOTP posts a login with a TOTP code.
func doLoginTOTP(t *testing.T, srv *httptest.Server, user, pass, code string) *http.Response {
	t.Helper()
	body, _ := json.Marshal(loginRequestJSON{Username: user, Password: pass, TOTP: code})
	resp, err := srv.Client().Post(srv.URL+ctlBase+"login", "application/json", strings.NewReader(string(body)))
	if err != nil {
		t.Fatalf("login POST: %v", err)
	}
	return resp
}
