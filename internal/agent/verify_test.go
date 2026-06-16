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
