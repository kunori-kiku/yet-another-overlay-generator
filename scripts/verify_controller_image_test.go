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
  case "$4" in
    *@sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb) architecture=amd64 ;;
    *@sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc) architecture=arm64 ;;
    *) echo "config inspection did not use an exact child: $*" >&2; exit 1 ;;
  esac
  if [[ $MOCK_MODE == wrong-child-config && $architecture == arm64 ]]; then
    architecture=amd64
  fi
  entrypoint=/usr/local/bin/yaog-server
  if [[ $MOCK_MODE == wrong-child-entrypoint && $architecture == arm64 ]]; then
    entrypoint=/bin/false
  fi
  printf '{"architecture":"%s","os":"linux","config":{"Entrypoint":["%s"],"Labels":{"org.opencontainers.image.revision":"%s","org.opencontainers.image.version":"%s"}}}\n' "$architecture" "$entrypoint" "$EXPECTED_REVISION" "$EXPECTED_VERSION"
elif [[ $1 == create && $2 == --platform ]]; then
  case "$3:$4" in
    linux/amd64:ghcr.io/owner/image:9.9.9-rc.7@sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb)
      printf '%064d\n' 1
      ;;
    linux/arm64:ghcr.io/owner/image:9.9.9-rc.7@sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc)
      printf '%064d\n' 2
      ;;
    *) echo "inspection did not use the exact child digest: $*" >&2; exit 1 ;;
  esac
elif [[ $1 == cp ]]; then
  case "$2" in
    0000000000000000000000000000000000000000000000000000000000000001:/usr/local/bin/yaog-server)
      marker=amd64
      ;;
    0000000000000000000000000000000000000000000000000000000000000002:/usr/local/bin/yaog-server)
      marker=arm64
      ;;
    *) echo "unexpected server extraction: $*" >&2; exit 1 ;;
  esac
  if [[ $MOCK_MODE == symlink-server && $marker == arm64 ]]; then
    ln -s /bin/sh "$3"
  else
    printf '%s\n' "$marker" >"$3"
    chmod 0755 "$3"
  fi
elif [[ $1 == rm && $2 == -f ]]; then
  printf '%s\n' "${@:3}" >>"$MOCK_CLEANUP_LOG"
  exit 0
elif [[ $1 == run ]]; then
  case "$4:$5:$6" in
    linux/amd64:ghcr.io/owner/image:9.9.9-rc.7@sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb:version) ;;
    linux/arm64:ghcr.io/owner/image:9.9.9-rc.7@sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc:version) ;;
    *) echo "runtime did not use the exact child digest: $*" >&2; exit 1 ;;
  esac
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
	od := `#!/usr/bin/env bash
set -euo pipefail
marker=$(<"${@: -1}")
magic=127
class=2
data=1
case "$marker" in
  amd64) machine=62 ;;
  arm64)
    machine=183
    case "$MOCK_MODE" in
      wrong-embedded-arch) machine=62 ;;
      non-elf) magic=0 ;;
      truncated-elf) printf '127 69 76 70\n'; exit 0 ;;
      wrong-elf-class) class=1 ;;
      wrong-elf-endian) data=2 ;;
    esac
    ;;
  *) echo "unexpected embedded binary marker: $marker" >&2; exit 1 ;;
esac
printf '%s 69 76 70 %s %s 1 0 0 0 0 0 0 0 0 0 2 0 %s 0\n' "$magic" "$class" "$data" "$machine"
`
	if err := os.WriteFile(filepath.Join(root, "od"), []byte(od), 0o700); err != nil {
		t.Fatal(err)
	}
	revision := strings.Repeat("1", 40)
	digest := "sha256:" + strings.Repeat("a", 64)
	cleanupLog := filepath.Join(root, "cleanup.log")
	run := func(mode string, arguments ...string) ([]byte, error) {
		command := exec.Command("bash", append([]string{"verify-controller-image.sh"}, arguments...)...)
		command.Env = append(os.Environ(),
			"PATH="+root+":"+os.Getenv("PATH"),
			"MOCK_MODE="+mode,
			"MOCK_CLEANUP_LOG="+cleanupLog,
			"EXPECTED_REVISION="+revision,
			"EXPECTED_VERSION=v9.9.9-rc.7",
		)
		return command.CombinedOutput()
	}
	assertCleanup := func(want ...string) {
		t.Helper()
		cleaned, err := os.ReadFile(cleanupLog)
		if err != nil {
			t.Fatalf("read cleanup log: %v", err)
		}
		if got := strings.Join(strings.Fields(string(cleaned)), ","); got != strings.Join(want, ",") {
			t.Fatalf("cleaned containers = %q, want %q", got, strings.Join(want, ","))
		}
	}
	containerAMD64 := strings.Repeat("0", 63) + "1"
	containerARM64 := strings.Repeat("0", 63) + "2"
	output, err := run("success", "ghcr.io/owner/image:9.9.9-rc.7", "v9.9.9-rc.7", revision, digest)
	if err != nil || strings.TrimSpace(string(output)) != digest {
		t.Fatalf("success: %v\n%s", err, output)
	}
	assertCleanup(containerAMD64, containerARM64)
	if output, err = run("success", "ghcr.io/owner/image:9.9.9-rc.7", "v9.9.9-rc.7", revision, "sha256:"+strings.Repeat("f", 64)); err == nil {
		t.Fatalf("digest mismatch unexpectedly passed:\n%s", output)
	}
	if err := os.Remove(cleanupLog); err != nil {
		t.Fatal(err)
	}
	if output, err = run("wrong-embedded-arch", "ghcr.io/owner/image:9.9.9-rc.7", "v9.9.9-rc.7", revision, digest); err == nil || !strings.Contains(string(output), "linux/arm64 embeds ELF e_machine 62, expected 183 (AArch64)") {
		t.Fatalf("wrong embedded architecture was not rejected: %v\n%s", err, output)
	}
	assertCleanup(containerAMD64, containerARM64)
	for _, test := range []struct {
		mode    string
		message string
	}{
		{mode: "non-elf", message: "linux/arm64 server is not an ELF executable"},
		{mode: "truncated-elf", message: "linux/arm64 server has a truncated ELF header"},
		{mode: "wrong-elf-class", message: "linux/arm64 server is not a little-endian ELF64 executable"},
		{mode: "wrong-elf-endian", message: "linux/arm64 server is not a little-endian ELF64 executable"},
	} {
		if output, err = run(test.mode, "ghcr.io/owner/image:9.9.9-rc.7", "v9.9.9-rc.7", revision, digest); err == nil || !strings.Contains(string(output), test.message) {
			t.Fatalf("%s was not rejected: %v\n%s", test.mode, err, output)
		}
	}
	if output, err = run("symlink-server", "ghcr.io/owner/image:9.9.9-rc.7", "v9.9.9-rc.7", revision, digest); err == nil || !strings.Contains(string(output), "linux/arm64 server is not a regular executable file") {
		t.Fatalf("symlinked server was not rejected: %v\n%s", err, output)
	}
	if output, err = run("wrong-child-config", "ghcr.io/owner/image:9.9.9-rc.7", "v9.9.9-rc.7", revision, digest); err == nil || !strings.Contains(string(output), "linux/arm64 child config has the wrong platform or server entrypoint") {
		t.Fatalf("wrong child config was not rejected: %v\n%s", err, output)
	}
	if output, err = run("wrong-child-entrypoint", "ghcr.io/owner/image:9.9.9-rc.7", "v9.9.9-rc.7", revision, digest); err == nil || !strings.Contains(string(output), "linux/arm64 child config has the wrong platform or server entrypoint") {
		t.Fatalf("wrong child entrypoint was not rejected: %v\n%s", err, output)
	}
	command := exec.Command("bash", "verify-controller-image.sh", "ghcr.io/owner/image:absent", "v9.9.9-rc.7", revision)
	command.Env = append(os.Environ(),
		"PATH="+root+":"+os.Getenv("PATH"),
		"MOCK_MODE=absent",
		"MOCK_CLEANUP_LOG="+cleanupLog,
		"EXPECTED_REVISION="+revision,
		"EXPECTED_VERSION=v9.9.9-rc.7",
	)
	output, err = command.CombinedOutput()
	if exit, ok := err.(*exec.ExitError); !ok || exit.ExitCode() != 3 {
		t.Fatalf("absence exit = %v, output:\n%s", err, output)
	}
}
