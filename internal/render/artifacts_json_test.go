package render

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestBuildArtifactsJSON_AgentBlockPerNode pins the plan-9 per-node gating: the agent self-update
// block is emitted ONLY for a node in the rollout set, the mimic block stays fleet-wide, and the
// floor key is .agent.min_version (never a bare top-level key).
func TestBuildArtifactsJSON_AgentBlockPerNode(t *testing.T) {
	fs := FetchSettings{
		AgentVersion:        "1.2.0",
		AgentMinVersion:     "1.1.0",
		AgentReleaseBase:    "https://example/dl",
		AgentBins:           map[string]Artifact{"linux-amd64": {Asset: "yaog-agent-linux-amd64", SHA256: "ab"}},
		AgentRolloutNodeIDs: map[string]bool{"canary": true},
	}

	// Rollout node: agent block present with the version + min_version under .agent.
	got, err := buildArtifactsJSON(fs, "canary")
	if err != nil {
		t.Fatalf("buildArtifactsJSON(canary): %v", err)
	}
	var parsed struct {
		Agent struct {
			Version    string `json:"version"`
			MinVersion string `json:"min_version"`
			ReleaseURL string `json:"release_url"`
		} `json:"agent"`
	}
	if err := json.Unmarshal([]byte(got), &parsed); err != nil {
		t.Fatalf("parse canary artifacts.json: %v", err)
	}
	if parsed.Agent.Version != "1.2.0" || parsed.Agent.MinVersion != "1.1.0" || parsed.Agent.ReleaseURL != "https://example/dl" {
		t.Errorf("rollout node agent block wrong: %+v", parsed.Agent)
	}

	// Non-rollout node, no mimic catalog: NOTHING (export omits the file → byte-identity).
	if out, _ := buildArtifactsJSON(fs, "other"); out != "" {
		t.Errorf("non-rollout node with no mimic must get no artifacts.json; got %q", out)
	}

	// Add a fleet-wide mimic catalog: the non-rollout node now gets a mimic-ONLY artifacts.json
	// (empty agent block), byte-shape-identical to plan-3.
	fs.MimicVersion = "v1.4.0"
	fs.MimicReleaseBase = "https://example/mimic"
	fs.MimicDebs = map[string]MimicDebPin{"bookworm-amd64": {Asset: "m.deb", SHA256: "cd"}}
	mo, err := buildArtifactsJSON(fs, "other")
	if err != nil {
		t.Fatalf("buildArtifactsJSON(other, mimic): %v", err)
	}
	var mp struct {
		Mimic struct {
			Version string `json:"version"`
		} `json:"mimic"`
		Agent struct {
			Version string `json:"version"`
		} `json:"agent"`
	}
	if err := json.Unmarshal([]byte(mo), &mp); err != nil {
		t.Fatalf("parse other artifacts.json: %v", err)
	}
	if mp.Mimic.Version != "v1.4.0" {
		t.Errorf("non-rollout node must still get the fleet-wide mimic block; got %+v", mp.Mimic)
	}
	if mp.Agent.Version != "" {
		t.Errorf("non-rollout node must get an EMPTY agent block; got version %q", mp.Agent.Version)
	}
}

// TestBuildArtifactsJSON_MimicDkmsCompanion pins the two-package mimic catalog shape: a MimicDebPin
// with a dkms companion serializes to .mimic.debs[k] = {asset, sha256, dkms_asset, dkms_sha256} under
// the bumped schema, while a legacy mimic-only pin omits the dkms_* keys (omitempty) so an old
// {asset,sha256}-only catalog stays byte-clean and round-trips.
func TestBuildArtifactsJSON_MimicDkmsCompanion(t *testing.T) {
	withDkms := FetchSettings{
		MimicVersion:     "v0.7.1",
		MimicReleaseBase: "https://example/mimic",
		MimicDebs:        map[string]MimicDebPin{"bookworm-amd64": {Asset: "m.deb", SHA256: "aa", DKMSAsset: "m-dkms.deb", DKMSSHA256: "bb"}},
	}
	got, err := buildArtifactsJSON(withDkms, "any")
	if err != nil {
		t.Fatalf("buildArtifactsJSON(withDkms): %v", err)
	}
	var parsed struct {
		Schema int `json:"schema"`
		Mimic  struct {
			Debs map[string]struct {
				Asset      string `json:"asset"`
				SHA256     string `json:"sha256"`
				DKMSAsset  string `json:"dkms_asset"`
				DKMSSHA256 string `json:"dkms_sha256"`
			} `json:"debs"`
		} `json:"mimic"`
	}
	if err := json.Unmarshal([]byte(got), &parsed); err != nil {
		t.Fatalf("parse artifacts.json: %v", err)
	}
	if parsed.Schema != artifactsFileSchema {
		t.Errorf("schema = %d, want %d (the two-package bump)", parsed.Schema, artifactsFileSchema)
	}
	if p := parsed.Mimic.Debs["bookworm-amd64"]; p.Asset != "m.deb" || p.SHA256 != "aa" || p.DKMSAsset != "m-dkms.deb" || p.DKMSSHA256 != "bb" {
		t.Errorf("companion pin wrong: %+v", p)
	}

	// A legacy mimic-only pin (no companion) must NOT emit the dkms_ keys at all (omitempty).
	mimicOnly := FetchSettings{
		MimicVersion:     "v0.7.1",
		MimicReleaseBase: "https://example/mimic",
		MimicDebs:        map[string]MimicDebPin{"bookworm-amd64": {Asset: "m.deb", SHA256: "aa"}},
	}
	gotOnly, err := buildArtifactsJSON(mimicOnly, "any")
	if err != nil {
		t.Fatalf("buildArtifactsJSON(mimicOnly): %v", err)
	}
	if strings.Contains(gotOnly, "dkms_") {
		t.Errorf("mimic-only pin must omit dkms_ keys (omitempty); got:\n%s", gotOnly)
	}
}
