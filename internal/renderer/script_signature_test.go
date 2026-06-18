package renderer

import (
	"crypto/ed25519"
	"crypto/x509"
	"encoding/pem"
	"strings"
	"testing"

	"github.com/kunorikiku/yet-another-overlay-generator/internal/compiler"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/model"
)

// sigTestPubkeyPEM produces a self-contained PKIX/PEM Ed25519 public key for the signature-block
// rendering tests. It does NOT depend on internal/bundlesig so this test pins the renderer's
// template behavior independently of the signing package.
func sigTestPubkeyPEM(t *testing.T) string {
	t.Helper()
	pub, _, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("generate Ed25519 key: %v", err)
	}
	der, err := x509.MarshalPKIXPublicKey(pub)
	if err != nil {
		t.Fatalf("marshal PKIX public key: %v", err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der}))
}

// sigTestRouterNode / sigTestPeers are the minimal per-peer inputs shared by the router-path tests.
func sigTestRouterNode() *model.Node {
	return &model.Node{
		ID:        "node-1",
		Name:      "alpha",
		Role:      "router",
		Platform:  "debian",
		OverlayIP: "10.11.0.1",
		Capabilities: model.NodeCapabilities{
			CanForward: true,
		},
	}
}

func sigTestPeers() []compiler.PeerInfo {
	return []compiler.PeerInfo{
		{NodeID: "node-2", NodeName: "beta", InterfaceName: "wg-beta",
			ListenPort: 51820, LocalTransitIP: "10.10.0.1", LocalLinkLocal: "fe80::1"},
	}
}

func sigTestClientNode() *model.Node {
	return &model.Node{
		ID:        "client-1",
		Name:      "laptop",
		Role:      "client",
		Platform:  "debian",
		OverlayIP: "10.11.0.9",
	}
}

// TestRenderInstallScriptSigned_VerifyStepPrecedesChecksum asserts that, with a pubkey set, the
// rendered per-peer install.sh contains the openssl Ed25519 verify step over bundle.sig and that it
// precedes the existing 'sha256sum -c checksums.sha256' (signature gates the checksum check).
func TestRenderInstallScriptSigned_VerifyStepPrecedesChecksum(t *testing.T) {
	pubPEM := sigTestPubkeyPEM(t)

	script, err := RenderInstallScriptSigned(sigTestRouterNode(), sigTestPeers(), true, pubPEM, CustodySplice{}, model.InstallFetch{})
	if err != nil {
		t.Fatalf("render signed install script: %v", err)
	}

	const (
		opensslVerify = "openssl pkeyutl -verify -pubin"
		bundleSigRef  = "$SCRIPT_DIR/bundle.sig"
		checksumCheck = "sha256sum --status -c checksums.sha256"
	)

	if !strings.Contains(script, opensslVerify) {
		t.Errorf("signed install.sh must contain the openssl verify step %q", opensslVerify)
	}
	if !strings.Contains(script, bundleSigRef) {
		t.Errorf("signed install.sh must reference %q", bundleSigRef)
	}
	if !strings.Contains(script, pubPEM) {
		t.Errorf("signed install.sh must embed the pinned signing pubkey PEM")
	}

	verifyIdx := strings.Index(script, opensslVerify)
	checksumIdx := strings.Index(script, checksumCheck)
	if checksumIdx < 0 {
		t.Fatalf("signed install.sh missing checksum check %q", checksumCheck)
	}
	if verifyIdx < 0 || verifyIdx >= checksumIdx {
		t.Errorf("openssl verify step must PRECEDE the sha256sum check (verifyIdx=%d, checksumIdx=%d)", verifyIdx, checksumIdx)
	}

	// Fail-clear discipline: a present bundle.sig with no/insufficient openssl must abort.
	if !strings.Contains(script, "bundle.sig present but openssl is not installed") {
		t.Errorf("signed install.sh must fail clearly when openssl is missing")
	}
}

// TestRenderInstallScript_UnsignedByteIdentical asserts that with an empty pubkey the per-peer
// script contains neither the openssl block nor any signing remnant, and that the signed renderer
// with an empty pubkey is byte-identical to the plain renderer (no drift for existing users).
func TestRenderInstallScript_UnsignedByteIdentical(t *testing.T) {
	node := sigTestRouterNode()
	peers := sigTestPeers()

	plain, err := RenderInstallScript(node, peers, true)
	if err != nil {
		t.Fatalf("render plain install script: %v", err)
	}
	signedEmpty, err := RenderInstallScriptSigned(node, peers, true, "", CustodySplice{}, model.InstallFetch{})
	if err != nil {
		t.Fatalf("render signed-empty install script: %v", err)
	}

	if plain != signedEmpty {
		t.Errorf("RenderInstallScriptSigned with empty pubkey must be byte-identical to RenderInstallScript")
	}

	for _, remnant := range []string{
		"openssl pkeyutl",
		"bundle.sig",
		"Verifying bundle signature",
		"YAOG_SIGNING_PUBKEY_PEM",
		"SigningPubkeyPEM",
	} {
		if strings.Contains(plain, remnant) {
			t.Errorf("unsigned install.sh must not contain signing remnant %q", remnant)
		}
	}
}

// TestRenderClientInstallScriptSigned_VerifyStepPrecedesChecksum mirrors the router assertion for
// the client (single wg0) path.
func TestRenderClientInstallScriptSigned_VerifyStepPrecedesChecksum(t *testing.T) {
	pubPEM := sigTestPubkeyPEM(t)

	script, err := RenderClientInstallScriptSigned(sigTestClientNode(), pubPEM, CustodySplice{}, model.InstallFetch{})
	if err != nil {
		t.Fatalf("render signed client install script: %v", err)
	}

	const (
		opensslVerify = "openssl pkeyutl -verify -pubin"
		checksumCheck = "sha256sum --status -c checksums.sha256"
	)

	if !strings.Contains(script, opensslVerify) {
		t.Errorf("signed client install.sh must contain the openssl verify step")
	}
	if !strings.Contains(script, pubPEM) {
		t.Errorf("signed client install.sh must embed the pinned signing pubkey PEM")
	}

	verifyIdx := strings.Index(script, opensslVerify)
	checksumIdx := strings.Index(script, checksumCheck)
	if checksumIdx < 0 {
		t.Fatalf("signed client install.sh missing checksum check")
	}
	if verifyIdx < 0 || verifyIdx >= checksumIdx {
		t.Errorf("client openssl verify must PRECEDE the sha256sum check (verifyIdx=%d, checksumIdx=%d)", verifyIdx, checksumIdx)
	}
}

// TestRenderClientInstallScript_UnsignedByteIdentical is the client-path back-compat assertion.
func TestRenderClientInstallScript_UnsignedByteIdentical(t *testing.T) {
	node := sigTestClientNode()

	plain, err := RenderClientInstallScript(node)
	if err != nil {
		t.Fatalf("render plain client install script: %v", err)
	}
	signedEmpty, err := RenderClientInstallScriptSigned(node, "", CustodySplice{}, model.InstallFetch{})
	if err != nil {
		t.Fatalf("render signed-empty client install script: %v", err)
	}

	if plain != signedEmpty {
		t.Errorf("RenderClientInstallScriptSigned with empty pubkey must be byte-identical to RenderClientInstallScript")
	}

	for _, remnant := range []string{
		"openssl pkeyutl",
		"bundle.sig",
		"Verifying bundle signature",
		"YAOG_SIGNING_PUBKEY_PEM",
	} {
		if strings.Contains(plain, remnant) {
			t.Errorf("unsigned client install.sh must not contain signing remnant %q", remnant)
		}
	}
}
