package controller

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// FileStore is a JSON-on-disk Store implementation, durable for the single-tenant
// v1 deployment. It mirrors MemStore's semantics exactly (the shared compat test
// runs against both) while persisting every mutation to disk via temp-file +
// rename so a crash can never leave a half-written record. All in-process access
// is serialized by a sync.Mutex; cross-process durability is provided by the
// atomic renames, but FileStore does not arbitrate between separate processes
// sharing a root (a single controller process owns its root).
//
// On-disk layout under <root>/<tenant>/:
//
//	nodes/<nodeID>.json                 one Node record
//	topology.json                       the current TopologyRecord (with Version)
//	bundles/<nodeID>.staged.json        the node's staged SignedBundle (if any)
//	bundles/<nodeID>.current.json       the node's current SignedBundle (if any)
//	generation.json                     the tenant's current generation counter
//	audit.json                          the full []AuditEntry, in Seq order
//
// Directories are created 0700 and files written 0600. SignedBundle.Files
// (map[string][]byte) serializes as base64 under encoding/json, which round-trips
// the raw bytes faithfully.
type FileStore struct {
	root string
	mu   sync.Mutex
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
	return &FileStore{root: root}, nil
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
	for _, sub := range []string{"", "nodes", "bundles"} {
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

// --- atomic JSON IO ---------------------------------------------------------

// writeJSONAtomic marshals v and writes it to path via a temp-file + rename so a
// crash cannot leave a truncated file. The parent directory must already exist.
func writeJSONAtomic(path string, v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return fmt.Errorf("controller: marshal %s: %w", filepath.Base(path), err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0600); err != nil {
		return fmt.Errorf("controller: write %s: %w", filepath.Base(path), err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("controller: install %s: %w", filepath.Base(path), err)
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

// readAudit returns the tenant's audit entries (empty slice when audit.json is
// absent), in their stored Seq order.
func (fs *FileStore) readAudit(dir string) ([]AuditEntry, error) {
	var entries []AuditEntry
	err := readJSON(filepath.Join(dir, "audit.json"), &entries)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	return entries, nil
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
	return fs.listNodesLocked(dir)
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

// SetAppliedGeneration records what an agent reported applying.
func (fs *FileStore) SetAppliedGeneration(ctx context.Context, t TenantID, nodeID string, gen int64, checksum string) error {
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
	return writeJSONAtomic(p, n)
}

// TouchLastSeen records that the agent for nodeID checked in at the given time.
func (fs *FileStore) TouchLastSeen(ctx context.Context, t TenantID, nodeID string, at time.Time) error {
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
	n.LastSeen = at
	return writeJSONAtomic(p, n)
}

// =============================== Topology ==================================

// PutTopology stores a new topology version and returns the stored record with
// its assigned Version (1, 2, 3, …).
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

	rec := TopologyRecord{
		Version:   prevVersion + 1,
		JSON:      jsonBytes,
		UpdatedAt: time.Now().UTC(),
	}
	if err := writeJSONAtomic(p, rec); err != nil {
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

// PromoteStaged atomically flips all staged bundles to current, clears each
// promoted node's prior current bundle, increments the tenant generation by one,
// sets each promoted node's DesiredGeneration to the new generation, wakes
// WaitForGeneration waiters, and returns the new generation. With nothing staged
// it returns ErrNoStagedBundle and changes nothing.
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

	// Collect staged bundles in stable NodeID order for deterministic behavior.
	var staged []SignedBundle
	for _, e := range ents {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".staged.json") {
			continue
		}
		var b SignedBundle
		if err := readJSON(filepath.Join(bundlesDir, e.Name()), &b); err != nil {
			return 0, err
		}
		staged = append(staged, b)
	}
	if len(staged) == 0 {
		return 0, ErrNoStagedBundle
	}
	sort.Slice(staged, func(i, j int) bool { return staged[i].NodeID < staged[j].NodeID })

	cur, err := fs.readGeneration(dir)
	if err != nil {
		return 0, err
	}
	newGen := cur + 1

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

// ================================ Audit ====================================

// AppendAudit appends an entry, chaining its PrevHash/Hash to the tenant's prior
// entry and assigning a monotonic Seq. The caller-provided Timestamp is
// preserved. Returns the stored entry with Seq/PrevHash/Hash set.
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
	entries, err := fs.readAudit(dir)
	if err != nil {
		return AuditEntry{}, err
	}

	var seq int64 = 1
	prevHash := ""
	if n := len(entries); n > 0 {
		seq = entries[n-1].Seq + 1
		prevHash = entries[n-1].Hash
	}
	e.Seq = seq
	e = chainAudit(e, prevHash)

	entries = append(entries, e)
	if err := writeJSONAtomic(filepath.Join(dir, "audit.json"), entries); err != nil {
		return AuditEntry{}, err
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
