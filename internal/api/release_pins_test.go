package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/kunorikiku/yet-another-overlay-generator/internal/controller"
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
		{"2606:4700:4700::1111", true},    // public IPv6
		{"127.0.0.1", false},              // loopback
		{"::1", false},                    // loopback IPv6
		{"169.254.169.254", false},        // link-local (cloud metadata)
		{"10.0.0.1", false},               // RFC1918
		{"172.16.5.4", false},             // RFC1918
		{"192.168.1.1", false},            // RFC1918
		{"100.64.0.1", false},             // RFC6598 CGNAT
		{"fc00::1", false},                // ULA
		{"fe80::1", false},                // link-local IPv6
		{"0.0.0.0", false},                // unspecified
		{"224.0.0.1", false},              // multicast
		{"::ffff:127.0.0.1", false},       // IPv4-mapped loopback
		{"::ffff:169.254.169.254", false}, // IPv4-mapped link-local (metadata)
		{"2002:7f00:1::", false},          // 6to4 of 127.0.0.1
		{"2002:a9fe:a9fe::", false},       // 6to4 of 169.254.169.254 (metadata)
		{"64:ff9b::7f00:1", false},        // NAT64 of 127.0.0.1
		{"64:ff9b::a9fe:a9fe", false},     // NAT64 of 169.254.169.254 (metadata)
		{"64:ff9b::808:808", true},        // NAT64 of public 8.8.8.8 → still public
		// S7: IPv4-compatible IPv6 (::a.b.c.d) — zero high-96, non-trivial low-32. Go's To4()
		// does NOT unwrap this deprecated form (unlike ::ffff:a.b.c.d), so without an explicit
		// embedded-v4 decode a 6to4/NAT64-style bypass exists for the OLDEST mapping too.
		{"::127.0.0.1", false},       // ::a.b.c.d of loopback (parses to ::7f00:1)
		{"::169.254.169.254", false}, // ::a.b.c.d of cloud-metadata link-local
		{"::8.8.8.8", true},          // ::a.b.c.d of public 8.8.8.8 → still public (no over-block)
		// S11: OCI instance-metadata service lives at 192.0.0.192 inside the IETF
		// special-purpose 192.0.0.0/24 block (RFC 6890) — not RFC1918, not CGNAT, so the
		// pre-fix To4 branch let it through. Deny the whole /24 (IETF protocol assignments,
		// never a public unicast destination).
		{"192.0.0.192", false}, // OCI metadata endpoint
		{"192.0.0.1", false},   // 192.0.0.0/24 lower bound
		{"192.0.0.255", false}, // 192.0.0.0/24 upper bound
		{"192.0.1.1", true},    // just outside the /24 → public again
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
		"[2002:7f00:1::]:443",      // 6to4 of loopback
		"[64:ff9b::a9fe:a9fe]:443", // NAT64 of the cloud-metadata IP
		"example.com:443",          // unresolved hostname → not an IP → refused
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

func TestReleasePins_MimicHappyPath(t *testing.T) {
	srv := newSidecarServer(t, testSidecarHash+"\n")
	env := newCtlTestEnvWith(t, permissiveReleaseClient)

	var resp releasePinResponseJSON
	status := doJSON(t, http.MethodPost, env.opURL("release-pins"), testOperatorToken, releasePinRequestJSON{
		Kind:   "mimic",
		Base:   srv.URL,
		Assets: []releasePinAssetJSON{{Key: "bookworm-amd64", Asset: "yaog-mimic_1.0_amd64.deb"}},
	}, &resp)
	if status != http.StatusOK {
		t.Fatalf("status %d, want 200", status)
	}
	// Exercises the mimic grammar (debKeyPattern + debAssetPattern) + the mimic base wiring,
	// distinct from the agent branch — a regression mis-wiring the kind switch would surface here.
	pin, ok := resp.Pins["bookworm-amd64"]
	if !ok {
		t.Fatalf("no pin for bookworm-amd64: %+v", resp.Pins)
	}
	if pin.Asset != "yaog-mimic_1.0_amd64.deb" || pin.SHA256 != testSidecarHash {
		t.Fatalf("pin = %+v", pin)
	}
	if got := resp.Resolved["bookworm-amd64"]; !strings.HasSuffix(got, "/yaog-mimic_1.0_amd64.deb.sha256") {
		t.Errorf("resolved url = %q, want the .deb.sha256 sidecar path", got)
	}
}

func TestReleasePins_ProxyApplied(t *testing.T) {
	// The whole reason this endpoint is server-side is to apply the gh-proxy. Prove it: with a
	// configured proxy, the fetched URL is proxy-prefixed and proxy_applied is true.
	srv := newSidecarServer(t, testSidecarHash+"\n")
	env := newCtlTestEnvWith(t, permissiveReleaseClient)
	// Persist a proxy pointing at the test server; the github base then rides as the proxy path.
	if err := env.store.PutSettings(context.Background(), testTenant, controller.ControllerSettings{
		GithubProxy: srv.URL + "/",
	}); err != nil {
		t.Fatalf("PutSettings: %v", err)
	}

	var resp releasePinResponseJSON
	status := doJSON(t, http.MethodPost, env.opURL("release-pins"), testOperatorToken, releasePinRequestJSON{
		Kind:   "agent",
		Base:   "https://github.com/o/r/releases/download/v1",
		Assets: []releasePinAssetJSON{{Key: "linux-amd64", Asset: "yaog-agent-linux-amd64"}},
	}, &resp)
	if status != http.StatusOK {
		t.Fatalf("status %d, want 200", status)
	}
	if !resp.ProxyApplied {
		t.Error("a configured gh-proxy should report proxy_applied=true")
	}
	if got := resp.Resolved["linux-amd64"]; !strings.HasPrefix(got, srv.URL+"/") {
		t.Errorf("resolved url = %q, want the gh-proxy prefix %q", got, srv.URL+"/")
	}
	if resp.Pins["linux-amd64"].SHA256 != testSidecarHash {
		t.Errorf("pin sha256 = %q, want %q", resp.Pins["linux-amd64"].SHA256, testSidecarHash)
	}
}

func TestReleasePins_UpstreamNon200(t *testing.T) {
	// The status-code 502 sub-branch (distinct from a refused/transport dial): upstream answers
	// the .sha256 path with a non-200.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "nope", http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)
	env := newCtlTestEnvWith(t, permissiveReleaseClient)

	// Assert the CODE, not just 502: both 502 sub-branches (this status branch and the
	// sidecar-invalid hex branch) map to 502, so a status-only check would still pass if the
	// status guard were removed (the body would then fail the hex check → sidecar-invalid).
	status, code, _ := postReleasePinErr(t, env, releasePinRequestJSON{
		Kind:   "agent",
		Base:   srv.URL,
		Assets: []releasePinAssetJSON{{Key: "linux-amd64", Asset: "yaog-agent-linux-amd64"}},
	})
	if status != http.StatusBadGateway {
		t.Fatalf("status %d, want 502 (non-200 upstream)", status)
	}
	if code != "agent_release_fetch_failed" {
		t.Errorf("code %q, want agent_release_fetch_failed (the status sub-branch, distinct from sidecar-invalid)", code)
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

// TestFetchSidecar_ErrorBodyHidesResolvedIP is the S8 DNS-oracle regression: a dial refused by the
// egress guard (or any transport failure) must NOT leak the resolved internal IP into the
// client-facing error. The guard's refusal string embeds the IP ("...non-public address
// 169.254.169.254"); if that string reaches .With("detail", err.Error()) it serializes into the
// response (params["detail"] AND the interpolated Message), turning a 502 into a DNS-rebind oracle
// that confirms which internal IP a hostname resolves to. The inner err stays in the server log
// (the wrapped cause), never on the wire.
func TestFetchSidecar_ErrorBodyHidesResolvedIP(t *testing.T) {
	ch := NewControllerHandler(controller.NewMemStore(), testTenant, controller.HashToken(testOperatorToken), DefaultOperatorName)
	ch.releaseClient = newReleasePinClient() // PRODUCTION egress-guarded client

	// A hostname (NOT a literal IP) so the resolved IP is a NEW fact the response would disclose:
	// the operator typed the hostname, the guard learns the IP, and "detail" must not echo it back.
	// Resolving to a fixed internal IP is the DNS-rebind oracle case; we assert the leak path is
	// closed regardless of which internal address resolution produced.
	_, aerr := ch.fetchSidecar(context.Background(), "http://localhost/yaog-agent-linux-amd64.sha256")
	if aerr == nil {
		t.Fatal("fetchSidecar to a loopback hostname should fail (egress guard)")
	}

	// The leak we forbid: the resolved internal IP reaching the serialized "detail" param. The
	// production guard's refusal string is "...non-public address <IP>"; collapsing it to a fixed
	// generic detail keeps that IP out of params[detail] (and out of the {detail} interpolation in
	// Message). The "url" param echoes only the operator-supplied URL (a loopback HOSTNAME here, no
	// IP), so it is not an oracle.
	detail := aerr.Params()["detail"]
	for _, leak := range []string{"127.0.0.1", "::1", "address", "refusing", "non-public"} {
		if strings.Contains(detail, leak) {
			t.Errorf("params[detail] = %q leaks the dial/guard internals (%q); must be a fixed generic detail", detail, leak)
		}
	}
	if strings.Contains(aerr.Message(), "127.0.0.1") {
		t.Errorf("Message leaks the resolved loopback IP: %q", aerr.Message())
	}
	// The inner cause (server-log only) is preserved for the log but never serialized:
	// writeAPIError serializes Code/Message/Params, NOT Unwrap(). It MAY carry the IP.
	if cause := aerr.Unwrap(); cause != nil {
		t.Logf("note: wrapped cause = %q (kept for server log, never serialized)", cause.Error())
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
		name      string
		body      releasePinRequestJSON
		wantField string // each row violates exactly one guard; pin it to its own field
	}{
		{"unknown kind", releasePinRequestJSON{Kind: "weird", Base: "https://github.com/x", Assets: []releasePinAssetJSON{{Key: "linux-amd64", Asset: "a"}}}, "kind"},
		{"non-semver version", releasePinRequestJSON{Kind: "agent", Version: "not a version", Base: "https://github.com/x", Assets: []releasePinAssetJSON{{Key: "linux-amd64", Asset: "yaog-agent-linux-amd64"}}}, "version"},
		{"mimic without base", releasePinRequestJSON{Kind: "mimic", Assets: []releasePinAssetJSON{{Key: "bookworm-amd64", Asset: "mimic.deb"}}}, "base"},
		{"bad agent asset", releasePinRequestJSON{Kind: "agent", Base: "https://github.com/x", Assets: []releasePinAssetJSON{{Key: "linux-amd64", Asset: "bad/asset"}}}, "assets.asset"},
		{"bad agent key", releasePinRequestJSON{Kind: "agent", Base: "https://github.com/x", Assets: []releasePinAssetJSON{{Key: "win-amd64", Asset: "yaog-agent"}}}, "assets.key"},
		{"mimic empty assets", releasePinRequestJSON{Kind: "mimic", Base: "https://github.com/x"}, "assets"},
		{"base not http(s)", releasePinRequestJSON{Kind: "agent", Base: "ftp://github.com/x", Assets: []releasePinAssetJSON{{Key: "linux-amd64", Asset: "yaog-agent-linux-amd64"}}}, "base"},
	}
	for _, c := range cases {
		status, code, field := postReleasePinErr(t, env, c.body)
		if status != http.StatusBadRequest {
			t.Errorf("%s: status %d, want 400", c.name, status)
		}
		if code != "agent_release_request_invalid" {
			t.Errorf("%s: code %q, want agent_release_request_invalid", c.name, code)
		}
		if field != c.wantField {
			t.Errorf("%s: field %q, want %q (a reordered guard would fire the wrong one)", c.name, field, c.wantField)
		}
	}
}

// postReleasePinErr POSTs a release-pin request and decodes the coded-error envelope
// ({"error":{"code","params":{...}}}), returning the status, the apierr code, and the "field"
// param — so a bad-input test pins each guard to its own field rather than only checking 400.
func postReleasePinErr(t *testing.T, env *ctlTestEnv, body releasePinRequestJSON) (int, string, string) {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	req, _ := http.NewRequest(http.MethodPost, env.opURL("release-pins"), bytes.NewReader(raw))
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

func TestReleasePins_MethodAndAuth(t *testing.T) {
	env := newCtlTestEnv(t)
	if status := doJSON(t, http.MethodGet, env.opURL("release-pins"), testOperatorToken, nil, nil); status != http.StatusMethodNotAllowed {
		t.Errorf("GET status %d, want 405", status)
	}
	if status := doJSON(t, http.MethodPost, env.opURL("release-pins"), "", releasePinRequestJSON{Kind: "agent"}, nil); status != http.StatusUnauthorized {
		t.Errorf("unauthenticated status %d, want 401", status)
	}
}
