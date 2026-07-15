package controller

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/kunorikiku/yet-another-overlay-generator/internal/bundlesig"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/trustlist"
)

type keystoneTransitionFaultStore struct {
	Store
	failCASBefore        int
	failCASAfter         int
	failAppendBefore     int
	failAppendAfter      int
	failTransitionDelete int
}

func (s *keystoneTransitionFaultStore) CompareAndSetOperatorCredential(ctx context.Context, t TenantID, expected *OperatorCredential, next OperatorCredential) error {
	if s.failCASBefore > 0 {
		s.failCASBefore--
		return errors.New("injected credential CAS failure before commit")
	}
	err := s.Store.CompareAndSetOperatorCredential(ctx, t, expected, next)
	if err == nil && s.failCASAfter > 0 {
		s.failCASAfter--
		return errors.New("injected credential CAS failure after commit")
	}
	return err
}

func (s *keystoneTransitionFaultStore) AppendAudit(ctx context.Context, t TenantID, entry AuditEntry) (AuditEntry, error) {
	if s.failAppendBefore > 0 {
		s.failAppendBefore--
		return AuditEntry{}, errors.New("injected audit append failure before commit")
	}
	stored, err := s.Store.AppendAudit(ctx, t, entry)
	if err == nil && s.failAppendAfter > 0 {
		s.failAppendAfter--
		return stored, errors.New("injected audit append failure after commit")
	}
	return stored, err
}

func (s *keystoneTransitionFaultStore) DeletePendingKeystoneTransition(ctx context.Context, t TenantID, eventID string) error {
	if s.failTransitionDelete > 0 {
		s.failTransitionDelete--
		return errors.New("injected pending-transition delete failure")
	}
	return s.Store.DeletePendingKeystoneTransition(ctx, t, eventID)
}

func openKeystoneTransitionFileStore(t *testing.T, root string) *FileStore {
	t.Helper()
	store, err := NewFileStore(root)
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}
	return store
}

func auditedKeystoneTransition(ctx context.Context, store Store, tenant TenantID, expected *OperatorCredential, next OperatorCredential) error {
	return CompareAndSetKeystoneCredential(ctx, store, tenant, expected, next, &AuditEntry{
		Actor:  "operator:admin",
		Action: "pin-operator-credential",
	})
}

func assertOneKeystoneTransitionAudit(t *testing.T, store Store, tenant TenantID) AuditEntry {
	t.Helper()
	entries, err := store.ListAudit(context.Background(), tenant)
	if err != nil {
		t.Fatalf("ListAudit: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("audit entries = %d, want exactly 1: %+v", len(entries), entries)
	}
	entry := entries[0]
	if entry.EventID == "" || entry.Actor != "operator:admin" || entry.Action != "pin-operator-credential" {
		t.Fatalf("unexpected recovered audit entry: %+v", entry)
	}
	if bad := VerifyAuditChain(entries); bad != -1 {
		t.Fatalf("VerifyAuditChain = %d, want -1", bad)
	}
	return entry
}

func assertNoPendingKeystoneTransition(t *testing.T, store Store, tenant TenantID) {
	t.Helper()
	if pending, err := store.GetPendingKeystoneTransition(context.Background(), tenant); !errors.Is(err, ErrNotFound) {
		t.Fatalf("GetPendingKeystoneTransition = (%+v, %v), want ErrNotFound", pending, err)
	}
}

func TestKeystoneTransitionCASFailureRetainsThenRestartsAfterReopen(t *testing.T) {
	ctx := context.Background()
	const tenant = TenantID("keystone-cas-failure")
	root := t.TempDir()
	base := openKeystoneTransitionFileStore(t, root)
	next := OperatorCredential{Alg: "ed25519", PublicKeyPEM: "next-public-key"}
	faults := &keystoneTransitionFaultStore{Store: base, failCASBefore: 1}

	if err := auditedKeystoneTransition(ctx, faults, tenant, nil, next); err == nil {
		t.Fatal("transition with injected CAS failure succeeded, want error")
	}
	if _, err := base.GetPendingKeystoneTransition(ctx, tenant); err != nil {
		t.Fatalf("pending marker must survive CAS error: %v", err)
	}
	if _, err := base.GetOperatorCredential(ctx, tenant); !errors.Is(err, ErrNotFound) {
		t.Fatalf("credential after pre-commit CAS failure = %v, want ErrNotFound", err)
	}
	if entries, err := base.ListAudit(ctx, tenant); err != nil || len(entries) != 0 {
		t.Fatalf("audit after uncommitted CAS = (%+v, %v), want empty", entries, err)
	}

	reopened := openKeystoneTransitionFileStore(t, root)
	if err := auditedKeystoneTransition(ctx, reopened, tenant, nil, next); err != nil {
		t.Fatalf("retry after reopen: %v", err)
	}
	assertOneKeystoneTransitionAudit(t, reopened, tenant)
	assertNoPendingKeystoneTransition(t, reopened, tenant)
}

func TestKeystoneTransitionCASCommittedThenErroredHealsOnStatusRead(t *testing.T) {
	ctx := context.Background()
	const tenant = TenantID("keystone-cas-ambiguous")
	root := t.TempDir()
	base := openKeystoneTransitionFileStore(t, root)
	next := OperatorCredential{Alg: "ed25519", PublicKeyPEM: "next-public-key"}
	faults := &keystoneTransitionFaultStore{Store: base, failCASAfter: 1}

	if err := auditedKeystoneTransition(ctx, faults, tenant, nil, next); err == nil {
		t.Fatal("transition with post-commit CAS error succeeded, want ambiguous error")
	}
	if got, err := base.GetOperatorCredential(ctx, tenant); err != nil || got != next {
		t.Fatalf("credential after ambiguous CAS = (%+v, %v), want next", got, err)
	}
	if entries, err := base.ListAudit(ctx, tenant); err != nil || len(entries) != 0 {
		t.Fatalf("audit before recovery = (%+v, %v), want empty", entries, err)
	}

	reopened := openKeystoneTransitionFileStore(t, root)
	if got, err := GetKeystoneCredential(ctx, reopened, tenant); err != nil || got != next {
		t.Fatalf("status read recovery = (%+v, %v), want next", got, err)
	}
	assertOneKeystoneTransitionAudit(t, reopened, tenant)
	assertNoPendingKeystoneTransition(t, reopened, tenant)
}

func TestKeystoneTransitionAppendFailureHealsExactlyOnceAfterReopen(t *testing.T) {
	ctx := context.Background()
	const tenant = TenantID("keystone-append-failure")
	root := t.TempDir()
	base := openKeystoneTransitionFileStore(t, root)
	next := OperatorCredential{Alg: "ed25519", PublicKeyPEM: "next-public-key"}
	faults := &keystoneTransitionFaultStore{Store: base, failAppendBefore: 1}

	if err := auditedKeystoneTransition(ctx, faults, tenant, nil, next); err == nil {
		t.Fatal("transition with pre-commit append failure succeeded, want error")
	}
	if got, err := base.GetOperatorCredential(ctx, tenant); err != nil || got != next {
		t.Fatalf("credential must remain committed = (%+v, %v), want next", got, err)
	}
	if _, err := base.GetPendingKeystoneTransition(ctx, tenant); err != nil {
		t.Fatalf("pending marker must survive audit failure: %v", err)
	}

	reopened := openKeystoneTransitionFileStore(t, root)
	if got, err := GetKeystoneCredential(ctx, reopened, tenant); err != nil || got != next {
		t.Fatalf("status read recovery = (%+v, %v), want next", got, err)
	}
	first := assertOneKeystoneTransitionAudit(t, reopened, tenant)
	assertNoPendingKeystoneTransition(t, reopened, tenant)

	// Both recovery-aware reads and the handler's idempotent compare-only path must remain
	// duplicate-free after the marker is gone.
	if got, err := GetKeystoneCredential(ctx, reopened, tenant); err != nil || got != next {
		t.Fatalf("second status read = (%+v, %v), want next", got, err)
	}
	if err := CompareAndSetKeystoneCredential(ctx, reopened, tenant, &next, next, nil); err != nil {
		t.Fatalf("idempotent compare-only retry: %v", err)
	}
	if got := assertOneKeystoneTransitionAudit(t, reopened, tenant); got.EventID != first.EventID {
		t.Fatalf("audit identity changed across idempotent retries: %q -> %q", first.EventID, got.EventID)
	}
}

func TestKeystoneTransitionIdempotentCompareOnlyHealsPendingAudit(t *testing.T) {
	ctx := context.Background()
	const tenant = TenantID("keystone-compare-only-heal")
	base := NewMemStore()
	next := OperatorCredential{Alg: "ed25519", PublicKeyPEM: "next-public-key"}
	faults := &keystoneTransitionFaultStore{Store: base, failAppendBefore: 1}

	if err := auditedKeystoneTransition(ctx, faults, tenant, nil, next); err == nil {
		t.Fatal("transition with pre-commit append failure succeeded, want error")
	}
	if err := CompareAndSetKeystoneCredential(ctx, base, tenant, &next, next, nil); err != nil {
		t.Fatalf("idempotent compare-only recovery: %v", err)
	}
	assertOneKeystoneTransitionAudit(t, base, tenant)
	assertNoPendingKeystoneTransition(t, base, tenant)
}

func TestKeystoneTransitionAppendCommittedThenErroredIsReportedAsSuccess(t *testing.T) {
	ctx := context.Background()
	const tenant = TenantID("keystone-append-ambiguous")
	root := t.TempDir()
	base := openKeystoneTransitionFileStore(t, root)
	next := OperatorCredential{Alg: "ed25519", PublicKeyPEM: "next-public-key"}
	faults := &keystoneTransitionFaultStore{Store: base, failAppendAfter: 1}

	if err := auditedKeystoneTransition(ctx, faults, tenant, nil, next); err != nil {
		t.Fatalf("durably appended audit must turn ambiguous append error into success: %v", err)
	}
	first := assertOneKeystoneTransitionAudit(t, base, tenant)
	assertNoPendingKeystoneTransition(t, base, tenant)

	reopened := openKeystoneTransitionFileStore(t, root)
	if got, err := GetKeystoneCredential(ctx, reopened, tenant); err != nil || got != next {
		t.Fatalf("status after reopen = (%+v, %v), want next", got, err)
	}
	if got := assertOneKeystoneTransitionAudit(t, reopened, tenant); got.EventID != first.EventID {
		t.Fatalf("audit identity changed after reopen: %q -> %q", first.EventID, got.EventID)
	}
}

func TestKeystoneTransitionDeleteFailureDoesNotDuplicateAudit(t *testing.T) {
	ctx := context.Background()
	const tenant = TenantID("keystone-delete-failure")
	root := t.TempDir()
	base := openKeystoneTransitionFileStore(t, root)
	next := OperatorCredential{Alg: "ed25519", PublicKeyPEM: "next-public-key"}
	faults := &keystoneTransitionFaultStore{Store: base, failTransitionDelete: 1}

	if err := auditedKeystoneTransition(ctx, faults, tenant, nil, next); err == nil {
		t.Fatal("transition with marker-delete failure succeeded, want error")
	}
	first := assertOneKeystoneTransitionAudit(t, base, tenant)
	if _, err := base.GetPendingKeystoneTransition(ctx, tenant); err != nil {
		t.Fatalf("marker must survive delete failure: %v", err)
	}

	reopened := openKeystoneTransitionFileStore(t, root)
	if got, err := GetKeystoneCredential(ctx, reopened, tenant); err != nil || got != next {
		t.Fatalf("status read after delete failure = (%+v, %v), want next", got, err)
	}
	if got := assertOneKeystoneTransitionAudit(t, reopened, tenant); got.EventID != first.EventID {
		t.Fatalf("audit identity changed during delete healing: %q -> %q", first.EventID, got.EventID)
	}
	assertNoPendingKeystoneTransition(t, reopened, tenant)
}

func TestKeystoneTransitionNeverAuditsUncommittedConflictingMarker(t *testing.T) {
	ctx := context.Background()
	const tenant = TenantID("keystone-conflicting-marker")
	store := NewMemStore()
	expected := OperatorCredential{Alg: "ed25519", PublicKeyPEM: "expected"}
	next := OperatorCredential{Alg: "ed25519", PublicKeyPEM: "next"}
	other := OperatorCredential{Alg: "ed25519", PublicKeyPEM: "out-of-band"}
	if err := store.CompareAndSetOperatorCredential(ctx, tenant, nil, other); err != nil {
		t.Fatal(err)
	}
	if err := store.CreatePendingKeystoneTransition(ctx, tenant, PendingKeystoneTransition{
		Expected: &expected,
		Next:     next,
		Audit: AuditEntry{
			Timestamp: mustKeystoneTransitionTimestamp(t),
			Actor:     "operator:admin",
			Action:    "rotate-operator-credential",
			EventID:   "00000000000000000000000000000001",
		},
	}); err != nil {
		t.Fatal(err)
	}

	_, err := GetKeystoneCredential(ctx, store, tenant)
	if !errors.Is(err, ErrPendingKeystoneTransitionConflict) {
		t.Fatalf("GetKeystoneCredential conflict = %v, want ErrPendingKeystoneTransitionConflict", err)
	}
	entries, listErr := store.ListAudit(ctx, tenant)
	if listErr != nil || len(entries) != 0 {
		t.Fatalf("conflicting uncommitted marker produced audit = (%+v, %v)", entries, listErr)
	}
	if _, markerErr := store.GetPendingKeystoneTransition(ctx, tenant); markerErr != nil {
		t.Fatalf("conflicting marker must be retained: %v", markerErr)
	}
}

func TestKeystoneTransitionAuditIsBarrierAfterRestart(t *testing.T) {
	operations := []struct {
		name string
		run  func(context.Context, Store, TenantID) error
	}{
		{
			name: "stage",
			run: func(ctx context.Context, store Store, tenant TenantID) error {
				_, err := CompileAndStage(ctx, store, tenant, time.Now().UTC())
				return err
			},
		},
		{
			name: "sign",
			run: func(ctx context.Context, store Store, tenant TenantID) error {
				_, err := InstallTrustListSignature(ctx, store, tenant, []byte(`{}`), trustlist.SignedTrustList{})
				return err
			},
		},
		{
			name: "promote",
			run: func(ctx context.Context, store Store, tenant TenantID) error {
				_, err := PromoteStaged(ctx, store, tenant)
				return err
			},
		},
	}

	for _, operation := range operations {
		t.Run(operation.name, func(t *testing.T) {
			ctx := context.Background()
			tenant := TenantID("keystone-audit-barrier-" + operation.name)
			root := t.TempDir()
			base := openKeystoneTransitionFileStore(t, root)
			if err := base.StageBundle(ctx, tenant, SignedBundle{NodeID: "alpha", Generation: 1}); err != nil {
				t.Fatalf("StageBundle: %v", err)
			}
			next := OperatorCredential{Alg: "ed25519", PublicKeyPEM: "next-public-key"}
			initialFault := &keystoneTransitionFaultStore{Store: base, failAppendBefore: 1}
			if err := auditedKeystoneTransition(ctx, initialFault, tenant, nil, next); err == nil {
				t.Fatal("transition with audit failure succeeded")
			}

			// Reopen to prove the barrier is durable, then keep the audit backend unavailable for
			// the first trust-sensitive operation. It must stop at reconciliation rather than use
			// the already-committed new credential.
			reopened := openKeystoneTransitionFileStore(t, root)
			blocking := &keystoneTransitionFaultStore{Store: reopened, failAppendBefore: 1}
			err := operation.run(ctx, blocking, tenant)
			if err == nil || !strings.Contains(err.Error(), "injected audit append failure") {
				t.Fatalf("operation crossed unavailable audit barrier: %v", err)
			}
			if generation, err := reopened.CurrentGeneration(ctx, tenant); err != nil || generation != 0 {
				t.Fatalf("generation after blocked operation = (%d, %v), want 0", generation, err)
			}
			if entries, err := reopened.ListAudit(ctx, tenant); err != nil || len(entries) != 0 {
				t.Fatalf("audit before recovery = (%+v, %v), want empty", entries, err)
			}
			if _, err := reopened.GetPendingKeystoneTransition(ctx, tenant); err != nil {
				t.Fatalf("pending marker was lost after blocked operation: %v", err)
			}

			if got, err := GetKeystoneCredential(ctx, reopened, tenant); err != nil || got != next {
				t.Fatalf("recovery after audit returns = (%+v, %v), want next", got, err)
			}
			assertOneKeystoneTransitionAudit(t, reopened, tenant)
			assertNoPendingKeystoneTransition(t, reopened, tenant)
		})
	}
}

// TestDeployPreviewReconcilesKeystoneAuditBeforeUse keeps the read-only preview on the same
// audit-before-use boundary as stage/sign/promote and bootstrap. Although preview does not mutate
// deployment state, it uses the pinned credential to decide whether a full-fleet re-stage is needed;
// it must not consult a newly committed anchor while that transition remains unaudited.
func TestDeployPreviewReconcilesKeystoneAuditBeforeUse(t *testing.T) {
	t.Setenv(bundlesig.EnvSigningKey, writeControllerBundleSigningKey(t))
	t.Setenv(bundlesig.EnvSigningKeyRotate, "")

	ctx := context.Background()
	const tenant = TenantID("keystone-audit-preview-barrier")
	base := NewMemStore()
	faults := &keystoneTransitionFaultStore{Store: base, failAppendBefore: 2}
	next := OperatorCredential{Alg: "ed25519", PublicKeyPEM: "next-public-key"}
	if err := auditedKeystoneTransition(ctx, faults, tenant, nil, next); err == nil {
		t.Fatal("transition with injected audit failure succeeded")
	}

	putStageTopo(t, base, tenant)
	for _, id := range []string{"node-router", "node-peer", "node-client"} {
		approveNode(t, ctx, base, tenant, id, genWGPubKey(t))
	}

	if _, err := DeployPreview(ctx, faults, tenant, stageTestTopo()); err == nil || !strings.Contains(err.Error(), "injected audit append failure") {
		t.Fatalf("preview crossed unavailable audit boundary: %v", err)
	}
	if entries, err := base.ListAudit(ctx, tenant); err != nil || len(entries) != 0 {
		t.Fatalf("audit before preview recovery = (%+v, %v), want empty", entries, err)
	}
	if _, err := base.GetPendingKeystoneTransition(ctx, tenant); err != nil {
		t.Fatalf("preview lost pending transition marker: %v", err)
	}

	preview, err := DeployPreview(ctx, base, tenant, stageTestTopo())
	if err != nil {
		t.Fatalf("DeployPreview after audit recovery: %v", err)
	}
	if !preview.KeystoneFullRestage || len(preview.Nodes) != 3 {
		t.Fatalf("recovered first-pin preview = %+v, want full restage for three nodes", preview)
	}
	assertOneKeystoneTransitionAudit(t, base, tenant)
	assertNoPendingKeystoneTransition(t, base, tenant)
}

func mustKeystoneTransitionTimestamp(t *testing.T) time.Time {
	t.Helper()
	stamp, err := time.Parse(time.RFC3339Nano, "2026-07-16T12:00:00Z")
	if err != nil {
		t.Fatal(fmt.Errorf("parse test timestamp: %w", err))
	}
	return stamp
}
