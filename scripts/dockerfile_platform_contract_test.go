package main

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

func TestControllerDockerfileInheritsAutomaticTargetPlatform(t *testing.T) {
	dockerfile, err := os.ReadFile("../Dockerfile")
	if err != nil {
		t.Fatal(err)
	}
	source := string(dockerfile)

	maskedTarget := regexp.MustCompile(`(?m)^\s*ARG\s+TARGET(?:OS|ARCH)\s*=`)
	if match := maskedTarget.FindString(source); match != "" {
		t.Fatalf("Dockerfile masks a BuildKit automatic target argument: %q", match)
	}

	for _, required := range []string{
		"FROM --platform=$BUILDPLATFORM node:20-alpine AS frontend",
		"FROM --platform=$BUILDPLATFORM golang:1.26-alpine AS backend",
		"ARG TARGETOS\nARG TARGETARCH",
		`test "${built_os}" = "${TARGETOS}"`,
		`test "${built_arch}" = "${TARGETARCH}"`,
	} {
		if !strings.Contains(source, required) {
			t.Errorf("Dockerfile is missing multi-platform build contract %q", required)
		}
	}
}
