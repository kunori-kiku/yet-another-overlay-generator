package api

// release_assets.go is the operator-only assisted release-ASSET DISCOVERY fetch (beta9-smoke-
// hardening plan-4). The mimic ".deb catalog" otherwise forces the operator to hand-type the exact
// upstream package filenames; this lists a GitHub release's .deb assets so the panel can present a
// pick-from checklist. It reuses the same egress-guarded client (h.releaseClient) + SSRF dial guard
// (blockPrivateAddr) as the sibling release-pin fetch (release_pins.go), but — unlike that pin fetch
// — it hits the GitHub REST API DIRECTLY (NOT the gh-proxy): the proxy's shared API identity is
// globally rate-limited, and api.github.com is broadly reachable + the listing is non-custody. The
// .deb DOWNLOADS the install performs still route through the gh-proxy (its real purpose).
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
	// defaultGithubAPIBase is the GitHub REST API origin asset discovery hits directly.
	defaultGithubAPIBase = "https://api.github.com"
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

// deriveReleaseRefs maps a github.com release URL — in any of the forms an operator might naturally
// paste — to (a) the REST API PATH that lists that release's assets and (b) the canonical DOWNLOAD
// base the install fetches .debs from (`downloadBase + "/" + asset`). Accepted inputs:
//
//	github.com/<owner>/<repo>                          \
//	github.com/<owner>/<repo>/releases                  > latest -> apiPath /repos/<o>/<r>/releases/latest
//	github.com/<owner>/<repo>/releases/latest           |        downloadBase .../releases/latest/download
//	github.com/<owner>/<repo>/releases/latest/download /
//	github.com/<owner>/<repo>/releases/download/<tag>  \
//	github.com/<owner>/<repo>/releases/tag/<tag>        > tagged -> apiPath /repos/<o>/<r>/releases/tags/<tag>
//	github.com/<owner>/<repo>/releases/tags/<tag>      /        downloadBase .../releases/download/<tag>
//
// The host is PINNED to github.com and owner/repo/tag must each pass safeGitHubSeg (char allow-list +
// no "."/".." traversal), so the derived API path cannot be steered off-host. Returning the canonical
// download base lets discover NORMALIZE a loosely-typed base to the form the install actually needs
// (the panel adopts it). A non-github / unrecognizable URL hard-fails — discovery is a github-release
// convenience, not a general fetcher.
func deriveReleaseRefs(base string) (apiPath, downloadBase string, err error) {
	base = strings.TrimRight(strings.TrimSpace(base), "/")
	u, perr := url.Parse(base)
	if perr != nil {
		return "", "", fmt.Errorf("unparseable release base: %w", perr)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", "", errors.New("release base must be an http(s) URL")
	}
	if !strings.EqualFold(u.Host, "github.com") {
		return "", "", errors.New("the release base must be a github.com release URL (e.g. https://github.com/<owner>/<repo>/releases/latest/download)")
	}
	segs := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(segs) < 2 {
		return "", "", errors.New("the release base must name an owner/repo (e.g. https://github.com/<owner>/<repo>/releases/latest/download)")
	}
	owner, repo := segs[0], segs[1]
	if !safeGitHubSeg(owner) || !safeGitHubSeg(repo) {
		return "", "", errors.New("owner/repo contain disallowed characters")
	}
	apiBase := "/repos/" + owner + "/" + repo
	dlBase := "https://github.com/" + owner + "/" + repo
	rest := segs[2:] // path after /<owner>/<repo>

	// latest: repo root, ".../releases", ".../releases/latest", ".../releases/latest/download".
	isLatest := len(rest) == 0 ||
		(len(rest) == 1 && rest[0] == "releases") ||
		(len(rest) == 2 && rest[0] == "releases" && rest[1] == "latest") ||
		(len(rest) == 3 && rest[0] == "releases" && rest[1] == "latest" && rest[2] == "download")
	if isLatest {
		return apiBase + "/releases/latest", dlBase + "/releases/latest/download", nil
	}
	// tagged: ".../releases/(download|tag|tags)/<tag>".
	if len(rest) == 3 && rest[0] == "releases" && (rest[1] == "download" || rest[1] == "tag" || rest[1] == "tags") {
		tag := rest[2]
		if !safeGitHubSeg(tag) {
			return "", "", errors.New("tag contains disallowed characters")
		}
		return apiBase + "/releases/tags/" + tag, dlBase + "/releases/download/" + tag, nil
	}
	return "", "", errors.New("unrecognized github release URL; use .../releases/latest/download or .../releases/download/<tag>")
}

// --- wire types ---

// releaseAssetsRequestJSON asks the server to list the .deb assets of a GitHub release. base
// (optional) overrides the settings MimicReleaseBase so the panel can discover before saving. There
// is NO version field: which release is listed is determined entirely by the base (a
// ".../releases/latest/..." base lists latest; a ".../releases/download/<tag>" base lists that tag).
// The catalog "version" is operator bookkeeping only — it does not steer discovery.
type releaseAssetsRequestJSON struct {
	Base string `json:"base,omitempty"`
}

// releaseAssetsResponseJSON returns the discovered .deb asset names for the operator to pick from.
// base is the CANONICAL download base derived from the request (normalized to the
// ".../releases/latest/download" | ".../releases/download/<tag>" form the install actually fetches
// from), so the panel can adopt it. Discovery hits the GitHub REST API directly — never the
// gh-proxy — so there is no proxy_applied field.
type releaseAssetsResponseJSON struct {
	Assets []string `json:"assets"`
	Base   string   `json:"base"`
}

// HandleReleaseAssets (POST {operatorBase}release-assets, operator-authenticated) lists a GitHub
// release's .deb asset names by hitting the GitHub REST API DIRECTLY (not the gh-proxy — see the
// githubAPIBase field doc), so the mimic catalog UI can offer a pick-from checklist instead of
// hand-typed filenames. See the file header: discovery is a convenience; the SHA-256 pin is still
// fetched + saved separately (through the proxy).
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

	// Settings supply the default mimic release base (when the request does not override it).
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
	if err := validateReleaseURL(base); err != nil {
		writeAPIError(w, apierr.New(apierr.CodeAgentReleaseRequestInvalid).With("field", "base").Wrap(err))
		return
	}

	apiPath, downloadBase, err := deriveReleaseRefs(base)
	if err != nil {
		writeAPIError(w, apierr.New(apierr.CodeAgentReleaseRequestInvalid).With("field", "base").Wrap(err))
		return
	}
	// Direct to the GitHub REST API — NOT through the gh-proxy (its shared API identity is globally
	// rate-limited; a 403 for everyone). The egress-guarded releaseClient still applies the dial-time
	// private-IP reject, and the host is pinned by deriveReleaseRefs.
	apiURL := strings.TrimRight(h.githubAPIBase, "/") + apiPath

	names, aerr := h.fetchReleaseAssetNames(r.Context(), apiURL)
	if aerr != nil {
		writeAPIError(w, aerr)
		return
	}

	writeJSON(w, http.StatusOK, releaseAssetsResponseJSON{
		Assets: names,
		Base:   downloadBase,
	})
}

// fetchReleaseAssetNames GETs the GitHub releases API through the egress-guarded client and returns
// the release's *.deb asset names — excluding debug sidecars (dbgsym / .ddeb) — deduped and capped.
// A transport/status failure → CodeAgentReleaseFetchFailed (502); a non-JSON body (e.g. a captive
// portal or an intercepting middlebox returning HTML) is rejected as a fetch failure rather than
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
		// An intercepting middlebox / captive portal returns HTML — not a github asset list.
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
