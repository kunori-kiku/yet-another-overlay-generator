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
	"github.com/kunorikiku/yet-another-overlay-generator/internal/renderer"
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
		"cred_file=/etc/wireguard/operator-cred.pem",
		"--operator-cred $cred_file --operator-cred-alg ${OPERATOR_CRED_ALG}",
		"systemctl enable --now yaog-agent.service",
		`URL="${GH_PROXY}${RELEASE_BASE}/${ASSET}"`,
		// The stale-clobber guard: a differing existing pin is NOT overwritten, and the operator
		// is pointed at reprovision-keystone to adopt a rotated keystone deliberately.
		"DIFFERS from this script's baked operator credential",
		"yaog-agent reprovision-keystone --operator-cred <new-cred.pem>",
		// A fresh node (no existing pin) still writes the credential as before.
		`printf '%s\n' "$OPERATOR_CRED_PEM" > "$cred_file"`,
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

// TestValidateAbsoluteHTTPURL: accepts http(s) URLs, rejects non-http schemes, missing
// host, and whitespace (the bootstrap-script word-split vector).
func TestValidateAbsoluteHTTPURL(t *testing.T) {
	good := []string{"https://overlay.example.com", "http://10.0.0.1:9090/s3cr3t", "https://gh-proxy.com/"}
	for _, s := range good {
		if err := validateAbsoluteHTTPURL(s); err != nil {
			t.Errorf("validateAbsoluteHTTPURL(%q) = %v, want nil", s, err)
		}
	}
	bad := []string{
		"not a url",                // whitespace
		"ftp://x",                  // wrong scheme
		"javascript:alert(1)",      // wrong scheme
		"https://",                 // no host
		"https://ok.example/p ath", // embedded space
		"https://ok.example/p\tx",  // embedded tab
		"https://ok.example/p\nx",  // embedded newline
	}
	for _, s := range bad {
		if err := validateAbsoluteHTTPURL(s); err == nil {
			t.Errorf("validateAbsoluteHTTPURL(%q) = nil, want error", s)
		}
	}
}

// TestValidateMimicCatalog: D8 strict format rules on the mimic GitHub-.deb catalog. An empty
// catalog is valid; a good catalog passes; bad semver / non-http base / bad sha / unsafe asset /
// bad key / debs-without-base are each rejected with a coded field error.
func TestValidateMimicCatalog(t *testing.T) {
	sha := strings.Repeat("a", 64)
	base := "https://github.com/hack3ric/mimic/releases/download/v0.1.0"
	goodDebs := map[string]renderer.Artifact{"bookworm-amd64": {Asset: "mimic_0.1.0_amd64.deb", SHA256: sha}}

	good := []controller.ControllerSettings{
		{}, // empty = no catalog
		{MimicVersion: "0.1.0", MimicReleaseBase: base, MimicDebs: goodDebs},         // full
		{MimicVersion: "v1.2.3-beta.1", MimicReleaseBase: base, MimicDebs: goodDebs}, // semver pre-release
		{MimicReleaseBase: base, MimicDebs: goodDebs},                                // version optional
	}
	for i, cs := range good {
		if err := validateMimicCatalog(cs); err != nil {
			t.Errorf("good[%d] validateMimicCatalog = %v, want nil", i, err)
		}
	}

	bad := []controller.ControllerSettings{
		{MimicVersion: "not.semver", MimicReleaseBase: base, MimicDebs: goodDebs},                                                   // bad semver
		{MimicReleaseBase: "ftp://x", MimicDebs: goodDebs},                                                                          // non-http base
		{MimicReleaseBase: "https://ok/ p", MimicDebs: goodDebs},                                                                    // whitespace in base
		{MimicReleaseBase: "https://ok/p$(reboot)", MimicDebs: goodDebs},                                                            // shell metachars (valid URL, caught by the charset guard)
		{MimicReleaseBase: base, MimicDebs: map[string]renderer.Artifact{"bookworm-amd64": {Asset: "mimic.deb", SHA256: "short"}}},  // bad sha
		{MimicReleaseBase: base, MimicDebs: map[string]renderer.Artifact{"bookworm-amd64": {Asset: "m$(reboot).deb", SHA256: sha}}}, // unsafe asset
		{MimicReleaseBase: base, MimicDebs: map[string]renderer.Artifact{"bad key": {Asset: "mimic.deb", SHA256: sha}}},             // bad key
		{MimicDebs: goodDebs}, // debs without a release base
	}
	for i, cs := range bad {
		if err := validateMimicCatalog(cs); err == nil {
			t.Errorf("bad[%d] validateMimicCatalog = nil, want error (cs=%+v)", i, cs)
		}
	}
}

// TestRenderBootstrapScript_SafeShellForms: the rendered script uses the set -e-safe
// flag-shift form and quotes the ExecStart controller/node-id (no silent abort on a
// trailing flag; no ExecStart word-split).
func TestRenderBootstrapScript_SafeShellForms(t *testing.T) {
	s := renderBootstrapScript("https://x", "", "https://r/dl", nil)
	for _, want := range []string{
		`shift; [ $# -gt 0 ] && shift ;;`, // safe shift
		`ExecStart=/usr/local/bin/yaog-agent run --controller "${CONTROLLER}" --node-id "${NODE_ID}"`, // quoted
		`trap 'rm -f "$tmp_bin"' EXIT`, // temp cleanup
		`--proto '=https,http' "$URL"`, // proto pin (single comma list — both schemes)
	} {
		if !strings.Contains(s, want) {
			t.Errorf("rendered script missing %q", want)
		}
	}
	// The scheme-widening curl fallback must be gone.
	if strings.Contains(s, `|| curl -fL --retry 3 "$URL"`) {
		t.Error("script still has the proto-dropping curl fallback")
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

	const opBase = "/api/v1/operator/" // operator routes (settings)
	const agentBase = "/api/v1/agent/" // agent routes (bootstrap)

	opReq := func(method, route, body string) *http.Response {
		t.Helper()
		var r io.Reader
		if body != "" {
			r = strings.NewReader(body)
		}
		req, _ := http.NewRequest(method, opSrv.URL+opBase+route, r)
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
	bresp, err := agentSrv.Client().Get(agentSrv.URL + agentBase + "bootstrap")
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

// TestSettingsTranslucencyRoundTrip: the panel Translucency setting (P5) defaults to
// true, round-trips through GET/POST /settings, and is NEVER injected into the agent
// bootstrap script (it is a panel-appearance setting with no bearing on a node).
func TestSettingsTranslucencyRoundTrip(t *testing.T) {
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

	const opBase = "/api/v1/operator/" // operator routes (settings)
	const agentBase = "/api/v1/agent/" // agent routes (bootstrap)
	opReq := func(method, route, body string) *http.Response {
		t.Helper()
		var r io.Reader
		if body != "" {
			r = strings.NewReader(body)
		}
		req, _ := http.NewRequest(method, opSrv.URL+opBase+route, r)
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

	// GET defaults: translucency ON.
	resp := opReq(http.MethodGet, "settings", "")
	var got settingsJSON
	_ = json.NewDecoder(resp.Body).Decode(&got)
	resp.Body.Close()
	if !got.Translucency {
		t.Fatalf("default translucency = false, want true")
	}

	// POST translucency:false round-trips and persists.
	body, _ := json.Marshal(settingsJSON{PublicAgentURL: "https://x.example.com", Translucency: false})
	resp = opReq(http.MethodPost, "settings", string(body))
	var saved settingsJSON
	_ = json.NewDecoder(resp.Body).Decode(&saved)
	resp.Body.Close()
	if saved.Translucency {
		t.Fatalf("POST translucency=false returned true")
	}
	resp = opReq(http.MethodGet, "settings", "")
	var reread settingsJSON
	_ = json.NewDecoder(resp.Body).Decode(&reread)
	resp.Body.Close()
	if reread.Translucency {
		t.Fatalf("translucency=false not persisted")
	}

	// The bootstrap script must NOT mention translucency.
	bresp, err := agentSrv.Client().Get(agentSrv.URL + agentBase + "bootstrap")
	if err != nil {
		t.Fatalf("GET bootstrap: %v", err)
	}
	script, _ := io.ReadAll(bresp.Body)
	bresp.Body.Close()
	if bytes.Contains(bytes.ToLower(script), []byte("translucen")) {
		t.Errorf("bootstrap script must not contain translucency")
	}
}
