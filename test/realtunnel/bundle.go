//go:build linux && integration

package realtunnel

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kunorikiku/yet-another-overlay-generator/internal/artifacts"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/compiler"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/localcompile"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/model"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/render"
)

// bundle.go — Phase 3: the bundle-production helper. It compiles a topology through the SAME shared
// façade cmd/compiler uses (localcompile.CompileResult with render.AirGap), then exports the per-node
// deployment bundle with artifacts.Export. The bundle the test brings up is therefore byte-for-byte
// the bundle that ships (oracle integrity) — no test-only compile path, no fake keys.

// repoFile resolves a repo-root-relative path (e.g. "examples/simple-mesh/topology.json") to an
// absolute path by walking up from the test's working directory to the go.mod root.
func repoFile(t *testing.T, rel string) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return filepath.Join(dir, rel)
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("repo root (go.mod) not found above %s", dir)
		}
		dir = parent
	}
}

// loadTopology reads + parses a topology JSON fixture (e.g. examples/simple-mesh/topology.json).
func loadTopology(t *testing.T, path string) model.Topology {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read topology %s: %v", path, err)
	}
	var topo model.Topology
	if err := json.Unmarshal(data, &topo); err != nil {
		t.Fatalf("parse topology %s: %v", path, err)
	}
	return topo
}

// produceBundle compiles topo via the shared local-compile façade (the exact path cmd/compiler takes:
// AirGap key custody, zero FetchSettings ⇒ distro-only mimic + byte-identical bundle) and exports the
// per-node tree under outDir. Returns the resolved CompileResult (its Topology carries the allocated
// OverlayIPs the netns assertions poll for) + the Export result.
func produceBundle(t *testing.T, topo model.Topology, outDir string) *compileBundle {
	t.Helper()
	result, err := localcompile.CompileResult(localcompile.CompileRequest{
		Topology: topo,
		Custody:  render.AirGap,
	})
	if err != nil {
		t.Fatalf("compile failed: %v", err)
	}
	if _, err := artifacts.Export(result, outDir); err != nil {
		t.Fatalf("export bundle: %v", err)
	}
	return &compileBundle{result: result, outDir: outDir}
}

// compileBundle bundles the compile result with the exported output directory.
type compileBundle struct {
	result *compiler.CompileResult
	outDir string
}

// requireBundleFiles asserts every node directory carries the deployable artifact set and that its
// checksums.sha256 verifies against the on-disk bytes — the oracle-integrity gate (the bundle the
// test runs is the bundle that ships). nodeDirs returns the per-node directory names it verified.
func (b *compileBundle) requireBundleFiles(t *testing.T) []string {
	t.Helper()
	entries, err := os.ReadDir(b.outDir)
	if err != nil {
		t.Fatalf("read export dir %s: %v", b.outDir, err)
	}
	var dirs []string
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		nodeDir := filepath.Join(b.outDir, e.Name())
		for _, rel := range []string{"babel/babeld.conf", "sysctl/99-overlay.conf", "install.sh", "checksums.sha256"} {
			if _, err := os.Stat(filepath.Join(nodeDir, rel)); err != nil {
				t.Errorf("node %s missing %s: %v", e.Name(), rel, err)
			}
		}
		// At least one WireGuard interface config must exist.
		wgs, _ := filepath.Glob(filepath.Join(nodeDir, "wireguard", "*.conf"))
		if len(wgs) == 0 {
			t.Errorf("node %s has no wireguard/*.conf", e.Name())
		}
		verifyChecksums(t, nodeDir)
		dirs = append(dirs, e.Name())
	}
	if len(dirs) == 0 {
		t.Fatalf("export produced no node directories under %s", b.outDir)
	}
	return dirs
}

// verifyChecksums re-hashes every file listed in a node's checksums.sha256 and asserts the digest
// matches — proving the rendered artifacts are intact (the same check the agent does before deploy).
func verifyChecksums(t *testing.T, nodeDir string) {
	t.Helper()
	f, err := os.Open(filepath.Join(nodeDir, "checksums.sha256"))
	if err != nil {
		t.Errorf("open checksums.sha256 in %s: %v", nodeDir, err)
		return
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	n := 0
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		// Format: "<hex-sha256>  <relative-path>" (sha256sum style, two spaces).
		fields := strings.SplitN(line, "  ", 2)
		if len(fields) != 2 {
			t.Errorf("%s: malformed checksum line %q", nodeDir, line)
			continue
		}
		want, rel := fields[0], fields[1]
		got, err := sha256File(filepath.Join(nodeDir, rel))
		if err != nil {
			t.Errorf("%s: hash %s: %v", nodeDir, rel, err)
			continue
		}
		if got != want {
			t.Errorf("%s: checksum mismatch for %s: have %s want %s", nodeDir, rel, got, want)
		}
		n++
	}
	if n == 0 {
		t.Errorf("%s: checksums.sha256 listed no files", nodeDir)
	}
}

func sha256File(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}
