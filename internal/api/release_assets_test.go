package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/kunorikiku/yet-another-overlay-generator/internal/controller"
)

// A GitHub-releases-API-shaped body exercising the .deb filter: two real arch packages, a dkms
// package (kept — it is a real installable package, only the FE deriveKey blanks its label), a
// dbgsym + a .ddeb debug sidecar (both excluded), a non-.deb (excluded), and a duplicate of the
// amd64 package (deduped).
const releaseAssetsJSON = `{"assets":[
  {"name":"mimic_0.4.0_amd64.deb"},
  {"name":"mimic_0.4.0_arm64.deb"},
  {"name":"mimic-dkms_0.4.0_all.deb"},
  {"name":"mimic-dbgsym_0.4.0_amd64.deb"},
  {"name":"mimic_0.4.0_amd64.ddeb"},
  {"name":"checksums.txt"},
  {"name":"mimic_0.4.0_amd64.deb"}
]}`

// newReleaseAPIServer serves body (with the given content type) for any path — so a loopback
// gh-proxy stub can stand in for api.github.com regardless of the derived path.
func newReleaseAPIServer(t *testing.T, status int, contentType, body string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", contentType)
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestDeriveReleasesAPIURL(t *testing.T) {
	cases := []struct {
		name    string
		base    string
		want    string
		wantErr bool
	}{
		{"latest/download", "https://github.com/o/r/releases/latest/download", "https://api.github.com/repos/o/r/releases/latest", false},
		{"download/<tag>", "https://github.com/o/r/releases/download/v1.4.0", "https://api.github.com/repos/o/r/releases/tags/v1.4.0", false},
		{"trailing slash trimmed", "https://github.com/o/r/releases/latest/download/", "https://api.github.com/repos/o/r/releases/latest", false},
		{"dotted repo name", "https://github.com/o/r.repo/releases/latest/download", "https://api.github.com/repos/o/r.repo/releases/latest", false},
		{"non-github host rejected", "https://gitlab.com/o/r/releases/latest/download", "", true},
		{"mirror host rejected", "https://mirror.local/o/r/releases/latest/download", "", true},
		{"non-http scheme rejected", "ftp://github.com/o/r/releases/latest/download", "", true},
		{"short path rejected", "https://github.com/o/r/releases", "", true},
		{"wrong middle segment rejected", "https://github.com/o/r/tags/latest/download", "", true},
		{"owner traversal rejected", "https://github.com/../r/releases/latest/download", "", true},
		{"unknown terminal rejected", "https://github.com/o/r/releases/foo/bar", "", true},
	}
	for _, c := range cases {
		got, err := deriveReleasesAPIURL(c.base)
		if c.wantErr {
			if err == nil {
				t.Errorf("%s: deriveReleasesAPIURL(%q) = %q, want error", c.name, c.base, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("%s: deriveReleasesAPIURL(%q) errored: %v", c.name, c.base, err)
			continue
		}
		if got != c.want {
			t.Errorf("%s: deriveReleasesAPIURL(%q) = %q, want %q", c.name, c.base, got, c.want)
		}
	}
}

func TestReleaseAssets_HappyPath(t *testing.T) {
	srv := newReleaseAPIServer(t, http.StatusOK, "application/json", releaseAssetsJSON)
	env := newCtlTestEnvWith(t, permissiveReleaseClient)
	// Route the derived api.github.com fetch through a loopback gh-proxy stub.
	if err := env.store.PutSettings(context.Background(), testTenant, controller.ControllerSettings{
		GithubProxy: srv.URL + "/",
	}); err != nil {
		t.Fatalf("PutSettings: %v", err)
	}

	var resp releaseAssetsResponseJSON
	status := doJSON(t, http.MethodPost, env.opURL("release-assets"), testOperatorToken, releaseAssetsRequestJSON{
		Base: "https://github.com/o/r/releases/latest/download",
	}, &resp)
	if status != http.StatusOK {
		t.Fatalf("status %d, want 200", status)
	}
	want := []string{"mimic_0.4.0_amd64.deb", "mimic_0.4.0_arm64.deb", "mimic-dkms_0.4.0_all.deb"}
	if len(resp.Assets) != len(want) {
		t.Fatalf("assets = %v, want %v (dbgsym/.ddeb/non-deb excluded, dupes removed)", resp.Assets, want)
	}
	for i, n := range want {
		if resp.Assets[i] != n {
			t.Errorf("assets[%d] = %q, want %q", i, resp.Assets[i], n)
		}
	}
	if !resp.ProxyApplied {
		t.Error("a configured gh-proxy should report proxy_applied=true")
	}
}

func TestReleaseAssets_DefaultBaseAndVersionTagPin(t *testing.T) {
	srv := newReleaseAPIServer(t, http.StatusOK, "application/json", releaseAssetsJSON)
	env := newCtlTestEnvWith(t, permissiveReleaseClient)
	// No request base → fall back to the settings MimicReleaseBase (a github latest alias); a
	// version then tag-pins it. The proxy routes the fetch to the loopback stub.
	if err := env.store.PutSettings(context.Background(), testTenant, controller.ControllerSettings{
		MimicReleaseBase: "https://github.com/o/r/releases/latest/download",
		GithubProxy:      srv.URL + "/",
	}); err != nil {
		t.Fatalf("PutSettings: %v", err)
	}

	var resp releaseAssetsResponseJSON
	status := doJSON(t, http.MethodPost, env.opURL("release-assets"), testOperatorToken, releaseAssetsRequestJSON{
		Version: "1.4.0", // bare → v-prefixed tag
	}, &resp)
	if status != http.StatusOK {
		t.Fatalf("status %d, want 200 (base-less request falls back to the settings mimic base)", status)
	}
	if !resp.VersionApplied {
		t.Error("latest base + version should report version_applied")
	}
	if want := "https://github.com/o/r/releases/download/v1.4.0"; resp.Base != want {
		t.Errorf("resp.Base = %q, want the v-tagged download base %q", resp.Base, want)
	}
}

func TestReleaseAssets_NonJSONRejected(t *testing.T) {
	// A gh-proxy that does not proxy the REST API returns HTML → reject as a fetch failure, never
	// trust it as an asset list.
	srv := newReleaseAPIServer(t, http.StatusOK, "text/html", "<html>not the api</html>")
	env := newCtlTestEnvWith(t, permissiveReleaseClient)
	if err := env.store.PutSettings(context.Background(), testTenant, controller.ControllerSettings{
		GithubProxy: srv.URL + "/",
	}); err != nil {
		t.Fatalf("PutSettings: %v", err)
	}
	status, code, _ := postReleaseAssetsErr(t, env, releaseAssetsRequestJSON{Base: "https://github.com/o/r/releases/latest/download"})
	if status != http.StatusBadGateway {
		t.Fatalf("status %d, want 502 (non-JSON body)", status)
	}
	if code != "agent_release_fetch_failed" {
		t.Errorf("code %q, want agent_release_fetch_failed", code)
	}
}

func TestReleaseAssets_UpstreamNon200(t *testing.T) {
	srv := newReleaseAPIServer(t, http.StatusInternalServerError, "application/json", `{"message":"boom"}`)
	env := newCtlTestEnvWith(t, permissiveReleaseClient)
	if err := env.store.PutSettings(context.Background(), testTenant, controller.ControllerSettings{
		GithubProxy: srv.URL + "/",
	}); err != nil {
		t.Fatalf("PutSettings: %v", err)
	}
	status, code, _ := postReleaseAssetsErr(t, env, releaseAssetsRequestJSON{Base: "https://github.com/o/r/releases/latest/download"})
	if status != http.StatusBadGateway {
		t.Fatalf("status %d, want 502 (non-200 upstream)", status)
	}
	if code != "agent_release_fetch_failed" {
		t.Errorf("code %q, want agent_release_fetch_failed", code)
	}
}

func TestReleaseAssets_SSRFRefusesLoopback(t *testing.T) {
	// The PRODUCTION (default) egress-guarded client must refuse to dial a loopback gh-proxy: the
	// SSRF + DNS-rebind defense applies to the discover fetch exactly as to the pin fetch.
	srv := newReleaseAPIServer(t, http.StatusOK, "application/json", releaseAssetsJSON)
	env := newCtlTestEnv(t) // default guarded releaseClient
	if err := env.store.PutSettings(context.Background(), testTenant, controller.ControllerSettings{
		GithubProxy: srv.URL + "/", // http://127.0.0.1:PORT/
	}); err != nil {
		t.Fatalf("PutSettings: %v", err)
	}
	status := doJSON(t, http.MethodPost, env.opURL("release-assets"), testOperatorToken, releaseAssetsRequestJSON{
		Base: "https://github.com/o/r/releases/latest/download",
	}, nil)
	if status != http.StatusBadGateway {
		t.Fatalf("status %d, want 502 (the egress guard must refuse the loopback proxy)", status)
	}
}

func TestReleaseAssets_BadInput(t *testing.T) {
	env := newCtlTestEnv(t)
	cases := []struct {
		name      string
		body      releaseAssetsRequestJSON
		wantField string
	}{
		{"non-github base", releaseAssetsRequestJSON{Base: "https://mirror.local/o/r/releases/latest/download"}, "base"},
		{"base not http(s)", releaseAssetsRequestJSON{Base: "ftp://github.com/o/r/releases/latest/download"}, "base"},
		{"malformed github path", releaseAssetsRequestJSON{Base: "https://github.com/o/r/tags/latest/download"}, "base"},
		{"non-semver version", releaseAssetsRequestJSON{Base: "https://github.com/o/r/releases/latest/download", Version: "not a version"}, "version"},
		// NOTE: "empty base" is NOT tested as a 400 here — DefaultSettings/WithDefaults fill
		// MimicReleaseBase with the upstream default, so a base-less request resolves against it
		// and proceeds to fetch (mirrors release-pins' TestReleasePins_MimicNoBaseUsesDefaultBase).
		// The base=="" guard stays as defense-in-depth but is unreachable via the default-applied
		// load path. Testing it would make a real api.github.com call; we keep this suite hermetic.
	}
	for _, c := range cases {
		status, code, field := postReleaseAssetsErr(t, env, c.body)
		if status != http.StatusBadRequest {
			t.Errorf("%s: status %d, want 400", c.name, status)
		}
		if code != "agent_release_request_invalid" {
			t.Errorf("%s: code %q, want agent_release_request_invalid", c.name, code)
		}
		if field != c.wantField {
			t.Errorf("%s: field %q, want %q", c.name, field, c.wantField)
		}
	}
}

func TestReleaseAssets_MethodAndAuth(t *testing.T) {
	env := newCtlTestEnv(t)
	if status := doJSON(t, http.MethodGet, env.opURL("release-assets"), testOperatorToken, nil, nil); status != http.StatusMethodNotAllowed {
		t.Errorf("GET status %d, want 405", status)
	}
	if status := doJSON(t, http.MethodPost, env.opURL("release-assets"), "", releaseAssetsRequestJSON{Base: "https://github.com/o/r/releases/latest/download"}, nil); status != http.StatusUnauthorized {
		t.Errorf("unauthenticated status %d, want 401", status)
	}
}

// postReleaseAssetsErr POSTs a release-assets request and decodes the coded-error envelope,
// returning the status, apierr code, and "field" param so a bad-input test pins each guard.
func postReleaseAssetsErr(t *testing.T, env *ctlTestEnv, body releaseAssetsRequestJSON) (int, string, string) {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	req, _ := http.NewRequest(http.MethodPost, env.opURL("release-assets"), bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+testOperatorToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()
	var env2 struct {
		Error struct {
			Code   string            `json:"code"`
			Params map[string]string `json:"params"`
		} `json:"error"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&env2)
	return resp.StatusCode, env2.Error.Code, env2.Error.Params["field"]
}
