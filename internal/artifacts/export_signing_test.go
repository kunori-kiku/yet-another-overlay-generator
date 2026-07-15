package artifacts

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/kunorikiku/yet-another-overlay-generator/internal/bundlesig"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/compiler"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/model"
)

// newTestResult builds a minimal CompileResult with two nodes (one non-client
// with per-peer WireGuard + Babel, and one client with a single wg0 and no
// Babel) so the export covers both the signed/multi-file and the simpler bundle
// shapes. Export only reads from the maps below and Topology.Nodes, so no full
// compile is required.
func newTestResult() *compiler.CompileResult {
	return &compiler.CompileResult{
		Topology: &model.Topology{
			Nodes: []model.Node{
				{ID: "n1", Name: "alpha", Role: "router", DomainID: "d1", OverlayIP: "10.0.0.1"},
				{ID: "n2", Name: "bravo", Role: "client", DomainID: "d1", OverlayIP: "10.0.0.2"},
			},
		},
		WireGuardConfigs: map[string]string{
			// alpha: two per-peer interfaces (deliberately out of sorted order in
			// map-iteration terms; Canonicalize must sort them deterministically).
			"n1:wg-zulu":  "[Interface]\n# zulu\n",
			"n1:wg-alpha": "[Interface]\n# alpha\n",
			// bravo: single wg0 (client).
			"n2:wg0": "[Interface]\n# client wg0\n",
		},
		BabelConfigs: map[string]string{
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
		Manifest: compiler.CompileManifest{
			ProjectID:   "p1",
			ProjectName: "proj",
			Version:     "1.0.0",
			CompiledAt:  time.Date(2026, 6, 8, 0, 0, 0, 0, time.UTC),
			Checksum:    "deadbeef",
		},
	}
}

// writeTestSigningKey generates a fresh Ed25519 key, writes it as a PKCS#8 PEM
// to a temp file, and returns the path plus the public half for verification.
func writeTestSigningKey(t *testing.T) (string, ed25519.PublicKey) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		t.Fatalf("marshal pkcs8: %v", err)
	}
	pemBytes := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})
	keyPath := filepath.Join(t.TempDir(), "signing-key.pem")
	if err := os.WriteFile(keyPath, pemBytes, 0600); err != nil {
		t.Fatalf("write key: %v", err)
	}
	return keyPath, pub
}

// parsePubPEM parses a PKIX PEM public key the way openssl / install.sh would,
// so the test verifies against exactly what signing-pubkey.pem ships.
func parsePubPEM(t *testing.T, pemBytes []byte) ed25519.PublicKey {
	t.Helper()
	block, _ := pem.Decode(pemBytes)
	if block == nil {
		t.Fatal("signing-pubkey.pem is not valid PEM")
	}
	pub, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		t.Fatalf("parse PKIX public key: %v", err)
	}
	edPub, ok := pub.(ed25519.PublicKey)
	if !ok {
		t.Fatalf("signing-pubkey.pem is not an Ed25519 key: %T", pub)
	}
	return edPub
}

// TestExportSigned verifies that a signed export emits bundle.sig +
// signing-pubkey.pem in every node dir, that the signature verifies against the
// shipped public key over the checksums.sha256 content, and that the checksums
// are sorted/deterministic.
func TestExportSigned(t *testing.T) {
	keyPath, _ := writeTestSigningKey(t)
	t.Setenv(bundlesig.EnvSigningKey, keyPath)

	outDir := t.TempDir()
	if _, err := Export(newTestResult(), outDir); err != nil {
		t.Fatalf("Export: %v", err)
	}

	for _, node := range []string{"n1", "n2"} {
		nodeDir := filepath.Join(outDir, node)

		checksums, err := os.ReadFile(filepath.Join(nodeDir, "checksums.sha256"))
		if err != nil {
			t.Fatalf("%s: read checksums: %v", node, err)
		}
		sigB64, err := os.ReadFile(filepath.Join(nodeDir, "bundle.sig"))
		if err != nil {
			t.Fatalf("%s: read bundle.sig: %v", node, err)
		}
		pubPEM, err := os.ReadFile(filepath.Join(nodeDir, "signing-pubkey.pem"))
		if err != nil {
			t.Fatalf("%s: read signing-pubkey.pem: %v", node, err)
		}

		// The signature is base64 of the raw 64-byte Ed25519 signature.
		sig, err := base64.StdEncoding.DecodeString(string(bytes.TrimSpace(sigB64)))
		if err != nil {
			t.Fatalf("%s: decode bundle.sig: %v", node, err)
		}
		if len(sig) != ed25519.SignatureSize {
			t.Fatalf("%s: signature size = %d, want %d", node, len(sig), ed25519.SignatureSize)
		}

		// The signature must verify against the shipped pubkey over the exact
		// checksums.sha256 bytes (which are the canonical serialization).
		edPub := parsePubPEM(t, pubPEM)
		if !bundlesig.Verify(checksums, sig, edPub) {
			t.Errorf("%s: bundle.sig does not verify against signing-pubkey.pem over checksums.sha256", node)
		}

		// checksums.sha256 must equal Canonicalize over the same file set, which
		// is sorted by path. Independently assert sorted order line-by-line.
		assertChecksumsSorted(t, node, checksums)
	}
}

// TestExportSignedDeterministic verifies that two signed exports produce
// byte-identical checksums.sha256 (Canonicalize is deterministic + order
// independent of map iteration).
func TestExportSignedDeterministic(t *testing.T) {
	keyPath, _ := writeTestSigningKey(t)
	t.Setenv(bundlesig.EnvSigningKey, keyPath)

	out1 := t.TempDir()
	out2 := t.TempDir()
	if _, err := Export(newTestResult(), out1); err != nil {
		t.Fatalf("Export run 1: %v", err)
	}
	if _, err := Export(newTestResult(), out2); err != nil {
		t.Fatalf("Export run 2: %v", err)
	}

	for _, node := range []string{"n1", "n2"} {
		c1, err := os.ReadFile(filepath.Join(out1, node, "checksums.sha256"))
		if err != nil {
			t.Fatalf("%s: read checksums run 1: %v", node, err)
		}
		c2, err := os.ReadFile(filepath.Join(out2, node, "checksums.sha256"))
		if err != nil {
			t.Fatalf("%s: read checksums run 2: %v", node, err)
		}
		if !bytes.Equal(c1, c2) {
			t.Errorf("%s: checksums.sha256 not deterministic across runs:\n--- run1 ---\n%s\n--- run2 ---\n%s", node, c1, c2)
		}

		// Ed25519 is deterministic (RFC 8032) and the canonical input is stable, so
		// the same key must yield a byte-identical bundle.sig across runs.
		s1, err := os.ReadFile(filepath.Join(out1, node, "bundle.sig"))
		if err != nil {
			t.Fatalf("%s: read bundle.sig run 1: %v", node, err)
		}
		s2, err := os.ReadFile(filepath.Join(out2, node, "bundle.sig"))
		if err != nil {
			t.Fatalf("%s: read bundle.sig run 2: %v", node, err)
		}
		if !bytes.Equal(s1, s2) {
			t.Errorf("%s: bundle.sig not deterministic across runs", node)
		}
	}
}

// TestExportUnsigned verifies that with no signing key in the environment the
// export stays hash-only (no bundle.sig, no signing-pubkey.pem), produces a
// valid checksums.sha256, and is deterministic — i.e. today's back-compatible
// behavior is preserved.
func TestExportUnsigned(t *testing.T) {
	// Ensure the env var is unset for this test even if the runner has it.
	t.Setenv(bundlesig.EnvSigningKey, "")

	out1 := t.TempDir()
	out2 := t.TempDir()
	if _, err := Export(newTestResult(), out1); err != nil {
		t.Fatalf("Export run 1: %v", err)
	}
	if _, err := Export(newTestResult(), out2); err != nil {
		t.Fatalf("Export run 2: %v", err)
	}

	for _, node := range []string{"n1", "n2"} {
		nodeDir := filepath.Join(out1, node)

		if _, err := os.Stat(filepath.Join(nodeDir, "bundle.sig")); !os.IsNotExist(err) {
			t.Errorf("%s: bundle.sig must not exist for an unsigned export (err=%v)", node, err)
		}
		if _, err := os.Stat(filepath.Join(nodeDir, "signing-pubkey.pem")); !os.IsNotExist(err) {
			t.Errorf("%s: signing-pubkey.pem must not exist for an unsigned export (err=%v)", node, err)
		}

		checksums, err := os.ReadFile(filepath.Join(nodeDir, "checksums.sha256"))
		if err != nil {
			t.Fatalf("%s: read checksums: %v", node, err)
		}
		if len(bytes.TrimSpace(checksums)) == 0 {
			t.Errorf("%s: checksums.sha256 is empty", node)
		}
		assertChecksumsSorted(t, node, checksums)

		// Determinism: byte-identical across two unsigned runs.
		c2, err := os.ReadFile(filepath.Join(out2, node, "checksums.sha256"))
		if err != nil {
			t.Fatalf("%s: read checksums run 2: %v", node, err)
		}
		if !bytes.Equal(checksums, c2) {
			t.Errorf("%s: unsigned checksums.sha256 not deterministic across runs", node)
		}
	}
}

// assertChecksumsSorted parses a sha256sum-format file ("<hex>  <path>") and
// asserts the paths appear in ascending byte order (the determinism guarantee).
func assertChecksumsSorted(t *testing.T, node string, content []byte) {
	t.Helper()
	lines := bytes.Split(bytes.TrimRight(content, "\n"), []byte("\n"))
	var prev string
	for i, line := range lines {
		// sha256sum format: 64 hex chars, two spaces, then the path.
		const sep = "  "
		idx := bytes.Index(line, []byte(sep))
		if idx < 0 {
			t.Fatalf("%s: line %d is not sha256sum format: %q", node, i, line)
		}
		path := string(line[idx+len(sep):])
		if i > 0 && path < prev {
			t.Errorf("%s: checksums not sorted: %q follows %q", node, path, prev)
		}
		prev = path
	}
}
