// Package agent implements the YAOG node agent: a thin, single-tenant control
// loop that pulls a per-node install bundle from a configured source, verifies
// it (Ed25519 signature over the canonical checksums plus per-file SHA-256),
// enforces anti-rollback, then hands the staged bundle to the very same
// install.sh the air-gap operator would run by hand. The agent never reimplements
// WireGuard/Babel orchestration and never splices the private key — install.sh
// owns the custody-gated splice from /etc/wireguard/agent.key. Identity is
// configured (a --node-id), not enrolled; real enrollment is a later plan.
package agent

import (
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"
)

// bundleFileNames is the closed set of bundle-relative paths the agent knows how
// to fetch for a node. Sources that cannot enumerate a directory (HTTP) are
// driven by this list; checksums.sha256 is always fetched first because it is
// the authority for which of the optional files must actually be present.
//
// The list is a superset: babel/, sysctl/, bundle.sig and signing-pubkey.pem are
// optional (client bundles omit babel; unsigned bundles omit the signature pair),
// trustlist.json/trustlist.sig are present only when the keystone (off-host signed
// trust-list) is enabled, and the wireguard/ confs are per-peer so their exact names
// are not known ahead of time. Fetch therefore tolerates "not found" for everything
// EXCEPT the files that checksums.sha256 lists — verify (not Fetch) enforces
// completeness. trustlist.json/.sig are NOT listed in checksums.sha256 (they bind its
// digest, so they live OUTSIDE it); when the keystone is on, VerifyMembership requires
// them explicitly (and the off-host signature + digest binding enforce them).
var bundleFileNames = []string{
	"checksums.sha256",
	"manifest.json",
	"install.sh",
	"bundle.sig",
	"signing-pubkey.pem",
	"trustlist.json",
	"trustlist.sig",
	"sysctl/99-overlay.conf",
	"babel/babeld.conf",
	"README.txt",
}

// Source abstracts where a node's bundle comes from. Fetch returns a map of
// bundle-relative path -> file content for the named node. The returned map MUST
// include checksums.sha256 (the integrity authority) and every file that
// checksums.sha256 lists; it must also include manifest.json (anti-rollback) and
// install.sh (apply). When the bundle is signed it must include bundle.sig and
// signing-pubkey.pem. Optional files absent from a given bundle (e.g. babel/ for
// a client) are simply omitted from the map.
type Source interface {
	// Fetch retrieves the bundle for nodeID as path->content pairs.
	Fetch(nodeID string) (map[string][]byte, error)
}

// Reporter is implemented by sources that can accept a status report back from
// the agent (HTTPSource does; DirSource does not). report is best-effort: callers
// must not fail an otherwise-successful apply if Report errors.
type Reporter interface {
	// Report POSTs a status payload for nodeID; the payload is opaque JSON bytes.
	Report(nodeID string, payload []byte) error
}

// DirSource reads bundles from a local directory tree rooted at Root, where each
// node's bundle lives under Root/<nodeID>/ with the export path's relative layout
// (wireguard/*.conf, babel/babeld.conf, sysctl/99-overlay.conf, install.sh,
// checksums.sha256, manifest.json, and bundle.sig + signing-pubkey.pem when
// signed). It enumerates the tree, so it does not rely on bundleFileNames and
// naturally picks up the per-peer wireguard confs by walking the directory.
type DirSource struct {
	// Root is the parent directory that contains one subdirectory per node.
	Root string
}

// NewDirSource builds a DirSource rooted at the given directory.
func NewDirSource(root string) *DirSource {
	return &DirSource{Root: root}
}

// Fetch walks Root/<nodeID>/ and returns every regular file as a
// bundle-relative (slash-separated) path -> content map.
func (d *DirSource) Fetch(nodeID string) (map[string][]byte, error) {
	if strings.TrimSpace(nodeID) == "" {
		return nil, fmt.Errorf("agent: DirSource.Fetch: empty nodeID")
	}
	nodeRoot := filepath.Join(d.Root, nodeID)
	info, err := os.Stat(nodeRoot)
	if err != nil {
		return nil, fmt.Errorf("agent: bundle dir for node %q: %w", nodeID, err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("agent: bundle path for node %q is not a directory: %s", nodeID, nodeRoot)
	}

	files := make(map[string][]byte)
	walkErr := filepath.Walk(nodeRoot, func(p string, fi os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if fi.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(nodeRoot, p)
		if err != nil {
			return err
		}
		// Bundle paths are always slash-separated regardless of host OS so they
		// match the keys in checksums.sha256 exactly.
		rel = filepath.ToSlash(rel)
		content, err := os.ReadFile(p)
		if err != nil {
			return fmt.Errorf("agent: read %s: %w", rel, err)
		}
		files[rel] = content
		return nil
	})
	if walkErr != nil {
		return nil, fmt.Errorf("agent: walk bundle dir for node %q: %w", nodeID, walkErr)
	}
	if _, ok := files["checksums.sha256"]; !ok {
		return nil, fmt.Errorf("agent: bundle for node %q has no checksums.sha256", nodeID)
	}
	return files, nil
}

// HTTPSource fetches bundles over HTTP(S). Files live under
// BaseURL/<nodeID>/<relative-path>. Because plain HTTP cannot enumerate a
// directory, HTTPSource fetches checksums.sha256 first, then fetches every file
// the checksums list names (the per-peer wireguard confs are discovered this
// way), plus the well-known control files in bundleFileNames. A 404 for an
// optional control file is tolerated; a 404 for a checksums-listed file is fatal.
type HTTPSource struct {
	// BaseURL is the prefix under which each node's bundle is served. A trailing
	// slash is optional; it is normalized away.
	BaseURL string
	// Client is the HTTP client used for all requests. When nil a default client
	// with a bounded timeout is used.
	Client *http.Client
}

// NewHTTPSource builds an HTTPSource for the given base URL, installing a default
// HTTP client with a conservative timeout when none is supplied.
func NewHTTPSource(baseURL string) *HTTPSource {
	return &HTTPSource{
		BaseURL: strings.TrimRight(baseURL, "/"),
		Client:  &http.Client{Timeout: 30 * time.Second},
	}
}

// httpClient returns the configured client or a bounded default.
func (h *HTTPSource) httpClient() *http.Client {
	if h.Client != nil {
		return h.Client
	}
	return &http.Client{Timeout: 30 * time.Second}
}

// fileURL joins BaseURL, nodeID and a bundle-relative path into a request URL,
// URL-escaping each path segment so node IDs and filenames with reserved
// characters cannot break out of the bundle namespace.
func (h *HTTPSource) fileURL(nodeID, rel string) string {
	base := strings.TrimRight(h.BaseURL, "/")
	segments := []string{url.PathEscape(nodeID)}
	for _, seg := range strings.Split(rel, "/") {
		if seg == "" {
			continue
		}
		segments = append(segments, url.PathEscape(seg))
	}
	return base + "/" + path.Join(segments...)
}

// get performs a single GET. It returns (content, true, nil) on 200,
// (nil, false, nil) on 404, and (nil, false, err) on any other failure so
// callers can distinguish "optional file absent" from "source unreachable".
func (h *HTTPSource) get(nodeID, rel string) ([]byte, bool, error) {
	reqURL := h.fileURL(nodeID, rel)
	resp, err := h.httpClient().Get(reqURL)
	if err != nil {
		return nil, false, fmt.Errorf("agent: GET %s: %w", reqURL, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return nil, false, nil
	}
	if resp.StatusCode != http.StatusOK {
		return nil, false, fmt.Errorf("agent: GET %s: status %d", reqURL, resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, false, fmt.Errorf("agent: read body %s: %w", reqURL, err)
	}
	return body, true, nil
}

// Fetch implements Source over HTTP. It fetches checksums.sha256 first, then the
// well-known control files and every path the checksums list names.
func (h *HTTPSource) Fetch(nodeID string) (map[string][]byte, error) {
	if strings.TrimSpace(nodeID) == "" {
		return nil, fmt.Errorf("agent: HTTPSource.Fetch: empty nodeID")
	}

	files := make(map[string][]byte)

	// checksums.sha256 is mandatory and is the authority for the rest of the set.
	checksums, ok, err := h.get(nodeID, "checksums.sha256")
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, fmt.Errorf("agent: node %q: checksums.sha256 not found at source", nodeID)
	}
	files["checksums.sha256"] = checksums

	// Every file the checksums list names must be present (these include the
	// per-peer wireguard confs, which are not in the well-known list).
	listed, err := parseChecksums(checksums)
	if err != nil {
		return nil, fmt.Errorf("agent: node %q: %w", nodeID, err)
	}
	for rel := range listed {
		if _, have := files[rel]; have {
			continue
		}
		body, ok, err := h.get(nodeID, rel)
		if err != nil {
			return nil, err
		}
		if !ok {
			return nil, fmt.Errorf("agent: node %q: checksums lists %q but source returned 404", nodeID, rel)
		}
		files[rel] = body
	}

	// Well-known control files (manifest.json, install.sh, signature pair, etc.).
	// These may or may not be listed in checksums; fetch any we do not yet have.
	// A 404 here is tolerated (optional file), but unreachable is fatal.
	for _, rel := range bundleFileNames {
		if _, have := files[rel]; have {
			continue
		}
		body, ok, err := h.get(nodeID, rel)
		if err != nil {
			return nil, err
		}
		if ok {
			files[rel] = body
		}
	}

	return files, nil
}

// Report POSTs a status payload to BaseURL/<nodeID>/report. It is best-effort:
// any non-2xx status or transport error is returned to the caller, which logs it
// but does not fail the apply.
func (h *HTTPSource) Report(nodeID string, payload []byte) error {
	reqURL := h.fileURL(nodeID, "report")
	resp, err := h.httpClient().Post(reqURL, "application/json", strings.NewReader(string(payload)))
	if err != nil {
		return fmt.Errorf("agent: POST %s: %w", reqURL, err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("agent: POST %s: status %d", reqURL, resp.StatusCode)
	}
	return nil
}

// NewSourceFromSpec parses a --source spec into a concrete Source.
//
// Accepted forms:
//   - "dir:PATH"            -> DirSource rooted at PATH
//   - "http://..."          -> HTTPSource
//   - "https://..."         -> HTTPSource
//
// Any other form is a configuration error.
func NewSourceFromSpec(spec string) (Source, error) {
	spec = strings.TrimSpace(spec)
	switch {
	case strings.HasPrefix(spec, "dir:"):
		dir := strings.TrimPrefix(spec, "dir:")
		if strings.TrimSpace(dir) == "" {
			return nil, fmt.Errorf("agent: source spec %q has empty directory path", spec)
		}
		return NewDirSource(dir), nil
	case strings.HasPrefix(spec, "http://"), strings.HasPrefix(spec, "https://"):
		return NewHTTPSource(spec), nil
	default:
		return nil, fmt.Errorf("agent: unrecognized source spec %q (want dir:PATH or http(s)://...)", spec)
	}
}
