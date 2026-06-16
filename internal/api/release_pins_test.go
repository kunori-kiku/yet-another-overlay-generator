package api

import (
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// testSidecarHash is a valid 64-hex SHA-256 (sha256 of the empty string) used as sidecar content.
const testSidecarHash = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"

// permissiveReleaseClient swaps in a plain client (no egress guard) so an integration test can
// reach a loopback httptest server. The PRODUCTION client (newReleasePinClient) refuses loopback
// by design; TestReleasePins_SSRFRefusesLoopback asserts exactly that with the default client.
func permissiveReleaseClient(ch *ControllerHandler) {
	ch.releaseClient = &http.Client{Timeout: 5 * time.Second}
}

// newSidecarServer serves the given body for any path ending in ".sha256" (404 otherwise).
func newSidecarServer(t *testing.T, body string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, ".sha256") {
			http.NotFound(w, r)
			return
		}
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// --- PERPETUAL SSRF boundary (never retire): the egress guard is the only DNS-rebind defense ---

func TestIsPublicUnicastIP(t *testing.T) {
	cases := []struct {
		ip     string
		public bool
	}{
		{"8.8.8.8", true},
		{"1.1.1.1", true},
		{"2606:4700:4700::1111", true}, // public IPv6
		{"127.0.0.1", false},           // loopback
		{"::1", false},                 // loopback IPv6
		{"169.254.169.254", false},     // link-local (cloud metadata)
		{"10.0.0.1", false},            // RFC1918
		{"172.16.5.4", false},          // RFC1918
		{"192.168.1.1", false},         // RFC1918
		{"100.64.0.1", false},          // RFC6598 CGNAT
		{"fc00::1", false},             // ULA
		{"fe80::1", false},             // link-local IPv6
		{"0.0.0.0", false},             // unspecified
		{"224.0.0.1", false},           // multicast
	}
	for _, c := range cases {
		ip := net.ParseIP(c.ip)
		if ip == nil {
			t.Fatalf("bad test ip %q", c.ip)
		}
		if got := isPublicUnicastIP(ip); got != c.public {
			t.Errorf("isPublicUnicastIP(%s) = %v, want %v", c.ip, got, c.public)
		}
	}
}

func TestBlockPrivateAddr(t *testing.T) {
	// Refused: non-public resolved addresses (this is what defeats DNS-rebind — the guard runs
	// on the RESOLVED ip:port) and a still-a-hostname address (DNS not resolved → fail closed).
	blocked := []string{
		"127.0.0.1:443", "169.254.169.254:80", "10.1.2.3:80", "192.168.0.5:8080",
		"100.64.0.1:443", "[fc00::1]:443", "[::1]:80", "[fe80::1]:443",
		"example.com:443", // unresolved hostname → not an IP → refused
	}
	for _, a := range blocked {
		if err := blockPrivateAddr("tcp", a, nil); err == nil {
			t.Errorf("blockPrivateAddr(%q) = nil, want refusal", a)
		}
	}
	allowed := []string{"8.8.8.8:443", "[2606:4700:4700::1111]:443"}
	for _, a := range allowed {
		if err := blockPrivateAddr("tcp", a, nil); err != nil {
			t.Errorf("blockPrivateAddr(%q) = %v, want allow", a, err)
		}
	}
}

func TestResolveReleaseBase(t *testing.T) {
	const latest = "https://github.com/o/r/releases/latest/download"
	cases := []struct {
		name        string
		base        string
		version     string
		wantBase    string
		wantApplied bool
	}{
		{"v-prefixed version", latest, "v2.0.0-beta.3", "https://github.com/o/r/releases/download/v2.0.0-beta.3", true},
		{"bare version gets v", latest, "2.0.0-beta.3", "https://github.com/o/r/releases/download/v2.0.0-beta.3", true},
		{"no version keeps latest", latest, "", latest, false},
		{"trailing slash trimmed", latest + "/", "v1.2.3", "https://github.com/o/r/releases/download/v1.2.3", true},
		{"custom base ignores version", "https://mirror.local/agent", "v1.0.0", "https://mirror.local/agent", false},
	}
	for _, c := range cases {
		gotBase, gotApplied := resolveReleaseBase(c.base, c.version)
		if gotBase != c.wantBase || gotApplied != c.wantApplied {
			t.Errorf("%s: resolveReleaseBase(%q,%q) = (%q,%v), want (%q,%v)",
				c.name, c.base, c.version, gotBase, gotApplied, c.wantBase, c.wantApplied)
		}
	}
}

// --- endpoint integration ---

func TestReleasePins_AgentHappyPath(t *testing.T) {
	srv := newSidecarServer(t, testSidecarHash+"\n")
	env := newCtlTestEnvWith(t, permissiveReleaseClient)

	var resp releasePinResponseJSON
	status := doJSON(t, http.MethodPost, env.opURL("release-pins"), testOperatorToken, releasePinRequestJSON{
		Kind:   "agent",
		Base:   srv.URL,
		Assets: []releasePinAssetJSON{{Key: "linux-amd64", Asset: "yaog-agent-linux-amd64"}},
	}, &resp)
	if status != http.StatusOK {
		t.Fatalf("status %d, want 200", status)
	}
	pin, ok := resp.Pins["linux-amd64"]
	if !ok {
		t.Fatalf("no pin for linux-amd64: %+v", resp.Pins)
	}
	if pin.Asset != "yaog-agent-linux-amd64" || pin.SHA256 != testSidecarHash {
		t.Fatalf("pin = %+v, want asset=yaog-agent-linux-amd64 sha256=%s", pin, testSidecarHash)
	}
	if resp.VersionApplied {
		t.Error("a custom base must not report version_applied")
	}
	if resp.ProxyApplied {
		t.Error("no gh-proxy configured, proxy_applied should be false")
	}
}

func TestReleasePins_AgentDerivesCertifiedArches(t *testing.T) {
	srv := newSidecarServer(t, testSidecarHash+"\n")
	env := newCtlTestEnvWith(t, permissiveReleaseClient)

	var resp releasePinResponseJSON
	status := doJSON(t, http.MethodPost, env.opURL("release-pins"), testOperatorToken, releasePinRequestJSON{
		Kind: "agent",
		Base: srv.URL, // no assets → derive linux-amd64 + linux-arm64
	}, &resp)
	if status != http.StatusOK {
		t.Fatalf("status %d, want 200", status)
	}
	for _, key := range []string{"linux-amd64", "linux-arm64"} {
		pin, ok := resp.Pins[key]
		if !ok {
			t.Errorf("derived arches missing %s: %+v", key, resp.Pins)
			continue
		}
		if pin.Asset != "yaog-agent-"+key {
			t.Errorf("%s asset = %q, want yaog-agent-%s", key, pin.Asset, key)
		}
	}
}

func TestReleasePins_VersionAppliedRewritesTag(t *testing.T) {
	srv := newSidecarServer(t, testSidecarHash+"\n")
	env := newCtlTestEnvWith(t, permissiveReleaseClient)

	var resp releasePinResponseJSON
	base := srv.URL + "/releases/latest/download"
	status := doJSON(t, http.MethodPost, env.opURL("release-pins"), testOperatorToken, releasePinRequestJSON{
		Kind:    "agent",
		Version: "2.0.0-beta.3", // bare → v-prefixed tag
		Base:    base,
		Assets:  []releasePinAssetJSON{{Key: "linux-amd64", Asset: "yaog-agent-linux-amd64"}},
	}, &resp)
	if status != http.StatusOK {
		t.Fatalf("status %d, want 200", status)
	}
	if !resp.VersionApplied {
		t.Error("latest base + version should report version_applied")
	}
	wantBase := srv.URL + "/releases/download/v2.0.0-beta.3"
	if resp.Base != wantBase {
		t.Errorf("resp.Base = %q, want %q", resp.Base, wantBase)
	}
	if got := resp.Resolved["linux-amd64"]; !strings.HasSuffix(got, "/releases/download/v2.0.0-beta.3/yaog-agent-linux-amd64.sha256") {
		t.Errorf("resolved url = %q, want the v-tagged sidecar path", got)
	}
}

func TestReleasePins_SSRFRefusesLoopback(t *testing.T) {
	// The PRODUCTION (default) client must refuse to dial the loopback test server: SSRF + the
	// DNS-rebind defense. A refused dial surfaces as the 502 upstream-fetch-failed code.
	srv := newSidecarServer(t, testSidecarHash+"\n")
	env := newCtlTestEnv(t) // default guarded releaseClient

	status := doJSON(t, http.MethodPost, env.opURL("release-pins"), testOperatorToken, releasePinRequestJSON{
		Kind:   "agent",
		Base:   srv.URL, // http://127.0.0.1:PORT
		Assets: []releasePinAssetJSON{{Key: "linux-amd64", Asset: "yaog-agent-linux-amd64"}},
	}, nil)
	if status != http.StatusBadGateway {
		t.Fatalf("status %d, want 502 (the egress guard must refuse loopback)", status)
	}
}

func TestReleasePins_SidecarInvalid(t *testing.T) {
	srv := newSidecarServer(t, "<html>404 not found</html>\n") // not a hex digest
	env := newCtlTestEnvWith(t, permissiveReleaseClient)

	status := doJSON(t, http.MethodPost, env.opURL("release-pins"), testOperatorToken, releasePinRequestJSON{
		Kind:   "agent",
		Base:   srv.URL,
		Assets: []releasePinAssetJSON{{Key: "linux-amd64", Asset: "yaog-agent-linux-amd64"}},
	}, nil)
	if status != http.StatusBadGateway {
		t.Fatalf("status %d, want 502 (non-SHA-256 sidecar)", status)
	}
}

func TestReleasePins_ResponseCapTruncatesBeforeHash(t *testing.T) {
	// The hash sits AFTER 600 bytes of whitespace, past the 512-byte cap: the LimitReader read
	// yields only whitespace → no valid token → rejected. Proves the response cap is enforced.
	body := strings.Repeat(" ", 600) + testSidecarHash + "\n"
	srv := newSidecarServer(t, body)
	env := newCtlTestEnvWith(t, permissiveReleaseClient)

	status := doJSON(t, http.MethodPost, env.opURL("release-pins"), testOperatorToken, releasePinRequestJSON{
		Kind:   "agent",
		Base:   srv.URL,
		Assets: []releasePinAssetJSON{{Key: "linux-amd64", Asset: "yaog-agent-linux-amd64"}},
	}, nil)
	if status != http.StatusBadGateway {
		t.Fatalf("status %d, want 502 (cap truncates the read before the hash)", status)
	}
}

func TestReleasePins_BadInput(t *testing.T) {
	env := newCtlTestEnv(t)
	cases := []struct {
		name string
		body releasePinRequestJSON
	}{
		{"unknown kind", releasePinRequestJSON{Kind: "weird", Base: "https://github.com/x", Assets: []releasePinAssetJSON{{Key: "linux-amd64", Asset: "a"}}}},
		{"non-semver version", releasePinRequestJSON{Kind: "agent", Version: "not a version", Base: "https://github.com/x", Assets: []releasePinAssetJSON{{Key: "linux-amd64", Asset: "yaog-agent-linux-amd64"}}}},
		{"mimic without base", releasePinRequestJSON{Kind: "mimic", Assets: []releasePinAssetJSON{{Key: "bookworm-amd64", Asset: "mimic.deb"}}}},
		{"bad agent asset", releasePinRequestJSON{Kind: "agent", Base: "https://github.com/x", Assets: []releasePinAssetJSON{{Key: "linux-amd64", Asset: "bad/asset"}}}},
		{"bad agent key", releasePinRequestJSON{Kind: "agent", Base: "https://github.com/x", Assets: []releasePinAssetJSON{{Key: "win-amd64", Asset: "yaog-agent"}}}},
		{"mimic empty assets", releasePinRequestJSON{Kind: "mimic", Base: "https://github.com/x"}},
		{"base not http(s)", releasePinRequestJSON{Kind: "agent", Base: "ftp://github.com/x", Assets: []releasePinAssetJSON{{Key: "linux-amd64", Asset: "yaog-agent-linux-amd64"}}}},
	}
	for _, c := range cases {
		status := doJSON(t, http.MethodPost, env.opURL("release-pins"), testOperatorToken, c.body, nil)
		if status != http.StatusBadRequest {
			t.Errorf("%s: status %d, want 400", c.name, status)
		}
	}
}

func TestReleasePins_MethodAndAuth(t *testing.T) {
	env := newCtlTestEnv(t)
	if status := doJSON(t, http.MethodGet, env.opURL("release-pins"), testOperatorToken, nil, nil); status != http.StatusMethodNotAllowed {
		t.Errorf("GET status %d, want 405", status)
	}
	if status := doJSON(t, http.MethodPost, env.opURL("release-pins"), "", releasePinRequestJSON{Kind: "agent"}, nil); status != http.StatusUnauthorized {
		t.Errorf("unauthenticated status %d, want 401", status)
	}
}
