package artifacts

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kunorikiku/yet-another-overlay-generator/internal/apierr"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/compiler"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/model"
)

func TestExport_BasicStructure(t *testing.T) {
	// Build a minimal compile result.
	result := &compiler.CompileResult{
		Topology: &model.Topology{
			Project: model.Project{ID: "test-001", Name: "Test", Version: "0.1.0"},
			Domains: []model.Domain{{ID: "d1", Name: "test", CIDR: "10.10.0.0/24", RoutingMode: "babel"}},
			Nodes: []model.Node{
				{ID: "n1", Name: "alpha", OverlayIP: "10.10.0.1", Role: "router", DomainID: "d1"},
				{ID: "n2", Name: "beta", OverlayIP: "10.10.0.2", Role: "peer", DomainID: "d1"},
			},
		},
		PeerMap: map[string][]compiler.PeerInfo{
			"n1": {{NodeID: "n2", NodeName: "beta"}},
			"n2": {{NodeID: "n1", NodeName: "alpha"}},
		},
		WireGuardConfigs: map[string]string{
			"n1:wg-beta":  "[Interface]\nPrivateKey = test\n",
			"n2:wg-alpha": "[Interface]\nPrivateKey = test\n",
		},
		BabelConfigs: map[string]string{
			"n1": "router-id alpha\n",
			"n2": "router-id beta\n",
		},
		SysctlConfigs: map[string]string{
			"n1": "net.ipv4.ip_forward = 1\n",
			"n2": "net.ipv4.conf.all.rp_filter = 2\n",
		},
		InstallScripts: map[string]string{
			"n1": "#!/bin/bash\necho install alpha\n",
			"n2": "#!/bin/bash\necho install beta\n",
		},
		Manifest: compiler.CompileManifest{
			ProjectID:   "test-001",
			ProjectName: "Test",
			Version:     "0.1.0",
			CompiledAt:  time.Now(),
			NodeCount:   2,
			Checksum:    "abc123",
		},
	}

	// Export to a temp directory.
	outputDir := t.TempDir()
	exportResult, err := Export(result, outputDir)
	if err != nil {
		t.Fatalf("Export failed: %v", err)
	}

	// Expect 2 nodes.
	if len(exportResult.Nodes) != 2 {
		t.Errorf("want 2 nodes, got %d", len(exportResult.Nodes))
	}

	// Verify each node's exported files.
	for _, node := range []struct{ id, name string }{{"n1", "alpha"}, {"n2", "beta"}} {
		nodeDir := filepath.Join(outputDir, node.id)

		// per-peer architecture: each node's wireguard directory contains the interface config for its peer
		var expectedFiles []string
		if node.name == "alpha" {
			expectedFiles = []string{
				"wireguard/wg-beta.conf",
				"babel/babeld.conf",
				"sysctl/99-overlay.conf",
				"install.sh",
				"manifest.json",
				"README.txt",
			}
		} else {
			expectedFiles = []string{
				"wireguard/wg-alpha.conf",
				"babel/babeld.conf",
				"sysctl/99-overlay.conf",
				"install.sh",
				"manifest.json",
				"README.txt",
			}
		}

		for _, f := range expectedFiles {
			path := filepath.Join(nodeDir, f)
			if _, err := os.Stat(path); os.IsNotExist(err) {
				t.Errorf("node %s missing expected file: %s", node.name, f)
			}
		}
	}
}

func TestExport_RejectsCaseCollidingNodeDirectories(t *testing.T) {
	result := minimalCompileResult()
	second := result.Topology.Nodes[0]
	result.Topology.Nodes[0].ID = "Node-East"
	second.ID = "node-east"
	result.Topology.Nodes = append(result.Topology.Nodes, second)
	if _, err := Export(result, t.TempDir()); !apierr.HasCode(err, apierr.CodeExportUnsafeName) {
		t.Fatalf("case-colliding export error = %v, want %s", err, apierr.CodeExportUnsafeName)
	}
}

func TestExport_RejectsUnexpectedProjectHelperPath(t *testing.T) {
	result := minimalCompileResult()
	result.DeployScripts = map[string]string{"../deploy-all.sh": "bad"}
	if _, err := Export(result, t.TempDir()); !apierr.HasCode(err, apierr.CodeExportUnsafeName) {
		t.Fatalf("unsafe helper export error = %v, want %s", err, apierr.CodeExportUnsafeName)
	}
}

func TestExport_RejectsNonCanonicalBundleMemberPath(t *testing.T) {
	result := minimalCompileResult()
	result.WireGuardConfigs = map[string]string{"n1:../../escape": "secret"}
	output := t.TempDir()
	if _, err := Export(result, output); !apierr.HasCode(err, apierr.CodeExportUnsafeName) {
		t.Fatalf("unsafe member export error = %v, want %s", err, apierr.CodeExportUnsafeName)
	}
	if _, err := os.Stat(filepath.Join(output, "escape.conf")); !os.IsNotExist(err) {
		t.Fatalf("unsafe member escaped its node directory (err=%v)", err)
	}
}

func TestExport_WireGuardPermissions(t *testing.T) {
	result := minimalCompileResult()
	outputDir := t.TempDir()

	_, err := Export(result, outputDir)
	if err != nil {
		t.Fatalf("Export failed: %v", err)
	}

	// WireGuard configs must be 0600.
	wgPath := filepath.Join(outputDir, "n1", "wireguard", "wg-beta.conf")
	info, err := os.Stat(wgPath)
	if err != nil {
		t.Fatalf("failed to stat wireguard config: %v", err)
	}

	perm := info.Mode().Perm()
	if perm != 0600 {
		t.Errorf("WireGuard config should be 0600, got %o", perm)
	}
}

func TestExport_WireGuardPermissionsTightenedOnReplacement(t *testing.T) {
	result := minimalCompileResult()
	outputDir := t.TempDir()
	wgDir := filepath.Join(outputDir, "n1", "wireguard")
	if err := os.MkdirAll(wgDir, 0755); err != nil {
		t.Fatal(err)
	}
	wgPath := filepath.Join(wgDir, "wg-beta.conf")
	if err := os.WriteFile(wgPath, []byte("old private key material\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := Export(result, outputDir); err != nil {
		t.Fatalf("Export failed: %v", err)
	}
	info, err := os.Stat(wgPath)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0600 {
		t.Fatalf("replaced WireGuard config mode = %o, want 600", got)
	}
}

func TestExportPublishesExactTreeAndRemovesStaleContent(t *testing.T) {
	keyPath, _ := writeTestSigningKey(t)
	t.Setenv("YAOG_BUNDLE_SIGNING_KEY", keyPath)
	outputDir := t.TempDir()
	if _, err := Export(newTestResult(), outputDir); err != nil {
		t.Fatalf("signed seed export: %v", err)
	}
	if err := os.WriteFile(filepath.Join(outputDir, "stale-root.txt"), []byte("stale"), 0644); err != nil {
		t.Fatalf("seed stale root file: %v", err)
	}

	t.Setenv("YAOG_BUNDLE_SIGNING_KEY", "")
	if _, err := Export(minimalCompileResult(), outputDir); err != nil {
		t.Fatalf("replacement export: %v", err)
	}
	for _, stale := range []string{
		"stale-root.txt",
		filepath.Join("n2", "manifest.json"),
		filepath.Join("n1", "bundle.sig"),
		filepath.Join("n1", "signing-pubkey.pem"),
	} {
		if _, err := os.Lstat(filepath.Join(outputDir, stale)); !os.IsNotExist(err) {
			t.Fatalf("stale export member %q survived exact-tree replacement (err=%v)", stale, err)
		}
	}
	if _, err := os.Stat(filepath.Join(outputDir, "n1", "manifest.json")); err != nil {
		t.Fatalf("replacement export missing current node: %v", err)
	}
}

func TestExportReportsBackupCleanupFailureAsCommittedWarning(t *testing.T) {
	outputDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(outputDir, "prior-only"), []byte("prior"), 0600); err != nil {
		t.Fatal(err)
	}
	originalRemove := removeExportBackup
	var backupPath string
	removeExportBackup = func(path string) error {
		backupPath = path
		return errors.New("injected cleanup failure")
	}
	t.Cleanup(func() {
		removeExportBackup = originalRemove
		if backupPath != "" {
			_ = os.RemoveAll(backupPath)
		}
	})

	result, err := Export(minimalCompileResult(), outputDir)
	if err != nil {
		t.Fatalf("post-commit cleanup must not masquerade as export failure: %v", err)
	}
	if len(result.CleanupWarnings) != 1 || !strings.Contains(result.CleanupWarnings[0], "new export committed") {
		t.Fatalf("CleanupWarnings = %#v, want one committed-result warning", result.CleanupWarnings)
	}
	if _, err := os.Lstat(filepath.Join(outputDir, "prior-only")); !os.IsNotExist(err) {
		t.Fatalf("new exact tree was not published (prior-only err=%v)", err)
	}
	if _, err := os.Stat(filepath.Join(outputDir, "n1", "manifest.json")); err != nil {
		t.Fatalf("committed tree missing manifest: %v", err)
	}
	info, err := os.Stat(backupPath)
	if err != nil {
		t.Fatalf("injected leftover backup is absent: %v", err)
	}
	if got := info.Mode().Perm(); got != 0700 {
		t.Fatalf("leftover backup mode = %04o, want 0700", got)
	}
}

func TestExportRejectsSymlinkDestinationWithoutTouchingTarget(t *testing.T) {
	parent := t.TempDir()
	target := filepath.Join(parent, "real-output")
	if err := os.Mkdir(target, 0700); err != nil {
		t.Fatalf("mkdir target: %v", err)
	}
	sentinel := filepath.Join(target, "sentinel")
	if err := os.WriteFile(sentinel, []byte("unchanged"), 0600); err != nil {
		t.Fatalf("write sentinel: %v", err)
	}
	link := filepath.Join(parent, "output-link")
	if err := os.Symlink(target, link); err != nil {
		t.Fatalf("symlink output: %v", err)
	}

	if _, err := Export(minimalCompileResult(), link); !apierr.HasCode(err, apierr.CodeExportUnsafeName) {
		t.Fatalf("symlink destination error = %v, want %s", err, apierr.CodeExportUnsafeName)
	}
	got, err := os.ReadFile(sentinel)
	if err != nil {
		t.Fatalf("read target sentinel: %v", err)
	}
	if string(got) != "unchanged" {
		t.Fatalf("symlink target changed to %q", got)
	}
}

func TestExportValidationFailureLeavesPriorTreeUntouched(t *testing.T) {
	outputDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(outputDir, "sentinel"), []byte("prior"), 0600); err != nil {
		t.Fatalf("seed prior tree: %v", err)
	}
	result := minimalCompileResult()
	late := result.Topology.Nodes[0]
	late.ID = "../late-invalid"
	result.Topology.Nodes = append(result.Topology.Nodes, late)

	if _, err := Export(result, outputDir); !apierr.HasCode(err, apierr.CodeExportUnsafeName) {
		t.Fatalf("late validation error = %v, want %s", err, apierr.CodeExportUnsafeName)
	}
	got, err := os.ReadFile(filepath.Join(outputDir, "sentinel"))
	if err != nil {
		t.Fatalf("prior tree disappeared after failed export: %v", err)
	}
	if string(got) != "prior" {
		t.Fatalf("prior tree changed to %q", got)
	}
	entries, err := os.ReadDir(outputDir)
	if err != nil {
		t.Fatalf("read prior tree: %v", err)
	}
	if len(entries) != 1 || entries[0].Name() != "sentinel" {
		t.Fatalf("failed export partially mutated prior tree: %v", entries)
	}
}

func TestExport_InstallScriptExecutable(t *testing.T) {
	result := minimalCompileResult()
	outputDir := t.TempDir()

	_, err := Export(result, outputDir)
	if err != nil {
		t.Fatalf("Export failed: %v", err)
	}

	// install.sh must be executable.
	scriptPath := filepath.Join(outputDir, "n1", "install.sh")
	info, err := os.Stat(scriptPath)
	if err != nil {
		t.Fatalf("failed to stat install.sh: %v", err)
	}

	perm := info.Mode().Perm()
	if perm&0100 == 0 {
		t.Errorf("install.sh should be executable, got mode: %o", perm)
	}
}

func TestExport_READMEIsCustodyAware(t *testing.T) {
	for _, tc := range []struct {
		name      string
		agentHeld bool
		want      string
		forbidden string
	}{
		{name: "airgap permits locally trusted direct install", want: "sudo bash install.sh", forbidden: "kit apply"},
		{name: "agent-held requires trusted apply", agentHeld: true, want: "sudo yaog-agent kit apply", forbidden: "Run: sudo bash install.sh"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			result := minimalCompileResult()
			result.AgentHeld = tc.agentHeld
			outputDir := t.TempDir()
			if _, err := Export(result, outputDir); err != nil {
				t.Fatalf("Export: %v", err)
			}
			readme, err := os.ReadFile(filepath.Join(outputDir, "n1", "README.txt"))
			if err != nil {
				t.Fatalf("read README: %v", err)
			}
			body := string(readme)
			if !strings.Contains(body, tc.want) {
				t.Fatalf("README missing custody guidance %q:\n%s", tc.want, body)
			}
			if strings.Contains(body, tc.forbidden) {
				t.Fatalf("README contains contradictory custody guidance %q:\n%s", tc.forbidden, body)
			}
		})
	}
}

func TestExport_ManifestContent(t *testing.T) {
	result := minimalCompileResult()
	outputDir := t.TempDir()

	_, err := Export(result, outputDir)
	if err != nil {
		t.Fatalf("Export failed: %v", err)
	}

	// Read and parse the manifest.
	manifestPath := filepath.Join(outputDir, "n1", "manifest.json")
	data, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatalf("failed to read manifest: %v", err)
	}

	var manifest map[string]interface{}
	if err := json.Unmarshal(data, &manifest); err != nil {
		t.Fatalf("failed to parse manifest: %v", err)
	}

	if manifest["node_id"] != "n1" {
		t.Errorf("manifest node_id want n1, got %v", manifest["node_id"])
	}
	if manifest["overlay_ip"] != "10.10.0.1" {
		t.Errorf("manifest overlay_ip want 10.10.0.1, got %v", manifest["overlay_ip"])
	}
	if manifest["project_id"] != "test-001" {
		t.Errorf("manifest project_id want test-001, got %v", manifest["project_id"])
	}
}

func minimalCompileResult() *compiler.CompileResult {
	return &compiler.CompileResult{
		Topology: &model.Topology{
			Project: model.Project{ID: "test-001", Name: "Test", Version: "0.1.0"},
			Nodes: []model.Node{
				{ID: "n1", Name: "alpha", OverlayIP: "10.10.0.1", Role: "router", DomainID: "d1"},
			},
		},
		PeerMap:          map[string][]compiler.PeerInfo{"n1": {{NodeID: "n2", NodeName: "beta", InterfaceName: "wg-beta"}}},
		WireGuardConfigs: map[string]string{"n1:wg-beta": "[Interface]\nPrivateKey = test\n"},
		BabelConfigs:     map[string]string{"n1": "router-id alpha\n"},
		SysctlConfigs:    map[string]string{"n1": "net.ipv4.ip_forward = 1\n"},
		InstallScripts:   map[string]string{"n1": "#!/bin/bash\necho install\n"},
		Manifest: compiler.CompileManifest{
			ProjectID:   "test-001",
			ProjectName: "Test",
			Version:     "0.1.0",
			CompiledAt:  time.Now(),
			NodeCount:   1,
			Checksum:    "abc123",
		},
	}
}
