package controller

// trustlist_sign.go — InstallTrustListSignature installs the operator's off-host signature over the
// STAGED membership manifest under the SAME per-tenant op lock that serializes CompileAndStage /
// PromoteStaged. Splitting the read-verify-write out of the api handler and putting the WHOLE thing
// under lockTenantOps closes the sign-vs-restage custody desync: a CompileAndStage landing between the
// handler's manifest READ and its signed-manifest WRITE would otherwise pair a stale signed manifest
// (M_old) with the freshly staged bundles (B_new). The promote gate checks only signature-vs-pin, so
// it would silently promote that desynced pair — nodes then fetch a B_new bundle bound to M_old,
// VerifyMembership mismatches, and the fleet strands (fail-closed, recoverable). Serializing here makes
// the (staged manifest, staged bundles) pair a single consistent unit.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/kunorikiku/yet-another-overlay-generator/internal/trustlist"
)

// InstallTrustListSignature verifies and installs the operator's off-host signature over the tenant's
// STAGED membership manifest, atomically against any concurrent stage/promote, and returns the signed
// manifest's epoch. It mirrors PromoteStaged's shape: ONE `defer lockTenantOps(t)()` acquisition spans
// the whole critical section — read the current staged manifest, the substitution guard, the signature
// verification, and the write-back — so a CompileAndStage cannot interleave.
//
// The api handler decodes the request (base64 of the submitted canonical bytes + the detached
// signature) and maps these typed sentinels to HTTP codes:
//   - ErrNoPinnedCredential       — keystone OFF (no anchor to verify against) → 412
//   - ErrNoStagedManifest         — nothing has been staged yet                → 404
//   - ErrStagedManifestMismatch   — a re-stage moved the staged manifest; the submitted bytes are stale → 409
//   - ErrManifestSignatureInvalid — the signature does not verify against the pin → 400
//
// DEADLOCK AUDIT: every store method called inside the lock (GetOperatorCredential,
// GetCurrentSignedTrustList, PutSignedTrustList) serializes on the backend's own kv lock via
// c.kv.withLock, NEVER on lockTenantOps — so there is no re-entrancy of the (non-reentrant) tenant op
// mutex, and the lock order is always tenantOpMu (outer) → kv lock (inner), matching CompileAndStage /
// PromoteStaged / Enroll / Rekey. pinFromOperatorCredential, trustlist.Verify, json.*, and bytes.Equal
// take no locks.
func InstallTrustListSignature(ctx context.Context, store Store, t TenantID, submittedCanonical []byte, signed trustlist.SignedTrustList) (int64, error) {
	defer lockTenantOps(t)()
	if err := reconcileKeystoneTrustBoundaryLocked(ctx, store, t); err != nil {
		return 0, err
	}

	// A signature is meaningless without a pinned credential to verify it against.
	cred, err := store.GetOperatorCredential(ctx, t)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return 0, ErrNoPinnedCredential
		}
		return 0, fmt.Errorf("controller: loading operator credential to install trust-list signature: %w", err)
	}

	// Re-read the CURRENTLY staged manifest under the lock — the same read the handler did, now inside
	// the critical section so a re-stage cannot slip between it and the write-back below.
	stored, err := store.GetCurrentSignedTrustList(ctx, t)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return 0, ErrNoStagedManifest
		}
		return 0, fmt.Errorf("controller: loading staged manifest to install trust-list signature: %w", err)
	}

	// Substitution guard: the operator must have signed EXACTLY the bytes currently staged. If a
	// re-stage moved the staged manifest since GET /trustlist, these differ and we reject so the
	// operator re-fetches and re-signs — a stale (M_old) signature can never be installed onto a
	// B_new stage.
	if !bytes.Equal(submittedCanonical, stored.TrustListJSON) {
		return 0, ErrStagedManifestMismatch
	}

	// Verify the off-host signature over the staged manifest's canonical bytes against the pin (Verify
	// re-canonicalizes the parsed manifest internally, so it checks the exact stored bytes).
	var manifest trustlist.TrustList
	if err := json.Unmarshal(stored.TrustListJSON, &manifest); err != nil {
		return 0, fmt.Errorf("controller: parsing staged manifest to verify signature: %w", err)
	}
	pin, err := pinFromOperatorCredential(cred)
	if err != nil {
		return 0, fmt.Errorf("controller: building pin from operator credential: %w", err)
	}
	if err := trustlist.Verify(manifest, signed, pin); err != nil {
		return 0, fmt.Errorf("%w: %v", ErrManifestSignatureInvalid, err)
	}

	// Persist the signature onto the staged manifest record — canonical bytes + epoch UNCHANGED, so
	// PromoteStaged still matches and re-verifies these exact bytes.
	signedJSON, err := json.Marshal(signed)
	if err != nil {
		return 0, fmt.Errorf("controller: marshalling trust-list signature: %w", err)
	}
	if err := store.PutSignedTrustList(ctx, t, StoredTrustList{
		TrustListJSON: stored.TrustListJSON,
		SignatureJSON: signedJSON,
		Epoch:         stored.Epoch,
	}); err != nil {
		return 0, fmt.Errorf("controller: storing signed staged manifest: %w", err)
	}
	return stored.Epoch, nil
}
