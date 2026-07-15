package controller

// compile_promote.go — PromoteStaged and its keystone promote gate.
// Split from compile.go (plan-2); no logic change.

import (
	"context"
	"errors"
	"fmt"
)

// PromoteStaged flips the tenant's staged bundles to current via Store.PromoteStaged,
// after enforcing the KEYSTONE gate: when an operator credential is pinned (keystone
// ON), a promote is refused unless a NON-EMPTY off-host signature exists over EXACTLY
// the staged manifest bytes AND that signature verifies against the pinned credential.
// This is the deploy-time chokepoint that makes the off-host signature mandatory: a
// breached controller can stage anything, but cannot make a node trust it without a
// signature only the off-host key can produce.
//
// Keystone OFF (no credential pinned): promote exactly as before — Store.PromoteStaged
// with no extra gate.
//
// It returns the new generation, ErrNoStagedBundle when nothing is staged, or a
// descriptive error when the keystone gate refuses.
//
// NOTE: with the keystone on this verifies the off-host SIGNATURE over the stored staged
// manifest as an early, operator-visible defense-in-depth check — it does NOT re-derive
// the staged bundles' checksums digests and compare them to the manifest's BundleSHA256
// values. The authoritative chokepoint is the AGENT, which re-derives
// hex(sha256(checksums.sha256)) offline and binds it to its signed member entry before
// applying. Do not mistake this controller gate for the trust root.
func PromoteStaged(ctx context.Context, store Store, t TenantID) (int64, error) {
	// Serialized against any concurrent stage/promote for this tenant — a promote
	// landing mid-stage would flip a partial stage set (see lockTenantOps).
	defer lockTenantOps(t)()
	if err := reconcileKeystoneTrustBoundaryLocked(ctx, store, t); err != nil {
		return 0, err
	}

	cred, err := store.GetOperatorCredential(ctx, t)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			// Keystone OFF: promote as today.
			return store.PromoteStaged(ctx, t)
		}
		return 0, fmt.Errorf("controller: loading operator credential to promote: %w", err)
	}

	// Keystone ON: a valid off-host signature over the staged manifest is mandatory.
	stored, err := store.GetCurrentSignedTrustList(ctx, t)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return 0, errors.New("controller: keystone is enabled but no membership manifest is staged; stage a deploy before promote")
		}
		return 0, fmt.Errorf("controller: loading staged manifest to promote: %w", err)
	}
	if len(stored.SignatureJSON) == 0 {
		return 0, errors.New("controller: the staged membership manifest is not signed off-host yet; sign it (GET /trustlist, POST /trustlist-signature) before promote")
	}

	// Verify the stored off-host signature over the staged manifest against the pinned
	// credential — exactly what a node does offline (the shared verifyStoredAgainstPin, so the
	// promote gate and the redeploy-required signal can never drift). The promote gate refuses on
	// EITHER failure kind (a corrupt record or a non-verifying signature both block a promote).
	if parseErr, verifyErr := verifyStoredAgainstPin(stored, cred); parseErr != nil || verifyErr != nil {
		blocking := verifyErr
		if blocking == nil {
			blocking = parseErr
		}
		return 0, fmt.Errorf("controller: the staged membership manifest is signed under a credential that is no longer the pinned keystone (it was likely signed before a rotation); re-sign it with the current credential (GET /trustlist, POST /trustlist-signature) before promote: %w", blocking)
	}

	return store.PromoteStaged(ctx, t)
}
