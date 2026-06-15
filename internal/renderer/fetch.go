package renderer

// Artifact is one fetchable, integrity-pinned file: its release asset name and the
// SHA-256 the downloader must verify the bytes against before use. It is the shared
// pin type carried by render.FetchSettings (mimic .debs, agent binaries) and, here,
// by InstallFetch for the install.sh GitHub-.deb mimic fallback. Defined in package
// renderer (the lowest consumer — the install-script template) so render can reference
// it without the render -> renderer -> render import cycle that would arise if it lived
// in render. Zero value (both fields empty) means "no pin", which callers treat as
// absent.
type Artifact struct {
	Asset  string `json:"asset"`
	SHA256 string `json:"sha256"`
}

// InstallFetch is the install.sh-relevant subset of render.FetchSettings: the pins the
// generated install script needs to fetch mimic from GitHub when the distro package is
// unavailable (plan-3). It is threaded into InstallScriptConfig/ClientInstallScriptConfig
// by the signed renderers (set after buildInstallScriptConfig, mirroring SigningPubkeyPEM).
//
// The zero value carries no catalog, so the install.sh template emits no fetch branch and
// stays byte-identical to the pre-FetchSettings output — the air-gap byte-identity
// invariant. The agent self-update fields of render.FetchSettings are deliberately NOT
// here: install.sh never fetches the agent binary (the agent self-updates from the signed
// artifacts.json at runtime).
type InstallFetch struct {
	// GithubProxy is an optional prefix applied to GitHub downloads (e.g.
	// "https://gh-proxy.com/"). Empty = direct github.com.
	GithubProxy string
	// MimicVersion is the pinned mimic release version (semver), used to locate the
	// release and as a sanity tag. Empty = no GitHub fallback configured.
	MimicVersion string
	// MimicReleaseBase is the release base URL the .deb is downloaded from (the proxy is
	// prepended at install time). Empty = no GitHub fallback configured.
	MimicReleaseBase string
	// MimicDebs maps "<codename>-<arch>" (e.g. "bookworm-amd64") to the pinned .deb asset
	// + its SHA-256. Nil/empty = no GitHub fallback (distro-only mimic install).
	MimicDebs map[string]Artifact
}
