package controller

// kv.go — the storage PORT (plan-8). The controller has exactly ONE behavioral core
// (storecore.go): every custody/allocation rule is authored there, once, over this thin
// record-KV port. A backend (memkv / filekv) implements ONLY storage — it holds no business
// rule — so MemStore (all tests) and FileStore run the identical shipping logic, including the
// telemetry overlay. This is the collapse the understand phase proved safe (the sole prior
// divergence, the telemetry overlay, now lives in the core and both backends share it).
//
// Locking contract (READ THIS before adding a call site):
//
//   - The core wraps every custody/point method body in withLock(fn). Inside that scope the
//     record primitives (get/put/del/list, generation/setGeneration) are LOCK-FREE — they assume
//     the caller already holds the store-wide lock — so a multi-record read-modify-write
//     (PromoteStaged, GetServedConfig, IssueNodeAPIToken, AdvanceTOTPStep) executes ATOMICALLY and
//     can never tear against a concurrent op. Do NOT call these outside withLock.
//   - exists is the ONE self-synchronizing primitive: it is the heartbeat node-existence check
//     (RecordTelemetry / TouchLastSeen), which deliberately runs OUTSIDE withLock so a high-
//     frequency heartbeat never contends on the custody lock. It is metadata-only (os.Stat, no
//     deserialize) so a corrupt-but-present record still passes. Never call it inside withLock
//     (a self-locking backend would deadlock); use get for an in-lock existence probe.
//   - appendAudit / listAudit are self-synchronizing standalone primitives (audit is appended by
//     the controller layer, never composed inside another store op). generation-wake and the audit
//     log are the two genuinely backend-specific concerns the port keeps as first-class hooks.

import (
	"context"
	"errors"
)

// errKVNotFound is the port-internal "no such record" sentinel. The core maps it to the
// PUBLIC Store sentinels (ErrNotFound / ErrTokenInvalid / ErrChallengeInvalid / …) per method,
// so a backend never needs to know which public error a missing record means.
var errKVNotFound = errors.New("controller: kv record not found")

// kvRecord is one (key, raw-bytes) pair returned by list. The core owns all (un)marshaling;
// the bytes are exactly what a prior put stored (a filekv JSON file / a memkv byte copy).
type kvRecord struct {
	key string
	val []byte
}

// Collections partition a tenant's records. A backend maps each to its storage shape (memkv: an
// in-memory map; filekv: the on-disk file/dir named in the comment — the layout is FROZEN for
// persisted-data backward-compat, so these names/paths must not change). Keyed collections take
// a key (nodeID / hash / username / version); singleton collections take key == "".
const (
	collNodes         = "nodes"         // keyed by nodeID       — filekv: nodes/<id>.json
	collStaged        = "staged"        // keyed by nodeID       — filekv: bundles/<id>.staged.json
	collCurrent       = "current"       // keyed by nodeID       — filekv: bundles/<id>.current.json
	collTokens        = "tokens"        // keyed by tokenHash    — filekv: tokens/<h>.json
	collLoginChal     = "loginchal"     // keyed by challengeHash — filekv: login-challenges/<h>.json
	collAPITokens     = "apitokens"     // keyed by tokenHash    — filekv: apitokens/<h>.json
	collOperators     = "operators"     // keyed by username     — filekv: operators/<u>.json
	collSessions      = "sessions"      // keyed by tokenHash    — filekv: sessions/<h>.json
	collTopoHistory   = "topohistory"   // keyed by version      — filekv: topology-history/<v>.json
	collTopology      = "topology"      // singleton             — filekv: topology.json
	collOperatorCred  = "operatorcred"  // singleton             — filekv: operator_credential.json
	collStagedTL      = "stagedtl"      // singleton             — filekv: signed_trustlist.json
	collServedTL      = "servedtl"      // singleton             — filekv: served_trustlist.json
	collSettings      = "settings"      // singleton             — filekv: settings.json
	collSigningAnchor = "signinganchor" // singleton             — filekv: signing-anchor.json
)

// kvBackend is the storage port every backend implements. It is intentionally small: record
// CRUD, a metadata-only existence probe, the tenant generation counter + its wake hook, and the
// append-only audit log. NO business rule lives here — the core authors those over these
// primitives. Every method is tenant-scoped (the structural tenant-isolation chokepoint).
type kvBackend interface {
	// withLock runs fn while holding the store-wide lock, so the core can compose several
	// record primitives into one atomic read-modify-write. memkv: a sync.Mutex; filekv: the
	// single fs.mu the whole store already serializes on.
	withLock(fn func() error) error

	// Record CRUD — caller MUST hold withLock. get/list report a miss via errKVNotFound /
	// an empty slice; del is idempotent (a missing record is success); put is durable+atomic
	// (filekv temp+fsync+rename / memkv byte-copy set). list is stable-ordered by key.
	get(t TenantID, coll, key string) ([]byte, error)
	put(t TenantID, coll, key string, val []byte) error
	del(t TenantID, coll, key string) error
	list(t TenantID, coll string) ([]kvRecord, error)

	// exists is the metadata-only heartbeat existence probe — SELF-SYNCHRONIZING, called
	// OUTSIDE withLock (see the locking contract above). No deserialize.
	exists(t TenantID, coll, key string) (bool, error)

	// generation reads / setGeneration writes the tenant generation counter — caller MUST hold
	// withLock. setGeneration also WAKES any WaitForGeneration waiters (memkv: sync.Cond.Broadcast
	// under the held lock; filekv: no-op — the 200ms poller re-stats generation.json). It is the
	// last write in PromoteStaged (commit ordering) so the counter never runs ahead of the bundles.
	generation(t TenantID) (int64, error)
	setGeneration(t TenantID, gen int64) error

	// awaitGenerationChange blocks until the tenant generation is strictly greater than afterGen
	// (returning it) or ctx is done (returning 0, ctx.Err()). The ONE legitimately backend-specific
	// wake hook: memkv parks on the sync.Cond; filekv polls generation.json every 200ms. The core's
	// WaitForGeneration adds only the fast path. Self-managing (NOT under withLock).
	awaitGenerationChange(ctx context.Context, t TenantID, afterGen int64) (int64, error)

	// appendAudit / listAudit are the append-only, hash-chained audit log. Self-synchronizing
	// standalone primitives. The SHARED chain crypto (chainAudit) + the bound constants live in
	// audit.go; only the log STORAGE differs per backend (memkv: a slice; filekv: rotated JSONL
	// with torn-tail tolerance + legacy migration — heavily white-box tested, so it stays in filekv).
	appendAudit(t TenantID, e AuditEntry) (AuditEntry, error)
	listAudit(t TenantID) ([]AuditEntry, error)
}
