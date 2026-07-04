package render

import (
	"encoding/json"

	"github.com/kunorikiku/yet-another-overlay-generator/internal/model"
)

// artifactsFileSchema is the schema version stamped into every emitted artifacts.json. Bumped 1→2
// when the mimic catalog gained the two-package `mimic-dkms` companion pin (.mimic.debs[k] now
// carries dkms_asset/dkms_sha256): the shape is additive (an old {asset,sha256}-only catalog still
// loads on a schema-2 binary — the loader guard rejects only schema > supported), but the bump lets
// an OLD binary reading a NEW catalog fail-closed instead of silently installing without the module.
const artifactsFileSchema = 2

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
	Debs       map[string]model.MimicDebPin `json:"debs,omitempty"`
}

// artifactsAgent is the agent self-update block (plan-9): the version the node should run, the
// floor below which it must update before applying a bundle, the release base URL the binary is
// fetched from (the GitHub proxy is prepended at runtime, not stored here), and the
// per-"linux-<arch>" binary asset + SHA-256 the agent verifies before exec. All fields omitempty,
// so a node NOT in the rollout marshals an empty "{}" agent block — byte-identical to plan-3's
// reserved-empty block. The floor's JSON key is min_version UNDER agent (.agent.min_version),
// never a bare top-level min_version, to avoid conflation with the bundle anti-rollback floor.
type artifactsAgent struct {
	Version    string                    `json:"version,omitempty"`
	MinVersion string                    `json:"min_version,omitempty"`
	ReleaseURL string                    `json:"release_url,omitempty"`
	Bins       map[string]model.Artifact `json:"bins,omitempty"`
}

// hasCatalog reports whether fs configures any external artifact at the FLEET level (mimic or
// agent). It answers the air-gap "is anything configured at all" question (used by the air-gap
// loader/tests); the per-NODE emit decision is buildArtifactsJSON's, which additionally gates the
// agent block on rollout membership.
func hasCatalog(fs FetchSettings) bool {
	return fs.MimicVersion != "" || len(fs.MimicDebs) > 0 ||
		fs.AgentVersion != "" || len(fs.AgentBins) > 0
}

// buildArtifactsJSON serializes the artifacts.json content for ONE node, or returns "" when that
// node gets neither a mimic nor an agent block (the D4 air-gap-omit / non-rollout case → export
// omits the file). The MIMIC block is fleet-wide (every node with a mimic catalog gets the same
// one); the AGENT block is PER-NODE — emitted only when a target version is set AND this node is
// in the rollout set (canary subset, or the whole fleet once promoted). A node that gets only the
// mimic block produces bytes identical to plan-3 (empty agent block "{}").
func buildArtifactsJSON(fs FetchSettings, nodeID string) (string, error) {
	hasMimic := fs.MimicVersion != "" || len(fs.MimicDebs) > 0
	hasAgent := fs.AgentVersion != "" && fs.AgentRolloutNodeIDs[nodeID]
	if !hasMimic && !hasAgent {
		return "", nil
	}
	af := artifactsFile{Schema: artifactsFileSchema}
	if hasMimic {
		af.Mimic = artifactsMimic{
			Version:    fs.MimicVersion,
			ReleaseURL: fs.MimicReleaseBase,
			Debs:       fs.MimicDebs,
		}
	}
	if hasAgent {
		af.Agent = artifactsAgent{
			Version:    fs.AgentVersion,
			MinVersion: fs.AgentMinVersion,
			ReleaseURL: fs.AgentReleaseBase,
			Bins:       fs.AgentBins,
		}
	}
	b, err := json.MarshalIndent(af, "", "  ")
	if err != nil {
		return "", err
	}
	return string(b) + "\n", nil
}
