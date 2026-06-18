package controller

import (
	"context"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/kunorikiku/yet-another-overlay-generator/internal/apierr"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/bundlesig"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/trustlist"
)

// bundleSHA256 is the lowercase-hex SHA-256 of a bundle's checksums.sha256 bytes — the
// digest bound into each member of the off-host-signed manifest. checksums.sha256 covers
// install.sh AND every config, so this single digest pins the entire deployed bundle: a
// tampered install.sh changes checksums.sha256, which changes this digest, which the
// breached controller cannot re-sign without the off-host key. It is computed from the
// SAME bytes the agent re-derives (files["checksums.sha256"]).
func bundleSHA256(checksums []byte) string {
	sum := sha256.Sum256(checksums)
	return hex.EncodeToString(sum[:])
}

// memberKey is the comparable identity of a manifest member used by the monotonic-epoch
// rule: the tuple (wg_public_key, bundle_sha256) keyed by node_id. Two manifests carry
// the same membership iff they map the same node_id set to the same tuples.
type memberKey struct {
	wgPublicKey  string
	bundleSHA256 string
}

// manifestMembers decodes a stored manifest's canonical JSON into a node_id -> memberKey
// map for membership comparison.
func manifestMembers(trustListJSON []byte) (map[string]memberKey, error) {
	var tl trustlist.TrustList
	if err := json.Unmarshal(trustListJSON, &tl); err != nil {
		return nil, fmt.Errorf("controller: parsing stored manifest: %w", err)
	}
	out := make(map[string]memberKey, len(tl.Members))
	for _, m := range tl.Members {
		out[m.NodeID] = memberKey{wgPublicKey: m.WGPublicKey, bundleSHA256: m.BundleSHA256}
	}
	return out, nil
}

// sameMembership reports whether two node_id -> memberKey maps are equal (same node set,
// same tuple per node). It is the freshness test the monotonic-epoch rule uses to decide
// whether to REUSE the stored epoch (identical signed content) or BUMP it.
func sameMembership(a, b map[string]memberKey) bool {
	if len(a) != len(b) {
		return false
	}
	for id, ka := range a {
		kb, ok := b[id]
		if !ok || ka != kb {
			return false
		}
	}
	return true
}

// enforceSigningAnchor reconciles the configured bundle signing key (YAOG_BUNDLE_SIGNING_KEY,
// resolved here exactly as artifacts.Export will) against the tenant's persisted SigningAnchor, so
// a redeploy that dropped or swapped the key is caught BEFORE any bundle is produced:
//
//   - no anchor + key present → pin it (trust-on-first-use), then sign as usual
//   - no anchor + no key      → never-signed fleet, allowed (back-compat, hash-only bundles)
//   - anchor + same key       → normal signed stage
//   - anchor + NO key         → CodeSigningKeyMissing (refuse: would silently downgrade to unsigned)
//   - anchor + different key  → CodeSigningKeyMismatch, unless YAOG_BUNDLE_SIGNING_KEY_ROTATE re-pins
//
// The two refusal cases return a coded *apierr.Error so the operator gets a precise reason on stage.
func enforceSigningAnchor(ctx context.Context, store Store, t TenantID, now time.Time) error {
	signer, err := bundlesig.LoadConfigSignerFromEnv()
	if err != nil {
		// A set-but-unreadable/unparsable key already fails the export closed; surface it here too
		// so a half-configured signer never slips past the anchor reconciliation.
		return fmt.Errorf("controller: loading bundle signing key: %w", err)
	}
	var configuredPub string
	if signer != nil {
		configuredPub = string(signer.PublicKeyPEM())
	}

	anchor, err := store.GetSigningAnchor(ctx, t)
	switch {
	case errors.Is(err, ErrNotFound):
		if configuredPub == "" {
			return nil // never-signed fleet: nothing to pin, nothing to enforce
		}
		if err := store.PutSigningAnchor(ctx, t, SigningAnchor{PubKeyPEM: configuredPub}); err != nil {
			return err
		}
		// Trust-on-first-use: this re-points which key the fleet is signed under, so audit it
		// (best-effort, like the stage audits) — a trust transition must be attributable.
		appendStageAudit(ctx, store, t, now, "signing-anchor-pin", "")
		return nil
	case err != nil:
		return fmt.Errorf("controller: loading signing anchor: %w", err)
	}

	switch {
	case configuredPub == "":
		return apierr.New(apierr.CodeSigningKeyMissing) // pinned-but-absent → refuse
	case configuredPub == anchor.PubKeyPEM:
		return nil // same key as pinned — normal signed stage
	case bundlesig.RotateRequested():
		if err := store.PutSigningAnchor(ctx, t, SigningAnchor{PubKeyPEM: configuredPub}); err != nil {
			return err
		}
		// Explicit rotation (YAOG_BUNDLE_SIGNING_KEY_ROTATE) — audit the re-pin so a key change is
		// attributable in the hash-chained log, not indistinguishable from a routine stage.
		appendStageAudit(ctx, store, t, now, "signing-anchor-rotate", "")
		return nil
	default:
		return apierr.New(apierr.CodeSigningKeyMismatch) // configured key != pinned → refuse
	}
}

// stageManifest assembles the off-host-signable membership manifest from the staged
// nodes — each Member is {NodeID, WGPublicKey, BundleSHA256} — and stores it as the
// staged, UNSIGNED manifest (StoredTrustList.TrustListJSON = Canonical(manifest),
// SignatureJSON empty, Epoch set by the monotonic rule). The members are exactly the
// nodes that were rendered this stage (only they carry a bundle digest); their WG public
// keys come from the registry value stamped on the subgraph.
//
// Monotonic epoch (anti-rollback): reuse the prior stored manifest's epoch iff its
// membership (node_id -> {wg key, bundle digest}) is byte-for-byte the same; otherwise
// prior-epoch+1, or 0 when no manifest has ever been stored. Because BundleSHA256 is now
// part of the membership tuple, ANY change to a node's install.sh/config (which changes
// its bundle digest) advances the epoch, so a node's anti-rollback floor admits the fresh
// deploy and rejects a stale one.
func stageManifest(ctx context.Context, store Store, t TenantID, digests, pubKeys map[string]string) error {
	members := make([]trustlist.Member, 0, len(digests))
	for nodeID, dig := range digests {
		members = append(members, trustlist.Member{
			NodeID:       nodeID,
			WGPublicKey:  pubKeys[nodeID],
			BundleSHA256: dig,
		})
	}

	newMembers := make(map[string]memberKey, len(members))
	for _, m := range members {
		newMembers[m.NodeID] = memberKey{wgPublicKey: m.WGPublicKey, bundleSHA256: m.BundleSHA256}
	}

	// Monotonic epoch relative to the prior STAGED manifest (GetCurrentSignedTrustList) — NOT the
	// served slot. Chaining off the staging history keeps in-flight re-stages monotonic; because the
	// served epoch is always a subsequence of staged epochs (served only ever takes a value that was
	// once staged, at promote), anti-rollback on the served path is preserved. Do not "simplify" this
	// to read the served slot — that would lose monotonicity across un-promoted re-stages.
	var epoch int64
	if stored, err := store.GetCurrentSignedTrustList(ctx, t); err == nil {
		priorMembers, perr := manifestMembers(stored.TrustListJSON)
		if perr != nil {
			return perr
		}
		if sameMembership(newMembers, priorMembers) {
			epoch = stored.Epoch
		} else {
			epoch = stored.Epoch + 1
		}
	} else if !errors.Is(err, ErrNotFound) {
		return fmt.Errorf("controller: loading prior manifest for epoch: %w", err)
	}

	manifest := trustlist.TrustList{
		SchemaVersion: 1,
		Tenant:        string(t),
		Epoch:         epoch,
		Members:       members,
	}
	canonical, err := trustlist.Canonical(manifest)
	if err != nil {
		return fmt.Errorf("controller: canonicalizing staged manifest: %w", err)
	}

	// Store the staged manifest with an EMPTY signature: staging never requires a
	// signature. The operator signs it off-host (GET /trustlist → POST
	// /trustlist-signature, which sets SignatureJSON), and PromoteStaged refuses until
	// that signature exists, matches these bytes, and verifies.
	if err := store.PutSignedTrustList(ctx, t, StoredTrustList{
		TrustListJSON: canonical,
		SignatureJSON: nil,
		Epoch:         epoch,
	}); err != nil {
		return fmt.Errorf("controller: storing staged manifest: %w", err)
	}
	return nil
}

// pinFromOperatorCredential builds the trustlist.PinnedCredential the verifier checks
// against from a stored OperatorCredential, parsing the PEM by the credential's
// algorithm. It mirrors the HTTP layer's pinFromCredential so the promote-gate verifies
// with exactly the anchor a node would use.
func pinFromOperatorCredential(c OperatorCredential) (trustlist.PinnedCredential, error) {
	pin := trustlist.PinnedCredential{
		Alg:          trustlist.Alg(c.Alg),
		CredentialID: c.CredentialID,
		RPID:         c.RPID,
		Origin:       c.Origin,
	}
	switch trustlist.Alg(c.Alg) {
	case trustlist.AlgEd25519, trustlist.AlgWebAuthnEdDSA:
		pub, err := trustlist.ParseEd25519PinPEM([]byte(c.PublicKeyPEM))
		if err != nil {
			return trustlist.PinnedCredential{}, err
		}
		pin.Ed25519Pub = pub
	case trustlist.AlgWebAuthnES256:
		pub, err := trustlist.ParseES256Pin([]byte(c.PublicKeyPEM))
		if err != nil {
			return trustlist.PinnedCredential{}, err
		}
		pin.ES256Pub = pub
	default:
		return trustlist.PinnedCredential{}, fmt.Errorf("controller: unsupported operator credential algorithm %q", c.Alg)
	}
	return pin, nil
}

// KeystoneFingerprint is the stable hex SHA-256 of a pinned operator credential's PUBLIC
// key, hashed over its CANONICAL x509 PKIX DER (never the PEM string) so a benign
// re-encode — a trailing newline, different line wrapping — of the SAME key yields the
// SAME fingerprint. It is the single identity used to tell a keystone ROTATION (a genuinely
// different key) apart from an idempotent re-pin, and is surfaced to the operator panel as
// a short, comparable credential identity. It is NON-SECRET (a public-key digest). The
// alg→parse dispatch is reused from pinFromOperatorCredential so it lives in one place.
func KeystoneFingerprint(c OperatorCredential) (string, error) {
	pin, err := pinFromOperatorCredential(c)
	if err != nil {
		return "", err
	}
	var pub any
	switch {
	case pin.Ed25519Pub != nil:
		pub = pin.Ed25519Pub
	case pin.ES256Pub != nil:
		pub = pin.ES256Pub
	default:
		return "", fmt.Errorf("controller: operator credential has no public key to fingerprint")
	}
	der, err := x509.MarshalPKIXPublicKey(pub)
	if err != nil {
		return "", fmt.Errorf("controller: marshalling operator credential public key: %w", err)
	}
	sum := sha256.Sum256(der)
	return hex.EncodeToString(sum[:]), nil
}

// SameKeystoneCredential reports whether two operator credentials are the SAME trust anchor:
// the same public key (by fingerprint), the same algorithm, and — for the WebAuthn algorithms
// whose assertion binds them — the same relying-party ID, origin, and credential id. Any
// difference is a ROTATION (a new key, or a rebinding that changes what trustlist.Verify
// accepts), which the handler refuses without an explicit ack. RPID/Origin/CredentialID all
// feed trustlist.Verify (rpIdHash + the fail-closed origin check + allowCredentials), so a
// change in any of them is a real trust shift, not cosmetic. A fingerprint error (an unparsable
// PEM) is returned to the caller rather than masked as "same".
func SameKeystoneCredential(a, b OperatorCredential) (bool, error) {
	if a.Alg != b.Alg {
		return false, nil
	}
	fpA, err := KeystoneFingerprint(a)
	if err != nil {
		return false, err
	}
	fpB, err := KeystoneFingerprint(b)
	if err != nil {
		return false, err
	}
	if fpA != fpB {
		return false, nil
	}
	// The WebAuthn assertion binds rpid + origin + credential id (all consumed by
	// trustlist.Verify), so a change in ANY of them changes what verifies even with the same
	// key; raw ed25519 ignores all three (empty on both sides).
	switch trustlist.Alg(a.Alg) {
	case trustlist.AlgWebAuthnES256, trustlist.AlgWebAuthnEdDSA:
		return a.RPID == b.RPID && a.Origin == b.Origin && a.CredentialID == b.CredentialID, nil
	default:
		return true, nil
	}
}

// verifyStoredAgainstPin is the single trust-critical parse-and-verify of a stored signed
// manifest against a pinned credential, shared by the promote gate (PromoteStaged) and the
// redeploy-required signal (KeystoneRedeployRequired) so the two can never drift. It SEPARATES
// the two failure kinds the callers must treat differently:
//
//   - parseErr: a corrupt stored record or an unparsable pinned credential — a real fault.
//   - verifyErr: a well-formed signature that does NOT verify against the pin — for the redeploy
//     signal this is precisely the "rotated away from the served key" condition, not a fault.
//
// Exactly one is non-nil on failure; both are nil on success.
func verifyStoredAgainstPin(stored StoredTrustList, cred OperatorCredential) (parseErr, verifyErr error) {
	var manifest trustlist.TrustList
	if err := json.Unmarshal(stored.TrustListJSON, &manifest); err != nil {
		return fmt.Errorf("controller: parsing stored manifest: %w", err), nil
	}
	var signed trustlist.SignedTrustList
	if err := json.Unmarshal(stored.SignatureJSON, &signed); err != nil {
		return fmt.Errorf("controller: parsing stored signature: %w", err), nil
	}
	pin, err := pinFromOperatorCredential(cred)
	if err != nil {
		return err, nil
	}
	return nil, trustlist.Verify(manifest, signed, pin)
}

// KeystoneRedeployRequired reports whether the SERVED signed membership trust-list (read from the
// served slot via GetServedTrustList — exactly what /config hands the fleet) carries a signature
// that no longer verifies against the pinned credential `cred` — i.e. the keystone was rotated but
// no fresh deploy has been signed + promoted under the new key yet, so every (correctly
// re-provisioned) node would refuse the served bundle. It is the single source of the operator-
// facing "redeploy required" signal.
//
// It is deliberately CONSERVATIVE: it returns false (not "required") when nothing has been promoted
// (served slot empty — a stage→sign→promote may be mid-flight in the STAGED slot, which this
// function does not read) or when the served signature still verifies — so it fires ONLY for a
// genuine rotated-but-not-redeployed fleet, never mid-deploy. A parse/pin error (a corrupt stored
// record or an unparsable pinned credential) is surfaced to the caller, never masked as "not
// required".
func KeystoneRedeployRequired(ctx context.Context, store Store, t TenantID, cred OperatorCredential) (bool, error) {
	stored, err := store.GetServedTrustList(ctx, t)
	if errors.Is(err, ErrNotFound) {
		return false, nil // nothing promoted/served yet — not a rotation
	}
	if err != nil {
		return false, fmt.Errorf("controller: loading served trust-list for redeploy check: %w", err)
	}
	if len(stored.SignatureJSON) == 0 {
		// Defensive only: the served slot is signed-only by construction (both PromoteStaged writers
		// gate the served copy on a non-empty signature, and the keystone-ON promote gate refuses an
		// unsigned promote). Treat an impossible unsigned served manifest as not-required.
		return false, nil
	}
	parseErr, verifyErr := verifyStoredAgainstPin(stored, cred)
	if parseErr != nil {
		return false, parseErr // a real fault — never silently "not required"
	}
	// verifyErr != nil is the rotated-away-from-the-served-key signal: redeploy required.
	return verifyErr != nil, nil
}
