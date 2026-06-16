package agent_test

// reprovision_test.go covers the node-side keystone re-provisioning: ReprovisionKeystone's
// fail-closed validate-before-atomic-write, and that re-provisioning to the right key makes a
// served bundle verify while the actionable mismatch error guides the operator otherwise.

import (
	"crypto/ed25519"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kunorikiku/yet-another-overlay-generator/internal/agent"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/bundlesig"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/trustlist"
)

func freshEd25519PEM(t *testing.T) []byte {
	t.Helper()
	pub, _, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("genkey: %v", err)
	}
	return bundlesig.MarshalPublicKeyPEM(pub)
}

// TestReprovisionKeystone_FailClosed: an empty or unparsable PEM is REFUSED before any write, so a
// botched rotation never blanks the pin (which would silently turn the keystone OFF).
func TestReprovisionKeystone_FailClosed(t *testing.T) {
	dir := t.TempDir()
	credPath := filepath.Join(dir, "operator-cred.pem")
	sentinel := []byte("-----BEGIN PUBLIC KEY-----\nEXISTING-PIN\n-----END PUBLIC KEY-----\n")
	if err := os.WriteFile(credPath, sentinel, 0o600); err != nil {
		t.Fatalf("seed sentinel: %v", err)
	}

	for _, tc := range []struct {
		name string
		pem  []byte
	}{
		{"empty", nil},
		{"garbage", []byte("not a pem")},
		{"wrong-key-type-for-alg", []byte("-----BEGIN PUBLIC KEY-----\nAAAA\n-----END PUBLIC KEY-----\n")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := agent.ReprovisionKeystone(credPath, string(trustlist.AlgEd25519), tc.pem); err == nil {
				t.Fatal("expected an error for an invalid PEM, got nil")
			}
			got, err := os.ReadFile(credPath)
			if err != nil {
				t.Fatalf("read back: %v", err)
			}
			if string(got) != string(sentinel) {
				t.Fatalf("fail-closed violated: the existing pin was modified on a rejected reprovision")
			}
		})
	}
}

// TestReprovisionKeystone_WritesAtomic0600: a valid PEM is written verbatim at 0600, creating the
// parent dir when absent.
func TestReprovisionKeystone_WritesAtomic0600(t *testing.T) {
	credPath := filepath.Join(t.TempDir(), "wg", "operator-cred.pem") // parent does not exist yet
	pem := freshEd25519PEM(t)
	if err := agent.ReprovisionKeystone(credPath, string(trustlist.AlgEd25519), pem); err != nil {
		t.Fatalf("ReprovisionKeystone: %v", err)
	}
	got, err := os.ReadFile(credPath)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if string(got) != string(pem) {
		t.Fatalf("written PEM differs from input")
	}
	info, err := os.Stat(credPath)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("pinned credential mode = %v, want 0600", info.Mode().Perm())
	}
}

// TestReprovisionKeystone_AdoptsRotatedKey closes the Variant-B loop: a bundle that does NOT verify
// against an unrelated key DOES verify once the node is re-provisioned (via ReprovisionKeystone) to
// the key the bundle was actually signed under.
func TestReprovisionKeystone_AdoptsRotatedKey(t *testing.T) {
	files, pinPEM, _ := keystoneBundle(t) // signed under the bundle's own keystone; pinPEM is that key

	// A node pinned to an UNRELATED key refuses (this is the rotated-but-not-reprovisioned state).
	wrong := freshEd25519PEM(t)
	if _, err := agent.VerifyMembership(files, edCfg("node-1", wrong), 0); err == nil {
		t.Fatal("a node pinned to the wrong key must refuse the bundle")
	}

	// Re-provision to the correct key via the real on-disk path, then verify using the file's bytes.
	credPath := filepath.Join(t.TempDir(), "operator-cred.pem")
	if err := agent.ReprovisionKeystone(credPath, string(trustlist.AlgEd25519), pinPEM); err != nil {
		t.Fatalf("ReprovisionKeystone: %v", err)
	}
	adopted, err := os.ReadFile(credPath)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if _, err := agent.VerifyMembership(files, edCfg("node-1", adopted), 0); err != nil {
		t.Fatalf("after re-provisioning to the correct key, the bundle must verify, got: %v", err)
	}
}

// TestVerifyMembership_MismatchErrorIsActionable: a pin/signer mismatch yields an error that keeps
// the original phrase (so log greps still match) AND names the fingerprints, the reprovision-keystone
// remedy, and the controller-claimed/UNVERIFIED qualifier on the served fingerprint.
func TestVerifyMembership_MismatchErrorIsActionable(t *testing.T) {
	files, _, _ := keystoneBundle(t)
	wrong := freshEd25519PEM(t)
	_, err := agent.VerifyMembership(files, edCfg("node-1", wrong), 0)
	if err == nil {
		t.Fatal("expected a verification error on a pin/signer mismatch")
	}
	msg := err.Error()
	for _, want := range []string{
		"trust-list signature verification failed", // original phrase preserved for log greps
		"reprovision-keystone",                     // names the remedy command
		"UNVERIFIED",                               // served fingerprint is controller-claimed, not trusted
		"pinned keystone fingerprint",              // names the pinned fp
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("mismatch error missing %q; got: %s", want, msg)
		}
	}
}

// TestCredFingerprintShort: stable short hex for a real key, "unknown" for garbage.
func TestCredFingerprintShort(t *testing.T) {
	pem := freshEd25519PEM(t)
	fp := agent.CredFingerprintShort(pem)
	if len(fp) != 12 {
		t.Fatalf("fingerprint %q, want 12 hex chars", fp)
	}
	if agent.CredFingerprintShort(pem) != fp {
		t.Fatal("fingerprint not stable across calls")
	}
	if got := agent.CredFingerprintShort([]byte("garbage")); got != "unknown" {
		t.Fatalf("garbage fingerprint = %q, want unknown", got)
	}
}
