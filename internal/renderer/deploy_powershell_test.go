package renderer

import (
	"archive/zip"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func powerShellForDeployTest(t *testing.T) string {
	t.Helper()
	pwsh, err := exec.LookPath("pwsh")
	if err != nil {
		if os.Getenv("CI") != "" {
			t.Fatal("pwsh is required in CI for the generated deploy-all.ps1 contract")
		}
		t.Skip("pwsh is not installed; CI enforces this generated-script contract")
	}
	return pwsh
}

func writeGeneratedPS1(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "deploy-all.ps1")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestRenderDeployScripts_PowerShellParses(t *testing.T) {
	pwsh := powerShellForDeployTest(t)
	topo, peerMap, babelConfigs := deployGoldenTopology()
	_, ps1, err := RenderDeployScripts(topo, peerMap, babelConfigs)
	if err != nil {
		t.Fatal(err)
	}
	path := writeGeneratedPS1(t, ps1)
	parse := `$tokens = $null; $parseErrors = $null; ` +
		`[System.Management.Automation.Language.Parser]::ParseFile($env:YAOG_PS1_PATH, [ref]$tokens, [ref]$parseErrors) > $null; ` +
		`if ($parseErrors.Count -ne 0) { $parseErrors | ForEach-Object { Write-Error $_.Message }; exit 1 }`
	cmd := exec.Command(pwsh, "-NoLogo", "-NoProfile", "-NonInteractive", "-Command", parse)
	cmd.Env = append(os.Environ(), "YAOG_PS1_PATH="+path)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("generated PowerShell did not parse: %v\n%s", err, out)
	}
}

type psZipEntry struct {
	name string
	body string
	mode os.FileMode
}

func writePowerShellTestZip(t *testing.T, entries []psZipEntry) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "artifacts.zip")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(f)
	for _, entry := range entries {
		h := &zip.FileHeader{Name: entry.name, Method: zip.Deflate}
		if entry.mode != 0 {
			h.SetMode(entry.mode)
		}
		w, err := zw.CreateHeader(h)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(entry.body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestRenderDeployScripts_PowerShellArchivePreflight(t *testing.T) {
	pwsh := powerShellForDeployTest(t)
	ps1, err := renderPS1Deploy(DeployScriptConfig{ProjectName: "archive preflight"})
	if err != nil {
		t.Fatal(err)
	}
	script := writeGeneratedPS1(t, ps1)

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
			cmd := exec.Command(pwsh, "-NoLogo", "-NoProfile", "-NonInteractive", "-File", script, "-ArtifactsZip", archive)
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

func TestRenderDeployScripts_PowerShellLargeUninstallUsesFileTransport(t *testing.T) {
	config := DeployScriptConfig{ProjectName: "large", Nodes: []DeployNodeInfo{{
		NodeID: "node-1", NodeName: "node-1", SSHTarget: "root@example.test", HasSSH: true,
	}}}
	for i := 0; i < 2000; i++ {
		config.Nodes[0].WgInterfaces = append(config.Nodes[0].WgInterfaces, fmt.Sprintf("wg-%04d", i))
	}
	ps1, err := renderPS1Deploy(config)
	if err != nil {
		t.Fatal(err)
	}
	if len(ps1) < 32_767 {
		t.Fatalf("test did not exceed the Windows command-line ceiling: len=%d", len(ps1))
	}
	for _, want := range []string{"[System.IO.File]::WriteAllBytes", "$RemoteDir + \"/uninstall.sh\"", "$UninstallTemp $ScpDestination"} {
		if !strings.Contains(ps1, want) {
			t.Fatalf("large uninstall is missing file-transport marker %q", want)
		}
	}
	if strings.Contains(ps1, "ToBase64String($uninstallBytes)") || strings.Contains(ps1, "$uninstallScript | & ssh") {
		t.Fatal("large uninstall still transports its body through a command-line argument or text pipeline")
	}
}
