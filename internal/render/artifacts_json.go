package render

import (
	"encoding/json"

	"github.com/kunorikiku/yet-another-overlay-generator/internal/renderer"
)

// artifactsFileSchema is the schema version stamped into every emitted artifacts.json.
const artifactsFileSchema = 1

// artifactsFile is the controller-signed artifacts.json carried as a bundleFiles member
// (internal/artifacts/export.go). It pins the EXTERNAL artifacts a node may fetch — the mimic
// GitHub-.deb catalog (plan-3) and, reserved for plan-9, the agent self-update block — so a
// node verifies a download against a pin that rides the same Ed25519 signature + keystone
// binding as the rest of the bundle (no new trust primitive). The install.sh reads the mimic
// pin from this file ONLY after the bundle's integrity has been verified.
//
// It is marshaled with encoding/json (which sorts map keys), and carries no timestamps, so a
// re-compile of the same catalog is byte-identical — the keystone epoch is reused, not churned.
type artifactsFile struct {
	Schema int            `json:"schema"`
	Mimic  artifactsMimic `json:"mimic"`
	Agent  artifactsAgent `json:"agent"`
}

// artifactsMimic is the mimic GitHub-.deb catalog: the pinned release version, the release base
// URL the .deb is fetched from (the GitHub proxy is prepended at install time, not stored here),
// and the per-"<codename>-<arch>" asset + SHA-256 the installer verifies before dpkg.
type artifactsMimic struct {
	Version    string                       `json:"version,omitempty"`
	ReleaseURL string                       `json:"release_url,omitempty"`
	Debs       map[string]renderer.Artifact `json:"debs,omitempty"`
}

// artifactsAgent is the RESERVED agent self-update block, filled by plan-9. Empty marshals as
// "{}" so the schema is stable and plan-9 only adds fields.
type artifactsAgent struct{}

// hasCatalog reports whether fs configures any external artifact (mimic or agent). When false,
// buildArtifactsJSON returns "" and export emits no artifacts.json, so the air-gap bundle stays
// byte-identical to today (D4).
func hasCatalog(fs FetchSettings) bool {
	return fs.MimicVersion != "" || len(fs.MimicDebs) > 0 ||
		fs.AgentVersion != "" || len(fs.AgentBins) > 0
}

// buildArtifactsJSON serializes the artifacts.json content for fs, or returns "" when no catalog
// is configured (the D4 air-gap-omit). The content is fleet-wide in plan-3 (the same pins for
// every node); plan-9 makes the agent block per-node.
func buildArtifactsJSON(fs FetchSettings) (string, error) {
	if !hasCatalog(fs) {
		return "", nil
	}
	af := artifactsFile{
		Schema: artifactsFileSchema,
		Mimic: artifactsMimic{
			Version:    fs.MimicVersion,
			ReleaseURL: fs.MimicReleaseBase,
			Debs:       fs.MimicDebs,
		},
		Agent: artifactsAgent{},
	}
	b, err := json.MarshalIndent(af, "", "  ")
	if err != nil {
		return "", err
	}
	return string(b) + "\n", nil
}
