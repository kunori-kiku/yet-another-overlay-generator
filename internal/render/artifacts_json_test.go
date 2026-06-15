package render

import (
	"encoding/json"
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
	fs.MimicDebs = map[string]Artifact{"bookworm-amd64": {Asset: "m.deb", SHA256: "cd"}}
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
