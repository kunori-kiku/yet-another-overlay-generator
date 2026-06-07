package render

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kunorikiku/yet-another-overlay-generator/internal/artifacts"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/bundlesig"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/compiler"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/model"
)

// signingTestTopology builds a router + peer + client topology with pinned real
// keys, mirroring equivalence_test, so render.All exercises both the per-peer
// and client install-script branches.
func signingTestTopology(t *testing.T) *model.Topology {
	t.Helper()
	routerKey := mustGenerateKey(t)
	peerKey := mustGenerateKey(t)
	clientKey := mustGenerateKey(t)
	return &model.Topology{
		Project: model.Project{ID: "sign-001", Name: "Signing", Version: "1"},
		Domains: []model.Domain{{
			ID: "domain-1", Name: "sign-net", CIDR: "10.41.0.0/24",
			AllocationMode: "auto", RoutingMode: "babel",
		}},
		Nodes: []model.Node{
			{
				ID: "router-1", Name: "router-1", Role: "router", DomainID: "domain-1",
				Capabilities:    model.NodeCapabilities{CanAcceptInbound: true, CanForward: true, HasPublicIP: true},
				PublicEndpoints: []model.PublicEndpoint{{ID: "ep", Host: "router-1.example", Port: 51820}},
				WireGuardPrivateKey: routerKey.String(),
			},
			{
				ID: "peer-1", Name: "peer-1", Role: "peer", DomainID: "domain-1",
				WireGuardPrivateKey: peerKey.String(),
			},
			{
				ID: "client-1", Name: "client-1", Role: "client", DomainID: "domain-1",
				WireGuardPrivateKey: clientKey.String(),
			},
		},
		Edges: []model.Edge{
			{ID: "e-peer", FromNodeID: "peer-1", ToNodeID: "router-1", Type: "public-endpoint",
				EndpointHost: "router-1.example", Transport: "udp", IsEnabled: true},
			{ID: "e-client", FromNodeID: "client-1", ToNodeID: "router-1", Type: "public-endpoint",
				EndpointHost: "router-1.example", Transport: "udp", IsEnabled: true},
		},
	}
}

// renderAll runs the shared GenerateKeys → Compile → All path and returns the result.
func renderAll(t *testing.T, topo *model.Topology) *compiler.CompileResult {
	t.Helper()
	keys, err := GenerateKeys(topo)
	if err != nil {
		t.Fatalf("GenerateKeys: %v", err)
	}
	result, err := compiler.NewCompiler().Compile(topo, keys)
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	if err := All(result, keys); err != nil {
		t.Fatalf("render.All: %v", err)
	}
	return result
}

// writeSigningKey writes a fresh Ed25519 PKCS#8 PEM to a temp file and returns the path.
func writeSigningKey(t *testing.T) string {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		t.Fatalf("marshal pkcs8: %v", err)
	}
	path := filepath.Join(t.TempDir(), "signing-key.pem")
	if err := os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}), 0600); err != nil {
		t.Fatalf("write key: %v", err)
	}
	return path
}

// signingMarkers are strings the signed install.sh must contain (the verify block
// plus the mandatory-signature downgrade guard) and the unsigned install.sh must
// NOT contain.
var signingMarkers = []string{
	"Verifying bundle signature",
	"openssl pkeyutl -verify",
	"bundle.sig",
	"refusing to proceed (possible signature-stripping tamper)",
}

// TestAll_SignedInstallScripts asserts render.All embeds the bundle-signature
// verify block into BOTH the per-peer and client install scripts when a signing
// key is configured. This is the seam between bundle signing (export) and the
// install scripts: render.All must call the *Signed renderers, not the plain ones.
func TestAll_SignedInstallScripts(t *testing.T) {
	t.Setenv(bundlesig.EnvSigningKey, writeSigningKey(t))

	result := renderAll(t, signingTestTopology(t))

	for _, nodeID := range []string{"router-1", "client-1"} {
		script, ok := result.InstallScripts[nodeID]
		if !ok {
			t.Fatalf("%s: missing install script", nodeID)
		}
		for _, marker := range signingMarkers {
			if !strings.Contains(script, marker) {
				t.Errorf("%s: signed install script missing %q", nodeID, marker)
			}
		}
		// The verify step must run BEFORE the checksum check (fail-closed ordering).
		vi := strings.Index(script, "openssl pkeyutl -verify")
		ci := strings.Index(script, "sha256sum --status -c checksums.sha256")
		if vi < 0 || ci < 0 || vi >= ci {
			t.Errorf("%s: signature verify must precede checksum check (verify=%d, checksum=%d)", nodeID, vi, ci)
		}
	}
}

// TestAll_UnsignedInstallScripts asserts that with no signing key configured the
// install scripts carry no signature-verify remnant (byte-identical-to-today path).
func TestAll_UnsignedInstallScripts(t *testing.T) {
	// Neutralize any signing key the runner might have set.
	t.Setenv(bundlesig.EnvSigningKey, "")

	result := renderAll(t, signingTestTopology(t))

	for _, nodeID := range []string{"router-1", "client-1"} {
		script := result.InstallScripts[nodeID]
		for _, marker := range signingMarkers {
			if strings.Contains(script, marker) {
				t.Errorf("%s: unsigned install script must not contain %q", nodeID, marker)
			}
		}
	}
}

// TestAll_BadSigningKeyFailsClosed asserts a configured-but-unreadable signing key
// fails the render rather than silently producing unsigned bundles.
func TestAll_BadSigningKeyFailsClosed(t *testing.T) {
	t.Setenv(bundlesig.EnvSigningKey, filepath.Join(t.TempDir(), "does-not-exist.pem"))

	topo := signingTestTopology(t)
	keys, err := GenerateKeys(topo)
	if err != nil {
		t.Fatalf("GenerateKeys: %v", err)
	}
	result, err := compiler.NewCompiler().Compile(topo, keys)
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	if err := All(result, keys); err == nil {
		t.Fatal("render.All must fail closed when the signing key path is unreadable")
	}
}

// extractEmbeddedPubPEM pulls the verifying public key embedded in install.sh
// between the YAOG_SIGNING_PUBKEY_PEM heredoc markers — the Go-emitted trust
// anchor the script verifies against.
func extractEmbeddedPubPEM(t *testing.T, script string) []byte {
	t.Helper()
	const marker = "YAOG_SIGNING_PUBKEY_PEM"
	openTok := "<< '" + marker + "'\n"
	o := strings.Index(script, openTok)
	if o < 0 {
		t.Fatal("install.sh missing the pubkey heredoc open marker")
	}
	rest := script[o+len(openTok):]
	c := strings.Index(rest, "\n"+marker)
	if c < 0 {
		t.Fatal("install.sh missing the pubkey heredoc close marker")
	}
	return []byte(rest[:c])
}

// parsePub parses a PKIX PEM Ed25519 public key the way openssl/install.sh would.
func parsePub(t *testing.T, pemBytes []byte) ed25519.PublicKey {
	t.Helper()
	block, _ := pem.Decode(pemBytes)
	if block == nil {
		t.Fatalf("not valid PEM: %q", pemBytes)
	}
	pub, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		t.Fatalf("parse PKIX public key: %v", err)
	}
	edPub, ok := pub.(ed25519.PublicKey)
	if !ok {
		t.Fatalf("not an Ed25519 key: %T", pub)
	}
	return edPub
}

// TestSignedExport_EmbeddedPubkeyMatchesShippedAndVerifies is the end-to-end seam
// test: render.All -> artifacts.Export with a signing key set must (1) embed the
// SAME public key into install.sh as it ships in signing-pubkey.pem, (2) produce a
// bundle.sig that verifies over checksums.sha256 under that embedded key, and (3)
// reject a tampered checksums. This guards the render<->export signing seam that a
// divergent env read or a missing *Signed call would silently break.
func TestSignedExport_EmbeddedPubkeyMatchesShippedAndVerifies(t *testing.T) {
	t.Setenv(bundlesig.EnvSigningKey, writeSigningKey(t))

	result := renderAll(t, signingTestTopology(t))
	outDir := t.TempDir()
	if _, err := artifacts.Export(result, outDir); err != nil {
		t.Fatalf("Export: %v", err)
	}

	// router-1 is a non-client node with a real signed install.sh in the bundle.
	const nodeID = "router-1"
	nodeDir := filepath.Join(outDir, nodeID)

	embedded := extractEmbeddedPubPEM(t, result.InstallScripts[nodeID])
	shipped, err := os.ReadFile(filepath.Join(nodeDir, "signing-pubkey.pem"))
	if err != nil {
		t.Fatalf("read signing-pubkey.pem: %v", err)
	}
	embPub := parsePub(t, embedded)
	if !embPub.Equal(parsePub(t, shipped)) {
		t.Error("pubkey embedded in install.sh does not match the shipped signing-pubkey.pem")
	}

	checksums, err := os.ReadFile(filepath.Join(nodeDir, "checksums.sha256"))
	if err != nil {
		t.Fatalf("read checksums.sha256: %v", err)
	}
	sigB64, err := os.ReadFile(filepath.Join(nodeDir, "bundle.sig"))
	if err != nil {
		t.Fatalf("read bundle.sig: %v", err)
	}
	sig, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(sigB64)))
	if err != nil {
		t.Fatalf("decode bundle.sig: %v", err)
	}
	if !bundlesig.Verify(checksums, sig, embPub) {
		t.Error("bundle.sig does not verify over checksums.sha256 with the embedded pubkey")
	}

	// Tamper: the signature is bound to the exact checksums bytes.
	tampered := append([]byte(nil), checksums...)
	tampered[0] ^= 0xff
	if bundlesig.Verify(tampered, sig, embPub) {
		t.Error("tampered checksums.sha256 must not verify")
	}
}
