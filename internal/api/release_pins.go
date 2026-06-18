package api

// release_pins.go is the operator-only assisted release-pin fetch (controller-panel-rollout-ui
// plan-1). The panel cannot fetch raw GitHub release .sha256 sidecars itself (they send no CORS
// headers, and the gh-proxy must be applied server-side), so the server fetches them here and
// returns renderer.Artifact-shaped pins the operator REVIEWS before saving.
//
// CUSTODY (PRINCIPLES.md "Signed-artifact self-update custody"): the fetched sidecar is a
// CONVENIENCE for filling a pin — it rides the SAME untrusted transport (github.com / the
// gh-proxy) as the binary itself and is NOT a trust anchor. Trust comes only from the controller
// signing artifacts.json and the agent verifying the downloaded bytes against the signed pin
// before exec. This endpoint never persists or auto-trusts anything; it only echoes hashes back
// for the operator to inspect and (separately) save through the validated /settings path.

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/kunorikiku/yet-another-overlay-generator/internal/apierr"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/model"
)

const (
	// releaseFetchTimeout bounds the whole sidecar GET (a .sha256 is one short line).
	releaseFetchTimeout = 10 * time.Second
	// releaseDialTimeout bounds the TCP/TLS dial (the egress guard runs inside it).
	releaseDialTimeout = 5 * time.Second
	// releaseSidecarCap caps the response body read: a sidecar is one hex line
	// ("<64-hex>\n", or at most "<64-hex>  <filename>\n"), so 512 bytes is generous and
	// makes a wrong (HTML error page, huge file) response cheap to reject.
	releaseSidecarCap = 512
	// releaseMaxRedirects caps redirect hops (each still re-dials through the egress guard).
	releaseMaxRedirects = 5
	// releaseLatestSuffix is the "newest release" alias a version request rewrites to a tag.
	releaseLatestSuffix = "releases/latest/download"
)

// blockPrivateAddr is the net.Dialer.Control hook that refuses to connect to a non-public IP.
// It runs AFTER DNS resolution for EACH candidate address, so it also defeats DNS-rebinding (a
// hostname that resolves to 127.0.0.1 / 169.254.169.254 / an RFC1918 or ULA address) — the
// SSRF protection a URL format check (validateAbsoluteHTTPURL) fundamentally cannot give.
func blockPrivateAddr(_ /*network*/, address string, _ syscall.RawConn) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return fmt.Errorf("release-pin dial: unparseable address %q: %w", address, err)
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return fmt.Errorf("release-pin dial: non-IP dial address %q", host)
	}
	if !isPublicUnicastIP(ip) {
		return fmt.Errorf("release-pin dial: refusing to connect to non-public address %s", ip)
	}
	return nil
}

// isPublicUnicastIP reports whether ip is a routable public unicast address — i.e. NOT loopback,
// link-local, multicast, unspecified, RFC1918/ULA private, or RFC6598 CGNAT, and not a
// special-purpose IPv6 prefix that embeds such an IPv4. This is the egress allow predicate for
// the assisted release fetch.
func isPublicUnicastIP(ip net.IP) bool {
	if ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() ||
		ip.IsMulticast() || ip.IsInterfaceLocalMulticast() || ip.IsUnspecified() {
		return false
	}
	if ip.IsPrivate() { // 10/8, 172.16/12, 192.168/16 and fc00::/7 (ULA)
		return false
	}
	if ip4 := ip.To4(); ip4 != nil {
		// 100.64.0.0/10 (RFC6598 CGNAT) is not covered by IsPrivate but is not public either.
		if ip4[0] == 100 && ip4[1]&0xc0 == 64 {
			return false
		}
		// 192.0.0.0/24 (RFC 6890 IETF protocol assignments) is not RFC1918/CGNAT but is NOT a
		// public unicast destination — and it contains the OCI instance-metadata service at
		// 192.0.0.192 (S11). Deny the whole /24 so the metadata endpoint cannot be reached.
		if ip4[0] == 192 && ip4[1] == 0 && ip4[2] == 0 {
			return false
		}
		return true
	}
	// A non-v4-mapped IPv6 (To4 already unwraps ::ffff:a.b.c.d) may still EMBED an internal
	// IPv4 inside a special-purpose prefix (6to4 2002::/16, NAT64 64:ff9b::/96, or the
	// deprecated IPv4-compatible ::a.b.c.d form) that a host's 6to4 relay, DNS64/NAT64 gateway,
	// or legacy stack can translate onto the internal network. Go's net.IP predicates and To4()
	// do not look through these, so unwrap and re-check the embedded IPv4 — else a 6to4/NAT64
	// or ::a.b.c.d form of 127.0.0.1 / 169.254.169.254 would slip past the guard.
	if embedded := embeddedIPv4(ip); embedded != nil {
		return isPublicUnicastIP(embedded)
	}
	return true
}

// nat64WellKnownPrefix is the 96-bit RFC 6052 NAT64 well-known prefix 64:ff9b::/96; the embedded
// IPv4 is the trailing 4 bytes.
var nat64WellKnownPrefix = []byte{0x00, 0x64, 0xff, 0x9b, 0, 0, 0, 0, 0, 0, 0, 0}

// embeddedIPv4 returns the IPv4 carried inside a 6to4 (2002::/16), NAT64 well-known
// (64:ff9b::/96), or IPv4-compatible (::a.b.c.d, the deprecated RFC 4291 form with a zero
// high-96 and a non-trivial low-32) IPv6 address, or nil if ip is none of those.
func embeddedIPv4(ip net.IP) net.IP {
	ip = ip.To16()
	if ip == nil {
		return nil
	}
	if ip[0] == 0x20 && ip[1] == 0x02 { // 6to4: IPv4 W.X.Y.Z lives in bytes 2..5
		return net.IPv4(ip[2], ip[3], ip[4], ip[5])
	}
	if bytes.Equal(ip[:12], nat64WellKnownPrefix) { // NAT64: IPv4 in the trailing 4 bytes
		return net.IPv4(ip[12], ip[13], ip[14], ip[15])
	}
	// IPv4-compatible ::a.b.c.d: a zero high-96 with a non-trivial low-32. Go's To4() does NOT
	// unwrap this form (only ::ffff:a.b.c.d), so without this branch ::127.0.0.1 (parsed to
	// ::7f00:1) and ::169.254.169.254 would slip through as an "ordinary" public IPv6. Guard
	// against the trivial low values that are NOT an embedded host: :: (unspecified — already
	// caught upstream) and ::a.b.c.d where the low-32 is < 256 (e.g. ::1 loopback, ::255), which
	// are reserved/degenerate and never a real translated v4 destination.
	for i := 0; i < 12; i++ {
		if ip[i] != 0 {
			return nil // non-zero high-96 → not the ::a.b.c.d form
		}
	}
	if ip[12] == 0 && ip[13] == 0 { // low-32 < 65536: :: , ::1 , ::a.b — degenerate, not an embedded v4
		return nil
	}
	return net.IPv4(ip[12], ip[13], ip[14], ip[15])
}

// newReleasePinClient builds the egress-guarded HTTP client the release-pin handler fetches
// sidecars with: a bounded timeout, a redirect cap that refuses any non-http(s) hop, and a
// dial-time private-IP reject (blockPrivateAddr). Environment proxies are deliberately ignored
// (Proxy: nil) — the gh-proxy is applied in the URL, so egress is fully determined by the URL.
func newReleasePinClient() *http.Client {
	dialer := &net.Dialer{Timeout: releaseDialTimeout, Control: blockPrivateAddr}
	transport := &http.Transport{
		DialContext:           dialer.DialContext,
		Proxy:                 nil,
		TLSHandshakeTimeout:   releaseDialTimeout,
		ResponseHeaderTimeout: releaseFetchTimeout,
		DisableKeepAlives:     true, // one-shot fetches; no connection reuse needed
	}
	return &http.Client{
		Timeout:   releaseFetchTimeout,
		Transport: transport,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= releaseMaxRedirects {
				return fmt.Errorf("release-pin fetch: too many redirects (>%d)", releaseMaxRedirects)
			}
			if s := req.URL.Scheme; s != "http" && s != "https" {
				return fmt.Errorf("release-pin fetch: refusing redirect to non-http(s) scheme %q", s)
			}
			return nil
		},
	}
}

// --- wire types ---

// releasePinRequestJSON asks the server to fetch the .sha256 sidecars for a set of release
// assets. kind selects the asset/key grammar and the default release base ("agent" → the
// settings AgentReleaseBaseURL + linux-<arch> keys; "mimic" → the settings MimicReleaseBase +
// <codename>-<arch> keys). version (optional) pins a "releases/latest/download" base to a tag.
// base (optional) overrides the settings base so the panel can assist before saving. assets may
// be empty for kind=agent (the two certified arches are derived).
type releasePinRequestJSON struct {
	Kind    string                `json:"kind"`
	Version string                `json:"version,omitempty"`
	Base    string                `json:"base,omitempty"`
	Assets  []releasePinAssetJSON `json:"assets,omitempty"`
}

type releasePinAssetJSON struct {
	Key   string `json:"key"`
	Asset string `json:"asset"`
}

// releasePinResponseJSON returns the resolved pins for operator review. pins maps each requested
// key to a renderer.Artifact ({asset, sha256}) the operator can save through /settings.
//
// base + version echo what was used. version_applied is false when a custom/mirror base ignored
// the requested version (so the UI can warn). IMPORTANT when version_applied is TRUE: base is the
// TAGGED url the pins were computed against, and the UI MUST persist it as the agent release base
// (AgentReleaseBaseURL) — the agent fetches the VERBATIM saved base with no latest->tag rewrite
// (render/artifacts_json.go -> agent/selfupdate.go), so pinning a tagged hash while leaving the
// base at the moving "latest" alias makes the agent download a different binary whose hash will
// not match (a fail-closed mismatch that silently stalls the rollout).
//
// proxy_applied reports whether the gh-proxy prefixed the fetch. resolved carries the exact
// fetched URL per key so a 404 cause is visible.
type releasePinResponseJSON struct {
	Pins           map[string]model.Artifact `json:"pins"`
	Base           string                    `json:"base"`
	Version        string                    `json:"version"`
	VersionApplied bool                      `json:"version_applied"`
	ProxyApplied   bool                      `json:"proxy_applied"`
	Resolved       map[string]string         `json:"resolved"`
}

// defaultAgentAssets are the linux-<arch> agent assets self-update is certified for
// (selfupdate.go: amd64/arm64 only). The release publishes "yaog-agent-<key>" per arch
// (release.yml "Stage Standalone Agent Binary"), so the asset name is derivable from the key.
func defaultAgentAssets() []releasePinAssetJSON {
	return []releasePinAssetJSON{
		{Key: "linux-amd64", Asset: "yaog-agent-linux-amd64"},
		{Key: "linux-arm64", Asset: "yaog-agent-linux-arm64"},
	}
}

// resolveReleaseBase pins base to a specific version when it can. When version is non-empty AND
// base is the project's "releases/latest/download" alias, it rewrites to "releases/download/<tag>"
// where tag is the version with a leading "v" prepended if absent (git tags are v-prefixed though
// semverPattern accepts the bare form, so a bare "2.0.0" would otherwise 404). A custom/mirror
// base is returned verbatim and the version is IGNORED (versionApplied=false) so the UI can warn.
func resolveReleaseBase(base, version string) (resolved string, versionApplied bool) {
	base = strings.TrimRight(base, "/")
	if version == "" || !strings.HasSuffix(base, releaseLatestSuffix) {
		return base, false
	}
	tag := version
	if !strings.HasPrefix(tag, "v") {
		tag = "v" + tag
	}
	return strings.TrimSuffix(base, releaseLatestSuffix) + "releases/download/" + tag, true
}

// validateReleaseURL is the pre-fetch SSRF format gate: an absolute http(s) URL (the
// validateAbsoluteHTTPURL whitespace+scheme+host check) with no shell metacharacters (mirroring
// validateMimicCatalog's release-base check). It is a FORMAT check only — the actual egress
// safety is the dial-time private-IP reject in blockPrivateAddr.
func validateReleaseURL(s string) error {
	if err := validateAbsoluteHTTPURL(s); err != nil {
		return err
	}
	if strings.ContainsAny(s, "$`;|&<>(){}[]'\"\\*? ") {
		return errors.New("must not contain shell metacharacters")
	}
	return nil
}

// HandleReleasePins (POST {operatorBase}release-pins, operator-authenticated) fetches the
// .sha256 sidecars for the requested release assets through the persisted gh-proxy and returns
// renderer.Artifact pins for the operator to REVIEW and save. See the file header for the custody
// argument: this is a convenience fetch, never a trust primitive.
func (h *ControllerHandler) HandleReleasePins(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeAPIError(w, apierr.New(apierr.CodeMethodNotAllowed).With("method", "POST"))
		return
	}
	var req releasePinRequestJSON
	if err := decodeJSON(w, r, &req); err != nil {
		writeCodedOr(w, apierr.CodeReqInvalidBody, err)
		return
	}

	// Settings supply the gh-proxy (always) and the default release base (when the request does
	// not override it). Defaults applied so kind=agent always has a base.
	cs, err := h.loadSettings(r)
	if err != nil {
		writeCodedOr(w, apierr.CodeInternalStorage, err)
		return
	}

	// kind selects the key/asset grammar (the same patterns /settings validates saved pins
	// against, so an assist that passes here also passes save) and the default release base.
	// Default to the agent grammar; only mimic overrides it.
	keyRe, assetRe := agentBinKeyPattern, agentBinAssetPattern
	var defaultBase string
	switch req.Kind {
	case "agent":
		defaultBase = cs.AgentReleaseBaseURL
	case "mimic":
		keyRe, assetRe, defaultBase = debKeyPattern, debAssetPattern, cs.MimicReleaseBase
	default:
		writeAPIError(w, apierr.New(apierr.CodeAgentReleaseRequestInvalid).With("field", "kind"))
		return
	}

	// version: format-check before it is interpolated into a tag path (a non-semver value could
	// otherwise path-traverse the release URL).
	version := strings.TrimSpace(req.Version)
	if version != "" && !semverPattern.MatchString(version) {
		writeAPIError(w, apierr.New(apierr.CodeAgentReleaseRequestInvalid).With("field", "version"))
		return
	}

	// base: the request override or the settings default; required (mimic with no base cannot fetch).
	base := strings.TrimSpace(req.Base)
	if base == "" {
		base = defaultBase
	}
	if base == "" {
		writeAPIError(w, apierr.New(apierr.CodeAgentReleaseRequestInvalid).With("field", "base"))
		return
	}
	if err := validateReleaseURL(base); err != nil {
		writeAPIError(w, apierr.New(apierr.CodeAgentReleaseRequestInvalid).With("field", "base").Wrap(err))
		return
	}

	// assets: validate each key+asset against the kind's grammar, or derive the certified agent
	// arches when none were supplied (mimic assets are operator-defined, so empty is an error).
	assets := req.Assets
	if len(assets) == 0 {
		if req.Kind != "agent" {
			writeAPIError(w, apierr.New(apierr.CodeAgentReleaseRequestInvalid).With("field", "assets"))
			return
		}
		assets = defaultAgentAssets()
	}
	for _, a := range assets {
		if !keyRe.MatchString(a.Key) {
			writeAPIError(w, apierr.New(apierr.CodeAgentReleaseRequestInvalid).With("field", "assets.key"))
			return
		}
		if !assetRe.MatchString(a.Asset) {
			writeAPIError(w, apierr.New(apierr.CodeAgentReleaseRequestInvalid).With("field", "assets.asset"))
			return
		}
	}

	resolvedBase, versionApplied := resolveReleaseBase(base, version)
	proxy := cs.GithubProxy

	pins := make(map[string]model.Artifact, len(assets))
	resolved := make(map[string]string, len(assets))
	for _, a := range assets {
		url := proxy + resolvedBase + "/" + a.Asset + ".sha256"
		resolved[a.Key] = url
		if err := validateReleaseURL(url); err != nil {
			writeAPIError(w, apierr.New(apierr.CodeAgentReleaseRequestInvalid).With("field", "url").Wrap(err))
			return
		}
		sum, aerr := h.fetchSidecar(r.Context(), url)
		if aerr != nil {
			writeAPIError(w, aerr)
			return
		}
		pins[a.Key] = model.Artifact{Asset: a.Asset, SHA256: sum}
	}

	writeJSON(w, http.StatusOK, releasePinResponseJSON{
		Pins:           pins,
		Base:           resolvedBase,
		Version:        version,
		VersionApplied: versionApplied,
		ProxyApplied:   proxy != "",
		Resolved:       resolved,
	})
}

// genericFetchDetail is the FIXED client-facing detail for a transport/dial/read failure. The
// real err (which a dial refusal embeds the RESOLVED IP into — "...non-public address <IP>") is
// kept ONLY in the wrapped cause for the server log, never in a serialized param: putting the
// resolved IP on the wire would turn a 502 into a DNS-rebind oracle (S8 — confirm which internal
// IP a hostname resolves to). The status-code branch keeps "HTTP <code>" (no IP).
const genericFetchDetail = "fetch failed"

// fetchSidecar GETs a release .sha256 sidecar through the egress-guarded client and returns the
// lower-cased 64-hex digest it contains. The body is read through a small LimitReader and the
// first whitespace-delimited token is validated against sha256HexPattern, so an HTML error page,
// an oversize file, or a malformed sidecar is rejected rather than trusted. A transport/status
// failure → CodeAgentReleaseFetchFailed (502); a non-SHA-256 body → CodeAgentReleaseSidecarInvalid.
//
// S8: transport/dial/read failures collapse to genericFetchDetail on the wire; the underlying err
// (which may carry the resolved internal IP) is logged server-side and kept only as the wrapped
// cause, never as a serialized "detail" param.
func (h *ControllerHandler) fetchSidecar(ctx context.Context, url string) (string, *apierr.Error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", apierr.New(apierr.CodeAgentReleaseRequestInvalid).With("field", "url").Wrap(err)
	}
	resp, err := h.releaseClient.Do(req)
	if err != nil {
		log.Printf("release-pin fetch %s: transport error: %v", url, err) // server log only; never serialized
		return "", apierr.New(apierr.CodeAgentReleaseFetchFailed).With("url", url).With("detail", genericFetchDetail).Wrap(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", apierr.New(apierr.CodeAgentReleaseFetchFailed).With("url", url).With("detail", "HTTP "+strconv.Itoa(resp.StatusCode))
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, releaseSidecarCap))
	if err != nil {
		log.Printf("release-pin fetch %s: body read error: %v", url, err) // server log only; never serialized
		return "", apierr.New(apierr.CodeAgentReleaseFetchFailed).With("url", url).With("detail", genericFetchDetail).Wrap(err)
	}
	fields := strings.Fields(string(body))
	if len(fields) == 0 || !sha256HexPattern.MatchString(fields[0]) {
		return "", apierr.New(apierr.CodeAgentReleaseSidecarInvalid).With("url", url)
	}
	return strings.ToLower(fields[0]), nil
}
