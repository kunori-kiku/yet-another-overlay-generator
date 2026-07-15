package agent

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kunorikiku/yet-another-overlay-generator/internal/bundlesig"
)

// bundleFixture is an in-memory per-node bundle plus its signing material, built
// so the tests exercise the exact contract the export path emits: checksums.sha256
// is bundlesig.Canonicalize over the checksummed file set, bundle.sig is
// base64(raw Ed25519 sig over those exact bytes), signing-pubkey.pem is the PKIX
// PEM of the matching public key, and manifest.json carries compiled_at/checksum.
type bundleFixture struct {
	files  map[string][]byte
	priv   ed25519.PrivateKey
	pubPEM []byte
}

type staticBundleSource map[string][]byte

func (s staticBundleSource) Fetch(string) (map[string][]byte, error) {
	out := make(map[string][]byte, len(s))
	for name, content := range s {
		out[name] = append([]byte(nil), content...)
	}
	return out, nil
}

type fetchTrackingSource struct {
	fetched *bool
}

func (s fetchTrackingSource) Fetch(string) (map[string][]byte, error) {
	*s.fetched = true
	return nil, nil
}

// newSignedBundle constructs a valid signed bundle with the given manifest
// compiled_at. The checksummed set mirrors a non-client node bundle.
func newSignedBundle(t *testing.T, compiledAt string) *bundleFixture {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate ed25519 key: %v", err)
	}

	// The checksummed file set (string content; matches export.go's bundleFiles).
	checksummed := map[string]string{
		"install.sh":              "#!/usr/bin/env bash\necho stub-install\n",
		"wireguard/wg-alpha.conf": "[Interface]\nPrivateKey = PRIVATEKEY_PLACEHOLDER\n",
		"babel/babeld.conf":       "interface wg-alpha\n",
		"sysctl/99-overlay.conf":  "net.ipv4.ip_forward=1\n",
	}
	canonical := bundlesig.Canonicalize(checksummed)
	sig := bundlesig.Sign(canonical, priv)
	pubPEM := bundlesig.MarshalPublicKeyPEM(pub)

	manifest := map[string]any{
		"node_id":     "alpha",
		"compiled_at": compiledAt,
		"checksum":    "abc123",
	}
	manJSON, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		t.Fatalf("marshal manifest: %v", err)
	}

	files := map[string][]byte{
		"checksums.sha256":   canonical,
		"bundle.sig":         []byte(base64.StdEncoding.EncodeToString(sig) + "\n"),
		"signing-pubkey.pem": pubPEM,
		"manifest.json":      manJSON,
	}
	for p, c := range checksummed {
		files[p] = []byte(c)
	}

	return &bundleFixture{files: files, priv: priv, pubPEM: pubPEM}
}

func TestVerifyBundleValidSigned(t *testing.T) {
	b := newSignedBundle(t, "2026-06-08T12:00:00Z")
	res, err := VerifyBundle(b.files, b.pubPEM)
	if err != nil {
		t.Fatalf("VerifyBundle(valid signed): %v", err)
	}
	if !res.Signed {
		t.Fatalf("expected Signed=true")
	}
	if res.FileCount != 4 {
		t.Fatalf("FileCount = %d, want 4", res.FileCount)
	}
}

func TestVerifyBundleBadSignature(t *testing.T) {
	b := newSignedBundle(t, "2026-06-08T12:00:00Z")
	// Corrupt the signature bytes.
	b.files["bundle.sig"] = []byte(base64.StdEncoding.EncodeToString(make([]byte, ed25519.SignatureSize)) + "\n")
	if _, err := VerifyBundle(b.files, b.pubPEM); err == nil {
		t.Fatalf("expected error on bad signature, got nil")
	}
}

func TestVerifyBundleBadChecksum(t *testing.T) {
	b := newSignedBundle(t, "2026-06-08T12:00:00Z")
	// Tamper a file's content WITHOUT touching checksums.sha256 -> hash mismatch.
	b.files["sysctl/99-overlay.conf"] = []byte("net.ipv4.ip_forward=0\n")
	// The signature still covers the (unchanged) checksums bytes, so the
	// signature passes but the per-file hash must fail closed.
	if _, err := VerifyBundle(b.files, b.pubPEM); err == nil {
		t.Fatalf("expected error on tampered file, got nil")
	}
}

func TestVerifyBundlePinnedButUnsigned(t *testing.T) {
	b := newSignedBundle(t, "2026-06-08T12:00:00Z")
	pinned := b.pubPEM
	// Strip the signature material -> a pinned operator must reject an unsigned bundle.
	delete(b.files, "bundle.sig")
	delete(b.files, "signing-pubkey.pem")
	if _, err := VerifyBundle(b.files, pinned); err == nil {
		t.Fatalf("expected error: pinned key but no signature, got nil")
	}
}

func TestVerifyBundleUnsignedNoPinAllowed(t *testing.T) {
	b := newSignedBundle(t, "2026-06-08T12:00:00Z")
	delete(b.files, "bundle.sig")
	delete(b.files, "signing-pubkey.pem")
	// No pinned key + no signature: hash-only verification is allowed.
	res, err := VerifyBundle(b.files, nil)
	if err != nil {
		t.Fatalf("VerifyBundle(unsigned, no pin): %v", err)
	}
	if res.Signed {
		t.Fatalf("expected Signed=false for unsigned bundle")
	}
}

func TestVerifyBundleWrongPinnedKey(t *testing.T) {
	b := newSignedBundle(t, "2026-06-08T12:00:00Z")
	// A different pinned key must reject a bundle signed by another key.
	otherPub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("gen other key: %v", err)
	}
	otherPEM := bundlesig.MarshalPublicKeyPEM(otherPub)
	if _, err := VerifyBundle(b.files, otherPEM); err == nil {
		t.Fatalf("expected error: signature does not match pinned key, got nil")
	}
}

func TestCheckRollback(t *testing.T) {
	prev := &State{LastCompiledAt: "2026-06-08T12:00:00Z"}

	// Older -> refused.
	if err := CheckRollback(prev, "2026-06-07T12:00:00Z"); err == nil {
		t.Fatalf("expected rollback refusal for older compiled_at")
	}
	// Equal -> allowed (idempotent re-apply).
	if err := CheckRollback(prev, "2026-06-08T12:00:00Z"); err != nil {
		t.Fatalf("equal compiled_at should be allowed: %v", err)
	}
	// Newer -> allowed.
	if err := CheckRollback(prev, "2026-06-09T12:00:00Z"); err != nil {
		t.Fatalf("newer compiled_at should be allowed: %v", err)
	}
	// First run (no baseline) -> allowed.
	if err := CheckRollback(&State{}, "2020-01-01T00:00:00Z"); err != nil {
		t.Fatalf("first-run should be allowed: %v", err)
	}
}

// writeBundleToDir writes a fixture's files to dir/<nodeID>/ so DirSource can
// fetch them, returning the root dir.
func writeBundleToDir(t *testing.T, nodeID string, files map[string][]byte) string {
	t.Helper()
	root := t.TempDir()
	nodeDir := filepath.Join(root, nodeID)
	for rel, content := range files {
		dst := filepath.Join(nodeDir, rel)
		if err := os.MkdirAll(filepath.Dir(dst), 0700); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(dst, content, 0600); err != nil {
			t.Fatalf("write %s: %v", rel, err)
		}
	}
	return root
}

func TestDirSourceFetch(t *testing.T) {
	b := newSignedBundle(t, "2026-06-08T12:00:00Z")
	root := writeBundleToDir(t, "alpha", b.files)

	src := NewDirSource(root)
	got, err := src.Fetch("alpha")
	if err != nil {
		t.Fatalf("DirSource.Fetch: %v", err)
	}
	for rel := range b.files {
		if _, ok := got[rel]; !ok {
			t.Fatalf("DirSource.Fetch missing %q", rel)
		}
	}
	// Fetched content must verify end-to-end.
	if _, err := VerifyBundle(got, b.pubPEM); err != nil {
		t.Fatalf("fetched bundle failed verify: %v", err)
	}

	// Unknown node -> error.
	if _, err := src.Fetch("nope"); err == nil {
		t.Fatalf("expected error fetching unknown node")
	}
}

// TestRunRefusesRollback drives the full Run loop against a DirSource and asserts
// that a bundle older than the recorded last-applied is refused and install.sh is
// NEVER executed (degradation: keep last-good).
func TestRunRefusesRollback(t *testing.T) {
	b := newSignedBundle(t, "2026-06-01T00:00:00Z") // older than baseline below
	root := writeBundleToDir(t, "alpha", b.files)
	stateDir := t.TempDir()

	// Seed a newer last-applied baseline.
	if err := SaveState(stateDir, &State{
		NodeID:         "alpha",
		LastCompiledAt: "2026-06-08T00:00:00Z",
		LastChecksum:   "prevsum",
	}); err != nil {
		t.Fatalf("seed state: %v", err)
	}

	// Sentinel: install.sh would create this file; it must NOT exist after a refusal.
	sentinel := filepath.Join(t.TempDir(), "ran.flag")
	b.files["install.sh"] = []byte("#!/usr/bin/env bash\ntouch " + sentinel + "\n")
	// Rewrite the bundle dir with the sentinel-bearing install.sh, then refresh
	// checksums+signature so it still passes verify (only rollback should refuse).
	resign(t, b)
	root = writeBundleToDir(t, "alpha", b.files)

	cfg := &Config{
		NodeID:       "alpha",
		Source:       NewDirSource(root),
		PinnedPubPEM: b.pubPEM,
		KeyPath:      filepath.Join(t.TempDir(), "agent.key"),
		StateDir:     stateDir,
		Stdout:       &strings.Builder{},
		Stderr:       &strings.Builder{},
	}
	if _, err := Run(cfg); err == nil {
		t.Fatalf("expected Run to refuse rollback")
	}
	if _, err := os.Stat(sentinel); err == nil {
		t.Fatalf("install.sh ran despite rollback refusal")
	}

	// Last-good baseline must be preserved (not clobbered by the failed attempt).
	st, err := LoadState(stateDir)
	if err != nil {
		t.Fatalf("load state: %v", err)
	}
	if st.LastCompiledAt != "2026-06-08T00:00:00Z" || st.LastChecksum != "prevsum" {
		t.Fatalf("last-good baseline was clobbered: %+v", st)
	}
}

// TestRunRefusesBadSignatureBeforeApply confirms a bad-signature bundle is
// refused by the Go-side gate before install.sh is ever invoked.
func TestRunRefusesBadSignatureBeforeApply(t *testing.T) {
	b := newSignedBundle(t, "2026-06-08T12:00:00Z")
	sentinel := filepath.Join(t.TempDir(), "ran.flag")
	b.files["install.sh"] = []byte("#!/usr/bin/env bash\ntouch " + sentinel + "\n")
	resign(t, b)
	// Now corrupt the signature AFTER resigning so checksums are consistent but
	// the signature is wrong.
	b.files["bundle.sig"] = []byte(base64.StdEncoding.EncodeToString(make([]byte, ed25519.SignatureSize)) + "\n")
	root := writeBundleToDir(t, "alpha", b.files)

	cfg := &Config{
		NodeID:       "alpha",
		Source:       NewDirSource(root),
		PinnedPubPEM: b.pubPEM,
		KeyPath:      filepath.Join(t.TempDir(), "agent.key"),
		StateDir:     t.TempDir(),
		Stdout:       &strings.Builder{},
		Stderr:       &strings.Builder{},
	}
	if _, err := Run(cfg); err == nil {
		t.Fatalf("expected Run to refuse bad signature")
	}
	if _, err := os.Stat(sentinel); err == nil {
		t.Fatalf("install.sh ran despite bad signature")
	}
}

// TestRunRefusesNoncanonicalInstallAlias closes the verified-script overwrite primitive: an
// unlisted extra map key must not normalize onto install.sh after VerifyBundle authenticated the
// canonical entry. Neither the verified nor attacker alias is allowed to execute.
func TestRunRefusesNoncanonicalInstallAlias(t *testing.T) {
	b := newSignedBundle(t, "2026-07-15T10:00:00Z")
	verifiedSentinel := filepath.Join(t.TempDir(), "verified-ran")
	aliasSentinel := filepath.Join(t.TempDir(), "alias-ran")
	b.files["install.sh"] = []byte("#!/usr/bin/env bash\ntouch " + verifiedSentinel + "\n")
	resign(t, b)
	// This extra is intentionally outside checksums.sha256. Before the central path preflight,
	// filepath.Clean made it the same staging destination as the verified install.sh and map order
	// decided which bytes root executed.
	b.files["x/../install.sh"] = []byte("#!/usr/bin/env bash\ntouch " + aliasSentinel + "\n")

	cfg := &Config{
		NodeID:       "alpha",
		Source:       staticBundleSource(b.files),
		PinnedPubPEM: b.pubPEM,
		StateDir:     t.TempDir(),
		Stdout:       &strings.Builder{},
		Stderr:       &strings.Builder{},
	}
	if _, err := Run(cfg); err == nil || !strings.Contains(err.Error(), "unsafe bundle path") {
		t.Fatalf("Run did not reject the noncanonical install alias: %v", err)
	}
	for _, sentinel := range []string{verifiedSentinel, aliasSentinel} {
		if _, err := os.Stat(sentinel); !os.IsNotExist(err) {
			t.Fatalf("a script executed despite alias refusal (%s): %v", sentinel, err)
		}
	}
}

func TestRunRejectsInstallArgumentsOutsideClosedSetBeforeFetch(t *testing.T) {
	for _, args := range [][]string{{"--shell"}, {"--uninstall", "--shell"}, {""}} {
		t.Run(strings.Join(args, ","), func(t *testing.T) {
			fetched := false
			cfg := &Config{
				NodeID:      "alpha",
				Source:      fetchTrackingSource{fetched: &fetched},
				StateDir:    t.TempDir(),
				InstallArgs: args,
			}
			if _, err := Run(cfg); err == nil || !strings.Contains(err.Error(), "unsupported install.sh arguments") {
				t.Fatalf("Run(%q) error = %v, want closed-set refusal", args, err)
			}
			if fetched {
				t.Fatal("Run fetched a bundle before rejecting unsupported root-script arguments")
			}
		})
	}
}

func TestRunRejectsUnsafeStateDirectoryBeforeFetch(t *testing.T) {
	t.Run("group-world-writable", func(t *testing.T) {
		stateDir := filepath.Join(t.TempDir(), "state")
		if err := os.Mkdir(stateDir, 0700); err != nil {
			t.Fatalf("mkdir state: %v", err)
		}
		if err := os.Chmod(stateDir, 0777); err != nil {
			t.Fatalf("chmod state: %v", err)
		}
		fetched := false
		_, err := Run(&Config{
			NodeID:   "alpha",
			Source:   fetchTrackingSource{fetched: &fetched},
			StateDir: stateDir,
		})
		if err == nil || !strings.Contains(err.Error(), "group/world-writable") {
			t.Fatalf("Run error = %v, want unsafe-directory refusal", err)
		}
		if fetched {
			t.Fatal("Run fetched before rejecting unsafe state custody")
		}
	})

	t.Run("symlink", func(t *testing.T) {
		realDir := filepath.Join(t.TempDir(), "real-state")
		if err := os.Mkdir(realDir, 0700); err != nil {
			t.Fatalf("mkdir real state: %v", err)
		}
		stateDir := filepath.Join(t.TempDir(), "state-link")
		if err := os.Symlink(realDir, stateDir); err != nil {
			t.Fatalf("symlink state: %v", err)
		}
		fetched := false
		_, err := Run(&Config{
			NodeID:   "alpha",
			Source:   fetchTrackingSource{fetched: &fetched},
			StateDir: stateDir,
		})
		if err == nil || !strings.Contains(err.Error(), "not a symlink") {
			t.Fatalf("Run error = %v, want state symlink refusal", err)
		}
		if fetched {
			t.Fatal("Run fetched before rejecting symlink state custody")
		}
	})
}

func TestStageRejectsEntireAmbiguousPathSetBeforeWriting(t *testing.T) {
	tests := map[string]map[string][]byte{
		"dot segment":       {"install.sh": []byte("ok"), "./README.txt": []byte("bad")},
		"parent traversal":  {"install.sh": []byte("ok"), "x/../install.sh": []byte("bad")},
		"backslash":         {"install.sh": []byte("ok"), `wireguard\wg0.conf`: []byte("bad")},
		"empty":             {"install.sh": []byte("ok"), "": []byte("bad")},
		"absolute":          {"install.sh": []byte("ok"), "/tmp/escape": []byte("bad")},
		"drive qualified":   {"install.sh": []byte("ok"), "C:/escape": []byte("bad")},
		"case collision":    {"README.txt": []byte("one"), "readme.txt": []byte("two")},
		"file parent/child": {"wireguard": []byte("file"), "wireguard/wg0.conf": []byte("child")},
	}
	for name, files := range tests {
		t.Run(name, func(t *testing.T) {
			staging := t.TempDir()
			if _, _, err := stage(&Config{StagingDir: staging}, files); err == nil {
				t.Fatal("stage accepted an ambiguous/noncanonical path set")
			}
			entries, err := os.ReadDir(staging)
			if err != nil {
				t.Fatalf("read staging dir: %v", err)
			}
			if len(entries) != 0 {
				t.Fatalf("stage wrote %d entries before rejecting the path set", len(entries))
			}
		})
	}
}

func TestRunPreflightsBundlePathsBeforeMaterialization(t *testing.T) {
	b := newSignedBundle(t, "2026-06-08T12:00:00Z")
	b.files["README.txt"] = []byte("first alias")
	b.files["readme.txt"] = []byte("second alias")
	staging := t.TempDir()

	if _, err := Run(&Config{
		NodeID:       "alpha",
		Source:       staticBundleSource(b.files),
		PinnedPubPEM: b.pubPEM,
		StateDir:     filepath.Join(t.TempDir(), "state"),
		StagingDir:   staging,
	}); err == nil || !strings.Contains(err.Error(), "bundle materialization preflight") {
		t.Fatalf("Run error = %v, want materialization preflight refusal", err)
	}
	entries, err := os.ReadDir(staging)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("Run wrote %d staging entries before path preflight", len(entries))
	}
}

func TestStageUsesFreshChildUnderSuppliedParent(t *testing.T) {
	parent := t.TempDir()
	target := filepath.Join(t.TempDir(), "outside-install")
	if err := os.WriteFile(target, []byte("unchanged"), 0600); err != nil {
		t.Fatalf("write symlink target: %v", err)
	}
	if err := os.Symlink(target, filepath.Join(parent, "install.sh")); err != nil {
		t.Fatalf("seed stale staging symlink: %v", err)
	}

	dir, cleanup, err := stage(&Config{StagingDir: parent}, map[string][]byte{
		"install.sh": []byte("#!/bin/sh\nexit 0\n"),
	})
	if err != nil {
		t.Fatalf("stage: %v", err)
	}
	if cleanup != nil {
		t.Fatal("operator-supplied staging parent should retain its fresh child")
	}
	if dir == parent || filepath.Dir(dir) != parent {
		t.Fatalf("staging dir = %q, want a fresh child of %q", dir, parent)
	}
	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("read symlink target: %v", err)
	}
	if string(got) != "unchanged" {
		t.Fatalf("stale symlink target changed to %q", got)
	}
	staged, err := os.ReadFile(filepath.Join(dir, "install.sh"))
	if err != nil {
		t.Fatalf("read fresh staged install.sh: %v", err)
	}
	if string(staged) != "#!/bin/sh\nexit 0\n" {
		t.Fatalf("fresh staged install.sh = %q", staged)
	}
}

func TestStageCleansOwnedTempDirectoryOnMaterializationError(t *testing.T) {
	tempRoot := t.TempDir()
	t.Setenv("TMPDIR", tempRoot)

	// This path is canonical but exceeds every supported filesystem's component/path
	// limit, forcing the write to fail after stage has allocated its owned temp dir.
	tooLong := strings.Repeat("x", 5000)
	dir, cleanup, err := stage(&Config{}, map[string][]byte{tooLong: []byte("unwritable")})
	if err == nil {
		t.Fatal("stage unexpectedly wrote an overlong path")
	}
	if dir != "" || cleanup != nil {
		t.Fatalf("failed owned stage returned dir=%q cleanup=%v; want neither after self-cleanup", dir, cleanup != nil)
	}
	entries, readErr := os.ReadDir(tempRoot)
	if readErr != nil {
		t.Fatalf("read temp root: %v", readErr)
	}
	if len(entries) != 0 {
		t.Fatalf("failed stage leaked owned temp entries: %v", entries)
	}
}

// TestRunAppliesValidBundle confirms the happy path: a valid signed, forward
// bundle runs install.sh, records success, and never writes the private key
// anywhere but the (untouched) key path.
func TestRunAppliesValidBundle(t *testing.T) {
	b := newSignedBundle(t, "2026-06-08T12:00:00Z")
	stagingParent := t.TempDir()
	sentinel := filepath.Join(stagingParent, "ran.flag")
	b.files["install.sh"] = []byte("#!/usr/bin/env bash\ntouch " + sentinel + "\n")
	resign(t, b)
	root := writeBundleToDir(t, "alpha", b.files)

	keyPath := filepath.Join(t.TempDir(), "wg", "agent.key")
	stateDir := t.TempDir()
	cfg := &Config{
		NodeID:       "alpha",
		Source:       NewDirSource(root),
		PinnedPubPEM: b.pubPEM,
		KeyPath:      keyPath,
		StateDir:     stateDir,
		Stdout:       &strings.Builder{},
		Stderr:       &strings.Builder{},
	}
	res, err := Run(cfg)
	if err != nil {
		t.Fatalf("Run(valid): %v", err)
	}
	if !res.Applied {
		t.Fatalf("expected Applied=true")
	}
	if _, err := os.Stat(sentinel); err != nil {
		t.Fatalf("install.sh did not run: %v", err)
	}

	// State must record the success.
	st, err := LoadState(stateDir)
	if err != nil {
		t.Fatalf("load state: %v", err)
	}
	if st.LastResult != "ok" || st.LastCompiledAt != "2026-06-08T12:00:00Z" {
		t.Fatalf("state not recorded correctly: %+v", st)
	}

	// The agent must NOT have written a private key (keygen is a separate step;
	// the splice is install.sh's job). The key path must not exist here.
	if _, err := os.Stat(keyPath); err == nil {
		t.Fatalf("Run unexpectedly created the private key at %s", keyPath)
	}
}

// TestRunRefusesNodeIDMismatch confirms Run refuses a bundle whose manifest node_id
// does not match the configured --node-id, so a misconfigured or malicious source
// cannot get the agent to apply another node's (validly-signed) bundle.
func TestRunRefusesNodeIDMismatch(t *testing.T) {
	b := newSignedBundle(t, "2026-06-08T12:00:00Z") // manifest node_id = "alpha"
	root := writeBundleToDir(t, "bravo", b.files)   // served under "bravo"
	cfg := &Config{
		NodeID:       "bravo", // matches the fetch path, but NOT the manifest node_id ("alpha")
		Source:       NewDirSource(root),
		PinnedPubPEM: b.pubPEM,
		StateDir:     t.TempDir(),
		Stdout:       &strings.Builder{},
		Stderr:       &strings.Builder{},
	}
	if _, err := Run(cfg); err == nil {
		t.Fatal("Run must refuse a bundle whose manifest node_id != configured node id")
	}
}

func TestRunRefusesStateIdentityReuseBeforeFetch(t *testing.T) {
	stateDir := t.TempDir()
	if err := SaveState(stateDir, &State{NodeID: "alpha", LastCompiledAt: "2026-07-15T00:00:00Z"}); err != nil {
		t.Fatalf("seed state: %v", err)
	}
	cfg := &Config{
		NodeID:   "bravo",
		Source:   NewDirSource(t.TempDir()), // no bundle: identity refusal must happen before Fetch
		StateDir: stateDir,
		Stdout:   &strings.Builder{},
		Stderr:   &strings.Builder{},
	}
	if _, err := Run(cfg); err == nil || !strings.Contains(err.Error(), `state belongs to node "alpha"`) {
		t.Fatalf("Run reused another node's state, err=%v", err)
	}
	st, err := LoadState(stateDir)
	if err != nil {
		t.Fatalf("reload state: %v", err)
	}
	if st.NodeID != "alpha" || st.LastCompiledAt != "2026-07-15T00:00:00Z" {
		t.Fatalf("identity refusal changed prior state: %+v", st)
	}
}

func TestRunRefusesImplicitKeystoneDowngradeBeforeFetch(t *testing.T) {
	stateDir := t.TempDir()
	if err := SaveState(stateDir, &State{NodeID: "alpha", MembershipEpoch: 0, MembershipVerified: true, LastCompiledAt: "2026-07-15T00:00:00Z"}); err != nil {
		t.Fatalf("seed state: %v", err)
	}
	cfg := &Config{
		NodeID:   "alpha",
		Source:   NewDirSource(t.TempDir()), // no bundle: trust refusal must happen before Fetch
		StateDir: stateDir,
		Stdout:   &strings.Builder{},
		Stderr:   &strings.Builder{},
	}
	if _, err := Run(cfg); err == nil || !strings.Contains(err.Error(), "refusing trust downgrade") {
		t.Fatalf("Run accepted missing credential after keystone use, err=%v", err)
	}
	st, err := LoadState(stateDir)
	if err != nil {
		t.Fatalf("reload state: %v", err)
	}
	if st.MembershipEpoch != 0 || !st.MembershipVerified {
		t.Fatalf("trust refusal changed epoch-zero membership marker: %+v", st)
	}
}

// TestRecordSuccessEpochFloorMonotonic pins bug #2: recordSuccess must persist
// max(membershipEpoch, prev.MembershipEpoch) — a successful apply must NEVER lower the
// anti-rollback floor. The trap: a keystone-OFF apply reports membershipEpoch==0
// (VerifyMembership is a no-op without a pinned credential), so a node that locked in floor
// E under the keystone, then ran once with it disabled, would reset its floor to 0 — and
// afterward accept a replayed older-but-validly-signed (E-1) membership once re-enabled.
func TestRecordSuccessEpochFloorMonotonic(t *testing.T) {
	man := &manifestInfo{NodeID: "n1", CompiledAt: "2026-06-16T00:00:00Z", Checksum: "abc"}

	t.Run("keystone-OFF apply (epoch 0) preserves a prior floor", func(t *testing.T) {
		dir := t.TempDir()
		cfg := &Config{NodeID: "n1", StateDir: dir}
		prev := &State{NodeID: "n1", MembershipEpoch: 5}
		recordSuccess(cfg, prev, man, &VerifyResult{}, 0) // keystone OFF this run
		st, err := LoadState(dir)
		if err != nil {
			t.Fatalf("LoadState: %v", err)
		}
		if st.MembershipEpoch != 5 {
			t.Fatalf("keystone-OFF apply lowered the anti-rollback floor: got %d, want 5", st.MembershipEpoch)
		}
	})

	t.Run("a real epoch bump advances the floor", func(t *testing.T) {
		dir := t.TempDir()
		cfg := &Config{NodeID: "n1", StateDir: dir}
		prev := &State{NodeID: "n1", MembershipEpoch: 5}
		recordSuccess(cfg, prev, man, &VerifyResult{Signed: true}, 7) // genuine advance
		st, err := LoadState(dir)
		if err != nil {
			t.Fatalf("LoadState: %v", err)
		}
		if st.MembershipEpoch != 7 {
			t.Fatalf("a real epoch bump must advance the floor: got %d, want 7", st.MembershipEpoch)
		}
	})

	t.Run("no prior state takes the applied epoch verbatim", func(t *testing.T) {
		dir := t.TempDir()
		cfg := &Config{NodeID: "n1", StateDir: dir}
		recordSuccess(cfg, nil, man, &VerifyResult{Signed: true}, 3) // first ever apply
		st, err := LoadState(dir)
		if err != nil {
			t.Fatalf("LoadState: %v", err)
		}
		if st.MembershipEpoch != 3 {
			t.Fatalf("first apply must record the applied epoch: got %d, want 3", st.MembershipEpoch)
		}
	})

	t.Run("equal prior and applied epoch is preserved", func(t *testing.T) {
		dir := t.TempDir()
		cfg := &Config{NodeID: "n1", StateDir: dir}
		prev := &State{NodeID: "n1", MembershipEpoch: 5}
		recordSuccess(cfg, prev, man, &VerifyResult{Signed: true}, 5) // idempotent re-apply at the same epoch
		st, err := LoadState(dir)
		if err != nil {
			t.Fatalf("LoadState: %v", err)
		}
		if st.MembershipEpoch != 5 {
			t.Fatalf("equal-epoch re-apply must keep the floor: got %d, want 5", st.MembershipEpoch)
		}
	})

	t.Run("a lower NON-ZERO applied epoch is floored to the higher prior", func(t *testing.T) {
		// Proves the guard is max(applied, prev) generally — not merely a keystone-OFF (epoch 0)
		// special case. prev=5, applied=3 (a stale/older but validly-signed manifest) must NOT lower
		// the floor below 5.
		dir := t.TempDir()
		cfg := &Config{NodeID: "n1", StateDir: dir}
		prev := &State{NodeID: "n1", MembershipEpoch: 5}
		recordSuccess(cfg, prev, man, &VerifyResult{Signed: true}, 3)
		st, err := LoadState(dir)
		if err != nil {
			t.Fatalf("LoadState: %v", err)
		}
		if st.MembershipEpoch != 5 {
			t.Fatalf("a lower applied epoch must be floored to the prior: got %d, want 5", st.MembershipEpoch)
		}
	})
}

// resign recomputes checksums.sha256 over the current checksummed file set and
// re-signs it, keeping the fixture internally consistent after a test mutates a
// checksummed file (e.g. install.sh).
func resign(t *testing.T, b *bundleFixture) {
	t.Helper()
	checksummed := map[string]string{}
	for _, p := range []string{"install.sh", "wireguard/wg-alpha.conf", "babel/babeld.conf", "sysctl/99-overlay.conf"} {
		checksummed[p] = string(b.files[p])
	}
	canonical := bundlesig.Canonicalize(checksummed)
	b.files["checksums.sha256"] = canonical
	sig := bundlesig.Sign(canonical, b.priv)
	b.files["bundle.sig"] = []byte(base64.StdEncoding.EncodeToString(sig) + "\n")
}
