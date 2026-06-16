package controller

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/kunorikiku/yet-another-overlay-generator/internal/apierr"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/bundlesig"
)

// writeEd25519KeyPEM writes a fresh Ed25519 PKCS#8 PEM (the form YAOG_BUNDLE_SIGNING_KEY points at)
// to dir/name and returns its path + the matching PKIX public-key PEM (what the anchor stores).
func writeEd25519KeyPEM(t *testing.T, dir, name string) (path, pubPEM string) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("genkey: %v", err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		t.Fatalf("marshal pkcs8: %v", err)
	}
	path = filepath.Join(dir, name)
	if err := os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}), 0o600); err != nil {
		t.Fatalf("write key: %v", err)
	}
	return path, string(bundlesig.MarshalPublicKeyPEM(pub))
}

// TestEnforceSigningAnchor exercises the full reconcile matrix: trust-on-first-use, same-key,
// pinned-but-absent (the silent-downgrade guard), changed-key, and the explicit rotation hatch.
func TestEnforceSigningAnchor(t *testing.T) {
	ctx := context.Background()
	const tnt = TenantID("acme")
	dir := t.TempDir()
	keyA, pubA := writeEd25519KeyPEM(t, dir, "a.pem")
	keyB, pubB := writeEd25519KeyPEM(t, dir, "b.pem")

	t.Run("never-signed: no anchor + no key is a no-op", func(t *testing.T) {
		t.Setenv(bundlesig.EnvSigningKey, "")
		s := NewMemStore()
		if err := enforceSigningAnchor(ctx, s, tnt); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if _, err := s.GetSigningAnchor(ctx, tnt); !errors.Is(err, ErrNotFound) {
			t.Fatalf("anchor should stay unpinned, got %v", err)
		}
	})

	t.Run("TOFU: no anchor + key pins the pubkey", func(t *testing.T) {
		t.Setenv(bundlesig.EnvSigningKey, keyA)
		s := NewMemStore()
		if err := enforceSigningAnchor(ctx, s, tnt); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		a, err := s.GetSigningAnchor(ctx, tnt)
		if err != nil {
			t.Fatalf("anchor not pinned: %v", err)
		}
		if a.PubKeyPEM != pubA {
			t.Fatalf("pinned the wrong pubkey")
		}
	})

	t.Run("same key passes", func(t *testing.T) {
		t.Setenv(bundlesig.EnvSigningKey, keyA)
		s := NewMemStore()
		mustPin(t, s, tnt, pubA)
		if err := enforceSigningAnchor(ctx, s, tnt); err != nil {
			t.Fatalf("same key should pass: %v", err)
		}
	})

	t.Run("pinned-but-absent → signing_key_missing (the silent-downgrade guard)", func(t *testing.T) {
		t.Setenv(bundlesig.EnvSigningKey, "")
		s := NewMemStore()
		mustPin(t, s, tnt, pubA)
		err := enforceSigningAnchor(ctx, s, tnt)
		if !apierr.HasCode(err, apierr.CodeSigningKeyMissing) {
			t.Fatalf("want signing_key_missing, got %v", err)
		}
	})

	t.Run("changed key without rotate → signing_key_mismatch, anchor unchanged", func(t *testing.T) {
		t.Setenv(bundlesig.EnvSigningKey, keyB)
		t.Setenv(bundlesig.EnvSigningKeyRotate, "")
		s := NewMemStore()
		mustPin(t, s, tnt, pubA)
		err := enforceSigningAnchor(ctx, s, tnt)
		if !apierr.HasCode(err, apierr.CodeSigningKeyMismatch) {
			t.Fatalf("want signing_key_mismatch, got %v", err)
		}
		if a, _ := s.GetSigningAnchor(ctx, tnt); a.PubKeyPEM != pubA {
			t.Fatalf("mismatch must NOT re-pin the anchor")
		}
	})

	t.Run("changed key with rotate → re-pins to the new key", func(t *testing.T) {
		t.Setenv(bundlesig.EnvSigningKey, keyB)
		t.Setenv(bundlesig.EnvSigningKeyRotate, "1")
		s := NewMemStore()
		mustPin(t, s, tnt, pubA)
		if err := enforceSigningAnchor(ctx, s, tnt); err != nil {
			t.Fatalf("rotate should pass: %v", err)
		}
		if a, _ := s.GetSigningAnchor(ctx, tnt); a.PubKeyPEM != pubB {
			t.Fatalf("rotate must re-pin to the new key")
		}
	})
}

func mustPin(t *testing.T, s Store, tnt TenantID, pub string) {
	t.Helper()
	if err := s.PutSigningAnchor(context.Background(), tnt, SigningAnchor{PubKeyPEM: pub}); err != nil {
		t.Fatalf("pin: %v", err)
	}
}

// TestSigningAnchorStore covers Get/Put round-trip + per-tenant isolation for both store impls.
func TestSigningAnchorStore(t *testing.T) {
	ctx := context.Background()
	impls := []struct {
		name string
		make func(t *testing.T) Store
	}{
		{"mem", func(t *testing.T) Store { return NewMemStore() }},
		{"file", func(t *testing.T) Store {
			s, err := NewFileStore(t.TempDir())
			if err != nil {
				t.Fatalf("NewFileStore: %v", err)
			}
			return s
		}},
	}
	for _, impl := range impls {
		t.Run(impl.name, func(t *testing.T) {
			s := impl.make(t)
			if _, err := s.GetSigningAnchor(ctx, "t1"); !errors.Is(err, ErrNotFound) {
				t.Fatalf("unpinned → want ErrNotFound, got %v", err)
			}
			if err := s.PutSigningAnchor(ctx, "t1", SigningAnchor{PubKeyPEM: "PUB-1"}); err != nil {
				t.Fatalf("put: %v", err)
			}
			a, err := s.GetSigningAnchor(ctx, "t1")
			if err != nil || a.PubKeyPEM != "PUB-1" {
				t.Fatalf("round-trip: got %+v err %v", a, err)
			}
			// Tenant isolation: t2 must not see t1's anchor.
			if _, err := s.GetSigningAnchor(ctx, "t2"); !errors.Is(err, ErrNotFound) {
				t.Fatalf("tenant leak: t2 saw an anchor (%v)", err)
			}
		})
	}
}
