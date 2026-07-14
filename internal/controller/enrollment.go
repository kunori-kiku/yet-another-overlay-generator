package controller

// enrollment.go implements the node-enrollment ceremony for the controller panel
// (plan-4.5). The mTLS model of plan-4.2/4.3 is withdrawn: there is no CA, no CSR,
// and no in-app TLS. Enrollment now turns a single-use, node-scoped enrollment
// token plus the node's WireGuard PUBLIC key into a per-node bearer API TOKEN, and
// records the public key in the registry.
//
// Two facts shape this file:
//
//   - The proof-of-possession that used to be carried by an mTLS CSR is gone. The
//     enrollment token IS the authorization: it is single-use, short-TTL, and scoped
//     to a NodeID, minted out-of-band by an operator and burned atomically here. The
//     WireGuard public key is registered as-is, trusted only insofar as it arrives on
//     the already-authorized enroll call. WireGuard keys are Curve25519 (DH-only) and
//     cannot sign, so they were never the PoP primitive and are not one now.
//
//   - The issued credential is a bearer token, not a certificate. The controller
//     stores only its hex SHA-256 (APITokenHash); the plaintext is returned to the
//     node exactly once and never persisted. A bearer token is replayable if leaked,
//     so transport confidentiality is delegated to a reverse proxy's TLS (nginx/caddy)
//     — this is the conscious v1 trade-off recorded in docs/spec/controller/.

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/kunorikiku/yet-another-overlay-generator/internal/validator"
)

// enrollTokenBytes is the number of crypto/rand bytes behind a plaintext token
// (both enrollment tokens and per-node API tokens). 32 bytes (256 bits) of entropy
// makes the token unguessable; it is base64url-encoded (no padding) for transport
// and hashed for storage.
const enrollTokenBytes = 32

// HashToken returns the hex-encoded SHA-256 of a plaintext token. This is the ONLY
// representation of a token the controller ever stores: the Store keeps TokenHash /
// APITokenHash, never the plaintext, so a store/DB read cannot recover a usable
// token. Both the enrollment path and the per-node bearer-auth path hash the
// presented plaintext through this same function before comparing against stored
// hashes, so every lookup is hash-vs-hash.
func HashToken(plaintext string) string {
	sum := sha256.Sum256([]byte(plaintext))
	return hex.EncodeToString(sum[:])
}

// NewEnrollmentToken mints a fresh single-use token for nodeID.
//
// It returns the plaintext (to be handed to the node OUT-OF-BAND — e.g. copied
// into the agent's config) and the EnrollmentToken record (to be persisted by
// the operator via Store.CreateEnrollmentToken). The plaintext is never stored:
// only tok.TokenHash (hex SHA-256) lives in the Store. The caller is responsible
// for persisting tok and delivering plaintext; this function performs no I/O.
//
// It panics if the system CSPRNG fails. crypto/rand.Read is backed by the kernel
// getrandom(2) and does not fail in practice; a failure means the platform's
// entropy source is unavailable, in which case minting a security token is
// impossible and there is no safe value to return — failing loud is correct, and
// it keeps the signature panic-or-succeed for the callers and tests built against
// this two-value contract.
func NewEnrollmentToken(nodeID string, ttl time.Duration, now time.Time) (plaintext string, tok EnrollmentToken) {
	raw := make([]byte, enrollTokenBytes)
	if _, err := rand.Read(raw); err != nil {
		panic(fmt.Sprintf("controller: system CSPRNG failed generating enrollment token: %v", err))
	}
	plaintext = base64.RawURLEncoding.EncodeToString(raw)
	tok = EnrollmentToken{
		TokenHash:  HashToken(plaintext),
		NodeID:     nodeID,
		ExpiresAt:  now.Add(ttl),
		ConsumedAt: nil,
	}
	return plaintext, tok
}

// NewNodeAPIToken mints a fresh per-node bearer API token. It mirrors
// NewEnrollmentToken's entropy and encoding: 32 bytes of crypto/rand, base64url
// (no padding), hashed with HashToken for storage.
//
// It returns the plaintext (returned to the enrolling node exactly once, then
// discarded by the controller) and the hash (stamped on the node as APITokenHash
// and written to the reverse index by Store.IssueNodeAPIToken). The plaintext is
// NEVER stored: a store/DB read can only ever recover the hash, which is not a
// usable token. The agent presents the plaintext as "Authorization: Bearer <t>";
// the auth layer hashes it and compares hash-vs-hash.
//
// The now parameter is accepted to mirror NewEnrollmentToken's shape (and to keep
// the call sites uniform); the token itself carries no embedded timestamp — its
// validity is governed by the node's lifecycle (revocation clears the hash), not by
// an expiry baked into the token.
//
// It panics if the system CSPRNG fails, for the same reason NewEnrollmentToken does.
func NewNodeAPIToken(now time.Time) (plaintext, hash string) {
	raw := make([]byte, enrollTokenBytes)
	if _, err := rand.Read(raw); err != nil {
		panic(fmt.Sprintf("controller: system CSPRNG failed generating node API token: %v", err))
	}
	plaintext = base64.RawURLEncoding.EncodeToString(raw)
	hash = HashToken(plaintext)
	return plaintext, hash
}

// EnrollRequest is the node's enrollment payload: the plaintext enrollment token,
// the claimed NodeID, and the node's WireGuard PUBLIC key (registered as-is; never
// a private key). There is no CSR — the enrollment token is the authorization.
type EnrollRequest struct {
	Token       string
	NodeID      string
	WGPublicKey string
}

// EnrollResult is returned to a successfully enrolled node: its NodeID and the
// freshly minted per-node bearer API token (plaintext, returned exactly once). The
// controller retains only the token's hash; this plaintext is the node's sole copy.
type EnrollResult struct {
	NodeID   string
	APIToken string
}

// Enroll runs the full enrollment ceremony for one node:
//
//  1. Atomically BURN the enrollment token (ConsumeEnrollmentToken): it validates
//     the token (hash, node scope, expiry) and marks it consumed under the store
//     lock. Single-use is enforced here, so two concurrent enrollments with the
//     same token cannot both pass this step.
//  2. Mint a fresh per-node bearer API token (NewNodeAPIToken): plaintext returned
//     to the node, hash retained by the controller.
//  3. Register the node (WG PUBLIC key + APITokenHash) as NodeApproved.
//  4. Issue the API token in the Store (stamp APITokenHash + write the reverse
//     hash->nodeID index) so the node's later bearer-authed calls resolve.
//  5. Append an audit entry for the enrollment.
//
// IMPORTANT — single-use ordering: the token is burned in step 1, BEFORE anything
// else. If a later step fails, the burned token is NOT un-burned: the same token
// cannot be retried. This is deliberate. Single-use is the safety property we are
// protecting; making the burn best-effort-reversible would reopen the replay
// window. To retry after a post-burn failure, the operator issues a fresh token.
// The burn-first ordering trades a small operator inconvenience for a hard
// single-use guarantee.
func Enroll(ctx context.Context, store Store, t TenantID, req EnrollRequest, now time.Time) (EnrollResult, error) {
	// Serialize the whole ceremony per tenant (plan-6 review): the dedupe check
	// (ListNodes) and the registration (UpsertNode) are separate Store-lock
	// acquisitions, so without an enclosing lock two concurrent enrolls with the
	// same pubkey under different ids could both pass the check before either writes —
	// the exact duplicate-fleet-rows the dedupe forbids. The lock also serializes
	// enroll against stage/promote (which read the registry). Enroll is infrequent.
	defer lockTenantOps(t)()

	// (a0) Reject a malformed WireGuard public key up front, BEFORE burning the token: a pure format
	// check (no store access, no oracle) so a valid token is never wasted on a typo, and a bad key
	// never reaches the registry or a rendered peer config. Curve25519 keys are 32 bytes of standard
	// base64 — the same source of truth the schema validator and validateManualNodes use.
	if !validator.ValidWGPublicKey(req.WGPublicKey) {
		return EnrollResult{}, fmt.Errorf("%w: %q", ErrInvalidWGKey, req.WGPublicKey)
	}

	// (a) Atomically validate-and-burn the token. On any token error (invalid,
	// expired, or already consumed) we return immediately without touching the
	// registry — an unauthorized caller learns nothing and changes nothing.
	if err := store.ConsumeEnrollmentToken(ctx, t, HashToken(req.Token), req.NodeID, now); err != nil {
		return EnrollResult{}, err
	}

	// (a1) Lifecycle guard: load the claimed node-id's current record (if any). A
	// REVOKED node-id must NOT be silently resurrected by a still-valid token — refuse
	// (the operator deletes the node to reuse the id; revoke also purges the node's
	// tokens, so this is belt-and-braces against the resurrection vector). A re-enroll
	// over an APPROVED node (a legitimate reinstall) is allowed, but recorded with a
	// DISTINCT audit action below so the identity overwrite is never silent (S4).
	reenrollApproved := false
	if existing, gerr := store.GetNode(ctx, t, req.NodeID); gerr == nil {
		if existing.Status == NodeRevoked {
			if _, auditErr := store.AppendAudit(ctx, t, AuditEntry{
				Timestamp: now,
				Actor:     "agent:" + req.NodeID,
				Action:    "enroll-rejected-revoked",
				NodeID:    req.NodeID,
			}); auditErr != nil {
				return EnrollResult{}, fmt.Errorf("controller: appending revoked-enroll audit: %w", auditErr)
			}
			return EnrollResult{}, ErrNodeRevoked
		}
		if existing.Status == NodeApproved {
			reenrollApproved = true
		}
	} else if !errors.Is(gerr, ErrNotFound) {
		return EnrollResult{}, fmt.Errorf("controller: loading node for enroll guard: %w", gerr)
	}

	// (a2) Dedupe (plan-6): one approved WG public key ↔ one node-id. Refuse if the
	// presented pubkey is already approved under a DIFFERENT node-id — the
	// duplicate-fleet-rows vector. Same-id re-enroll (a reinstalled host with a fresh
	// token) is unaffected. Checked AFTER the burn so only an authorized caller
	// reaches it (auditing here cannot be spammed by an unauthenticated probe); the
	// trade-off is that a duplicate burns the token (consistent with the burn-first
	// principle — the operator mints a fresh one after revoking the conflict). Under
	// the tenant lock the check-then-register is now atomic. The refusal is audited.
	if conflict, err := CheckWGKeyUnique(ctx, store, t, req.WGPublicKey, req.NodeID); err != nil {
		if _, auditErr := store.AppendAudit(ctx, t, AuditEntry{
			Timestamp: now,
			Actor:     "agent:" + req.NodeID,
			Action:    "enroll-rejected-duplicate-key",
			NodeID:    req.NodeID,
		}); auditErr != nil {
			return EnrollResult{}, fmt.Errorf("controller: appending duplicate-key audit: %w", auditErr)
		}
		return EnrollResult{}, fmt.Errorf("%w: this WireGuard public key is already enrolled as node %q; revoke it first or reuse that node id", err, conflict)
	}

	// (b) Mint the per-node bearer token. Plaintext is returned to the node once;
	// only the hash is stored.
	plaintext, hash := NewNodeAPIToken(now)

	// (c) Register the node with its WireGuard PUBLIC key (as-is) and the API token
	// hash, marked approved and stamped with the enrollment time. UpsertNode must
	// run before IssueNodeAPIToken so the latter finds a node to stamp.
	node := Node{
		NodeID:       req.NodeID,
		WGPublicKey:  req.WGPublicKey,
		APITokenHash: hash,
		Status:       NodeApproved,
		EnrolledAt:   now,
	}
	if err := store.UpsertNode(ctx, t, node); err != nil {
		return EnrollResult{}, fmt.Errorf("controller: registering enrolled node: %w", err)
	}

	// (d) Issue the API token in the Store: stamp APITokenHash on the node and write
	// the reverse hash->nodeID index that authenticateNode resolves on every authed
	// agent call.
	if err := store.IssueNodeAPIToken(ctx, t, req.NodeID, hash); err != nil {
		return EnrollResult{}, fmt.Errorf("controller: issuing node API token: %w", err)
	}

	// (e) Audit the enrollment. The actor is the agent itself (the enroll call is
	// authorized by the burned token, not by an operator session). A re-enroll over an
	// existing approved node records a DISTINCT action so the identity (key + bearer)
	// overwrite is auditable, never silent (S4).
	enrollAction := "enroll"
	if reenrollApproved {
		enrollAction = "enroll-reenroll-approved"
	}
	if _, err := store.AppendAudit(ctx, t, AuditEntry{
		Timestamp: now,
		Actor:     "agent:" + req.NodeID,
		Action:    enrollAction,
		NodeID:    req.NodeID,
	}); err != nil {
		return EnrollResult{}, fmt.Errorf("controller: appending enroll audit: %w", err)
	}

	return EnrollResult{
		NodeID:   req.NodeID,
		APIToken: plaintext,
	}, nil
}

// CheckWGKeyUnique reports a conflict when an APPROVED node OTHER than selfNodeID
// already holds wgPubKey. It returns the conflicting node-id and ErrDuplicateWGKey,
// or ("", nil) when the key is free (or empty — an empty key is never deduped). The
// comparison is whitespace-insensitive so a padded key cannot evade the gate (a
// padded key would also break the rendered WG config, so this is belt-and-braces).
//
// This is the single definition of the identity invariant — one approved WG pubkey ↔
// one node-id — shared by BOTH write paths (Enroll and Rekey). Callers MUST hold the
// per-tenant op lock across this check AND the subsequent write; the check-then-act is
// only atomic under that lock (Enroll and Rekey both take lockTenantOps).
func CheckWGKeyUnique(ctx context.Context, store Store, t TenantID, wgPubKey, selfNodeID string) (string, error) {
	key := strings.TrimSpace(wgPubKey)
	if key == "" {
		return "", nil
	}
	nodes, err := store.ListNodes(ctx, t)
	if err != nil {
		return "", fmt.Errorf("controller: checking for duplicate WG key: %w", err)
	}
	for _, n := range nodes {
		if n.Status == NodeApproved && n.NodeID != selfNodeID && strings.TrimSpace(n.WGPublicKey) == key {
			return n.NodeID, ErrDuplicateWGKey
		}
	}
	// Cross-source: the one-pubkey-one-node invariant also spans MANUAL nodes, whose operator-asserted
	// public key lives in the stored TOPOLOGY (not the registry). Reject enrolling/rekeying to a key a
	// manual node already claims (the enrolled→manual direction; validateManualNodes covers the reverse,
	// at stage/preview time). manualKeyConflict returns ("", nil) when free, so it is a safe tail.
	return manualKeyConflict(ctx, store, t, key, selfNodeID)
}

// Rekey re-registers a node's rotated WireGuard PUBLIC key (the zero-knowledge
// rekey response), swapping WGPublicKey and clearing RekeyRequested while preserving
// every other field. It enforces the SAME identity invariant as Enroll
// (CheckWGKeyUnique) — the parallel write path must not be able to create the
// duplicate the enroll dedupe forbids — and holds the per-tenant op lock so the
// check-then-write is atomic. Returns ErrNotFound (unknown node), ErrDuplicateWGKey
// (the new key already belongs to another approved node), or a wrapped store error.
func Rekey(ctx context.Context, store Store, t TenantID, nodeID, newPubKey string, now time.Time) error {
	defer lockTenantOps(t)()

	// Reject a malformed new key before any store work (same gate as Enroll): a rekey must not be
	// able to bind an invalid key the enroll path forbids.
	if !validator.ValidWGPublicKey(newPubKey) {
		return fmt.Errorf("%w: %q", ErrInvalidWGKey, newPubKey)
	}

	// Durable read for the read-modify-write: this record is written back below (WGPublicKey +
	// RekeyRequested), so use GetNodeRecord — GetNode would merge the volatile telemetry overlay and
	// bake a possibly-dead node's stale live conditions/metrics into the durable record on UpsertNode.
	rec, err := store.GetNodeRecord(ctx, t, nodeID)
	if err != nil {
		return err // ErrNotFound mapped by the caller
	}
	if conflict, err := CheckWGKeyUnique(ctx, store, t, newPubKey, nodeID); err != nil {
		// Audit the refusal so a duplicate-key binding ATTEMPT via the rekey path is
		// as visible in the trail as one via enroll (the Enroll path emits
		// "enroll-rejected-duplicate-key"); without this, a /rekey collision would be
		// refused but leave no trace. The bearer already proved itself, so this cannot
		// be spammed by an unauthenticated probe.
		if _, auditErr := store.AppendAudit(ctx, t, AuditEntry{
			Timestamp: now,
			Actor:     "agent:" + nodeID,
			Action:    "rekey-rejected-duplicate-key",
			NodeID:    nodeID,
		}); auditErr != nil {
			return fmt.Errorf("controller: appending duplicate-key audit: %w", auditErr)
		}
		return fmt.Errorf("%w: this WireGuard public key is already enrolled as node %q", err, conflict)
	}
	rec.WGPublicKey = newPubKey
	rec.RekeyRequested = false
	if err := store.UpsertNode(ctx, t, rec); err != nil {
		return fmt.Errorf("controller: recording rekey: %w", err)
	}
	if _, err := store.AppendAudit(ctx, t, AuditEntry{
		Timestamp: now,
		Actor:     "agent:" + nodeID,
		Action:    "rekey",
		NodeID:    nodeID,
	}); err != nil {
		return fmt.Errorf("controller: appending rekey audit: %w", err)
	}
	return nil
}
