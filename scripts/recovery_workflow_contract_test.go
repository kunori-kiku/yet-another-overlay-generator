package main

import (
	"os"
	"strings"
	"testing"
)

func TestWithdrawnRC7CannotBeRecovered(t *testing.T) {
	workflow, err := os.ReadFile("../.github/workflows/recover-release.yml")
	if err != nil {
		t.Fatal(err)
	}
	source := string(workflow)
	for _, required := range []string{
		`if [ "$RELEASE_TAG" = v2.0.0-rc.7 ]; then`,
		`v2.0.0-rc.7 is withdrawn because its arm64 image contains an amd64 server`,
	} {
		if !strings.Contains(source, required) {
			t.Errorf("recovery workflow is missing withdrawn-rc.7 guard %q", required)
		}
	}
}
