package controller

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"
)

// CompareAndSetKeystoneCredential is the controller-level keystone mutation boundary. It orders a
// credential transition with stage/sign/promote under lockTenantOps and uses a durable write-ahead
// marker to make the credential CAS and its audit record recover as one logical transition.
//
// Keep public-key verification and browser work outside this lock. Callers classify and verify
// first, then pass the exact credential snapshot here. Every entry reconciles an older marker before
// starting new work, including compare-only idempotent calls. An audited transition writes its exact
// expected/next/audit identity before the CAS; once Next is current, reconciliation appends that
// audit identity exactly once and clears the marker. Errors retain enough state for a later mutation
// or GetKeystoneCredential status read to finish the transition after a process restart.
func CompareAndSetKeystoneCredential(ctx context.Context, store Store, t TenantID, expected *OperatorCredential, next OperatorCredential, audit *AuditEntry) error {
	defer lockTenantOps(t)()

	if err := reconcilePendingKeystoneTransitionLocked(ctx, store, t); err != nil {
		return err
	}
	compareOnly := expected != nil && *expected == next
	if compareOnly {
		if audit != nil {
			return fmt.Errorf("%w: an exact compare-only keystone check must not create an audit event", ErrKeystoneAuditRequired)
		}
		return store.CompareAndSetOperatorCredential(ctx, t, expected, next)
	}
	if err := validateKeystoneTransitionAudit(expected, audit); err != nil {
		return err
	}

	// Reject a stale classified request before publishing a new marker. In particular, a replay of
	// an already-committed target must not manufacture a second audit event after recovery cleared
	// the first marker.
	current, present, err := currentKeystoneCredential(ctx, store, t)
	if err != nil {
		return err
	}
	if !credentialStateMatches(current, present, expected) {
		return ErrOperatorCredentialChanged
	}

	eventID, err := newKeystoneAuditEventID()
	if err != nil {
		return err
	}
	entry := *audit
	entry.Seq = 0
	entry.Timestamp = time.Now().UTC()
	entry.EventID = eventID
	entry.PrevHash = ""
	entry.Hash = ""
	pending := PendingKeystoneTransition{
		Expected: cloneOperatorCredential(expected),
		Next:     next,
		Audit:    entry,
	}
	if err := store.CreatePendingKeystoneTransition(ctx, t, pending); err != nil {
		return fmt.Errorf("controller: writing pending keystone transition: %w", err)
	}
	if err := store.CompareAndSetOperatorCredential(ctx, t, expected, next); err != nil {
		// Deliberately retain the marker: a backend may have committed the CAS before returning an
		// error. The next entry distinguishes that shape from a CAS that never committed.
		return err
	}
	return reconcilePendingKeystoneTransitionLocked(ctx, store, t)
}

// reconcileKeystoneTrustBoundaryLocked is the mandatory first step for every tenant-locked
// operation that can use the pinned credential. It establishes "audit before use": if a prior CAS
// committed but its audit append did not, stage/sign/promote either durably reconciles that exact
// event first or fails without consulting the new trust anchor. Caller must hold lockTenantOps(t).
func reconcileKeystoneTrustBoundaryLocked(ctx context.Context, store Store, t TenantID) error {
	if err := reconcilePendingKeystoneTransitionLocked(ctx, store, t); err != nil {
		return fmt.Errorf("controller: reconciling keystone transition before trust operation: %w", err)
	}
	return nil
}

func validateKeystoneTransitionAudit(expected *OperatorCredential, audit *AuditEntry) error {
	if audit == nil {
		return ErrKeystoneAuditRequired
	}
	wantAction := "pin-operator-credential"
	if expected != nil {
		wantAction = "rotate-operator-credential"
	}
	if audit.Action != wantAction {
		return fmt.Errorf("%w: keystone transition action = %q, want %q", ErrKeystoneAuditRequired, audit.Action, wantAction)
	}
	if !strings.HasPrefix(audit.Actor, "operator:") || strings.TrimSpace(strings.TrimPrefix(audit.Actor, "operator:")) == "" {
		return fmt.Errorf("%w: keystone transition actor must identify an operator", ErrKeystoneAuditRequired)
	}
	if !audit.Timestamp.IsZero() || audit.Seq != 0 || audit.EventID != "" || audit.PrevHash != "" || audit.Hash != "" || audit.NodeID != "" {
		return fmt.Errorf("%w: caller supplied reserved keystone audit identity fields", ErrKeystoneAuditRequired)
	}
	return nil
}

// GetKeystoneCredential is the recovery-aware credential read for callers that do not already hold
// the tenant operation lock. A failed pin response may have committed the credential before its audit
// append; this read heals that durable marker before reporting, distributing, or otherwise consulting
// the new trust anchor. The operator status path can therefore treat a matching candidate as committed
// without repeating the mutation, while bootstrap and preview cannot cross an unavailable audit gate.
func GetKeystoneCredential(ctx context.Context, store Store, t TenantID) (OperatorCredential, error) {
	defer lockTenantOps(t)()
	if err := reconcilePendingKeystoneTransitionLocked(ctx, store, t); err != nil {
		return OperatorCredential{}, err
	}
	return store.GetOperatorCredential(ctx, t)
}

func newKeystoneAuditEventID() (string, error) {
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		return "", fmt.Errorf("controller: generating keystone audit event id: %w", err)
	}
	return hex.EncodeToString(id[:]), nil
}

func cloneOperatorCredential(cred *OperatorCredential) *OperatorCredential {
	if cred == nil {
		return nil
	}
	copy := *cred
	return &copy
}

func currentKeystoneCredential(ctx context.Context, store Store, t TenantID) (OperatorCredential, bool, error) {
	current, err := store.GetOperatorCredential(ctx, t)
	if errors.Is(err, ErrNotFound) {
		return OperatorCredential{}, false, nil
	}
	if err != nil {
		return OperatorCredential{}, false, err
	}
	return current, true, nil
}

func credentialStateMatches(current OperatorCredential, present bool, expected *OperatorCredential) bool {
	if expected == nil {
		return !present
	}
	return present && current == *expected
}

// reconcilePendingKeystoneTransitionLocked resolves the one durable marker for t. Caller must hold
// lockTenantOps(t). It never appends an audit record unless the exact target credential is current.
func reconcilePendingKeystoneTransitionLocked(ctx context.Context, store Store, t TenantID) error {
	pending, err := store.GetPendingKeystoneTransition(ctx, t)
	if errors.Is(err, ErrNotFound) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("controller: loading pending keystone transition: %w", err)
	}
	if err := validatePendingKeystoneTransition(pending); err != nil {
		return err
	}

	current, present, err := currentKeystoneCredential(ctx, store, t)
	if err != nil {
		return fmt.Errorf("controller: loading credential for pending keystone transition: %w", err)
	}
	if present && current == pending.Next {
		return ensurePendingKeystoneAuditLocked(ctx, store, t, pending)
	}
	if credentialStateMatches(current, present, pending.Expected) {
		// The CAS did not commit. Clearing is safe; a caller entering through CompareAndSet can now
		// start a fresh transition, while a status read simply reports the unchanged credential.
		if err := store.DeletePendingKeystoneTransition(ctx, t, pending.Audit.EventID); err != nil {
			return fmt.Errorf("controller: clearing uncommitted keystone transition: %w", err)
		}
		return nil
	}
	return fmt.Errorf("%w: current credential matches neither expected nor next", ErrPendingKeystoneTransitionConflict)
}

func validatePendingKeystoneTransition(pending PendingKeystoneTransition) error {
	if len(pending.Audit.EventID) != 32 {
		return errors.New("controller: invalid pending keystone transition audit identity")
	}
	if _, err := hex.DecodeString(pending.Audit.EventID); err != nil || pending.Audit.Timestamp.IsZero() {
		return errors.New("controller: invalid pending keystone transition audit identity")
	}
	if pending.Audit.Seq != 0 || pending.Audit.PrevHash != "" || pending.Audit.Hash != "" {
		return errors.New("controller: invalid pending keystone transition contains chained audit fields")
	}
	if pending.Expected != nil && *pending.Expected == pending.Next {
		return errors.New("controller: invalid pending keystone transition is a no-op")
	}
	wantAction := "pin-operator-credential"
	if pending.Expected != nil {
		wantAction = "rotate-operator-credential"
	}
	if pending.Audit.Action != wantAction || pending.Audit.NodeID != "" ||
		!strings.HasPrefix(pending.Audit.Actor, "operator:") || strings.TrimSpace(strings.TrimPrefix(pending.Audit.Actor, "operator:")) == "" {
		return errors.New("controller: invalid pending keystone transition audit content")
	}
	return nil
}

func ensurePendingKeystoneAuditLocked(ctx context.Context, store Store, t TenantID, pending PendingKeystoneTransition) error {
	found, err := findPendingKeystoneAudit(ctx, store, t, pending.Audit)
	if err != nil {
		return err
	}
	if !found {
		_, appendErr := store.AppendAudit(ctx, t, pending.Audit)

		// Always re-read, even after an append error. A durable append may have committed before the
		// backend reported a sync/close/transport fault; EventID lets us prove that exact event landed.
		foundAfter, listErr := findPendingKeystoneAudit(ctx, store, t, pending.Audit)
		if listErr != nil {
			if appendErr != nil {
				return errors.Join(fmt.Errorf("controller: appending pending keystone audit: %w", appendErr), listErr)
			}
			return listErr
		}
		if !foundAfter {
			if appendErr != nil {
				return fmt.Errorf("controller: appending pending keystone audit: %w", appendErr)
			}
			return errors.New("controller: pending keystone audit append was not observable")
		}
	}

	if err := store.DeletePendingKeystoneTransition(ctx, t, pending.Audit.EventID); err != nil {
		return fmt.Errorf("controller: clearing committed keystone transition: %w", err)
	}
	return nil
}

func findPendingKeystoneAudit(ctx context.Context, store Store, t TenantID, want AuditEntry) (bool, error) {
	entries, err := store.ListAudit(ctx, t)
	if err != nil {
		return false, fmt.Errorf("controller: listing audit for pending keystone transition: %w", err)
	}
	found := false
	for _, entry := range entries {
		if entry.EventID != want.EventID {
			continue
		}
		if found {
			return false, fmt.Errorf("controller: duplicate keystone audit event id %q", want.EventID)
		}
		if !samePendingKeystoneAudit(entry, want) {
			return false, fmt.Errorf("controller: keystone audit event id %q has conflicting content", want.EventID)
		}
		found = true
	}
	return found, nil
}

func samePendingKeystoneAudit(stored, want AuditEntry) bool {
	return stored.Timestamp.Equal(want.Timestamp) &&
		stored.Actor == want.Actor &&
		stored.Action == want.Action &&
		stored.NodeID == want.NodeID &&
		stored.EventID == want.EventID
}
