package renderer

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
)

func TestRenderDeployScripts_BashArchivePreflight(t *testing.T) {
	for _, command := range []string{"bash", "unzip", "awk", "find"} {
		if _, err := exec.LookPath(command); err != nil {
			t.Skipf("%s unavailable", command)
		}
	}
	bash, err := renderBashDeploy(DeployScriptConfig{ProjectName: "archive preflight"})
	if err != nil {
		t.Fatal(err)
	}
	script := writeGeneratedShell(t, bash)
	tests := []struct {
		name    string
		entries []psZipEntry
		ok      bool
	}{
		{name: "ordinary generated shape", entries: []psZipEntry{{name: "node-1/install.sh", body: "#!/bin/sh\n"}}, ok: true},
		{name: "parent traversal", entries: []psZipEntry{{name: "../escape", body: "bad"}}},
		{name: "dot alias", entries: []psZipEntry{{name: "node-1/./install.sh", body: "bad"}}},
		{name: "backslash alias", entries: []psZipEntry{{name: `node-1\install.sh`, body: "bad"}}},
		{name: "case collision", entries: []psZipEntry{{name: "Node/install.sh", body: "one"}, {name: "node/install.sh", body: "two"}}},
		{name: "exact duplicate", entries: []psZipEntry{{name: "node/install.sh", body: "one"}, {name: "node/install.sh", body: "two"}}},
		{name: "Unix symlink", entries: []psZipEntry{{name: "node/install.sh", body: "/etc/passwd", mode: os.ModeSymlink | 0o777}}},
		{name: "too many entries", entries: deployArchiveEntries(513, 0)},
		{name: "oversized member", entries: deployArchiveEntries(1, 4*1024*1024+1)},
		{name: "oversized expansion", entries: deployArchiveEntries(5, 4*1024*1024)},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			archive := writePowerShellTestZip(t, tc.entries)
			cmd := exec.Command("bash", script, archive)
			out, runErr := cmd.CombinedOutput()
			if tc.ok && runErr != nil {
				t.Fatalf("safe archive rejected: %v\n%s", runErr, out)
			}
			if !tc.ok && runErr == nil {
				t.Fatalf("unsafe archive accepted:\n%s", out)
			}
			if !tc.ok && !strings.Contains(string(out), "ZIP") {
				t.Fatalf("unsafe archive failure was not explanatory:\n%s", out)
			}
		})
	}
}

func deployArchiveEntries(count, bodyBytes int) []psZipEntry {
	body := ""
	if bodyBytes > 0 {
		body = strings.Repeat("x", bodyBytes)
	}
	entries := make([]psZipEntry, 0, count)
	for i := 0; i < count; i++ {
		entries = append(entries, psZipEntry{name: fmt.Sprintf("node/file-%04d", i), body: body})
	}
	return entries
}

func writeGeneratedShell(t *testing.T, body string) string {
	t.Helper()
	f, err := os.CreateTemp(t.TempDir(), "deploy-*.sh")
	if err != nil {
		t.Fatal(err)
	}
	path := f.Name()
	if _, err := f.WriteString(body); err != nil {
		t.Fatal(err)
	}
	if err := f.Chmod(0o700); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}
