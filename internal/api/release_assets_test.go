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
// amd64 package (deduped). The names follow the real upstream hack3ric/mimic convention.
const releaseAssetsJSON = `{"assets":[
  {"name":"bookworm_mimic_0.7.1-1_amd64.deb"},
  {"name":"bookworm_mimic_0.7.1-1_arm64.deb"},
  {"name":"bookworm_mimic-dkms_0.7.1-1_all.deb"},
  {"name":"bookworm_mimic-dbgsym_0.7.1-1_amd64.deb"},
  {"name":"bookworm_mimic_0.7.1-1_amd64.ddeb"},
  {"name":"checksums.txt"},
  {"name":"bookworm_mimic_0.7.1-1_amd64.deb"}
]}`

// newReleaseAPIServer serves body (with the given content type) for any path — so a loopback API
// stub can stand in for api.github.com regardless of the derived path.
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

// discoverEnv builds a controller test env whose asset-discovery fetch is pointed at apiBase (a
// loopback stub). guarded=false swaps in the permissive client so the loopback stub is reachable;
// guarded=true keeps the production egress guard (so the SSRF test sees it refuse loopback).
func discoverEnv(t *testing.T, apiBase string, guarded bool) *ctlTestEnv {
	t.Helper()
	return newCtlTestEnvWith(t, func(ch *ControllerHandler) {
		ch.githubAPIBase = apiBase
		if !guarded {
			permissiveReleaseClient(ch)
		}
	})
}

func TestDeriveReleaseRefs(t *testing.T) {
	const latestAPI = "/repos/o/r/releases/latest"
	const latestDL = "https://github.com/o/r/releases/latest/download"
	const tagAPI = "/repos/o/r/releases/tags/v1.4.0"
	const tagDL = "https://github.com/o/r/releases/download/v1.4.0"
	cases := []struct {
		name            string
		base            string
		wantAPI, wantDL string
		wantErr         bool
	}{
		{"repo root", "https://github.com/o/r", latestAPI, latestDL, false},
		{"releases", "https://github.com/o/r/releases", latestAPI, latestDL, false},
		{"releases/latest", "https://github.com/o/r/releases/latest", latestAPI, latestDL, false},
		{"releases/latest/download", "https://github.com/o/r/releases/latest/download", latestAPI, latestDL, false},
		{"trailing slash", "https://github.com/o/r/releases/latest/download/", latestAPI, latestDL, false},
		{"download/<tag>", "https://github.com/o/r/releases/download/v1.4.0", tagAPI, tagDL, false},
		{"tag/<tag>", "https://github.com/o/r/releases/tag/v1.4.0", tagAPI, tagDL, false},
		{"tags/<tag>", "https://github.com/o/r/releases/tags/v1.4.0", tagAPI, tagDL, false},
		{"dotted repo", "https://github.com/o/r.repo/releases/latest", "/repos/o/r.repo/releases/latest", "https://github.com/o/r.repo/releases/latest/download", false},
		{"non-github host", "https://gitlab.com/o/r/releases/latest/download", "", "", true},
		{"mirror host", "https://mirror.local/o/r/releases/latest/download", "", "", true},
		{"non-http scheme", "ftp://github.com/o/r/releases/latest/download", "", "", true},
		{"owner only", "https://github.com/o", "", "", true},
		{"owner traversal", "https://github.com/../r/releases/latest/download", "", "", true},
		{"garbage path", "https://github.com/o/r/foo/bar/baz", "", "", true},
		{"tag traversal", "https://github.com/o/r/releases/download/..", "", "", true},
	}
	for _, c := range cases {
		api, dl, err := deriveReleaseRefs(c.base)
		if c.wantErr {
			if err == nil {
				t.Errorf("%s: deriveReleaseRefs(%q) = (%q,%q), want error", c.name, c.base, api, dl)
			}
			continue
		}
		if err != nil {
			t.Errorf("%s: deriveReleaseRefs(%q) errored: %v", c.name, c.base, err)
			continue
		}
		if api != c.wantAPI || dl != c.wantDL {
			t.Errorf("%s: deriveReleaseRefs(%q) = (%q,%q), want (%q,%q)", c.name, c.base, api, dl, c.wantAPI, c.wantDL)
		}
	}
}

func TestReleaseAssets_HappyPath(t *testing.T) {
	srv := newReleaseAPIServer(t, http.StatusOK, "application/json", releaseAssetsJSON)
	env := discoverEnv(t, srv.URL, false)

	var resp releaseAssetsResponseJSON
	status := doJSON(t, http.MethodPost, env.opURL("release-assets"), testOperatorToken, releaseAssetsRequestJSON{
		// A loosely-typed base (no /download) must still discover AND be normalized in the response.
		Base: "https://github.com/o/r/releases/latest",
	}, &resp)
	if status != http.StatusOK {
		t.Fatalf("status %d, want 200", status)
	}
	want := []string{"bookworm_mimic_0.7.1-1_amd64.deb", "bookworm_mimic_0.7.1-1_arm64.deb", "bookworm_mimic-dkms_0.7.1-1_all.deb"}
	if len(resp.Assets) != len(want) {
		t.Fatalf("assets = %v, want %v (dbgsym/.ddeb/non-deb excluded, dupes removed)", resp.Assets, want)
	}
	for i, n := range want {
		if resp.Assets[i] != n {
			t.Errorf("assets[%d] = %q, want %q", i, resp.Assets[i], n)
		}
	}
	// The response normalizes the loosely-typed base to the canonical download form for the panel.
	if resp.Base != "https://github.com/o/r/releases/latest/download" {
		t.Errorf("resp.Base = %q, want the canonical download base", resp.Base)
	}
}

func TestReleaseAssets_DefaultBase(t *testing.T) {
	srv := newReleaseAPIServer(t, http.StatusOK, "application/json", releaseAssetsJSON)
	env := discoverEnv(t, srv.URL, false)
	// No request base → fall back to the settings MimicReleaseBase.
	if err := env.store.PutSettings(context.Background(), testTenant, controller.ControllerSettings{
		MimicReleaseBase: "https://github.com/o/r/releases/latest/download",
	}); err != nil {
		t.Fatalf("PutSettings: %v", err)
	}
	var resp releaseAssetsResponseJSON
	status := doJSON(t, http.MethodPost, env.opURL("release-assets"), testOperatorToken, releaseAssetsRequestJSON{}, &resp)
	if status != http.StatusOK {
		t.Fatalf("status %d, want 200 (base-less request falls back to the settings mimic base)", status)
	}
	if len(resp.Assets) == 0 {
		t.Fatalf("expected discovered assets from the default base")
	}
}

func TestReleaseAssets_NonJSONRejected(t *testing.T) {
	// An intercepting middlebox returns HTML → reject as a fetch failure, never trust it.
	srv := newReleaseAPIServer(t, http.StatusOK, "text/html", "<html>not the api</html>")
	env := discoverEnv(t, srv.URL, false)
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
	env := discoverEnv(t, srv.URL, false)
	status, code, _ := postReleaseAssetsErr(t, env, releaseAssetsRequestJSON{Base: "https://github.com/o/r/releases/latest/download"})
	if status != http.StatusBadGateway {
		t.Fatalf("status %d, want 502 (non-200 upstream)", status)
	}
	if code != "agent_release_fetch_failed" {
		t.Errorf("code %q, want agent_release_fetch_failed", code)
	}
}

func TestReleaseAssets_SSRFRefusesLoopback(t *testing.T) {
	// The PRODUCTION (default) egress-guarded client must refuse to dial a loopback API base: the
	// SSRF + DNS-rebind defense applies to the direct discovery fetch.
	srv := newReleaseAPIServer(t, http.StatusOK, "application/json", releaseAssetsJSON)
	env := discoverEnv(t, srv.URL, true) // guarded client, API base = loopback stub
	status := doJSON(t, http.MethodPost, env.opURL("release-assets"), testOperatorToken, releaseAssetsRequestJSON{
		Base: "https://github.com/o/r/releases/latest/download",
	}, nil)
	if status != http.StatusBadGateway {
		t.Fatalf("status %d, want 502 (the egress guard must refuse the loopback API base)", status)
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
		{"garbage github path", releaseAssetsRequestJSON{Base: "https://github.com/o/r/foo/bar/baz"}, "base"},
		{"owner only", releaseAssetsRequestJSON{Base: "https://github.com/o"}, "base"},
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
