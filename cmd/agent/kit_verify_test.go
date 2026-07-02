package main

import (
	"archive/zip"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/kunorikiku/yet-another-overlay-generator/internal/bundlesig"
)

// buildSignedBundleFiles returns a valid, signed bundle (flat filename -> bytes) plus the signing
// public-key PEM, mirroring the export path: checksums.sha256 = Canonicalize over the checksummed set,
// bundle.sig = Sign over that canonical content, signing-pubkey.pem alongside. Keystone membership is
// NOT included — these tests exercise the keystone-OFF path (no --operator-cred), so VerifyMembership
// is a no-op and no signed manifest is required.
func buildSignedBundleFiles(t *testing.T) (map[string][]byte, []byte) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate ed25519 key: %v", err)
	}
	checksummed := map[string]string{
		"install.sh":              "#!/usr/bin/env bash\necho stub-install\n",
		"wireguard/wg-alpha.conf": "[Interface]\nPrivateKey = PRIVATEKEY_PLACEHOLDER\n",
	}
	canonical := bundlesig.Canonicalize(checksummed)
	sig := bundlesig.Sign(canonical, priv)
	pubPEM := bundlesig.MarshalPublicKeyPEM(pub)
	files := map[string][]byte{
		"checksums.sha256":   canonical,
		"bundle.sig":         []byte(base64.StdEncoding.EncodeToString(sig) + "\n"),
		"signing-pubkey.pem": pubPEM,
	}
	for p, c := range checksummed {
		files[p] = []byte(c)
	}
	return files, pubPEM
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
	if code := runKitVerify([]string{"--node-id", "n", "--bundle", noSums}); code != 1 {
		t.Errorf("bundle without checksums.sha256 = %d, want 1", code)
	}
	// A file tampered after signing (its listed hash no longer matches).
	tam, _ := buildSignedBundleFiles(t)
	tam["install.sh"] = []byte("#!/usr/bin/env bash\necho TAMPERED\n")
	if code := runKitVerify([]string{"--node-id", "n", "--bundle", writeFilesToDir(t, tam)}); code != 1 {
		t.Errorf("tampered bundle = %d, want 1", code)
	}
	// A pinned pubkey that does not match the signer.
	files, _ := buildSignedBundleFiles(t)
	otherPub, _, _ := ed25519.GenerateKey(rand.Reader)
	otherPEM := filepath.Join(t.TempDir(), "other.pem")
	if err := os.WriteFile(otherPEM, bundlesig.MarshalPublicKeyPEM(otherPub), 0o644); err != nil {
		t.Fatalf("write other pem: %v", err)
	}
	if code := runKitVerify([]string{"--node-id", "n", "--bundle", writeFilesToDir(t, files), "--pubkey", otherPEM}); code != 1 {
		t.Errorf("wrong pinned key = %d, want 1", code)
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
				code = runKitVerify([]string{"--node-id", "alpha", "--bundle", tc.bundle, "--pubkey", pemPath})
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
