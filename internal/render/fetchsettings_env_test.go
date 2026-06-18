package render

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/kunorikiku/yet-another-overlay-generator/internal/model"
)

// sampleCatalogJSON is an artifacts.json-shaped catalog an operator would hand to air-gap mode
// (the same shape the export path emits, so a controller-emitted artifacts.json round-trips).
const sampleCatalogJSON = `{
  "schema": 1,
  "mimic": {
    "version": "v1.4.0",
    "release_url": "https://github.com/example/mimic/releases/download/v1.4.0",
    "debs": {
      "bookworm-amd64": { "asset": "mimic_1.4.0_amd64.deb", "sha256": "aa11" },
      "bookworm-arm64": { "asset": "mimic_1.4.0_arm64.deb", "sha256": "bb22" }
    }
  },
  "agent": {}
}`

// TestLoadFetchSettings_Empty pins the D4 air-gap default: with no catalog/proxy/version the
// result is the zero FetchSettings (no catalog ⇒ no artifacts.json ⇒ byte-identical bundle).
func TestLoadFetchSettings_Empty(t *testing.T) {
	fs, err := LoadFetchSettings("", "", "")
	if err != nil {
		t.Fatalf("LoadFetchSettings(empty): %v", err)
	}
	if hasCatalog(fs) {
		t.Fatalf("empty inputs must yield no catalog; got %+v", fs)
	}
	if !reflect.DeepEqual(fs, FetchSettings{}) {
		t.Errorf("empty inputs must yield the zero FetchSettings; got %+v", fs)
	}
}

// TestLoadFetchSettings_Catalog parses a catalog file into the mimic pins.
func TestLoadFetchSettings_Catalog(t *testing.T) {
	path := filepath.Join(t.TempDir(), "catalog.json")
	if err := os.WriteFile(path, []byte(sampleCatalogJSON), 0600); err != nil {
		t.Fatalf("write catalog: %v", err)
	}
	fs, err := LoadFetchSettings(path, "", "")
	if err != nil {
		t.Fatalf("LoadFetchSettings(catalog): %v", err)
	}
	if fs.MimicVersion != "v1.4.0" {
		t.Errorf("MimicVersion = %q, want v1.4.0", fs.MimicVersion)
	}
	if fs.MimicReleaseBase != "https://github.com/example/mimic/releases/download/v1.4.0" {
		t.Errorf("MimicReleaseBase = %q", fs.MimicReleaseBase)
	}
	if got := fs.MimicDebs["bookworm-amd64"]; got != (model.Artifact{Asset: "mimic_1.4.0_amd64.deb", SHA256: "aa11"}) {
		t.Errorf("bookworm-amd64 deb = %+v", got)
	}
	if len(fs.MimicDebs) != 2 {
		t.Errorf("len(MimicDebs) = %d, want 2", len(fs.MimicDebs))
	}
	if !hasCatalog(fs) {
		t.Errorf("a populated catalog must register as a catalog")
	}
}

// TestLoadFetchSettings_Overrides layers proxy + version over the catalog (override wins).
func TestLoadFetchSettings_Overrides(t *testing.T) {
	path := filepath.Join(t.TempDir(), "catalog.json")
	if err := os.WriteFile(path, []byte(sampleCatalogJSON), 0600); err != nil {
		t.Fatalf("write catalog: %v", err)
	}
	fs, err := LoadFetchSettings(path, "https://gh-proxy.example/", "v1.4.1")
	if err != nil {
		t.Fatalf("LoadFetchSettings(overrides): %v", err)
	}
	if fs.GithubProxy != "https://gh-proxy.example/" {
		t.Errorf("GithubProxy = %q", fs.GithubProxy)
	}
	if fs.MimicVersion != "v1.4.1" {
		t.Errorf("MimicVersion override = %q, want v1.4.1", fs.MimicVersion)
	}
	// The override does not disturb the catalog's debs/release base.
	if len(fs.MimicDebs) != 2 || fs.MimicReleaseBase == "" {
		t.Errorf("override clobbered catalog pins: %+v", fs)
	}
}

// TestLoadFetchSettings_VersionOnly sets the version with no catalog (still a catalog for D4).
func TestLoadFetchSettings_VersionOnly(t *testing.T) {
	fs, err := LoadFetchSettings("", "", "v9.9.9")
	if err != nil {
		t.Fatalf("LoadFetchSettings(version-only): %v", err)
	}
	if fs.MimicVersion != "v9.9.9" || !hasCatalog(fs) {
		t.Errorf("version-only must register a (degenerate) catalog; got %+v", fs)
	}
}

// TestLoadFetchSettings_FutureSchema fails closed on a catalog from a newer build.
func TestLoadFetchSettings_FutureSchema(t *testing.T) {
	path := filepath.Join(t.TempDir(), "future.json")
	if err := os.WriteFile(path, []byte(`{"schema": 999, "mimic": {"version": "v1"}}`), 0600); err != nil {
		t.Fatalf("write catalog: %v", err)
	}
	if _, err := LoadFetchSettings(path, "", ""); err == nil {
		t.Fatalf("a future-schema catalog must be rejected")
	}
}

// TestLoadFetchSettings_BadPath errors (does not silently fall back) on an unreadable catalog.
func TestLoadFetchSettings_BadPath(t *testing.T) {
	if _, err := LoadFetchSettings(filepath.Join(t.TempDir(), "missing.json"), "", ""); err == nil {
		t.Fatalf("a missing catalog path must error, not silently yield a zero catalog")
	}
}

// TestFetchSettingsFromEnv resolves the three env vars (and yields zero when all unset).
func TestFetchSettingsFromEnv(t *testing.T) {
	path := filepath.Join(t.TempDir(), "catalog.json")
	if err := os.WriteFile(path, []byte(sampleCatalogJSON), 0600); err != nil {
		t.Fatalf("write catalog: %v", err)
	}
	t.Setenv(EnvArtifactCatalog, path)
	t.Setenv(EnvGithubProxy, "https://proxy.env/")
	t.Setenv(EnvMimicVersion, "")
	fs, err := FetchSettingsFromEnv()
	if err != nil {
		t.Fatalf("FetchSettingsFromEnv: %v", err)
	}
	if fs.GithubProxy != "https://proxy.env/" || fs.MimicVersion != "v1.4.0" || len(fs.MimicDebs) != 2 {
		t.Errorf("env resolution wrong: %+v", fs)
	}

	t.Setenv(EnvArtifactCatalog, "")
	t.Setenv(EnvGithubProxy, "")
	zero, err := FetchSettingsFromEnv()
	if err != nil {
		t.Fatalf("FetchSettingsFromEnv(unset): %v", err)
	}
	if !reflect.DeepEqual(zero, FetchSettings{}) {
		t.Errorf("all-unset env must yield the zero FetchSettings; got %+v", zero)
	}
}
