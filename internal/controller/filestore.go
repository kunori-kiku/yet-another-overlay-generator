package controller

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/kunorikiku/yet-another-overlay-generator/internal/model"
)

// FileStore is a JSON-on-disk Store implementation, durable for the single-tenant
// v1 deployment. It mirrors MemStore's semantics exactly (the shared compat test
// runs against both) while persisting every mutation to disk via temp-file +
// rename so a crash can never leave a half-written record. All in-process access
// is serialized by a sync.Mutex; cross-process durability is provided by the
// atomic renames, but FileStore does not arbitrate between separate processes
// sharing a root (a single controller process owns its root).
//
// SPOF / scaling note (plan-6, deferred to rc.2/GA — NOT fixed here): this design has two
// known single-point limits, acceptable for the single-tenant v1 controller but called out
// so they are not mistaken for oversights. (1) ONE global mutex (fs.mu) serializes EVERY
// store operation — including disk writes — so throughput is single-writer and a slow disk
// stalls all callers; a future revision can shard the lock per tenant/record or move to an
// embedded transactional KV. (2) WaitForGeneration POLLS generation.json every 200ms (no
// in-process condition variable on the disk store), so promotion is observed with up to
// ~200ms latency and N waiting agents each re-stat the file; a future revision can wake
// waiters via a notifier. Neither limit is a correctness bug; the bound work in plan-6 is
// the audit log, not these.
//
// On-disk layout under <root>/<tenant>/:
//
//	nodes/<nodeID>.json                 one Node record
//	topology.json                       the current TopologyRecord (with Version)
//	topology-history/<version>.json     one retained TopologyRecord per version
//	                                    (last TopologyHistoryLimit kept, oldest pruned)
//	bundles/<nodeID>.staged.json        the node's staged SignedBundle (if any)
//	bundles/<nodeID>.current.json       the node's current SignedBundle (if any)
//	tokens/<tokenHash>.json             one EnrollmentToken record (keyed by hash)
//	apitokens/<hash>.json               node API token reverse index ({NodeID}), keyed by hash
//	generation.json                     the tenant's current generation counter
//	audit.jsonl                         the append-only audit log, one AuditEntry per
//	                                    line (JSON), in Seq order, bounded + rotated
//	                                    (plan-6); a legacy audit.json array is migrated
//	                                    to this on first access
//	operator_credential.json            the pinned off-host operator credential (keystone)
//	signed_trustlist.json               the STAGED (to-be-signed/just-signed) membership manifest
//	served_trustlist.json               the SERVED (last-promoted) signed membership manifest
//	operators/<username>.json           one operator account (argon2id PHC hash)
//	sessions/<tokenHash>.json           one operator login session, keyed by token hash
//	settings.json                       operator-editable controller settings (bootstrap)
//	signing-anchor.json                 the pinned bundle-signing PUBLIC key (TOFU; non-secret)
//
// Directories are created 0700 and files written 0600. SignedBundle.Files
// (map[string][]byte) serializes as base64 under encoding/json, which round-trips
// the raw bytes faithfully.
type FileStore struct {
	root string
	mu   sync.Mutex
	// auditTails caches, per tenant, the tail of the append-only audit log (last Seq +
	// Hash, and the current entry count) so AppendAudit can chain and decide rotation
	// WITHOUT re-reading the whole file on every append (plan-6). It is lazily populated
	// (the first append for a tenant reads the file once) and kept coherent under mu, of
	// which AppendAudit/rotateAudit are the only writers. Guarded by mu.
	auditTails map[TenantID]*auditTail
	// telemetryMu guards the volatile telemetry overlay. It is a SEPARATE lock from mu so a
	// heartbeat (RecordTelemetry / TouchLastSeen) never contends on the store-wide mu nor forces a
	// durable fsync'd whole-record rewrite: telemetry is high-frequency observability that self-heals
	// within one interval after a restart, so it must not ride the custody write path (which is the
	// DoS-amplification the overlay removes). Lock order: mu is ALWAYS taken before telemetryMu — the
	// readers GetNode/ListNodes (via applyTelemetryOverlay) and the /report writer SetAppliedGeneration
	// (via refreshTelemetryOverlayFromReport) hold mu, THEN take telemetryMu; the heartbeat paths
	// (RecordTelemetry/TouchLastSeen) take ONLY telemetryMu — never mu while holding telemetryMu — so
	// the two locks cannot deadlock.
	telemetryMu sync.Mutex
	// telemetry is the in-memory overlay of a node's four OBSERVABILITY fields (LastSeen, Conditions,
	// Telemetry, LastAgentVersion), per tenant, per node. It is merged OVER the durable record on read;
	// it never holds custody fields (AppliedGeneration/LastChecksum/LastHealth/DesiredGeneration/keys),
	// which stay on the durable temp+fsync+rename path. Guarded by telemetryMu; lazily populated.
	telemetry map[TenantID]map[string]*volatileTelemetry
	// history is the bounded per-(tenant,node) resource-sample history backing the node-detail charts
	// (plan-2). RecordTelemetry appends IN-MEMORY (no disk on the heartbeat path); a background flusher
	// (Start/Close) drains to append-only JSONL under <root>/telemetry-history. Its own mutex.
	history *telemetryHistory
}

// volatileTelemetry is the in-memory-only overlay of a node's observability fields, written by the
// heartbeat paths WITHOUT a durable rewrite and merged over the durable record on read. The *Set flags
// distinguish "never overlaid" from a written zero value; writtenAt is the server observedAt of the
// last conditions/metrics/version write and gates a monotonic last-writer-wins so a /report's fresh
// conditions are never permanently shadowed by an older heartbeat (and vice-versa).
type volatileTelemetry struct {
	writtenAt    time.Time
	conditions   []NodeCondition
	telemetry    map[string]json.RawMessage
	agentVersion string
	telemetrySet bool
	lastSeen     time.Time
	lastSeenSet  bool
}

// auditTail is the in-memory tail of a tenant's audit log: the last entry's Seq and Hash
// (to chain the next append) and the live entry count (to trigger amortized rotation).
type auditTail struct {
	seq   int64
	hash  string
	count int
}

// Compile-time assertion that *FileStore satisfies the Store interface.
var _ Store = (*FileStore)(nil)

// NewFileStore returns a FileStore rooted at the given base directory, creating
// it (0700) if it does not exist.
func NewFileStore(root string) (*FileStore, error) {
	if strings.TrimSpace(root) == "" {
		return nil, errors.New("controller: filestore root must not be empty")
	}
	if err := os.MkdirAll(root, 0700); err != nil {
		return nil, fmt.Errorf("controller: create filestore root: %w", err)
	}
	fs := &FileStore{root: root, auditTails: make(map[TenantID]*auditTail)}
	fs.history = newTelemetryHistory(filepath.Join(root, "telemetry-history"), DefaultTelemetryHistoryCap)
	return fs, nil
}

// Start launches the resource-history background flusher (plan-2). The server calls it once after
// construction; tests that exercise durable flush call it explicitly (and Close). Idempotent-safe to
// omit (the buffer still fills; nothing is flushed until Start).
func (fs *FileStore) Start() {
	fs.history.start()
}

// Close stops the history flusher and does a final drain. The server calls it on graceful shutdown.
func (fs *FileStore) Close() {
	fs.history.close()
}

// --- path helpers -----------------------------------------------------------

// sanitizeComponent rejects values that are unsafe to use as a single path
// component: empty, ".", "..", or anything containing a path separator (or a
// platform-specific separator / NUL). This prevents a malicious or malformed
// TenantID/NodeID from escaping the store root via path traversal.
func sanitizeComponent(kind, v string) (string, error) {
	if v == "" {
		return "", fmt.Errorf("controller: %s must not be empty", kind)
	}
	if v == "." || v == ".." {
		return "", fmt.Errorf("controller: %s %q is not a valid path component", kind, v)
	}
	if strings.ContainsRune(v, '/') || strings.ContainsRune(v, os.PathSeparator) ||
		strings.ContainsRune(v, '\x00') {
		return "", fmt.Errorf("controller: %s %q must not contain a path separator", kind, v)
	}
	return v, nil
}

// tenantDir returns (and does not create) the directory for a tenant, after
// validating the TenantID is a safe path component.
func (fs *FileStore) tenantDir(t TenantID) (string, error) {
	tc, err := sanitizeComponent("tenant id", string(t))
	if err != nil {
		return "", err
	}
	return filepath.Join(fs.root, tc), nil
}

// ensureTenantDir creates the tenant directory and its sub-directories (0700).
func (fs *FileStore) ensureTenantDir(t TenantID) (string, error) {
	dir, err := fs.tenantDir(t)
	if err != nil {
		return "", err
	}
	for _, sub := range []string{"", "nodes", "bundles", "tokens", "login-challenges", "apitokens", "operators", "sessions", "topology-history"} {
		if err := os.MkdirAll(filepath.Join(dir, sub), 0700); err != nil {
			return "", fmt.Errorf("controller: create tenant dir: %w", err)
		}
	}
	return dir, nil
}

// nodePath returns the on-disk path for a node record after validating nodeID.
func (fs *FileStore) nodePath(dir, nodeID string) (string, error) {
	nc, err := sanitizeComponent("node id", nodeID)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "nodes", nc+".json"), nil
}

// bundlePath returns the on-disk path for a node's staged/current bundle.
func (fs *FileStore) bundlePath(dir, nodeID, kind string) (string, error) {
	nc, err := sanitizeComponent("node id", nodeID)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "bundles", nc+"."+kind+".json"), nil
}

// tokenPath returns the on-disk path for an enrollment token after validating the
// tokenHash is a safe single path component (it is a hex SHA-256 in practice, but
// it is sanitized like any other untrusted key to prevent path traversal).
func (fs *FileStore) tokenPath(dir, tokenHash string) (string, error) {
	tc, err := sanitizeComponent("token hash", tokenHash)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "tokens", tc+".json"), nil
}

// loginChallengePath returns the on-disk path for a passkey login challenge after
// validating the challengeHash is a safe single path component (a hex SHA-256 in
// practice, sanitized like any untrusted key to prevent path traversal).
func (fs *FileStore) loginChallengePath(dir, challengeHash string) (string, error) {
	cc, err := sanitizeComponent("login challenge hash", challengeHash)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "login-challenges", cc+".json"), nil
}

// apiTokenPath returns the on-disk path for a node API token's reverse-index entry
// after validating the hash is a safe single path component (a hex SHA-256 in
// practice, sanitized like any untrusted key to prevent path traversal).
func (fs *FileStore) apiTokenPath(dir, tokenHash string) (string, error) {
	tc, err := sanitizeComponent("api token hash", tokenHash)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "apitokens", tc+".json"), nil
}

// operatorPath returns the on-disk path for an operator account after validating the
// username is a safe single path component.
func (fs *FileStore) operatorPath(dir, username string) (string, error) {
	uc, err := sanitizeComponent("operator username", username)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "operators", uc+".json"), nil
}

// sessionPath returns the on-disk path for an operator session after validating the
// token hash is a safe single path component (a hex SHA-256 in practice, sanitized
// like any untrusted key to prevent path traversal).
func (fs *FileStore) sessionPath(dir, tokenHash string) (string, error) {
	tc, err := sanitizeComponent("session token hash", tokenHash)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "sessions", tc+".json"), nil
}

// --- atomic JSON IO ---------------------------------------------------------

// writeJSONAtomic marshals v and writes it to path via a temp-file + rename so a
// crash cannot leave a truncated file. The parent directory must already exist.
//
// Crash-consistency (B2): the temp file's bytes are fsync'd to stable storage BEFORE the
// rename, and the parent directory is fsync'd AFTER the rename, so a power loss between the
// two steps cannot leave the target naming a not-yet-durable inode. Without these syncs the
// rename can be ordered ahead of the data write on some filesystems, exposing a zero-length
// or stale file after a crash — for the identity/credential/trust-list store that backs the
// keystone, a silently-corrupt record is worse than an error.
func writeJSONAtomic(path string, v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return fmt.Errorf("controller: marshal %s: %w", filepath.Base(path), err)
	}
	return writeBytesDurable(path, data)
}

// writeBytesDurable writes data to path via a temp-file + rename so a crash cannot leave a
// truncated file, with the same crash-consistency guarantee writeJSONAtomic documents: the
// temp file's bytes are fsync'd to stable storage BEFORE the rename, and the parent directory
// is fsync'd AFTER the rename, so a power loss between the two steps cannot leave the target
// naming a not-yet-durable inode. The parent directory must already exist. This is the single
// durable-write primitive shared by every rename-atomic writer in the store (writeJSONAtomic
// for the per-record JSON files, writeAuditJSONL for the audit-log rotation/migration rewrite)
// so the fsync dance lives in exactly one place (B2).
func writeBytesDurable(path string, data []byte) error {
	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0600)
	if err != nil {
		return fmt.Errorf("controller: write %s: %w", filepath.Base(path), err)
	}
	if _, werr := f.Write(data); werr != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return fmt.Errorf("controller: write %s: %w", filepath.Base(path), werr)
	}
	if serr := f.Sync(); serr != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return fmt.Errorf("controller: sync %s: %w", filepath.Base(path), serr)
	}
	if cerr := f.Close(); cerr != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("controller: close %s: %w", filepath.Base(path), cerr)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("controller: install %s: %w", filepath.Base(path), err)
	}
	// fsync the parent directory so the rename (a directory metadata change) is itself
	// durable; otherwise a crash could lose the rename and leave the OLD file in place.
	// Best-effort: a dir that cannot be opened/synced (e.g. some network FS) must not fail
	// an otherwise-committed write — the rename already landed.
	if dir, derr := os.Open(filepath.Dir(path)); derr == nil {
		_ = dir.Sync()
		_ = dir.Close()
	}
	return nil
}

// readJSON reads and unmarshals path into v. A missing file is reported via
// os.IsNotExist on the returned error so callers can map it to ErrNotFound.
func readJSON(path string, v any) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(data, v); err != nil {
		return fmt.Errorf("controller: parse %s: %w", filepath.Base(path), err)
	}
	return nil
}

// --- generation -------------------------------------------------------------

// generationFile is the on-disk shape of generation.json.
type generationFile struct {
	Generation int64 `json:"generation"`
}

// apiTokenIndex is the on-disk shape of apitokens/<hash>.json: the reverse index
// from a node API token's hash to the owning NodeID.
type apiTokenIndex struct {
	NodeID string `json:"node_id"`
}

// readGeneration returns the tenant's current generation, defaulting to 0 when
// generation.json is absent (nothing promoted yet).
func (fs *FileStore) readGeneration(dir string) (int64, error) {
	var g generationFile
	err := readJSON(filepath.Join(dir, "generation.json"), &g)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, err
	}
	return g.Generation, nil
}

// --- audit ------------------------------------------------------------------

// auditFileName / legacyAuditFileName are the current (append-only JSONL) and the legacy
// (single JSON array) on-disk audit logs. A legacy file is migrated to JSONL on first
// access (loadAuditTail) so there is never a split-brain across the two formats.
const (
	auditFileName       = "audit.jsonl"
	legacyAuditFileName = "audit.json"
)

// readAudit returns the tenant's audit entries (empty slice when no log exists), in their
// stored Seq order. It reads the append-only JSONL log; if only a legacy audit.json array
// is present (pre-plan-6 data, not yet migrated by an append) it falls back to that, so
// ListAudit returns the full history either way.
func (fs *FileStore) readAudit(dir string) ([]AuditEntry, error) {
	entries, _, err := readAuditJSONL(filepath.Join(dir, auditFileName))
	if err != nil {
		return nil, err
	}
	if entries != nil {
		return entries, nil
	}
	// No JSONL yet — fall back to a (possibly present) legacy array.
	var legacy []AuditEntry
	if err := readJSON(filepath.Join(dir, legacyAuditFileName), &legacy); err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	return legacy, nil
}

// readAuditJSONL parses an append-only JSONL audit log (one AuditEntry per line). It
// returns (nil, false, nil) when the file does not exist so the caller can distinguish "no
// JSONL log" from "empty log". Blank lines are skipped (tolerant of a trailing newline).
//
// Crash tolerance: the append path is a bare O_APPEND write (not rename-atomic), so a crash
// or power loss can leave a partially-written FINAL line. That torn trailing line is DROPPED
// and reported via tornTail=true — preserving the durably-committed prefix so the log stays
// readable AND appendable (loadAuditTail self-heals by rewriting the clean prefix before the
// next append, so the torn bytes never become an interior line). A malformed INTERIOR line
// is real corruption, not a torn append, and is still surfaced as a hard error. This restores
// the store-wide "lose at most the last record, never corrupt/brick" guarantee that the
// rename-atomic writers provide.
func readAuditJSONL(path string) (entries []AuditEntry, tornTail bool, err error) {
	data, rerr := os.ReadFile(path)
	if rerr != nil {
		if os.IsNotExist(rerr) {
			return nil, false, nil
		}
		return nil, false, rerr
	}
	lines := strings.Split(string(data), "\n")
	lastNonBlank := -1
	for i, line := range lines {
		if strings.TrimSpace(line) != "" {
			lastNonBlank = i
		}
	}
	entries = []AuditEntry{}
	for i, line := range lines {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var e AuditEntry
		if jerr := json.Unmarshal([]byte(line), &e); jerr != nil {
			if i == lastNonBlank {
				// A malformed FINAL line: drop it, keep the durable prefix. This is the torn
				// residue of a crashed append (the common case — a torn O_APPEND never has a
				// trailing newline), and ALSO subsumes on-disk corruption of just the last
				// record, which is indistinguishable and was previously a brick. So a clean
				// read is not proof the last record was uncorrupted — only INTERIOR corruption
				// (below) is surfaced.
				return entries, true, nil
			}
			return nil, false, fmt.Errorf("controller: parse %s: %w", auditFileName, jerr)
		}
		entries = append(entries, e)
	}
	return entries, false, nil
}

// writeAuditJSONL atomically AND durably rewrites the whole JSONL log via the shared
// writeBytesDurable primitive (temp file + fsync + rename + parent-dir fsync). Used only by
// the legacy migration and by rotation — NOT by the steady-state append path. It is durable
// for the same reason the per-record writers are (B2): the rotation/migration rewrite must
// not be lost or left torn by a crash any more than a credential write — otherwise a power
// loss right after a rotation could resurrect the just-trimmed prefix or expose a zero-length
// log. The steady-state append (AppendAudit) handles its own O_APPEND f.Sync separately.
func writeAuditJSONL(path string, entries []AuditEntry) error {
	var buf bytes.Buffer
	for _, e := range entries {
		b, err := json.Marshal(e)
		if err != nil {
			return fmt.Errorf("controller: marshal %s: %w", auditFileName, err)
		}
		buf.Write(b)
		buf.WriteByte('\n')
	}
	if err := writeBytesDurable(path, buf.Bytes()); err != nil {
		return fmt.Errorf("controller: rewrite %s: %w", auditFileName, err)
	}
	return nil
}

// loadAuditTail returns the cached tail of a tenant's audit log, populating it on first
// use. On first use it migrates a legacy audit.json array to audit.jsonl (once), then
// reads the JSONL log to seed the last Seq/Hash + entry count. The caller must hold fs.mu.
func (fs *FileStore) loadAuditTail(t TenantID, dir string) (*auditTail, error) {
	if tail := fs.auditTails[t]; tail != nil {
		return tail, nil
	}
	jsonlPath := filepath.Join(dir, auditFileName)
	legacyPath := filepath.Join(dir, legacyAuditFileName)
	// Migrate a legacy array to JSONL once, BEFORE seeding the tail, so appends and
	// ListAudit never split across the two formats.
	if _, err := os.Stat(jsonlPath); os.IsNotExist(err) {
		var legacy []AuditEntry
		if err := readJSON(legacyPath, &legacy); err == nil {
			if werr := writeAuditJSONL(jsonlPath, legacy); werr != nil {
				return nil, werr
			}
			_ = os.Remove(legacyPath)
		} else if !os.IsNotExist(err) {
			return nil, err
		}
	}
	entries, tornTail, err := readAuditJSONL(jsonlPath)
	if err != nil {
		return nil, err
	}
	if tornTail {
		// A crash left a partial trailing line. Rewrite the clean prefix so the next O_APPEND
		// lands on a well-formed file rather than concatenating onto the torn bytes (which
		// would turn the torn line into an unreadable interior line). One-time, under fs.mu.
		if werr := writeAuditJSONL(jsonlPath, entries); werr != nil {
			return nil, werr
		}
	}
	tail := &auditTail{count: len(entries)}
	if n := len(entries); n > 0 {
		tail.seq = entries[n-1].Seq
		tail.hash = entries[n-1].Hash
	}
	fs.auditTails[t] = tail
	return tail, nil
}

// rotateAudit trims the JSONL log down to the most-recent auditRetain entries and updates
// the cached count. It rewrites the whole file, but only runs once per
// (auditRotateAt-auditRetain) appends (amortized), so steady-state appends stay O(1). The
// caller must hold fs.mu and pass the tenant's cached tail.
func (fs *FileStore) rotateAudit(dir string, tail *auditTail) error {
	// A torn tail was already self-healed by loadAuditTail before any append, so the read
	// here sees a clean file; the tornTail flag is irrelevant at rotation time.
	entries, _, err := readAuditJSONL(filepath.Join(dir, auditFileName))
	if err != nil {
		return err
	}
	if len(entries) <= auditRetain {
		tail.count = len(entries)
		return nil
	}
	kept := entries[len(entries)-auditRetain:]
	if err := writeAuditJSONL(filepath.Join(dir, auditFileName), kept); err != nil {
		return err
	}
	tail.count = len(kept)
	return nil
}

// =============================== Registry ==================================

// UpsertNode creates or updates a node registry record, matched by NodeID.
func (fs *FileStore) UpsertNode(ctx context.Context, t TenantID, n Node) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	fs.mu.Lock()
	defer fs.mu.Unlock()

	dir, err := fs.ensureTenantDir(t)
	if err != nil {
		return err
	}
	p, err := fs.nodePath(dir, n.NodeID)
	if err != nil {
		return err
	}
	return writeJSONAtomic(p, n)
}

// GetNode returns the node, or ErrNotFound.
func (fs *FileStore) GetNode(ctx context.Context, t TenantID, nodeID string) (Node, error) {
	if err := ctx.Err(); err != nil {
		return Node{}, err
	}
	fs.mu.Lock()
	defer fs.mu.Unlock()

	dir, err := fs.tenantDir(t)
	if err != nil {
		return Node{}, err
	}
	p, err := fs.nodePath(dir, nodeID)
	if err != nil {
		return Node{}, err
	}
	var n Node
	if err := readJSON(p, &n); err != nil {
		if os.IsNotExist(err) {
			return Node{}, ErrNotFound
		}
		return Node{}, err
	}
	fs.applyTelemetryOverlay(t, &n)
	return n, nil
}

// ListNodes returns all nodes for the tenant, stably ordered by NodeID.
func (fs *FileStore) ListNodes(ctx context.Context, t TenantID) ([]Node, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	fs.mu.Lock()
	defer fs.mu.Unlock()

	dir, err := fs.tenantDir(t)
	if err != nil {
		return nil, err
	}
	out, err := fs.listNodesLocked(dir)
	if err != nil {
		return nil, err
	}
	// Merge the live telemetry overlay over each durable record (observability fields only; custody
	// fields untouched) so the operator /nodes view reflects the latest heartbeat without a per-beat
	// durable write.
	for i := range out {
		fs.applyTelemetryOverlay(t, &out[i])
	}
	return out, nil
}

// listNodesLocked reads every node record under <dir>/nodes, sorted by NodeID.
// The caller must hold fs.mu.
func (fs *FileStore) listNodesLocked(dir string) ([]Node, error) {
	nodesDir := filepath.Join(dir, "nodes")
	ents, err := os.ReadDir(nodesDir)
	if err != nil {
		if os.IsNotExist(err) {
			return []Node{}, nil
		}
		return nil, fmt.Errorf("controller: list nodes: %w", err)
	}
	out := make([]Node, 0, len(ents))
	for _, e := range ents {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		var n Node
		if err := readJSON(filepath.Join(nodesDir, e.Name()), &n); err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].NodeID < out[j].NodeID })
	return out, nil
}

// SetAppliedGeneration records what an agent reported applying (generation,
// checksum, health, the reported agent build version, and the structured conditions
// set). An empty agentVersion (a legacy agent) leaves the stored version untouched;
// conditions are server-stamped with observedAt and a nil/empty slice clears the set.
func (fs *FileStore) SetAppliedGeneration(ctx context.Context, t TenantID, nodeID string, gen int64, checksum, health, agentVersion string, conditions []model.Condition, observedAt time.Time) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	fs.mu.Lock()
	defer fs.mu.Unlock()

	dir, err := fs.tenantDir(t)
	if err != nil {
		return err
	}
	p, err := fs.nodePath(dir, nodeID)
	if err != nil {
		return err
	}
	var n Node
	if err := readJSON(p, &n); err != nil {
		if os.IsNotExist(err) {
			return ErrNotFound
		}
		return err
	}
	n.AppliedGeneration = gen
	n.LastChecksum = checksum
	n.LastHealth = health
	if agentVersion != "" {
		n.LastAgentVersion = agentVersion
	}
	n.Conditions = stampConditions(conditions, observedAt)
	if err := writeJSONAtomic(p, n); err != nil {
		return err
	}
	// Keep the telemetry overlay coherent: a /report writes conditions durably, so refresh the overlay
	// to the report's (fresher) conditions — otherwise an older heartbeat overlay would shadow the
	// just-written report on the next merge-on-read. Monotonic, so a concurrent newer heartbeat wins.
	fs.refreshTelemetryOverlayFromReport(t, nodeID, n.Conditions, agentVersion, observedAt)
	return nil
}

// telemetryEntryLocked returns the volatile overlay entry for (t, nodeID), lazily creating the maps
// and the entry. The caller MUST hold telemetryMu.
func (fs *FileStore) telemetryEntryLocked(t TenantID, nodeID string) *volatileTelemetry {
	if fs.telemetry == nil {
		fs.telemetry = make(map[TenantID]map[string]*volatileTelemetry)
	}
	m := fs.telemetry[t]
	if m == nil {
		m = make(map[string]*volatileTelemetry)
		fs.telemetry[t] = m
	}
	ent := m[nodeID]
	if ent == nil {
		ent = &volatileTelemetry{}
		m[nodeID] = ent
	}
	return ent
}

// applyTelemetryOverlay merges the volatile telemetry overlay OVER a durable node record, deep-copying
// the slice/map so a returned Node can never alias (and later mutate) the shared overlay. Custody
// fields are never touched. Takes telemetryMu; the caller holds mu (lock order mu -> telemetryMu).
func (fs *FileStore) applyTelemetryOverlay(t TenantID, n *Node) {
	fs.telemetryMu.Lock()
	defer fs.telemetryMu.Unlock()
	m := fs.telemetry[t]
	if m == nil {
		return
	}
	ent := m[n.NodeID]
	if ent == nil {
		return
	}
	if ent.lastSeenSet {
		n.LastSeen = ent.lastSeen
	}
	if ent.telemetrySet {
		n.Conditions = cloneNodeConditions(ent.conditions)
		n.Telemetry = cloneMetrics(ent.telemetry)
		if ent.agentVersion != "" {
			n.LastAgentVersion = ent.agentVersion
		}
	}
}

// refreshTelemetryOverlayFromReport keeps the overlay coherent with a just-persisted /report: the
// report's fresher conditions (+ agent version, + last-seen) win over an older heartbeat, gated by the
// same monotonic writtenAt so a concurrent newer heartbeat is not regressed. It does NOT touch the
// overlay's metrics — /report carries none, so the last heartbeat's metrics persist. Takes telemetryMu.
func (fs *FileStore) refreshTelemetryOverlayFromReport(t TenantID, nodeID string, conditions []NodeCondition, agentVersion string, observedAt time.Time) {
	fs.telemetryMu.Lock()
	defer fs.telemetryMu.Unlock()
	ent := fs.telemetryEntryLocked(t, nodeID)
	if !ent.telemetrySet || !observedAt.Before(ent.writtenAt) { // observedAt >= writtenAt
		ent.conditions = cloneNodeConditions(conditions)
		ent.telemetrySet = true
		ent.writtenAt = observedAt
		if agentVersion != "" {
			ent.agentVersion = agentVersion
		}
	}
	if !ent.lastSeenSet || observedAt.After(ent.lastSeen) {
		ent.lastSeen = observedAt
		ent.lastSeenSet = true
	}
}

// cloneNodeConditions returns a deep copy of a stamped-conditions slice (nil-safe).
func cloneNodeConditions(in []NodeCondition) []NodeCondition {
	if in == nil {
		return nil
	}
	out := make([]NodeCondition, len(in))
	copy(out, in)
	return out
}

// cloneMetrics returns a copy of a metrics map (values are immutable json.RawMessage the callers never
// mutate in place, so a per-key copy is sufficient isolation); nil-safe.
func cloneMetrics(in map[string]json.RawMessage) map[string]json.RawMessage {
	if in == nil {
		return nil
	}
	out := make(map[string]json.RawMessage, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

// RecordTelemetry writes a LIVE health heartbeat to the in-memory overlay ONLY — conditions + metrics +
// last-seen (+ agent version when non-empty). It is a strict subset of SetAppliedGeneration (never
// touches AppliedGeneration/LastChecksum/LastHealth/DesiredGeneration) AND, unlike the pre-overlay
// version, performs NO durable rewrite: a 30s heartbeat must not fsync the whole node record (the DoS
// vector). Node existence is confirmed with a metadata-only os.Stat (a corrupt-but-present record no
// longer fails a heartbeat, which is strictly better — telemetry never writes the file). A monotonic
// writtenAt guard drops a stale write so a concurrent /report's fresher conditions win.
func (fs *FileStore) RecordTelemetry(ctx context.Context, t TenantID, nodeID string, conditions []model.Condition, metrics map[string]json.RawMessage, agentVersion string, observedAt time.Time) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	dir, err := fs.tenantDir(t)
	if err != nil {
		return err
	}
	p, err := fs.nodePath(dir, nodeID)
	if err != nil {
		return err
	}
	if _, statErr := os.Stat(p); statErr != nil {
		if os.IsNotExist(statErr) {
			return ErrNotFound
		}
		return statErr
	}
	fs.telemetryMu.Lock()
	defer fs.telemetryMu.Unlock()
	ent := fs.telemetryEntryLocked(t, nodeID)
	if !ent.lastSeenSet || observedAt.After(ent.lastSeen) {
		ent.lastSeen = observedAt
		ent.lastSeenSet = true
	}
	if !ent.telemetrySet || !observedAt.Before(ent.writtenAt) { // observedAt >= writtenAt
		ent.conditions = stampConditions(conditions, observedAt)
		ent.telemetry = metrics
		ent.telemetrySet = true
		ent.writtenAt = observedAt
		if agentVersion != "" {
			ent.agentVersion = agentVersion
		}
		// plan-2: retain a resource-history sample (in-memory append; a background flusher persists it
		// off the heartbeat path). Gated on the same monotonic freshness as the overlay, so history holds
		// only in-order samples. history has its OWN mutex; lock order telemetryMu -> history.mu is
		// consistent (nothing takes telemetryMu while holding history.mu), so this cannot deadlock.
		if s, ok := resourceSampleFromMetrics(metrics, observedAt); ok {
			fs.history.append(t, nodeID, s)
		}
	}
	return nil
}

// TouchLastSeen records that the agent for nodeID checked in at the given time — to the in-memory
// overlay ONLY (no durable rewrite; same DoS reasoning as RecordTelemetry). Existence is confirmed with
// a metadata-only os.Stat; LastSeen advances monotonically.
func (fs *FileStore) TouchLastSeen(ctx context.Context, t TenantID, nodeID string, at time.Time) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	dir, err := fs.tenantDir(t)
	if err != nil {
		return err
	}
	p, err := fs.nodePath(dir, nodeID)
	if err != nil {
		return err
	}
	if _, statErr := os.Stat(p); statErr != nil {
		if os.IsNotExist(statErr) {
			return ErrNotFound
		}
		return statErr
	}
	fs.telemetryMu.Lock()
	defer fs.telemetryMu.Unlock()
	ent := fs.telemetryEntryLocked(t, nodeID)
	if !ent.lastSeenSet || at.After(ent.lastSeen) {
		ent.lastSeen = at
		ent.lastSeenSet = true
	}
	return nil
}

// =============================== Topology ==================================

// PutTopology stores a new topology version and returns the stored record with
// its assigned Version (1, 2, 3, …). The version is also retained under
// topology-history/ (bounded by TopologyHistoryLimit, oldest pruned). The history
// file is written BEFORE topology.json flips, so a crash between the two leaves a
// harmless extra history entry, never a current record missing from history.
func (fs *FileStore) PutTopology(ctx context.Context, t TenantID, jsonBytes []byte) (TopologyRecord, error) {
	if err := ctx.Err(); err != nil {
		return TopologyRecord{}, err
	}
	fs.mu.Lock()
	defer fs.mu.Unlock()

	dir, err := fs.ensureTenantDir(t)
	if err != nil {
		return TopologyRecord{}, err
	}
	p := filepath.Join(dir, "topology.json")

	var prevVersion int64
	var prev TopologyRecord
	if err := readJSON(p, &prev); err != nil {
		if !os.IsNotExist(err) {
			return TopologyRecord{}, err
		}
	} else {
		prevVersion = prev.Version
	}

	// Defensive copy so the returned record does not alias the caller's slice
	// (parity with MemStore: a caller mutating its input must not affect the store).
	rec := TopologyRecord{
		Version:   prevVersion + 1,
		JSON:      append([]byte(nil), jsonBytes...),
		UpdatedAt: time.Now().UTC(),
	}
	histDir := filepath.Join(dir, "topology-history")
	// Upgrade backfill: a deployment that stored its current topology BEFORE the
	// history feature existed has no history file for it; write it lazily now so
	// the previous version remains recoverable after this put displaces it.
	if prevVersion > 0 {
		if prevHist := filepath.Join(histDir, historyFileName(prevVersion)); !fileExists(prevHist) {
			if err := writeJSONAtomic(prevHist, prev); err != nil {
				return TopologyRecord{}, err
			}
		}
	}
	if err := writeJSONAtomic(filepath.Join(histDir, historyFileName(rec.Version)), rec); err != nil {
		return TopologyRecord{}, err
	}
	if err := writeJSONAtomic(p, rec); err != nil {
		return TopologyRecord{}, err
	}
	// Prune beyond the retention bound. Best-effort: a leftover file is re-pruned on
	// the next put, and pruning must not fail a successful store.
	if cutoff := rec.Version - TopologyHistoryLimit; cutoff > 0 {
		entries, err := os.ReadDir(histDir)
		if err == nil {
			for _, e := range entries {
				v, ok := historyVersionFromName(e.Name())
				if ok && v <= cutoff {
					_ = os.Remove(filepath.Join(histDir, e.Name()))
				}
			}
		}
	}
	return rec, nil
}

// historyFileName is the single format-direction counterpart of
// historyVersionFromName: the on-disk file name for a retained version.
func historyFileName(version int64) string {
	return fmt.Sprintf("%d.json", version)
}

// fileExists reports whether path exists (any stat error other than not-exist is
// treated as existing, so a permission oddity never triggers an overwrite).
func fileExists(path string) bool {
	_, err := os.Stat(path)
	return !os.IsNotExist(err)
}

// historyVersionFromName parses a topology-history file name ("<version>.json")
// into its version, reporting ok=false for anything else (temp files, foreign
// names) so directory scans skip them instead of failing.
func historyVersionFromName(name string) (int64, bool) {
	base, found := strings.CutSuffix(name, ".json")
	if !found {
		return 0, false
	}
	v, err := strconv.ParseInt(base, 10, 64)
	if err != nil || v <= 0 {
		return 0, false
	}
	return v, true
}

// ListTopologyVersions returns the retained versions, newest first. An absent
// current topology (tenant never stored one) is an empty list.
//
// Robustness contract for the RECOVERY surface (review-hardened):
//   - A crash orphan (history file with version > the committed current record,
//     left by a crash between the history write and the topology.json flip) is
//     INVISIBLE — it was never the current topology, so listing or serving it
//     would offer to "recover" a write that never committed. The next put
//     overwrites it (self-heal).
//   - A corrupt history file is SKIPPED, never allowed to brick the whole list
//     (one bit-rotted entry must not 500 the endpoint an operator is using to
//     recover from a bad overwrite). The filename is the lookup key (the same
//     key GetTopologyVersion uses); a body whose Version disagrees is corrupt.
//   - The CURRENT record always appears, even when its history file is missing
//     (a deployment whose topology predates the history feature).
func (fs *FileStore) ListTopologyVersions(ctx context.Context, t TenantID) ([]TopologyVersionInfo, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	fs.mu.Lock()
	defer fs.mu.Unlock()

	dir, err := fs.tenantDir(t)
	if err != nil {
		return nil, err
	}
	var cur TopologyRecord
	if err := readJSON(filepath.Join(dir, "topology.json"), &cur); err != nil {
		if os.IsNotExist(err) {
			return []TopologyVersionInfo{}, nil
		}
		return nil, err
	}

	histDir := filepath.Join(dir, "topology-history")
	entries, err := os.ReadDir(histDir)
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	out := make([]TopologyVersionInfo, 0, len(entries)+1)
	sawCurrent := false
	for _, e := range entries {
		v, ok := historyVersionFromName(e.Name())
		if !ok || v > cur.Version {
			continue // foreign file, or a crash orphan newer than the committed record
		}
		var rec TopologyRecord
		if err := readJSON(filepath.Join(histDir, e.Name()), &rec); err != nil || rec.Version != v {
			continue // unreadable/corrupt entry: skip, never brick the recovery list
		}
		if v == cur.Version {
			sawCurrent = true
		}
		out = append(out, TopologyVersionInfo{
			Version:   rec.Version,
			UpdatedAt: rec.UpdatedAt,
			Bytes:     len(rec.JSON),
		})
	}
	if !sawCurrent {
		out = append(out, TopologyVersionInfo{
			Version:   cur.Version,
			UpdatedAt: cur.UpdatedAt,
			Bytes:     len(cur.JSON),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Version > out[j].Version })
	return out, nil
}

// GetTopologyVersion returns one retained version, or ErrNotFound (unknown,
// pruned, or a crash orphan that never committed). The current record's version
// is always servable, even when its history file is missing (upgrade shape).
func (fs *FileStore) GetTopologyVersion(ctx context.Context, t TenantID, version int64) (TopologyRecord, error) {
	if err := ctx.Err(); err != nil {
		return TopologyRecord{}, err
	}
	fs.mu.Lock()
	defer fs.mu.Unlock()

	dir, err := fs.tenantDir(t)
	if err != nil {
		return TopologyRecord{}, err
	}
	if version <= 0 {
		return TopologyRecord{}, ErrNotFound
	}
	var cur TopologyRecord
	if err := readJSON(filepath.Join(dir, "topology.json"), &cur); err != nil {
		if os.IsNotExist(err) {
			return TopologyRecord{}, ErrNotFound // nothing ever committed
		}
		return TopologyRecord{}, err
	}
	if version > cur.Version {
		return TopologyRecord{}, ErrNotFound // crash orphan / never existed
	}
	if version == cur.Version {
		return cur, nil // the committed current record is authoritative for its version
	}
	var rec TopologyRecord
	if err := readJSON(filepath.Join(dir, "topology-history", historyFileName(version)), &rec); err != nil {
		if os.IsNotExist(err) {
			return TopologyRecord{}, ErrNotFound
		}
		return TopologyRecord{}, err
	}
	return rec, nil
}

// GetTopology returns the current topology, or ErrNotFound.
func (fs *FileStore) GetTopology(ctx context.Context, t TenantID) (TopologyRecord, error) {
	if err := ctx.Err(); err != nil {
		return TopologyRecord{}, err
	}
	fs.mu.Lock()
	defer fs.mu.Unlock()

	dir, err := fs.tenantDir(t)
	if err != nil {
		return TopologyRecord{}, err
	}
	var rec TopologyRecord
	if err := readJSON(filepath.Join(dir, "topology.json"), &rec); err != nil {
		if os.IsNotExist(err) {
			return TopologyRecord{}, ErrNotFound
		}
		return TopologyRecord{}, err
	}
	return rec, nil
}

// ========================= Bundles + generation ============================

// StageBundle stores a node's bundle as the staged (not-yet-current) version,
// replacing any prior staged bundle for that node.
func (fs *FileStore) StageBundle(ctx context.Context, t TenantID, b SignedBundle) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	fs.mu.Lock()
	defer fs.mu.Unlock()

	dir, err := fs.ensureTenantDir(t)
	if err != nil {
		return err
	}
	p, err := fs.bundlePath(dir, b.NodeID, "staged")
	if err != nil {
		return err
	}
	b.IsStaged = true
	b.IsCurrent = false
	return writeJSONAtomic(p, b)
}

// PruneStagedBundles deletes staged bundles whose NodeID is not in keep and
// returns the purged node IDs in stable order. Current bundles are never touched.
func (fs *FileStore) PruneStagedBundles(ctx context.Context, t TenantID, keep []string) ([]string, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	fs.mu.Lock()
	defer fs.mu.Unlock()

	dir, err := fs.tenantDir(t)
	if err != nil {
		return nil, err
	}
	ents, err := os.ReadDir(filepath.Join(dir, "bundles"))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil // nothing staged at all
		}
		return nil, fmt.Errorf("controller: list bundles to prune: %w", err)
	}
	keepSet := make(map[string]bool, len(keep))
	for _, id := range keep {
		keepSet[id] = true
	}
	var purged []string
	var firstErr error
	for _, e := range ents {
		nodeID, found := strings.CutSuffix(e.Name(), ".staged.json")
		if e.IsDir() || !found {
			continue
		}
		if keepSet[nodeID] {
			continue
		}
		if err := os.Remove(filepath.Join(dir, "bundles", e.Name())); err != nil && !os.IsNotExist(err) {
			// Keep going: aborting mid-loop would discard the IDs already removed,
			// and the caller audits the purged list — every actual removal must be
			// reported even when a later one fails (review finding).
			if firstErr == nil {
				firstErr = fmt.Errorf("controller: prune staged bundle %s: %w", nodeID, err)
			}
			continue
		}
		purged = append(purged, nodeID)
	}
	sort.Strings(purged)
	return purged, firstErr
}

// PromoteStaged atomically flips the currently staged bundles to current, clears
// each promoted node's prior current bundle, increments the tenant generation by
// one, sets each promoted node's DesiredGeneration to the new generation, wakes
// WaitForGeneration waiters, and returns the new generation. With nothing
// (currently) staged it returns ErrNoStagedBundle and changes nothing.
//
// Scoping (plan-3): only bundles staged at the generation being promoted
// (current+1) flip; a bundle whose provisional generation was invalidated by an
// interleaved BumpGeneration/promote is stale and stays staged until a re-stage
// refreshes it (or purge-on-stage removes it).
func (fs *FileStore) PromoteStaged(ctx context.Context, t TenantID) (int64, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	fs.mu.Lock()
	defer fs.mu.Unlock()

	dir, err := fs.ensureTenantDir(t)
	if err != nil {
		return 0, err
	}
	bundlesDir := filepath.Join(dir, "bundles")

	ents, err := os.ReadDir(bundlesDir)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, ErrNoStagedBundle
		}
		return 0, fmt.Errorf("controller: list bundles: %w", err)
	}

	cur, err := fs.readGeneration(dir)
	if err != nil {
		return 0, err
	}
	newGen := cur + 1

	// Collect the staged bundles that belong to THIS promote (provisional
	// generation == newGen), in stable NodeID order for deterministic behavior.
	var staged []SignedBundle
	for _, e := range ents {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".staged.json") {
			continue
		}
		var b SignedBundle
		if err := readJSON(filepath.Join(bundlesDir, e.Name()), &b); err != nil {
			return 0, err
		}
		if b.Generation != newGen {
			continue // stale provisional generation — not part of the stage being promoted
		}
		staged = append(staged, b)
	}
	if len(staged) == 0 {
		return 0, ErrNoStagedBundle
	}
	sort.Slice(staged, func(i, j int) bool { return staged[i].NodeID < staged[j].NodeID })

	// Flip each staged bundle to current: write the new current, remove the
	// staged marker, and bump the node's DesiredGeneration. Each per-file write
	// is atomic; the in-process mutex makes the batch logically atomic for any
	// other caller of this Store.
	for _, b := range staged {
		curPath, err := fs.bundlePath(dir, b.NodeID, "current")
		if err != nil {
			return 0, err
		}
		stagedPath, err := fs.bundlePath(dir, b.NodeID, "staged")
		if err != nil {
			return 0, err
		}

		b.IsStaged = false
		b.IsCurrent = true
		b.Generation = newGen
		if err := writeJSONAtomic(curPath, b); err != nil {
			return 0, err
		}
		if err := os.Remove(stagedPath); err != nil && !os.IsNotExist(err) {
			return 0, fmt.Errorf("controller: clear staged bundle: %w", err)
		}

		// Bump the promoted node's DesiredGeneration if a node record exists.
		np, err := fs.nodePath(dir, b.NodeID)
		if err != nil {
			return 0, err
		}
		var n Node
		if err := readJSON(np, &n); err != nil {
			if !os.IsNotExist(err) {
				return 0, err
			}
			// No registry record for this node; nothing to update.
		} else {
			n.DesiredGeneration = newGen
			if err := writeJSONAtomic(np, n); err != nil {
				return 0, err
			}
		}
	}

	// Promote the staged trust-list to the SERVED slot together with the bundles, so /config always
	// serves a (bundle, manifest) pair from one generation and a STAGE never disturbs the live
	// served manifest. Only a SIGNED staged manifest is promoted (the controller PromoteStaged gate
	// verified it before calling this); an unsigned/absent staged slot leaves the served slot intact.
	//
	// CRASH ORDERING: these are independent atomic renames, NOT one transaction. Per-node current
	// bundles and served_trustlist.json are written BEFORE generation.json (committed last), so the
	// generation counter never runs ahead of the bundle/manifest pair. In-process, GetServedConfig
	// holds fs.mu across all three reads, so a live reader never sees a torn pair. Across a PROCESS
	// crash in the narrow window after a bundle flip but before this served write, a node could be
	// served a (new-bundle, old-served-manifest) pair; that is FAIL-CLOSED (the agent's offline
	// bundle-digest binding refuses the mismatch and keeps last-good) and SELF-REPAIRING (a re-run of
	// PromoteStaged rewrites served_trustlist.json). It is not a forgery (the off-host signature is
	// still mandatory) and is the same severity class as the pre-existing partial-promote window.
	var stagedTL StoredTrustList
	if err := readJSON(filepath.Join(dir, "signed_trustlist.json"), &stagedTL); err == nil {
		if len(stagedTL.SignatureJSON) > 0 {
			if err := writeJSONAtomic(filepath.Join(dir, "served_trustlist.json"), stagedTL); err != nil {
				return 0, err
			}
		}
	} else if !os.IsNotExist(err) {
		return 0, err
	}

	// Commit the new generation last: WaitForGeneration polls generation.json,
	// so the bundles/nodes are already in place before the counter advances.
	if err := writeJSONAtomic(filepath.Join(dir, "generation.json"), generationFile{Generation: newGen}); err != nil {
		return 0, err
	}
	return newGen, nil
}

// GetCurrentBundle returns the node's current (promoted) bundle, or ErrNotFound.
func (fs *FileStore) GetCurrentBundle(ctx context.Context, t TenantID, nodeID string) (SignedBundle, error) {
	if err := ctx.Err(); err != nil {
		return SignedBundle{}, err
	}
	fs.mu.Lock()
	defer fs.mu.Unlock()

	dir, err := fs.tenantDir(t)
	if err != nil {
		return SignedBundle{}, err
	}
	p, err := fs.bundlePath(dir, nodeID, "current")
	if err != nil {
		return SignedBundle{}, err
	}
	var b SignedBundle
	if err := readJSON(p, &b); err != nil {
		if os.IsNotExist(err) {
			return SignedBundle{}, ErrNotFound
		}
		return SignedBundle{}, err
	}
	return b, nil
}

// CurrentGeneration returns the tenant's current generation (0 if none promoted).
func (fs *FileStore) CurrentGeneration(ctx context.Context, t TenantID) (int64, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	fs.mu.Lock()
	defer fs.mu.Unlock()

	dir, err := fs.tenantDir(t)
	if err != nil {
		return 0, err
	}
	return fs.readGeneration(dir)
}

// BumpGeneration increments the persisted generation counter (generation.json)
// atomically, WITHOUT touching any bundle: the staged/current bundle files are left
// in place, so GetCurrentBundle keeps returning the last promoted bundle. It mirrors
// how PromoteStaged advances generation.json (read current, write current+1 via the
// atomic temp-file + rename), so the ~200ms WaitForGeneration poller picks it up and
// a parked agent's long-poll fires. Returns the new generation. Use PromoteStaged to
// actually flip a staged bundle set live; BumpGeneration is a WAKE-only advance.
func (fs *FileStore) BumpGeneration(ctx context.Context, t TenantID) (int64, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	fs.mu.Lock()
	defer fs.mu.Unlock()

	dir, err := fs.ensureTenantDir(t)
	if err != nil {
		return 0, err
	}
	cur, err := fs.readGeneration(dir)
	if err != nil {
		return 0, err
	}
	newGen := cur + 1
	if err := writeJSONAtomic(filepath.Join(dir, "generation.json"), generationFile{Generation: newGen}); err != nil {
		return 0, err
	}
	return newGen, nil
}

// WaitForGeneration blocks until the tenant's current generation is strictly
// greater than afterGen, then returns it; or returns ctx.Err() if ctx is done
// first. It polls generation.json on a short interval (the disk Store has no
// in-process condition variable to wake, so promotion is observed by polling).
// If the condition is already satisfied it returns immediately, even when ctx is
// already done — available data is delivered ahead of cancellation.
func (fs *FileStore) WaitForGeneration(ctx context.Context, t TenantID, afterGen int64) (int64, error) {
	if _, err := fs.tenantDir(t); err != nil {
		return 0, err
	}

	// Fast path: if already past afterGen, return without waiting.
	if gen, err := fs.lockedGeneration(t); err != nil {
		return 0, err
	} else if gen > afterGen {
		return gen, nil
	}

	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return 0, ctx.Err()
		case <-ticker.C:
			gen, err := fs.lockedGeneration(t)
			if err != nil {
				return 0, err
			}
			if gen > afterGen {
				return gen, nil
			}
		}
	}
}

// lockedGeneration reads the tenant's generation under the mutex without an
// up-front ctx check; WaitForGeneration uses it so an already-satisfied
// condition is reported even if ctx is already done.
func (fs *FileStore) lockedGeneration(t TenantID) (int64, error) {
	fs.mu.Lock()
	defer fs.mu.Unlock()

	dir, err := fs.tenantDir(t)
	if err != nil {
		return 0, err
	}
	return fs.readGeneration(dir)
}

// =========================== Enrollment tokens =============================

// CreateEnrollmentToken stores a single-use, node-scoped, TTL token as JSON under
// <root>/<tenant>/tokens/<tokenHash>.json (0700 dir / 0600 file, atomic write). A
// later CreateEnrollmentToken with the same hash overwrites the prior record.
func (fs *FileStore) CreateEnrollmentToken(ctx context.Context, t TenantID, tok EnrollmentToken) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	fs.mu.Lock()
	defer fs.mu.Unlock()

	dir, err := fs.ensureTenantDir(t)
	if err != nil {
		return err
	}
	p, err := fs.tokenPath(dir, tok.TokenHash)
	if err != nil {
		return err
	}
	return writeJSONAtomic(p, tok)
}

// ConsumeEnrollmentToken atomically validates and burns a token under the mutex:
// it reads the token at tokens/<tokenHash>.json and returns ErrTokenInvalid if it
// is absent, if its NodeID != nodeID, or if now is at/after ExpiresAt;
// ErrTokenConsumed if it was already burned; otherwise it sets ConsumedAt=now and
// writes the record back atomically. Holding fs.mu across the read-modify-write
// makes the check-and-burn race-safe within this process.
func (fs *FileStore) ConsumeEnrollmentToken(ctx context.Context, t TenantID, tokenHash, nodeID string, now time.Time) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	fs.mu.Lock()
	defer fs.mu.Unlock()

	dir, err := fs.tenantDir(t)
	if err != nil {
		return err
	}
	p, err := fs.tokenPath(dir, tokenHash)
	if err != nil {
		return err
	}
	var tok EnrollmentToken
	if err := readJSON(p, &tok); err != nil {
		if os.IsNotExist(err) {
			return ErrTokenInvalid
		}
		return err
	}
	if tok.NodeID != nodeID || !now.Before(tok.ExpiresAt) {
		return ErrTokenInvalid
	}
	if tok.ConsumedAt != nil {
		return ErrTokenConsumed
	}
	consumed := now
	tok.ConsumedAt = &consumed
	return writeJSONAtomic(p, tok)
}

// PurgeEnrollmentTokensForNode removes every enrollment token scoped to nodeID under
// the mutex, returning the count removed. It scans <tenant>/tokens/*.json, reads each
// record, and deletes those whose NodeID matches. A missing tokens dir yields (0, nil);
// a single unreadable/garbage file is skipped (best-effort GC), not fatal.
func (fs *FileStore) PurgeEnrollmentTokensForNode(ctx context.Context, t TenantID, nodeID string) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	fs.mu.Lock()
	defer fs.mu.Unlock()

	dir, err := fs.tenantDir(t)
	if err != nil {
		return 0, err
	}
	tokensDir := filepath.Join(dir, "tokens")
	entries, err := os.ReadDir(tokensDir)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, err
	}
	n := 0
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		p := filepath.Join(tokensDir, e.Name())
		var tok EnrollmentToken
		if err := readJSON(p, &tok); err != nil {
			continue // skip unreadable/garbage; best-effort purge
		}
		if tok.NodeID == nodeID {
			if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
				return n, err
			}
			n++
		}
	}
	return n, nil
}

// ====================== Passkey login challenges ===========================

func (fs *FileStore) CreateLoginChallenge(ctx context.Context, t TenantID, lc LoginChallenge) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	fs.mu.Lock()
	defer fs.mu.Unlock()

	dir, err := fs.ensureTenantDir(t)
	if err != nil {
		return err
	}
	p, err := fs.loginChallengePath(dir, lc.ChallengeHash)
	if err != nil {
		return err
	}
	return writeJSONAtomic(p, lc)
}

// ConsumeLoginChallenge atomically validates and burns a login challenge under the mutex
// by DELETING its file: it reads login-challenges/<challengeHash>.json and returns
// ErrChallengeInvalid if it is absent, if its Operator != operator, or if now is at/after
// ExpiresAt; otherwise it removes the file and returns nil. Holding fs.mu across the
// read-and-delete makes the check-and-burn race-safe within this process, so a captured
// assertion cannot be replayed (the file is gone) and two concurrent logins cannot both
// win. An expired file is removed (lazy GC); a wrong-operator file is left intact.
func (fs *FileStore) ConsumeLoginChallenge(ctx context.Context, t TenantID, challengeHash, operator string, now time.Time) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	fs.mu.Lock()
	defer fs.mu.Unlock()

	dir, err := fs.tenantDir(t)
	if err != nil {
		return err
	}
	p, err := fs.loginChallengePath(dir, challengeHash)
	if err != nil {
		return err
	}
	var lc LoginChallenge
	if err := readJSON(p, &lc); err != nil {
		if os.IsNotExist(err) {
			return ErrChallengeInvalid
		}
		return err
	}
	if !now.Before(lc.ExpiresAt) {
		_ = os.Remove(p) // expired: lazy GC
		return ErrChallengeInvalid
	}
	if lc.Operator != operator {
		return ErrChallengeInvalid // not the caller's challenge to burn
	}
	return os.Remove(p) // success: single-use consume
}

// ========================== Node API tokens ================================

// IssueNodeAPIToken stamps tokenHash onto the node record's APITokenHash AND writes
// the reverse index apitokens/<hash>.json ({node_id}) under the mutex. It returns
// ErrNotFound if no node record exists for nodeID. Rotation is self-cleaning: if the
// node already carried a different APITokenHash, that prior apitokens/<oldhash>.json
// entry is removed before the new one is written (a not-exist removal is tolerated),
// so a rotated (stale) token leaves no orphan index file. Each write is individually
// atomic (temp-file + rename); the in-process mutex makes the sequence logically
// atomic for any other caller of this Store.
func (fs *FileStore) IssueNodeAPIToken(ctx context.Context, t TenantID, nodeID, tokenHash string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	fs.mu.Lock()
	defer fs.mu.Unlock()

	dir, err := fs.ensureTenantDir(t)
	if err != nil {
		return err
	}
	np, err := fs.nodePath(dir, nodeID)
	if err != nil {
		return err
	}
	var n Node
	if err := readJSON(np, &n); err != nil {
		if os.IsNotExist(err) {
			return ErrNotFound
		}
		return err
	}
	ip, err := fs.apiTokenPath(dir, tokenHash)
	if err != nil {
		return err
	}
	// Drop any prior reverse-index file for this node's old token so a rotated
	// token can never linger and resolve to the node.
	if n.APITokenHash != "" && n.APITokenHash != tokenHash {
		oldIP, err := fs.apiTokenPath(dir, n.APITokenHash)
		if err != nil {
			return err
		}
		if err := os.Remove(oldIP); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("controller: delete stale api token index: %w", err)
		}
	}
	n.APITokenHash = tokenHash
	if err := writeJSONAtomic(np, n); err != nil {
		return err
	}
	return writeJSONAtomic(ip, apiTokenIndex{NodeID: nodeID})
}

// LookupNodeByAPIToken resolves a presented token's hash to its Node by reading the
// reverse index apitokens/<hash>.json, then the referenced node record, self-
// consistently: it returns ErrTokenInvalid unless the index resolves to a live node
// whose own APITokenHash still equals tokenHash AND whose Status is NodeApproved.
// This rejects an absent index entry, a missing node record, a stale/orphaned index
// file that no longer matches the node's current token, and any non-approved
// (pending or revoked) node.
func (fs *FileStore) LookupNodeByAPIToken(ctx context.Context, t TenantID, tokenHash string) (Node, error) {
	if err := ctx.Err(); err != nil {
		return Node{}, err
	}
	fs.mu.Lock()
	defer fs.mu.Unlock()

	dir, err := fs.tenantDir(t)
	if err != nil {
		return Node{}, err
	}
	ip, err := fs.apiTokenPath(dir, tokenHash)
	if err != nil {
		return Node{}, err
	}
	var idx apiTokenIndex
	if err := readJSON(ip, &idx); err != nil {
		if os.IsNotExist(err) {
			return Node{}, ErrTokenInvalid
		}
		return Node{}, err
	}
	np, err := fs.nodePath(dir, idx.NodeID)
	if err != nil {
		return Node{}, err
	}
	var n Node
	if err := readJSON(np, &n); err != nil {
		if os.IsNotExist(err) {
			return Node{}, ErrTokenInvalid
		}
		return Node{}, err
	}
	if n.APITokenHash != tokenHash || n.Status != NodeApproved {
		return Node{}, ErrTokenInvalid
	}
	return n, nil
}

// RevokeNodeAPIToken clears the node record's APITokenHash and deletes the reverse
// index entry, immediately invalidating the bearer token. It is idempotent: a
// missing node, or a node with no issued token, is a no-op success (no ErrNotFound).
func (fs *FileStore) RevokeNodeAPIToken(ctx context.Context, t TenantID, nodeID string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	fs.mu.Lock()
	defer fs.mu.Unlock()

	dir, err := fs.tenantDir(t)
	if err != nil {
		return err
	}
	np, err := fs.nodePath(dir, nodeID)
	if err != nil {
		return err
	}
	var n Node
	if err := readJSON(np, &n); err != nil {
		if os.IsNotExist(err) {
			return nil // idempotent: no node, nothing to revoke
		}
		return err
	}
	if n.APITokenHash == "" {
		return nil // idempotent: no issued token
	}
	ip, err := fs.apiTokenPath(dir, n.APITokenHash)
	if err != nil {
		return err
	}
	n.APITokenHash = ""
	if err := writeJSONAtomic(np, n); err != nil {
		return err
	}
	if err := os.Remove(ip); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("controller: delete api token index: %w", err)
	}
	return nil
}

// ===================== Keystone: operator credential + trust-list ==========

// SetOperatorCredential pins (or replaces) the tenant's off-host operator signing
// credential as <root>/<tenant>/operator_credential.json (0700 dir / 0600 file,
// atomic write). Pinning one turns KEYSTONE ON for the tenant.
func (fs *FileStore) SetOperatorCredential(ctx context.Context, t TenantID, c OperatorCredential) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	fs.mu.Lock()
	defer fs.mu.Unlock()

	dir, err := fs.ensureTenantDir(t)
	if err != nil {
		return err
	}
	return writeJSONAtomic(filepath.Join(dir, "operator_credential.json"), c)
}

// readOperatorCredentialLocked decodes the tenant's pinned operator credential from dir, mapping a
// missing file to ErrNotFound. It does NO locking — the caller must hold fs.mu — so the same
// read+ErrNotFound mapping serves both the standalone GetOperatorCredential and the atomic
// GetServedConfig snapshot, keeping their semantics in lockstep.
func readOperatorCredentialLocked(dir string) (OperatorCredential, error) {
	var c OperatorCredential
	if err := readJSON(filepath.Join(dir, "operator_credential.json"), &c); err != nil {
		if os.IsNotExist(err) {
			return OperatorCredential{}, ErrNotFound
		}
		return OperatorCredential{}, err
	}
	return c, nil
}

// readServedTrustListLocked decodes the tenant's SERVED (last-promoted) signed trust-list from dir,
// mapping a missing file to ErrNotFound. It does NO locking — the caller must hold fs.mu — so it
// composes into the atomic GetServedConfig snapshot alongside the standalone GetServedTrustList.
func readServedTrustListLocked(dir string) (StoredTrustList, error) {
	var sl StoredTrustList
	if err := readJSON(filepath.Join(dir, "served_trustlist.json"), &sl); err != nil {
		if os.IsNotExist(err) {
			return StoredTrustList{}, ErrNotFound
		}
		return StoredTrustList{}, err
	}
	return sl, nil
}

// GetOperatorCredential returns the tenant's pinned operator credential, or
// ErrNotFound when operator_credential.json is absent (keystone OFF).
func (fs *FileStore) GetOperatorCredential(ctx context.Context, t TenantID) (OperatorCredential, error) {
	if err := ctx.Err(); err != nil {
		return OperatorCredential{}, err
	}
	fs.mu.Lock()
	defer fs.mu.Unlock()

	dir, err := fs.tenantDir(t)
	if err != nil {
		return OperatorCredential{}, err
	}
	return readOperatorCredentialLocked(dir)
}

// PutSignedTrustList stores (replacing any prior) the STAGED membership trust-list as
// <root>/<tenant>/signed_trustlist.json (0700 dir / 0600 file, atomic write) — the
// to-be-signed / just-signed manifest of the pending generation, NOT the served slot
// (served_trustlist.json, advanced only by PromoteStaged). The byte fields serialize as
// base64 under encoding/json, round-tripping the raw bytes faithfully.
func (fs *FileStore) PutSignedTrustList(ctx context.Context, t TenantID, sl StoredTrustList) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	fs.mu.Lock()
	defer fs.mu.Unlock()

	dir, err := fs.ensureTenantDir(t)
	if err != nil {
		return err
	}
	return writeJSONAtomic(filepath.Join(dir, "signed_trustlist.json"), sl)
}

// GetCurrentSignedTrustList returns the tenant's STAGED signed trust-list (the to-be-signed /
// just-signed manifest backing signed_trustlist.json), or ErrNotFound when none is staged. The
// name is historical; the served manifest is GetServedTrustList.
func (fs *FileStore) GetCurrentSignedTrustList(ctx context.Context, t TenantID) (StoredTrustList, error) {
	if err := ctx.Err(); err != nil {
		return StoredTrustList{}, err
	}
	fs.mu.Lock()
	defer fs.mu.Unlock()

	dir, err := fs.tenantDir(t)
	if err != nil {
		return StoredTrustList{}, err
	}
	var sl StoredTrustList
	if err := readJSON(filepath.Join(dir, "signed_trustlist.json"), &sl); err != nil {
		if os.IsNotExist(err) {
			return StoredTrustList{}, ErrNotFound
		}
		return StoredTrustList{}, err
	}
	return sl, nil
}

// GetServedTrustList returns the tenant's SERVED (last-promoted) signed trust-list from
// served_trustlist.json, or ErrNotFound when nothing has been promoted under a keystone.
func (fs *FileStore) GetServedTrustList(ctx context.Context, t TenantID) (StoredTrustList, error) {
	if err := ctx.Err(); err != nil {
		return StoredTrustList{}, err
	}
	fs.mu.Lock()
	defer fs.mu.Unlock()

	dir, err := fs.tenantDir(t)
	if err != nil {
		return StoredTrustList{}, err
	}
	return readServedTrustListLocked(dir)
}

// GetServedConfig atomically snapshots what /config serves nodeID — the current bundle, the
// keystone-on flag (operator_credential.json present), and the served signed trust-list — all
// under one fs.mu lock so a concurrent PromoteStaged cannot expose a torn (old-bundle,
// new-manifest) pair. ErrNotFound when the node has no current bundle.
func (fs *FileStore) GetServedConfig(ctx context.Context, t TenantID, nodeID string) (ServedConfig, error) {
	if err := ctx.Err(); err != nil {
		return ServedConfig{}, err
	}
	fs.mu.Lock()
	defer fs.mu.Unlock()

	dir, err := fs.tenantDir(t)
	if err != nil {
		return ServedConfig{}, err
	}
	p, err := fs.bundlePath(dir, nodeID, "current")
	if err != nil {
		return ServedConfig{}, err
	}
	var b SignedBundle
	if err := readJSON(p, &b); err != nil {
		if os.IsNotExist(err) {
			return ServedConfig{}, ErrNotFound
		}
		return ServedConfig{}, err
	}
	sc := ServedConfig{Bundle: b}
	// Keystone ON iff a pinned operator credential exists (same read+ErrNotFound mapping as the
	// standalone GetOperatorCredential, via the shared lock-free helper).
	switch _, err := readOperatorCredentialLocked(dir); {
	case err == nil:
		sc.KeystoneOn = true
	case errors.Is(err, ErrNotFound):
		// keystone OFF — leave KeystoneOn false.
	default:
		return ServedConfig{}, err
	}
	if sc.KeystoneOn {
		switch sl, err := readServedTrustListLocked(dir); {
		case err == nil:
			if len(sl.SignatureJSON) > 0 {
				sc.TrustList = sl
				sc.HasTrustList = true
			}
		case errors.Is(err, ErrNotFound):
			// nothing promoted under the keystone yet — HasTrustList stays false (fail-closed at /config).
		default:
			return ServedConfig{}, err
		}
	}
	return sc, nil
}

// ===================== Operators + sessions (login) ========================

// PutOperator creates or replaces an operator account as operators/<username>.json
// (0700 dir / 0600 file, atomic write). The record carries only the argon2id PHC
// hash — never a plaintext password.
func (fs *FileStore) PutOperator(ctx context.Context, t TenantID, op Operator) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	fs.mu.Lock()
	defer fs.mu.Unlock()

	dir, err := fs.ensureTenantDir(t)
	if err != nil {
		return err
	}
	p, err := fs.operatorPath(dir, op.Username)
	if err != nil {
		return err
	}
	return writeJSONAtomic(p, op)
}

// GetOperator returns the operator account, or ErrNotFound.
func (fs *FileStore) GetOperator(ctx context.Context, t TenantID, username string) (Operator, error) {
	if err := ctx.Err(); err != nil {
		return Operator{}, err
	}
	fs.mu.Lock()
	defer fs.mu.Unlock()

	dir, err := fs.tenantDir(t)
	if err != nil {
		return Operator{}, err
	}
	p, err := fs.operatorPath(dir, username)
	if err != nil {
		return Operator{}, err
	}
	var op Operator
	if err := readJSON(p, &op); err != nil {
		if os.IsNotExist(err) {
			return Operator{}, ErrNotFound
		}
		return Operator{}, err
	}
	return op, nil
}

// ListOperators reads every operator record under <dir>/operators, sorted by
// Username.
func (fs *FileStore) ListOperators(ctx context.Context, t TenantID) ([]Operator, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	fs.mu.Lock()
	defer fs.mu.Unlock()

	dir, err := fs.tenantDir(t)
	if err != nil {
		return nil, err
	}
	opsDir := filepath.Join(dir, "operators")
	ents, err := os.ReadDir(opsDir)
	if err != nil {
		if os.IsNotExist(err) {
			return []Operator{}, nil
		}
		return nil, fmt.Errorf("controller: list operators: %w", err)
	}
	out := make([]Operator, 0, len(ents))
	for _, e := range ents {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		var op Operator
		if err := readJSON(filepath.Join(opsDir, e.Name()), &op); err != nil {
			return nil, err
		}
		out = append(out, op)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Username < out[j].Username })
	return out, nil
}

// DeleteOperator removes an operator account. Idempotent: a missing account is a
// no-op success.
func (fs *FileStore) DeleteOperator(ctx context.Context, t TenantID, username string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	fs.mu.Lock()
	defer fs.mu.Unlock()

	dir, err := fs.tenantDir(t)
	if err != nil {
		return err
	}
	p, err := fs.operatorPath(dir, username)
	if err != nil {
		return err
	}
	if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("controller: delete operator: %w", err)
	}
	return nil
}

// AdvanceTOTPStep atomically bumps the operator's TOTP replay watermark to step iff
// step > the stored value. The read-modify-write is held under fs.mu for the whole
// operation (in-process atomic), closing the login TOCTOU. See the Store doc.
func (fs *FileStore) AdvanceTOTPStep(ctx context.Context, t TenantID, username string, step int64) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	fs.mu.Lock()
	defer fs.mu.Unlock()

	dir, err := fs.tenantDir(t)
	if err != nil {
		return false, err
	}
	p, err := fs.operatorPath(dir, username)
	if err != nil {
		return false, err
	}
	var op Operator
	if err := readJSON(p, &op); err != nil {
		if os.IsNotExist(err) {
			return false, ErrNotFound
		}
		return false, err
	}
	if step <= op.TOTPLastUsedStep {
		return false, nil
	}
	op.TOTPLastUsedStep = step
	if err := writeJSONAtomic(p, op); err != nil {
		return false, err
	}
	return true, nil
}

// CreateSession stores a minted operator session as sessions/<tokenHash>.json (0700
// dir / 0600 file, atomic write), keyed by the token's hash.
func (fs *FileStore) CreateSession(ctx context.Context, t TenantID, sess Session) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	fs.mu.Lock()
	defer fs.mu.Unlock()

	dir, err := fs.ensureTenantDir(t)
	if err != nil {
		return err
	}
	p, err := fs.sessionPath(dir, sess.TokenHash)
	if err != nil {
		return err
	}
	return writeJSONAtomic(p, sess)
}

// LookupSession resolves a session token's hash to its Session, returning
// ErrTokenInvalid if the file is absent OR the session has expired (now at/after
// ExpiresAt). An expired session encountered here is lazily removed so abandoned-
// but-presented sessions self-clean.
func (fs *FileStore) LookupSession(ctx context.Context, t TenantID, tokenHash string, now time.Time) (Session, error) {
	if err := ctx.Err(); err != nil {
		return Session{}, err
	}
	fs.mu.Lock()
	defer fs.mu.Unlock()

	dir, err := fs.tenantDir(t)
	if err != nil {
		return Session{}, err
	}
	p, err := fs.sessionPath(dir, tokenHash)
	if err != nil {
		return Session{}, err
	}
	var sess Session
	if err := readJSON(p, &sess); err != nil {
		if os.IsNotExist(err) {
			return Session{}, ErrTokenInvalid
		}
		return Session{}, err
	}
	if !now.Before(sess.ExpiresAt) {
		if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
			return Session{}, fmt.Errorf("controller: delete expired session: %w", err)
		}
		return Session{}, ErrTokenInvalid
	}
	return sess, nil
}

// DeleteSession removes a session (logout / revoke). Idempotent: a missing session
// is a no-op success.
func (fs *FileStore) DeleteSession(ctx context.Context, t TenantID, tokenHash string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	fs.mu.Lock()
	defer fs.mu.Unlock()

	dir, err := fs.tenantDir(t)
	if err != nil {
		return err
	}
	p, err := fs.sessionPath(dir, tokenHash)
	if err != nil {
		return err
	}
	if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("controller: delete session: %w", err)
	}
	return nil
}

// ===================== Controller settings (bootstrap) =====================

// GetSettings returns the tenant's saved settings, or ErrNotFound when settings.json
// is absent.
func (fs *FileStore) GetSettings(ctx context.Context, t TenantID) (ControllerSettings, error) {
	if err := ctx.Err(); err != nil {
		return ControllerSettings{}, err
	}
	fs.mu.Lock()
	defer fs.mu.Unlock()

	dir, err := fs.tenantDir(t)
	if err != nil {
		return ControllerSettings{}, err
	}
	var cs ControllerSettings
	if err := readJSON(filepath.Join(dir, "settings.json"), &cs); err != nil {
		if os.IsNotExist(err) {
			return ControllerSettings{}, ErrNotFound
		}
		return ControllerSettings{}, err
	}
	fs.history.setCap(t, cs.EffectiveHistoryCap()) // keep the history cap cache in sync (no disk on append)
	return cs, nil
}

// PutSettings stores (replacing) the tenant's settings as settings.json (0700 dir /
// 0600 file, atomic write).
func (fs *FileStore) PutSettings(ctx context.Context, t TenantID, cs ControllerSettings) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	fs.mu.Lock()
	defer fs.mu.Unlock()

	dir, err := fs.ensureTenantDir(t)
	if err != nil {
		return err
	}
	if err := writeJSONAtomic(filepath.Join(dir, "settings.json"), cs); err != nil {
		return err
	}
	fs.history.setCap(t, cs.EffectiveHistoryCap()) // track the operator's cap without reading on append
	return nil
}

// QueryTelemetryHistory returns the node's retained resource-history samples within [from, to]
// (durable JSONL + the not-yet-flushed in-memory buffer), sorted by time and bounded by the operator's
// per-node cap (plan-2).
func (fs *FileStore) QueryTelemetryHistory(ctx context.Context, t TenantID, nodeID string, from, to time.Time) ([]ResourceSample, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return fs.history.query(t, nodeID, from, to)
}

// GetSigningAnchor reads the tenant's pinned signing public key from signing-anchor.json, or
// ErrNotFound when none is pinned.
func (fs *FileStore) GetSigningAnchor(ctx context.Context, t TenantID) (SigningAnchor, error) {
	if err := ctx.Err(); err != nil {
		return SigningAnchor{}, err
	}
	fs.mu.Lock()
	defer fs.mu.Unlock()

	dir, err := fs.tenantDir(t)
	if err != nil {
		return SigningAnchor{}, err
	}
	var a SigningAnchor
	if err := readJSON(filepath.Join(dir, "signing-anchor.json"), &a); err != nil {
		if os.IsNotExist(err) {
			return SigningAnchor{}, ErrNotFound
		}
		return SigningAnchor{}, err
	}
	return a, nil
}

// PutSigningAnchor pins (replacing any prior) the tenant's signing public key as
// signing-anchor.json (0700 dir / 0600 file, atomic write).
func (fs *FileStore) PutSigningAnchor(ctx context.Context, t TenantID, a SigningAnchor) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	fs.mu.Lock()
	defer fs.mu.Unlock()

	dir, err := fs.ensureTenantDir(t)
	if err != nil {
		return err
	}
	return writeJSONAtomic(filepath.Join(dir, "signing-anchor.json"), a)
}

// ================================ Audit ====================================

// AppendAudit appends an entry, chaining its PrevHash/Hash to the tenant's prior
// entry and assigning a monotonic Seq. The caller-provided Timestamp is
// preserved. Returns the stored entry with Seq/PrevHash/Hash set.
//
// The append is O(1): the next Seq + PrevHash come from the in-memory tail cache (seeded
// once via loadAuditTail), and the entry is written with a single O_APPEND line — no
// full-file read-modify-write per append (plan-6). The log is bounded by an amortized
// rotation that trims to auditRetain once it reaches auditRotateAt.
func (fs *FileStore) AppendAudit(ctx context.Context, t TenantID, e AuditEntry) (AuditEntry, error) {
	if err := ctx.Err(); err != nil {
		return AuditEntry{}, err
	}
	fs.mu.Lock()
	defer fs.mu.Unlock()

	dir, err := fs.ensureTenantDir(t)
	if err != nil {
		return AuditEntry{}, err
	}
	tail, err := fs.loadAuditTail(t, dir)
	if err != nil {
		return AuditEntry{}, err
	}

	e.Seq = tail.seq + 1
	e = chainAudit(e, tail.hash)

	line, err := json.Marshal(e)
	if err != nil {
		return AuditEntry{}, fmt.Errorf("controller: marshal %s: %w", auditFileName, err)
	}
	line = append(line, '\n')
	f, err := os.OpenFile(filepath.Join(dir, auditFileName), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		return AuditEntry{}, fmt.Errorf("controller: open %s: %w", auditFileName, err)
	}
	if _, werr := f.Write(line); werr != nil {
		_ = f.Close()
		return AuditEntry{}, fmt.Errorf("controller: append %s: %w", auditFileName, werr)
	}
	// fsync before close so the appended line is durable on stable storage (B2): the rotate
	// block below and the "durably appended" comment both assume the entry has actually hit
	// disk, not merely the page cache. Without this a crash right after a best-effort append
	// could silently drop the most recent audit row.
	if serr := f.Sync(); serr != nil {
		_ = f.Close()
		return AuditEntry{}, fmt.Errorf("controller: sync %s: %w", auditFileName, serr)
	}
	if cerr := f.Close(); cerr != nil {
		return AuditEntry{}, fmt.Errorf("controller: close %s: %w", auditFileName, cerr)
	}

	// Append committed — advance the cached tail, then rotate if we hit the high-water mark.
	tail.seq = e.Seq
	tail.hash = e.Hash
	tail.count++
	if tail.count > auditRotateAt {
		// The entry is already durably appended, so a rotation failure must neither lose it
		// nor fail the caller (many callers treat AppendAudit as best-effort). count stays
		// above the high-water mark, so the NEXT append retries rotation and self-heals; the
		// log just stays slightly over auditRetain until then. Caveat: while rotation keeps
		// failing (e.g. a full disk — writeAuditJSONL needs a momentary full temp copy), each
		// append re-reads + re-attempts the full rewrite, so the O(1) append degrades to O(N)
		// until space frees; it self-corrects once the rewrite succeeds.
		_ = fs.rotateAudit(dir, tail)
	}
	return e, nil
}

// ListAudit returns the tenant's audit entries in Seq order.
func (fs *FileStore) ListAudit(ctx context.Context, t TenantID) ([]AuditEntry, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	fs.mu.Lock()
	defer fs.mu.Unlock()

	dir, err := fs.tenantDir(t)
	if err != nil {
		return nil, err
	}
	entries, err := fs.readAudit(dir)
	if err != nil {
		return nil, err
	}
	if entries == nil {
		return []AuditEntry{}, nil
	}
	return entries, nil
}
