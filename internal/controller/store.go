// Package controller is the stateful control-plane layer for the YAOG controller
// panel (Phase 2+ of the controller-panel program). It is deliberately quarantined
// from the pure, stateless compiler/renderer: those packages stay frozen and
// dependency-minimal, while all server-side state lives behind the Store interface
// defined here.
//
// Zero-knowledge custody is a hard invariant of this package: the registry holds
// each node's WireGuard PUBLIC key only — a private key MUST never reach the
// controller, its Store, or any persisted bundle. See
// docs/spec/controller/key-custody.md and docs/spec/controller/persistence.md.
//
// Phase 2 ships two stdlib-only Store implementations — MemStore (in-memory, the
// CI-exercised impl and the long-poll primitive) and FileStore (JSON on disk,
// durable for a single-tenant v1 deployment). A Postgres adapter is a documented
// future Store impl (persistence.md §Postgres); the interface makes that swap
// drop-in. No third-party dependency is introduced here.
package controller

import (
	"context"
	"errors"
	"time"
)

// TenantID scopes every Store operation. It is the structural tenant-isolation
// chokepoint: EVERY Store method takes a TenantID as its mandatory first predicate,
// and a perpetual CI gate asserts none omits it. Phase 2 is single-tenant (a
// constant from YAOG_TENANT_ID); Phase 3 multi-tenant only changes how a TenantID
// is derived from the authenticated principal — never the data-access shape.
type TenantID string

// Sentinel errors returned across Store implementations so callers can branch on
// them with errors.Is regardless of the backing store.
var (
	// ErrNotFound is returned when a requested record does not exist for the tenant.
	ErrNotFound = errors.New("controller: not found")
	// ErrNoStagedBundle is returned by PromoteStaged when nothing is staged.
	ErrNoStagedBundle = errors.New("controller: no staged bundle to promote")
	// ErrTokenInvalid is returned by ConsumeEnrollmentToken when the token is
	// unknown, scoped to a different node, or expired.
	ErrTokenInvalid = errors.New("controller: enrollment token invalid or expired")
	// ErrTokenConsumed is returned by ConsumeEnrollmentToken when the token was
	// already burned (single-use).
	ErrTokenConsumed = errors.New("controller: enrollment token already consumed")
)

// NodeStatus is the lifecycle state of a registry node.
type NodeStatus string

const (
	// NodePending is a node slot created but not yet enrolled (no public key).
	NodePending NodeStatus = "pending"
	// NodeApproved is an enrolled node cleared to receive configuration.
	NodeApproved NodeStatus = "approved"
	// NodeRevoked is a node evicted from the fleet (no bundles are distributed).
	NodeRevoked NodeStatus = "revoked"
)

// Node is the controller's registry record for one fleet node. It holds the
// WireGuard PUBLIC key only — never a private key (zero-knowledge custody).
type Node struct {
	NodeID string
	// WGPublicKey is the node's WireGuard public key (base64), bound at enrollment.
	// Empty while the node slot is pending. NEVER a private key.
	WGPublicKey string
	// APITokenHash is the hex SHA-256 of the node's bearer API token, stamped by
	// IssueNodeAPIToken at enrollment. Empty while the node is pending and after a
	// RevokeNodeAPIToken. The plaintext token is NEVER stored — only this hash — so
	// a store/DB read cannot recover a usable token.
	APITokenHash string
	Status       NodeStatus
	// DesiredGeneration is the latest promoted generation that targets this node.
	DesiredGeneration int64
	// AppliedGeneration is the generation the agent last reported applying.
	AppliedGeneration int64
	// LastChecksum is the manifest checksum the agent last reported.
	LastChecksum string
	// LastHealth is the free-form health string the agent last reported alongside
	// its applied generation ("" until the first report carries one).
	LastHealth string
	LastSeen   time.Time
	EnrolledAt time.Time
	// RekeyRequested is set by the operator's fleet-wide key-rotation request
	// (POST /rekey-all) and cleared when the agent re-registers its new WireGuard
	// PUBLIC key (POST /rekey). It is a flag the agent observes via /config; it
	// carries no key material. Like every other Node field it is persisted by both
	// Store impls (it rides along on the whole-Node UpsertNode write).
	RekeyRequested bool
}

// TopologyRecord is the operator's stored topology for a tenant. The JSON is
// public-keys-only (it must not carry WireGuard private keys); Version increments
// on each PutTopology.
type TopologyRecord struct {
	Version   int64
	JSON      []byte
	UpdatedAt time.Time
}

// SignedBundle is one node's rendered, Phase-0-signed bundle at a generation.
// Files maps bundle-relative paths (install.sh, wireguard/<iface>.conf,
// checksums.sha256, bundle.sig, signing-pubkey.pem, manifest.json, …) to content.
type SignedBundle struct {
	NodeID     string
	Generation int64
	Files      map[string][]byte
	IsStaged   bool
	IsCurrent  bool
	CreatedAt  time.Time
}

// AuditEntry is one append-only, hash-chained audit record. Hash =
// hex(SHA256(canonical(entry incl. PrevHash))); PrevHash links to the prior
// entry. The chain is tamper-EVIDENT for operational visibility only — an actor
// with write access to the backing store can recompute the whole chain, so it is
// not a cryptographic anti-tamper guarantee (that is Plan 5). See audit.go.
type AuditEntry struct {
	Seq       int64
	Timestamp time.Time
	Actor     string
	Action    string
	NodeID    string
	PrevHash  string
	Hash      string
}

// EnrollmentToken authorizes one node to enroll: single-use, short-TTL, and scoped
// to a NodeID. The plaintext token is NEVER stored — only TokenHash (hex SHA-256 of
// the plaintext) — so a store/DB read cannot recover a usable token.
type EnrollmentToken struct {
	TokenHash string
	NodeID    string
	ExpiresAt time.Time
	// ConsumedAt is nil until the token is burned (single-use).
	ConsumedAt *time.Time
}

// Store is the single tenant-scoped data-access chokepoint for the controller.
//
// Contract for every implementation:
//   - TenantID is a mandatory predicate on every method; data for one tenant is
//     never visible to another (enforced by tenant_isolation_test.go).
//   - No method ever stores or returns a WireGuard private key.
//   - Reads of a missing record return ErrNotFound.
//   - Only the blocking method WaitForGeneration is required to honor ctx
//     cancellation. The non-blocking point methods complete synchronously; an
//     implementation MAY check ctx.Err() up front (FileStore does, for its I/O),
//     but callers must not rely on cancellation interrupting a point read/write.
//
// Enrollment-token methods are added by plan-4.2; the HTTP/deploy wiring that
// consumes WaitForGeneration is plan-4.3.
type Store interface {
	// --- Registry ---

	// UpsertNode creates or updates a node registry record (matched by NodeID).
	UpsertNode(ctx context.Context, t TenantID, n Node) error
	// GetNode returns the node, or ErrNotFound.
	GetNode(ctx context.Context, t TenantID, nodeID string) (Node, error)
	// ListNodes returns all nodes for the tenant (stable order by NodeID).
	ListNodes(ctx context.Context, t TenantID) ([]Node, error)
	// SetAppliedGeneration records what an agent reported applying (the applied
	// generation, the manifest checksum, and the free-form health string).
	SetAppliedGeneration(ctx context.Context, t TenantID, nodeID string, gen int64, checksum, health string) error
	// TouchLastSeen records that the agent for nodeID checked in at the given time.
	TouchLastSeen(ctx context.Context, t TenantID, nodeID string, at time.Time) error

	// --- Topology (public-keys-only) ---

	// PutTopology stores a new topology version (public-keys-only JSON) and returns
	// the stored record with its assigned Version.
	PutTopology(ctx context.Context, t TenantID, json []byte) (TopologyRecord, error)
	// GetTopology returns the current topology, or ErrNotFound.
	GetTopology(ctx context.Context, t TenantID) (TopologyRecord, error)

	// --- Bundles + generation ---

	// StageBundle stores a node's bundle as the staged (not-yet-current) version.
	// Staging replaces any prior staged bundle for that node.
	StageBundle(ctx context.Context, t TenantID, b SignedBundle) error
	// PromoteStaged atomically flips all staged bundles to current, increments the
	// tenant's generation, sets DesiredGeneration on each promoted node that has a
	// registry record (a node is registered at enrollment before any bundle is
	// staged for it; promote updates existing records, it does not create them), and
	// wakes any WaitForGeneration waiters. Returns the new generation, or
	// ErrNoStagedBundle when nothing is staged.
	PromoteStaged(ctx context.Context, t TenantID) (generation int64, err error)
	// GetCurrentBundle returns the node's current (promoted) bundle, or ErrNotFound.
	GetCurrentBundle(ctx context.Context, t TenantID, nodeID string) (SignedBundle, error)
	// CurrentGeneration returns the tenant's current generation (0 if none promoted).
	CurrentGeneration(ctx context.Context, t TenantID) (int64, error)
	// BumpGeneration atomically increments the tenant's generation and wakes any
	// WaitForGeneration waiters, WITHOUT changing any bundle: GetCurrentBundle keeps
	// returning the last promoted bundle for every node. It is a WAKE, not a deploy —
	// it lets a non-deploy signal (e.g. a fleet-wide rekey request flagged on the
	// registry) rouse parked daemon agents, which Fetch /config and observe the signal
	// rather than apply this generation's (unchanged) bundle. Returns the new
	// generation. Use PromoteStaged when a new bundle set should actually go live;
	// BumpGeneration only advances the counter so the long-poll fires.
	BumpGeneration(ctx context.Context, t TenantID) (int64, error)
	// WaitForGeneration blocks until the tenant's current generation is strictly
	// greater than afterGen, then returns it; or returns ctx.Err() if ctx is done
	// first. This is the long-poll primitive consumed by plan-4.3's /poll endpoint.
	WaitForGeneration(ctx context.Context, t TenantID, afterGen int64) (int64, error)

	// --- Enrollment tokens (added by plan-4.2) ---

	// CreateEnrollmentToken stores a single-use, node-scoped, TTL token (by its
	// hash). It is the operator-side step that authorizes one node to enroll.
	CreateEnrollmentToken(ctx context.Context, t TenantID, tok EnrollmentToken) error
	// ConsumeEnrollmentToken atomically validates and burns a token: it returns
	// ErrTokenInvalid if no token matches tokenHash for nodeID or it is expired
	// (relative to now), ErrTokenConsumed if it was already burned, otherwise it
	// marks the token consumed (ConsumedAt=now) and returns nil. Single-use is
	// enforced atomically so two concurrent enrollments cannot both succeed.
	ConsumeEnrollmentToken(ctx context.Context, t TenantID, tokenHash, nodeID string, now time.Time) error

	// --- Node API tokens (per-node bearer auth) ---

	// IssueNodeAPIToken stamps tokenHash onto the node's APITokenHash AND writes a
	// reverse index hash->nodeID so a presented token can be resolved in O(1). It
	// returns ErrNotFound if no node record exists for nodeID. The plaintext token
	// is never stored — only its hex SHA-256 hash. Rotation is self-cleaning: if the
	// node already carried a different APITokenHash, the prior reverse-index entry is
	// deleted before the new one is written so no orphaned (stale) token lingers in
	// the index.
	IssueNodeAPIToken(ctx context.Context, t TenantID, nodeID, tokenHash string) error
	// LookupNodeByAPIToken resolves a presented token's hash to its Node via the
	// reverse index. The lookup is self-consistent: it returns ErrTokenInvalid unless
	// the index resolves to a live node whose own APITokenHash still equals tokenHash
	// AND whose Status is NodeApproved. This rejects an unmapped hash, a stale/orphaned
	// index entry that no longer matches the node's current token, and any node that
	// is not approved (pending or revoked) — so a rotated, revoked, or non-approved
	// token can never authorize.
	LookupNodeByAPIToken(ctx context.Context, t TenantID, tokenHash string) (Node, error)
	// RevokeNodeAPIToken clears the node's APITokenHash and deletes the reverse index
	// entry, immediately invalidating the node's bearer token. It is idempotent: a
	// node with no issued token (or already revoked) is a no-op success.
	RevokeNodeAPIToken(ctx context.Context, t TenantID, nodeID string) error

	// --- Audit (append-only, hash-chained) ---

	// AppendAudit appends an entry, chaining its PrevHash/Hash to the tenant's prior
	// entry and assigning Seq. Returns the stored entry (with Seq/PrevHash/Hash set).
	AppendAudit(ctx context.Context, t TenantID, e AuditEntry) (AuditEntry, error)
	// ListAudit returns the tenant's audit entries in Seq order.
	ListAudit(ctx context.Context, t TenantID) ([]AuditEntry, error)
}
