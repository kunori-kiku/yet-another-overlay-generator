package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestControllerImageVerifierSuccessAbsenceAndDigestMismatch(t *testing.T) {
	root := t.TempDir()
	mock := `#!/usr/bin/env bash
set -euo pipefail
if [[ $MOCK_MODE == absent ]]; then
  echo "ERROR: $4: not found" >&2
  exit 1
fi
if [[ $1 == buildx && $2 == imagetools && $3 == inspect && $* == *'.Manifest'* ]]; then
  cat <<'JSON'
{"digest":"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","manifests":[{"digest":"sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","platform":{"os":"linux","architecture":"amd64"}},{"digest":"sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc","platform":{"os":"linux","architecture":"arm64"}},{"digest":"sha256:dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd","platform":{"os":"unknown","architecture":"unknown"}}]}
JSON
elif [[ $1 == buildx && $2 == imagetools && $3 == inspect && $* == *'.Image'* ]]; then
  printf '{"config":{"Labels":{"org.opencontainers.image.revision":"%s","org.opencontainers.image.version":"%s"}}}\n' "$EXPECTED_REVISION" "$EXPECTED_VERSION"
elif [[ $1 == run ]]; then
  printf '%s\n' "$EXPECTED_VERSION"
else
  echo "unexpected docker mock: $*" >&2
  exit 1
fi
`
	dockerPath := filepath.Join(root, "docker")
	if err := os.WriteFile(dockerPath, []byte(mock), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "sleep"), []byte("#!/usr/bin/env bash\nexit 0\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	revision := strings.Repeat("1", 40)
	digest := "sha256:" + strings.Repeat("a", 64)
	run := func(mode string, arguments ...string) ([]byte, error) {
		command := exec.Command("bash", append([]string{"verify-controller-image.sh"}, arguments...)...)
		command.Env = append(os.Environ(),
			"PATH="+root+":"+os.Getenv("PATH"),
			"MOCK_MODE="+mode,
			"EXPECTED_REVISION="+revision,
			"EXPECTED_VERSION=v9.9.9-rc.7",
		)
		return command.CombinedOutput()
	}
	output, err := run("success", "ghcr.io/owner/image:9.9.9-rc.7", "v9.9.9-rc.7", revision, digest)
	if err != nil || strings.TrimSpace(string(output)) != digest {
		t.Fatalf("success: %v\n%s", err, output)
	}
	if output, err = run("success", "ghcr.io/owner/image:9.9.9-rc.7", "v9.9.9-rc.7", revision, "sha256:"+strings.Repeat("f", 64)); err == nil {
		t.Fatalf("digest mismatch unexpectedly passed:\n%s", output)
	}
	command := exec.Command("bash", "verify-controller-image.sh", "ghcr.io/owner/image:absent", "v9.9.9-rc.7", revision)
	command.Env = append(os.Environ(),
		"PATH="+root+":"+os.Getenv("PATH"),
		"MOCK_MODE=absent",
		"EXPECTED_REVISION="+revision,
		"EXPECTED_VERSION=v9.9.9-rc.7",
	)
	output, err = command.CombinedOutput()
	if exit, ok := err.(*exec.ExitError); !ok || exit.ExitCode() != 3 {
		t.Fatalf("absence exit = %v, output:\n%s", err, output)
	}
}
