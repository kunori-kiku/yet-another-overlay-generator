package render

import (
	"encoding/json"
	"fmt"
	"os"
)

// Air-gap / local-mode artifact-catalog inputs (plan-7). Controller mode populates FetchSettings
// from ControllerSettings; air-gap and the local API have no controller, so they supply the same
// pins out-of-band via these env vars (cmd/compiler also exposes them as flags layered over env).
//
// ALL UNSET ⇒ the zero FetchSettings ⇒ distro-only mimic install and NO artifacts.json, so the
// signed bundle and install.sh stay byte-identical to today (D4, the air-gap byte-identity HIGH
// principle). Verified by the perpetual equivalence/signing byte-identity gates.
const (
	// EnvArtifactCatalog points at a JSON file in the SAME shape as the emitted artifacts.json
	// (schema/mimic/agent). It supplies the mimic release URL + the per-"<codename>-<arch>" .deb
	// pins; an operator can copy a controller-emitted artifacts.json straight into this file.
	EnvArtifactCatalog = "YAOG_ARTIFACT_CATALOG"
	// EnvGithubProxy is the optional GitHub download prefix baked into install.sh (e.g.
	// "https://gh-proxy.com/"). It is deployment-specific and NOT stored in the catalog file.
	EnvGithubProxy = "YAOG_GITHUB_PROXY"
	// EnvMimicVersion overrides (or sets, without a catalog) the pinned mimic version label.
	EnvMimicVersion = "YAOG_MIMIC_VERSION"
)

// FetchSettingsFromEnv resolves the air-gap catalog inputs from the environment. It is the local
// API's entry (the CLI layers flags over the same three inputs and calls LoadFetchSettings).
func FetchSettingsFromEnv() (FetchSettings, error) {
	return LoadFetchSettings(os.Getenv(EnvArtifactCatalog), os.Getenv(EnvGithubProxy), os.Getenv(EnvMimicVersion))
}

// LoadFetchSettings builds a FetchSettings from an optional artifact-catalog file plus a GitHub
// proxy and a mimic-version override. When all three are empty it returns the ZERO FetchSettings,
// so the bundle stays byte-identical (D4). The catalog file is parsed with the same struct the
// export path emits, so a controller-emitted artifacts.json round-trips as a valid catalog; a
// catalog stamped with a newer schema than this build understands is rejected (fail-closed,
// mirroring the model-validation forward-compat guard).
func LoadFetchSettings(catalogPath, githubProxy, mimicVersion string) (FetchSettings, error) {
	var fs FetchSettings
	if catalogPath != "" {
		data, err := os.ReadFile(catalogPath)
		if err != nil {
			return FetchSettings{}, fmt.Errorf("reading artifact catalog %q: %w", catalogPath, err)
		}
		var af artifactsFile
		if err := json.Unmarshal(data, &af); err != nil {
			return FetchSettings{}, fmt.Errorf("parsing artifact catalog %q: %w", catalogPath, err)
		}
		// Forward-compat: an absent/0 schema is tolerated (hand-written catalogs may omit it);
		// a schema newer than this build is refused rather than silently misread.
		if af.Schema > artifactsFileSchema {
			return FetchSettings{}, fmt.Errorf("artifact catalog %q has schema %d, newer than supported %d; upgrade YAOG to use it", catalogPath, af.Schema, artifactsFileSchema)
		}
		fs.MimicVersion = af.Mimic.Version
		fs.MimicReleaseBase = af.Mimic.ReleaseURL
		fs.MimicDebs = af.Mimic.Debs
		// The agent self-update block is reserved (plan-9); when it carries fields, map them here.
	}
	// Overrides layered on top of the catalog (a deployment-specific proxy, a version bump).
	if githubProxy != "" {
		fs.GithubProxy = githubProxy
	}
	if mimicVersion != "" {
		fs.MimicVersion = mimicVersion
	}
	return fs, nil
}
