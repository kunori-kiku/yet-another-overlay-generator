package api

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kunorikiku/yet-another-overlay-generator/internal/apierr"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/controller"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/model"
)

// TestBootstrapReconcilesKeystoneAuditBeforeServing pins the audit-before-use boundary on the
// unauthenticated bootstrap route. A credential CAS may commit before its mandatory audit append;
// bootstrap must fail without distributing that new trust anchor until the durable transition
// marker can be reconciled.
func TestBootstrapReconcilesKeystoneAuditBeforeServing(t *testing.T) {
	ctx := context.Background()
	base := controller.NewMemStore()
	faults := &apiKeystoneAppendFaultStore{Store: base, failBeforeCommit: 2}
	pub, _, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("generate operator credential: %v", err)
	}
	cred := controller.OperatorCredential{Alg: "ed25519", PublicKeyPEM: ed25519PinPEM(t, pub)}
	if err := controller.CompareAndSetKeystoneCredential(ctx, faults, testTenant, nil, cred, &controller.AuditEntry{
		Actor: "operator:admin", Action: "pin-operator-credential",
	}); err == nil {
		t.Fatal("credential transition with injected audit failure succeeded")
	}

	h := NewControllerHandler(faults, testTenant, controller.HashToken(testOperatorToken), DefaultOperatorName, "dev")
	mux := http.NewServeMux()
	h.RegisterAgentRoutes(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	resp, err := srv.Client().Get(srv.URL + "/api/v1/agent/bootstrap")
	if err != nil {
		t.Fatalf("GET bootstrap while audit unavailable: %v", err)
	}
	body, readErr := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if readErr != nil {
		t.Fatalf("read blocked bootstrap response: %v", readErr)
	}
	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("bootstrap while audit unavailable = %d, want 500", resp.StatusCode)
	}
	if bytes.Contains(body, []byte(cred.PublicKeyPEM)) {
		t.Fatal("blocked bootstrap response distributed the unaudited credential")
	}
	if entries, err := base.ListAudit(ctx, testTenant); err != nil || len(entries) != 0 {
		t.Fatalf("audit before recovery = (%+v, %v), want empty", entries, err)
	}
	if _, err := base.GetPendingKeystoneTransition(ctx, testTenant); err != nil {
		t.Fatalf("bootstrap lost pending transition marker: %v", err)
	}

	resp, err = srv.Client().Get(srv.URL + "/api/v1/agent/bootstrap")
	if err != nil {
		t.Fatalf("GET bootstrap after audit recovery: %v", err)
	}
	body, readErr = io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if readErr != nil {
		t.Fatalf("read recovered bootstrap response: %v", readErr)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("bootstrap after audit recovery = %d, want 200: %s", resp.StatusCode, body)
	}
	if !bytes.Contains(body, []byte(cred.PublicKeyPEM)) {
		t.Fatal("recovered bootstrap omitted the audited credential")
	}
	entries, err := base.ListAudit(ctx, testTenant)
	if err != nil || len(entries) != 1 || entries[0].Action != "pin-operator-credential" {
		t.Fatalf("audit after recovery = (%+v, %v), want one pin event", entries, err)
	}
	if _, err := base.GetPendingKeystoneTransition(ctx, testTenant); !errors.Is(err, controller.ErrNotFound) {
		t.Fatalf("pending marker after recovery = %v, want ErrNotFound", err)
	}
}

// TestShellSingleQuote proves the POSIX single-quote escaping primitive (renamed from shQuote):
// empty and no-quote values wrap verbatim; a single quote becomes the '\” idiom; adjacent quotes
// each escape independently; newlines are preserved (so a multiline PEM survives); and a shell
// metacharacter is inert inside the single quotes.
func TestShellSingleQuote(t *testing.T) {
	cases := map[string]string{
		"":          "''",           // empty → an empty single-quoted word
		"abc":       "'abc'",        // no quote → wrapped verbatim
		"a'b":       `'a'\''b'`,     // one quote → close, escaped literal ', reopen
		"a''b":      `'a'\'''\''b'`, // adjacent quotes → each escaped independently
		"a\nb":      "'a\nb'",       // newline preserved (multiline PEM survives)
		";rm -rf /": "';rm -rf /'",  // a metacharacter is inert inside single quotes
	}
	for in, want := range cases {
		if got := shellSingleQuote(in); got != want {
			t.Errorf("shellSingleQuote(%q) = %q, want %q", in, got, want)
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
		nil,
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
		`write_operator_cred "$cred_file" "$OPERATOR_CRED_PEM"`,
		"--operator-cred $cred_file --operator-cred-alg ${OPERATOR_CRED_ALG}",
		// Bootstrap must RESTART (not "enable --now") so a re-bootstrap of a running daemon picks up
		// the new bearer token + re-pinned operator credential (read only at daemon startup).
		"systemctl enable yaog-agent.service",
		"systemctl restart yaog-agent.service",
		`URL="${GH_PROXY}${RELEASE_BASE}/${ASSET}"`,
		// write_operator_cred RE-PINS by default: a differing existing pin is overwritten (the script
		// runs as root and bakes the current keystone), with a loud NOTICE and a reprovision-keystone
		// pointer for the if-this-was-a-stale-script case (behavior tested below).
		"write_operator_cred() {",
		"re-pinning",
		"yaog-agent reprovision-keystone --operator-cred <cred.pem>",
		// Every node (fresh or re-pinned) writes the credential.
		`printf '%s\n' "$woc_pem" > "$woc_file"`,
	} {
		if !strings.Contains(s, want) {
			t.Errorf("rendered script is missing %q", want)
		}
	}
	// The restart fix is only meaningful if the no-op-on-running "enable --now" is GONE: a substring
	// check for "enable" alone would pass either way, so assert the old form is absent.
	if strings.Contains(s, "enable --now yaog-agent.service") {
		t.Error("rendered script still uses `enable --now` (no-op on a running daemon); must `restart` so a re-bootstrap reloads the token/cred")
	}
}

// TestRenderBootstrapScript_KeystoneOff: with no pinned credential, the operator-cred
// values render empty (the runtime OP_FLAGS block stays inert).
func TestRenderBootstrapScript_KeystoneOff(t *testing.T) {
	s := renderBootstrapScript("https://x", "", "https://r/dl", nil, nil)
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
// TestRenderBootstrapScript_AgentPinning (plan-6): with AgentBins configured, the script bakes the
// per-arch pin vars and verifies the downloaded binary against the SHA-256 (fail-closed) before
// install; with no pins it warns loudly and proceeds. Pin values are shell-safe (shellSingleQuote).
func TestRenderBootstrapScript_AgentPinning(t *testing.T) {
	bins := map[string]model.Artifact{
		"linux-amd64": {Asset: "yaog-agent-linux-amd64", SHA256: strings.Repeat("a", 64)},
		"linux-arm64": {Asset: "yaog-agent-linux-arm64", SHA256: strings.Repeat("b", 64)},
	}
	s := renderBootstrapScript("https://x", "", "https://r/dl", bins, nil)
	for _, want := range []string{
		"AGENT_SHA_linux_amd64='" + strings.Repeat("a", 64) + "'",
		"AGENT_ASSET_linux_amd64='yaog-agent-linux-amd64'",
		"AGENT_SHA_linux_arm64='" + strings.Repeat("b", 64) + "'",
		`pin_sha="${!sha_var:-}"`,
		"sha256sum -c -",
		"refusing to install",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("pinned bootstrap script missing %q", want)
		}
	}

	// No pins configured → the loud WARNING; no baked pin block (the body still references the lookup
	// var AGENT_SHA_linux_${agent_arch}, so assert the absence of the BAKED block header + a concrete
	// per-arch assignment, not the generic string).
	off := renderBootstrapScript("https://x", "", "https://r/dl", nil, nil)
	if !strings.Contains(off, "no SHA-256 pin configured for linux-") {
		t.Error("unpinned bootstrap script must warn that integrity is NOT verified")
	}
	if strings.Contains(off, "# per-arch agent-binary pins") || strings.Contains(off, "AGENT_SHA_linux_amd64=") {
		t.Error("unpinned bootstrap script must not bake any per-arch pin assignment")
	}
}

func TestRenderBootstrapScript_InjectionSafe(t *testing.T) {
	s := renderBootstrapScript("https://x'; rm -rf / #", "", "https://r/dl", nil, nil)
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
	goodDebs := map[string]model.MimicDebPin{"bookworm-amd64": {Asset: "mimic_0.1.0_amd64.deb", SHA256: sha}}

	good := []controller.ControllerSettings{
		{}, // empty = no catalog
		{MimicVersion: "0.1.0", MimicReleaseBase: base, MimicDebs: goodDebs},         // full
		{MimicVersion: "v1.2.3-beta.1", MimicReleaseBase: base, MimicDebs: goodDebs}, // semver pre-release
		{MimicReleaseBase: base, MimicDebs: goodDebs},                                // version optional
		{MimicReleaseBase: base, MimicDebs: map[string]model.MimicDebPin{"bookworm-amd64": {Asset: "mimic_0.7.1-1_amd64.deb", SHA256: sha, DKMSAsset: "mimic-dkms_0.7.1-1_amd64.deb", DKMSSHA256: sha}}}, // two-package: mimic + dkms companion
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
		{MimicReleaseBase: base, MimicDebs: map[string]model.MimicDebPin{"bookworm-amd64": {Asset: "mimic.deb", SHA256: "short"}}},  // bad sha
		{MimicReleaseBase: base, MimicDebs: map[string]model.MimicDebPin{"bookworm-amd64": {Asset: "m$(reboot).deb", SHA256: sha}}}, // unsafe asset
		{MimicReleaseBase: base, MimicDebs: map[string]model.MimicDebPin{"bad key": {Asset: "mimic.deb", SHA256: sha}}},             // bad key
		{MimicDebs: goodDebs}, // debs without a release base
		{MimicReleaseBase: base, MimicDebs: map[string]model.MimicDebPin{"bookworm-amd64": {Asset: "mimic.deb", SHA256: sha, DKMSAsset: "dkms.deb", DKMSSHA256: "short"}}},   // bad dkms sha
		{MimicReleaseBase: base, MimicDebs: map[string]model.MimicDebPin{"bookworm-amd64": {Asset: "mimic.deb", SHA256: sha, DKMSAsset: "d$(reboot).deb", DKMSSHA256: sha}}}, // unsafe dkms asset
		{MimicReleaseBase: base, MimicDebs: map[string]model.MimicDebPin{"bookworm-amd64": {Asset: "mimic.deb", SHA256: sha, DKMSAsset: "dkms.deb"}}},                        // incomplete companion (asset, no sha)
	}
	for i, cs := range bad {
		if err := validateMimicCatalog(cs); err == nil {
			t.Errorf("bad[%d] validateMimicCatalog = nil, want error (cs=%+v)", i, cs)
		}
	}
}

// TestValidateAgentRolloutRefusesNewer pins the plan-8 refuse-newer floor: a target_agent_version
// strictly newer than the controller's own build version is rejected (the controller can only roll
// agents to a version its own pipeline shipped), while equal/older targets pass and a dev/non-semver
// controller version DISABLES the floor (so a `go run` controller is never frozen). The floor runs
// AFTER the agent_bins precondition, so every reachable case carries a valid AgentBins pin.
func TestValidateAgentRolloutRefusesNewer(t *testing.T) {
	sha := strings.Repeat("a", 64)
	bins := map[string]model.Artifact{"linux-amd64": {Asset: "yaog-agent-linux-amd64", SHA256: sha}}

	cases := []struct {
		name              string
		target            string
		controllerVersion string
		wantCode          apierr.Code // "" = expect nil (accepted)
	}{
		{"newer target rejected", "v2.0.0-beta.10", "v2.0.0-beta.9", apierr.CodeAgentTargetNewerThanController},
		{"newer major rejected", "v3.0.0", "v2.0.0-beta.9", apierr.CodeAgentTargetNewerThanController},
		{"equal target accepted", "v2.0.0-beta.9", "v2.0.0-beta.9", ""},
		{"older target accepted", "v2.0.0-beta.8", "v2.0.0-beta.9", ""},
		{"dev controller disables floor", "v2.0.0-beta.10", "dev", ""},
		{"empty controller disables floor", "v2.0.0-beta.10", "", ""},
		{"empty target skips floor", "", "v2.0.0-beta.9", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cs := controller.ControllerSettings{TargetAgentVersion: tc.target}
			if tc.target != "" {
				cs.AgentBins = bins // satisfy the prior agent_bins precondition
			}
			err := validateAgentRollout(cs, tc.controllerVersion)
			if tc.wantCode == "" {
				if err != nil {
					t.Fatalf("validateAgentRollout(target=%q, controller=%q) = %v, want nil", tc.target, tc.controllerVersion, err)
				}
				return
			}
			if err == nil {
				t.Fatalf("validateAgentRollout(target=%q, controller=%q) = nil, want %s", tc.target, tc.controllerVersion, tc.wantCode)
			}
			if err.Code() != tc.wantCode {
				t.Errorf("validateAgentRollout(target=%q, controller=%q) code = %s, want %s", tc.target, tc.controllerVersion, err.Code(), tc.wantCode)
			}
		})
	}
}

// TestValidateOperatorCredentialBinding: the operator-credential RPID/Origin are baked
// (unquoted, by design — OP_FLAGS is a word-split multi-flag accumulator) into the
// bootstrap script. Validate-at-pin rejects whitespace (the word-splitting vector) and
// the same shell-dangerous byte class the mimic-catalog base check uses, so the unquoted
// ${OP_FLAGS} expansion stays safe by construction. Empty RPID/Origin are valid (keystone
// may carry an alg-only binding). A clean RPID/Origin pair passes.
func TestValidateOperatorCredentialBinding(t *testing.T) {
	good := []controller.OperatorCredential{
		{}, // empty binding is fine (no RPID/Origin to inject)
		{RPID: "overlay.example.com", Origin: "https://overlay.example.com"},
		{RPID: "overlay.example.com:9090"}, // a port colon is not in the dangerous class
		{Origin: "https://overlay.example.com:9090"},
	}
	for i, c := range good {
		if err := validateOperatorCredentialBinding(c); err != nil {
			t.Errorf("good[%d] validateOperatorCredentialBinding = %v, want nil (cred=%+v)", i, err, c)
		}
	}

	bad := []controller.OperatorCredential{
		{RPID: "overlay.example.com --inject-flag"},      // whitespace (word-split vector) in RPID
		{Origin: "https://overlay.example.com --daemon"}, // whitespace in Origin
		{RPID: "rp\tid"},                 // tab whitespace
		{Origin: "https://x\nhttps://y"}, // newline whitespace
		{RPID: "rp$(reboot)id"},          // shell metachar in RPID
		{Origin: "https://x;reboot"},     // shell metachar in Origin
		{RPID: "rp`id`"},                 // backtick in RPID
		{Origin: "https://x|y"},          // pipe in Origin
	}
	for i, c := range bad {
		err := validateOperatorCredentialBinding(c)
		if err == nil {
			t.Errorf("bad[%d] validateOperatorCredentialBinding = nil, want coded error (cred=%+v)", i, c)
			continue
		}
		if err.Code() != apierr.CodeReqFieldInvalid {
			t.Errorf("bad[%d] code = %q, want %q", i, err.Code(), apierr.CodeReqFieldInvalid)
		}
	}
}

// TestRenderBootstrapScript_SafeShellForms: the rendered script uses the set -e-safe
// flag-shift form and quotes the ExecStart controller/node-id (no silent abort on a
// trailing flag; no ExecStart word-split).
func TestRenderBootstrapScript_SafeShellForms(t *testing.T) {
	s := renderBootstrapScript("https://x", "", "https://r/dl", nil, nil)
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
	ch := NewControllerHandler(store, testTenant, controller.HashToken(testOperatorToken), DefaultOperatorName, "dev")
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
	// plan-9: a never-configured controller also surfaces the default mimic release base end-to-end,
	// so the .deb catalog assist has a working pre-fill instead of the assistNeedsBase hard error.
	if got.MimicReleaseBase != controller.DefaultMimicReleaseBase {
		t.Fatalf("default settings mimic base = %q, want %q", got.MimicReleaseBase, controller.DefaultMimicReleaseBase)
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
	ch := NewControllerHandler(store, testTenant, controller.HashToken(testOperatorToken), DefaultOperatorName, "dev")
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

// extractWriteOperatorCred pulls the write_operator_cred() shell function VERBATIM out of the
// rendered bootstrap script (from its definition line to the first column-0 "}"), so the
// behavioral test exercises the ACTUAL rendered logic — not a re-typed copy that could drift.
func extractWriteOperatorCred(t *testing.T, script string) string {
	t.Helper()
	const marker = "write_operator_cred() {"
	start := strings.Index(script, marker)
	if start < 0 {
		t.Fatalf("rendered script has no write_operator_cred() function")
	}
	rest := script[start:]
	end := strings.Index(rest, "\n}\n")
	if end < 0 {
		t.Fatalf("write_operator_cred() function has no closing brace")
	}
	return rest[:end+len("\n}\n")]
}

// TestBootstrap_WriteOperatorCredBehavior runs the EXTRACTED write_operator_cred function under
// bash to prove the RE-PIN-BY-DEFAULT behavior actually behaves (a textual strings.Contains gives
// false confidence). It asserts: a fresh node writes the PEM at 0600; an existing file with
// DIFFERENT content IS overwritten (re-pinned) AND logs a loud NOTICE pointing at
// reprovision-keystone; an existing file with the SAME content stays put with no NOTICE (idempotent).
func TestBootstrap_WriteOperatorCredBehavior(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not available")
	}
	cred := &controller.OperatorCredential{
		Alg:          "ed25519",
		PublicKeyPEM: "-----BEGIN PUBLIC KEY-----\nNEWKEY\n-----END PUBLIC KEY-----\n",
	}
	fn := extractWriteOperatorCred(t, renderBootstrapScript("https://x", "", "https://r/dl", nil, cred))
	const pem = "-----BEGIN PUBLIC KEY-----\nNEWKEY\n-----END PUBLIC KEY-----\n"

	// run invokes the extracted function with (credFile, pem) and returns stdout+stderr.
	run := func(t *testing.T, credFile, pemArg string) string {
		t.Helper()
		script := "set -eu\nOPERATOR_CRED_ALG=ed25519\n" + fn + "\nwrite_operator_cred \"$1\" \"$2\"\n"
		out, err := exec.Command("bash", "-c", script, "bash", credFile, pemArg).CombinedOutput()
		if err != nil {
			t.Fatalf("bash run: %v\n%s", err, out)
		}
		return string(out)
	}

	t.Run("fresh node writes 0600", func(t *testing.T) {
		dir := t.TempDir()
		credFile := filepath.Join(dir, "operator-cred.pem")
		run(t, credFile, pem)
		got, err := os.ReadFile(credFile)
		if err != nil {
			t.Fatalf("read: %v", err)
		}
		// printf '%s\n' appends a newline; compare key material (trailing-newline-insensitive).
		if strings.TrimRight(string(got), "\n") != strings.TrimRight(pem, "\n") {
			t.Fatalf("fresh write content = %q, want the PEM", got)
		}
		if info, _ := os.Stat(credFile); info.Mode().Perm() != 0o600 {
			t.Fatalf("mode = %v, want 0600", info.Mode().Perm())
		}
	})

	t.Run("differing existing pin IS re-pinned + loud NOTICE", func(t *testing.T) {
		dir := t.TempDir()
		credFile := filepath.Join(dir, "operator-cred.pem")
		old := "-----BEGIN PUBLIC KEY-----\nOLDKEY\n-----END PUBLIC KEY-----\n"
		if err := os.WriteFile(credFile, []byte(old), 0o600); err != nil {
			t.Fatalf("seed: %v", err)
		}
		out := run(t, credFile, pem)
		got, _ := os.ReadFile(credFile)
		// Re-pin by default: a differing existing pin IS overwritten with the script's baked credential.
		if strings.TrimRight(string(got), "\n") != strings.TrimRight(pem, "\n") {
			t.Fatalf("a DIFFERING existing pin must be RE-PINNED to the baked credential; got %q", got)
		}
		// 0600 preserved on the overwrite.
		if info, _ := os.Stat(credFile); info.Mode().Perm() != 0o600 {
			t.Fatalf("re-pin mode = %v, want 0600", info.Mode().Perm())
		}
		// The overwrite must be LOUD (not silent), and still point at reprovision-keystone for the
		// stale-script-downgrade case.
		if !strings.Contains(out, "re-pinning") || !strings.Contains(out, "reprovision-keystone") {
			t.Fatalf("expected a loud re-pin NOTICE pointing at reprovision-keystone, got:\n%s", out)
		}
	})

	t.Run("same existing pin stays put (idempotent)", func(t *testing.T) {
		dir := t.TempDir()
		credFile := filepath.Join(dir, "operator-cred.pem")
		if err := os.WriteFile(credFile, []byte(pem), 0o600); err != nil {
			t.Fatalf("seed: %v", err)
		}
		out := run(t, credFile, pem)
		// Same key material (the compare normalizes trailing newlines, so it is not a re-pin).
		if got, _ := os.ReadFile(credFile); strings.TrimRight(string(got), "\n") != strings.TrimRight(pem, "\n") {
			t.Fatalf("idempotent write changed the key material: %q", got)
		}
		if strings.Contains(out, "re-pinning") || strings.Contains(out, "DIFFERS") {
			t.Fatalf("an identical pin must not log a re-pin NOTICE, got:\n%s", out)
		}
	})
}
