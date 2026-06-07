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
	// MTLSCertFP is the SHA-256 fingerprint of the node's issued mTLS client cert
	// (set by enrollment in plan-4.2). Empty until enrolled.
	MTLSCertFP string
	Status     NodeStatus
	// DesiredGeneration is the latest promoted generation that targets this node.
	DesiredGeneration int64
	// AppliedGeneration is the generation the agent last reported applying.
	AppliedGeneration int64
	// LastChecksum is the manifest checksum the agent last reported.
	LastChecksum string
	LastSeen     time.Time
	EnrolledAt   time.Time
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

// Store is the single tenant-scoped data-access chokepoint for the controller.
//
// Contract for every implementation:
//   - TenantID is a mandatory predicate on every method; data for one tenant is
//     never visible to another (enforced by tenant_isolation_test.go).
//   - No method ever stores or returns a WireGuard private key.
//   - Reads of a missing record return ErrNotFound.
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
	// SetAppliedGeneration records what an agent reported applying.
	SetAppliedGeneration(ctx context.Context, t TenantID, nodeID string, gen int64, checksum string) error
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
	// WaitForGeneration blocks until the tenant's current generation is strictly
	// greater than afterGen, then returns it; or returns ctx.Err() if ctx is done
	// first. This is the long-poll primitive consumed by plan-4.3's /poll endpoint.
	WaitForGeneration(ctx context.Context, t TenantID, afterGen int64) (int64, error)

	// --- Audit (append-only, hash-chained) ---

	// AppendAudit appends an entry, chaining its PrevHash/Hash to the tenant's prior
	// entry and assigning Seq. Returns the stored entry (with Seq/PrevHash/Hash set).
	AppendAudit(ctx context.Context, t TenantID, e AuditEntry) (AuditEntry, error)
	// ListAudit returns the tenant's audit entries in Seq order.
	ListAudit(ctx context.Context, t TenantID) ([]AuditEntry, error)
}
