package api

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kunorikiku/yet-another-overlay-generator/internal/controller"
)

// TestShQuote: values are single-quoted; embedded single quotes are escaped as '\”;
// newlines are preserved (so a multiline PEM survives).
func TestShQuote(t *testing.T) {
	cases := map[string]string{
		"abc":       "'abc'",
		"a'b":       `'a'\''b'`,
		"a\nb":      "'a\nb'",
		";rm -rf /": "';rm -rf /'", // a metacharacter is inert inside single quotes
	}
	for in, want := range cases {
		if got := shQuote(in); got != want {
			t.Errorf("shQuote(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestRenderBootstrapScript_KeystoneOn: the rendered script carries the injected
// (single-quoted) config, the keystone --operator-cred wiring, arch mapping, enroll +
// daemon run.
func TestRenderBootstrapScript_KeystoneOn(t *testing.T) {
	cred := &controller.OperatorCredential{
		Alg:          "webauthn-es256",
		RPID:         "overlay.example.com",
		Origin:       "https://overlay.example.com",
		PublicKeyPEM: "-----BEGIN PUBLIC KEY-----\nABCDEF\n-----END PUBLIC KEY-----\n",
	}
	s := renderBootstrapScript(
		"https://overlay.example.com:9090/s3cr3t",
		"https://gh-proxy.com/",
		"https://github.com/o/r/releases/latest/download",
		cred,
	)
	for _, want := range []string{
		"#!/usr/bin/env bash",
		"CONTROLLER='https://overlay.example.com:9090/s3cr3t'",
		"GH_PROXY='https://gh-proxy.com/'",
		"RELEASE_BASE='https://github.com/o/r/releases/latest/download'",
		"OPERATOR_CRED_ALG='webauthn-es256'",
		"OPERATOR_RPID='overlay.example.com'",
		"BEGIN PUBLIC KEY",
		"yaog-agent-linux-amd64",
		"yaog-agent-linux-arm64",
		`yaog-agent enroll --controller "$CONTROLLER"`,
		"--operator-cred /etc/wireguard/operator-cred.pem",
		"systemctl enable --now yaog-agent.service",
		`URL="${GH_PROXY}${RELEASE_BASE}/${ASSET}"`,
	} {
		if !strings.Contains(s, want) {
			t.Errorf("rendered script is missing %q", want)
		}
	}
}

// TestRenderBootstrapScript_KeystoneOff: with no pinned credential, the operator-cred
// values render empty (the runtime OP_FLAGS block stays inert).
func TestRenderBootstrapScript_KeystoneOff(t *testing.T) {
	s := renderBootstrapScript("https://x", "", "https://r/dl", nil)
	for _, want := range []string{
		"OPERATOR_CRED_PEM=''",
		"OPERATOR_CRED_ALG=''",
		"GH_PROXY=''",
		"CONTROLLER='https://x'",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("rendered script missing %q", want)
		}
	}
}

// TestRenderBootstrapScript_InjectionSafe: a hostile single quote in an injected value
// is escaped, never breaking out of the single-quoted assignment.
func TestRenderBootstrapScript_InjectionSafe(t *testing.T) {
	s := renderBootstrapScript("https://x'; rm -rf / #", "", "https://r/dl", nil)
	if strings.Contains(s, "CONTROLLER='https://x'; rm -rf / #'") {
		t.Fatal("single quote was not escaped — injection possible")
	}
	if !strings.Contains(s, `CONTROLLER='https://x'\''; rm -rf / #'`) {
		t.Errorf("expected the embedded quote to be escaped as '\\'' in:\n%s", s)
	}
}

// TestBootstrapHTTP: operator GET/POST /settings persists and is reflected by the
// (unauthenticated) agent GET /bootstrap.
func TestBootstrapHTTP(t *testing.T) {
	store := controller.NewMemStore()
	ch := NewControllerHandler(store, testTenant, controller.HashToken(testOperatorToken), DefaultOperatorName)
	opMux := http.NewServeMux()
	ch.RegisterOperatorRoutes(opMux)
	agentMux := http.NewServeMux()
	ch.RegisterAgentRoutes(agentMux)
	opSrv := httptest.NewServer(opMux)
	defer opSrv.Close()
	agentSrv := httptest.NewServer(agentMux)
	defer agentSrv.Close()

	const base = "/api/v1/controller/"

	opReq := func(method, route, body string) *http.Response {
		t.Helper()
		var r io.Reader
		if body != "" {
			r = strings.NewReader(body)
		}
		req, _ := http.NewRequest(method, opSrv.URL+base+route, r)
		req.Header.Set("Authorization", "Bearer "+testOperatorToken)
		if body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		resp, err := opSrv.Client().Do(req)
		if err != nil {
			t.Fatalf("%s %s: %v", method, route, err)
		}
		return resp
	}

	// GET /settings -> defaults (empty public URL, default release URL).
	resp := opReq(http.MethodGet, "settings", "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET settings = %d", resp.StatusCode)
	}
	var got settingsJSON
	_ = json.NewDecoder(resp.Body).Decode(&got)
	resp.Body.Close()
	if got.PublicAgentURL != "" || got.AgentReleaseBaseURL != controller.DefaultAgentReleaseBaseURL {
		t.Fatalf("default settings = %+v", got)
	}

	// POST invalid public_agent_url -> 400.
	resp = opReq(http.MethodPost, "settings", `{"public_agent_url":"not a url"}`)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("POST invalid url = %d, want 400", resp.StatusCode)
	}
	resp.Body.Close()

	// POST valid settings.
	body, _ := json.Marshal(settingsJSON{
		PublicAgentURL: "https://overlay.example.com",
		GithubProxy:    "https://gh-proxy.com/",
	})
	resp = opReq(http.MethodPost, "settings", string(body))
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("POST settings = %d (%s)", resp.StatusCode, b)
	}
	resp.Body.Close()

	// GET /bootstrap (agent mux, NO auth) reflects the saved settings.
	bresp, err := agentSrv.Client().Get(agentSrv.URL + base + "bootstrap")
	if err != nil {
		t.Fatalf("GET bootstrap: %v", err)
	}
	script, _ := io.ReadAll(bresp.Body)
	bresp.Body.Close()
	if bresp.StatusCode != http.StatusOK {
		t.Fatalf("GET bootstrap = %d", bresp.StatusCode)
	}
	if ct := bresp.Header.Get("Content-Type"); !strings.Contains(ct, "shellscript") {
		t.Errorf("bootstrap Content-Type = %q", ct)
	}
	for _, want := range []string{
		"CONTROLLER='https://overlay.example.com'",
		"GH_PROXY='https://gh-proxy.com/'",
	} {
		if !bytes.Contains(script, []byte(want)) {
			t.Errorf("bootstrap script missing %q", want)
		}
	}

	// The bootstrap route must NOT require auth (it is served on the agent port like
	// /enroll): a bare GET with no bearer already succeeded above, confirming that.
}
