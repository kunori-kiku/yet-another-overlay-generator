package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestDockerWorkflowVerifiesRunCandidateBeforePublishingVersion(t *testing.T) {
	workflow, err := os.ReadFile("../.github/workflows/docker.yml")
	if err != nil {
		t.Fatal(err)
	}
	source := string(workflow)

	for _, required := range []string{
		`candidate_tag=candidate-${GITHUB_RUN_ID}-${GITHUB_RUN_ATTEMPT}`,
		`ghcr_candidate_ref=$GHCR_IMAGE:$candidate_tag`,
		`dockerhub_candidate_ref=$DOCKERHUB_USERNAME/yaog-controller:$candidate_tag`,
		`echo "candidate_tags<<EOF"`,
		`if [ -n "$dockerhub_candidate_ref" ]; then`,
		`echo "$dockerhub_candidate_ref"`,
		`echo "EOF"`,
		`tags: ${{ steps.refs.outputs.candidate_tags }}`,
		`- name: Verify every run-scoped candidate`,
		`"$GHCR_CANDIDATE_REF" "$RELEASE_VERSION" "$SOURCE_REVISION" "$BUILD_DIGEST"`,
		`"$DOCKERHUB_CANDIDATE_REF" "$RELEASE_VERSION" "$SOURCE_REVISION" "$BUILD_DIGEST"`,
		`- name: Adopt or publish official registry references`,
		`copy_ref "$GHCR_CANDIDATE_REF" "$GHCR_REF" "$canonical_digest"`,
		`copy_ref "$DOCKERHUB_CANDIDATE_REF" "$DOCKERHUB_REF" "$canonical_digest"`,
		`GHCR version ref $ghcr_digest differs from verified candidate $canonical_digest`,
	} {
		if !strings.Contains(source, required) {
			t.Errorf("Docker workflow is missing candidate-publication contract %q", required)
		}
	}

	build := strings.Index(source, "- name: Build and push run-scoped candidates")
	verify := strings.Index(source, "- name: Verify every run-scoped candidate")
	publish := strings.Index(source, "- name: Adopt or publish official registry references")
	if build < 0 || verify <= build || publish <= verify {
		t.Fatalf("Docker workflow order is build=%d verify=%d publish=%d, want build < verify < publish", build, verify, publish)
	}

	buildSection := source[build:verify]
	for _, forbidden := range []string{
		`${{ steps.refs.outputs.ghcr_ref }}`,
		`${{ steps.refs.outputs.dockerhub_ref }}`,
		`continue-on-error: true`,
	} {
		if strings.Contains(buildSection, forbidden) {
			t.Errorf("candidate build writes or tolerates failure through forbidden contract %q", forbidden)
		}
	}
}

func TestDockerComposeOffersUserNamespaceSafeNamedVolume(t *testing.T) {
	compose, err := os.ReadFile("../docker-compose.yml")
	if err != nil {
		t.Fatal(err)
	}
	source := string(compose)
	for _, required := range []string{
		`For ROOTFUL Docker without userns-remap`,
		`add YAOG_DATA_SOURCE=controller-data to .env`,
		`"${YAOG_DATA_SOURCE:-./data}:/data"`,
		"volumes:\n  # Portable state storage",
		"  controller-data:",
	} {
		if !strings.Contains(source, required) {
			t.Errorf("compose file is missing storage portability contract %q", required)
		}
	}
}

func TestDockerWorkflowResolveRefsOutputShape(t *testing.T) {
	workflow, err := os.ReadFile("../.github/workflows/docker.yml")
	if err != nil {
		t.Fatal(err)
	}
	script := workflowRunScript(t, string(workflow), "Resolve image references")

	tests := []struct {
		name              string
		dockerHubEnabled  string
		dockerHubUsername string
		want              string
	}{
		{
			name:             "ghcr only",
			dockerHubEnabled: "false",
			want: strings.Join([]string{
				"ghcr_ref=ghcr.io/acme/yaog-controller:2.0.0-rc.9",
				"ghcr_candidate_ref=ghcr.io/acme/yaog-controller:candidate-12345-2",
				"dockerhub_ref=",
				"dockerhub_candidate_ref=",
				"dockerhub_enabled=false",
				"candidate_tags<<EOF",
				"ghcr.io/acme/yaog-controller:candidate-12345-2",
				"EOF",
				"",
			}, "\n"),
		},
		{
			name:              "ghcr and docker hub",
			dockerHubEnabled:  "true",
			dockerHubUsername: "mirror_owner",
			want: strings.Join([]string{
				"ghcr_ref=ghcr.io/acme/yaog-controller:2.0.0-rc.9",
				"ghcr_candidate_ref=ghcr.io/acme/yaog-controller:candidate-12345-2",
				"dockerhub_ref=mirror_owner/yaog-controller:2.0.0-rc.9",
				"dockerhub_candidate_ref=mirror_owner/yaog-controller:candidate-12345-2",
				"dockerhub_enabled=true",
				"candidate_tags<<EOF",
				"ghcr.io/acme/yaog-controller:candidate-12345-2",
				"mirror_owner/yaog-controller:candidate-12345-2",
				"EOF",
				"",
			}, "\n"),
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			outputPath := t.TempDir() + "/github-output"
			command := exec.Command("bash", "-c", script)
			command.Env = append(os.Environ(),
				"RELEASE_VERSION=v2.0.0-rc.9",
				"GITHUB_RUN_ID=12345",
				"GITHUB_RUN_ATTEMPT=2",
				"GHCR_IMAGE=ghcr.io/acme/yaog-controller",
				"DOCKERHUB_ENABLED="+test.dockerHubEnabled,
				"DOCKERHUB_USERNAME="+test.dockerHubUsername,
				"GITHUB_OUTPUT="+outputPath,
			)
			if output, err := command.CombinedOutput(); err != nil {
				t.Fatalf("resolve refs failed: %v\n%s", err, output)
			}
			got, err := os.ReadFile(outputPath)
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != test.want {
				t.Fatalf("resolved outputs:\n%s\nwant:\n%s", got, test.want)
			}
		})
	}
}

func TestDockerWorkflowModifiedShellBlocksParse(t *testing.T) {
	workflow, err := os.ReadFile("../.github/workflows/docker.yml")
	if err != nil {
		t.Fatal(err)
	}
	for _, stepName := range []string{
		"Resolve image references",
		"Probe policy-non-overwritten version references",
		"Verify every run-scoped candidate",
		"Adopt or publish official registry references",
	} {
		t.Run(stepName, func(t *testing.T) {
			command := exec.Command("bash", "-n")
			command.Stdin = strings.NewReader(workflowRunScript(t, string(workflow), stepName))
			if output, err := command.CombinedOutput(); err != nil {
				t.Fatalf("workflow shell block does not parse: %v\n%s", err, output)
			}
		})
	}
}

func TestDockerConvergeRetryRetainsExpectedDigest(t *testing.T) {
	workflow, err := os.ReadFile("../.github/workflows/docker.yml")
	if err != nil {
		t.Fatal(err)
	}
	script := workflowRunScript(t, string(workflow), "Adopt or publish official registry references")

	dir := t.TempDir()
	callsPath := filepath.Join(dir, "docker-calls")
	verifyCountPath := filepath.Join(dir, "verify-count")
	writeExecutable := func(name, body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	writeExecutable("docker", `#!/bin/sh
printf '%s\n' "$*" >>"$MOCK_DOCKER_CALLS"
`)
	writeExecutable("sleep", "#!/bin/sh\nexit 0\n")
	writeExecutable("bash", `#!/bin/sh
count=0
if [ -f "$MOCK_VERIFY_COUNT" ]; then count=$(cat "$MOCK_VERIFY_COUNT"); fi
count=$((count + 1))
printf '%s\n' "$count" >"$MOCK_VERIFY_COUNT"
# Fresh official ref probe: proven absent. First post-copy verification: transient failure.
# All later reads prove the exact expected digest.
case "$count" in
  1) exit 3 ;;
  2) exit 1 ;;
esac
printf '%s\n' "$5"
`)

	digest := "sha256:" + strings.Repeat("a", 64)
	outputPath := filepath.Join(dir, "github-output")
	command := exec.Command("/bin/bash", "-c", script)
	command.Env = append(os.Environ(),
		"PATH="+dir+":"+os.Getenv("PATH"),
		"MOCK_DOCKER_CALLS="+callsPath,
		"MOCK_VERIFY_COUNT="+verifyCountPath,
		"RELEASE_VERSION=v2.0.0-rc.9",
		"SOURCE_REVISION="+strings.Repeat("b", 40),
		"GHCR_REF=ghcr.io/acme/yaog-controller:2.0.0-rc.9",
		"DOCKERHUB_REF=",
		"GHCR_CANDIDATE_REF=ghcr.io/acme/yaog-controller:candidate-123-1",
		"DOCKERHUB_CANDIDATE_REF=",
		"PROBE_DIGEST=",
		"PROBE_SOURCE_REF=",
		"CANDIDATE_DIGEST="+digest,
		"GITHUB_OUTPUT="+outputPath,
	)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("converge retry failed: %v\n%s", err, output)
	}
	calls, err := os.ReadFile(callsPath)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.FieldsFunc(strings.TrimSpace(string(calls)), func(r rune) bool { return r == '\n' })
	if len(lines) != 2 {
		t.Fatalf("docker copy attempts = %d (%q), want two", len(lines), calls)
	}
	for _, line := range lines {
		if !strings.Contains(line, "candidate-123-1@"+digest) {
			t.Fatalf("retry lost its exact source digest: %q", line)
		}
	}
}

func TestRecoverReleaseRequiresCandidateBuildAndVerification(t *testing.T) {
	recovery, err := os.ReadFile("../.github/workflows/recover-release.yml")
	if err != nil {
		t.Fatal(err)
	}
	source := string(recovery)
	for _, required := range []string{
		`gh api --paginate --slurp`,
		`.[].jobs[]`,
		`.name == "Build and push run-scoped candidates" and .conclusion == "success"`,
		`.name == "Verify every run-scoped candidate" and .conclusion == "success"`,
		`for build_job_id in "${build_job_ids[@]}"; do`,
		`[ "$action_digest" = "$EXPECTED_GHCR_DIGEST" ]`,
		`[ "$converge_digest" = "$EXPECTED_GHCR_DIGEST" ]`,
	} {
		if !strings.Contains(source, required) {
			t.Errorf("recovery workflow is missing source candidate proof %q", required)
		}
	}
	if strings.Contains(source, `.name == "Build and push absent refs"`) {
		t.Fatal("recovery workflow still depends on the retired pre-candidate Docker step")
	}
	if strings.Contains(source, `if length == 1 then .[0].id`) {
		t.Fatal("recovery workflow still assumes exactly one candidate-producing job across reruns")
	}

	command := exec.Command("bash", "-n")
	command.Stdin = strings.NewReader(workflowRunScript(t, source, "Prove the source Release run and its complete prerequisite graph"))
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("recovery source-proof shell block does not parse: %v\n%s", err, output)
	}
}

func workflowRunScript(t *testing.T, workflow, stepName string) string {
	t.Helper()
	stepMarker := "      - name: " + stepName + "\n"
	stepStart := strings.Index(workflow, stepMarker)
	if stepStart < 0 {
		t.Fatalf("workflow has no step %q", stepName)
	}
	step := workflow[stepStart+len(stepMarker):]
	runMarker := "        run: |\n"
	runStart := strings.Index(step, runMarker)
	if runStart < 0 {
		t.Fatalf("workflow step %q has no block run script", stepName)
	}
	body := step[runStart+len(runMarker):]
	if end := strings.Index(body, "\n      - name:"); end >= 0 {
		body = body[:end]
	}
	lines := strings.Split(body, "\n")
	for i, line := range lines {
		if line == "" {
			continue
		}
		const indent = "          "
		if !strings.HasPrefix(line, indent) {
			t.Fatalf("workflow step %q has unexpected script indentation %q", stepName, line)
		}
		lines[i] = strings.TrimPrefix(line, indent)
	}
	return strings.Join(lines, "\n")
}
