package controller

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kunorikiku/yet-another-overlay-generator/internal/bundlesig"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/render"
)

func writeControllerBundleSigningKey(t *testing.T) string {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate Ed25519 key: %v", err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		t.Fatalf("marshal PKCS#8 key: %v", err)
	}
	path := filepath.Join(t.TempDir(), "bundle-signing.pem")
	if err := os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}), 0o600); err != nil {
		t.Fatalf("write signing key: %v", err)
	}
	return path
}

func controllerEmbeddedSigningPEM(t *testing.T, script string) []byte {
	t.Helper()
	const marker = "YAOG_SIGNING_PUBKEY_PEM"
	open := "<< '" + marker + "'\n"
	start := strings.Index(script, open)
	if start < 0 {
		t.Fatal("signed controller install.sh has no embedded signing-key heredoc")
	}
	rest := script[start+len(open):]
	end := strings.Index(rest, "\n"+marker)
	if end < 0 {
		t.Fatal("signed controller install.sh has no closing signing-key heredoc")
	}
	return []byte(rest[:end])
}

// TestCompileAndStage_SignerSnapshotAlignsRenderAndExport guards the controller-specific seam:
// the same resolved signer must render install.sh's mandatory verification block, pin the signing
// anchor, and emit bundle.sig/signing-pubkey.pem. Managed agents verify independently, but manual
// bundles run install.sh directly, so a signed export with an unsigned script is a trust bypass.
func TestCompileAndStage_SignerSnapshotAlignsRenderAndExport(t *testing.T) {
	t.Setenv(bundlesig.EnvSigningKey, writeControllerBundleSigningKey(t))
	t.Setenv(bundlesig.EnvSigningKeyRotate, "")

	store := NewMemStore()
	tenant := TenantID("controller-signed-render")
	ctx := putStageTopo(t, store, tenant)
	approveNode(t, ctx, store, tenant, "node-router", genWGPubKey(t))
	approveNode(t, ctx, store, tenant, "node-peer", genWGPubKey(t))
	approveNode(t, ctx, store, tenant, "node-client", genWGPubKey(t))

	if _, err := CompileAndStage(ctx, store, tenant, time.Now().UTC()); err != nil {
		t.Fatalf("CompileAndStage: %v", err)
	}
	if _, err := PromoteStaged(ctx, store, tenant); err != nil {
		t.Fatalf("PromoteStaged: %v", err)
	}
	bundle, err := store.GetCurrentBundle(ctx, tenant, "node-router")
	if err != nil {
		t.Fatalf("GetCurrentBundle: %v", err)
	}

	install := string(bundle.Files["install.sh"])
	verifyAt := strings.Index(install, "openssl pkeyutl -verify")
	checksumAt := strings.Index(install, "sha256sum --status -c checksums.sha256")
	if verifyAt < 0 || checksumAt < 0 || verifyAt >= checksumAt {
		t.Fatalf("install.sh must verify the signature before checksums (verify=%d checksum=%d)", verifyAt, checksumAt)
	}

	embedded := controllerEmbeddedSigningPEM(t, install)
	shipped := bundle.Files["signing-pubkey.pem"]
	if strings.TrimSpace(string(embedded)) != strings.TrimSpace(string(shipped)) {
		t.Fatal("install.sh embedded key differs from signing-pubkey.pem")
	}
	block, _ := pem.Decode(shipped)
	if block == nil {
		t.Fatal("signing-pubkey.pem is not PEM")
	}
	parsed, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		t.Fatalf("parse signing public key: %v", err)
	}
	pub, ok := parsed.(ed25519.PublicKey)
	if !ok {
		t.Fatalf("signing public key = %T, want ed25519.PublicKey", parsed)
	}
	sig, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(bundle.Files["bundle.sig"])))
	if err != nil {
		t.Fatalf("decode bundle.sig: %v", err)
	}
	if !bundlesig.Verify(bundle.Files["checksums.sha256"], sig, pub) {
		t.Fatal("bundle.sig does not verify with the key embedded in install.sh")
	}
}

// TestCompileAndStage_EmptyPurgeIgnoresBrokenSigner keeps the stale-promote prevention path
// independent of signing configuration. With no ready node there is nothing to render/sign, but
// a previously staged bundle is still dangerous and must be purged even if the configured key is
// unreadable.
func TestCompileAndStage_EmptyPurgeIgnoresBrokenSigner(t *testing.T) {
	t.Setenv(bundlesig.EnvSigningKey, filepath.Join(t.TempDir(), "missing.pem"))

	store := NewMemStore()
	tenant := TenantID("empty-stage-broken-signer")
	ctx := putStageTopo(t, store, tenant) // topology exists, but no managed node is enrolled
	if err := store.StageBundle(ctx, tenant, SignedBundle{
		NodeID:     "stale-node",
		Generation: 1,
		Files:      map[string][]byte{"install.sh": []byte("stale")},
		IsStaged:   true,
	}); err != nil {
		t.Fatalf("seed stale staged bundle: %v", err)
	}

	result, err := CompileAndStage(ctx, store, tenant, time.Now().UTC())
	if err != nil {
		t.Fatalf("empty CompileAndStage must purge despite broken signer: %v", err)
	}
	if len(result.Staged) != 0 {
		t.Fatalf("empty stage unexpectedly staged nodes: %v", result.Staged)
	}
	if _, err := store.PromoteStaged(ctx, tenant); err != ErrNoStagedBundle {
		t.Fatalf("PromoteStaged after empty purge = %v, want ErrNoStagedBundle", err)
	}
	entries, err := store.ListAudit(ctx, tenant)
	if err != nil {
		t.Fatalf("ListAudit: %v", err)
	}
	var sawPurge, sawEmpty bool
	for _, entry := range entries {
		sawPurge = sawPurge || entry.Action == "purge-staged"
		sawEmpty = sawEmpty || entry.Action == "stage-empty"
	}
	if !sawPurge || !sawEmpty {
		t.Fatalf("empty purge audit = %+v, want purge-staged and stage-empty", entries)
	}
}

// TestCompileSubgraph_EmptyIgnoresBrokenSigner keeps the operator compile-preview helper aligned
// with the empty stage: an unreadable configured key is irrelevant when every topology node is
// unready, because the helper has no install script or bundle bytes to render.
func TestCompileSubgraph_EmptyIgnoresBrokenSigner(t *testing.T) {
	t.Setenv(bundlesig.EnvSigningKey, filepath.Join(t.TempDir(), "missing.pem"))

	result, subgraph, skipped, err := CompileSubgraph(
		context.Background(),
		stageTestTopo(),
		nil,
		render.FetchSettings{},
	)
	if err != nil {
		t.Fatalf("empty CompileSubgraph must ignore broken signer: %v", err)
	}
	if result != nil {
		t.Fatal("empty CompileSubgraph returned a compile result")
	}
	if len(subgraph.Nodes) != 0 {
		t.Fatalf("empty CompileSubgraph projected nodes: %+v", subgraph.Nodes)
	}
	for _, nodeID := range []string{"node-router", "node-peer", "node-client"} {
		if !containsStr(skipped, nodeID) {
			t.Errorf("empty CompileSubgraph skipped = %v, want %s", skipped, nodeID)
		}
	}
}

// TestDeployPreview_EmptyIgnoresBrokenSigner proves the read-only deploy preview makes the same
// readiness decision as CompileAndStage before depending on the signing-key file. The preview is
// empty (with the unenrolled IDs reported), not a signing-configuration failure.
func TestDeployPreview_EmptyIgnoresBrokenSigner(t *testing.T) {
	t.Setenv(bundlesig.EnvSigningKey, filepath.Join(t.TempDir(), "missing.pem"))

	store := NewMemStore()
	tenant := TenantID("empty-preview-broken-signer")
	preview, err := DeployPreview(context.Background(), store, tenant, stageTestTopo())
	if err != nil {
		t.Fatalf("empty DeployPreview must ignore broken signer: %v", err)
	}
	if len(preview.Nodes) != 0 {
		t.Fatalf("empty DeployPreview returned node changes: %+v", preview.Nodes)
	}
	for _, nodeID := range []string{"node-router", "node-peer", "node-client"} {
		if !containsStr(preview.SkippedUnenrolled, nodeID) {
			t.Errorf("empty DeployPreview skipped = %v, want %s", preview.SkippedUnenrolled, nodeID)
		}
	}
}
