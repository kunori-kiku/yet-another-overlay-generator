package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

type releaseAssetFixture struct {
	Name   string `json:"name"`
	Size   int    `json:"size"`
	Digest string `json:"digest"`
	State  string `json:"state"`
}

func TestGitHubReleaseVerifierExactAndRecoveryStates(t *testing.T) {
	root := t.TempDir()
	assetsDir := filepath.Join(root, "assets")
	fixtureDir := filepath.Join(root, "fixture")
	mockDir := filepath.Join(root, "bin")
	for _, dir := range []string{assetsDir, fixtureDir, mockDir} {
		if err := os.Mkdir(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}

	assets := make([]releaseAssetFixture, 0, 22)
	for i := 0; i < 22; i++ {
		name := "asset-" + string(rune('a'+i))
		body := []byte("fixture-" + name)
		if err := os.WriteFile(filepath.Join(assetsDir, name), body, 0o600); err != nil {
			t.Fatal(err)
		}
		digest := sha256.Sum256(body)
		assets = append(assets, releaseAssetFixture{
			Name: name, Size: len(body), Digest: "sha256:" + hex.EncodeToString(digest[:]), State: "uploaded",
		})
	}
	writeJSONFixture(t, filepath.Join(fixtureDir, "assets.json"), assets)
	writeJSONFixture(t, filepath.Join(fixtureDir, "release.json"), map[string]any{
		"id": 42, "tag_name": "v9.9.9-rc.7", "draft": true, "prerelease": false,
	})
	writeJSONFixture(t, filepath.Join(fixtureDir, "releases.json"), []map[string]any{{
		"id": 42, "tag_name": "v9.9.9-rc.7", "draft": true, "prerelease": false,
	}})
	writeJSONFixture(t, filepath.Join(fixtureDir, "latest.json"), map[string]any{"tag_name": "v9.9.9-rc.7"})
	if err := os.WriteFile(filepath.Join(fixtureDir, "patch-count"), []byte("0\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	mock := `#!/usr/bin/env bash
set -euo pipefail
[[ $1 == api ]]
shift
args=" $* "
if [[ $args == *' --method PATCH '* && $args == *'/releases/42 '* ]]; then
  prerelease=false
  [[ $args == *' -F prerelease=true '* ]] && prerelease=true
  jq --argjson prerelease "$prerelease" '.draft = false | .prerelease = $prerelease' \
    "$GH_FIXTURE/release.json" >"$GH_FIXTURE/release.next"
  mv "$GH_FIXTURE/release.next" "$GH_FIXTURE/release.json"
  if [[ $args == *' -f make_latest=true '* ]]; then
    jq -n --arg tag "$(jq -r .tag_name "$GH_FIXTURE/release.json")" '{tag_name: $tag}' \
      >"$GH_FIXTURE/latest.next"
    mv "$GH_FIXTURE/latest.next" "$GH_FIXTURE/latest.json"
  elif [[ $args == *' -f make_latest=false '* ]]; then
    printf '{"tag_name":"v9.9.8"}\n' >"$GH_FIXTURE/latest.json"
  else
    echo "PATCH omitted make_latest" >&2
    exit 1
  fi
  count=$(<"$GH_FIXTURE/patch-count")
  printf '%s\n' "$((count + 1))" >"$GH_FIXTURE/patch-count"
  cat "$GH_FIXTURE/release.json"
elif [[ $args == *'/releases?per_page=100'* ]]; then
  jq -c '.[]' "$GH_FIXTURE/releases.json"
elif [[ $args == *'/releases/42/assets?per_page=100'* ]]; then
  cat "$GH_FIXTURE/assets.json"
elif [[ $args == *'/releases/latest'* ]]; then
  if [[ $args == *' --jq .tag_name '* ]]; then jq -r .tag_name "$GH_FIXTURE/latest.json"; else cat "$GH_FIXTURE/latest.json"; fi
elif [[ $args == *'/releases/42'* ]]; then
  cat "$GH_FIXTURE/release.json"
else
  echo "unexpected mock gh invocation: $args" >&2
  exit 1
fi
`
	mockPath := filepath.Join(mockDir, "gh")
	if err := os.WriteFile(mockPath, []byte(mock), 0o700); err != nil {
		t.Fatal(err)
	}

	run := func(arguments ...string) ([]byte, error) {
		command := exec.Command("bash", append([]string{"verify-github-release.sh"}, arguments...)...)
		command.Env = append(os.Environ(), "PATH="+mockDir+":"+os.Getenv("PATH"), "GH_FIXTURE="+fixtureDir)
		return command.CombinedOutput()
	}

	// A partial same-byte draft is an allowed failed-upload recovery state.
	writeJSONFixture(t, filepath.Join(fixtureDir, "assets.json"), assets[:1])
	output, err := run("preflight-upload", "owner/repo", "v9.9.9-rc.7", "false", assetsDir)
	if err != nil || !strings.Contains(string(output), "release_status=draft") {
		t.Fatalf("preflight recovery: %v\n%s", err, output)
	}

	// The ordinary first publication path seals a complete private draft and
	// transitions it to public + Latest exactly once.
	writeJSONFixture(t, filepath.Join(fixtureDir, "assets.json"), assets)
	writeJSONFixture(t, filepath.Join(fixtureDir, "latest.json"), map[string]any{"tag_name": "v9.9.8"})
	if output, err = run("publish", "owner/repo", "42", "v9.9.9-rc.7", "false", assetsDir, "must"); err != nil {
		t.Fatalf("draft publication: %v\n%s", err, output)
	}
	patchCount, err := os.ReadFile(filepath.Join(fixtureDir, "patch-count"))
	if err != nil || strings.TrimSpace(string(patchCount)) != "1" {
		t.Fatalf("patch count after draft publication = %q, %v", patchCount, err)
	}
	if err := os.WriteFile(filepath.Join(fixtureDir, "patch-count"), []byte("0\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	// Public reuse is rejected before the upload action can be called.
	writeJSONFixture(t, filepath.Join(fixtureDir, "release.json"), map[string]any{
		"id": 42, "tag_name": "v9.9.9-rc.7", "draft": false, "prerelease": false,
	})
	writeJSONFixture(t, filepath.Join(fixtureDir, "releases.json"), []map[string]any{{
		"id": 42, "tag_name": "v9.9.9-rc.7", "draft": false, "prerelease": false,
	}})
	if output, err = run("preflight-upload", "owner/repo", "v9.9.9-rc.7", "false", assetsDir); err == nil {
		t.Fatalf("public preflight unexpectedly passed:\n%s", output)
	}

	// A retry that finds an already-public RC off Latest must re-seal it and
	// converge Latest. Once converged, another retry must be a no-op so immutable
	// public-release configurations are not needlessly mutated.
	writeJSONFixture(t, filepath.Join(fixtureDir, "assets.json"), assets)
	writeJSONFixture(t, filepath.Join(fixtureDir, "latest.json"), map[string]any{"tag_name": "v9.9.8"})
	if output, err = run("publish", "owner/repo", "42", "v9.9.9-rc.7", "false", assetsDir, "must"); err != nil {
		t.Fatalf("public recovery publish: %v\n%s", err, output)
	}
	patchCount, err = os.ReadFile(filepath.Join(fixtureDir, "patch-count"))
	if err != nil || strings.TrimSpace(string(patchCount)) != "1" {
		t.Fatalf("patch count after recovery = %q, %v", patchCount, err)
	}
	if output, err = run("publish", "owner/repo", "42", "v9.9.9-rc.7", "false", assetsDir, "must"); err != nil {
		t.Fatalf("idempotent public publish: %v\n%s", err, output)
	}
	patchCount, err = os.ReadFile(filepath.Join(fixtureDir, "patch-count"))
	if err != nil || strings.TrimSpace(string(patchCount)) != "1" {
		t.Fatalf("idempotent retry patch count = %q, %v", patchCount, err)
	}

	// Exact published metadata/assets/Latest pass; one changed digest fails.
	if output, err = run("verify", "owner/repo", "42", "v9.9.9-rc.7", "false", "false", assetsDir, "must"); err != nil {
		t.Fatalf("exact published verification: %v\n%s", err, output)
	}
	mutated := append([]releaseAssetFixture(nil), assets...)
	mutated[0].Digest = "sha256:" + strings.Repeat("0", 64)
	writeJSONFixture(t, filepath.Join(fixtureDir, "assets.json"), mutated)
	if output, err = run("verify", "owner/repo", "42", "v9.9.9-rc.7", "false", "false", assetsDir, "must"); err == nil {
		t.Fatalf("different digest unexpectedly passed:\n%s", output)
	}
}

func writeJSONFixture(t *testing.T, path string, value any) {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}
