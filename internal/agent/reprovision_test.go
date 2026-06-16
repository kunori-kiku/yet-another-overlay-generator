package agent_test

// reprovision_test.go covers the node-side keystone re-provisioning: ReprovisionKeystone's
// fail-closed validate-before-atomic-write, and that re-provisioning to the right key makes a
// served bundle verify while the actionable mismatch error guides the operator otherwise.

import (
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
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

// freshES256PEM returns a real ES256 (P-256) PKIX public-key PEM — a VALID key of the WRONG type
// for the ed25519 alg, used to exercise the algorithm-confusion fail-closed path.
func freshES256PEM(t *testing.T) []byte {
	t.Helper()
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("ecdsa gen: %v", err)
	}
	der, err := x509.MarshalPKIXPublicKey(&priv.PublicKey)
	if err != nil {
		t.Fatalf("marshal pkix: %v", err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der})
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
		alg  string
		pem  []byte
	}{
		{"empty", string(trustlist.AlgEd25519), nil},
		{"garbage", string(trustlist.AlgEd25519), []byte("not a pem")},
		{"malformed-pem-block", string(trustlist.AlgEd25519), []byte("-----BEGIN PUBLIC KEY-----\nAAAA\n-----END PUBLIC KEY-----\n")},
		// A VALID key of the WRONG type for the declared alg (the algorithm-confusion guard): a real
		// ES256 key offered as ed25519, and a real ed25519 key offered as webauthn-es256.
		{"valid-es256-as-ed25519", string(trustlist.AlgEd25519), freshES256PEM(t)},
		{"valid-ed25519-as-es256", string(trustlist.AlgWebAuthnES256), freshEd25519PEM(t)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := agent.ReprovisionKeystone(credPath, tc.alg, tc.pem); err == nil {
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
