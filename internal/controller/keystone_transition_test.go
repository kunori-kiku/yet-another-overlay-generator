package controller

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

// observedKeystoneStore lets the test pause PromoteStaged after it has acquired lockTenantOps and
// reached the keystone read. CompareAndSetOperatorCredential reports when the transition reaches the
// underlying Store. If that signal arrives while the promote is paused, the controller-level wrapper
// failed to serialize keystone mutation with the deploy critical section.
type observedKeystoneStore struct {
	Store
	readEntered chan struct{}
	releaseRead chan struct{}
	casEntered  chan struct{}
	readOnce    sync.Once
	casOnce     sync.Once
}

func (s *observedKeystoneStore) GetOperatorCredential(ctx context.Context, t TenantID) (OperatorCredential, error) {
	s.readOnce.Do(func() { close(s.readEntered) })
	select {
	case <-s.releaseRead:
	case <-ctx.Done():
		return OperatorCredential{}, ctx.Err()
	}
	return s.Store.GetOperatorCredential(ctx, t)
}

func (s *observedKeystoneStore) CompareAndSetOperatorCredential(ctx context.Context, t TenantID, expected *OperatorCredential, next OperatorCredential) error {
	s.casOnce.Do(func() { close(s.casEntered) })
	return s.Store.CompareAndSetOperatorCredential(ctx, t, expected, next)
}

func TestKeystoneTransitionSerializesWithPromote(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	const tenant = TenantID("keystone-transition-order")
	base := NewMemStore()
	if err := base.StageBundle(ctx, tenant, SignedBundle{NodeID: "node-a", Generation: 1}); err != nil {
		t.Fatalf("StageBundle: %v", err)
	}
	store := &observedKeystoneStore{
		Store:       base,
		readEntered: make(chan struct{}),
		releaseRead: make(chan struct{}),
		casEntered:  make(chan struct{}),
	}

	promoteDone := make(chan error, 1)
	go func() {
		_, err := PromoteStaged(ctx, store, tenant)
		promoteDone <- err
	}()
	select {
	case <-store.readEntered:
	case <-ctx.Done():
		t.Fatal("promote did not reach the paused keystone read")
	}

	transitionStarted := make(chan struct{})
	transitionDone := make(chan error, 1)
	go func() {
		close(transitionStarted)
		transitionDone <- CompareAndSetKeystoneCredential(ctx, store, tenant, nil, OperatorCredential{
			Alg:          "ed25519",
			PublicKeyPEM: "test-public-key",
		}, &AuditEntry{Actor: "operator:admin", Action: "pin-operator-credential"})
	}()
	<-transitionStarted

	select {
	case <-store.casEntered:
		t.Fatal("keystone CAS entered the Store while PromoteStaged held the tenant operation lock")
	case <-time.After(50 * time.Millisecond):
		// Expected: the transition is parked on lockTenantOps, before the Store CAS.
	}

	close(store.releaseRead)
	if err := <-promoteDone; err != nil {
		t.Fatalf("PromoteStaged: %v", err)
	}
	if err := <-transitionDone; err != nil {
		t.Fatalf("CompareAndSetKeystoneCredential: %v", err)
	}
	if _, err := base.GetOperatorCredential(ctx, tenant); err != nil {
		t.Fatalf("credential was not pinned after the prior promote completed: %v", err)
	}
}

func TestKeystoneTransitionRetainsStoreCASConflict(t *testing.T) {
	ctx := context.Background()
	const tenant = TenantID("keystone-transition-cas")
	store := NewMemStore()
	current := OperatorCredential{Alg: "ed25519", PublicKeyPEM: "current"}
	if err := store.CompareAndSetOperatorCredential(ctx, tenant, nil, current); err != nil {
		t.Fatalf("CompareAndSetOperatorCredential: %v", err)
	}
	stale := OperatorCredential{Alg: "ed25519", PublicKeyPEM: "stale"}
	if err := CompareAndSetKeystoneCredential(ctx, store, tenant, &stale, stale, nil); !errors.Is(err, ErrOperatorCredentialChanged) {
		t.Fatalf("stale transition = %v, want ErrOperatorCredentialChanged", err)
	}
}

func TestKeystoneTransitionRejectsUnauditedMutation(t *testing.T) {
	ctx := context.Background()
	const tenant = TenantID("keystone-transition-audit-required")
	store := NewMemStore()
	first := OperatorCredential{Alg: "ed25519", PublicKeyPEM: "first"}
	if err := CompareAndSetKeystoneCredential(ctx, store, tenant, nil, first, nil); !errors.Is(err, ErrKeystoneAuditRequired) {
		t.Fatalf("unaudited first pin = %v, want ErrKeystoneAuditRequired", err)
	}
	if _, err := store.GetOperatorCredential(ctx, tenant); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unaudited first pin mutated store: %v", err)
	}
	if err := CompareAndSetKeystoneCredential(ctx, store, tenant, nil, first, &AuditEntry{
		Actor: "operator:admin", Action: "pin-operator-credential",
	}); err != nil {
		t.Fatalf("audited first pin: %v", err)
	}
	second := OperatorCredential{Alg: "ed25519", PublicKeyPEM: "second"}
	if err := CompareAndSetKeystoneCredential(ctx, store, tenant, &first, second, nil); !errors.Is(err, ErrKeystoneAuditRequired) {
		t.Fatalf("unaudited rotation = %v, want ErrKeystoneAuditRequired", err)
	}
	if got, err := store.GetOperatorCredential(ctx, tenant); err != nil || got != first {
		t.Fatalf("unaudited rotation changed credential = (%+v, %v), want first", got, err)
	}
	if err := CompareAndSetKeystoneCredential(ctx, store, tenant, &first, first, nil); err != nil {
		t.Fatalf("exact compare-only call: %v", err)
	}
}
