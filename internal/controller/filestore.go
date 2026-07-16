package controller

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// filekv is the JSON-on-disk kvBackend, durable for the single-tenant v1 deployment. It holds ONLY
// storage — the atomic temp-file+fsync+rename writer, the sort-by-key stable listing, the
// generation.json counter with a 200ms poll wake, and the append-only JSONL audit log (torn-tail
// tolerant + legacy migration). Every business rule lives in the shared storeCore (storecore.go); the
// telemetry overlay + resource-history live there too. FileStore wraps a storeCore over a filekv.
//
// All in-process access is serialized by fs.mu (the store-wide lock the core holds via withLock);
// cross-process durability is provided by the atomic renames (a single controller process owns its
// root). The on-disk layout below is FROZEN for persisted-data backward-compat.
//
// SPOF / scaling note (deferred to rc.2/GA — NOT fixed here): (1) ONE global mutex (fs.mu) serializes
// EVERY store operation, so throughput is single-writer; (2) WaitForGeneration POLLS generation.json
// every 200ms (no in-process condition variable on the disk store). Neither is a correctness bug.
//
// On-disk layout under <root>/<tenant>/ (the collection → path mapping is fileColls):
//
//	nodes/<nodeID>.json                 one Node record
//	topology.json                       the current TopologyRecord (with Version)
//	topology-history/<version>.json     one retained TopologyRecord per version
//	bundles/<nodeID>.staged.json        the node's staged SignedBundle (if any)
//	bundles/<nodeID>.current.json       the node's current SignedBundle (if any)
//	tokens/<tokenHash>.json             one EnrollmentToken record (keyed by hash)
//	login-challenges/<hash>.json        one AssertionChallenge (historical directory name)
//	apitokens/<hash>.json               node API token reverse index ({node_id}), keyed by hash
//	generation.json                     the tenant's current generation counter
//	audit.jsonl                         the append-only audit log (one AuditEntry per line, rotated)
//	operator_credential.json            the pinned off-host operator credential (keystone)
//	keystone-transition.json            pending audited keystone pin/rotation recovery marker
//	signed_trustlist.json               the STAGED (to-be-signed/just-signed) membership manifest
//	trustlist-history.json               last fully-written manifest (epoch history only; never served)
//	staged-set.json                      exact candidate commit seal (or non-promotable history marker)
//	served_trustlist.json               the SERVED (last-promoted) signed membership manifest
//	operators/<username>.json           one operator account (argon2id PHC hash)
//	sessions/<tokenHash>.json           one operator login session, keyed by token hash
//	settings.json                       operator-editable controller settings (bootstrap)
//	signing-anchor.json                 the pinned bundle-signing PUBLIC key (TOFU; non-secret)
//
// Directories are created 0700 and files written 0600.
type filekv struct {
	root string
	mu   sync.Mutex
	// auditTails caches, per tenant, the tail of the append-only audit log (last Seq + Hash + count)
	// so appendAudit can chain and decide rotation without re-reading the whole file. Lazily
	// populated; kept coherent under mu (appendAudit/rotateAudit are the only writers).
	auditTails map[TenantID]*auditTail
}

var _ kvBackend = (*filekv)(nil)

// newFilekv returns a filekv rooted at base, creating it (0700) if absent and
// rejecting an unsafe pre-existing custody directory.
func newFilekv(root string) (*filekv, error) {
	root, err := ensureSecureStoreRoot(root)
	if err != nil {
		return nil, err
	}
	return &filekv{root: root, auditTails: make(map[TenantID]*auditTail)}, nil
}

// --- collection → path mapping ---------------------------------------------

// fileCollSpec maps a collection to its on-disk shape: a singleton file directly under the tenant dir,
// or a keyed subdir + filename suffix (the key is the sanitized filename stem).
type fileCollSpec struct {
	singleton     bool
	singletonFile string
	subdir        string
	suffix        string
}

// fileColls is the FROZEN collection → on-disk-layout mapping (backward-compat: existing fleet state
// lives at these exact paths).
var fileColls = map[string]fileCollSpec{
	collNodes:              {subdir: "nodes", suffix: ".json"},
	collStaged:             {subdir: "bundles", suffix: ".staged.json"},
	collCurrent:            {subdir: "bundles", suffix: ".current.json"},
	collTokens:             {subdir: "tokens", suffix: ".json"},
	collLoginChal:          {subdir: "login-challenges", suffix: ".json"},
	collAPITokens:          {subdir: "apitokens", suffix: ".json"},
	collOperators:          {subdir: "operators", suffix: ".json"},
	collSessions:           {subdir: "sessions", suffix: ".json"},
	collTopoHistory:        {subdir: "topology-history", suffix: ".json"},
	collTopology:           {singleton: true, singletonFile: "topology.json"},
	collOperatorCred:       {singleton: true, singletonFile: "operator_credential.json"},
	collKeystoneTransition: {singleton: true, singletonFile: "keystone-transition.json"},
	collStagedTL:           {singleton: true, singletonFile: "signed_trustlist.json"},
	collStagedTLHist:       {singleton: true, singletonFile: "trustlist-history.json"},
	collStagedSeal:         {singleton: true, singletonFile: "staged-set.json"},
	collServedTL:           {singleton: true, singletonFile: "served_trustlist.json"},
	collSettings:           {singleton: true, singletonFile: "settings.json"},
	collSigningAnchor:      {singleton: true, singletonFile: "signing-anchor.json"},
}

// pathFor resolves the on-disk path for (collection, key) under dir. A keyed collection sanitizes key
// as a single path component (traversal-safe); a singleton ignores key.
func pathFor(dir, coll, key string) (string, error) {
	spec, ok := fileColls[coll]
	if !ok {
		return "", fmt.Errorf("controller: unknown collection %q", coll)
	}
	if spec.singleton {
		return filepath.Join(dir, spec.singletonFile), nil
	}
	kc, err := sanitizeComponent(coll+" key", key)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, spec.subdir, kc+spec.suffix), nil
}

// --- kvBackend: locked scope + record CRUD ---------------------------------

// withLock runs fn while holding fs.mu (the store-wide lock the core composes atomic RMWs under).
func (fs *filekv) withLock(fn func() error) error {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	return fn()
}

// get reads collection/key (caller holds withLock), mapping a missing file to errKVNotFound.
func (fs *filekv) get(t TenantID, coll, key string) ([]byte, error) {
	dir, err := fs.tenantDir(t)
	if err != nil {
		return nil, err
	}
	p, err := pathFor(dir, coll, key)
	if err != nil {
		return nil, err
	}
	data, err := readStoreFile(p)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, errKVNotFound
		}
		return nil, err
	}
	return data, nil
}

// put durably writes collection/key (caller holds withLock) via temp-file + fsync + rename.
func (fs *filekv) put(t TenantID, coll, key string, val []byte) error {
	dir, err := fs.ensureTenantDir(t)
	if err != nil {
		return err
	}
	p, err := pathFor(dir, coll, key)
	if err != nil {
		return err
	}
	return writeBytesDurable(p, val)
}

// del removes collection/key (caller holds withLock); idempotent (absent == success).
func (fs *filekv) del(t TenantID, coll, key string) error {
	dir, err := fs.tenantDir(t)
	if err != nil {
		return err
	}
	p, err := pathFor(dir, coll, key)
	if err != nil {
		return err
	}
	// Deleting the staged-set seal is the crash-safety commit invalidation. The
	// rooted helper both rejects an unsafe final component and durably syncs the
	// directory entry removal, so a parent-path swap cannot redirect the unlink.
	if _, err := removeStoreFile(p); err != nil {
		return fmt.Errorf("controller: delete %s/%s: %w", coll, key, err)
	}
	return nil
}

// list reads every record in a keyed collection (caller holds withLock), stable-ordered by key. A
// missing collection dir is an empty list. Raw bytes are returned even for a corrupt-JSON file (the
// core decides skip-vs-fail per collection); a NotExist race is skipped, any other read error surfaces.
func (fs *filekv) list(t TenantID, coll string) ([]kvRecord, error) {
	spec, ok := fileColls[coll]
	if !ok || spec.singleton {
		return nil, fmt.Errorf("controller: list on non-keyed collection %q", coll)
	}
	dir, err := fs.tenantDir(t)
	if err != nil {
		return nil, err
	}
	d := filepath.Join(dir, spec.subdir)
	if err := validateSecureStoreDirIfExists(d); err != nil {
		return nil, err
	}
	ents, err := os.ReadDir(d)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("controller: list %s: %w", coll, err)
	}
	out := make([]kvRecord, 0, len(ents))
	for _, e := range ents {
		if e.IsDir() {
			continue
		}
		key, found := strings.CutSuffix(e.Name(), spec.suffix)
		if !found {
			continue // wrong suffix (a temp sidecar, or the sibling staged/current file)
		}
		data, rerr := readStoreFile(filepath.Join(d, e.Name()))
		if rerr != nil {
			if errors.Is(rerr, os.ErrNotExist) {
				continue // deleted between ReadDir and ReadFile
			}
			return nil, rerr
		}
		out = append(out, kvRecord{key: key, val: data})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].key < out[j].key })
	return out, nil
}

// exists is the no-deserialize heartbeat existence probe, called OUTSIDE withLock so a heartbeat
// never contends on fs.mu and a corrupt-but-present regular record still passes. It securely inspects
// the final component with Lstat rather than following os.Stat through a symlink or accepting a special
// file.
func (fs *filekv) exists(t TenantID, coll, key string) (bool, error) {
	dir, err := fs.tenantDir(t)
	if err != nil {
		return false, err
	}
	p, err := pathFor(dir, coll, key)
	if err != nil {
		return false, err
	}
	return storeFileExists(p)
}

// --- kvBackend: generation counter + wake ----------------------------------

// generationFileName is generation.json under the tenant dir.
const generationFileName = "generation.json"

// readGeneration returns the tenant's generation, defaulting to 0 when generation.json is absent.
func readGeneration(dir string) (int64, error) {
	var g generationFile
	if err := readJSON(filepath.Join(dir, generationFileName), &g); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return 0, nil
		}
		return 0, err
	}
	return g.Generation, nil
}

// generation reads the counter — caller holds withLock.
func (fs *filekv) generation(t TenantID) (int64, error) {
	dir, err := fs.tenantDir(t)
	if err != nil {
		return 0, err
	}
	return readGeneration(dir)
}

// setGeneration commits the counter — caller holds withLock. filekv has no in-process wake; the 200ms
// poller (awaitGenerationChange) re-stats generation.json.
func (fs *filekv) setGeneration(t TenantID, gen int64) error {
	dir, err := fs.ensureTenantDir(t)
	if err != nil {
		return err
	}
	return writeJSONAtomic(filepath.Join(dir, generationFileName), generationFile{Generation: gen})
}

// lockedGeneration reads the counter under fs.mu (used by the poll loop, which runs outside withLock).
func (fs *filekv) lockedGeneration(t TenantID) (int64, error) {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	dir, err := fs.tenantDir(t)
	if err != nil {
		return 0, err
	}
	return readGeneration(dir)
}

// awaitGenerationChange polls generation.json every 200ms until the counter exceeds afterGen (the disk
// store has no in-process condition variable to wake), or ctx is done. Self-managing (outside withLock).
func (fs *filekv) awaitGenerationChange(ctx context.Context, t TenantID, afterGen int64) (int64, error) {
	// Re-check the fast path (the core checked once, but a promote may have landed since).
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

// ================================ FileStore ================================

// FileStore is the JSON-on-disk Store: a storeCore over a filekv, with a durable, bounded
// resource-history flusher (Start/Close). It embeds *filekv so the white-box durability tests can reach
// the file primitives (ensureTenantDir / rotateAudit) directly.
type FileStore struct {
	*storeCore
	*filekv
}

// Compile-time assertion that *FileStore satisfies the Store interface.
var _ Store = (*FileStore)(nil)

// NewFileStore returns a FileStore rooted at base, creating it (0700) if absent.
func NewFileStore(root string) (*FileStore, error) {
	kv, err := newFilekv(root)
	if err != nil {
		return nil, err
	}
	var core *storeCore
	// The cap loader lets the flusher seed a tenant's cap from persisted settings on first flush (off
	// the heartbeat path), so an operator's cap=0 (disable history) survives a controller restart. It
	// deliberately performs a side-effect-free settings read: ensureSeeded atomically installs the
	// result only when no concurrent GetSettings/PutSettings has already installed newer truth. The
	// closure runs after core is assigned just below.
	historyRoot, err := ensureSecureStoreChild(kv.root, "telemetry-history")
	if err != nil {
		return nil, fmt.Errorf("controller: prepare telemetry history custody: %w", err)
	}
	hist := newTelemetryHistory(historyRoot, DefaultTelemetryHistoryCap,
		func(t TenantID) int {
			var cs ControllerSettings
			err := core.kv.withLock(func() error {
				return core.loadJSON(t, collSettings, "", &cs)
			})
			if err != nil {
				return DefaultTelemetryHistoryCap
			}
			return cs.EffectiveHistoryCap()
		})
	core = newStoreCore(kv, hist)
	return &FileStore{storeCore: core, filekv: kv}, nil
}

// Start launches the telemetry-history background flusher (plan-2). The server calls it once after
// construction; tests that exercise durable flush call it explicitly (and Close).
func (fs *FileStore) Start() { fs.storeCore.history.start() }

// Close stops the history flusher and does a final drain (graceful shutdown).
func (fs *FileStore) Close() { fs.storeCore.history.close() }
