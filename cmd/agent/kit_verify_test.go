package main

import (
	"archive/zip"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kunorikiku/yet-another-overlay-generator/internal/agent"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/bundlesig"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/trustlist"
)

type testBundleSigner struct {
	priv   ed25519.PrivateKey
	pubPEM []byte
}

func newTestBundleSigner(t *testing.T) testBundleSigner {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate ed25519 key: %v", err)
	}
	return testBundleSigner{priv: priv, pubPEM: bundlesig.MarshalPublicKeyPEM(pub)}
}

// buildSignedBundleFiles returns a valid signed bundle plus its signing public key. The manifest is
// intentionally outside checksums.sha256, matching export, while install.sh and the WireGuard config
// are covered by the canonical checksum file and its Ed25519 signature.
func buildSignedBundleFiles(t *testing.T) (map[string][]byte, []byte) {
	t.Helper()
	signer := newTestBundleSigner(t)
	return buildSignedBundleFilesWith(t, signer, "#!/usr/bin/env bash\necho stub-install\n", "2026-07-15T00:00:00Z"), signer.pubPEM
}

func buildSignedBundleFilesWith(t *testing.T, signer testBundleSigner, installScript, compiledAt string) map[string][]byte {
	t.Helper()
	checksummed := map[string]string{
		"install.sh":              installScript,
		"wireguard/wg-alpha.conf": "[Interface]\nPrivateKey = PRIVATEKEY_PLACEHOLDER\n",
	}
	canonical := bundlesig.Canonicalize(checksummed)
	sig := bundlesig.Sign(canonical, signer.priv)
	manifestSum := sha256.Sum256(canonical)
	manifest, err := json.Marshal(map[string]string{
		"node_id":     "alpha",
		"compiled_at": compiledAt,
		"checksum":    hex.EncodeToString(manifestSum[:]),
	})
	if err != nil {
		t.Fatalf("marshal manifest: %v", err)
	}
	files := map[string][]byte{
		"checksums.sha256":   canonical,
		"bundle.sig":         []byte(base64.StdEncoding.EncodeToString(sig) + "\n"),
		"signing-pubkey.pem": signer.pubPEM,
		"manifest.json":      manifest,
	}
	for p, c := range checksummed {
		files[p] = []byte(c)
	}
	return files
}

type testOperatorSigner struct {
	signer trustlist.Signer
	pubPEM []byte
}

func newTestOperatorSigner(t *testing.T) testOperatorSigner {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate operator ed25519 key: %v", err)
	}
	return testOperatorSigner{
		signer: trustlist.NewEd25519Signer(priv),
		pubPEM: bundlesig.MarshalPublicKeyPEM(pub),
	}
}

func attachSignedTrustList(t *testing.T, files map[string][]byte, operator testOperatorSigner, nodeID string, epoch int64) {
	t.Helper()
	digest := sha256.Sum256(files["checksums.sha256"])
	tl := trustlist.TrustList{
		SchemaVersion: 1,
		Tenant:        "test",
		Epoch:         epoch,
		Members: []trustlist.Member{{
			NodeID:       nodeID,
			WGPublicKey:  "test-self-public-key",
			BundleSHA256: hex.EncodeToString(digest[:]),
		}},
	}
	canonical, err := trustlist.Canonical(tl)
	if err != nil {
		t.Fatalf("canonicalize trust list: %v", err)
	}
	artifact, err := operator.signer.Sign(tl)
	if err != nil {
		t.Fatalf("sign trust list: %v", err)
	}
	artifactJSON, err := json.Marshal(artifact)
	if err != nil {
		t.Fatalf("marshal trust-list signature: %v", err)
	}
	files["trustlist.json"] = canonical
	files["trustlist.sig"] = artifactJSON
}

func writeTestPEM(t *testing.T, name string, pemBytes []byte) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, pemBytes, 0o644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
	return p
}

// writeFilesToDir writes a flat/relative file map into a fresh temp dir and returns its path.
func writeFilesToDir(t *testing.T, files map[string][]byte) string {
	t.Helper()
	dir := t.TempDir()
	for name, data := range files {
		p := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(p, data, 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	return dir
}

// writeFilesToZip writes a flat file map into a .zip and returns its path.
func writeFilesToZip(t *testing.T, files map[string][]byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "bundle.zip")
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("create zip: %v", err)
	}
	defer f.Close()
	zw := zip.NewWriter(f)
	for name, data := range files {
		w, cErr := zw.Create(name)
		if cErr != nil {
			t.Fatalf("zip create %s: %v", name, cErr)
		}
		if _, wErr := w.Write(data); wErr != nil {
			t.Fatalf("zip write %s: %v", name, wErr)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("zip close: %v", err)
	}
	return path
}

// TestRunKitVerify_Usage: --bundle and --node-id are required, and a nonexistent bundle is an IO error
// (exit 2), distinct from a verification failure (exit 1).
func TestRunKitVerify_Usage(t *testing.T) {
	if code := runKitVerify([]string{}); code != 2 {
		t.Errorf("no flags = %d, want 2", code)
	}
	if code := runKitVerify([]string{"--bundle", "/x"}); code != 2 {
		t.Errorf("missing --node-id = %d, want 2", code)
	}
	if code := runKitVerify([]string{"--node-id", "n", "--bundle", filepath.Join(t.TempDir(), "nope")}); code != 2 {
		t.Errorf("nonexistent bundle = %d, want 2", code)
	}
}

// TestRunKitVerify_FailClosed: verification failures (missing checksums, tampered file, wrong pinned
// key) all exit 1 — the whole point is that a hand-installing operator learns BEFORE running install.sh.
func TestRunKitVerify_FailClosed(t *testing.T) {
	// No checksums.sha256 at all.
	noSums := writeFilesToDir(t, map[string][]byte{"install.sh": []byte("#!/bin/sh\necho hi\n")})
	if code := runKitVerify([]string{"--node-id", "n", "--bundle", noSums, "--dangerously-allow-no-keystone"}); code != 1 {
		t.Errorf("bundle without checksums.sha256 = %d, want 1", code)
	}
	// A file tampered after signing (its listed hash no longer matches).
	tam, _ := buildSignedBundleFiles(t)
	tam["install.sh"] = []byte("#!/usr/bin/env bash\necho TAMPERED\n")
	if code := runKitVerify([]string{"--node-id", "n", "--bundle", writeFilesToDir(t, tam), "--dangerously-allow-no-keystone"}); code != 1 {
		t.Errorf("tampered bundle = %d, want 1", code)
	}
	// A pinned pubkey that does not match the signer.
	files, _ := buildSignedBundleFiles(t)
	otherPub, _, _ := ed25519.GenerateKey(rand.Reader)
	otherPEM := filepath.Join(t.TempDir(), "other.pem")
	if err := os.WriteFile(otherPEM, bundlesig.MarshalPublicKeyPEM(otherPub), 0o644); err != nil {
		t.Fatalf("write other pem: %v", err)
	}
	if code := runKitVerify([]string{"--node-id", "n", "--bundle", writeFilesToDir(t, files), "--pubkey", otherPEM, "--dangerously-allow-no-keystone"}); code != 1 {
		t.Errorf("wrong pinned key = %d, want 1", code)
	}
}

func TestRunKitVerifyRejectsUnmaterializablePathSets(t *testing.T) {
	signer := newTestBundleSigner(t)
	pemPath := writeTestPEM(t, "signing.pem", signer.pubPEM)
	tests := map[string]func(map[string][]byte){
		"drive-qualified name": func(files map[string][]byte) {
			files["C:/escape"] = []byte("unchecked alias")
		},
		"case-fold collision": func(files map[string][]byte) {
			files["README.txt"] = []byte("first")
			files["readme.txt"] = []byte("second")
		},
		"file-versus-parent conflict": func(files map[string][]byte) {
			// The valid fixture already contains wireguard/wg-alpha.conf.
			files["wireguard"] = []byte("conflicting parent file")
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			files := buildSignedBundleFilesWith(t, signer, "#!/usr/bin/env bash\necho stub-install\n", "2026-07-15T00:00:00Z")
			mutate(files)
			bundle := writeFilesToZip(t, files)
			var code int
			stderr := captureStderr(t, func() {
				code = runKitVerify([]string{
					"--node-id", "alpha",
					"--bundle", bundle,
					"--pubkey", pemPath,
					"--dangerously-allow-no-keystone",
				})
			})
			if code != 1 {
				t.Fatalf("kit verify exit = %d, want verification failure 1; stderr=%q", code, stderr)
			}
			if !strings.Contains(stderr, "bundle materialization preflight FAILED") {
				t.Fatalf("kit verify rejected for the wrong reason; stderr=%q", stderr)
			}
		})
	}
}

// TestRunKitVerify_Success: a correctly signed bundle verified against the matching pin exits 0 from
// BOTH a directory and a .zip; the JSON result reports ok+signed and (keystone OFF) node_is_member=false.
func TestRunKitVerify_Success(t *testing.T) {
	files, pubPEM := buildSignedBundleFiles(t)
	pemPath := filepath.Join(t.TempDir(), "signing.pem")
	if err := os.WriteFile(pemPath, pubPEM, 0o644); err != nil {
		t.Fatalf("write pem: %v", err)
	}
	cases := []struct {
		name   string
		bundle string
	}{
		{"dir", writeFilesToDir(t, files)},
		{"zip", writeFilesToZip(t, files)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var code int
			out := captureStdout(t, func() {
				code = runKitVerify([]string{"--node-id", "alpha", "--bundle", tc.bundle, "--pubkey", pemPath, "--dangerously-allow-no-keystone"})
			})
			if code != 0 {
				t.Fatalf("verify signed+pinned %s = %d, want 0\n%s", tc.name, code, out)
			}
			var res kitVerifyResult
			if err := json.Unmarshal([]byte(out), &res); err != nil {
				t.Fatalf("stdout is not a kitVerifyResult: %v\n%s", err, out)
			}
			if !res.OK || !res.Signed {
				t.Errorf("result = %+v, want ok+signed", res)
			}
			if res.NodeIsMember {
				t.Errorf("node_is_member = true, but no operator credential was supplied (keystone OFF)")
			}
			// file_count is the number of files whose checksum was VERIFIED (the 2 checksummed entries),
			// not the total loaded (which also counts checksums.sha256/bundle.sig/signing-pubkey.pem).
			if res.FileCount != 2 {
				t.Errorf("file_count = %d, want 2 (verified files, not loaded meta-files)", res.FileCount)
			}
		})
	}
}

// TestLoadBundleFiles_DirAndZip: the loader yields the same flat, slash-separated key map from a
// directory tree and from a .zip.
func TestLoadBundleFiles_DirAndZip(t *testing.T) {
	src := map[string][]byte{"a.txt": []byte("A"), "sub/b.txt": []byte("B")}
	for _, tc := range []struct {
		name string
		path string
	}{
		{"dir", writeFilesToDir(t, src)},
		{"zip", writeFilesToZip(t, src)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := loadBundleFiles(tc.path)
			if err != nil {
				t.Fatalf("loadBundleFiles(%s): %v", tc.name, err)
			}
			if len(got) != 2 || string(got["a.txt"]) != "A" || string(got["sub/b.txt"]) != "B" {
				t.Errorf("%s load = %d keys, want flat slash keys a.txt + sub/b.txt", tc.name, len(got))
			}
		})
	}
}

func TestRunKitApply_ExitCodesAndSecureDefault(t *testing.T) {
	if code := runKitApply(nil); code != 2 {
		t.Fatalf("no flags = %d, want usage exit 2", code)
	}
	if code := runKitApply([]string{"--bundle", filepath.Join(t.TempDir(), "missing"), "--node-id", "alpha"}); code != 2 {
		t.Fatalf("missing bundle = %d, want input/IO exit 2", code)
	}

	signer := newTestBundleSigner(t)
	sentinel := filepath.Join(t.TempDir(), "applied")
	script := fmt.Sprintf("#!/usr/bin/env bash\nprintf applied > %q\n", sentinel)
	files := buildSignedBundleFilesWith(t, signer, script, "2026-07-15T01:00:00Z")
	bundle := writeFilesToDir(t, files)
	pubPath := writeTestPEM(t, "bundle-signing.pem", signer.pubPEM)
	stateDir := t.TempDir()

	// A stripped or legacy bundle cannot silently turn the off-host membership gate off. The
	// acknowledgement is intentionally long/loud and valid only for a state that never used it.
	if code := runKitApply([]string{"--bundle", bundle, "--node-id", "alpha", "--pubkey", pubPath, "--state-dir", stateDir}); code != 2 {
		t.Fatalf("no operator credential and no acknowledgement = %d, want 2", code)
	}
	if _, err := os.Stat(sentinel); !os.IsNotExist(err) {
		t.Fatalf("install ran despite secure-default refusal: %v", err)
	}
	if code := runKitApply([]string{"--bundle", bundle, "--node-id", "alpha", "--pubkey", pubPath, "--state-dir", stateDir, "--dangerously-allow-no-keystone"}); code != 0 {
		t.Fatalf("explicit legacy no-keystone apply = %d, want 0", code)
	}
	if _, err := os.Stat(sentinel); err != nil {
		t.Fatalf("acknowledged legacy install did not run: %v", err)
	}

	// Script failures are runtime/apply failures (1), not usage errors (2), while still running only
	// the verified copy.
	failing := buildSignedBundleFilesWith(t, signer, "#!/usr/bin/env bash\nexit 42\n", "2026-07-15T02:00:00Z")
	if code := runKitApply([]string{"--bundle", writeFilesToDir(t, failing), "--node-id", "alpha", "--pubkey", pubPath, "--state-dir", t.TempDir(), "--dangerously-allow-no-keystone"}); code != 1 {
		t.Fatalf("verified install.sh exit 42 = %d, want apply-failure exit 1", code)
	}
}

func TestRunKitApply_RequiresDurableStateBeforeAndAfterApply(t *testing.T) {
	signer := newTestBundleSigner(t)
	pubPath := writeTestPEM(t, "bundle-signing.pem", signer.pubPEM)

	t.Run("unwritable_before_apply_never_executes", func(t *testing.T) {
		stateDir := t.TempDir()
		if err := os.Chmod(stateDir, 0o500); err != nil {
			t.Fatalf("make state dir read-only: %v", err)
		}
		defer os.Chmod(stateDir, 0o700)
		sentinel := filepath.Join(t.TempDir(), "must-not-run")
		files := buildSignedBundleFilesWith(t, signer, fmt.Sprintf("#!/usr/bin/env bash\ntouch %q\n", sentinel), "2026-07-15T02:30:00Z")
		code := runKitApply([]string{
			"--bundle", writeFilesToDir(t, files),
			"--node-id", "alpha",
			"--pubkey", pubPath,
			"--state-dir", stateDir,
			"--dangerously-allow-no-keystone",
		})
		if code != 2 {
			t.Fatalf("unwritable state preflight = %d, want input/IO exit 2", code)
		}
		if _, err := os.Stat(sentinel); !os.IsNotExist(err) {
			t.Fatalf("installer ran despite unwritable state: %v", err)
		}
	})

	t.Run("post_apply_persistence_loss_is_not_reported_as_success", func(t *testing.T) {
		stateDir := t.TempDir()
		defer os.Chmod(stateDir, 0o700)
		sentinel := filepath.Join(t.TempDir(), "did-run")
		// The verified test script simulates storage becoming read-only between the preflight and
		// recordSuccess. The host change has happened, so kit apply must loudly return 1 instead of
		// claiming that its anti-rollback floor is durable.
		script := fmt.Sprintf("#!/usr/bin/env bash\ntouch %q\nchmod 0500 %q\n", sentinel, stateDir)
		files := buildSignedBundleFilesWith(t, signer, script, "2026-07-15T02:45:00Z")
		code := runKitApply([]string{
			"--bundle", writeFilesToDir(t, files),
			"--node-id", "alpha",
			"--pubkey", pubPath,
			"--state-dir", stateDir,
			"--dangerously-allow-no-keystone",
		})
		if code != 1 {
			t.Fatalf("lost post-apply persistence = %d, want failure exit 1", code)
		}
		if _, err := os.Stat(sentinel); err != nil {
			t.Fatalf("test did not reach the simulated post-apply failure: %v", err)
		}
	})

	t.Run("post_rename_directory_sync_failure_is_not_reported_as_success", func(t *testing.T) {
		stateDir := t.TempDir()
		sentinel := filepath.Join(t.TempDir(), "did-run")
		files := buildSignedBundleFilesWith(t, signer, fmt.Sprintf("#!/usr/bin/env bash\ntouch %q\n", sentinel), "2026-07-15T02:50:00Z")
		var code int
		stderr := captureStderr(t, func() {
			code = runKitApplyWithStateSaver([]string{
				"--bundle", writeFilesToDir(t, files),
				"--node-id", "alpha",
				"--pubkey", pubPath,
				"--state-dir", stateDir,
				"--dangerously-allow-no-keystone",
			}, func(dir string, state *agent.State) error {
				// Model SaveState's post-rename failure exactly: the new JSON is visible and
				// reloadable, but the durability operation reports that the parent directory
				// entry was not synchronized.
				if err := agent.SaveState(dir, state); err != nil {
					return err
				}
				return errors.New("injected state-directory sync failure after rename")
			})
		})
		if code != 1 {
			t.Fatalf("post-rename durability failure = %d, want failure exit 1", code)
		}
		if _, err := os.Stat(sentinel); err != nil {
			t.Fatalf("test did not reach the post-apply persistence failure: %v", err)
		}
		if _, err := agent.LoadState(stateDir); err != nil {
			t.Fatalf("renamed state should be visible despite unproven durability: %v", err)
		}
		if !strings.Contains(stderr, "anti-rollback state is not durable") || !strings.Contains(stderr, "injected state-directory sync failure") {
			t.Fatalf("kit apply did not surface the actual durability failure:\n%s", stderr)
		}
		if strings.Contains(stderr, "agent: applied generation") {
			t.Fatalf("kit apply falsely printed durable success after sync failure:\n%s", stderr)
		}
	})
}

func TestRunKitApply_ExecutesOnlyVerifiedTemporarySnapshot(t *testing.T) {
	for _, kind := range []string{"dir", "zip"} {
		t.Run(kind, func(t *testing.T) {
			signer := newTestBundleSigner(t)
			record := filepath.Join(t.TempDir(), "execution-paths")
			script := fmt.Sprintf("#!/usr/bin/env bash\nset -eu\nprintf '%%s\\n' \"$PWD\" > %q\nprintf '%%s\\n' \"$0\" >> %q\n", record, record)
			files := buildSignedBundleFilesWith(t, signer, script, "2026-07-15T03:00:00Z")
			var source string
			if kind == "dir" {
				source = writeFilesToDir(t, files)
			} else {
				source = writeFilesToZip(t, files)
			}
			pubPath := writeTestPEM(t, "bundle-signing.pem", signer.pubPEM)
			code := runKitApply([]string{
				"--bundle", source,
				"--node-id", "alpha",
				"--pubkey", pubPath,
				"--state-dir", t.TempDir(),
				"--dangerously-allow-no-keystone",
			})
			if code != 0 {
				t.Fatalf("kit apply %s = %d, want 0", kind, code)
			}
			raw, err := os.ReadFile(record)
			if err != nil {
				t.Fatalf("read execution record: %v", err)
			}
			lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
			if len(lines) != 2 {
				t.Fatalf("execution record = %q, want cwd + script path", raw)
			}
			cwd, scriptPath := filepath.Clean(lines[0]), filepath.Clean(lines[1])
			if scriptPath != filepath.Join(cwd, "install.sh") {
				t.Fatalf("script %q did not execute from recorded staging cwd %q", scriptPath, cwd)
			}
			if kind == "dir" && (cwd == filepath.Clean(source) || strings.HasPrefix(scriptPath, filepath.Clean(source)+string(os.PathSeparator))) {
				t.Fatalf("untrusted source directory was executed in place: cwd=%q script=%q", cwd, scriptPath)
			}
			if scriptPath == filepath.Clean(source) {
				t.Fatalf("untrusted source archive/path was passed to bash: %q", source)
			}
			if _, err := os.Stat(cwd); !os.IsNotExist(err) {
				t.Fatalf("owned temporary staging directory was not removed after apply: %q (%v)", cwd, err)
			}
		})
	}
}

func TestRunKitApply_KeystoneCredentialAndVerifiedUninstall(t *testing.T) {
	bundleSigner := newTestBundleSigner(t)
	operator := newTestOperatorSigner(t)
	operatorPath := writeTestPEM(t, "operator.pem", operator.pubPEM)
	bundlePubPath := writeTestPEM(t, "bundle-signing.pem", bundleSigner.pubPEM)
	sentinel := filepath.Join(t.TempDir(), "arg")
	script := fmt.Sprintf("#!/usr/bin/env bash\nprintf '%%s' \"${1:-install}\" > %q\n", sentinel)
	files := buildSignedBundleFilesWith(t, bundleSigner, script, "2026-07-15T04:00:00Z")
	attachSignedTrustList(t, files, operator, "alpha", 7)
	bundle := writeFilesToDir(t, files)
	stateDir := t.TempDir()

	base := []string{"--bundle", bundle, "--node-id", "alpha", "--pubkey", bundlePubPath, "--state-dir", stateDir, "--uninstall"}
	if code := runKitApply(base); code != 2 {
		t.Fatalf("trust-list bundle without out-of-band credential = %d, want 2", code)
	}
	if code := runKitApply(append(append([]string{}, base...), "--dangerously-allow-no-keystone")); code != 2 {
		t.Fatalf("dangerous legacy flag bypassed present trust-list = %d, want 2", code)
	}
	if _, err := os.Stat(sentinel); !os.IsNotExist(err) {
		t.Fatalf("uninstall script ran without membership verification: %v", err)
	}

	wrong := newTestOperatorSigner(t)
	wrongPath := writeTestPEM(t, "wrong-operator.pem", wrong.pubPEM)
	wrongArgs := append(append([]string{}, base...), "--operator-cred", wrongPath, "--operator-cred-alg", "ed25519")
	if code := runKitApply(wrongArgs); code != 1 {
		t.Fatalf("wrong operator credential = %d, want verification exit 1", code)
	}
	if _, err := os.Stat(sentinel); !os.IsNotExist(err) {
		t.Fatalf("uninstall script ran after wrong-credential refusal: %v", err)
	}

	goodArgs := append(append([]string{}, base...), "--operator-cred", operatorPath, "--operator-cred-alg", "ed25519")
	var goodCode int
	stderr := captureStderr(t, func() { goodCode = runKitApply(goodArgs) })
	if goodCode != 0 {
		t.Fatalf("verified keystone uninstall = %d, want 0", goodCode)
	}
	got, err := os.ReadFile(sentinel)
	if err != nil {
		t.Fatalf("verified uninstall did not run: %v", err)
	}
	if string(got) != "--uninstall" {
		t.Fatalf("install.sh arg = %q, want --uninstall", got)
	}
	if !strings.Contains(stderr, "agent: uninstalled generation") || strings.Contains(stderr, "agent: applied generation") {
		t.Fatalf("uninstall summary was not truthful:\n%s", stderr)
	}
	st, err := agent.LoadState(stateDir)
	if err != nil {
		t.Fatalf("load uninstall state: %v", err)
	}
	if st.LastResult != agent.LastResultOK || st.LastAction != agent.LastActionUninstall || st.Health != "uninstalled" || st.MembershipEpoch != 7 || !st.MembershipVerified {
		t.Fatalf("uninstall state/floors = %+v, want truthful uninstall with epoch 7 preserved", st)
	}
	if len(st.Conditions) == 0 || st.Conditions[0].Reason != "Uninstalled" || st.Conditions[0].Message != "configuration uninstalled" {
		t.Fatalf("uninstall condition = %+v, want configapply/Uninstalled", st.Conditions)
	}
}

func TestRunKitApply_PersistsEpochAndRejectsReplayOrTrustStripping(t *testing.T) {
	bundleSigner := newTestBundleSigner(t)
	operator := newTestOperatorSigner(t)
	bundlePubPath := writeTestPEM(t, "bundle-signing.pem", bundleSigner.pubPEM)
	operatorPath := writeTestPEM(t, "operator.pem", operator.pubPEM)
	stateDir := t.TempDir()
	newSentinel := filepath.Join(t.TempDir(), "new-applied")
	oldSentinel := filepath.Join(t.TempDir(), "old-applied")
	strippedSentinel := filepath.Join(t.TempDir(), "stripped-applied")

	newFiles := buildSignedBundleFilesWith(t, bundleSigner, fmt.Sprintf("#!/usr/bin/env bash\ntouch %q\n", newSentinel), "2026-07-15T05:00:00Z")
	attachSignedTrustList(t, newFiles, operator, "alpha", 5)
	common := []string{
		"--node-id", "alpha",
		"--pubkey", bundlePubPath,
		"--operator-cred", operatorPath,
		"--operator-cred-alg", "ed25519",
		"--state-dir", stateDir,
	}
	if code := runKitApply(append([]string{"--bundle", writeFilesToDir(t, newFiles)}, common...)); code != 0 {
		t.Fatalf("initial epoch-5 apply = %d, want 0", code)
	}
	if _, err := os.Stat(newSentinel); err != nil {
		t.Fatalf("initial verified apply did not execute: %v", err)
	}
	st, err := agent.LoadState(stateDir)
	if err != nil {
		t.Fatalf("load manual apply state: %v", err)
	}
	if st.NodeID != "alpha" || st.MembershipEpoch != 5 {
		t.Fatalf("persisted state = %+v, want node alpha / epoch 5", st)
	}

	// compiled_at is newer so the membership epoch is the gate being exercised.
	oldFiles := buildSignedBundleFilesWith(t, bundleSigner, fmt.Sprintf("#!/usr/bin/env bash\ntouch %q\n", oldSentinel), "2026-07-15T06:00:00Z")
	attachSignedTrustList(t, oldFiles, operator, "alpha", 4)
	if code := runKitApply(append([]string{"--bundle", writeFilesToDir(t, oldFiles)}, common...)); code != 1 {
		t.Fatalf("older signed membership epoch = %d, want refusal exit 1", code)
	}
	if _, err := os.Stat(oldSentinel); !os.IsNotExist(err) {
		t.Fatalf("replayed install executed despite epoch floor: %v", err)
	}

	// A controller able to rebuild the tier-1 bundle signature can strip both trust-list files.
	// Even the explicit legacy acknowledgement cannot downgrade state that has used the keystone.
	stripped := buildSignedBundleFilesWith(t, bundleSigner, fmt.Sprintf("#!/usr/bin/env bash\ntouch %q\n", strippedSentinel), "2026-07-15T07:00:00Z")
	strippedArgs := []string{
		"--bundle", writeFilesToDir(t, stripped),
		"--node-id", "alpha",
		"--pubkey", bundlePubPath,
		"--state-dir", stateDir,
		"--dangerously-allow-no-keystone",
	}
	if code := runKitApply(strippedArgs); code != 1 {
		t.Fatalf("trust-list stripping after keystone use = %d, want refusal exit 1", code)
	}
	if _, err := os.Stat(strippedSentinel); !os.IsNotExist(err) {
		t.Fatalf("trust-stripped install executed: %v", err)
	}
	st, err = agent.LoadState(stateDir)
	if err != nil {
		t.Fatalf("reload manual apply state: %v", err)
	}
	if st.MembershipEpoch != 5 {
		t.Fatalf("refusals changed epoch floor to %d, want 5", st.MembershipEpoch)
	}
}

func TestRunKitApply_InitialEpochZeroStillMakesKeystoneSticky(t *testing.T) {
	bundleSigner := newTestBundleSigner(t)
	operator := newTestOperatorSigner(t)
	bundlePubPath := writeTestPEM(t, "bundle-signing.pem", bundleSigner.pubPEM)
	operatorPath := writeTestPEM(t, "operator.pem", operator.pubPEM)
	stateDir := t.TempDir()
	firstSentinel := filepath.Join(t.TempDir(), "epoch-zero-applied")
	strippedSentinel := filepath.Join(t.TempDir(), "stripped-applied")

	first := buildSignedBundleFilesWith(t, bundleSigner, fmt.Sprintf("#!/usr/bin/env bash\ntouch %q\n", firstSentinel), "2026-07-15T08:00:00Z")
	attachSignedTrustList(t, first, operator, "alpha", 0) // zero is the controller's valid first epoch
	if code := runKitApply([]string{
		"--bundle", writeFilesToDir(t, first),
		"--node-id", "alpha",
		"--pubkey", bundlePubPath,
		"--operator-cred", operatorPath,
		"--operator-cred-alg", "ed25519",
		"--state-dir", stateDir,
	}); code != 0 {
		t.Fatalf("initial epoch-zero keystone apply = %d, want 0", code)
	}
	st, err := agent.LoadState(stateDir)
	if err != nil {
		t.Fatalf("load epoch-zero state: %v", err)
	}
	if st.MembershipEpoch != 0 || !st.MembershipVerified {
		t.Fatalf("epoch-zero membership was not durably marked as verified: %+v", st)
	}

	stripped := buildSignedBundleFilesWith(t, bundleSigner, fmt.Sprintf("#!/usr/bin/env bash\ntouch %q\n", strippedSentinel), "2026-07-15T09:00:00Z")
	if code := runKitApply([]string{
		"--bundle", writeFilesToDir(t, stripped),
		"--node-id", "alpha",
		"--pubkey", bundlePubPath,
		"--state-dir", stateDir,
		"--dangerously-allow-no-keystone",
	}); code != 1 {
		t.Fatalf("trust stripping after epoch-zero membership = %d, want refusal exit 1", code)
	}
	if _, err := os.Stat(strippedSentinel); !os.IsNotExist(err) {
		t.Fatalf("trust-stripped installer ran after epoch-zero keystone use: %v", err)
	}
}

func TestRunKitVerify_KeystoneCannotBeSkipped(t *testing.T) {
	bundleSigner := newTestBundleSigner(t)
	operator := newTestOperatorSigner(t)
	files := buildSignedBundleFilesWith(t, bundleSigner, "#!/usr/bin/env bash\nexit 0\n", "2026-07-15T08:00:00Z")
	attachSignedTrustList(t, files, operator, "alpha", 9)
	bundle := writeFilesToZip(t, files)
	bundlePubPath := writeTestPEM(t, "bundle-signing.pem", bundleSigner.pubPEM)
	operatorPath := writeTestPEM(t, "operator.pem", operator.pubPEM)

	if code := runKitVerify([]string{"--bundle", bundle, "--node-id", "alpha", "--pubkey", bundlePubPath, "--dangerously-allow-no-keystone"}); code != 2 {
		t.Fatalf("legacy acknowledgement bypassed present trust-list = %d, want 2", code)
	}
	if code := runKitVerify([]string{"--bundle", bundle, "--node-id", "alpha", "--pubkey", bundlePubPath, "--operator-cred", operatorPath, "--operator-cred-alg", "ed25519"}); code != 0 {
		t.Fatalf("verified trust-list audit = %d, want 0", code)
	}
}

func TestLoadBundleFiles_RejectsAmbiguousOrEscapingEntries(t *testing.T) {
	writeZipEntries := func(t *testing.T, names ...string) string {
		t.Helper()
		p := filepath.Join(t.TempDir(), "bad.zip")
		f, err := os.Create(p)
		if err != nil {
			t.Fatalf("create zip: %v", err)
		}
		zw := zip.NewWriter(f)
		for _, name := range names {
			w, err := zw.Create(name)
			if err != nil {
				t.Fatalf("create zip entry %q: %v", name, err)
			}
			if _, err := w.Write([]byte("x")); err != nil {
				t.Fatalf("write zip entry %q: %v", name, err)
			}
		}
		if err := zw.Close(); err != nil {
			t.Fatalf("close zip: %v", err)
		}
		if err := f.Close(); err != nil {
			t.Fatalf("close file: %v", err)
		}
		return p
	}

	if _, err := loadBundleFiles(writeZipEntries(t, "../install.sh")); err == nil {
		t.Fatal("zip traversal entry was accepted")
	}
	if _, err := loadBundleFiles(writeZipEntries(t, "install.sh", "install.sh")); err == nil {
		t.Fatal("duplicate zip entry was accepted")
	}
	dir := t.TempDir()
	target := filepath.Join(t.TempDir(), "target")
	if err := os.WriteFile(target, []byte("x"), 0o644); err != nil {
		t.Fatalf("write symlink target: %v", err)
	}
	if err := os.Symlink(target, filepath.Join(dir, "install.sh")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if _, err := loadBundleFiles(dir); err == nil {
		t.Fatal("directory symlink entry was accepted")
	}
}

func TestLoadBundleFiles_ResourceLimits_DirAndZip(t *testing.T) {
	builders := []struct {
		name string
		make func(*testing.T, map[string][]byte) string
	}{
		{name: "dir", make: writeFilesToDir},
		{name: "zip", make: writeFilesToZip},
	}

	for _, builder := range builders {
		t.Run(builder.name, func(t *testing.T) {
			t.Run("exact_byte_boundaries_are_accepted", func(t *testing.T) {
				// Four files at the per-file ceiling exactly fill the total decompressed ceiling.
				chunk := make([]byte, maxKitBundleFileBytes)
				files := map[string][]byte{
					"one":   chunk,
					"two":   chunk,
					"three": chunk,
					"four":  chunk,
				}
				got, err := loadBundleFiles(builder.make(t, files))
				if err != nil {
					t.Fatalf("exact file/total limits rejected: %v", err)
				}
				if len(got) != len(files) {
					t.Fatalf("loaded %d boundary files, want %d", len(got), len(files))
				}
			})

			t.Run("per_file_oversize_is_rejected", func(t *testing.T) {
				files := map[string][]byte{"too-large": make([]byte, maxKitBundleFileBytes+1)}
				if _, err := loadBundleFiles(builder.make(t, files)); err == nil || !strings.Contains(err.Error(), "per-file limit") {
					t.Fatalf("per-file oversize error = %v, want explicit limit refusal", err)
				}
			})

			t.Run("total_oversize_is_rejected", func(t *testing.T) {
				chunk := make([]byte, maxKitBundleFileBytes)
				files := map[string][]byte{
					"one":      chunk,
					"two":      chunk,
					"three":    chunk,
					"four":     chunk,
					"overflow": []byte("x"),
				}
				if _, err := loadBundleFiles(builder.make(t, files)); err == nil || !strings.Contains(err.Error(), "total decompressed limit") {
					t.Fatalf("total oversize error = %v, want explicit limit refusal", err)
				}
			})

			t.Run("entry_count_oversize_is_rejected", func(t *testing.T) {
				files := make(map[string][]byte, maxKitBundleEntries+1)
				for i := 0; i <= maxKitBundleEntries; i++ {
					files[fmt.Sprintf("entry-%04d", i)] = []byte("x")
				}
				if _, err := loadBundleFiles(builder.make(t, files)); err == nil || !strings.Contains(err.Error(), "entries") {
					t.Fatalf("entry-count oversize error = %v, want explicit limit refusal", err)
				}
			})
		})
	}

	t.Run("compressed_archive_size_is_bounded_before_zip_parse", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "oversize.zip")
		f, err := os.Create(path)
		if err != nil {
			t.Fatalf("create sparse archive: %v", err)
		}
		if err := f.Truncate(maxKitBundleArchiveSize + 1); err != nil {
			f.Close()
			t.Fatalf("truncate sparse archive: %v", err)
		}
		if err := f.Close(); err != nil {
			t.Fatalf("close sparse archive: %v", err)
		}
		if _, err := loadBundleFiles(path); err == nil || !strings.Contains(err.Error(), "compressed archive limit") {
			t.Fatalf("oversize archive error = %v, want pre-parse size refusal", err)
		}
	})
}
