package agent

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kunorikiku/yet-another-overlay-generator/internal/bundlesig"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/trustlist"
)

func TestRunRefusesRootMutationWhenPendingIntentIsNotDurable(t *testing.T) {
	b := newSignedBundle(t, "2026-07-16T02:00:00Z")
	sentinel := filepath.Join(t.TempDir(), "must-not-run")
	b.files["install.sh"] = []byte("#!/usr/bin/env bash\ntouch " + sentinel + "\n")
	resign(t, b)

	original := savePendingApply
	savePendingApply = func(string, *State) error {
		return errors.New("injected pending-intent sync failure")
	}
	t.Cleanup(func() { savePendingApply = original })

	_, err := Run(&Config{
		NodeID:       "alpha",
		Source:       staticBundleSource(b.files),
		PinnedPubPEM: b.pubPEM,
		StateDir:     t.TempDir(),
		Stdout:       &strings.Builder{},
		Stderr:       &strings.Builder{},
	})
	if err == nil || !strings.Contains(err.Error(), "persist pending apply before install.sh") ||
		!strings.Contains(err.Error(), "injected pending-intent sync failure") {
		t.Fatalf("Run error = %v, want pre-mutation durability refusal", err)
	}
	if _, err := os.Stat(sentinel); !os.IsNotExist(err) {
		t.Fatalf("install.sh ran without a durable pending intent: %v", err)
	}
}

func TestRunPostApplyCommitFailureRetainsIntentAndRejectsOlderBundle(t *testing.T) {
	b := newSignedBundle(t, "2026-07-16T03:00:00Z")
	sentinel := filepath.Join(t.TempDir(), "root-mutations")
	b.files["install.sh"] = []byte("#!/usr/bin/env bash\nprintf x >> " + sentinel + "\n")
	resign(t, b)
	stateDir := t.TempDir()
	if err := SaveState(stateDir, &State{
		NodeID:         "alpha",
		LastCompiledAt: "2026-07-16T01:00:00Z",
		LastChecksum:   "last-good-checksum",
		LastResult:     LastResultOK,
		Health:         "applied",
	}); err != nil {
		t.Fatalf("seed last-good state: %v", err)
	}

	res, err := Run(&Config{
		NodeID:       "alpha",
		Source:       staticBundleSource(b.files),
		PinnedPubPEM: b.pubPEM,
		StateDir:     stateDir,
		StateSaver: func(string, *State) error {
			// Model process death or storage loss after root mutation but before the
			// success replacement becomes visible. The write-ahead state must remain.
			return errors.New("injected final-state loss")
		},
		Stdout: &strings.Builder{},
		Stderr: &strings.Builder{},
	})
	if err == nil || !strings.Contains(err.Error(), "injected final-state loss") {
		t.Fatalf("Run error = %v, want final-commit failure", err)
	}
	if res == nil || !res.Applied {
		t.Fatalf("root script should have completed before injected commit loss: %+v", res)
	}
	if got, readErr := os.ReadFile(sentinel); readErr != nil || string(got) != "x" {
		t.Fatalf("root mutation record = %q, %v; want one execution", got, readErr)
	}

	st, err := LoadState(stateDir)
	if err != nil {
		t.Fatalf("load pending state: %v", err)
	}
	if st.LastCompiledAt != "2026-07-16T01:00:00Z" || st.LastChecksum != "last-good-checksum" {
		t.Fatalf("write-ahead record clobbered last-known-good: %+v", st)
	}
	if st.PendingApply == nil || st.PendingApply.CompiledAt != "2026-07-16T03:00:00Z" ||
		st.PendingApply.BundleSHA256 == "" || st.PendingApply.SigningKeyFingerprint == "" {
		t.Fatalf("root mutation was not represented by a complete pending intent: %+v", st.PendingApply)
	}

	// manifest.json is deliberately outside checksums.sha256, so keep the same authenticated
	// root bytes/key and make only its advertised time older. The pending compiled-at floor,
	// not signature failure or anchor drift, must stop a second root execution.
	setBundleManifest(t, b, "2026-07-16T02:00:00Z", "older-candidate")
	_, err = Run(&Config{
		NodeID:       "alpha",
		Source:       staticBundleSource(b.files),
		PinnedPubPEM: b.pubPEM,
		StateDir:     stateDir,
		Stdout:       &strings.Builder{},
		Stderr:       &strings.Builder{},
	})
	if err == nil || !strings.Contains(err.Error(), "anti-rollback") || !strings.Contains(err.Error(), "2026-07-16T03:00:00Z") {
		t.Fatalf("older candidate after interrupted root mutation = %v, want pending-floor refusal", err)
	}
	if got, readErr := os.ReadFile(sentinel); readErr != nil || string(got) != "x" {
		t.Fatalf("older candidate executed despite pending floor: %q, %v", got, readErr)
	}
}

func TestRunExactPendingRetryConvergesAndClearsIntent(t *testing.T) {
	b := newSignedBundle(t, "2026-07-16T04:00:00Z")
	dir := t.TempDir()
	firstAttempt := filepath.Join(dir, "first-attempt")
	success := filepath.Join(dir, "success")
	b.files["install.sh"] = []byte("#!/usr/bin/env bash\n" +
		"if [ ! -e " + firstAttempt + " ]; then touch " + firstAttempt + "; exit 42; fi\n" +
		"touch " + success + "\n")
	resign(t, b)
	stateDir := t.TempDir()
	cfg := &Config{
		NodeID:       "alpha",
		Source:       staticBundleSource(b.files),
		PinnedPubPEM: b.pubPEM,
		StateDir:     stateDir,
		Stdout:       &strings.Builder{},
		Stderr:       &strings.Builder{},
	}

	if _, err := Run(cfg); err == nil || !strings.Contains(err.Error(), "install.sh exit") {
		t.Fatalf("first partial apply = %v, want script failure", err)
	}
	interrupted, err := LoadState(stateDir)
	if err != nil {
		t.Fatalf("load interrupted state: %v", err)
	}
	if interrupted.PendingApply == nil || interrupted.LastResult != "error" {
		t.Fatalf("failed root script did not preserve pending intent: %+v", interrupted)
	}

	res, err := Run(cfg)
	if err != nil {
		t.Fatalf("exact verified retry did not converge: %v", err)
	}
	if res == nil || !res.Applied {
		t.Fatalf("exact retry result = %+v, want applied", res)
	}
	if _, err := os.Stat(success); err != nil {
		t.Fatalf("exact retry script did not complete: %v", err)
	}
	committed, err := LoadState(stateDir)
	if err != nil {
		t.Fatalf("load committed state: %v", err)
	}
	if committed.PendingApply != nil || committed.LastResult != LastResultOK ||
		committed.LastCompiledAt != "2026-07-16T04:00:00Z" {
		t.Fatalf("successful retry did not atomically clear intent/advance last-good: %+v", committed)
	}
}

func TestRunExactPendingRetryRefusesRootMutationWhenIntentCannotBeResynced(t *testing.T) {
	b := newSignedBundle(t, "2026-07-16T04:30:00Z")
	sentinel := filepath.Join(t.TempDir(), "root-mutations")
	b.files["install.sh"] = []byte("#!/usr/bin/env bash\nprintf x >> " + sentinel + "\nexit 42\n")
	resign(t, b)
	stateDir := t.TempDir()
	cfg := &Config{
		NodeID:       "alpha",
		Source:       staticBundleSource(b.files),
		PinnedPubPEM: b.pubPEM,
		StateDir:     stateDir,
		Stdout:       &strings.Builder{},
		Stderr:       &strings.Builder{},
	}

	if _, err := Run(cfg); err == nil || !strings.Contains(err.Error(), "install.sh exit") {
		t.Fatalf("first partial apply = %v, want script failure", err)
	}
	before, err := LoadState(stateDir)
	if err != nil || before.PendingApply == nil {
		t.Fatalf("load first pending intent: %+v, %v", before, err)
	}
	originalStartedAt := before.PendingApply.StartedAt
	if got, err := os.ReadFile(sentinel); err != nil || string(got) != "x" {
		t.Fatalf("first root execution = %q, %v", got, err)
	}

	originalSave := savePendingApply
	savePendingApply = func(string, *State) error {
		return errors.New("injected exact-retry directory sync failure")
	}
	t.Cleanup(func() { savePendingApply = originalSave })
	if _, err := Run(cfg); err == nil ||
		!strings.Contains(err.Error(), "re-persist pending apply before install.sh retry") ||
		!strings.Contains(err.Error(), "injected exact-retry directory sync failure") {
		t.Fatalf("exact retry resync error = %v, want pre-mutation refusal", err)
	}
	if got, err := os.ReadFile(sentinel); err != nil || string(got) != "x" {
		t.Fatalf("exact retry ran root script without durable reauthorization: %q, %v", got, err)
	}
	after, err := LoadState(stateDir)
	if err != nil || after.PendingApply == nil || after.PendingApply.StartedAt != originalStartedAt {
		t.Fatalf("failed retry changed original pending identity: %+v, %v", after, err)
	}
}

func TestRunPendingApplyRejectsSameVersionSubstitutionActionAndAnchorDrift(t *testing.T) {
	b := newSignedBundle(t, "2026-07-16T05:00:00Z")
	partial := filepath.Join(t.TempDir(), "partial")
	b.files["install.sh"] = []byte("#!/usr/bin/env bash\ntouch " + partial + "\nexit 42\n")
	resign(t, b)
	stateDir := t.TempDir()
	base := &Config{
		NodeID:       "alpha",
		Source:       staticBundleSource(b.files),
		PinnedPubPEM: b.pubPEM,
		StateDir:     stateDir,
		Stdout:       &strings.Builder{},
		Stderr:       &strings.Builder{},
	}
	if _, err := Run(base); err == nil {
		t.Fatal("fixture apply unexpectedly succeeded")
	}

	t.Run("missing signing anchor", func(t *testing.T) {
		cfg := *base
		cfg.PinnedPubPEM = nil
		if _, err := Run(&cfg); err == nil || !strings.Contains(err.Error(), "no signing key is configured") {
			t.Fatalf("missing pending anchor error = %v", err)
		}
	})

	t.Run("different signing anchor", func(t *testing.T) {
		other, _, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			t.Fatalf("generate other signing key: %v", err)
		}
		cfg := *base
		cfg.PinnedPubPEM = bundlesig.MarshalPublicKeyPEM(other)
		if _, err := Run(&cfg); err == nil || !strings.Contains(err.Error(), "refusing anchor change") {
			t.Fatalf("changed pending anchor error = %v", err)
		}
	})

	t.Run("different action", func(t *testing.T) {
		cfg := *base
		cfg.InstallArgs = []string{"--uninstall"}
		if _, err := Run(&cfg); err == nil || !strings.Contains(err.Error(), "refusing uninstall") {
			t.Fatalf("changed pending action error = %v", err)
		}
	})

	t.Run("same version different verified root bytes", func(t *testing.T) {
		replacement := filepath.Join(t.TempDir(), "replacement-must-not-run")
		b.files["install.sh"] = []byte("#!/usr/bin/env bash\ntouch " + replacement + "\n")
		resign(t, b)
		cfg := *base
		cfg.Source = staticBundleSource(b.files)
		if _, err := Run(&cfg); err == nil || !strings.Contains(err.Error(), "different same-version candidate") {
			t.Fatalf("same-version substitution error = %v", err)
		}
		if _, err := os.Stat(replacement); !os.IsNotExist(err) {
			t.Fatalf("replacement script ran despite unresolved bundle identity: %v", err)
		}
	})
}

func TestRunPendingApplyMakesMembershipEpochAndKeystoneAnchorEffective(t *testing.T) {
	b := newSignedBundle(t, "2026-07-16T06:00:00Z")
	sentinel := filepath.Join(t.TempDir(), "membership-root-mutations")
	b.files["install.sh"] = []byte("#!/usr/bin/env bash\nprintf x >> " + sentinel + "\nexit 42\n")
	resign(t, b)

	opPub, opPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate operator key: %v", err)
	}
	opPEM := bundlesig.MarshalPublicKeyPEM(opPub)
	addEd25519Membership(t, b.files, opPriv, 7)
	stateDir := t.TempDir()
	base := &Config{
		NodeID:          "alpha",
		Source:          staticBundleSource(b.files),
		PinnedPubPEM:    b.pubPEM,
		OperatorCredPEM: opPEM,
		OperatorCredAlg: string(trustlist.AlgEd25519),
		StateDir:        stateDir,
		Stdout:          &strings.Builder{},
		Stderr:          &strings.Builder{},
	}
	if _, err := Run(base); err == nil {
		t.Fatal("fixture keystone apply unexpectedly succeeded")
	}
	st, err := LoadState(stateDir)
	if err != nil {
		t.Fatalf("load pending keystone state: %v", err)
	}
	if st.PendingApply == nil || !st.PendingApply.MembershipVerified ||
		st.PendingApply.MembershipEpoch != 7 || st.PendingApply.OperatorCredentialFingerprint == "" {
		t.Fatalf("keystone pending intent is incomplete: %+v", st.PendingApply)
	}

	// The trust-list sidecars are outside checksums.sha256. Re-sign an otherwise identical
	// candidate at an older epoch under the same keystone, proving the pending epoch—not
	// bundle-signature or byte-identity drift—prevents a second root execution.
	addEd25519Membership(t, b.files, opPriv, 6)
	base.Source = staticBundleSource(b.files)
	if _, err := Run(base); err == nil || !strings.Contains(err.Error(), "older than last applied 7") {
		t.Fatalf("older membership after interrupted root mutation = %v, want epoch-7 refusal", err)
	}
	if got, readErr := os.ReadFile(sentinel); readErr != nil || string(got) != "x" {
		t.Fatalf("older membership reached root script: %q, %v", got, readErr)
	}

	otherPub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate other operator key: %v", err)
	}
	changed := *base
	changed.OperatorCredPEM = bundlesig.MarshalPublicKeyPEM(otherPub)
	if _, err := Run(&changed); err == nil || !strings.Contains(err.Error(), "different keystone") {
		t.Fatalf("changed keystone during pending recovery error = %v", err)
	}
}

func setBundleManifest(t *testing.T, b *bundleFixture, compiledAt, checksum string) {
	t.Helper()
	manifest, err := json.MarshalIndent(map[string]any{
		"node_id":     "alpha",
		"compiled_at": compiledAt,
		"checksum":    checksum,
	}, "", "  ")
	if err != nil {
		t.Fatalf("marshal manifest: %v", err)
	}
	b.files["manifest.json"] = manifest
}

func addEd25519Membership(t *testing.T, files map[string][]byte, priv ed25519.PrivateKey, epoch int64) {
	t.Helper()
	digest, err := verifiedBundleDigest(files)
	if err != nil {
		t.Fatalf("bundle digest: %v", err)
	}
	tl := trustlist.TrustList{
		SchemaVersion: 1,
		Tenant:        "test",
		Epoch:         epoch,
		Members: []trustlist.Member{{
			NodeID:       "alpha",
			BundleSHA256: digest,
		}},
	}
	canonical, err := trustlist.Canonical(tl)
	if err != nil {
		t.Fatalf("canonical trust list: %v", err)
	}
	signed, err := trustlist.NewEd25519Signer(priv).Sign(tl)
	if err != nil {
		t.Fatalf("sign trust list: %v", err)
	}
	signedJSON, err := json.Marshal(signed)
	if err != nil {
		t.Fatalf("marshal signed trust list: %v", err)
	}
	files["trustlist.json"] = canonical
	files["trustlist.sig"] = signedJSON
}
