package api

// release_assets.go is the operator-only assisted release-ASSET DISCOVERY fetch (beta9-smoke-
// hardening plan-4). The mimic ".deb catalog" otherwise forces the operator to hand-type the exact
// upstream package filenames; this lists a GitHub release's .deb assets so the panel can present a
// pick-from checklist. It reuses the same egress-guarded client (h.releaseClient), gh-proxy, and
// SSRF dial guard (blockPrivateAddr) as the sibling release-pin fetch (release_pins.go).
//
// CUSTODY: like release-pins, this is a CONVENIENCE — the discovered names are just labels the
// operator picks from; nothing is trusted or persisted here. The SHA-256 pin (the actual custody
// material) is still fetched separately through the existing per-row Assist (release-pins) and
// saved through the validated /settings path. Discovery never auto-fills a hash.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"github.com/kunorikiku/yet-another-overlay-generator/internal/apierr"
)

const (
	// releaseAssetsBodyCap caps the GitHub release JSON read. A release with many assets is still
	// small (each asset is a short JSON object), so 2 MiB is generous and makes an unexpected huge
	// body (a mis-proxied binary) cheap to reject.
	releaseAssetsBodyCap = 2 << 20
	// releaseAssetsMaxCount caps how many .deb names are returned, so a release with a pathological
	// asset count cannot balloon the response. Far above any real mimic .deb matrix.
	releaseAssetsMaxCount = 200
)

// ghPathSegPattern is the character allow-list for a github.com owner / repo / tag path segment:
// no slashes, so a derived api.github.com path cannot be steered to another host or endpoint.
var ghPathSegPattern = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

// safeGitHubSeg reports whether s is a usable github path segment: it matches the character
// allow-list AND is not a "." / ".." traversal token (both of which the dot-permitting pattern
// would otherwise accept, e.g. ".." in /<owner>/../releases/...).
func safeGitHubSeg(s string) bool {
	return s != "." && s != ".." && ghPathSegPattern.MatchString(s)
}

// deriveReleasesAPIURL maps a GitHub release DOWNLOAD base to the GitHub REST API endpoint that
// lists that release's assets. It accepts ONLY the two canonical github.com download bases:
//
//	https://github.com/<owner>/<repo>/releases/latest/download  ->  .../releases/latest
//	https://github.com/<owner>/<repo>/releases/download/<tag>    ->  .../releases/tags/<tag>
//
// The host is PINNED to github.com and owner/repo/tag must each match ghPathSegPattern (no slashes,
// no traversal), so the derived API URL cannot be steered off api.github.com. A non-github or
// malformed base hard-fails — asset discovery is a github-release convenience, not a general
// fetcher. The returned URL targets api.github.com; the caller applies any gh-proxy prefix.
func deriveReleasesAPIURL(base string) (string, error) {
	base = strings.TrimRight(strings.TrimSpace(base), "/")
	u, err := url.Parse(base)
	if err != nil {
		return "", fmt.Errorf("unparseable release base: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", errors.New("release base must be an http(s) URL")
	}
	if !strings.EqualFold(u.Host, "github.com") {
		return "", errors.New("asset discovery supports only github.com release bases")
	}
	// Path: /<owner>/<repo>/releases/(latest/download | download/<tag>)
	segs := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(segs) != 5 || segs[2] != "releases" {
		return "", errors.New("release base path must be /<owner>/<repo>/releases/...")
	}
	owner, repo := segs[0], segs[1]
	if !safeGitHubSeg(owner) || !safeGitHubSeg(repo) {
		return "", errors.New("owner/repo contain disallowed characters")
	}
	switch {
	case segs[3] == "latest" && segs[4] == "download":
		return "https://api.github.com/repos/" + owner + "/" + repo + "/releases/latest", nil
	case segs[3] == "download":
		tag := segs[4]
		if !safeGitHubSeg(tag) {
			return "", errors.New("tag contains disallowed characters")
		}
		return "https://api.github.com/repos/" + owner + "/" + repo + "/releases/tags/" + tag, nil
	default:
		return "", errors.New("release base must end in releases/latest/download or releases/download/<tag>")
	}
}

// --- wire types ---

// releaseAssetsRequestJSON asks the server to list the .deb assets of a GitHub release. base
// (optional) overrides the settings MimicReleaseBase so the panel can discover before saving;
// version (optional) pins a "releases/latest/download" base to a tag (same rule as release-pins).
type releaseAssetsRequestJSON struct {
	Base    string `json:"base,omitempty"`
	Version string `json:"version,omitempty"`
}

// releaseAssetsResponseJSON returns the discovered .deb asset names for the operator to pick from.
// base + version echo what was used; version_applied is false when a custom/mirror base ignored the
// requested version; proxy_applied reports whether the gh-proxy prefixed the fetch.
type releaseAssetsResponseJSON struct {
	Assets         []string `json:"assets"`
	Base           string   `json:"base"`
	Version        string   `json:"version"`
	VersionApplied bool     `json:"version_applied"`
	ProxyApplied   bool     `json:"proxy_applied"`
}

// HandleReleaseAssets (POST {operatorBase}release-assets, operator-authenticated) lists a GitHub
// release's .deb asset names through the persisted gh-proxy and egress-guarded client, so the mimic
// catalog UI can offer a pick-from checklist instead of hand-typed filenames. See the file header:
// discovery is a convenience; the SHA-256 pin is still fetched + saved separately.
func (h *ControllerHandler) HandleReleaseAssets(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeAPIError(w, apierr.New(apierr.CodeMethodNotAllowed).With("method", "POST"))
		return
	}
	var req releaseAssetsRequestJSON
	if err := decodeJSON(w, r, &req); err != nil {
		writeCodedOr(w, apierr.CodeReqInvalidBody, err)
		return
	}

	// Settings supply the gh-proxy and the default mimic release base (when not overridden).
	cs, err := h.loadSettings(r)
	if err != nil {
		writeCodedOr(w, apierr.CodeInternalStorage, err)
		return
	}

	base := strings.TrimSpace(req.Base)
	if base == "" {
		base = cs.MimicReleaseBase
	}
	if base == "" {
		writeAPIError(w, apierr.New(apierr.CodeAgentReleaseRequestInvalid).With("field", "base"))
		return
	}
	// version: format-check before it is interpolated into a tag path (mirrors release-pins).
	version := strings.TrimSpace(req.Version)
	if version != "" && !semverPattern.MatchString(version) {
		writeAPIError(w, apierr.New(apierr.CodeAgentReleaseRequestInvalid).With("field", "version"))
		return
	}
	if err := validateReleaseURL(base); err != nil {
		writeAPIError(w, apierr.New(apierr.CodeAgentReleaseRequestInvalid).With("field", "base").Wrap(err))
		return
	}

	resolvedBase, versionApplied := resolveReleaseBase(base, version)
	apiURL, err := deriveReleasesAPIURL(resolvedBase)
	if err != nil {
		writeAPIError(w, apierr.New(apierr.CodeAgentReleaseRequestInvalid).With("field", "base").Wrap(err))
		return
	}
	proxy := cs.GithubProxy
	fetchURL := proxy + apiURL
	if err := validateReleaseURL(fetchURL); err != nil {
		writeAPIError(w, apierr.New(apierr.CodeAgentReleaseRequestInvalid).With("field", "url").Wrap(err))
		return
	}

	names, aerr := h.fetchReleaseAssetNames(r.Context(), fetchURL)
	if aerr != nil {
		writeAPIError(w, aerr)
		return
	}

	writeJSON(w, http.StatusOK, releaseAssetsResponseJSON{
		Assets:         names,
		Base:           resolvedBase,
		Version:        version,
		VersionApplied: versionApplied,
		ProxyApplied:   proxy != "",
	})
}

// fetchReleaseAssetNames GETs the GitHub releases API through the egress-guarded client and returns
// the release's *.deb asset names — excluding debug sidecars (dbgsym / .ddeb) — deduped and capped.
// A transport/status failure → CodeAgentReleaseFetchFailed (502); a non-JSON body (e.g. a gh-proxy
// that does not proxy api.github.com and returns HTML) is rejected as a fetch failure rather than
// trusted. The resolved IP a dial refusal carries is logged server-side only (S8), never serialized.
func (h *ControllerHandler) fetchReleaseAssetNames(ctx context.Context, fetchURL string) ([]string, *apierr.Error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, fetchURL, nil)
	if err != nil {
		return nil, apierr.New(apierr.CodeAgentReleaseRequestInvalid).With("field", "url").Wrap(err)
	}
	// The GitHub REST API requires a User-Agent and honors the versioned Accept header.
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "yaog-controller")
	resp, err := h.releaseClient.Do(req)
	if err != nil {
		log.Printf("release-assets fetch %s: transport error: %v", fetchURL, err) // server log only; never serialized
		return nil, apierr.New(apierr.CodeAgentReleaseFetchFailed).With("url", fetchURL).With("detail", genericFetchDetail).Wrap(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, apierr.New(apierr.CodeAgentReleaseFetchFailed).With("url", fetchURL).With("detail", "HTTP "+strconv.Itoa(resp.StatusCode))
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, releaseAssetsBodyCap))
	if err != nil {
		log.Printf("release-assets fetch %s: body read error: %v", fetchURL, err) // server log only; never serialized
		return nil, apierr.New(apierr.CodeAgentReleaseFetchFailed).With("url", fetchURL).With("detail", genericFetchDetail).Wrap(err)
	}
	var parsed struct {
		Assets []struct {
			Name string `json:"name"`
		} `json:"assets"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		// A gh-proxy that does not proxy the REST API typically returns HTML — not a github asset list.
		return nil, apierr.New(apierr.CodeAgentReleaseFetchFailed).With("url", fetchURL).With("detail", "non-JSON response")
	}

	seen := make(map[string]bool, len(parsed.Assets))
	names := make([]string, 0, len(parsed.Assets))
	for _, a := range parsed.Assets {
		n := strings.TrimSpace(a.Name)
		if !strings.HasSuffix(n, ".deb") {
			continue
		}
		// Exclude debug-symbol sidecars: they are not installable mimic packages.
		if strings.Contains(n, "dbgsym") || strings.HasSuffix(n, ".ddeb") {
			continue
		}
		if seen[n] {
			continue
		}
		seen[n] = true
		names = append(names, n)
		if len(names) >= releaseAssetsMaxCount {
			break
		}
	}
	return names, nil
}
