package artifacts

import (
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/kunorikiku/yet-another-overlay-generator/internal/bundlesig"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/compiler"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/model"
)

// nonMemberSidecars are the files Export writes into a node dir that are NOT bundle
// members: the checksum/signature metadata layer (checksums.sha256, bundle.sig,
// signing-pubkey.pem) and the compile-time manifest (manifest.json). README.txt is deliberately
// a member because its custody-critical application guidance must be integrity-bound.
// Everything ELSE on disk under a node dir MUST be a BundleFiles member — that is the
// custody invariant this test guards.
var nonMemberSidecars = map[string]bool{
	"checksums.sha256":   true,
	"manifest.json":      true,
	"bundle.sig":         true,
	"signing-pubkey.pem": true,
}

// bundleFileSetResult builds a representative multi-node compile that exercises every
// bundle shape at once: a non-client "peer" (alpha) with multiple per-peer WireGuard
// interfaces + babel + sysctl + install.sh + an artifacts.json catalog pin (the optional,
// D4-guarded member), and a "client" (bravo) with a single wg0 and no babel. Together they
// cover the members most at risk of write/list/checksum drift. Export reads only the maps
// below and Topology.Nodes, so no full compile is needed.
func bundleFileSetResult() *compiler.CompileResult {
	return &compiler.CompileResult{
		Topology: &model.Topology{
			Project: model.Project{ID: "p1", Name: "proj", Version: "1.0.0"},
			Nodes: []model.Node{
				{ID: "n1", Name: "alpha", Role: "peer", DomainID: "d1", OverlayIP: "10.0.0.1"},
				{ID: "n2", Name: "bravo", Role: "client", DomainID: "d1", OverlayIP: "10.0.0.2"},
			},
		},
		WireGuardConfigs: map[string]string{
			// alpha: two per-peer interfaces, deliberately not in sorted map order so a
			// reliance on map iteration order would surface.
			"n1:wg-zulu":  "[Interface]\n# zulu\n",
			"n1:wg-alpha": "[Interface]\n# alpha\n",
			// bravo: single wg0 (client).
			"n2:wg0": "[Interface]\n# client wg0\n",
		},
		BabelConfigs: map[string]string{
			// non-client only.
			"n1": "router-id 02:11:22:33:44:55\n",
		},
		SysctlConfigs: map[string]string{
			"n1": "net.ipv4.ip_forward = 1\n",
			"n2": "net.ipv4.ip_forward = 0\n",
		},
		InstallScripts: map[string]string{
			"n1": "#!/usr/bin/env bash\necho alpha\n",
			"n2": "#!/usr/bin/env bash\necho bravo\n",
		},
		// artifacts.json only on the peer: the optional (D4-guarded) member that is the
		// highest-risk write/list/checksum drift case.
		ArtifactsJSON: map[string]string{
			"n1": "{\"mimic\":{\"asset\":\"x\"}}\n",
		},
		Manifest: compiler.CompileManifest{
			ProjectID:   "p1",
			ProjectName: "proj",
			Version:     "1.0.0",
			CompiledAt:  time.Date(2026, 7, 13, 0, 0, 0, 0, time.UTC),
			Checksum:    "deadbeef",
		},
	}
}

// TestExport_BundleFileSet_SingleSource is the perpetual custody guard for plan-1.5: for
// every node it asserts the four independent views of the bundle member set are EXACTLY
// equal —
//
//	written-to-disk == sorted BundleFiles keys == manifest.json "files" == checksums.sha256 entries
//
// (the on-disk and manifest views minus the non-member sidecars bundle.sig/signing-pubkey.pem
// where signing adds them). A member written but not listed would ship UNSIGNED/UNCHECKSUMMED
// (a tamper surface); a member listed but not written would fail `sha256sum -c` on the node.
// The test runs signed AND unsigned: signing must add EXACTLY bundle.sig + signing-pubkey.pem
// to the on-disk + manifest views and NOTHING to the checksummed/BundleFiles views.
func TestExport_BundleFileSet_SingleSource(t *testing.T) {
	for _, tc := range []struct {
		name   string
		signed bool
	}{
		{"unsigned", false},
		{"signed", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.signed {
				keyPath, _ := writeTestSigningKey(t)
				t.Setenv(bundlesig.EnvSigningKey, keyPath)
			} else {
				// Ensure signing is off even if the runner env has the var set.
				t.Setenv(bundlesig.EnvSigningKey, "")
			}

			result := bundleFileSetResult()
			outDir := t.TempDir()
			if _, err := Export(result, outDir); err != nil {
				t.Fatalf("Export: %v", err)
			}

			for _, node := range result.Topology.Nodes {
				nodeDir := filepath.Join(outDir, node.ID)

				// (B) sorted BundleFiles keys — the single source of truth.
				bundleFiles, err := BundleFiles(result, node.ID)
				if err != nil {
					t.Fatalf("%s: BundleFiles: %v", node.Name, err)
				}
				want := sortedKeys(bundleFiles)
				if len(want) == 0 {
					t.Fatalf("%s: BundleFiles returned no members", node.Name)
				}

				// (A) files WRITTEN to disk under nodeDir, minus the non-member sidecars.
				if written := writtenBundleMembers(t, nodeDir); !equalStringSlices(written, want) {
					t.Errorf("%s: WRITTEN files != BundleFiles keys:\n  written: %v\n  bundle:  %v", node.Name, written, want)
				}

				// (C) manifest.json "files", minus bundle.sig/signing-pubkey.pem.
				if manifestFiles := manifestBundleFiles(t, nodeDir); !equalStringSlices(manifestFiles, want) {
					t.Errorf("%s: manifest \"files\" != BundleFiles keys:\n  manifest: %v\n  bundle:   %v", node.Name, manifestFiles, want)
				}

				// (D) checksums.sha256 entries.
				if sums := checksumPaths(t, filepath.Join(nodeDir, "checksums.sha256")); !equalStringSlices(sums, want) {
					t.Errorf("%s: checksums.sha256 entries != BundleFiles keys:\n  checksums: %v\n  bundle:    %v", node.Name, sums, want)
				}

				// When signing, the sidecars must be present on disk AND listed in the manifest,
				// proving they are the ONLY difference between the raw on-disk file set and the
				// checksummed member set (never a member masquerading as a sidecar, or vice versa).
				if tc.signed {
					manifestAll := rawManifestFiles(t, nodeDir)
					for _, sc := range []string{"bundle.sig", "signing-pubkey.pem"} {
						if _, err := os.Stat(filepath.Join(nodeDir, sc)); err != nil {
							t.Errorf("%s: signed export missing sidecar %s on disk: %v", node.Name, sc, err)
						}
						if !contains(manifestAll, sc) {
							t.Errorf("%s: signed export manifest \"files\" does not list sidecar %s", node.Name, sc)
						}
					}
				}
			}
		})
	}
}

// sortedKeys returns the map's keys in ascending byte order.
func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// writtenBundleMembers walks nodeDir and returns the slash-relative paths of every regular
// file written EXCEPT the non-member sidecars, sorted. Directories are skipped (only files
// are bundle members).
func writtenBundleMembers(t *testing.T, nodeDir string) []string {
	t.Helper()
	var out []string
	err := filepath.WalkDir(nodeDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, relErr := filepath.Rel(nodeDir, path)
		if relErr != nil {
			return relErr
		}
		rel = filepath.ToSlash(rel)
		if nonMemberSidecars[rel] {
			return nil
		}
		out = append(out, rel)
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", nodeDir, err)
	}
	sort.Strings(out)
	return out
}

// manifestBundleFiles returns manifest.json's "files" array with the signing sidecars
// (bundle.sig, signing-pubkey.pem) removed, sorted — i.e. the manifest's view of the
// checksummed member set.
func manifestBundleFiles(t *testing.T, nodeDir string) []string {
	t.Helper()
	var out []string
	for _, f := range rawManifestFiles(t, nodeDir) {
		if f == "bundle.sig" || f == "signing-pubkey.pem" {
			continue
		}
		out = append(out, f)
	}
	sort.Strings(out)
	return out
}

// rawManifestFiles reads and returns manifest.json's "files" array verbatim (order as written).
func rawManifestFiles(t *testing.T, nodeDir string) []string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(nodeDir, "manifest.json"))
	if err != nil {
		t.Fatalf("read manifest.json: %v", err)
	}
	var manifest struct {
		Files []string `json:"files"`
	}
	if err := json.Unmarshal(data, &manifest); err != nil {
		t.Fatalf("parse manifest.json: %v", err)
	}
	return manifest.Files
}

// checksumPaths parses a sha256sum-format file ("<hex>  <path>") and returns the listed
// paths, sorted.
func checksumPaths(t *testing.T, checksumsPath string) []string {
	t.Helper()
	data, err := os.ReadFile(checksumsPath)
	if err != nil {
		t.Fatalf("read checksums.sha256: %v", err)
	}
	var out []string
	for _, line := range strings.Split(strings.TrimRight(string(data), "\n"), "\n") {
		if line == "" {
			continue
		}
		fields := strings.SplitN(line, "  ", 2)
		if len(fields) != 2 {
			t.Fatalf("checksums line not sha256sum format: %q", line)
		}
		out = append(out, fields[1])
	}
	sort.Strings(out)
	return out
}

// equalStringSlices reports whether two sorted string slices are element-for-element equal.
func equalStringSlices(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func contains(haystack []string, needle string) bool {
	for _, h := range haystack {
		if h == needle {
			return true
		}
	}
	return false
}
