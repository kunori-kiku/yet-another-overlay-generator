package model

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

// InstallFetch is the install.sh-relevant subset of render.FetchSettings. The install.sh
// mimic-from-GitHub fallback reads the pin (release_url + per-"<codename>-<arch>" asset +
// sha256) from the integrity-verified artifacts.json bundle member at install time — the
// single signed source of truth — so the only value that must be BAKED into the script is
// the optional GitHub proxy prefix (a deploy-network preference, kept out of the signed
// catalog so changing it does not churn the bundle digest). It is set on
// InstallScriptConfig/ClientInstallScriptConfig by the signed renderers (after
// buildInstallScriptConfig, mirroring SigningPubkeyPEM).
//
// The zero value carries no proxy and, paired with HasMimic=false / no artifacts.json,
// leaves install.sh byte-identical to the pre-FetchSettings output (air-gap byte-identity).
type InstallFetch struct {
	// GithubProxy is an optional prefix applied to GitHub downloads (e.g.
	// "https://gh-proxy.com/"). Empty = direct github.com. It is shell-escaped (shq) at the
	// template boundary before being baked as GH_PROXY.
	GithubProxy string
}
